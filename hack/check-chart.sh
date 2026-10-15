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
  --set serviceAccount.name=viewer | tee "$WORK/chart-nopolicy.yaml" | validate
echo "--- without the kubelets' stats"
render console --set clusterName=ci --set kubeletStats=false | tee "$WORK/chart-nostats.yaml" | validate
echo "--- without reading Secrets"
render console --set clusterName=ci --set secrets.read=false | tee "$WORK/chart-nosecrets.yaml" | validate
render console --set clusterName=ci --set connections.store=memory | tee "$WORK/chart-memory.yaml" | validate
render console --set clusterName=ci --set preferences.perUser=false | tee "$WORK/chart-noprefs.yaml" | validate
render console --set clusterName=ci --set sherlock.existingSecret=claude --set sherlock.egress=false --set connections.store=memory | tee "$WORK/chart-sherlock.yaml" | validate
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
echo "--- OIDC sign-in, an Ingress with TLS, named administrators and an audit webhook (P3-01, P3-03, P3-10, P3-11)"
render console --set clusterName=ci --set auth.mode=oidc --set auth.oidc.issuer=https://login.example/realms/data \
  --set auth.oidc.clientID=console --set auth.oidc.clientSecret=hunter22 --set auth.oidc.providerName=Keycloak \
  --set 'auth.adminGroups[0]=platform-admins' --set 'auth.admins[0]=ops@corp.example' \
  --set ingress.enabled=true --set ingress.className=nginx --set ingress.host=console.example.com --set ingress.tls.enabled=true --set ingress.tls.secretName=console-tls \
  --set audit.webhook.url=https://hooks.example/221b --set audit.webhook.token=hook-token |
  tee "$WORK/chart-oidc.yaml" | validate
echo "--- local accounts, from values and from an existing Secret; sign-in off"
render console --set clusterName=ci --set auth.mode=local --set 'auth.local.users[0].email=ops@corp.example' \
  --set 'auth.local.users[0].passwordHash=pbkdf2-sha256$600000$c2FsdA$aGFzaA' --set 'auth.admins[0]=ops@corp.example' | tee "$WORK/chart-local.yaml" | validate
