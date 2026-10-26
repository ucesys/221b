#!/usr/bin/env bash
# Checks that a ServiceAccount can read what the tabs show (nodes, pods and
# their logs, namespaces, workloads, Services, metrics, the kubelets'
# stats, from Phase 4 roles, bindings and the policy kinds, and from Phase
# 8 volumes, storage classes and volume attachments), patch and
# update its own settings ConfigMap, hold its replicas' Lease (P7-02),
# manage the Secrets of its own namespace (the connections to other
# clusters, P3-04), and nothing else
# (P1-09, X-03, P2-10). Secrets elsewhere are readable only when the chart
# was installed with secrets.read (the default); with --no-secrets the
# script expects the API server to refuse them. With --memory the chart
# was installed with connections.store=memory and nothing is granted on
# the namespace's Secrets. With sign-in on (the default) it may read as
# the signed-in people: impersonate users and the groups listed in values, and create
# SubjectAccessReviews about them (P3-02); with --no-signin the chart was
# installed with auth.mode=none and neither is granted. Every
# authenticated identity can also review its own access and read
# discovery and version URLs; those are allowed.
#   hack/check-rbac.sh [--no-secrets] [--memory] [--no-signin] system:serviceaccount:221b:k8s-console-221b
set -euo pipefail
secrets=yes
connections=yes
people=yes
while [ $# -gt 1 ]; do
  case $1 in
    --no-secrets) secrets=no ;;
    --memory) connections=no ;;
    --no-signin) people=no ;;
    *) echo "unknown option $1"; exit 2 ;;
  esac
  shift
done
SA=$1
# system:serviceaccount:<namespace>:<name>: the ConfigMap is named after
# the ServiceAccount, in its namespace.
NS=$(echo "$SA" | cut -d: -f3)
NAME=$(echo "$SA" | cut -d: -f4)
failures=()

failed() {
  echo "FAIL: $*"
  failures+=("$*")
}

expect() {
  local want=$1; shift
  local got
  got=$(kubectl auth can-i "$@" --as="$SA" 2>/dev/null || true)
  if [ "$got" != "$want" ]; then
    failed "can-i $* is '$got', want '$want'"
  else
    echo "ok: can-i $* = $got"
  fi
}

# Watched across the cluster: the kinds the tabs show.
read_kinds=(nodes namespaces pods services endpoints persistentvolumeclaims configmaps serviceaccounts
  resourcequotas limitranges events
  deployments.apps statefulsets.apps daemonsets.apps replicasets.apps controllerrevisions.apps
  jobs.batch cronjobs.batch endpointslices.discovery.k8s.io
  ingresses.networking.k8s.io networkpolicies.networking.k8s.io
  roles.rbac.authorization.k8s.io rolebindings.rbac.authorization.k8s.io
  poddisruptionbudgets.policy horizontalpodautoscalers.autoscaling leases.coordination.k8s.io
  clusterroles.rbac.authorization.k8s.io clusterrolebindings.rbac.authorization.k8s.io
  priorityclasses.scheduling.k8s.io ingressclasses.networking.k8s.io
  validatingwebhookconfigurations.admissionregistration.k8s.io mutatingwebhookconfigurations.admissionregistration.k8s.io
  persistentvolumes storageclasses.storage.k8s.io volumeattachments.storage.k8s.io)
for kind in "${read_kinds[@]}"; do
  for verb in get list watch; do
    expect yes "$verb" "$kind" --all-namespaces
  done
done
for verb in get list watch; do
  expect "$secrets" "$verb" secrets --all-namespaces
  expect "$secrets" "$verb" secrets -n kube-system
done
expect yes get pods --subresource=log --all-namespaces
for kind in nodes.metrics.k8s.io pods.metrics.k8s.io; do
  expect yes get "$kind" --all-namespaces
  expect yes list "$kind" --all-namespaces
  expect no watch "$kind" --all-namespaces
