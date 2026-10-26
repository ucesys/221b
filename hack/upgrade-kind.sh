#!/usr/bin/env bash
# Upgrades a release of 221B to this build in a kind cluster and checks that
# nothing kept in the cluster is lost (P7-03): the chart and image of an
# earlier release are installed, given a local user, settings, a fleet, an
# acknowledged alert, a cluster connection and a Sherlock key through its
# own API, then upgraded to this build, rolled back and upgraded again.
#
#   IMAGE=ghcr.io/ucesys/221b:ci hack/upgrade-kind.sh v0.6.6
#
# The release's image (ghcr.io/ucesys/221b:0.6.6 for v0.6.6) is pulled
# unless FROM_IMAGE names one already in the local docker; the release's
# chart is taken from its git tag. Needs docker, kubectl, helm and git with
# the tag fetched. Set KEEP=1 to keep the cluster afterwards.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

FROM_TAG=${1:?usage: hack/upgrade-kind.sh vX.Y.Z}
IMAGE=${IMAGE:?set IMAGE to the build to upgrade to, already in the local docker}
FROM_IMAGE=${FROM_IMAGE:-ghcr.io/ucesys/221b:${FROM_TAG#v}}
MINOR=${MINOR:-1.37}
CLUSTER=${CLUSTER:-221b-upgrade}
NS=221b
RELEASE=k8s-console
FULLNAME=k8s-console-221b
URL=http://127.0.0.1:18090
ADMIN=admin@corp.example
# Made on the Users page of the earlier release, so it lives only in the
# accounts Secret the console writes. A user name has no @: the console
# refuses one.
USER2=una
USER2_EMAIL=una@corp.example
PASSWORD=upgrade-password-12
COOKIES=$WORK/upgrade-cookies
SHERLOCK_KEY=sk-ant-upgrade-fake-key-0123456789

# signin URL USER: signs the shell in and keeps the session cookie.
signin() {
  rm -f "$COOKIES"
  local form
  form=$(curl -fsS -c "$COOKIES" "$1/signin" | grep -o 'name="form" value="[^"]*"' | cut -d'"' -f4)
  [ -n "$form" ]
  curl -fsS -b "$COOKIES" -c "$COOKIES" -o /dev/null -X POST --data-urlencode "email=$2" --data-urlencode "password=$PASSWORD" --data-urlencode "next=/" --data-urlencode "form=$form" "$1/signin"
  grep -q 221b_session "$COOKIES"
}
# A signed-in curl: api [curl options] URL
api() { curl -fsS -b "$COOKIES" "$@"; }
# A signed-in change, sent as the console's own pages send it (X-06).
change() { api -H 'Content-Type: application/json' -H 'Sec-Fetch-Site: same-origin' "$@"; }

pf=
# open_console: waits for the rollout and for its pods alone to answer, then
# forwards $URL to the Service.
open_console() {
  if [ -n "$pf" ]; then kill "$pf" 2>/dev/null || true; fi
  kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=120s
  local want
  want=$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.replicas}')
  for _ in $(seq 90); do
    [ "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b --no-headers 2>/dev/null | wc -l)" = "$want" ] && break
    sleep 1
  done
  kubectl -n "$NS" port-forward "svc/$FULLNAME" 18090:8081 > "$WORK/upgrade-port-forward.log" 2>&1 &
  pf=$!
  for _ in $(seq 30); do curl -fs "$URL/readyz" > /dev/null && break; sleep 1; done
  curl -fsS "$URL/readyz"
}

# What must come through every upgrade and rollback, as JSON with sorted
# keys: the refresh rate, the fleets and the acknowledged alerts.
kept_settings() {
  api "$URL/api/v1/settings" | python3 -c '
import json, sys
s = json.load(sys.stdin)
print(json.dumps({"refreshSeconds": s.get("refreshSeconds"), "fleets": s.get("fleets"), "acks": s.get("acks")}, sort_keys=True))'
}

# The format version of the JSON document on standard input, or 0.
stored_version() {
  python3 -c 'import json, sys; print(json.loads(sys.stdin.read() or "{}").get("version", 0))'
}

cleanup() {
  local code=$?
  if [ -n "$pf" ]; then kill "$pf" 2>/dev/null || true; fi
  if [ "$code" -ne 0 ]; then
    step "Diagnostics"
    {
      helm -n "$NS" history "$RELEASE"
      kubectl get pods -A -o wide
      kubectl -n "$NS" describe pods
      kubectl get events -A --sort-by=.lastTimestamp | tail -40
      kubectl -n "$NS" get configmap "$FULLNAME" -o yaml
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=200 --all-containers --prefix
    } > "$WORK/diagnostics.log" 2>&1 || true
    if [ -z "${GITHUB_ACTIONS:-}" ]; then cat "$WORK/diagnostics.log"; fi
    endstep
  fi
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  exit "$code"
}
trap cleanup EXIT

step "The chart and image of $FROM_TAG"
install_kind "$MINOR"
FROM_CHART=$WORK/from-chart
rm -rf "$FROM_CHART"
mkdir -p "$FROM_CHART"
git -C "$ROOT" archive "$FROM_TAG" deploy/helm/221b | tar -x -C "$FROM_CHART"
FROM_CHART=$FROM_CHART/deploy/helm/221b
grep -E '^(version|appVersion):' "$FROM_CHART/Chart.yaml"
docker image inspect "$FROM_IMAGE" > /dev/null 2>&1 || docker pull -q "$FROM_IMAGE"
echo "from $FROM_IMAGE to $IMAGE"
endstep

step "Create a kind cluster with Kubernetes $MINOR"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE[$MINOR]}" --config "$ROOT/hack/kind.yaml" --wait 120s
kind load docker-image "$FROM_IMAGE" --name "$CLUSTER"
kind load docker-image "$IMAGE" --name "$CLUSTER"
endstep