render console --set clusterName=ci --set auth.mode=local --set auth.local.existingSecret=my-accounts | tee "$WORK/chart-local-existing.yaml" | validate
render console --set clusterName=ci --set auth.mode=none | tee "$WORK/chart-noauth.yaml" | validate
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
  [ "$(grep -E '^kind: ' "$out" | sort | tr '\n' ' ')" = "kind: ClusterRole kind: ClusterRoleBinding kind: ConfigMap kind: Deployment kind: NetworkPolicy kind: Role kind: RoleBinding kind: Secret kind: Service kind: ServiceAccount " ]
}
# Sign-in: local accounts by default, with the (empty) account list in a
# Secret mounted as a file; OIDC with its client secret from a Secret;
# the probes and metrics stay open to the kubelet and Prometheus.
local_accounts() {
  grep -A1 'name: AUTH_MODE' "$out" | grep -q '"local"' && grep -A1 'name: LOCAL_USERS_FILE' "$out" | grep -q '/etc/221b/auth/users.yaml' &&
    grep -q 'secretName: "console-221b-auth"' "$out" && grep -q 'users.yaml: |' "$out" &&
    grep -q 'email: ops@corp.example' "$WORK/chart-local.yaml" && grep -q 'passwordHash: pbkdf2-sha256' "$WORK/chart-local.yaml" &&
    grep -A1 'name: ADMINS' "$WORK/chart-local.yaml" | grep -q '"ops@corp.example"' &&
    grep -q 'secretName: "my-accounts"' "$WORK/chart-local-existing.yaml" && ! grep -q '^kind: Secret' "$WORK/chart-local-existing.yaml" &&
    ! grep -q 'LOCAL_USERS_FILE' "$WORK/chart-noauth.yaml" && grep -A1 'name: AUTH_MODE' "$WORK/chart-noauth.yaml" | grep -q '"none"' && ! grep -q '^kind: Secret' "$WORK/chart-noauth.yaml"
}
oidc_signin() {
  local o=$WORK/chart-oidc.yaml
  grep -A1 'name: AUTH_MODE' "$o" | grep -q '"oidc"' && grep -A1 'name: OIDC_ISSUER' "$o" | grep -q 'https://login.example/realms/data' &&
    grep -A5 'name: OIDC_CLIENT_SECRET' "$o" | grep -q 'key: client-secret' && grep -q 'client-secret: "hunter22"' "$o" && ! grep -q 'value: "hunter22"' "$o" &&
    grep -A1 'name: ADMIN_GROUPS' "$o" | grep -q '"platform-admins"' && grep -A1 'name: OIDC_PROVIDER_NAME' "$o" | grep -q '"Keycloak"' &&
    ! grep -q 'LOCAL_USERS_FILE' "$o"
}
oidc_requires_client() {
  ! render console --set clusterName=ci --set auth.mode=oidc --set auth.oidc.issuer=https://login.example > /dev/null 2>&1 &&
    ! render console --set clusterName=ci --set auth.mode=magic > /dev/null 2>&1
}
audit_webhook() {
  local o=$WORK/chart-oidc.yaml
  grep -A1 'name: AUDIT_WEBHOOK_URL' "$o" | grep -q 'https://hooks.example/221b' && grep -A5 'name: AUDIT_WEBHOOK_TOKEN' "$o" | grep -q 'key: token' &&
    grep -q 'token: "hook-token"' "$o" && ! grep -q 'AUDIT_WEBHOOK' "$out"
}
ingress_tls() {
  local o=$WORK/chart-oidc.yaml
  grep -q '^kind: Ingress' "$o" && grep -q 'ingressClassName: "nginx"' "$o" && grep -q 'secretName: "console-tls"' "$o" &&
    grep -q 'host: "console.example.com"' "$o" && grep -A1 'name: HTTPS_REDIRECT' "$o" | grep -q '"true"' &&
    grep -A1 'name: ALLOWED_HOSTS' "$o" | grep -q ',console.example.com"' && ! grep -q 'HTTPS_REDIRECT' "$out" &&
    ! render console --set clusterName=ci --set ingress.enabled=true --set ingress.host= > /dev/null 2>&1
}
# Outbound traffic: only the API server by default; with OIDC or a webhook
# also DNS and HTTPS.
outbound() {
  grep -c 'port: 53' "$out" | grep -q 2 && grep -c 'port: 443' "$out" | grep -q 2 && grep -c 'port: 443' "$WORK/chart-sherlock.yaml" | grep -q 1 && grep -c 'port: 443' "$WORK/chart-oidc.yaml" | grep -q 2
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
# What it reads, plus, with sign-in, the people it reads as (users and
# groups, impersonated) and the reviews of their access it creates.
WANT_READ='admissionregistration.k8s.io/mutatingwebhookconfigurations admissionregistration.k8s.io/validatingwebhookconfigurations apps/controllerrevisions apps/daemonsets apps/deployments apps/replicasets apps/statefulsets authorization.k8s.io/subjectaccessreviews autoscaling/horizontalpodautoscalers batch/cronjobs batch/jobs configmaps coordination.k8s.io/leases discovery.k8s.io/endpointslices endpoints events groups limitranges metrics.k8s.io/nodes metrics.k8s.io/pods namespaces networking.k8s.io/ingressclasses networking.k8s.io/ingresses networking.k8s.io/networkpolicies nodes nodes/proxy persistentvolumeclaims pods pods/log policy/poddisruptionbudgets rbac.authorization.k8s.io/clusterrolebindings rbac.authorization.k8s.io/clusterroles rbac.authorization.k8s.io/rolebindings rbac.authorization.k8s.io/roles resourcequotas scheduling.k8s.io/priorityclasses secrets serviceaccounts services users'
read_only_resources() { [ "$(granted "$out")" = "$WANT_READ" ]; }
# Read-only, but for the access reviews it creates to ask what a person may see.
read_only_verbs() {
  ! cluster_role | awk '/resources:/ { res = $0 } /verbs:/ && res !~ /subjectaccessreviews/ { print }' | grep -q -E 'create|update|patch|delete|\*'
}
# With sign-in, it may impersonate people and review their access; with
# sign-in off there is nobody to read as, and neither is granted.
impersonation() {
  cluster_role | grep -A1 '"users", "groups"' | grep -q 'verbs: \["impersonate"\]' &&
    cluster_role | grep -A1 '"subjectaccessreviews"' | grep -q 'verbs: \["create"\]' &&
    grep -A1 'name: IMPERSONATE_USER_PREFIX' "$out" | grep -q '""' &&
    ! cluster_role "$WORK/chart-noauth.yaml" | grep -q -E 'impersonate|subjectaccessreviews' &&
    ! grep -q 'IMPERSONATE_USER_PREFIX' "$WORK/chart-noauth.yaml"
}
# Logs and metrics are read, never listed or watched more than needed.
log_verbs() { cluster_role | grep -A1 '"pods/log"' | grep -q 'verbs: \["get"\]'; }
secrets_optional() {
  cluster_role | grep -A1 '"secrets"' | grep -q 'verbs: \["get", "list", "watch"\]' && grep -A1 'name: SECRETS_READ' "$out" | grep -q '"true"' &&
    [ "$(granted "$WORK/chart-nosecrets.yaml")" = "${WANT_READ/ secrets/}" ] && ! cluster_role "$WORK/chart-nosecrets.yaml" | grep -q '"secrets"' &&
    grep -A1 'name: SECRETS_READ' "$WORK/chart-nosecrets.yaml" | grep -q '"false"'
}
user_prefs() {
  grep -A1 'name: USER_PREFS' "$out" | grep -q '"true"' && grep -A1 'name: USER_PREFS' "$WORK/chart-noprefs.yaml" | grep -q '"false"'
}
# Sherlock (Phase 5): the Claude API's address; its key in the release's
# own Secret, or from a Secret the chart is given, never in the values.
sherlock_key() {
  grep -A1 'name: SHERLOCK_API_URL' "$out" | grep -q '"https://api.anthropic.com"' &&
    grep -A1 'name: SHERLOCK_SECRET' "$out" | grep -q '/console-221b-sherlock"' &&
    ! grep -q 'name: SHERLOCK_API_KEY' "$out" &&
    grep -A4 'name: SHERLOCK_API_KEY' "$WORK/chart-sherlock.yaml" | grep -q 'name: "claude"' &&
    ! grep -q 'name: SHERLOCK_SECRET' "$WORK/chart-sherlock.yaml"
}
settings_only() {
  settings_role | grep -q 'resources: \["configmaps"\]' && settings_role | grep -q 'resourceNames: \["console-221b"\]' &&
    [ "$(settings_role | grep -E 'verbs:' | tr -d ' ' | tr '\n' ' ')" = 'verbs:["get","patch"] verbs:["get","list","create","update","delete"] ' ] &&
    [ "$(grep -E 'verbs:' "$out" | grep -c -E 'update|delete|\*')" = 1 ]
}
# The connections to other clusters: Secrets in the release namespace, by
# default; in memory on request, and then nothing is granted on Secrets.
connections_store() {
  settings_role | grep -A1 'resources: \["secrets"\]' | grep -q 'verbs: \["get", "list", "create", "update", "delete"\]' &&
    grep -A1 'name: CONNECTIONS_SECRETS' "$out" | grep -q '"consoles/console-221b"' &&
    ! sed -n '/^kind: Role$/,/^---/p' "$WORK/chart-memory.yaml" | grep -q 'secrets' &&
    ! grep -q 'CONNECTIONS_SECRETS' "$WORK/chart-memory.yaml" &&
    ! render console --set clusterName=ci --set connections.store=disk > /dev/null 2>&1
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
dns_egress() { grep -B1 'protocol: UDP' "$out" | grep -q 'port: 53' && ! grep -q 'port: 53' "$WORK/chart-nopolicy.yaml"; }
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
check "the ClusterRole's verbs are read-only, but for the access reviews" read_only_verbs
check "with sign-in it impersonates people and reviews their access; without, neither" impersonation
check "pod logs are only read" log_verbs
check "secrets.read=false grants nothing on Secrets and tells the server" secrets_optional
check "it writes only its own settings ConfigMap and the connection Secrets in its namespace" settings_only
check "connections are kept in Secrets in the release namespace, or in memory on request" connections_store
check "kubeletStats=false grants nothing on the kubelets" no_kubelet_stats
check "the settings ConfigMap is created empty and named to the server" settings_configmap
check "each signed-in person's preferences are kept with their account, unless preferences.perUser=false" user_prefs
check "Sherlock's API key is kept in the release's Secret or taken from sherlock.existingSecret, never in the values" sherlock_key
check "a release named after the chart still gets a valid Service name" service_name
check "clusterName is required" name_required
check "an empty list of API server ports is refused" no_open_egress
check "DNS is allowed out, for the clusters given by name" dns_egress
check "outbound traffic: API servers and DNS only, or also HTTPS for OIDC, a webhook or Sherlock" outbound
check "local accounts are mounted from a Secret, or an existing one; none with sign-in off" local_accounts
check "OIDC sign-in takes its client secret from a Secret and names the administrators" oidc_signin
check "OIDC without a client secret, and an unknown mode, are refused" oidc_requires_client
check "the audit webhook and its token" audit_webhook
check "an Ingress with TLS redirects HTTP, allows its host and needs a host" ingress_tls
check "API server addresses narrow the egress rule" api_server_addresses
check "it answers to its Service's names and the names in values" host_names
check "an override that starts with a digit still gets a valid name" digit_override
check "the Service listens on 8081" exposed_port
check "moving tags are pulled on every start and rolled on every upgrade; releases, sha- tags and digests once" pull_policy
check "a LoadBalancer Service carries its class, address list, node port and annotations" lb_service
check "nothing may reach a ClusterIP Service through the network policy" closed_by_default
check "an exposed Service is reachable through the network policy" open_when_exposed
exit "$fail"
