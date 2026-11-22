#!/usr/bin/env bash
# Installs the chart in a kind cluster, with its bundled database (one
# member, the default; DB-69), and checks it the way the acceptance
# criteria ask: Ready (the console within 60 seconds of its database),
# the console's records in its database at the schema's last step,
# read-only permissions, the browser tests, then upgrade, turn Secrets
# off, and uninstall leaving nothing behind but what is kept on purpose.
#
#   IMAGE=ghcr.io/ucesys/221b:dev DB_IMAGE=ghcr.io/ucesys/221b-postgres:dev hack/e2e-kind.sh 1.37
#
# IMAGE and DB_IMAGE are taken from the local docker, and built there when
# they are not (hack/lib.sh, build_images). CLUSTER names the kind cluster
# (221b-e2e), HOST_PORT the console's port on the host (8080). Needs
# docker, kubectl, helm, Go (for the fake Claude API) and, for the browser
# tests, the web/ packages and Playwright's Chromium. E2E_ARGS goes to
# Playwright, such as some spec files (all of them by default). Set KEEP=1
# to keep the cluster afterwards.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

MINOR=${1:-1.37}
CLUSTER=${CLUSTER:-221b-e2e}
NS=221b
RELEASE=k8s-console
FULLNAME=k8s-console-221b
DBNAME=$FULLNAME-db
SA=system:serviceaccount:$NS:$FULLNAME
BASE_URL=http://127.0.0.1:$HOST_PORT
# Ports on the host: the port-forward to the console's Service, and the
# console run from a kubeconfig, outside the cluster, with a PostgreSQL of
# its own.
PF_PORT=${PF_PORT:-18080}
KUBECONFIG_PORT=${KUBECONFIG_PORT:-18081}
KUBECONFIG_DB_PORT=${KUBECONFIG_DB_PORT:-18082}
# The console is installed with local accounts (P3-01): an administrator
# and a viewer, both with the password "e2e-password-12", which the browser tests
# use too (web/e2e/accounts.ts).
E2E_USER=admin@corp.example
E2E_VIEWER=viewer@corp.example
E2E_DEV=dev@corp.example
# A local user is known to Kubernetes with the prefix of its source (RB-15).
E2E_DEV_KUBE=221b-local:$E2E_DEV
E2E_VIEWER_KUBE=221b-local:$E2E_VIEWER
E2E_PASSWORD=e2e-password-12
COOKIES=$WORK/cookies
# Signs the shell in to a console at $1 as the administrator and keeps the
# session cookie for api().
signin() {
  rm -f "$COOKIES"
  # The form carries a token the page's cookie must match (X-06).
  local form
  form=$(curl -fsS -c "$COOKIES" "$1/signin" | grep -o 'name="form" value="[^"]*"' | cut -d'"' -f4)
  [ -n "$form" ]
  curl -fsS -b "$COOKIES" -c "$COOKIES" -o /dev/null -X POST --data-urlencode "email=$E2E_USER" --data-urlencode "password=$E2E_PASSWORD" --data-urlencode "next=/" --data-urlencode "form=$form" "$1/signin"
  grep -q 221b_session "$COOKIES"
}
# A signed-in curl: api [curl options] URL
api() { curl -fsS -b "$COOKIES" "$@"; }

install_kind "$MINOR"
build_images
db_values

# Sherlock's tests (web/e2e/sherlock.spec.ts) talk to the simulator's fake
# Claude API, run here and reached from the cluster through the kind
# network's gateway.
step "Start the fake Claude API"
(cd "$ROOT/server" && go build -o ../bin/221b-sim ./cmd/221b-sim)
"$ROOT/bin/221b-sim" -claude-only -listen 0.0.0.0:7445 > "$WORK/fake-claude.log" 2>&1 &
fake_claude=$!
endstep

cleanup() {
  local code=$?
  kill "$fake_claude" 2>/dev/null || true
  docker rm -f 221b-kubeconfig 221b-kubeconfig-db > /dev/null 2>&1 || true
  if [ "$code" -ne 0 ]; then
    step "Diagnostics"
    # The console's own log comes last: CI reports the file's tail.
    {
      kubectl get nodes -o wide
      kubectl get pods -A -o wide
      kubectl -n "$NS" describe pods
      kubectl get events -A --sort-by=.lastTimestamp | tail -40
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b-db --tail=100 --all-containers --prefix
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=200 --all-containers
    } > "$WORK/diagnostics.log" 2>&1 || true
    # CI reports the file on its own, so the log's tail shows the failure.
    if [ -z "${GITHUB_ACTIONS:-}" ]; then cat "$WORK/diagnostics.log"; fi
    endstep
  fi
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  exit "$code"
}
trap cleanup EXIT
# Which check failed, since many have no message of their own.
trap 'echo "failed at hack/e2e-kind.sh:$LINENO: $BASH_COMMAND" >&2' ERR