step "helm install $FROM_TAG"
kubectl create namespace "$NS"
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest
hash=$(printf '%s' "$PASSWORD" | docker run --rm -i "$FROM_IMAGE" hash-password)
# The earlier chart's appVersion is not its release's version, so the
# image is named outright; the same values serve every release since.
helm install "$RELEASE" "$FROM_CHART" -n "$NS" \
  --set clusterName=kind-upgrade \
  --set image.repository="${FROM_IMAGE%:*}" --set image.tag="${FROM_IMAGE##*:}" --set image.pullPolicy=Never \
  --set auth.mode=local \
  --set "auth.local.users[0].email=$ADMIN" --set "auth.local.users[0].name=Ada Admin" --set "auth.local.users[0].passwordHash=$hash" \
  --set 'auth.local.users[0].roles[0]=superAdmin' \
  --set "auth.admins[0]=$ADMIN" \
  --wait --timeout 120s
open_console
signin "$URL" "$ADMIN"
api "$URL/api/v1/me" | grep -q '"admin":true'
endstep

step "Give $FROM_TAG something to keep"
# A local user made on the Users page.
change -X POST -d "{\"username\":\"$USER2\",\"name\":\"Una Upgrade\",\"email\":\"$USER2_EMAIL\",\"password\":\"$PASSWORD\",\"groups\":[]}" "$URL/api/v1/admin/users" > /dev/null
kubectl -n "$NS" get secret "$FULLNAME-users" -o jsonpath='{.data.users\.json}' | base64 -d | grep -q "\"$USER2\""
# Settings and a fleet.
change -X PUT -d '{"refreshSeconds":13}' "$URL/api/v1/settings" | grep -q '"refreshSeconds":13'
change -X PUT -d '{"fleets":[{"id":"upgrade","name":"Upgrade pool","color":2,"labels":["upgrade-test=yes"],"taints":[]}]}' "$URL/api/v1/fleets" > /dev/null
# An acknowledged alert: a pod that fails to start, in a namespace of its own.
kubectl create namespace upgrade-test
kubectl -n upgrade-test run failing --image="$FROM_IMAGE" --image-pull-policy=Never --restart=Always -- -no-such-flag
key=pod:upgrade-test/failing
for _ in $(seq 90); do
  api "$URL/api/v1/snapshot?views=alerts" | python3 -c '
import json, sys
rows = json.load(sys.stdin)["views"]["alerts"]["rows"]
sys.exit(0 if any(r["key"] == sys.argv[1] and r["severity"] != "recovered" for r in rows) else 1)' "$key" && break
  sleep 2
done
# A crash-looping pod's alert flickers between failing and recovered, and
# the release refuses (409) to acknowledge one that has just cleared: try
# again for a minute.
for i in $(seq 30); do
  change -X POST -d "{\"key\":\"$key\",\"acknowledge\":true}" "$URL/api/v1/alerts/ack" > /dev/null && break
  [ "$i" -lt 30 ] || { echo "$FROM_TAG would not acknowledge $key"; exit 1; }
  sleep 2
