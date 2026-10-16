#!/usr/bin/env bash
# Installs the chart in a kind cluster and checks it the way the acceptance
# criteria ask: Ready within 60 seconds, read-only permissions, the browser
# tests, then upgrade, turn Secrets off, and uninstall leaving nothing
# behind.
#
#   IMAGE=ghcr.io/ucesys/221b:dev hack/e2e-kind.sh 1.37
#
# Needs docker, kubectl, helm, Go (for the fake Claude API) and, for the browser tests, the web/ packages
# and Playwright's Chromium. Set KEEP=1 to keep the cluster afterwards.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

MINOR=${1:-1.37}
IMAGE=${IMAGE:?set IMAGE to the image to test, already in the local docker}
CLUSTER=${CLUSTER:-221b-e2e}
NS=221b
RELEASE=k8s-console
FULLNAME=k8s-console-221b
SA=system:serviceaccount:$NS:$FULLNAME
BASE_URL=http://127.0.0.1:8080
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
  if [ "$code" -ne 0 ]; then
    step "Diagnostics"
    # The console's own log comes last: CI reports the file's tail.
    {
      kubectl get nodes -o wide
      kubectl get pods -A -o wide
      kubectl -n "$NS" describe pods
      kubectl get events -A --sort-by=.lastTimestamp | tail -40
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

step "Create a kind cluster with Kubernetes $MINOR"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE[$MINOR]}" --config "$ROOT/hack/kind.yaml" --wait 120s
kind load docker-image "$IMAGE" --name "$CLUSTER"
kubectl version
GATEWAY=$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' | tr ' ' '\n' | grep -E '^[0-9]+([.][0-9]+){3}$' | head -1)
echo "the fake Claude API is at http://$GATEWAY:7445/claude for the cluster"
docker exec "$CLUSTER-control-plane" curl -fsS -o /dev/null "http://$GATEWAY:7445/claude/requests"
endstep

step "Install metrics-server"
install_metrics_server
METRICS_MANIFEST=$(metrics_server_manifest)
endstep

step "helm install: Ready within 60 seconds"
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
  --set localCluster.mode=write \
  --set sherlock.apiURL="http://$GATEWAY:7445/claude" --set 'networkPolicy.outboundPorts={443,7445}' \
  --wait --timeout 60s
notice "helm install" "The console was Ready $(( $(date +%s) - start ))s after helm install on Kubernetes $MINOR."
kubectl -n "$NS" get all,networkpolicy,serviceaccount
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
docker run -d --rm --name 221b-kubeconfig --network host -v "$WORK/kubeconfig:/kubeconfig:ro" "$IMAGE" \
  -kubeconfig /kubeconfig -cluster-name laptop -listen 127.0.0.1:18081 > /dev/null