step "Create a kind cluster with Kubernetes $MINOR"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE[$MINOR]}" --config "$(kind_config)" --wait 120s
load_images "$CLUSTER"
kubectl version
GATEWAY=$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' | tr ' ' '\n' | grep -E '^[0-9]+([.][0-9]+){3}$' | head -1)
echo "the fake Claude API is at http://$GATEWAY:7445/claude for the cluster"
docker exec "$CLUSTER-control-plane" curl -fsS -o /dev/null "http://$GATEWAY:7445/claude/requests"
endstep

step "Install metrics-server"
install_metrics_server
METRICS_MANIFEST=$(metrics_server_manifest)
endstep

step "helm install, with the bundled database: Ready within 60 seconds of the database"
# The namespace enforces the Pod Security restricted profile (X-05).
kubectl create namespace "$NS"
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest
hash=$(printf '%s' "$E2E_PASSWORD" | docker run --rm -i "$IMAGE" hash-password)
echo "password hash: ${hash:0:24}..."
start=$(date +%s)
helm install "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" \
  --set clusterName=kind-e2e \
  --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
  --set service.type=NodePort --set service.nodePort=30080 \
  --set auth.mode=local \
  --set "auth.local.users[0].email=$E2E_USER" --set "auth.local.users[0].name=Ada Admin" --set "auth.local.users[0].passwordHash=$hash" \
  --set 'auth.local.users[0].roles[0]=superAdmin' \
  --set "auth.local.users[1].email=$E2E_VIEWER" --set "auth.local.users[1].name=Vera Viewer" --set "auth.local.users[1].passwordHash=$hash" \
  --set 'auth.local.users[1].roles[0]=superViewer' \
  --set "auth.local.users[2].email=$E2E_DEV" --set "auth.local.users[2].name=Dev Developer" --set "auth.local.users[2].passwordHash=$hash" \
  --set "auth.admins[0]=$E2E_USER" \
  --set auth.kubernetes.accessTTL=10s \
  --set localCluster.mode=write --set toolbox.ssh.enabled=true \
  --set sherlock.apiURL="http://$GATEWAY:7445/claude" --set 'networkPolicy.outboundPorts={443,7445}' \
  "${DB_VALUES[@]}" \
  --wait --timeout "$HELM_TIMEOUT"
# helm waits for both. The database's member is Ready once Patroni has made
# it the primary; the console's pod starts with it, waits for it, makes the
# schema and only then listens.
ready() { kubectl -n "$NS" get pods -l "app.kubernetes.io/name=$1" -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].lastTransitionTime}'; }
db_ready=$(date -d "$(ready 221b-db)" +%s)
console_ready=$(date -d "$(ready 221b)" +%s)
notice "helm install" "On Kubernetes $MINOR the database was Ready $(( db_ready - start ))s after helm install, and the console $(( console_ready - db_ready ))s after the database."
[ $(( console_ready - db_ready )) -le 60 ] || { echo "the console was Ready $(( console_ready - db_ready ))s after its database"; exit 1; }
kubectl -n "$NS" get all,networkpolicy,serviceaccount,pvc
# The agents on their own read as the user 221b:agents (JB-08): an audit
# that can read nothing says so and stops (JB-23), so the cluster lets that
# user read, as an administrator binds a role to it (values.yaml, jobs).
kubectl create clusterrolebinding e2e-agents --clusterrole=view --user=221b:agents
# What each person may see is what Kubernetes lets them (P3-02): the
# administrator everything (SuperAdmin, 221b-super-admin), the viewer
# everything but Secrets (SuperViewer, 221b-super-viewer), and the developer
# one namespace at a time (web/e2e/access.spec.ts binds it, to the name the
# clusters know a local user by).
endstep

step "Permissions"
"$ROOT/hack/check-rbac.sh" "$SA"
endstep

