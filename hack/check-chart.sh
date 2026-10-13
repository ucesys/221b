#!/usr/bin/env bash
# Lints the chart and validates what it renders against the Kubernetes API
# schemas, with the settings people are likely to use.
set -euo pipefail
# shellcheck source=hack/lib.sh
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
  for v in 1.35.0 1.37.1; do kubeconform -strict -summary -kubernetes-version "$v" < "$f"; done
}

echo "--- defaults"
render console --set clusterName=ci | tee "$WORK/chart-default.yaml" | validate
echo "--- NodePort, no NetworkPolicy, no RBAC, an existing ServiceAccount"
render console --set clusterName=ci --set service.type=NodePort --set service.nodePort=30080 \
  --set networkPolicy.enabled=false --set rbac.create=false --set serviceAccount.create=false \
  --set serviceAccount.name=viewer | validate
echo "--- allowed ingress peers, API server addresses, extra host names and a private registry"
render console --set clusterName=ci --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.team=platform' \
  --set 'networkPolicy.apiServerCIDRs[0]=10.0.0.10/32' --set 'allowedHosts[0]=console.example.com' \
  --set 'imagePullSecrets[0].name=ghcr' | tee "$WORK/chart-options.yaml" | validate
echo "--- a release name and an override that start with a digit"
render 221b --set clusterName=ci --set fullnameOverride=2b | validate

fail=0
check() {
  local what=$1
  shift
  if "$@"; then
    echo "ok: $what"
  else
    echo "FAIL: $what"
    fail=1
  fi
}
out=$WORK/chart-default.yaml
kinds() {
  [ "$(grep -E '^kind: ' "$out" | sort | tr '\n' ' ')" = "kind: ClusterRole kind: ClusterRoleBinding kind: Deployment kind: NetworkPolicy kind: Service kind: ServiceAccount " ]
}
no_ingress() { ! grep -q '^kind: Ingress' "$out"; }
cluster_ip() { grep -q 'type: ClusterIP' "$out"; }
probes_and_resources() {
  grep -q 'livenessProbe:' "$out" && grep -q 'readinessProbe:' "$out" && grep -q 'requests:' "$out" && grep -q 'limits:' "$out"
}
hardened() { grep -q 'runAsNonRoot: true' "$out" && grep -q 'readOnlyRootFilesystem: true' "$out"; }
read_only_resources() {
  [ "$(grep -A2 -E '^  - apiGroups' "$out" | grep -E 'resources:' | tr -d ' ' | tr '\n' ' ')" = 'resources:["nodes","pods"] resources:["nodes"] ' ]
}
read_only_verbs() { ! grep -E 'verbs:' "$out" | grep -q -E 'create|update|patch|delete|\*'; }
service_name() { render 221b --set clusterName=ci | grep -A3 '^kind: Service$' | grep -q 'name: console-221b'; }
name_required() { ! render console > /dev/null 2>&1; }
no_open_egress() { ! render console --set clusterName=ci --set networkPolicy.apiServerPorts=null > /dev/null 2>&1; }
no_dns_egress() { ! grep -q 'port: 53' "$out"; }
api_server_addresses() { grep -q 'cidr: "10.0.0.10/32"' "$WORK/chart-options.yaml"; }
host_names() {
  grep -A1 'name: ALLOWED_HOSTS' "$out" | grep -q 'console-221b,console-221b.consoles,console-221b.consoles.svc,console-221b.consoles.svc.cluster.local' &&
    grep -A1 'name: ALLOWED_HOSTS' "$WORK/chart-options.yaml" | grep -q ',console.example.com"'
}
digit_override() { render 221b --set clusterName=ci --set fullnameOverride=2b | grep -A3 '^kind: Service$' | grep -q 'name: console-2b'; }

check "renders a Deployment, Service, ServiceAccount, ClusterRole, ClusterRoleBinding and NetworkPolicy" kinds
check "no Ingress" no_ingress
check "the Service is a ClusterIP" cluster_ip
check "probes, requests and limits" probes_and_resources
check "runs as non-root on a read-only root filesystem" hardened
check "reads nothing but nodes, pods and node metrics" read_only_resources
check "verbs are read-only" read_only_verbs
check "a release named after the chart still gets a valid Service name" service_name
check "clusterName is required" name_required
check "an empty list of API server ports is refused" no_open_egress
check "no DNS traffic is allowed out" no_dns_egress
check "API server addresses narrow the egress rule" api_server_addresses
check "it answers to its Service's names and the names in values" host_names
check "an override that starts with a digit still gets a valid name" digit_override
exit "$fail"
