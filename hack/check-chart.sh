#!/usr/bin/env bash
# Lints the chart and validates what it renders against the Kubernetes API
# schemas, with the settings people are likely to use.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
CHART=$ROOT/deploy/helm/221b
install_kubeconform

helm lint --strict "$CHART" --set clusterName=ci

render() { helm template "$@" "$CHART" --namespace consoles; }
# Against the oldest and newest Kubernetes releases CI tests.
validate() {
  local f
  f=$(mktemp)
  cat > "$f"
  for v in 1.35.0 1.37.1; do kubeconform -strict -summary -kubernetes-version "$v" "$f"; done
}

echo "--- defaults"
render console --set clusterName=ci | tee "$WORK/chart-default.yaml" | validate
echo "--- NodePort, no NetworkPolicy, no RBAC, an existing ServiceAccount"
render console --set clusterName=ci --set service.type=NodePort --set service.nodePort=30080 \
  --set networkPolicy.enabled=false --set rbac.create=false --set serviceAccount.create=false \
  --set serviceAccount.name=viewer | validate
echo "--- allowed ingress peers and a private registry"
render console --set clusterName=ci --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.team=platform' \
  --set 'imagePullSecrets[0].name=ghcr' | validate

fail=0
check() {
  if ! eval "$2"; then
    echo "FAIL: $1"
    fail=1
  else
    echo "ok: $1"
  fi
}
out=$WORK/chart-default.yaml
check "renders a Deployment, Service, ServiceAccount, ClusterRole, ClusterRoleBinding and NetworkPolicy" \
  '[ "$(grep -E "^kind: " "$out" | sort | tr "\n" " ")" = "kind: ClusterRole kind: ClusterRoleBinding kind: Deployment kind: NetworkPolicy kind: Service kind: ServiceAccount " ]'
check "no Ingress" '! grep -q "^kind: Ingress" "$out"'
check "the Service is a ClusterIP" 'grep -q "type: ClusterIP" "$out"'
check "probes, requests and limits" 'grep -q "livenessProbe:" "$out" && grep -q "readinessProbe:" "$out" && grep -q "requests:" "$out" && grep -q "limits:" "$out"'
check "runs as non-root on a read-only root filesystem" 'grep -q "runAsNonRoot: true" "$out" && grep -q "readOnlyRootFilesystem: true" "$out"'
check "reads nothing but nodes, pods and node metrics" \
  '[ "$(grep -A2 -E "^  - apiGroups" "$out" | grep -E "resources:" | tr -d " " | tr "\n" " ")" = "resources:[\"nodes\",\"pods\"] resources:[\"nodes\"] " ]'
check "verbs are read-only" '! grep -E "verbs:" "$out" | grep -q -E "create|update|patch|delete|\*"'
check "a release named after the chart still gets a valid Service name" \
  'render 221b --set clusterName=ci | grep -A3 "^kind: Service$" | grep -q "name: console-221b"'
check "clusterName is required" '! render console > /dev/null 2>&1'
exit "$fail"