step "Run from a kubeconfig too: both modes show the same nodes (P1-08)"
kind get kubeconfig --name "$CLUSTER" > "$WORK/kubeconfig"
chmod 644 "$WORK/kubeconfig"
# Outside the cluster the console needs a PostgreSQL too (DB-69): a
# throwaway one of the image CI pins.
docker run -d --rm --name 221b-kubeconfig-db -e POSTGRES_HOST_AUTH_METHOD=trust -p "127.0.0.1:$KUBECONFIG_DB_PORT:5432" \
  "$(sed -n 's/^POSTGRES_IMAGE=//p' "$ROOT/hack/base-images.env")" > /dev/null
for _ in $(seq 60); do docker exec 221b-kubeconfig-db pg_isready -q -h 127.0.0.1 && break; sleep 1; done
docker run -d --rm --name 221b-kubeconfig --network host -v "$WORK/kubeconfig:/kubeconfig:ro" "$IMAGE" \
  -kubeconfig /kubeconfig -cluster-name laptop -listen "127.0.0.1:$KUBECONFIG_PORT" \
  -release-namespace default -release-fullname laptop \
  -db-host 127.0.0.1 -db-port "$KUBECONFIG_DB_PORT" -db-user postgres -db-name postgres -db-sslmode disable > /dev/null
for _ in $(seq 120); do curl -fs "http://127.0.0.1:$KUBECONFIG_PORT/readyz" >/dev/null && break; sleep 1; done
names() { curl -fsS -b "$COOKIES" "$1/api/v1/snapshot?views=nodes" | python3 -c 'import json, sys; print(" ".join(sorted(r["name"] for r in json.load(sys.stdin)["views"]["nodes"]["rows"])))'; }
# The kubeconfig instance runs without sign-in; the in-cluster one needs a session.
rm -f "$COOKIES"; touch "$COOKIES"
laptop=$(names "http://127.0.0.1:$KUBECONFIG_PORT")
signin "$BASE_URL"
incluster=$(names "$BASE_URL")
docker rm -f 221b-kubeconfig 221b-kubeconfig-db > /dev/null
echo "kubeconfig: $laptop"
echo "in-cluster: $incluster"
[ -n "$laptop" ] && [ "$laptop" = "$incluster" ]
endstep

