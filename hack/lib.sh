# shellcheck shell=bash disable=SC2034
# Shared by the hack/ scripts: tool versions, checksums and log helpers.
# (The scripts that source this file use its variables, hence SC2034.)

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

# has is grep -q that reads all its input: grep -q stops at the first
# match, and under pipefail the command writing to it then fails, as a
# large render or a long curl answer does.
has() { grep "$@" > /dev/null; }

# The kind scripts install the chart with its bundled database (DB-69):
# there is no console without one. Both images must be in the local docker
# before a cluster takes them with `kind load`: IMAGE (the console's) and
# DB_IMAGE (221b-postgres, deploy/postgres). CI passes the images it built
# and scanned; on a laptop an unset one is built here, and one named but
# not there is built under that name.
CONSOLE_DEV_IMAGE=ghcr.io/ucesys/221b:kind-dev
DB_DEV_IMAGE=ghcr.io/ucesys/221b-postgres:kind-dev
# The console's NodePort on the host (hack/kind.yaml); another port where
# 8080 is taken.
HOST_PORT=${HOST_PORT:-8080}
# How long helm waits for an install or upgrade: the database's pod first
# (its image, its volume, initdb), then the console, whose first start
# makes the schema and copies what it finds before it listens.
HELM_TIMEOUT=${HELM_TIMEOUT:-600s}

# build_images: sets IMAGE and DB_IMAGE, building what is not there.
build_images() {
  IMAGE=${IMAGE:-$CONSOLE_DEV_IMAGE}
  DB_IMAGE=${DB_IMAGE:-$DB_DEV_IMAGE}
  if ! docker image inspect "$IMAGE" > /dev/null 2>&1; then
    DOCKER_BUILDKIT=1 docker build --build-arg VERSION="kind-$(git -C "$ROOT" rev-parse --short=12 HEAD)" -t "$IMAGE" "$ROOT"
  fi
  if ! docker image inspect "$DB_IMAGE" > /dev/null 2>&1; then
    DOCKER_BUILDKIT=1 docker build -t "$DB_IMAGE" "$ROOT/deploy/postgres"
  fi
  echo "the console's image $IMAGE, the database's $DB_IMAGE"
}

# kind_config: prints the path of hack/kind.yaml with the console's
# NodePort on HOST_PORT.
kind_config() {
  local out=$WORK/kind-$HOST_PORT.yaml
  sed "s/hostPort: 8080$/hostPort: $HOST_PORT/" "$ROOT/hack/kind.yaml" > "$out"
  grep -q "hostPort: $HOST_PORT$" "$out"
  echo "$out"
}

# load_images CLUSTER: both images into every node of the cluster.
load_images() {
  kind load docker-image "$IMAGE" --name "$1"
  kind load docker-image "$DB_IMAGE" --name "$1"
}

# db_values: sets DB_VALUES, the chart's values that run the bundled
# database from DB_IMAGE as loaded, never pulled. After build_images.
db_values() {
  DB_VALUES=(--set "database.bundled.image.repository=${DB_IMAGE%:*}" --set "database.bundled.image.tag=${DB_IMAGE##*:}"
    --set database.bundled.image.pullPolicy=Never)
}

# The newest step of the schema this build applies.
last_step() {
  find "$ROOT/server/internal/db/migrations" -name '[0-9]*.sql' -printf '%f\n' | sort | tail -1 | sed 's/^0*\([0-9]*\)_.*/\1/'
}

# check_database URL CURL...: the console at URL, through the signed-in
# curl given (a SuperAdmin's), keeps its records in its database: it
# answers, and its schema is at this build's last step.
check_database() {
  local url=$1 want
  shift
  want=$(last_step)
  for _ in $(seq 30); do
    "$@" "$url/api/v1/database" > "$WORK/database.json" 2>/dev/null && has '"available":true' "$WORK/database.json" && break
    sleep 1
  done
  python3 - "$want" "$WORK/database.json" <<'PY'
import json, sys
want, path = int(sys.argv[1]), sys.argv[2]
d = json.load(open(path))
assert d.get("configured") and d.get("available"), d
assert d.get("schemaVersion") == want, f"schema at step {d.get('schemaVersion')}, want {want}"
print(f"the database answers, its schema at step {want}")
PY
}

# db_sql NAMESPACE POD SQL: a query in the console's database, run as
# PostgreSQL's superuser inside a member of the bundled database; prints
# the rows unaligned, a field separator of |.
db_sql() {
  kubectl -n "$1" exec "$2" -c postgres -- psql -X -q -At -U postgres -d 221b -c "$3"
}