done
expect yes get nodes --subresource=proxy
expect yes get "configmaps/$NAME" -n "$NS"
expect yes patch "configmaps/$NAME" -n "$NS"
expect yes update "configmaps/$NAME" -n "$NS"
# The replicas' election (P7-02): the one Lease, in its own namespace.
expect yes create leases.coordination.k8s.io -n "$NS"
expect yes update "leases.coordination.k8s.io/$NAME-leader" -n "$NS"
expect no update leases.coordination.k8s.io/kube-scheduler -n kube-system
expect no update leases.coordination.k8s.io/someone-else -n "$NS"
expect no create leases.coordination.k8s.io -n default
expect no delete "leases.coordination.k8s.io/$NAME-leader" -n "$NS"
# The connections to other clusters: Secrets in its own namespace only.
# (With secrets.read, watch comes from the ClusterRole, and reading them
# anywhere; the Role adds the writes in this namespace only.)
for verb in get list create update delete; do
  expect "$connections" "$verb" secrets -n "$NS"
done
# The replicas watch them, so a change made through one reaches the others.
if [ "$connections" = yes ]; then
  expect yes watch secrets -n "$NS"
else
  expect "$secrets" watch secrets -n "$NS"
fi
expect no create secrets -n default
expect no delete secrets -n kube-system
if [ "$secrets" = no ]; then
  expect no get secrets -n default
fi

# Reading as people: impersonating users and groups, and reviewing their
# access, with sign-in on; nothing of the kind with it off. Never a
# ServiceAccount or a UID, which would let it become anything.
# The groups it may assert are the reserved ones and those listed in values
# (RB-37); never system:masters, whatever a token or a directory says.
expect "$people" impersonate users
for group in 221b:console-users 221b:superadmins 221b:superviewers system:authenticated; do
  expect "$people" impersonate "groups/$group"
done
expect no impersonate groups/system:masters
expect no impersonate groups/not-in-values
expect "$people" create subjectaccessreviews.authorization.k8s.io
expect no impersonate serviceaccounts
expect no impersonate uids
expect no impersonate userextras.authentication.k8s.io
# The kubelets' endpoints are read-only through the proxy, and only the one
# ConfigMap is written.
expect no create nodes --subresource=proxy
expect no patch configmaps/kube-root-ca.crt -n "$NS"
expect no delete "configmaps/$NAME" -n "$NS"
# Subresources need --subresource: "pods/log" would mean the pod named log.
for args in "create pods" "delete pods" "patch nodes" "update nodes" "delete nodes" "delete namespaces" \
  "create pods --subresource=exec" "create pods --subresource=attach" "create pods --subresource=portforward" \
  "create pods --subresource=eviction" "create serviceaccounts --subresource=token" "patch nodes --subresource=status" \
  "create deployments.apps" "patch deployments.apps" "delete jobs.batch" "create secrets" "patch secrets" "delete secrets" \
  "patch services" "delete services" "create configmaps" "patch configmaps" "delete configmaps" \
  "create roles.rbac.authorization.k8s.io" "create rolebindings.rbac.authorization.k8s.io" \
  "create clusterrolebindings.rbac.authorization.k8s.io" "delete rolebindings.rbac.authorization.k8s.io" \
  "patch rolebindings.rbac.authorization.k8s.io" "patch clusterroles.rbac.authorization.k8s.io" \
  "delete clusterrolebindings.rbac.authorization.k8s.io" "patch persistentvolumeclaims" "delete persistentvolumeclaims" \
  "delete persistentvolumes" "patch storageclasses.storage.k8s.io" "delete storageclasses.storage.k8s.io" "create volumeattachments.storage.k8s.io" \
  "get customresourcedefinitions.apiextensions.k8s.io" "patch validatingwebhookconfigurations.admissionregistration.k8s.io" \
  "escalate clusterroles.rbac.authorization.k8s.io" "bind clusterroles.rbac.authorization.k8s.io"; do
  # shellcheck disable=SC2086
  expect no $args --all-namespaces
done

# Everything --list shows, in every namespace it might differ. Besides what
# the chart grants, every identity may review its own access and, from
# Kubernetes 1.36, read ClusterTrustBundles (public CA certificates). The
# settings ConfigMap shows in the console's own namespace only, by name.
allowed_kinds=(nodes nodes/proxy pods pods/log "${read_kinds[@]}" nodes.metrics.k8s.io pods.metrics.k8s.io
  selfsubjectreviews.authentication.k8s.io selfsubjectaccessreviews.authorization.k8s.io
  selfsubjectrulesreviews.authorization.k8s.io clustertrustbundles.certificates.k8s.io)
if [ "$secrets" = yes ]; then
  allowed_kinds+=(secrets)