step "Open it through kubectl port-forward: the sign-in page, then the app (P3-01)"
kubectl -n "$NS" port-forward "svc/$FULLNAME" "$PF_PORT:8081" > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs "http://127.0.0.1:$PF_PORT/readyz" >/dev/null && break; sleep 1; done
curl -fsS "http://127.0.0.1:$PF_PORT/readyz"
# Without a session: the sign-in page, and 401 from the API.
curl -fsS "http://127.0.0.1:$PF_PORT/" | has 'data-testid="signin-form"'
[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/snapshot")" = 401 ]
# A wrong password is refused and recorded; the right one gets the app.
# A form posted from elsewhere, without the page's token, is refused; the
# page's own form with a wrong password is 401.
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST --data-urlencode "email=$E2E_USER" --data-urlencode "password=$E2E_PASSWORD" "http://127.0.0.1:$PF_PORT/signin")" = 403 ]
form=$(curl -fsS -c "$WORK/form-cookies" "http://127.0.0.1:$PF_PORT/signin" | grep -o 'name="form" value="[^"]*"' | cut -d'"' -f4)
[ "$(curl -s -b "$WORK/form-cookies" -o /dev/null -w '%{http_code}' -X POST --data-urlencode "email=$E2E_USER" --data-urlencode "password=wrong" --data-urlencode "form=$form" "http://127.0.0.1:$PF_PORT/signin")" = 401 ]
signin "http://127.0.0.1:$PF_PORT"
api "http://127.0.0.1:$PF_PORT/" | has '<div id="root">'
api "http://127.0.0.1:$PF_PORT/api/v1/me" | tee "$WORK/me.json" | has '"admin":true'
grep -q "\"username\":\"$E2E_USER\"" "$WORK/me.json"
# The audit log names the person, on standard output (P3-10).
kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=-1 | grep '"action"' | tee "$WORK/audit.log"
grep -q "\"action\":\"signin\".*\"email\":\"$E2E_USER\"" "$WORK/audit.log"
grep -q '"action":"signin.failed"' "$WORK/audit.log"
endstep

step "The console keeps its records in its database, and uptime comes from the kubelets"
# The database answers and its schema is at this build's last step (DB-15).
check_database "http://127.0.0.1:$PF_PORT" api
api "http://127.0.0.1:$PF_PORT/api/v1/settings" | tee "$WORK/settings.json"
grep -q '"refreshSeconds":3' "$WORK/settings.json"
grep -q '"storage":"database"' "$WORK/settings.json"
api -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":7}' "http://127.0.0.1:$PF_PORT/api/v1/settings" | has '"refreshSeconds":7'
# Saved as a row of the database, and nowhere in a ConfigMap (DB-69).
db_sql "$NS" "$DBNAME-0" "select scope from settings where value::text ~ '\"refreshSeconds\": ?7[,}]'" | tee "$WORK/settings-row.txt"
[ -s "$WORK/settings-row.txt" ] || { echo "no settings row holds refreshSeconds 7"; exit 1; }
if kubectl -n "$NS" get configmap -o json | has '\\"refreshSeconds\\":7'; then echo "a ConfigMap holds the setting saved"; exit 1; fi
api -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":3}' "http://127.0.0.1:$PF_PORT/api/v1/settings" > /dev/null
# Every kind node has a real kubelet, so every row carries a boot time.
for _ in $(seq 60); do
  nodes=$(api "http://127.0.0.1:$PF_PORT/api/v1/snapshot?views=nodes" | python3 -c 'import json, sys; rows = json.load(sys.stdin)["views"]["nodes"]["rows"]; print(sum(1 for r in rows if r.get("bootedAt")), len(rows))')
  [ "${nodes% *}" = "${nodes#* }" ] && break
  sleep 2
done
echo "nodes with a boot time: $nodes"
[ "${nodes% *}" = "${nodes#* }" ]
api "http://127.0.0.1:$PF_PORT/api/v1/snapshot" | has '"uptime":"available"'
# Secrets are read (the default): the console's own Helm release shows,
# with its values, and the record's Secret is listed by key name and size.
api "http://127.0.0.1:$PF_PORT/api/v1/snapshot" | has '"secrets":"read"'
api "http://127.0.0.1:$PF_PORT/api/v1/helm/$NS/$RELEASE" | tee "$WORK/release.json" | has '"name":"k8s-console"'
grep -q 'clusterName: kind-e2e' "$WORK/release.json"
api "http://127.0.0.1:$PF_PORT/api/v1/snapshot?views=ns:$NS" | has '"kind":"Secret"'
kill "$pf"
endstep

step "Browser tests"
read -r -a e2e_args <<< "${E2E_ARGS:-}"
(
  cd "$ROOT/web"
  E2E_MODE=kind E2E_BASE_URL=$BASE_URL E2E_NAMESPACE=$NS E2E_IMAGE=$IMAGE \
    E2E_CONTROL_PLANE=$CLUSTER-control-plane E2E_METRICS_MANIFEST=$METRICS_MANIFEST \
    E2E_USER=$E2E_USER E2E_VIEWER=$E2E_VIEWER E2E_DEV=$E2E_DEV E2E_DEV_KUBE=$E2E_DEV_KUBE E2E_VIEWER_KUBE=$E2E_VIEWER_KUBE E2E_PASSWORD=$E2E_PASSWORD \
    E2E_CLAUDE_URL=http://127.0.0.1:7445/claude \
    npx playwright test "${e2e_args[@]}" 2>&1 | tee "$WORK/playwright.log"
)
endstep

step "a cluster connection is kept in a Secret the chart does not own (P3-07, P3-09)"
# The kind cluster itself, added a second time through its in-cluster
# address with a token of the console's own ServiceAccount and the CA
# every pod gets; the connection must survive the upgrade below.
kubectl -n "$NS" port-forward "svc/$FULLNAME" "$PF_PORT:8081" > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs "http://127.0.0.1:$PF_PORT/readyz" >/dev/null && break; sleep 1; done
signin "http://127.0.0.1:$PF_PORT"
TOKEN=$(kubectl -n "$NS" create token "$FULLNAME" --duration=2h)
python3 - "$TOKEN" "$(kubectl -n "$NS" get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}')" > "$WORK/draft.json" <<'PY'
import json, sys
print(json.dumps({"name": "kept", "env": "staging", "description": "kind, through its in-cluster address", "server": "https://kubernetes.default.svc", "ca": "pem", "caPem": sys.argv[2], "auth": "token", "token": sys.argv[1], "mode": "read"}))
PY
api -X POST -H 'Content-Type: application/json' -H 'Sec-Fetch-Site: same-origin' --data-binary "@$WORK/draft.json" "http://127.0.0.1:$PF_PORT/api/v1/clusters" | tee "$WORK/kept.json" | has '"id":"kept"'
if grep -q "$TOKEN" "$WORK/kept.json"; then echo "the API answered with the token"; exit 1; fi
kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" -o jsonpath='{.data}' | has '"token"'
echo "the connection's Secret holds the token; the API answered without it"
# Connected, then synced a moment later, once the informers have listed.
for _ in $(seq 60); do
  api "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept" | tee "$WORK/kept-state.json" | has '"synced":true' && break
  sleep 1
done
grep -q '"synced":true' "$WORK/kept-state.json" || { echo "the connection did not come up: $(cat "$WORK/kept-state.json")"; exit 1; }
api "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept/snapshot?views=nodes" | has "\"$CLUSTER-control-plane\""
echo "the connection serves the cluster's nodes under its own route"
# Its test passes every step with the CA and token given.
api -X POST -H 'Sec-Fetch-Site: same-origin' "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept/test" | tee "$WORK/kept-test.json" | python3 -c '
import json, sys
r = json.load(sys.stdin)
assert r["tone"] in ("ok", "warn"), r
for s in r["steps"]:
    assert s["tone"] in ("ok", "warn"), s
assert isinstance(r["impersonate"], bool), r
print("test:", r["summary"])'
# The audit log names the administrator and the cluster, never the token.
kubectl -n "$NS" logs "deploy/$FULLNAME" > "$WORK/audit-connections.log"
grep -q "\"action\":\"cluster.add\".*\"email\":\"$E2E_USER\".*\"cluster\":\"kept\"" "$WORK/audit-connections.log" ||
  grep -q "\"action\":\"cluster.add\"" "$WORK/audit-connections.log"
if grep -q "$TOKEN" "$WORK/audit-connections.log"; then echo "the audit log holds the token"; exit 1; fi
kill "$pf"
endstep

step "helm upgrade keeps the settings, in the database"
# A setting saved before the upgrade must survive it: the database's
# volume and its Secret outlive every upgrade.
kubectl -n "$NS" port-forward "svc/$FULLNAME" "$PF_PORT:8081" > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs "http://127.0.0.1:$PF_PORT/readyz" >/dev/null && break; sleep 1; done
api -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":11}' "http://127.0.0.1:$PF_PORT/api/v1/settings" | has '"refreshSeconds":11'
kill "$pf"
dbpod=$(kubectl -n "$NS" get pod "$DBNAME-0" -o jsonpath='{.metadata.uid}')
helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values --set logLevel=debug --wait --timeout "$HELM_TIMEOUT"
kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=120s
[ "$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="LOG_LEVEL")].value}')" = debug ]
# The console's values changed, the database's did not: its member was
# left running.
[ "$(kubectl -n "$NS" get pod "$DBNAME-0" -o jsonpath='{.metadata.uid}')" = "$dbpod" ] || { echo "the upgrade restarted the database"; exit 1; }
# Through a port-forward: kube-proxy may still be catching up after the
# browser tests restarted the API server, which would delay the NodePort.
for _ in $(seq 60); do
  [ "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b -o name | wc -l)" = 1 ] && break
  sleep 1
done
kubectl -n "$NS" port-forward "svc/$FULLNAME" "$PF_PORT:8081" > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs "http://127.0.0.1:$PF_PORT/readyz" >/dev/null && break; sleep 1; done
curl -fsS "http://127.0.0.1:$PF_PORT/readyz"
# Sessions are kept in the cluster (P7-02), so the restart signed nobody
# out: the session from before the upgrade still works.
# Until the new pod knows which replica leads, it answers 503 (ADR-70).
for _ in $(seq 30); do
  [ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/settings")" = 200 ] && break
  sleep 1
done
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/settings")" = 200 ]
api "http://127.0.0.1:$PF_PORT/api/v1/settings" | has '"refreshSeconds":11'
check_database "http://127.0.0.1:$PF_PORT" api
# The connection came back, its row from the database and its credential
# from its Secret, and connects again.
for _ in $(seq 60); do
  api "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept" | has '"state":"connected"' && break
  sleep 1
