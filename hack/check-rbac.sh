#!/usr/bin/env bash
# Checks that a ServiceAccount can read nodes, pods and node metrics, and
# nothing else (P1-09). Every authenticated identity can also review its own
# access and read discovery and version URLs; those are allowed here too.
#   hack/check-rbac.sh system:serviceaccount:221b:console-221b
set -euo pipefail
SA=$1
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

for verb in get list watch; do
  expect no "$verb" secrets --all-namespaces
  expect no "$verb" secrets -n kube-system
  expect no "$verb" configmaps --all-namespaces
  expect no "$verb" pods.metrics.k8s.io --all-namespaces
done
for args in "create pods" "delete pods" "patch nodes" "update nodes" "delete nodes" "create pods/exec" \
  "get pods/log" "create pods/eviction" "create deployments" "impersonate users" "create serviceaccounts/token" \
  "escalate clusterroles" "bind clusterroles" "get events"; do
  # shellcheck disable=SC2086
  expect no $args --all-namespaces
done

# Everything --list shows, in every namespace it might differ. Besides what
# the chart grants, every identity may review its own access and, from
# Kubernetes 1.36, read ClusterTrustBundles (public CA certificates).
allowed='^(nodes|pods|nodes\.metrics\.k8s\.io|selfsubjectreviews\.authentication\.k8s\.io|selfsubjectaccessreviews\.authorization\.k8s\.io|selfsubjectrulesreviews\.authorization\.k8s\.io|clustertrustbundles\.certificates\.k8s\.io)$'
for ns in default kube-system 221b; do
  echo "--- kubectl auth can-i --list -n $ns"
  list=$(kubectl auth can-i --list --as="$SA" -n "$ns")
  echo "$list"
  extra=$(echo "$list" | awk 'NR > 1 && $1 !~ /^\[/ {print $1}' | grep -v -E "$allowed" || true)
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