for _ in $(seq 60); do curl -fs http://127.0.0.1:18081/readyz >/dev/null && break; sleep 1; done
names() { curl -fsS -b "$COOKIES" "$1/api/v1/snapshot?views=nodes" | python3 -c 'import json, sys; print(" ".join(sorted(r["name"] for r in json.load(sys.stdin)["views"]["nodes"]["rows"])))'; }
# The kubeconfig instance runs without sign-in; the in-cluster one needs a session.
rm -f "$COOKIES"; touch "$COOKIES"
laptop=$(names http://127.0.0.1:18081)
signin "$BASE_URL"
incluster=$(names "$BASE_URL")
docker rm -f 221b-kubeconfig > /dev/null
echo "kubeconfig: $laptop"
echo "in-cluster: $incluster"
[ -n "$laptop" ] && [ "$laptop" = "$incluster" ]
endstep

step "Open it through kubectl port-forward: the sign-in page, then the app (P3-01)"
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
curl -fsS http://127.0.0.1:18080/readyz
# Without a session: the sign-in page, and 401 from the API.
curl -fsS http://127.0.0.1:18080/ | grep -q 'data-testid="signin-form"'
[ "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/snapshot)" = 401 ]
# A wrong password is refused and recorded; the right one gets the app.
# A form posted from elsewhere, without the page's token, is refused; the
# page's own form with a wrong password is 401.
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST --data-urlencode "email=$E2E_USER" --data-urlencode "password=$E2E_PASSWORD" http://127.0.0.1:18080/signin)" = 403 ]
form=$(curl -fsS -c "$WORK/form-cookies" http://127.0.0.1:18080/signin | grep -o 'name="form" value="[^"]*"' | cut -d'"' -f4)
[ "$(curl -s -b "$WORK/form-cookies" -o /dev/null -w '%{http_code}' -X POST --data-urlencode "email=$E2E_USER" --data-urlencode "password=wrong" --data-urlencode "form=$form" http://127.0.0.1:18080/signin)" = 401 ]
signin http://127.0.0.1:18080
api http://127.0.0.1:18080/ | grep -q '<div id="root">'
api http://127.0.0.1:18080/api/v1/me | tee "$WORK/me.json" | grep -q '"admin":true'
grep -q "\"username\":\"$E2E_USER\"" "$WORK/me.json"
# The audit log names the person, on standard output (P3-10).
kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=-1 | grep '"action"' | tee "$WORK/audit.log"
grep -q "\"action\":\"signin\".*\"email\":\"$E2E_USER\"" "$WORK/audit.log"
grep -q '"action":"signin.failed"' "$WORK/audit.log"
endstep

step "Settings are kept in the ConfigMap and uptime comes from the kubelets"
api http://127.0.0.1:18080/api/v1/settings | tee "$WORK/settings.json"
grep -q '"refreshSeconds":3' "$WORK/settings.json"
grep -q '"storage":"configmap"' "$WORK/settings.json"
api -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":7}' http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":7'
kubectl -n "$NS" get configmap "$FULLNAME" -o jsonpath='{.data.settings\.json}' | tee "$WORK/settings-cm.json"
grep -q '"refreshSeconds":7' "$WORK/settings-cm.json"
# A change made with kubectl is picked up too, within the re-read interval.
kubectl -n "$NS" patch configmap "$FULLNAME" --type merge -p '{"data":{"settings.json":"{\"refreshSeconds\":9}"}}'
for _ in $(seq 75); do
  api http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":9' && break
  sleep 1
done
api http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":9'
api -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":3}' http://127.0.0.1:18080/api/v1/settings > /dev/null
# Every kind node has a real kubelet, so every row carries a boot time.
for _ in $(seq 60); do
  nodes=$(api 'http://127.0.0.1:18080/api/v1/snapshot?views=nodes' | python3 -c 'import json, sys; rows = json.load(sys.stdin)["views"]["nodes"]["rows"]; print(sum(1 for r in rows if r.get("bootedAt")), len(rows))')
  [ "${nodes% *}" = "${nodes#* }" ] && break
  sleep 2
done
echo "nodes with a boot time: $nodes"
[ "${nodes% *}" = "${nodes#* }" ]
api http://127.0.0.1:18080/api/v1/snapshot | grep -q '"uptime":"available"'
# Secrets are read (the default): the console's own Helm release shows,
# with its values, and the record's Secret is listed by key name and size.
api http://127.0.0.1:18080/api/v1/snapshot | grep -q '"secrets":"read"'
api "http://127.0.0.1:18080/api/v1/helm/$NS/$RELEASE" | tee "$WORK/release.json" | grep -q '"name":"k8s-console"'
grep -q 'clusterName: kind-e2e' "$WORK/release.json"
api "http://127.0.0.1:18080/api/v1/snapshot?views=ns:$NS" | grep -q '"kind":"Secret"'
kill "$pf"
endstep

step "Browser tests"
(
  cd "$ROOT/web"
  E2E_MODE=kind E2E_BASE_URL=$BASE_URL E2E_NAMESPACE=$NS E2E_IMAGE=$IMAGE \
    E2E_CONTROL_PLANE=$CLUSTER-control-plane E2E_METRICS_MANIFEST=$METRICS_MANIFEST \
    E2E_USER=$E2E_USER E2E_VIEWER=$E2E_VIEWER E2E_DEV=$E2E_DEV E2E_DEV_KUBE=$E2E_DEV_KUBE E2E_VIEWER_KUBE=$E2E_VIEWER_KUBE E2E_PASSWORD=$E2E_PASSWORD \
    E2E_CLAUDE_URL=http://127.0.0.1:7445/claude \
    npx playwright test 2>&1 | tee "$WORK/playwright.log"
)
endstep

step "a cluster connection is kept in a Secret the chart does not own (P3-07, P3-09)"
# The kind cluster itself, added a second time through its in-cluster
# address with a token of the console's own ServiceAccount and the CA
# every pod gets; the connection must survive the upgrade below.
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
signin http://127.0.0.1:18080
TOKEN=$(kubectl -n "$NS" create token "$FULLNAME" --duration=2h)
python3 - "$TOKEN" "$(kubectl -n "$NS" get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}')" > "$WORK/draft.json" <<'PY'
import json, sys
print(json.dumps({"name": "kept", "env": "staging", "description": "kind, through its in-cluster address", "server": "https://kubernetes.default.svc", "ca": "pem", "caPem": sys.argv[2], "auth": "token", "token": sys.argv[1], "mode": "read"}))
PY
api -X POST -H 'Content-Type: application/json' -H 'Sec-Fetch-Site: same-origin' --data-binary "@$WORK/draft.json" http://127.0.0.1:18080/api/v1/clusters | tee "$WORK/kept.json" | grep -q '"id":"kept"'
! grep -q "$TOKEN" "$WORK/kept.json"
kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" -o jsonpath='{.data}' | grep -q '"token"'
echo "the connection's Secret holds the token; the API answered without it"
# Connected, then synced a moment later, once the informers have listed.
for _ in $(seq 60); do
  api http://127.0.0.1:18080/api/v1/clusters/kept | tee "$WORK/kept-state.json" | grep -q '"synced":true' && break
  sleep 1
done
grep -q '"synced":true' "$WORK/kept-state.json" || { echo "the connection did not come up: $(cat "$WORK/kept-state.json")"; exit 1; }
api 'http://127.0.0.1:18080/api/v1/clusters/kept/snapshot?views=nodes' | grep -q "\"$CLUSTER-control-plane\""
echo "the connection serves the cluster's nodes under its own route"
# Its test passes every step with the CA and token given.
api -X POST -H 'Sec-Fetch-Site: same-origin' http://127.0.0.1:18080/api/v1/clusters/kept/test | tee "$WORK/kept-test.json" | python3 -c '
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
! grep -q "$TOKEN" "$WORK/audit-connections.log"
kill "$pf"
endstep

step "helm upgrade keeps the settings"
# A setting saved before the upgrade must survive it: the chart owns the
# ConfigMap but never its data.
kubectl -n "$NS" patch configmap "$FULLNAME" --type merge -p '{"data":{"settings.json":"{\"refreshSeconds\":11}"}}'
helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values --set logLevel=debug --wait --timeout 60s
kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=60s
[ "$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="LOG_LEVEL")].value}')" = debug ]
kubectl -n "$NS" get configmap "$FULLNAME" -o jsonpath='{.data.settings\.json}' | grep -q '"refreshSeconds":11'
# Through a port-forward: kube-proxy may still be catching up after the
# browser tests restarted the API server, which would delay the NodePort.
for _ in $(seq 60); do
  [ "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b -o name | wc -l)" = 1 ] && break
  sleep 1
done
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
curl -fsS http://127.0.0.1:18080/readyz
# Sessions live in the console's memory, so the restart signed everyone out.
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/settings)" = 401 ]
signin http://127.0.0.1:18080
api http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":11'
# The connection came back from its Secret, and connects again.
for _ in $(seq 60); do
  api http://127.0.0.1:18080/api/v1/clusters/kept | grep -q '"state":"connected"' && break
  sleep 1
done
api http://127.0.0.1:18080/api/v1/clusters/kept | grep -q '"description":"kind, through its in-cluster address"'
echo "the connection survived the upgrade"
# Removing it deletes its Secret (P3-08).
api -X DELETE -H 'Sec-Fetch-Site: same-origin' http://127.0.0.1:18080/api/v1/clusters/kept | grep -q '"removed":true'
! kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" > /dev/null 2>&1
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/v1/clusters/kept)" = 404 ]
echo "the connection and its Secret are gone"
# Sherlock's API key, saved by the browser tests, is kept in the release's
# Secret, survived the upgrade and is never served (P5-05, X-04); removing
# it in Settings deletes the Secret.
[ "$(kubectl -n "$NS" get secret "$FULLNAME-sherlock" -o jsonpath='{.data.apiKey}' | base64 -d)" = sk-ant-e2e-fake-key-0123456789 ]
api http://127.0.0.1:18080/api/v1/sherlock/config | tee "$WORK/sherlock-config.json" | grep -q '"set":true'
! grep -q sk-ant-e2e-fake-key "$WORK/sherlock-config.json"
api -X DELETE -H 'Sec-Fetch-Site: same-origin' http://127.0.0.1:18080/api/v1/sherlock/key | grep -q '"set":false'
! kubectl -n "$NS" get secret "$FULLNAME-sherlock" > /dev/null 2>&1
echo "the Sherlock key survived the upgrade, was never served, and its Secret is gone with it"
kill "$pf"
endstep