done
api "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept" | has '"description":"kind, through its in-cluster address"'
echo "the connection survived the upgrade"
# Removing it deletes its Secret (P3-08).
api -X DELETE -H 'Sec-Fetch-Site: same-origin' "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept" | has '"removed":true'
if kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" > /dev/null 2>&1; then echo "the connection's Secret is still there"; exit 1; fi
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/clusters/kept")" = 404 ]
echo "the connection and its Secret are gone"
# Sherlock's API key, saved by the browser tests, is kept in the release's
# Secret, survived the upgrade and is never served (P5-05, X-04); removing
# it in Settings deletes the Secret.
if [ "$(kubectl -n "$NS" get secret "$FULLNAME-sherlock" -o jsonpath='{.data.apiKey}' | base64 -d)" != sk-ant-e2e-fake-key-0123456789 ]; then
  # Who wrote the Secret last, and every key change before the upgrade.
  echo "Sherlock's key is not in its Secret after the upgrade:"
  kubectl -n "$NS" get secret "$FULLNAME-sherlock" -o json --show-managed-fields | python3 -c '
import json, sys
s = json.load(sys.stdin)
print("keys:", sorted(s.get("data", {})))
print(json.dumps(s["metadata"], indent=1))' || true
  grep '"action":"sherlock.key' "$WORK/audit-connections.log" || echo "no sherlock.key records before the upgrade"
  exit 1
