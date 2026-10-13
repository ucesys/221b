#!/usr/bin/env bash
# Installs the chart in a kind cluster and checks it the way the acceptance
# criteria ask: Ready within 60 seconds, read-only permissions, the browser
# tests, then upgrade, turn Secrets off, and uninstall leaving nothing
# behind.
#
#   IMAGE=ghcr.io/ucesys/221b:dev hack/e2e-kind.sh 1.37
#
# Needs docker, kubectl, helm and, for the browser tests, the web/ packages
# and Playwright's Chromium. Set KEEP=1 to keep the cluster afterwards.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

MINOR=${1:-1.37}
IMAGE=${IMAGE:?set IMAGE to the image to test, already in the local docker}
CLUSTER=${CLUSTER:-221b-e2e}
NS=221b
RELEASE=console
FULLNAME=console-221b
SA=system:serviceaccount:$NS:$FULLNAME
BASE_URL=http://127.0.0.1:8080

install_kind "$MINOR"

cleanup() {
  local code=$?
  if [ "$code" -ne 0 ]; then
    step "Diagnostics"
    {
      kubectl get nodes -o wide
      kubectl get pods -A -o wide
      kubectl -n "$NS" describe pods
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=200 --all-containers
      kubectl get events -A --sort-by=.lastTimestamp | tail -40
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
endstep

step "Install metrics-server"
install_metrics_server
METRICS_MANIFEST=$(metrics_server_manifest)
endstep

step "helm install: Ready within 60 seconds"
# The namespace enforces the Pod Security restricted profile (X-05).
kubectl create namespace "$NS"
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest
start=$(date +%s)
helm install "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" \
  --set clusterName=kind-e2e \
  --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
  --set service.type=NodePort --set service.nodePort=30080 \
  --wait --timeout 60s
notice "helm install" "The console was Ready $(( $(date +%s) - start ))s after helm install on Kubernetes $MINOR."
kubectl -n "$NS" get all,networkpolicy,serviceaccount
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
names() { curl -fsS "$1/api/v1/snapshot?views=nodes" | python3 -c 'import json, sys; print(" ".join(sorted(r["name"] for r in json.load(sys.stdin)["views"]["nodes"]["rows"])))'; }
laptop=$(names http://127.0.0.1:18081)
incluster=$(names "$BASE_URL")
docker rm -f 221b-kubeconfig > /dev/null
echo "kubeconfig: $laptop"
echo "in-cluster: $incluster"
[ -n "$laptop" ] && [ "$laptop" = "$incluster" ]
endstep

step "Open it through kubectl port-forward"
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
curl -fsS http://127.0.0.1:18080/readyz
curl -fsS http://127.0.0.1:18080/ | grep -q '<div id="root">'
endstep

step "Settings are kept in the ConfigMap and uptime comes from the kubelets"
curl -fsS http://127.0.0.1:18080/api/v1/settings | tee "$WORK/settings.json"
grep -q '"refreshSeconds":3' "$WORK/settings.json"
grep -q '"storage":"configmap"' "$WORK/settings.json"
curl -fsS -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":7}' http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":7'
kubectl -n "$NS" get configmap "$FULLNAME" -o jsonpath='{.data.settings\.json}' | tee "$WORK/settings-cm.json"
grep -q '"refreshSeconds":7' "$WORK/settings-cm.json"
# A change made with kubectl is picked up too, within the re-read interval.
kubectl -n "$NS" patch configmap "$FULLNAME" --type merge -p '{"data":{"settings.json":"{\"refreshSeconds\":9}"}}'
for _ in $(seq 75); do
  curl -fsS http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":9' && break
  sleep 1
done
curl -fsS http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":9'
curl -fsS -X PUT -H 'Content-Type: application/json' -d '{"refreshSeconds":3}' http://127.0.0.1:18080/api/v1/settings > /dev/null
# Every kind node has a real kubelet, so every row carries a boot time.
for _ in $(seq 60); do
  nodes=$(curl -fsS 'http://127.0.0.1:18080/api/v1/snapshot?views=nodes' | python3 -c 'import json, sys; rows = json.load(sys.stdin)["views"]["nodes"]["rows"]; print(sum(1 for r in rows if r.get("bootedAt")), len(rows))')
  [ "${nodes% *}" = "${nodes#* }" ] && break
  sleep 2
done
echo "nodes with a boot time: $nodes"
[ "${nodes% *}" = "${nodes#* }" ]
curl -fsS http://127.0.0.1:18080/api/v1/snapshot | grep -q '"uptime":"available"'
# Secrets are read (the default): the console's own Helm release shows,
# with its values, and the record's Secret is listed by key name and size.
curl -fsS http://127.0.0.1:18080/api/v1/snapshot | grep -q '"secrets":"read"'
curl -fsS "http://127.0.0.1:18080/api/v1/helm/$NS/$RELEASE" | tee "$WORK/release.json" | grep -q '"name":"console"'
grep -q 'clusterName: kind-e2e' "$WORK/release.json"
curl -fsS "http://127.0.0.1:18080/api/v1/snapshot?views=ns:$NS" | grep -q '"kind":"Secret"'
kill "$pf"
endstep

step "Browser tests"
(
  cd "$ROOT/web"
  E2E_MODE=kind E2E_BASE_URL=$BASE_URL E2E_NAMESPACE=$NS E2E_IMAGE=$IMAGE \
    E2E_CONTROL_PLANE=$CLUSTER-control-plane E2E_METRICS_MANIFEST=$METRICS_MANIFEST \
    npx playwright test 2>&1 | tee "$WORK/playwright.log"
)
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
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
curl -fsS http://127.0.0.1:18080/readyz
curl -fsS http://127.0.0.1:18080/api/v1/settings | grep -q '"refreshSeconds":11'
kill "$pf"
endstep

step "secrets.read=false grants nothing on Secrets, lists none and hides the Helm tab (P2-10)"
helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values --set secrets.read=false --wait --timeout 60s
kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=60s
"$ROOT/hack/check-rbac.sh" --no-secrets "$SA"
kubectl -n "$NS" port-forward "svc/$FULLNAME" 18080:8081 > "$WORK/port-forward.log" 2>&1 &
pf=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:18080/readyz >/dev/null && break; sleep 1; done
curl -fsS http://127.0.0.1:18080/api/v1/snapshot | grep -q '"secrets":"off"'
# No release records are read, so the Helm view is empty and a release is not found.
[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080/api/v1/helm/$NS/$RELEASE")" = 404 ]
curl -fsS 'http://127.0.0.1:18080/api/v1/snapshot?views=helm' | python3 -c 'import json, sys; d = json.load(sys.stdin); assert d["views"]["helm"]["rows"] == [], d["views"]["helm"]'
# The namespace lists no Secrets, and their YAML is refused.
! curl -fsS "http://127.0.0.1:18080/api/v1/snapshot?views=ns:$NS" | grep -q '"kind":"Secret"'
[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:18080/api/v1/yaml/secrets/$NS/sh.helm.release.v1.$RELEASE.v1")" = 404 ]
# The console still works without Secrets: nodes and pods are served.
curl -fsS 'http://127.0.0.1:18080/api/v1/snapshot?views=nodes,pods' | grep -q '"kind-e2e-control-plane"'
kill "$pf"
endstep

step "helm uninstall leaves nothing behind"
kubectl -n "$NS" delete pod -l e2e=crashloop --ignore-not-found
# Foreground deletion: the Deployment goes only once its pods have gone.
helm uninstall "$RELEASE" -n "$NS" --cascade foreground --wait --timeout 120s
leftovers() {
  kubectl -n "$NS" get all,serviceaccount,configmap,secret,networkpolicy,role,rolebinding,ingress -o name |
    grep -v -E '^(serviceaccount/default|configmap/kube-root-ca.crt)$' || true
  kubectl get clusterrole,clusterrolebinding -o name -l "app.kubernetes.io/instance=$RELEASE"
}
for _ in $(seq 30); do
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