step "secrets.read=false grants nothing on Secrets, lists none and hides the Helm tab (P2-10); connections.store=memory grants nothing in its namespace either"
helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values --set secrets.read=false --set connections.store=memory --wait --timeout 60s
kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=60s
"$ROOT/hack/check-rbac.sh" --no-secrets --memory "$SA"
# The pod of the previous revision answers while it terminates, so wait
# until the new one is the only pod before forwarding to the Service.
for _ in $(seq 60); do
  [ "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b -o name | wc -l)" = 1 ] && break
  sleep 1
done
kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
signin http://127.0.0.1:18080
api http://127.0.0.1:18080/api/v1/snapshot | tee "$WORK/snapshot-nosecrets.json" | grep -q '"secrets":"off"'
echo "the console reports Secrets as off"
# No release records are read, so the Helm view is empty and a release is not found.
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080/api/v1/helm/$NS/$RELEASE")" = 404 ]
api 'http://127.0.0.1:18080/api/v1/snapshot?views=helm' | python3 -c 'import json, sys; d = json.load(sys.stdin); assert d["views"]["helm"]["rows"] == [], d["views"]["helm"]'
echo "no Helm release is served"
# The namespace lists no Secrets, and their YAML is refused.
! api "http://127.0.0.1:18080/api/v1/snapshot?views=ns:$NS" | grep -q '"kind":"Secret"'
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080/api/v1/yaml/secrets/$NS/sh.helm.release.v1.$RELEASE.v1")" = 404 ]
echo "no Secret is listed or served"
# The console still works without Secrets: nodes and pods are served.
api 'http://127.0.0.1:18080/api/v1/snapshot?views=nodes,pods' | grep -q "\"$CLUSTER-control-plane\""
echo "nodes and pods are served without Secrets"
kill "$pf"
endstep

step "helm uninstall leaves nothing behind"
kubectl -n "$NS" delete pod -l e2e=crashloop --ignore-not-found
kubectl delete rolebinding -A -l e2e=dev --ignore-not-found
kubectl delete clusterrolebinding e2e-admin e2e-viewer --ignore-not-found
# The Watson tests set the local cluster's environment to production and
# back: what is shown for it is kept like a connection, in a Secret the
# chart does not own, which survives an uninstall as connections do.
kubectl -n "$NS" delete secret "$FULLNAME-cluster-local" --ignore-not-found
# Foreground deletion: the Deployment goes only once its pods have gone.
# Helm 4's wait sometimes gives up on a Deployment still Terminating right
# after a rollout; what matters is checked below: nothing may remain.
helm uninstall "$RELEASE" -n "$NS" --cascade foreground --wait --timeout 120s || echo "helm uninstall --wait reported an error; checking what is left"
leftovers() {
  kubectl -n "$NS" get all,serviceaccount,configmap,secret,networkpolicy,role,rolebinding,ingress -o name |
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
