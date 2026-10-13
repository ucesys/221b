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
echo "--- without the kubelets' stats"
render console --set clusterName=ci --set kubeletStats=false | tee "$WORK/chart-nostats.yaml" | validate
echo "--- without reading Secrets"
render console --set clusterName=ci --set secrets.read=false | tee "$WORK/chart-nosecrets.yaml" | validate
echo "--- allowed ingress peers, API server addresses, extra host names and a private registry"
render console --set clusterName=ci --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.team=platform' \
  --set 'networkPolicy.apiServerCIDRs[0]=10.0.0.10/32' --set 'allowedHosts[0]=console.example.com' \
  --set 'imagePullSecrets[0].name=ghcr' | tee "$WORK/chart-options.yaml" | validate
echo "--- exposed with a LoadBalancer on every node"
render console --set clusterName=ci --set service.type=LoadBalancer --set service.nodePort=30081 \
  --set 'service.externalIPs[0]=10.0.0.21' --set 'service.externalIPs[1]=10.0.0.22' \
  --set service.loadBalancerClass=metallb --set 'service.annotations.example\.com/pool=office' |
  tee "$WORK/chart-lb.yaml" | validate
echo "--- a release name and an override that start with a digit"
render 221b --set clusterName=ci --set fullnameOverride=2b | validate
echo "--- image tags that move, tags that do not, a digest and an explicit pull policy"
render console --set clusterName=ci --set image.tag=pr-7 | tee "$WORK/chart-moving.yaml" | validate
render console --set clusterName=ci --set image.tag=sha-d548f3953839 | tee "$WORK/chart-sha.yaml" | validate
render console --set clusterName=ci --set image.digest=sha256:ce75aee9b9c2cde129bb8796ece11c3004b4eaba1d9afceed8b11e6996f0665d | tee "$WORK/chart-digest.yaml" | validate
render console --set clusterName=ci --set image.tag=main --set image.pullPolicy=IfNotPresent | tee "$WORK/chart-policy.yaml" | validate

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
  [ "$(grep -E '^kind: ' "$out" | sort | tr '\n' ' ')" = "kind: ClusterRole kind: ClusterRoleBinding kind: ConfigMap kind: Deployment kind: NetworkPolicy kind: Role kind: RoleBinding kind: Service kind: ServiceAccount " ]
}
no_ingress() { ! grep -q '^kind: Ingress' "$out"; }
cluster_ip() { grep -q 'type: ClusterIP' "$out"; }
probes_and_resources() {
  grep -q 'livenessProbe:' "$out" && grep -q 'readinessProbe:' "$out" && grep -q 'requests:' "$out" && grep -q 'limits:' "$out"
}
hardened() { grep -q 'runAsNonRoot: true' "$out" && grep -q 'readOnlyRootFilesystem: true' "$out"; }
# The ClusterRole reads the kinds the tabs show, pod logs, metrics and the
# kubelets' stats, and Secrets only when asked to; the Role patches the
# settings ConfigMap, by name, and nothing else.
cluster_role() { sed -n '/^kind: ClusterRole$/,/^---/p' "${1:-$out}"; }
settings_role() { sed -n '/^kind: Role$/,/^---/p' "$out"; }
# Every resource the ClusterRole names, as group/resource, sorted.
granted() {
  cluster_role "$1" | python3 -c '
import sys, yaml
out = set()
for rule in next(yaml.safe_load_all(sys.stdin))["rules"]:
    for g in rule["apiGroups"]:
        for r in rule["resources"]:
            out.add(f"{g}/{r}" if g else r)
print(" ".join(sorted(out)))'
}
WANT_READ='apps/controllerrevisions apps/daemonsets apps/deployments apps/replicasets apps/statefulsets autoscaling/horizontalpodautoscalers batch/cronjobs batch/jobs configmaps coordination.k8s.io/leases discovery.k8s.io/endpointslices endpoints events limitranges metrics.k8s.io/nodes metrics.k8s.io/pods namespaces networking.k8s.io/ingresses networking.k8s.io/networkpolicies nodes nodes/proxy persistentvolumeclaims pods pods/log policy/poddisruptionbudgets rbac.authorization.k8s.io/rolebindings rbac.authorization.k8s.io/roles resourcequotas secrets serviceaccounts services'
read_only_resources() { [ "$(granted "$out")" = "$WANT_READ" ]; }
read_only_verbs() { ! cluster_role | grep -E 'verbs:' | grep -q -E 'create|update|patch|delete|\*'; }
# Logs and metrics are read, never listed or watched more than needed.
log_verbs() { cluster_role | grep -A1 '"pods/log"' | grep -q 'verbs: \["get"\]'; }
secrets_optional() {
  cluster_role | grep -A1 '"secrets"' | grep -q 'verbs: \["get", "list", "watch"\]' && grep -A1 'name: SECRETS_READ' "$out" | grep -q '"true"' &&
    [ "$(granted "$WORK/chart-nosecrets.yaml")" = "${WANT_READ/ secrets/}" ] && ! grep -q '"secrets"' "$WORK/chart-nosecrets.yaml" &&
    grep -A1 'name: SECRETS_READ' "$WORK/chart-nosecrets.yaml" | grep -q '"false"'
}
settings_only() {
  settings_role | grep -q 'resources: \["configmaps"\]' && settings_role | grep -q 'resourceNames: \["console-221b"\]' &&
    [ "$(settings_role | grep -E 'verbs:' | tr -d ' ' | tr '\n' ' ')" = 'verbs:["get","patch"] ' ] &&
    ! grep -E 'verbs:' "$out" | grep -q -E 'create|update|delete|\*'
}
no_kubelet_stats() {
  grep -q 'nodes/proxy' "$out" && ! grep -q 'nodes/proxy' "$WORK/chart-nostats.yaml" &&
    grep -A1 'name: KUBELET_STATS' "$WORK/chart-nostats.yaml" | grep -q '"false"'
}
settings_configmap() {
  grep -A3 '^kind: ConfigMap$' "$out" | grep -q 'name: console-221b' && ! sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | grep -q '^data:' &&
    grep -A1 'name: SETTINGS_CONFIGMAP' "$out" | grep -q '"consoles/console-221b"' && grep -A1 'name: REFRESH' "$out" | grep -q '"3s"'
}
service_name() { render 221b --set clusterName=ci | grep -A3 '^kind: Service$' | grep -q 'name: console-221b'; }
name_required() { ! render console > /dev/null 2>&1; }
no_open_egress() { ! render console --set clusterName=ci --set networkPolicy.apiServerPorts=null > /dev/null 2>&1; }
no_dns_egress() { ! grep -q 'port: 53' "$out"; }
api_server_addresses() { grep -q 'cidr: "10.0.0.10/32"' "$WORK/chart-options.yaml"; }
host_names() {
  grep -A1 'name: ALLOWED_HOSTS' "$out" | grep -q 'console-221b,console-221b.consoles,console-221b.consoles.svc,console-221b.consoles.svc.cluster.local' &&
    grep -A1 'name: ALLOWED_HOSTS' "$WORK/chart-options.yaml" | grep -q ',console.example.com"'
}
exposed_port() { grep -q 'port: 8081' "$out" && grep -q 'port: 8081' "$WORK/chart-lb.yaml"; }
# A release tag (the default) is fetched once; a moving tag is fetched on
# every pod start and the pod rolls on every upgrade; a sha- tag or a
# digest is fetched once; an explicit policy wins.
pull_policy() {
  grep -q 'image: "ghcr.io/ucesys/221b:0.2.0"' "$out" && grep -q 'imagePullPolicy: IfNotPresent' "$out" && ! grep -q 'release-revision' "$out" &&
    grep -q 'image: "ghcr.io/ucesys/221b:pr-7"' "$WORK/chart-moving.yaml" && grep -q 'imagePullPolicy: Always' "$WORK/chart-moving.yaml" &&
    grep -q '221b/release-revision: "1"' "$WORK/chart-moving.yaml" &&
    grep -q 'imagePullPolicy: IfNotPresent' "$WORK/chart-sha.yaml" && ! grep -q 'release-revision' "$WORK/chart-sha.yaml" &&
    grep -q 'image: "ghcr.io/ucesys/221b@sha256:ce75aee9b9c2cde129bb8796ece11c3004b4eaba1d9afceed8b11e6996f0665d"' "$WORK/chart-digest.yaml" &&
    grep -q 'imagePullPolicy: IfNotPresent' "$WORK/chart-digest.yaml" && ! grep -q 'release-revision' "$WORK/chart-digest.yaml" &&
    grep -q 'imagePullPolicy: IfNotPresent' "$WORK/chart-policy.yaml" && grep -q 'release-revision' "$WORK/chart-policy.yaml"
}
lb_service() {
  grep -q 'type: LoadBalancer' "$WORK/chart-lb.yaml" && grep -q 'nodePort: 30081' "$WORK/chart-lb.yaml" &&
    grep -q 'loadBalancerClass: "metallb"' "$WORK/chart-lb.yaml" && grep -q -- '- 10.0.0.22' "$WORK/chart-lb.yaml" &&
    grep -q 'externalTrafficPolicy: Cluster' "$WORK/chart-lb.yaml" && grep -q 'example.com/pool: office' "$WORK/chart-lb.yaml"
}
# A ClusterIP Service lets nothing in; an exposed one lets everyone in
# unless peers are listed.
closed_by_default() { grep -q 'ingress: \[\]' "$out"; }
open_when_exposed() { ! grep -q 'ingress: \[\]' "$WORK/chart-lb.yaml" && grep -A3 '^  ingress:' "$WORK/chart-lb.yaml" | grep -q 'port: http'; }
digit_override() { render 221b --set clusterName=ci --set fullnameOverride=2b | grep -A3 '^kind: Service$' | grep -q 'name: console-2b'; }

