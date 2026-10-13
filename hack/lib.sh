# Shared by the hack/ scripts: tool versions, checksums and log helpers.
# shellcheck shell=bash

KUBECONFORM_VERSION=v0.8.0
KUBECONFORM_SHA256=9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883
METRICS_SERVER_VERSION=v0.9.0
METRICS_SERVER_SHA256=1cec29a5267809306a2c6ec74a3e449abbb705b4a8beed0c8a1963910f72c79b
KWOK_VERSION=v0.8.0
KWOK_SHA256=a4c16e6431e382dcb5c1903139344b7a68652f16a6460337fe17a678a426f405
KWOK_STAGE_FAST_SHA256=2f28d95564ec43056c0873f7a25ac7d2a5bba4c8496c72f8b3ee73fd4f54ee24

# kind releases and the node image each one defaults to, by Kubernetes minor.
declare -A KIND_VERSION=([1.35]=v0.31.0 [1.36]=v0.32.0 [1.37]=v0.33.0)
declare -A KIND_SHA256=(
  [v0.31.0]=eb244cbafcc157dff60cf68693c14c9a75c4e6e6fedaf9cd71c58117cb93e3fa
  [v0.32.0]=50030de23cf40a18505f20426f6a8506bedf13c6e509244bd1fa9463721b0f54
  [v0.33.0]=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
)
declare -A KIND_NODE_IMAGE=(
  [1.35]=kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f
  [1.36]=kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5
  [1.37]=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
)

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TOOLS=${TOOLS:-$ROOT/bin/tools}
WORK=${WORK:-$ROOT/bin/e2e}
mkdir -p "$TOOLS" "$WORK"
export PATH="$TOOLS:$PATH"

# Log groups in GitHub Actions, plain headings elsewhere.
step() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::group::$*"; else echo "==> $*"; fi
}
endstep() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::endgroup::"; fi
}
notice() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::notice title=$1::$2"; else echo "$1: $2"; fi
}

# fetch URL FILE SHA256: downloads once and checks the checksum.
fetch() {
  local url=$1 out=$2 sum=$3
  if [ ! -f "$out" ] || ! echo "$sum  $out" | sha256sum -c --quiet - 2>/dev/null; then
    curl -fsSL --retry 3 -o "$out.tmp" "$url"
    echo "$sum  $out.tmp" | sha256sum -c --quiet -
    mv "$out.tmp" "$out"
  fi
}

install_kind() {
  local minor=$1 version=${KIND_VERSION[$1]:?no kind release for Kubernetes $1}
  fetch "https://github.com/kubernetes-sigs/kind/releases/download/$version/kind-linux-amd64" "$TOOLS/kind" "${KIND_SHA256[$version]}"
  chmod +x "$TOOLS/kind"
  echo "kind $version, node image ${KIND_NODE_IMAGE[$minor]}"
}

install_kubeconform() {
  fetch "https://github.com/yannh/kubeconform/releases/download/$KUBECONFORM_VERSION/kubeconform-linux-amd64.tar.gz" "$TOOLS/kubeconform.tar.gz" "$KUBECONFORM_SHA256"
  tar -xzf "$TOOLS/kubeconform.tar.gz" -C "$TOOLS" kubeconform
}

# metrics-server with --kubelet-insecure-tls, since kind kubelets use
# self-signed serving certificates. Prints the manifest's path.
metrics_server_manifest() {
  local src=$WORK/metrics-server-$METRICS_SERVER_VERSION.yaml out=$WORK/metrics-server.yaml
  fetch "https://github.com/kubernetes-sigs/metrics-server/releases/download/$METRICS_SERVER_VERSION/components.yaml" "$src" "$METRICS_SERVER_SHA256"
  sed 's|^\( *\)- --metric-resolution=15s$|&\n\1- --kubelet-insecure-tls|' "$src" > "$out"
  grep -q -- '--kubelet-insecure-tls' "$out"
  echo "$out"
}

install_metrics_server() {
  local manifest
  manifest=$(metrics_server_manifest)
  kubectl apply -f "$manifest"
  kubectl -n kube-system rollout status deployment/metrics-server --timeout=180s
  # Ready is not enough: wait for the first reading of every node.
  local nodes
  nodes=$(kubectl get nodes --no-headers | wc -l)
  for _ in $(seq 90); do
    if [ "$(kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes 2>/dev/null | grep -o '"name"' | wc -l)" -ge "$nodes" ]; then
      return 0
    fi
    sleep 2
  done
  echo "metrics-server has no reading for every node" >&2
  return 1
}
