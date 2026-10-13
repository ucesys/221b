#!/usr/bin/env bash
# Checks that a ServiceAccount can read nodes, pods, node metrics and the
# kubelets' stats, patch its own settings ConfigMap, and nothing else
# (P1-09, X-03). Every authenticated identity can also review its own access
# and read discovery and version URLs; those are allowed here too.
#   hack/check-rbac.sh system:serviceaccount:221b:console-221b
set -euo pipefail
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

for verb in get list watch; do
  expect yes "$verb" nodes
  expect yes "$verb" pods --all-namespaces
done
expect yes get nodes.metrics.k8s.io
expect yes list nodes.metrics.k8s.io
expect yes get nodes --subresource=proxy
expect yes get "configmaps/$NAME" -n "$NS"
expect yes patch "configmaps/$NAME" -n "$NS"

for verb in get list watch; do
  expect no "$verb" secrets --all-namespaces
  expect no "$verb" secrets -n kube-system
  expect no "$verb" configmaps --all-namespaces
  expect no "$verb" configmaps -n "$NS"
  expect no "$verb" pods.metrics.k8s.io --all-namespaces
done
# The kubelets' endpoints are read-only through the proxy, and only the one
# ConfigMap is written.
expect no create nodes --subresource=proxy
expect no get "configmaps/$NAME" -n kube-system
expect no patch configmaps/kube-root-ca.crt -n "$NS"
expect no update "configmaps/$NAME" -n "$NS"
expect no delete "configmaps/$NAME" -n "$NS"
# Subresources need --subresource: "pods/log" would mean the pod named log.
for args in "create pods" "delete pods" "patch nodes" "update nodes" "delete nodes" \
  "create pods --subresource=exec" "get pods --subresource=log" "create pods --subresource=portforward" \
  "create pods --subresource=eviction" "create serviceaccounts --subresource=token" "patch nodes --subresource=status" \
  "create deployments" "impersonate users" "escalate clusterroles" "bind clusterroles" "get events"; do
  # shellcheck disable=SC2086
  expect no $args --all-namespaces
done

# Everything --list shows, in every namespace it might differ. Besides what
# the chart grants, every identity may review its own access and, from
# Kubernetes 1.36, read ClusterTrustBundles (public CA certificates). The
# settings ConfigMap shows in the console's own namespace only, by name.
allowed='^(nodes|nodes/proxy|pods|nodes\.metrics\.k8s\.io|selfsubjectreviews\.authentication\.k8s\.io|selfsubjectaccessreviews\.authorization\.k8s\.io|selfsubjectrulesreviews\.authorization\.k8s\.io|clustertrustbundles\.certificates\.k8s\.io)$'
for ns in default kube-system "$NS"; do
  echo "--- kubectl auth can-i --list -n $ns"
  list=$(kubectl auth can-i --list --as="$SA" -n "$ns")
  echo "$list"
  extra=$(echo "$list" | awk 'NR > 1 && $1 !~ /^\[/ {print $1}' | grep -v -E "$allowed" || true)
  if [ "$ns" = "$NS" ]; then
    if ! echo "$list" | grep -E "^configmaps +\[\] +\[$NAME\] +\[get patch\]" > /dev/null; then
      failed "the settings ConfigMap is not listed as get and patch by name in $ns"
    fi
    extra=$(echo "$extra" | grep -v -x configmaps || true)
  fi
  if [ -n "$extra" ]; then
    failed "more than the console needs in $ns: $extra"
  fi
done

# And reading a Secret is refused by the API server itself.
if out=$(kubectl get secrets -A --as="$SA" 2>&1); then
  failed "listing Secrets worked"
elif ! echo "$out" | grep -q -i forbidden; then
  failed "listing Secrets failed, but not as Forbidden: $out"
else
  echo "ok: listing Secrets is Forbidden"
fi

if [ "${#failures[@]}" -gt 0 ]; then
  echo "${#failures[@]} permission checks failed:"
  printf '  %s\n' "${failures[@]}"
  exit 1
fi
echo "All permission checks passed."
