#!/usr/bin/env bash
# Checks that a ServiceAccount can read what the tabs show (nodes, pods and
# their logs, namespaces, workloads, Services, metrics, the kubelets'
# stats), patch its own settings ConfigMap, and nothing else (P1-09, X-03,
# P2-10). Secrets are readable only when the chart was installed with
# secrets.read (the default); with --no-secrets the script expects the API
# server to refuse them. Every authenticated identity can also review its
# own access and read discovery and version URLs; those are allowed here.
#   hack/check-rbac.sh [--no-secrets] system:serviceaccount:221b:console-221b
set -euo pipefail
secrets=yes
if [ "${1:-}" = "--no-secrets" ]; then
  secrets=no
  shift
fi
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
  poddisruptionbudgets.policy horizontalpodautoscalers.autoscaling leases.coordination.k8s.io)
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

# The kubelets' endpoints are read-only through the proxy, and only the one
# ConfigMap is written.
expect no create nodes --subresource=proxy
expect no patch configmaps/kube-root-ca.crt -n "$NS"
expect no update "configmaps/$NAME" -n "$NS"
expect no delete "configmaps/$NAME" -n "$NS"
# Subresources need --subresource: "pods/log" would mean the pod named log.
for args in "create pods" "delete pods" "patch nodes" "update nodes" "delete nodes" "delete namespaces" \
  "create pods --subresource=exec" "create pods --subresource=attach" "create pods --subresource=portforward" \
  "create pods --subresource=eviction" "create serviceaccounts --subresource=token" "patch nodes --subresource=status" \
  "create deployments.apps" "patch deployments.apps" "delete jobs.batch" "create secrets" "patch secrets" "delete secrets" \
  "patch services" "delete services" "create configmaps" "patch configmaps" "delete configmaps" \
  "create roles.rbac.authorization.k8s.io" "create rolebindings.rbac.authorization.k8s.io" \
  "get clusterroles.rbac.authorization.k8s.io" "get persistentvolumes" "get storageclasses.storage.k8s.io" \
  "get customresourcedefinitions.apiextensions.k8s.io" "get validatingwebhookconfigurations.admissionregistration.k8s.io" \
  "impersonate users" "escalate clusterroles.rbac.authorization.k8s.io" "bind clusterroles.rbac.authorization.k8s.io"; do
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
allowed="^($(IFS='|'; echo "${allowed_kinds[*]}"))$"
for ns in default kube-system "$NS"; do
  echo "--- kubectl auth can-i --list -n $ns"
  list=$(kubectl auth can-i --list --as="$SA" -n "$ns")
  echo "$list"
  extra=$(echo "$list" | awk 'NR > 1 && $1 !~ /^\[/ {print $1}' | grep -v -E "$allowed" || true)
  if [ "$ns" = "$NS" ]; then
    if ! echo "$list" | grep -E "^configmaps +\[\] +\[$NAME\] +\[get patch\]" > /dev/null; then
      failed "the settings ConfigMap is not listed as get and patch by name in $ns"
    fi
  fi
  # Every granted verb list is read-only, except the settings ConfigMap's
  # and the self-reviews every identity may create.
  writes=$(echo "$list" | awk 'NR > 1 && $1 !~ /^\[/' | grep -v -E "^(configmaps +\[\] +\[$NAME\]|selfsubject)" | grep -E '\[[^]]*(create|update|patch|delete|deletecollection|\*)[^]]*\]$' || true)
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