check "renders a Deployment, Service, ServiceAccount, ConfigMap, ClusterRole, Role, their bindings and a NetworkPolicy" kinds
check "no Ingress" no_ingress
check "the Service is a ClusterIP" cluster_ip
check "probes, requests and limits" probes_and_resources
check "runs as non-root on a read-only root filesystem" hardened
check "reads the kinds the tabs show, pod logs, metrics and the kubelets' stats, and nothing else" read_only_resources
check "the ClusterRole's verbs are read-only" read_only_verbs
check "pod logs are only read" log_verbs
check "secrets.read=false grants nothing on Secrets and tells the server" secrets_optional
check "it writes only its own settings ConfigMap" settings_only
check "kubeletStats=false grants nothing on the kubelets" no_kubelet_stats
check "the settings ConfigMap is created empty and named to the server" settings_configmap
check "a release named after the chart still gets a valid Service name" service_name
check "clusterName is required" name_required
check "an empty list of API server ports is refused" no_open_egress
check "no DNS traffic is allowed out" no_dns_egress
check "API server addresses narrow the egress rule" api_server_addresses
check "it answers to its Service's names and the names in values" host_names
check "an override that starts with a digit still gets a valid name" digit_override
check "the Service listens on 8081" exposed_port
check "moving tags are pulled on every start and rolled on every upgrade; releases, sha- tags and digests once" pull_policy
check "a LoadBalancer Service carries its class, address list, node port and annotations" lb_service
check "nothing may reach a ClusterIP Service through the network policy" closed_by_default
check "an exposed Service is reachable through the network policy" open_when_exposed
exit "$fail"
