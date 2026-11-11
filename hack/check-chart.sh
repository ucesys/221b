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
render k8s-console --set clusterName=ci | tee "$WORK/chart-default.yaml" | validate
echo "--- NodePort, no NetworkPolicy, no RBAC, an existing ServiceAccount"
render k8s-console --set clusterName=ci --set service.type=NodePort --set service.nodePort=30080 \
  --set networkPolicy.enabled=false --set rbac.create=false --set serviceAccount.create=false \
  --set serviceAccount.name=viewer | tee "$WORK/chart-nopolicy.yaml" | validate
echo "--- without the kubelets' stats"
render k8s-console --set clusterName=ci --set kubeletStats=false | tee "$WORK/chart-nostats.yaml" | validate
render k8s-console --set clusterName=ci --set cases.cleanStart.token=2026-11-07 --set cases.cleanStart.dryRun=true | tee "$WORK/chart-clean.yaml" | validate
echo "--- without reading Secrets"
render k8s-console --set clusterName=ci --set secrets.read=false | tee "$WORK/chart-nosecrets.yaml" | validate
render k8s-console --set clusterName=ci --set connections.store=memory | tee "$WORK/chart-memory.yaml" | validate
render k8s-console --set clusterName=ci --set sherlock.existingSecret=claude --set sherlock.egress=false --set connections.store=memory | tee "$WORK/chart-sherlock.yaml" | validate
render k8s-console --set clusterName=ci --set sherlock.provider.kind=bedrock --set sherlock.provider.region=eu-west-1 --set sherlock.lockProvider=true | tee "$WORK/chart-provider.yaml" | validate
echo "--- allowed ingress peers, API server addresses, extra host names and a private registry"
render k8s-console --set clusterName=ci --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.team=platform' \
  --set 'networkPolicy.apiServerCIDRs[0]=10.0.0.10/32' --set 'allowedHosts[0]=console.example.com' \
  --set 'imagePullSecrets[0].name=ghcr' | tee "$WORK/chart-options.yaml" | validate
echo "--- exposed with a LoadBalancer on every node"
render k8s-console --set clusterName=ci --set service.type=LoadBalancer --set service.nodePort=30081 \
  --set 'service.externalIPs[0]=10.0.0.21' --set 'service.externalIPs[1]=10.0.0.22' \
  --set service.loadBalancerClass=metallb --set 'service.annotations.example\.com/pool=office' |
  tee "$WORK/chart-lb.yaml" | validate
echo "--- a release name and an override that start with a digit"
render 221b --set clusterName=ci --set fullnameOverride=2b | validate
echo "--- OIDC sign-in, an Ingress with TLS, named administrators and an audit webhook (P3-01, P3-03, P3-10, P3-11)"
render k8s-console --set clusterName=ci --set auth.mode=oidc --set auth.oidc.issuer=https://login.example/realms/data \
  --set auth.oidc.clientID=console --set auth.oidc.clientSecret=hunter22 --set auth.oidc.providerName=Keycloak \
  --set 'auth.adminGroups[0]=platform-admins' --set 'auth.admins[0]=ops@corp.example' \
  --set ingress.enabled=true --set ingress.className=nginx --set ingress.host=console.example.com --set ingress.tls.enabled=true --set ingress.tls.secretName=console-tls \
  --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.kubernetes\.io/metadata\.name=ingress-nginx' \
  --set audit.webhook.url=https://hooks.example/221b --set audit.webhook.token=hook-token |
  tee "$WORK/chart-oidc.yaml" | validate
echo "--- local accounts, from values and from an existing Secret; sign-in off"
render k8s-console --set clusterName=ci --set auth.mode=local --set 'auth.local.users[0].email=ops@corp.example' \
  --set 'auth.local.users[0].passwordHash=pbkdf2-sha256$600000$c2FsdA$aGFzaA' --set 'auth.admins[0]=ops@corp.example' | tee "$WORK/chart-local.yaml" | validate
render k8s-console --set clusterName=ci --set auth.mode=local --set auth.local.existingSecret=my-accounts | tee "$WORK/chart-local-existing.yaml" | validate
render k8s-console --set clusterName=ci --set auth.mode=none | tee "$WORK/chart-noauth.yaml" | validate
echo "--- the four roles (Phase 6.1): local groups, LDAP, OIDC role lists, both definitions read-only"
render k8s-console --set clusterName=ci -f "$ROOT/hack/chart-values/groups.yaml" | tee "$WORK/chart-groups.yaml" | validate
render k8s-console --set clusterName=ci -f "$ROOT/hack/chart-values/ldap.yaml" | tee "$WORK/chart-ldap.yaml" | validate
render k8s-console --set clusterName=ci --set rbac.roles.admin.template=readOnly --set rbac.roles.superAdmin.template=readOnly \
  --set rbac.enforce=strict | tee "$WORK/chart-protected.yaml" | validate
render k8s-console --set clusterName=ci --set auth.mode=oidc --set auth.oidc.issuer=https://login.example/realms/data \
  --set auth.oidc.clientID=console --set auth.oidc.clientSecret=hunter22 --set auth.kubernetes.groupPrefix='oidc:' \
  --set 'auth.oidc.roles.viewer[0].group=devs' --set 'auth.oidc.roles.viewer[0].namespaces[0]=web' | tee "$WORK/chart-oidc-roles.yaml" | validate
echo "--- Watson's changes in the local cluster, with sign-in off and on (Phase 6)"
render k8s-console --set clusterName=ci --set auth.mode=none --set localCluster.mode=write | tee "$WORK/chart-write.yaml" | validate
render k8s-console --set clusterName=ci --set localCluster.mode=write | tee "$WORK/chart-write-signin.yaml" | validate
echo "--- several replicas (P7-02)"
render k8s-console --set clusterName=ci --set replicaCount=2 | tee "$WORK/chart-ha.yaml" | validate
render k8s-console --set clusterName=ci --set replicaCount=2 --set antiAffinity=soft | tee "$WORK/chart-ha-soft.yaml" | validate
render k8s-console --set clusterName=ci --set replicaCount=2 --set replicas.warmStandby=false | tee "$WORK/chart-ha-cold.yaml" | validate
render k8s-console --set clusterName=ci --set replicaCount=2 --set antiAffinity=hard \
  --set 'affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key=kubernetes.io/os' \
  --set 'affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator=In' \
  --set 'affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].values[0]=linux' \
  | tee "$WORK/chart-ha-hard.yaml" | validate
render k8s-console --set clusterName=ci --set replicaCount=3 --set 'topologySpreadConstraints[0].maxSkew=1' \
  --set 'topologySpreadConstraints[0].topologyKey=topology.kubernetes.io/zone' --set 'topologySpreadConstraints[0].whenUnsatisfiable=DoNotSchedule' \
  --set 'topologySpreadConstraints[0].labelSelector.matchLabels.app=x' | tee "$WORK/chart-ha-zones.yaml" | validate