fi
# Sherlock runs on the replica that holds the Lease (P7-02), which the new
# pod takes when the old one lets it go, a moment after the rollout.
for _ in $(seq 30); do
  code=$(curl -s -b "$COOKIES" -o "$WORK/sherlock-config.json" -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/sherlock/config" || true)
  [ "$code" = 200 ] && break
  sleep 1
done
grep -q '"set":true' "$WORK/sherlock-config.json" ||
  { echo "Sherlock's config after the upgrade, HTTP $code: $(cat "$WORK/sherlock-config.json")"; exit 1; }
if grep -q sk-ant-e2e-fake-key "$WORK/sherlock-config.json"; then echo "Sherlock's key was served"; exit 1; fi
api -X DELETE -H 'Sec-Fetch-Site: same-origin' "http://127.0.0.1:$PF_PORT/api/v1/sherlock/key" | has '"set":false'
if kubectl -n "$NS" get secret "$FULLNAME-sherlock" > /dev/null 2>&1; then echo "Sherlock's Secret is still there"; exit 1; fi
echo "the Sherlock key survived the upgrade, was never served, and its Secret is gone with it"
kill "$pf"
endstep

step "secrets.read=false grants nothing on Secrets outside its namespace, lists none and hides the Helm tab (P2-10)"
helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values --set secrets.read=false --wait --timeout "$HELM_TIMEOUT"
kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=120s
"$ROOT/hack/check-rbac.sh" --no-secrets "$SA"
# The pod of the previous revision answers while it terminates, so wait
# until the new one is the only pod before forwarding to the Service.
for _ in $(seq 60); do
  [ "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b -o name | wc -l)" = 1 ] && break
  sleep 1
done
kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b
kubectl -n "$NS" port-forward "svc/$FULLNAME" "$PF_PORT:8081" > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs "http://127.0.0.1:$PF_PORT/readyz" >/dev/null && break; sleep 1; done
signin "http://127.0.0.1:$PF_PORT"
api "http://127.0.0.1:$PF_PORT/api/v1/snapshot" | tee "$WORK/snapshot-nosecrets.json" | has '"secrets":"off"'
echo "the console reports Secrets as off"
# No release records are read, so the Helm view is empty and a release is not found.
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/helm/$NS/$RELEASE")" = 404 ]
api "http://127.0.0.1:$PF_PORT/api/v1/snapshot?views=helm" | python3 -c 'import json, sys; d = json.load(sys.stdin); assert d["views"]["helm"]["rows"] == [], d["views"]["helm"]'
echo "no Helm release is served"
# The namespace lists no Secrets, and their YAML is refused.
if api "http://127.0.0.1:$PF_PORT/api/v1/snapshot?views=ns:$NS" | has '"kind":"Secret"'; then echo "a Secret is listed"; exit 1; fi
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PF_PORT/api/v1/yaml/secrets/$NS/sh.helm.release.v1.$RELEASE.v1")" = 404 ]
echo "no Secret is listed or served"
# The console still works without Secrets: nodes and pods are served.
api "http://127.0.0.1:$PF_PORT/api/v1/snapshot?views=nodes,pods" | has "\"$CLUSTER-control-plane\""
echo "nodes and pods are served without Secrets"
# The records are still in the database: the setting saved before.
check_database "http://127.0.0.1:$PF_PORT" api
api "http://127.0.0.1:$PF_PORT/api/v1/settings" | has '"refreshSeconds":11'
kill "$pf"
endstep