done
# A connection to the cluster itself, kept in a Secret the chart does not own.
TOKEN=$(kubectl -n "$NS" create token "$FULLNAME" --duration=2h)
python3 - "$TOKEN" "$(kubectl -n "$NS" get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}')" > "$WORK/upgrade-draft.json" <<'PY'
import json, sys
print(json.dumps({"name": "kept", "env": "staging", "description": "kind, through its in-cluster address", "server": "https://kubernetes.default.svc", "ca": "pem", "caPem": sys.argv[2], "auth": "token", "token": sys.argv[1], "mode": "read"}))
PY
change -X POST --data-binary "@$WORK/upgrade-draft.json" "$URL/api/v1/clusters" | grep -q '"id":"kept"'
# Sherlock's key, kept in the release's Secret.
change -X PUT -d "{\"key\":\"$SHERLOCK_KEY\"}" "$URL/api/v1/sherlock/key" > /dev/null
before=$(kept_settings)
echo "kept: $before"
echo "$before" | grep -q '"refreshSeconds": 13'
echo "$before" | grep -q '"upgrade"'
echo "$before" | grep -q "$key"
endstep

# check_kept WHAT: everything given above is still there.
check_kept() {
  signin "$URL" "$ADMIN"
  local now
  now=$(kept_settings)
  if [ "$now" != "$before" ]; then
    echo "The settings changed $1:"
    echo "before: $before"
    echo "after:  $now"
    exit 1
  fi
  api "$URL/api/v1/admin/users" | grep -q "\"$USER2\""
  for _ in $(seq 60); do
    api "$URL/api/v1/clusters/kept" | grep -q '"state":"connected"' && break
    sleep 1
  done
  api "$URL/api/v1/clusters/kept" | tee "$WORK/upgrade-kept.json" | grep -q '"state":"connected"' ||
    { echo "the connection did not come back $1: $(cat "$WORK/upgrade-kept.json")"; exit 1; }
  # Sherlock answers once the new pod holds the Lease (P7-02).
  for _ in $(seq 30); do
    api "$URL/api/v1/sherlock/config" > "$WORK/upgrade-sherlock.json" 2>/dev/null && break
    sleep 1
  done
  grep -q '"set":true' "$WORK/upgrade-sherlock.json"
  [ "$(kubectl -n "$NS" get secret "$FULLNAME-sherlock" -o jsonpath='{.data.apiKey}' | base64 -d)" = "$SHERLOCK_KEY" ]
  # The user made on the Users page signs in with the same password.
  signin "$URL" "$USER2"
  api "$URL/api/v1/me" | grep -q "\"username\":\"$USER2\""
  signin "$URL" "$ADMIN"
  echo "the settings, fleet, acknowledgement, user, connection and Sherlock key came through $1"
}

# upgrade: the earlier release to this build, reusing its values.
upgrade() {
  helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values \
    --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
    --wait --timeout 180s
  open_console
}

step "helm upgrade from $FROM_TAG to this build"
start=$(date +%s)
upgrade
notice "Upgrade from $FROM_TAG" "Ready $(( $(date +%s) - start ))s after helm upgrade."
check_kept "the upgrade"
endstep

step "Saved again, the settings and accounts carry their format's version"
change -X PUT -d '{"refreshSeconds":13,"timeFormat":{"date":"eu","clock":"24h"}}' "$URL/api/v1/settings" > /dev/null
v=$(kubectl -n "$NS" get configmap "$FULLNAME" -o jsonpath='{.data.settings\.json}' | stored_version)
echo "settings.json: version $v"
[ "$v" -ge 1 ]
change -X PATCH -d '{"name":"Una Upgraded"}' "$URL/api/v1/admin/users/$USER2" > /dev/null
v=$(kubectl -n "$NS" get secret "$FULLNAME-users" -o jsonpath='{.data.users\.json}' | base64 -d | stored_version)
echo "users.json: version $v"
[ "$v" -ge 1 ]
endstep

step "helm rollback to $FROM_TAG: it reads what this build saved"
helm rollback "$RELEASE" 1 -n "$NS" --wait --timeout 180s
open_console
[ "$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$FROM_IMAGE" ]
check_kept "the rollback"
api "$URL/api/v1/admin/users" | grep -q 'Una Upgraded'
endstep

step "helm upgrade again: what the earlier release did not know is still there"
upgrade
check_kept "the second upgrade"
api "$URL/api/v1/settings" | python3 -c '
import json, sys
s = json.load(sys.stdin)
assert s.get("timeFormat") == {"date": "eu", "clock": "24h"}, s.get("timeFormat")
print("the time format, unknown to the earlier release, came through its rollback")'
api "$URL/api/v1/admin/users" | grep -q 'Una Upgraded'
notice "Upgrade from $FROM_TAG" "Upgrade, rollback and upgrade again kept every setting, user, connection and key."
endstep