echo "--- the Toolbox (Phase 9): off, transcripts on a volume, Admin's shells, no node shells"
render k8s-console --set clusterName=ci --set toolbox.enabled=false | tee "$WORK/chart-notoolbox.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.transcripts=pvc --set toolbox.transcriptsVolume.storageClass=fast | tee "$WORK/chart-transcripts.yaml" | validate
render k8s-console --set clusterName=ci --set rbac.roles.admin.exec=true --set toolbox.nodeShell.enabled=false | tee "$WORK/chart-toolbox-exec.yaml" | validate
echo "--- SSH to nodes (Phase 11): off, with another login user"
render k8s-console --set clusterName=ci --set toolbox.ssh.enabled=false --set toolbox.ssh.loginUser=ops | tee "$WORK/chart-ssh-off.yaml" | validate
render k8s-console --set clusterName=ci --set sherlock.egress=false --set 'networkPolicy.outboundCIDRs[0]=140.82.112.0/20' | tee "$WORK/chart-outbound.yaml" | validate
render k8s-console --set clusterName=ci --set 'backup.s3.egressCIDRs[0]=10.20.0.0/16' --set 'backup.s3.egressPorts={9000}' | tee "$WORK/chart-backup-s3.yaml" | validate
render k8s-console --set clusterName=ci --set backup=null | tee "$WORK/chart-backup-reuse.yaml" | validate
# --- Knowledge (Phase 12): its egress, and values from before it.
render k8s-console --set clusterName=ci --set 'knowledge.egressCIDRs[0]=10.30.0.0/16' --set 'knowledge.egressPorts={7990}' --set knowledge.consoleURL=https://221b.example.com | tee "$WORK/chart-knowledge.yaml" | validate
render k8s-console --set clusterName=ci --set knowledge=null | tee "$WORK/chart-knowledge-reuse.yaml" | validate
render k8s-console --set clusterName=ci --set knowledge.configMapStore.enabled=false | tee "$WORK/chart-knowledge-nowiki.yaml" | validate
# --- Helm repositories (Phase 12, wave 5): their egress, and values from before it.
render k8s-console --set clusterName=ci --set 'helmRepositories.egressCIDRs[0]=10.40.0.0/16' --set 'helmRepositories.egressPorts={8443}' | tee "$WORK/chart-helm-repos.yaml" | validate
render k8s-console --set clusterName=ci --set helmRepositories=null | tee "$WORK/chart-helm-repos-reuse.yaml" | validate
render k8s-console --set clusterName=ci --set 'service.externalIPs[0]=10.0.0.21' | tee "$WORK/chart-external.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.transcripts=pvc | tee "$WORK/chart-pvc.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.ssh.enabled=true --set rbac.roles.superAdmin.template=readOnly --set rbac.roles.admin.template=full | tee "$WORK/chart-ssh-readonly.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.ssh=null | tee "$WORK/chart-ssh-reuse.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.ssh.enabled=true | tee "$WORK/chart-ssh-on.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.ssh.enabled=true --set toolbox.ssh.port=2222 --set 'networkPolicy.nodeCIDRs[0]=192.168.1.0/24' | tee "$WORK/chart-ssh-cidr.yaml" | validate
render k8s-console --set clusterName=ci --set toolbox.ssh.enabled=true --set 'networkPolicy.hostPorts={2200,22}' --set 'networkPolicy.nodeCIDRs[0]=192.168.1.0/24' | tee "$WORK/chart-ssh-hosts.yaml" | validate
echo "--- image tags that move, tags that do not, a digest and an explicit pull policy"
render k8s-console --set clusterName=ci --set image.tag=pr-7 | tee "$WORK/chart-moving.yaml" | validate
render k8s-console --set clusterName=ci --set image.tag=sha-d548f3953839 | tee "$WORK/chart-sha.yaml" | validate
render k8s-console --set clusterName=ci --set image.digest=sha256:ce75aee9b9c2cde129bb8796ece11c3004b4eaba1d9afceed8b11e6996f0665d | tee "$WORK/chart-digest.yaml" | validate
render k8s-console --set clusterName=ci --set image.tag=main --set image.pullPolicy=IfNotPresent | tee "$WORK/chart-policy.yaml" | validate

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
netpol() { sed -n '/^kind: NetworkPolicy$/,/^---/p' "${1:-$out}"; }
# SSH to nodes (Phase 11): the toolbox config carries toolbox.ssh.
ssh_values() {
  grep -q '"ssh": {' "$WORK/chart-toolbox-exec.yaml" && grep -q '"loginUser": "root"' "$WORK/chart-toolbox-exec.yaml" && grep -q '"port": 22' "$WORK/chart-toolbox-exec.yaml" &&
    sed -n '/"ssh": {/,/}/p' "$WORK/chart-ssh-off.yaml" | has '"enabled": false' && grep -q '"loginUser": "ops"' "$WORK/chart-ssh-off.yaml"
}
# A read-only SuperAdmin definition turns SSH off with the node shell; values from an older release have no toolbox.ssh and SSH stays off.
ssh_gated() {
    sed -n '/"ssh": {/,/}/p' "$WORK/chart-ssh-readonly.yaml" | has '"enabled": false' &&
    ! netpol "$WORK/chart-ssh-readonly.yaml" | has 'port: 22$' &&
    sed -n '/"ssh": {/,/}/p' "$WORK/chart-ssh-reuse.yaml" | has '"enabled": false' &&
    ! netpol "$WORK/chart-ssh-reuse.yaml" | has 'port: 22$'
}
# The network policy lets the console reach sshd on the nodes, only with SSH on, narrowed to the nodes' CIDRs when given.
ssh_egress() {
  netpol "$WORK/chart-ssh-on.yaml" | has 'port: 22$' &&
    ! netpol "$out" | has 'port: 22$' &&
    ! netpol "$WORK/chart-ssh-off.yaml" | has 'port: 22$' &&
    netpol "$WORK/chart-ssh-cidr.yaml" | grep -A4 'port: 2222$' | has 'cidr: "192.168.1.0/24"'
}
# The scheduled backups' S3 endpoint (Phase 12): a rule only when its
# CIDRs are listed, on its ports; values from an older release have none.
backup_egress() {
    netpol "$WORK/chart-backup-s3.yaml" | grep -A6 'port: 9000$' | has 'cidr: "10.20.0.0/16"' &&
    ! netpol "$WORK/chart-default.yaml" | has 'port: 9000$' &&
    ! netpol "$WORK/chart-backup-reuse.yaml" | has 'port: 9000$'
}
# --- Helm repositories (Phase 12, wave 5): the network policy reaches
# them only at helmRepositories.egressCIDRs, a rule only when listed.
helm_repos_egress() {
    netpol "$WORK/chart-helm-repos.yaml" | grep -A6 'port: 8443$' | has 'cidr: "10.40.0.0/16"' &&
    ! netpol "$WORK/chart-default.yaml" | has 'port: 8443$' &&
    ! netpol "$WORK/chart-helm-repos-reuse.yaml" | has 'port: 8443$'
}
# Mycroft's knowledge (Phase 12): its records in a ConfigMap of its own,
# named in the env and the Role; the wiki's local copy in an emptyDir;
# its sources and store reached at knowledge.egressCIDRs, a rule only when
# listed; values from an older release take the defaults.
knowledge_values() {
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-knowledge$' &&
    grep -A1 'name: KNOWLEDGE_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-knowledge"' &&
    grep -A1 'name: KNOWLEDGE_DIR' "$out" | has '/var/lib/221b/knowledge' &&
    grep -A1 'name: KNOWLEDGE_REFRESH_GIT' "$out" | has '"15m"' &&
    grep -A2 'name: knowledge$' "$out" | has 'sizeLimit: "512Mi"' &&
    grep -B1 'mountPath: /var/lib/221b/knowledge' "$out" | has 'name: knowledge' &&
    ! grep -q 'name: CONSOLE_URL' "$out" &&
    grep -A1 'name: CONSOLE_URL' "$WORK/chart-knowledge.yaml" | has '"https://221b.example.com"' &&
    grep -A1 'name: KNOWLEDGE_REFRESH_S3' "$WORK/chart-knowledge-reuse.yaml" | has '"5m"' &&
    netpol "$WORK/chart-knowledge.yaml" | grep -A6 'port: 7990$' | has 'cidr: "10.30.0.0/16"' &&
    ! netpol "$out" | has 'cidr: "10.30.0.0/16"' &&
    ! netpol "$WORK/chart-knowledge-reuse.yaml" | has 'port: 7990$'
}
# The temporary wiki store (CF-57): its ConfigMap, named in the env and the
# Role, on by default and with values from an older release; none with
# knowledge.configMapStore.enabled=false.
wiki_configmap() {
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-wiki$' &&
    grep -A1 'name: WIKI_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-wiki"' &&
    grep -A1 'name: WIKI_CONFIGMAP' "$WORK/chart-knowledge-reuse.yaml" | has '"consoles/k8s-console-221b-wiki"' &&
    ! grep -q 'k8s-console-221b-wiki' "$WORK/chart-knowledge-nowiki.yaml" &&
    ! grep -q 'name: WIKI_CONFIGMAP' "$WORK/chart-knowledge-nowiki.yaml"
}
# Hosts outside Kubernetes (Phase 12) are reached on the same rule: their
# other SSH ports are added once, to the same CIDRs; with SSH off, none.
host_ports() {
  render k8s-console --set clusterName=ci --set 'networkPolicy.hostPorts={2200}' > "$WORK/chart-hosts-off.yaml"
  [ "$(netpol "$WORK/chart-ssh-hosts.yaml" | grep -c 'port: 22$')" = 1 ] &&
    netpol "$WORK/chart-ssh-hosts.yaml" | grep -A6 'port: 22$' | has 'port: 2200$' &&
    netpol "$WORK/chart-ssh-hosts.yaml" | grep -A6 'port: 2200$' | has 'cidr: "192.168.1.0/24"' &&
    ! netpol "$WORK/chart-hosts-off.yaml" | has 'port: 2200$'
}
# The Secret with the local accounts, the OIDC client secret and such; the
# replicas' proxy token is a Secret of its own, always there.
automation_configmap() {
  # On by default: a ConfigMap of its own, named in the env and in the
  # settings Role; off, neither.
  local off
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-automation$' &&
    grep -A1 'name: AUTOMATION_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-automation"' &&
    grep -q '"k8s-console-221b-automation"' "$out" || return 1
  off=$(render k8s-console --set clusterName=ci --set automation.enabled=false)
  ! grep -q 'k8s-console-221b-automation' <<<"$off" && grep -A1 'name: AUTOMATION$' <<<"$off" | has '"false"'
}
issues_values() {
  # On by default (Phase 12): a ConfigMap of its own, named in the env and
  # in the settings Role, and who reports and triages; reporter groups and
  # local reporter groups reach the auth file and bind nothing; off, the
  # report page is off and the ConfigMap stays, for the cases (wave 5).
  local off reporters
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-issues$' &&
    grep -A1 'name: ISSUES_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-issues"' &&
    grep -A1 'name: ISSUES_REPORT_ROLES' "$out" | has '"superAdmin,superViewer,admin,viewer"' &&
    grep -A1 'name: ISSUES_OPERATOR_ROLES' "$out" | has '"superAdmin,admin"' || return 1
  off=$(render k8s-console --set clusterName=ci --set issues.enabled=false)
  sed -n '/^kind: ConfigMap$/,/^---/p' <<<"$off" | has '^  name: k8s-console-221b-issues$' && grep -A1 'name: ISSUES_CONFIGMAP' <<<"$off" | has '"consoles/k8s-console-221b-issues"' &&
    grep -q '"k8s-console-221b-issues"' <<<"$off" && grep -A1 'name: ISSUES$' <<<"$off" | has '"false"' || return 1
  reporters=$(render k8s-console --set clusterName=ci --set 'issues.reporterGroups[0]=staff' \
    --set 'auth.local.groups[0].name=helpdesk' --set 'auth.local.groups[0].role=reporter')
  has '"reporterGroups": \[' <<<"$reporters" && has '"role": "reporter"' <<<"$reporters" && ! grep -q 'RoleBinding.*helpdesk\|221b-reporter-' <<<"$reporters" || return 1
  ! render k8s-console --set clusterName=ci --set 'issues.operatorRoles[0]=reporter' >/dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set 'auth.local.groups[0].name=helpdesk' --set 'auth.local.groups[0].role=reporter' --set 'auth.local.groups[0].namespaces[0]=web' >/dev/null 2>&1
}
# The AI agents (Phase 12): their records in a ConfigMap of their own, named
# in the env and the Role; the baselines from values in another, mounted,
# with a checksum that restarts the console when they change.
ai_configmaps() {
  local with
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-ai$' &&
    grep -A1 'name: AI_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-ai"' &&
    grep -A1 'name: AGENTS_DIR' "$out" | has '/etc/221b/agents' || return 1
  with=$(render k8s-console --set clusterName=ci --set agents.troubleshooter.instructions='Cite our runbooks.')
  grep -q '^  troubleshooter.md: "Cite our runbooks."$' <<<"$with" &&
    [ "$(grep 'checksum/agents' <<<"$with")" != "$(grep 'checksum/agents' "$out")" ] &&
    ! render k8s-console --set clusterName=ci --set agents.watson.instructions=x > /dev/null 2>&1
}
# The agents' proposed changes (Phase 12): their records in a ConfigMap of
# their own, <release>-proposals since CF-104, named in the env and the
# Role, approved by cases.approverRoles. <release>-cases, where builds
# before kept them, is still made, for the console's one copy at start,
# and no longer named in the env or the Role.
cases_values() {
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-proposals$' &&
    sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-cases$' &&
    grep -A1 'name: PROPOSALS_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-proposals"' &&
    ! grep -q 'name: CASES_CONFIGMAP' "$out" &&
    grep -A1 'name: CASES_APPROVER_ROLES' "$out" | has '"superAdmin,admin"' || return 1
  grep -A1 'name: CASES_APPROVER_ROLES' <<<"$(render k8s-console --set clusterName=ci --set 'cases.approverRoles[0]=viewer')" | has '"viewer"' &&
    ! render k8s-console --set clusterName=ci --set 'cases.approverRoles[0]=reporter' >/dev/null 2>&1
}
# --- Jobs (Phase 12): their records in a ConfigMap of their own, named in
# the env and the Role; the time zone of new schedules only when set.
jobs_configmap() {
  local with
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-runs$' &&
    grep -A1 'name: RUNS_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-runs"' &&
    grep -q '"k8s-console-221b-runs"' "$out" && ! grep -q 'name: JOBS_TIME_ZONE' "$out" || return 1
  with=$(render k8s-console --set clusterName=ci --set jobs.timeZone=Europe/Berlin)
  grep -A1 'name: JOBS_TIME_ZONE' <<<"$with" | has '"Europe/Berlin"'
}
# --- Case work (Phase 12): the cases in a ConfigMap of their own, named
# in the env and the Role.
casework_configmap() {
  sed -n '/^kind: ConfigMap$/,/^---/p' "$out" | has '^  name: k8s-console-221b-casework$' &&
    grep -A1 'name: CASEWORK_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b-casework"' &&
    grep -q '"k8s-console-221b-casework"' "$out"
}
# --- One flow for findings and cases (Phase 12, wave 6): each case is a
# ConfigMap the console makes, <release>-case-<n>, named by a prefix and
# the release in the env; the Role grants ConfigMaps in the namespace only
# (a Role, never the ClusterRole), without resourceNames, since the names
# do not exist yet; the clean start's token and dry run reach the env.
case_configmaps() {
  grep -A1 'name: CASE_CONFIGMAPS' "$out" | has '"consoles/k8s-console-221b-case-"' &&
    grep -A1 'name: CASE_INSTANCE' "$out" | has '"k8s-console"' &&
    settings_role | grep -B2 'verbs: \["create", "list", "get", "update", "patch", "delete"\]' | has 'resources: \["configmaps"\]' &&
    ! settings_role | grep -A1 'verbs: \["create", "list", "get", "update", "patch", "delete"\]' | has 'resourceNames' &&
    grep -A1 'name: CASES_CLEAN_START_TOKEN' "$out" | has 'value: ""' &&
    grep -A1 'name: CASES_CLEAN_START_DRY_RUN' "$out" | has '"false"' &&
    grep -A1 'name: CASES_CLEAN_START_TOKEN' "$WORK/chart-clean.yaml" | has '"2026-11-07"' &&
    grep -A1 'name: CASES_CLEAN_START_DRY_RUN' "$WORK/chart-clean.yaml" | has '"true"'
}
auth_secret() { sed -n '/^kind: Secret$/,/^---/p' "$1" | has '^  name: k8s-console-221b-auth$'; }
kinds() {
  [ "$(grep -E '^kind: ' "$out" | sort | uniq -c | awk '{ printf "%s=%s ", $3, $1 }')" = "ClusterRole=6 ClusterRoleBinding=4 ConfigMap=16 Deployment=1 Lease=1 Namespace=1 NetworkPolicy=2 Role=1 RoleBinding=1 Secret=2 Service=1 ServiceAccount=1 " ]
}
# Sign-in: local accounts by default, with the (empty) account list in a
# Secret mounted as a file; OIDC with its client secret from a Secret;
# the probes and metrics stay open to the kubelet and Prometheus.
local_accounts() {
  grep -A1 'name: AUTH_MODE' "$out" | has '"local"' && grep -A1 'name: LOCAL_USERS_FILE' "$out" | has '/etc/221b/auth/users.yaml' &&
    grep -q 'secretName: "k8s-console-221b-auth"' "$out" && grep -q 'users.yaml: |' "$out" &&
    grep -q 'email: ops@corp.example' "$WORK/chart-local.yaml" && grep -q 'passwordHash: pbkdf2-sha256' "$WORK/chart-local.yaml" &&
    grep -A1 'name: ADMINS' "$WORK/chart-local.yaml" | has '"ops@corp.example"' &&
    grep -q 'secretName: "my-accounts"' "$WORK/chart-local-existing.yaml" && ! auth_secret "$WORK/chart-local-existing.yaml" &&
    ! grep -q 'LOCAL_USERS_FILE' "$WORK/chart-noauth.yaml" && grep -A1 'name: AUTH_MODE' "$WORK/chart-noauth.yaml" | has '"none"' && ! auth_secret "$WORK/chart-noauth.yaml" &&
    grep -A1 'name: ANONYMOUS_ROLE' "$WORK/chart-noauth.yaml" | has '"viewer"' && ! grep -q 'ANONYMOUS_ROLE' "$WORK/chart-local.yaml"
}
oidc_signin() {
  local o=$WORK/chart-oidc.yaml
  grep -A1 'name: AUTH_MODE' "$o" | has '"oidc"' && grep -A1 'name: OIDC_ISSUER' "$o" | has 'https://login.example/realms/data' &&
    grep -A5 'name: OIDC_CLIENT_SECRET' "$o" | has 'key: client-secret' && grep -q 'client-secret: "hunter22"' "$o" && ! grep -q 'value: "hunter22"' "$o" &&
    grep -A1 'name: ADMIN_GROUPS' "$o" | has '"platform-admins"' && grep -A1 'name: OIDC_PROVIDER_NAME' "$o" | has '"Keycloak"' &&
    ! grep -q 'LOCAL_USERS_FILE' "$o"
}
oidc_requires_client() {
  ! render k8s-console --set clusterName=ci --set auth.mode=oidc --set auth.oidc.issuer=https://login.example > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set auth.mode=magic > /dev/null 2>&1
}
audit_webhook() {
  local o=$WORK/chart-oidc.yaml
  grep -A1 'name: AUDIT_WEBHOOK_URL' "$o" | has 'https://hooks.example/221b' && grep -A5 'name: AUDIT_WEBHOOK_TOKEN' "$o" | has 'key: token' &&
    grep -q 'token: "hook-token"' "$o" && ! grep -q 'AUDIT_WEBHOOK' "$out"
}
ingress_tls() {
  local o=$WORK/chart-oidc.yaml
  grep -q '^kind: Ingress' "$o" && grep -q 'ingressClassName: "nginx"' "$o" && grep -q 'secretName: "console-tls"' "$o" &&
    grep -q 'host: "console.example.com"' "$o" && grep -A1 'name: HTTPS_REDIRECT' "$o" | has '"true"' &&
    grep -A1 'name: ALLOWED_HOSTS' "$o" | has ',console.example.com"' && ! grep -q 'HTTPS_REDIRECT' "$out" &&
    ! render k8s-console --set clusterName=ci --set ingress.enabled=true --set ingress.host= > /dev/null 2>&1
}
# An Ingress in front of a ClusterIP Service needs its controller listed as
# a peer: a policy that admits nothing would answer every request with 502.
ingress_peers() {
  local peers refused
  peers=$(ingress_rules "$WORK/chart-oidc.yaml")
  refused=$(render k8s-console --set clusterName=ci --set ingress.enabled=true --set ingress.host=console.example.com 2>&1 || true)
  [[ $peers == *ingress-nginx* && $refused == *'needs networkPolicy.ingressFrom'* ]] &&
    render k8s-console --set clusterName=ci --set ingress.enabled=true --set ingress.host=console.example.com --set networkPolicy.enabled=false > /dev/null
}
# Outbound traffic: only the API server by default; with OIDC or a webhook
# also DNS and HTTPS.
outbound() {
  grep -c 'port: 53' "$out" | has 2 && grep -c 'port: 443' "$out" | has 2 && grep -c 'port: 443' "$WORK/chart-sherlock.yaml" | has 1 && grep -c 'port: 443' "$WORK/chart-oidc.yaml" | has 2
}
no_ingress() { ! grep -q '^kind: Ingress' "$out"; }
cluster_ip() { grep -q 'type: ClusterIP' "$out"; }
probes_and_resources() {
  grep -q 'livenessProbe:' "$out" && grep -q 'readinessProbe:' "$out" && grep -q 'requests:' "$out" && grep -q 'limits:' "$out"
}
hardened() { grep -q 'runAsNonRoot: true' "$out" && grep -q 'readOnlyRootFilesystem: true' "$out"; }
# The ClusterRole reads the kinds the tabs show (from Phase 8 volumes,
# storage classes and volume attachments too), pod logs, metrics and the
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
WANT_READ='admissionregistration.k8s.io/mutatingwebhookconfigurations admissionregistration.k8s.io/validatingwebhookconfigurations apps/controllerrevisions apps/daemonsets apps/deployments apps/replicasets apps/statefulsets authorization.k8s.io/subjectaccessreviews autoscaling/horizontalpodautoscalers batch/cronjobs batch/jobs configmaps coordination.k8s.io/leases discovery.k8s.io/endpointslices endpoints events groups limitranges metrics.k8s.io/nodes metrics.k8s.io/pods namespaces networking.k8s.io/ingressclasses networking.k8s.io/ingresses networking.k8s.io/networkpolicies nodes nodes/proxy persistentvolumeclaims persistentvolumes pods pods/log policy/poddisruptionbudgets rbac.authorization.k8s.io/clusterrolebindings rbac.authorization.k8s.io/clusterroles rbac.authorization.k8s.io/rolebindings rbac.authorization.k8s.io/roles resourcequotas scheduling.k8s.io/priorityclasses secrets serviceaccounts services storage.k8s.io/storageclasses storage.k8s.io/volumeattachments users'
read_only_resources() { [ "$(granted "$out")" = "$WANT_READ" ]; }
# Read-only, but for the access reviews it creates to ask what a person may see.
read_only_verbs() {
  ! cluster_role | awk '/resources:/ { res = $0 } /verbs:/ && res !~ /subjectaccessreviews/ { print }' | has -E 'create|update|patch|delete|\*'
}
# With sign-in, it may impersonate people and review their access; with
# sign-in off there is nobody to read as, and neither is granted.
impersonation() {
  cluster_role | grep -A1 '"users"' | has 'verbs: \["impersonate"\]' &&
    cluster_role | grep -A1 '"groups"' | has 'verbs: \["impersonate"\]' &&
    cluster_role | grep -A1 '"subjectaccessreviews"' | has 'verbs: \["create"\]' &&
    grep -A1 'name: IMPERSONATE_USER_PREFIX' "$out" | has '""' &&
    ! cluster_role "$WORK/chart-noauth.yaml" | has -E 'impersonate|subjectaccessreviews' &&
    ! grep -q 'IMPERSONATE_USER_PREFIX' "$WORK/chart-noauth.yaml"
}
# Logs and metrics are read, never listed or watched more than needed.
log_verbs() { cluster_role | grep -A1 '"pods/log"' | has 'verbs: \["get"\]'; }
secrets_optional() {
  cluster_role | grep -A1 '"secrets"' | has 'verbs: \["get", "list", "watch"\]' && grep -A1 'name: SECRETS_READ' "$out" | has '"true"' &&
    [ "$(granted "$WORK/chart-nosecrets.yaml")" = "${WANT_READ/ secrets/}" ] && ! cluster_role "$WORK/chart-nosecrets.yaml" | has '"secrets"' &&
    grep -A1 'name: SECRETS_READ' "$WORK/chart-nosecrets.yaml" | has '"false"'
}
# Column choices are kept in each browser (asked at the Phase 6 review).
no_user_prefs() { ! grep -q 'USER_PREFS' "$out"; }
# Sherlock (Phase 5): the Claude API's address; its key in the release's
# own Secret, or from a Secret the chart is given, never in the values.
# Sherlock's provider from the chart, locked; none by default.
sherlock_provider() {
  grep -A1 'name: SHERLOCK_PROVIDER' "$WORK/chart-provider.yaml" | has '"kind\\":\\"bedrock\\"' &&
    grep -A1 'name: SHERLOCK_LOCK_PROVIDER' "$WORK/chart-provider.yaml" | has '"true"' &&
    ! grep -q 'SHERLOCK_PROVIDER\|SHERLOCK_LOCK_PROVIDER' "$out"
}
sherlock_key() {
  grep -A1 'name: SHERLOCK_API_URL' "$out" | has '"https://api.anthropic.com"' &&
    grep -A1 'name: SHERLOCK_SECRET' "$out" | has '/k8s-console-221b-sherlock"' &&
    ! grep -q 'name: SHERLOCK_API_KEY' "$out" &&
    grep -A4 'name: SHERLOCK_API_KEY' "$WORK/chart-sherlock.yaml" | has 'name: "claude"' &&
    ! grep -q 'name: SHERLOCK_SECRET' "$WORK/chart-sherlock.yaml"
}
settings_only() {
  settings_role | has 'resources: \["configmaps"\]' && settings_role | has 'resourceNames: \["k8s-console-221b", "k8s-console-221b-audit", "k8s-console-221b-sessions", "k8s-console-221b-ai", "k8s-console-221b-knowledge", "k8s-console-221b-wiki", "k8s-console-221b-runs", "k8s-console-221b-automation", "k8s-console-221b-automation-runs", "k8s-console-221b-issues", "k8s-console-221b-proposals", "k8s-console-221b-cases", "k8s-console-221b-casework"\]' &&
    [ "$(settings_role | grep -E 'verbs:' | tr -d ' ' | tr '\n' ' ')" = 'verbs:["get","list","watch","patch","update"] verbs:["create","list","get","update","patch","delete"] verbs:["create"] verbs:["get","update"] verbs:["get"] verbs:["get","list","watch","create","update","delete"] ' ] &&
    [ "$(grep -E 'verbs:' "$out" | grep -c -E 'update|delete|\*')" = 4 ]
}
# The connections to other clusters: Secrets in the release namespace, by
# default; in memory on request, and then nothing is granted on Secrets.
connections_store() {
  settings_role | grep -A1 'resources: \["secrets"\]' | has 'verbs: \["get", "list", "watch", "create", "update", "delete"\]' &&
    grep -A1 'name: CONNECTIONS_SECRETS' "$out" | has '"consoles/k8s-console-221b"' &&
    ! sed -n '/^kind: Role$/,/^---/p' "$WORK/chart-memory.yaml" | has 'secrets' &&
    ! grep -q 'CONNECTIONS_SECRETS' "$WORK/chart-memory.yaml" &&
    ! render k8s-console --set clusterName=ci --set connections.store=disk > /dev/null 2>&1
}
# The backups' key (ADR-137): its Secret named and only read; nothing on
# Secrets with connections in memory, where there is no key.
backup_key() {
  settings_role | grep -A2 'resourceNames: \["k8s-console-221b-backup-key"\]' | has 'verbs: \["get"\]' &&
    ! sed -n '/^kind: Role$/,/^---/p' "$WORK/chart-memory.yaml" | has 'backup-key' &&
    grep -q 'backup-key' "$CHART/templates/NOTES.txt"
}
no_kubelet_stats() {
  grep -q 'nodes/proxy' "$out" && ! grep -q 'nodes/proxy' "$WORK/chart-nostats.yaml" &&
    grep -A1 'name: KUBELET_STATS' "$WORK/chart-nostats.yaml" | has '"false"'
}
settings_configmap() {
  # Only the settings ConfigMap: the auth-config ConfigMap beside it has data.
  awk 'function done() { if (cm && nm) { found = 1; if (data) bad = 1 } cm = nm = data = 0 }
       /^---$/ { done(); next }
       /^kind: ConfigMap$/ { cm = 1 }
       /^  name: k8s-console-221b$/ { nm = 1 }
       /^data:/ { data = 1 }
       END { done(); exit !(found && !bad) }' "$out" &&
    grep -A1 'name: SETTINGS_CONFIGMAP' "$out" | has '"consoles/k8s-console-221b"' && grep -A1 'name: REFRESH' "$out" | has '"3s"'
}
service_name() { render 221b --set clusterName=ci | grep -A3 '^kind: Service$' | has 'name: k8s-console-221b'; }
name_required() { ! render k8s-console > /dev/null 2>&1; }
no_open_egress() { ! render k8s-console --set clusterName=ci --set networkPolicy.apiServerPorts=null > /dev/null 2>&1; }
shared_roles() {
  local only none
  only=$(render roles --set rbac.shared=only --set clusterName=ci)
  none=$(render k8s-console --set clusterName=ci --set rbac.shared=none)
  [[ $only == *'name: 221b-super-admin'* && $only != *'kind: Deployment'* && $only != *'kind: NetworkPolicy'* ]] &&
    [[ $none != *'name: 221b-super-admin'* && $none == *'kind: Deployment'* ]] &&
    ! render k8s-console --set clusterName=ci --set rbac.shared=some > /dev/null 2>&1
}
no_open_outbound() { ! render k8s-console --set clusterName=ci --set networkPolicy.outboundPorts=null > /dev/null 2>&1; }
numeric_tag() { render k8s-console --set clusterName=ci --set image.tag=20261031 > /dev/null 2>&1; }
outbound_cidrs() { netpol "$WORK/chart-outbound.yaml" | has 'cidr: "140.82.112.0/20"'; }
external_ingress() { netpol "$WORK/chart-external.yaml" | sed -n '/ingress:/,/egress:/p' | has '^    - ports:'; }
pvc_recreate() { grep -q 'type: Recreate' "$WORK/chart-pvc.yaml"; }
dns_egress() { grep -B1 'protocol: UDP' "$out" | has 'port: 53' && ! grep -q 'port: 53' "$WORK/chart-nopolicy.yaml"; }
api_server_addresses() { grep -q 'cidr: "10.0.0.10/32"' "$WORK/chart-options.yaml"; }
host_names() {
  grep -A1 'name: ALLOWED_HOSTS' "$out" | has 'k8s-console-221b,k8s-console-221b.consoles,k8s-console-221b.consoles.svc,k8s-console-221b.consoles.svc.cluster.local' &&
    grep -A1 'name: ALLOWED_HOSTS' "$WORK/chart-options.yaml" | has ',console.example.com"'
}
exposed_port() { grep -q 'port: 8081' "$out" && grep -q 'port: 8081' "$WORK/chart-lb.yaml"; }
# A release tag (the default) is fetched once; a moving tag is fetched on
# every pod start and the pod rolls on every upgrade; a sha- tag or a
# digest is fetched once; an explicit policy wins.
pull_policy() {
  local app
  app=$(sed -n 's/^appVersion: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$CHART/Chart.yaml")
  grep -q "image: \"ghcr.io/ucesys/221b:$app\"" "$out" && grep -q 'imagePullPolicy: IfNotPresent' "$out" && ! grep -q 'release-revision' "$out" &&
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
# A ClusterIP Service lets in only the console's other replicas; an exposed
# one lets everyone in unless peers are listed.
ingress_rules() { sed -n '/^kind: NetworkPolicy$/,/^---/p' "$1" | sed -n '/^  ingress:/,/^  egress:/p'; }
closed_by_default() {
  [ "$(ingress_rules "$out" | grep -c -E '^    - ')" = 1 ] && ingress_rules "$out" | has 'podSelector:' &&
    ingress_rules "$out" | has -E 'app.kubernetes.io/instance: "?k8s-console"?$'
}
open_when_exposed() {
  [ "$(ingress_rules "$WORK/chart-lb.yaml" | grep -c -E '^    - ')" = 2 ] &&
    ingress_rules "$WORK/chart-lb.yaml" | grep -B1 -A2 '^    - ports:' | has 'port: http'
}
# Several replicas (P7-02): every pod joins the election, the leader is
# reached at its pod IP with the shared token, the replicas reach each
# other through the network policy, and with more than one a disruption
# budget and a spread over nodes; connections in memory cannot be shared.
replicas() {
  local ha=$WORK/chart-ha.yaml
  grep -A1 'name: LEADER_LEASE' "$out" | has '"consoles/k8s-console-221b-leader"' &&
    grep -A3 'name: POD_IP' "$out" | has 'fieldPath: status.podIP' &&
    grep -A4 'name: PROXY_TOKEN' "$out" | has 'name: k8s-console-221b-proxy' &&
    grep -A1 'name: WARM_STANDBY' "$out" | has 'value: "true"' &&
    grep -A1 'name: WARM_STANDBY' "$WORK/chart-ha-cold.yaml" | has 'value: "false"' &&
    sed -n '/^kind: Secret$/,/^---/p' "$out" | has '^  name: k8s-console-221b-proxy$' &&
    settings_role | grep -A1 'resourceNames: \["k8s-console-221b-leader"\]' | has 'verbs: \["get", "update"\]' &&
    netpol "$out" | sed -n '/^  egress:/,$p' | has 'podSelector:' &&
    grep -q 'terminationGracePeriodSeconds: 20' "$out" &&
    ! grep -q '^kind: PodDisruptionBudget' "$out" && ! grep -q 'topologySpreadConstraints' "$out" &&
    grep -q 'replicas: 2' "$ha" && grep -A1 '^kind: PodDisruptionBudget' -A12 "$ha" | has 'maxUnavailable: 1' &&
    grep -A3 'topologySpreadConstraints:' "$ha" | has 'topologyKey: kubernetes.io/hostname' && grep -q 'whenUnsatisfiable: ScheduleAnyway' "$ha" &&
    grep -q 'topologyKey: topology.kubernetes.io/zone' "$WORK/chart-ha-zones.yaml" && ! grep -q 'kubernetes.io/hostname' "$WORK/chart-ha-zones.yaml" &&
    ! grep -q 'podAntiAffinity' "$ha" && ! grep -q 'podAntiAffinity' "$out" &&
    grep -A10 'podAntiAffinity:' "$WORK/chart-ha-soft.yaml" | has 'weight: 100' && grep -A12 'podAntiAffinity:' "$WORK/chart-ha-soft.yaml" | has 'topologyKey: kubernetes.io/hostname' &&
    grep -A1 'podAntiAffinity:' "$WORK/chart-ha-hard.yaml" | has 'requiredDuringSchedulingIgnoredDuringExecution' &&
    grep -q 'nodeAffinity:' "$WORK/chart-ha-hard.yaml" && grep -A6 'podAntiAffinity:' "$WORK/chart-ha-hard.yaml" | has 'app.kubernetes.io/instance: k8s-console' &&
    ! render k8s-console --set clusterName=ci --set antiAffinity=hard | has 'podAntiAffinity' &&
    { render k8s-console --set clusterName=ci --set replicaCount=2 --set antiAffinity=always 2>&1 || true; } | has 'antiAffinity must be soft, hard or empty' &&
    { render k8s-console --set clusterName=ci --set replicaCount=2 --set connections.store=memory 2>&1 || true; } | has 'needs connections.store=secrets'
}
# Watson (Phase 6): reads only by default. With localCluster.mode=write and
# sign-in off, changes run as the console, bound to the write role and a
# small role for nodes; with sign-in they run as the approver, and the
# console is granted nothing more.
watson_write() {
  local w=$WORK/chart-write.yaml
  grep -A1 'name: LOCAL_MODE' "$out" | has '"read"' && ! grep -q 'watson' "$out" && ! grep -q 'watson' "$WORK/chart-noauth.yaml" &&
    grep -A1 'name: LOCAL_MODE' "$w" | has '"write"' &&
    sed -n '/name: consoles-k8s-console-221b-watson-nodes$/,/^---/p' "$w" | grep -A1 '"nodes"' | has 'verbs: \["patch", "update"\]' &&
    sed -n '/name: consoles-k8s-console-221b-watson-nodes$/,/^---/p' "$w" | grep -A1 '"pods/eviction"' | has 'verbs: \["create"\]' &&
    sed -n '/^kind: ClusterRoleBinding$/,/^---/p' "$w" | has "name: consoles-k8s-console-221b-watson$" &&
    grep -A12 "name: consoles-k8s-console-221b-watson$" "$w" | has 'name: consoles-k8s-console-221b-watson-write' &&
    ! sed -n '/name: consoles-k8s-console-221b-watson-write$/,/^---/p' "$w" | has 'impersonate' &&
    render k8s-console --set clusterName=ci --set auth.mode=none --set localCluster.mode=write --set secrets.read=false | sed -n '/name: consoles-k8s-console-221b-watson-write$/,/^---/p' | grep -A1 '"secrets"' | has '"create", "update", "patch", "delete"\]' &&
    render k8s-console --set clusterName=ci --set auth.mode=none --set localCluster.mode=write --set localCluster.writeClusterRole=edit | has 'name: edit' &&
    grep -A1 'name: LOCAL_MODE' "$WORK/chart-write-signin.yaml" | has '"write"' && ! grep -q 'watson' "$WORK/chart-write-signin.yaml" &&
    ! render k8s-console --set clusterName=ci --set auth.mode=none --set localCluster.mode=write --set localCluster.writeClusterRole= | has 'name: edit' &&
    ! render k8s-console --set clusterName=ci --set localCluster.mode=admin > /dev/null 2>&1
}
# Phase 6.1: the four roles are installed from the definitions the console
# ships, bound to groups from values; the console gains no right on RBAC;
# the combinations that cannot protect anything are refused.
roles_and_bindings() { python3 "$ROOT/hack/chart_roles_check.py" "$WORK"; }
roles_refused() {
  local ldap="-f $ROOT/hack/chart-values/ldap.yaml"
  ! render k8s-console --set clusterName=ci --set rbac.roles.admin.template=readOnly --set rbac.roles.superAdmin.template=readOnly --set localCluster.mode=write > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set auth.mode=none --set localCluster.mode=write --set rbac.roles.admin.template=readOnly > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set rbac.roles.admin.template=sometimes > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set rbac.enforce=maybe > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set 'auth.local.groups[0].name=g' --set 'auth.local.groups[0].role=superAdmin' --set 'auth.local.groups[0].namespaces[0]=a' > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set 'auth.local.users[0].username=a' --set 'auth.local.users[0].passwordHash=h' --set 'auth.local.users[0].groups[0]=undefined' > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set 'auth.local.groups[0].name=g' --set 'auth.local.groups[0].role=admin' --set 'auth.local.groups[0].namespaces[0]=consoles' > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set 'auth.local.groups[0].name=g' --set 'auth.local.groups[0].role=viewer' --set 'auth.local.groups[0].namespaces[0]=web' --set 'auth.local.groups[0].namespaces[1]=consoles-privileged' > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set toolbox.nodeShell.namespace=tools --set 'auth.local.groups[0].name=g' --set 'auth.local.groups[0].role=admin' --set 'auth.local.groups[0].namespaces[0]=tools' > /dev/null 2>&1 &&
    ! render k8s-console $ldap --set 'auth.ldap.roles.admin[0].group=g' --set 'auth.ldap.roles.admin[0].namespaces[0]=consoles' > /dev/null 2>&1 &&
    ! render k8s-console $ldap --set 'auth.ldap.roles.viewer[0].group=g' --set 'auth.ldap.roles.viewer[0].namespaces[0]=consoles-privileged' > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set auth.mode=oidc --set auth.oidc.issuer=https://idp.example --set auth.oidc.clientID=c --set auth.oidc.clientSecret=s --set 'auth.oidc.roles.admin[0].group=g' --set 'auth.oidc.roles.admin[0].namespaces[0]=consoles' > /dev/null 2>&1 &&
    ! render k8s-console $ldap --set 'auth.ldap.roles.superAdmin[0].namespaces[0]=a' > /dev/null 2>&1 &&
    ! render k8s-console $ldap --set 'auth.ldap.roles.viewer[0].group=system:masters' > /dev/null 2>&1 &&
    ! render k8s-console $ldap --set auth.ldap.url=ldap://ldap.corp.example > /dev/null 2>&1 &&
    ! render k8s-console $ldap --set auth.ldap.existingSecret= > /dev/null 2>&1 &&
    render k8s-console $ldap --set auth.ldap.url=ldap://ldap.corp.example --set auth.ldap.startTLS=true > /dev/null 2>&1
}
# Whose X-Forwarded-For the console believes comes from values; nobody's
# by default.
trusted_proxies() {
  render k8s-console --set clusterName=ci | grep -A1 'name: TRUSTED_PROXIES' | has 'value: ""' &&
    render k8s-console --set clusterName=ci --set 'auth.trustedProxies[0]=10.42.0.0/16' --set 'auth.trustedProxies[1]=10.43.0.1/32' | grep -A1 'name: TRUSTED_PROXIES' | has 'value: "10.42.0.0/16,10.43.0.1/32"'
}
# The setup token reaches the console as a Secret's value, never as a plain
# environment value.
setup_token() {
  local o=$WORK/chart-groups.yaml
  grep -A5 'name: SETUP_TOKEN' "$o" | has 'key: setup-token' && grep -q 'setup-token: "first-run-code"' "$o" &&
    ! grep -q 'value: "first-run-code"' "$o" && ! grep -q 'SETUP_TOKEN' "$out"
}
# The Toolbox's values reach the console as a file; node shells get a
# namespace at Pod Security privileged that its pods cannot leave, unless
# they are off or no full SuperAdmin could create their pods (TB-48, TB-49).
toolbox_values() {
  grep -A1 'name: TOOLBOX_CONFIG_FILE' "$out" | has '/etc/221b/toolbox/toolbox.json' && grep -q '"idleTimeout": "10m"' "$out" &&
    grep -q '"enabled": false' "$WORK/chart-notoolbox.yaml" && grep -q '"transcriptDir": "/var/lib/221b/transcripts"' "$WORK/chart-transcripts.yaml" &&
    grep -q 'claimName: k8s-console-221b-transcripts' "$WORK/chart-transcripts.yaml" && grep -q 'storageClassName: "fast"' "$WORK/chart-transcripts.yaml" &&
    ! render k8s-console --set clusterName=ci --set toolbox.transcripts=pvc --set replicaCount=2 > /dev/null 2>&1 &&
    ! render k8s-console --set clusterName=ci --set toolbox.transcripts=s3 > /dev/null 2>&1
}
toolbox_namespace() {
  sed -n '/^kind: Namespace$/,/^---/p' "$out" | has 'pod-security.kubernetes.io/enforce: privileged' &&
    netpol "$out" | has 'namespace: consoles-privileged' &&
    grep -q '"namespace": "consoles-privileged"' "$out" &&
    ! grep -q '^kind: Namespace$' "$WORK/chart-notoolbox.yaml" && ! grep -q '^kind: Namespace$' "$WORK/chart-toolbox-exec.yaml" &&
    ! grep -q '^kind: Namespace$' "$WORK/chart-protected.yaml" && grep -q '"enabled": false' "$WORK/chart-protected.yaml" &&
    ! render k8s-console --set clusterName=ci --set toolbox.nodeShell.namespace=consoles > /dev/null 2>&1
}
# Admin forwards ports and starts debug pods; it opens shells only with
# rbac.roles.admin.exec (TB-50).
admin_exec() {
  admin() { sed -n '/^  name: 221b-admin$/,/^---/p' "$1"; }
  admin "$out" | has 'pods/portforward' && ! admin "$out" | has 'pods/exec' &&
    admin "$WORK/chart-toolbox-exec.yaml" | has 'pods/exec' && grep -q '"adminExec": true' "$WORK/chart-toolbox-exec.yaml" &&
    ! admin "$WORK/chart-protected.yaml" | has 'pods/portforward'
}
digit_override() { render 221b --set clusterName=ci --set fullnameOverride=2b | grep -A3 '^kind: Service$' | has 'name: k8s-console-2b'; }

check "renders a Deployment, Service, ServiceAccount, ConfigMap, ClusterRole, Role, their bindings, a Lease and a NetworkPolicy" kinds
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
check "the backups' key Secret is named in the Role and only read" backup_key
check "kubeletStats=false grants nothing on the kubelets" no_kubelet_stats
check "the settings ConfigMap is created empty and named to the server" settings_configmap
check "column choices stay in the browser: nothing about preferences reaches the server" no_user_prefs
check "Sherlock's API key is kept in the release's Secret or taken from sherlock.existingSecret, never in the values" sherlock_key
check "Sherlock's provider from the chart, locked when asked (Phase 6 review)" sherlock_provider
check "a release named after the chart still gets a valid Service name" service_name
check "clusterName is required" name_required
check "the shared roles install on their own, and a console can leave them out" shared_roles
check "an empty list of API server ports is refused" no_open_egress
check "an empty list of outbound ports is refused" no_open_outbound
check "an all-digit image tag renders" numeric_tag
check "networkPolicy.outboundCIDRs opens the way to a git host with Sherlock's egress off" outbound_cidrs
check "external IPs are let in by the network policy" external_ingress
check "transcripts on a volume of one node replace the pod rather than roll it" pvc_recreate
check "DNS is allowed out, for the clusters given by name" dns_egress
check "outbound traffic: API servers and DNS only, or also HTTPS for OIDC, a webhook or Sherlock" outbound
check "local accounts are mounted from a Secret, or an existing one; none with sign-in off" local_accounts
check "OIDC sign-in takes its client secret from a Secret and names the administrators" oidc_signin
check "OIDC without a client secret, and an unknown mode, are refused" oidc_requires_client
check "the audit webhook and its token" audit_webhook
check "an Ingress with TLS redirects HTTP, allows its host and needs a host" ingress_tls
check "an Ingress needs its controller among the network policy's peers" ingress_peers
check "API server addresses narrow the egress rule" api_server_addresses
check "it answers to its Service's names and the names in values" host_names
check "Watson reads only by default; with mode write and sign-in off it is bound to the write role and may cordon and drain" watson_write
check "the four roles are installed from the shipped definitions and bound to groups; the console gains no right on RBAC" roles_and_bindings
check "contradictory or unsafe role settings are refused" roles_refused
check "the proxies to believe come from values, and none by default" trusted_proxies
check "the setup token is a Secret's value, not an environment value" setup_token
check "an override that starts with a digit still gets a valid name" digit_override
check "the Service listens on 8081" exposed_port
check "moving tags are pulled on every start and rolled on every upgrade; releases, sha- tags and digests once" pull_policy
check "a LoadBalancer Service carries its class, address list, node port and annotations" lb_service
check "only the console's own replicas may reach a ClusterIP Service through the network policy" closed_by_default
check "an exposed Service is reachable through the network policy" open_when_exposed
check "the replicas elect a leader and reach each other, the standbys warm unless told otherwise; more than one adds a budget and a spread, and needs Secrets" replicas
check "the Toolbox's values reach the console, and transcripts get a volume" toolbox_values
check "node shells run in a namespace of their own at Pod Security privileged, closed to the network" toolbox_namespace
check "SSH to nodes is in the toolbox config, off by default with port 22 and root, and a value of its own when asked" ssh_values
check "a read-only SuperAdmin definition, or values kept from an older release, leave SSH off and its egress out" ssh_gated
check "the network policy lets the console reach sshd on the nodes only with SSH on, narrowed to networkPolicy.nodeCIDRs" ssh_egress
check "the network policy lets the console reach the backups' S3 endpoint only at backup.s3.egressCIDRs" backup_egress
check "hosts outside Kubernetes are reached on the SSH rule, with their own ports when listed" host_ports
check "the network policy lets the console reach the Helm repositories only at helmRepositories.egressCIDRs" helm_repos_egress
check "Admin forwards ports and starts debug pods, and opens shells only when asked" admin_exec
check "Automation keeps its records in a ConfigMap of its own, named in the env and the Role; off, neither" automation_configmap
check "the AI agents keep their records in a ConfigMap of their own, and their baselines come from values, mounted" ai_configmaps
check "the issue service keeps its records in a ConfigMap of its own, named in the env and the Role, with its roles and reporter groups; off, the report page is off and the cases keep the ConfigMap" issues_values
check "Mycroft's knowledge keeps its records in a ConfigMap of its own, its local copy in an emptyDir, and reaches its sources only at knowledge.egressCIDRs" knowledge_values
check "the temporary wiki store has a ConfigMap of its own, named in the env and the Role, and none when knowledge.configMapStore.enabled is false" wiki_configmap
check "the jobs keep their runs in a ConfigMap of their own, named in the env and the Role; the time zone of new schedules when set" jobs_configmap
check "the cases are kept in a ConfigMap of their own, named in the env and the Role" casework_configmap
check "each case is a ConfigMap the console makes in its namespace, granted by the Role without names, with the clean start's values in the env" case_configmaps
check "the agents' proposed changes keep their records in a ConfigMap of their own, <release>-proposals, named in the env and the Role, with who approves them; <release>-cases stays, readable, for the copy at start" cases_values
exit "$fail"