step "helm uninstall leaves nothing behind but the database's Secrets, volumes and Patroni's ConfigMaps"
kubectl -n "$NS" delete pod -l e2e=crashloop --ignore-not-found
kubectl delete rolebinding -A -l e2e=dev --ignore-not-found
kubectl delete clusterrolebinding e2e-admin e2e-viewer e2e-agents --ignore-not-found
# The Watson tests set the local cluster's environment to production and
# back: what is shown for it is kept like a connection, in a Secret the
# chart does not own, which survives an uninstall as connections do.
kubectl -n "$NS" delete secret "$FULLNAME-cluster-local" --ignore-not-found
# Sign-ins are kept one per Secret (P7-02); the chart does not own them
# either, and they would let a reinstall keep its sessions.
kubectl -n "$NS" delete secret -l app.kubernetes.io/component=session --ignore-not-found
# The AIOps cases are ConfigMaps the console makes, one per case (ADR-128),
# and the backup key (kept on purpose, BK-15) and the credentials of the
# knowledge sources and Helm repositories, and Administration › Credentials
# (<release>-credentials, 0.12.1), are Secrets it makes: records
# of its own, which outlive the release like its connections. So is the
# key that signs a session made while the session store is away
# (<release>-session-key, DB-65).
kubectl -n "$NS" delete configmap -l 221b.io/case --ignore-not-found
kubectl -n "$NS" delete secret "$FULLNAME-backup-key" "$FULLNAME-session-key" "$FULLNAME-knowledge" "$FULLNAME-helm-repositories" "$FULLNAME-credentials" --ignore-not-found
# Foreground deletion: the Deployment goes only once its pods have gone.
# Helm 4's wait sometimes gives up on a Deployment still Terminating right
# after a rollout; what matters is checked below: nothing may remain.
helm uninstall "$RELEASE" -n "$NS" --cascade foreground --wait --timeout 180s || echo "helm uninstall --wait reported an error; checking what is left"
# The bundled database outlives the release on purpose (docs/upgrading.md,
# docs/database.md): its two Secrets (helm.sh/resource-policy: keep), the
# StatefulSet's claims (whenDeleted: Retain) and Patroni's own ConfigMaps,
# so a reinstall finds its records. They must be there; then they go, as
# docs/database.md says to remove a bundled database no longer wanted.
for _ in $(seq 60); do
  [ -z "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b-db -o name)" ] && break
  sleep 2
done
kubectl -n "$NS" get secret "$DBNAME" "$DBNAME-tls" -o name
kubectl -n "$NS" get pvc "data-$DBNAME-0" "backup-$DBNAME-0" -o name
kubectl -n "$NS" get configmap "$DBNAME-config" "$DBNAME-leader" -o name
kubectl -n "$NS" delete secret "$DBNAME" "$DBNAME-tls"
kubectl -n "$NS" delete pvc "data-$DBNAME-0" "backup-$DBNAME-0" --wait=false
kubectl -n "$NS" delete configmap "$DBNAME-config" "$DBNAME-leader" "$DBNAME-failover" "$DBNAME-sync" "$DBNAME-failsafe" --ignore-not-found
leftovers() {
  kubectl -n "$NS" get all,serviceaccount,configmap,secret,networkpolicy,role,rolebinding,ingress,pvc,poddisruptionbudget -o name |
    grep -v -E '^(serviceaccount/default|configmap/kube-root-ca.crt)$' || true
  kubectl get clusterrole,clusterrolebinding -o name -l "app.kubernetes.io/instance=$RELEASE"
}
for _ in $(seq 60); do
  left=$(leftovers)
  [ -z "$left" ] && break
  sleep 2
done
if [ -n "$left" ]; then
  echo "Left behind after helm uninstall:"
  echo "$left"
  exit 1
fi
notice "helm uninstall" "Nothing left behind on Kubernetes $MINOR."
endstep
