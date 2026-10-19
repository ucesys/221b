#!/usr/bin/env bash
# Runs the console with two replicas in a kind cluster and takes them away
# one at a time, first a follower and then the leader, checking what P7-02
# asks: deleting a replica costs viewers no more than a reconnect. Each
# time the session still works without signing in again, a new event
# stream connects, Sherlock's and Watson's requests reach the new leader
# through either replica, and a setting saved through one replica is read
# back through the other.
#
#   IMAGE=ghcr.io/ucesys/221b:dev hack/ha-kind.sh 1.37
#
# Needs docker, kubectl, helm and python3. Set KEEP=1 to keep the cluster.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

MINOR=${1:-1.37}
IMAGE=${IMAGE:?set IMAGE to the image to test, already in the local docker}
CLUSTER=${CLUSTER:-221b-ha}
NS=221b
RELEASE=k8s-console
FULLNAME=k8s-console-221b
LEASE=$FULLNAME-leader
BASE_URL=http://127.0.0.1:8080
E2E_USER=admin@corp.example
E2E_PASSWORD=e2e-password-12
COOKIES=$WORK/ha-cookies
# A port-forward per pod, to ask one replica in particular.
declare -A FORWARD=()

signin() {
  rm -f "$COOKIES"
  local form
  form=$(curl -fsS -c "$COOKIES" "$BASE_URL/signin" | grep -o 'name="form" value="[^"]*"' | cut -d'"' -f4)
  [ -n "$form" ]
  curl -fsS -b "$COOKIES" -c "$COOKIES" -o /dev/null -X POST --data-urlencode "email=$E2E_USER" --data-urlencode "password=$E2E_PASSWORD" --data-urlencode "next=/" --data-urlencode "form=$form" "$BASE_URL/signin"
  grep -q 221b_session "$COOKIES"
}
# A signed-in request with the one session: api [curl options] URL
api() { curl -fsS -b "$COOKIES" "$@"; }
# The HTTP status of a signed-in GET.
status() { curl -s -o /dev/null -w '%{http_code}' -b "$COOKIES" --max-time 5 "$1"; }

# The replicas that are Ready and not on their way out.
pods() {
  kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b -o json | python3 -c '
import json, sys
for p in json.load(sys.stdin)["items"]:
    ready = any(c["type"] == "Ready" and c["status"] == "True" for c in p["status"].get("conditions", []))
    if ready and not p["metadata"].get("deletionTimestamp"):
        print(p["metadata"]["name"])'
}
holder() { kubectl -n "$NS" get lease "$LEASE" -o jsonpath='{.spec.holderIdentity}' 2>/dev/null | cut -d@ -f1; }

NEXT_PORT=18100
# forward POD: sets PORT to a local port that reaches that pod only.
forward() {
  local pod=$1
  if [ -z "${FORWARD[$pod]:-}" ]; then
    NEXT_PORT=$((NEXT_PORT + 1))
    kubectl -n "$NS" port-forward "pod/$pod" "$NEXT_PORT:8080" > "$WORK/forward-$pod.log" 2>&1 &
    for _ in $(seq 30); do curl -fs "http://127.0.0.1:$NEXT_PORT/healthz" > /dev/null && break; sleep 0.5; done
    FORWARD[$pod]=$NEXT_PORT
  fi
  PORT=${FORWARD[$pod]}
}

# Waits until two replicas are Ready and one of them holds the Lease.
settled() {
  kubectl -n "$NS" rollout status "deploy/$FULLNAME" --timeout 120s
  local i
  for i in $(seq 60); do
    local ready
    ready=$(kubectl -n "$NS" get deploy "$FULLNAME" -o jsonpath='{.status.readyReplicas}')
    if [ "${ready:-0}" = 2 ] && pods | grep -qx "$(holder)"; then return 0; fi
    sleep 1
  done
  echo "the replicas did not settle: $(pods | tr '\n' ' ') lease $(holder)"
  return 1
}