fi
if [ "$people" = yes ]; then
  allowed_kinds+=(users groups subjectaccessreviews.authorization.k8s.io)
fi
allowed="^($(IFS='|'; echo "${allowed_kinds[*]}"))$"
for ns in default kube-system "$NS"; do
  echo "--- kubectl auth can-i --list -n $ns"
  list=$(kubectl auth can-i --list --as="$SA" -n "$ns")
  echo "$list"
  extra=$(echo "$list" | awk 'NR > 1 && $1 !~ /^\[/ {print $1}' | grep -v -E "$allowed" || true)
  if [ "$ns" = "$NS" ]; then
    if ! echo "$list" | grep -E "^configmaps +\[\] +\[$NAME\] +\[get list watch patch update\]" > /dev/null; then
      failed "the settings ConfigMap is not listed as get, list, watch, patch and update by name in $ns"
    fi
    if ! echo "$list" | grep -E "^leases.coordination.k8s.io +\[\] +\[$NAME-leader\] +\[get update\]" > /dev/null; then
      failed "the replicas' Lease is not listed as get and update by name in $ns"
    fi
  fi
  # Every granted verb list is read-only, except the settings ConfigMap's,
  # the self-reviews every identity may create and, in the console's own
  # namespace, the connection Secrets.
  # The reviews of people's access are created, with sign-in on.
  own_writes="^(configmaps +\[\] +\[$NAME\]|selfsubject|subjectaccessreviews.authorization.k8s.io +\[\] +\[\] +\[create\])"
  if [ "$ns" = "$NS" ]; then
    # The Lease: created when none exists (the listing may merge the
    # create with the ClusterRole's reads), then only this one renewed.
    own_writes="^(configmaps +\[\] +\[$NAME\]|selfsubject|subjectaccessreviews.authorization.k8s.io +\[\] +\[\] +\[create\]|leases.coordination.k8s.io +\[\] +\[\] +\[(get list watch )?create\]|leases.coordination.k8s.io +\[\] +\[$NAME-leader\] +\[get update\])"
  fi
  if [ "$ns" = "$NS" ] && [ "$connections" = yes ]; then
    # The Role's writes merge with the ClusterRole's reads in the listing:
    # [get list watch create update delete] with secrets.read, without watch otherwise.
    own_writes="$own_writes|^secrets +\[\] +\[\] +\[get list (watch )?create update delete\]"
    if ! echo "$list" | grep -E "^secrets +\[\] +\[\] +\[get list (watch )?create update delete\]" > /dev/null; then
      failed "the connection Secrets are not listed as get, list, create, update and delete in $ns"
    fi
  fi
  writes=$(echo "$list" | awk 'NR > 1 && $1 !~ /^\[/' | grep -v -E "$own_writes" | grep -E '\[[^]]*(create|update|patch|delete|deletecollection|\*)[^]]*\]$' || true)
  if [ -n "$writes" ]; then
    failed "writes granted in $ns: $writes"
  fi
  if [ -n "$extra" ]; then
    failed "more than the console needs in $ns: $extra"
  fi
done

# And the API server itself says so for Secrets.
if [ "$secrets" = yes ]; then
  if ! kubectl get secrets -A --as="$SA" -o name > /dev/null 2>&1; then
    failed "listing Secrets failed, although secrets.read is on"
  else
    echo "ok: listing Secrets works (secrets.read)"
  fi
elif out=$(kubectl get secrets -A --as="$SA" 2>&1); then
  failed "listing Secrets worked with secrets.read=false"
elif ! echo "$out" | grep -q -i forbidden; then
  failed "listing Secrets failed, but not as Forbidden: $out"
else
  echo "ok: listing Secrets is Forbidden (secrets.read=false)"
fi
# Writing anything is refused by the API server itself.
if out=$(kubectl -n "$NS" delete pod does-not-exist --as="$SA" 2>&1); then
  failed "deleting a pod was not refused"
elif ! echo "$out" | grep -q -i forbidden; then
  failed "deleting a pod failed, but not as Forbidden: $out"
else
  echo "ok: deleting a pod is Forbidden"
fi

if [ "${#failures[@]}" -gt 0 ]; then
  echo "${#failures[@]} permission checks failed:"
  printf '  %s\n' "${failures[@]}"
  exit 1
fi
echo "All permission checks passed."
