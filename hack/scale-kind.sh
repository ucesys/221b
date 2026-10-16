#!/usr/bin/env bash
# P1-18: the console with 500 nodes and 15,000 pods, simulated with kwok in
# a one-node kind cluster. Measures the first screen, sorting and the
# server's memory with the browser test in web/e2e/scale.spec.ts.
#
#   IMAGE=ghcr.io/ucesys/221b:dev hack/scale-kind.sh
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

MINOR=${MINOR:-1.37}
NODES=${NODES:-500}
PODS=${PODS:-15000}
IMAGE=${IMAGE:?set IMAGE to the image to test, already in the local docker}
CLUSTER=${CLUSTER:-221b-scale}
NS=221b

install_kind "$MINOR"

proxy=""
cleanup() {
  local code=$?
  [ -n "$proxy" ] && kill "$proxy" 2>/dev/null
  if [ "$code" -ne 0 ]; then
    {
      kubectl get nodes --no-headers | awk '{print $2}' | sort | uniq -c
      kubectl get pods -A --no-headers | awk '{print $4}' | sort | uniq -c
      kubectl -n "$NS" describe pods
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=100
      kubectl -n kube-system logs deployment/kwok-controller --tail=50
    } > "$WORK/diagnostics.log" 2>&1 || true
    if [ -z "${GITHUB_ACTIONS:-}" ]; then cat "$WORK/diagnostics.log"; fi
  fi
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  exit "$code"
}
trap cleanup EXIT

step "Create a one-node kind cluster"
cat > "$WORK/kind-scale.yaml" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30080
        hostPort: 8080
        listenAddress: 127.0.0.1
YAML
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE[$MINOR]}" --config "$WORK/kind-scale.yaml" --wait 120s
kind load docker-image "$IMAGE" --name "$CLUSTER"
endstep

step "Install kwok $KWOK_VERSION"
fetch "https://github.com/kubernetes-sigs/kwok/releases/download/$KWOK_VERSION/kwok.yaml" "$WORK/kwok.yaml" "$KWOK_SHA256"
fetch "https://github.com/kubernetes-sigs/kwok/releases/download/$KWOK_VERSION/stage-fast.yaml" "$WORK/kwok-stage-fast.yaml" "$KWOK_STAGE_FAST_SHA256"
kubectl apply -f "$WORK/kwok.yaml"
kubectl apply -f "$WORK/kwok-stage-fast.yaml"
kubectl -n kube-system rollout status deployment/kwok-controller --timeout=180s
endstep

step "Create $NODES nodes and $PODS pods"
kubectl proxy --port=8001 > "$WORK/proxy.log" 2>&1 &
proxy=$!
for _ in $(seq 30); do curl -fs http://127.0.0.1:8001/version >/dev/null && break; sleep 1; done
python3 "$ROOT/hack/fill-kwok.py" --nodes "$NODES" --pods "$PODS"
start=$(date +%s)
for _ in $(seq 240); do
  ready=$(kubectl get nodes --no-headers | awk '$2 == "Ready"' | wc -l)
  running=$(kubectl get pods -A --no-headers --field-selector=status.phase=Running | grep -c ' app-' || true)
  echo "$ready nodes Ready, $running of $PODS pods Running"
  [ "$ready" -gt "$NODES" ] && [ "$running" -ge "$PODS" ] && break
  sleep 5
done
[ "$ready" -gt "$NODES" ] && [ "$running" -ge "$PODS" ]
notice "kwok" "$NODES nodes and $PODS pods running $(( $(date +%s) - start ))s after creation."
endstep

step "Install the console"
kubectl create namespace "$NS"
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted
helm install k8s-console "$ROOT/deploy/helm/221b" -n "$NS" \
  --set clusterName=kwok-scale \
  --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
  --set service.type=NodePort --set service.nodePort=30080 \
  --set auth.mode=none \
  --wait --timeout 120s
for _ in $(seq 60); do curl -fs http://127.0.0.1:8080/readyz >/dev/null && break; sleep 1; done
endstep

step "Measure"
(
  cd "$ROOT/web"
  E2E_BASE_URL=http://127.0.0.1:8080 E2E_SCALE=$((NODES + 1)) npx playwright test scale.spec.ts 2>&1 | tee "$WORK/scale.log"
)
result=$(grep -o 'SCALE: .*' "$WORK/scale.log" | head -1 || true)
notice "Scale with $NODES nodes and $PODS pods" "${result#SCALE: }"
endstep