# The checks after each loss.
check_both() {
  local pod port n
  # The same session works through every replica: no new sign-in.
  for pod in $(pods); do
    forward "$pod"
    port=$PORT
    [ "$(status "http://127.0.0.1:$port/api/v1/me")" = 200 ] || { echo "the session does not work through $pod"; return 1; }
    # Sherlock's and Watson's settings are the leader's: through a follower
    # they are forwarded to it.
    [ "$(status "http://127.0.0.1:$port/api/v1/watson/config")" = 200 ] || { echo "Watson does not answer through $pod"; return 1; }
    [ "$(status "http://127.0.0.1:$port/api/v1/sherlock/config")" = 200 ] || { echo "Sherlock does not answer through $pod"; return 1; }
    # The assistants' stream, forwarded too, sends Watson's status.
    curl -sN -b "$COOKIES" --max-time 4 "http://127.0.0.1:$port/api/v1/assistants/events" > "$WORK/assistants-$pod.txt" || true
    grep -q '^event: watson-status' "$WORK/assistants-$pod.txt" || { echo "no assistants' stream through $pod"; return 1; }
  done
  # A new event stream connects through the Service.
  curl -sN -b "$COOKIES" --max-time 4 "$BASE_URL/api/v1/events?views=nodes" > "$WORK/events.txt" || true
  grep -q '^event: snapshot' "$WORK/events.txt" || { echo "no event stream"; return 1; }
  # A setting saved through one replica is read back through the other.
  local a b
  a=$(pods | sed -n 1p)
  b=$(pods | sed -n 2p)
  n=$((RANDOM % 50 + 5))
  local pa pb i
  forward "$a"
  pa=$PORT
  forward "$b"
  pb=$PORT
  api -X PUT -H 'Content-Type: application/json' -d "{\"refreshSeconds\":$n}" "http://127.0.0.1:$pa/api/v1/settings" > /dev/null
  for i in $(seq 20); do
    api "http://127.0.0.1:$pb/api/v1/settings" | grep -q "\"refreshSeconds\":$n" && break
    sleep 0.5
  done
  api "http://127.0.0.1:$pb/api/v1/settings" | grep -q "\"refreshSeconds\":$n" ||
    { echo "a setting saved through $a did not reach $b"; return 1; }
  notice "Replicas" "A setting saved through $a was read through $b after $((i / 2)) s."
}

# lose POD WHAT: deletes it, polls the session through the Service while
# it goes, and says how many requests failed and how long the leader took.
lose() {
  local pod=$1 what=$2 before after fails=0 total=0 start
  before=$(holder)
  start=$(date +%s)
  kubectl -n "$NS" delete pod "$pod" --wait=false
  unset "FORWARD[$pod]"
  # A viewer's browser asking every half second while the pod goes.
  while [ $(( $(date +%s) - start )) -lt 20 ]; do
    total=$((total + 1))
    [ "$(status "$BASE_URL/api/v1/me")" = 200 ] || fails=$((fails + 1))
    sleep 0.5
  done
  settled
  after=$(holder)
  notice "Replicas" "Deleting the $what ($pod): $fails of $total requests failed while it went; the leader was $before and is $after."
  if [ "$what" = leader ] && [ "$after" = "$before" ]; then echo "the lease did not move"; return 1; fi
  if [ "$what" = follower ] && [ "$after" != "$before" ]; then echo "losing a follower moved the lease"; return 1; fi
  # Requests to a pod that is stopping may fail before the Service drops
  # it; a viewer reconnects, but most requests must still be answered.
  [ "$fails" -le $((total / 4)) ] || { echo "too many failed requests: $fails of $total"; return 1; }
}

install_kind "$MINOR"

cleanup() {
  local code=$?
  jobs -p | xargs -r kill 2>/dev/null || true
  if [ "$code" -ne 0 ]; then
    step "Diagnostics"
    {
      kubectl -n "$NS" get pods -o wide
      kubectl -n "$NS" get lease "$LEASE" -o yaml
      kubectl -n "$NS" describe pods
      kubectl get events -A --sort-by=.lastTimestamp | tail -40
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=200 --all-containers --prefix
    } > "$WORK/diagnostics.log" 2>&1 || true
    if [ -z "${GITHUB_ACTIONS:-}" ]; then cat "$WORK/diagnostics.log"; fi
    endstep
  fi
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  exit "$code"
}
trap cleanup EXIT

step "Create a kind cluster with Kubernetes $MINOR"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE[$MINOR]}" --config "$ROOT/hack/kind.yaml" --wait 120s
kind load docker-image "$IMAGE" --name "$CLUSTER"
endstep

step "helm install with two replicas"
kubectl create namespace "$NS"
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest
hash=$(printf '%s' "$E2E_PASSWORD" | docker run --rm -i "$IMAGE" hash-password)
helm install "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" \
  --set clusterName=kind-ha --set replicaCount=2 \
  --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
  --set service.type=NodePort --set service.nodePort=30080 \
  --set auth.mode=local \
  --set "auth.local.users[0].email=$E2E_USER" --set "auth.local.users[0].passwordHash=$hash" \
  --set 'auth.local.users[0].roles[0]=superAdmin' \
  --wait --timeout 120s
settled
kubectl -n "$NS" get pods -o wide
kubectl -n "$NS" get pdb,lease
echo "leader: $(holder)"
endstep

step "Sign in once, through the Service"
for _ in $(seq 30); do curl -fs "$BASE_URL/readyz" > /dev/null && break; sleep 1; done
signin
check_both
endstep

step "Lose a follower"
follower=$(pods | grep -vx "$(holder)" | head -1)
lose "$follower" follower
check_both
endstep

step "Lose the leader"
lose "$(holder)" leader
check_both
endstep

step "One sign-in all along"
# Every check above used the session from the first sign-in.
[ "$(status "$BASE_URL/api/v1/me")" = 200 ]
endstep
