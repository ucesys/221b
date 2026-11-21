#!/usr/bin/env bash
# Builds 221b-postgres's Wolfi variant (deploy/postgres-wolfi, docs/database.md,
# "The Wolfi variant") and keeps a copy of its packages. Not the default:
# the chart, CI and the release build deploy/postgres, on Debian.
#
#   hack/wolfi-postgres.sh tools          the pinned apko, melange, crane, oras and cosign, into bin/tools
#   hack/wolfi-postgres.sh lock           Wolfi's current packages for the majors of apko.yaml, into apko.lock.json
#   hack/wolfi-postgres.sh base           the base from the lock, pushed to WOLFI_BASE_REPO; its digest
#                                         must be the Dockerfile's WOLFI_BASE_DIGEST
#   hack/wolfi-postgres.sh pin            the same, writing the digest into the Dockerfile (after a new lock)
#   hack/wolfi-postgres.sh image          base, then the image for PLATFORM as DB_WOLFI_IMAGE
#   hack/wolfi-postgres.sh packages       the locked .apk files of both architectures, checked against
#                                         the lock, with an APKINDEX signed by WOLFI_PACKAGES_KEY, and
#                                         WAL-G's release files: bin/wolfi/packages
#   hack/wolfi-postgres.sh push           that copy to WOLFI_PACKAGES_REF as one OCI artifact (oras),
#                                         signed with cosign (COSIGN_KEY, or keyless when empty)
#   hack/wolfi-postgres.sh rebuild-base DIGEST
#                                         the base again from the copy alone: the artifact pulled by
#                                         digest, its signature checked, apko run against it trusting
#                                         only WOLFI_PACKAGES_PUBKEY; the package set must be the lock's
#   hack/wolfi-postgres.sh rebuild DIGEST rebuild-base, then the image from that base
#
# Variables:
#   WOLFI_BASE_REPO        where the base goes (the Dockerfile's default: a registry on 127.0.0.1:5123,
#                          started here from crane when nothing answers)
#   DB_WOLFI_IMAGE         the image's name (ghcr.io/ucesys/221b-postgres:wolfi-dev)
#   PLATFORM               linux/amd64 or linux/arm64 (linux/amd64); neither needs emulation
#   DOCKER                 the docker command ("sudo docker" where docker needs it)
#   WOLFI_PACKAGES_REF     repository:tag of the copy (push), e.g. ghcr.io/ucesys/221b-postgres-packages:0.14.0
#   WOLFI_PACKAGES_REPO    repository the rebuild reads by digest (ghcr.io/ucesys/221b-postgres-packages)
#   WOLFI_PACKAGES_KEY     the RSA key that signs the APKINDEX (packages); never committed
#   WOLFI_PACKAGES_PUBKEY  its public key, the only key a rebuild trusts; its file name is the key's
#                          name in the index (deploy/postgres-wolfi/221b-packages.rsa.pub, the release's)
#   COSIGN_KEY, COSIGN_PUB a cosign key pair; empty signs and verifies keylessly, as CI signs images,
#                          the signer then being COSIGN_IDENTITY (a regexp) from COSIGN_ISSUER
# Registry credentials come from the docker config (DOCKER_CONFIG), as for crane, oras and cosign.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

APKO_VERSION=v1.5.2
APKO_SHA256=b49256442c8dd3031d1ec2ad84383e08b0f25f6fcb58c21cd40fba7df28520d4
MELANGE_VERSION=v0.65.0
MELANGE_SHA256=82340e525934c634d2a15f8f5ae3ac6fe859b0a27e3841465b274cb5508af9d0
CRANE_VERSION=v0.22.1
CRANE_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
ORAS_VERSION=v1.4.0
ORAS_SHA256=d8e1dce2fd8ce996f1d901845b688eb0cf505a6082897606f053825bb47935d1
COSIGN_VERSION=v3.1.4
COSIGN_SHA256=424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa

DIR=$ROOT/deploy/postgres-wolfi
CONFIG=$DIR/apko.yaml
LOCK=$DIR/apko.lock.json
DOCKERFILE=$DIR/Dockerfile
OUT=$ROOT/bin/wolfi
mkdir -p "$OUT"

# arg NAME: the default of ARG NAME in the Dockerfile.
arg() { sed -n "s/^ARG $1=//p" "$DOCKERFILE"; }

WOLFI_BASE_REPO=${WOLFI_BASE_REPO:-$(arg WOLFI_BASE_REPO)}
DB_WOLFI_IMAGE=${DB_WOLFI_IMAGE:-ghcr.io/ucesys/221b-postgres:wolfi-dev}
PLATFORM=${PLATFORM:-linux/amd64}
DOCKER=${DOCKER:-docker}
WOLFI_PACKAGES_REPO=${WOLFI_PACKAGES_REPO:-ghcr.io/ucesys/221b-postgres-packages}
WOLFI_PACKAGES_PUBKEY=${WOLFI_PACKAGES_PUBKEY:-$DIR/221b-packages.rsa.pub}
COSIGN_KEY=${COSIGN_KEY:-}
COSIGN_PUB=${COSIGN_PUB:-}
COSIGN_IDENTITY=${COSIGN_IDENTITY:-^https://github.com/ucesys/221b/\.github/workflows/}
COSIGN_ISSUER=${COSIGN_ISSUER:-https://token.actions.githubusercontent.com}

tools() {
  fetch "https://github.com/chainguard-dev/apko/releases/download/$APKO_VERSION/apko_${APKO_VERSION#v}_linux_amd64.tar.gz" "$TOOLS/apko.tar.gz" "$APKO_SHA256"
  tar -xzf "$TOOLS/apko.tar.gz" -C "$TOOLS" --strip-components 1 "apko_${APKO_VERSION#v}_linux_amd64/apko"
  fetch "https://github.com/chainguard-dev/melange/releases/download/$MELANGE_VERSION/melange_${MELANGE_VERSION#v}_linux_amd64.tar.gz" "$TOOLS/melange.tar.gz" "$MELANGE_SHA256"
  tar -xzf "$TOOLS/melange.tar.gz" -C "$TOOLS" --strip-components 1 "melange_${MELANGE_VERSION#v}_linux_amd64/melange"
  fetch "https://github.com/google/go-containerregistry/releases/download/$CRANE_VERSION/go-containerregistry_Linux_x86_64.tar.gz" "$TOOLS/crane.tar.gz" "$CRANE_SHA256"
  tar -xzf "$TOOLS/crane.tar.gz" -C "$TOOLS" crane
  fetch "https://github.com/oras-project/oras/releases/download/$ORAS_VERSION/oras_${ORAS_VERSION#v}_linux_amd64.tar.gz" "$TOOLS/oras.tar.gz" "$ORAS_SHA256"
  tar -xzf "$TOOLS/oras.tar.gz" -C "$TOOLS" oras
  fetch "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/cosign-linux-amd64" "$TOOLS/cosign" "$COSIGN_SHA256"
  chmod +x "$TOOLS/cosign"
}

# The registry the base goes to on a laptop: crane's, on 127.0.0.1 (port
# 5123 unless WOLFI_BASE_REPO names another), its blobs on disk under
# bin/wolfi. Docker pulls from 127.0.0.1 over plain HTTP.
local_registry() {
  [[ "$WOLFI_BASE_REPO" == 127.0.0.1:*/* ]] || return 0
  local addr=${WOLFI_BASE_REPO%%/*}
  local data=$OUT/registry-${addr#*:}
  curl -fs "http://$addr/v2/" > /dev/null && return 0
  mkdir -p "$data"
  nohup crane registry serve --address "$addr" --disk "$data" > "$data.log" 2>&1 &
  for _ in $(seq 20); do curl -fs "http://$addr/v2/" > /dev/null && return 0; sleep 0.5; done
  echo "the registry on $addr did not start: $data.log" >&2
  return 1
}

lock() {
  apko lock "$CONFIG" --output "$LOCK"
  echo "$LOCK: $(grep -c '"name"' "$LOCK") entries"
}

# build_base: the base from the lock, both architectures in one index,
# pushed; prints its digest. apko gives the same digest for the same lock.
build_base() {
  local_registry
  local tag
  tag=lock-$(sha256sum "$LOCK" | cut -c1-12)
  apko publish "$CONFIG" "$WOLFI_BASE_REPO:$tag" --lockfile "$LOCK" --sbom-path "$OUT" >&2
  crane digest "$WOLFI_BASE_REPO:$tag"
}

base() {
  local got want
  got=$(build_base)
  want=$(arg WOLFI_BASE_DIGEST)
  if [ "$got" != "$want" ]; then
    echo "the base from apko.lock.json is $got, the Dockerfile names $want: run hack/wolfi-postgres.sh pin after a new lock" >&2
    return 1
  fi
  echo "$WOLFI_BASE_REPO@$got"
}

pin() {
  local got
  got=$(build_base)
  sed -i "s|^ARG WOLFI_BASE_DIGEST=.*|ARG WOLFI_BASE_DIGEST=$got|" "$DOCKERFILE"
  echo "$DOCKERFILE: WOLFI_BASE_DIGEST=$got"
}

# walg DIR: WAL-G's release files for both architectures in DIR, fetched
# unless WALG_DIR has them, each checked against the Dockerfile's digest.
walg() {
  local dir=$1 version a sum
  version=$(arg WALG_VERSION)
  mkdir -p "$dir"
  for a in amd64 aarch64; do
    if [ "$a" = amd64 ]; then sum=$(arg WALG_SHA256_AMD64); else sum=$(arg WALG_SHA256_ARM64); fi
    if [ -n "${WALG_DIR:-}" ]; then
      cp "$WALG_DIR/wal-g-pg-24.04-$a" "$dir/wal-g-pg-24.04-$a"
      echo "$sum  $dir/wal-g-pg-24.04-$a" | sha256sum -c --quiet -
    else
      fetch "https://github.com/wal-g/wal-g/releases/download/$version/wal-g-pg-24.04-$a" "$dir/wal-g-pg-24.04-$a" "$sum"
    fi
  done
}

# image [DIGEST]: the image for PLATFORM from the base at DIGEST (the
# Dockerfile's, checked by building it, when none is given). The context is
# deploy/postgres's scripts and backup program, as the Debian image's, and
# WAL-G's release files.
image() {
  local digest=${1:-}
  if [ -z "$digest" ]; then
    digest=$(base)
    digest=${digest##*@}
  fi
  local ctx=$OUT/context
  rm -rf "$ctx"
  mkdir -p "$ctx"
  cp -r "$ROOT/deploy/postgres/entrypoint.sh" "$ROOT/deploy/postgres/post-bootstrap.sh" "$ROOT/deploy/postgres/restore.sh" "$ROOT/deploy/postgres/backup" "$ctx/"
  walg "$ctx/wal-g"
  local go_image
  go_image=$(sed -n 's/^GO_IMAGE=//p' "$ROOT/hack/base-images.env")
  # BuildKit, through sudo too.
  local d docker
  read -ra d <<< "$DOCKER"
  docker=(env DOCKER_BUILDKIT=1 "${d[@]}")
  [ "${d[0]}" = sudo ] && docker=(sudo env DOCKER_BUILDKIT=1 "${d[@]:1}")
  "${docker[@]}" build --platform "$PLATFORM" -f "$DOCKERFILE" \
    --build-arg GO_IMAGE="$go_image" --build-arg WOLFI_BASE_REPO="$WOLFI_BASE_REPO" --build-arg WOLFI_BASE_DIGEST="$digest" \
    -t "$DB_WOLFI_IMAGE" "$ctx"
  echo "$DB_WOLFI_IMAGE ($PLATFORM) from $WOLFI_BASE_REPO@$digest"
}

packages() {
  : "${WOLFI_PACKAGES_KEY:?the RSA key that signs the index (melange keygen); never committed}"
  local repo=$OUT/packages
  rm -rf "$repo"
  mkdir -p "$repo"
  # Each locked package, downloaded once and checked against the lock: the
  # hash of its control part (apk's checksum) and of its data part.
  python3 - "$LOCK" "$repo" <<'PY'
import base64, hashlib, json, os, sys, urllib.request
lock, repo = sys.argv[1], sys.argv[2]
for p in json.load(open(lock))["contents"]["packages"]:
    d = os.path.join(repo, p["architecture"])
    os.makedirs(d, exist_ok=True)
    f = os.path.join(d, os.path.basename(p["url"]))
    with urllib.request.urlopen(p["url"]) as r, open(f, "wb") as out:
        out.write(r.read())
    data = open(f, "rb").read()
    for part, algo in (("control", "sha1"), ("data", "sha256")):
        lo, hi = (int(x) for x in p[part]["range"].removeprefix("bytes=").split("-"))
        want = p[part]["checksum"].split("-", 1)[1]
        got = base64.b64encode(hashlib.new(algo, data[lo:hi + 1]).digest()).decode()
        if got != want:
            sys.exit(f"{f}: its {part} part is not the lock's ({got} != {want})")
    if "Q1" + p["control"]["checksum"].split("-", 1)[1] != p["checksum"]:
        sys.exit(f"{f}: the lock's checksum and its control hash disagree")
print(f"{sum(1 for _ in json.load(open(lock))['contents']['packages'])} packages checked against the lock")
PY
  local arch
  for arch in x86_64 aarch64; do
    melange index --arch "$arch" --signing-key "$WOLFI_PACKAGES_KEY" -o "$repo/$arch/APKINDEX.tar.gz" "$repo/$arch"/*.apk
    echo "$arch: $(find "$repo/$arch" -name '*.apk' | wc -l) packages, $(du -sb "$repo/$arch" | cut -f1) bytes"
  done
  walg "$repo/wal-g"
  cp "$LOCK" "$repo/apko.lock.json"
}

push() {
  : "${WOLFI_PACKAGES_REF:?repository:tag of the copy, e.g. ghcr.io/ucesys/221b-postgres-packages:0.14.0}"
  local repo=$OUT/packages digest
  [ -f "$repo/apko.lock.json" ] || { echo "run hack/wolfi-postgres.sh packages first" >&2; return 1; }
  # Directories go as one tar.gz layer each.
  digest=$(cd "$repo" && oras push "$WOLFI_PACKAGES_REF" --artifact-type application/vnd.ucesys.221b.postgres-packages.v1 \
    --annotation "org.opencontainers.image.source=https://github.com/ucesys/221b" \
    --annotation "org.opencontainers.image.description=The Wolfi packages of a 221b-postgres Wolfi build, with an APKINDEX signed by 221B's key" \
    --format go-template='{{.digest}}' \
    apko.lock.json:application/vnd.ucesys.221b.apko-lock.v1+json x86_64 aarch64 wal-g)
  echo "$digest" > "$OUT/packages.digest"
  local ref=${WOLFI_PACKAGES_REF%:*}@$digest
  if [ -n "$COSIGN_KEY" ]; then
    # A key pair of our own: a signing config without services, so nothing
    # goes to the public transparency log or a timestamp authority.
    cosign signing-config create --out "$OUT/signing-config-keyed.json"
    cosign sign --yes --key "$COSIGN_KEY" --signing-config "$OUT/signing-config-keyed.json" "$ref"
  else
    # As the release job does: keyless, the workflow's identity in the
    # certificate, recorded in the transparency log.
    cosign sign --yes "$ref"
  fi
  echo "$ref"
}

verify_signature() {
  if [ -n "$COSIGN_PUB" ]; then
    cosign verify --key "$COSIGN_PUB" --insecure-ignore-tlog=true "$1" > /dev/null
  else
    cosign verify --certificate-identity-regexp "$COSIGN_IDENTITY" --certificate-oidc-issuer "$COSIGN_ISSUER" "$1" > /dev/null
  fi
  echo "the signature of $1 is good"
}

# same_as_lock IMAGE: every architecture's installed packages (name,
# version, checksum) are exactly the lock's.
same_as_lock() {
  local platform arch
  for platform in linux/amd64 linux/arm64; do
    arch=x86_64
    [ "$platform" = linux/arm64 ] && arch=aarch64
    crane export --platform "$platform" "$1" - | tar -xO --wildcards "*lib/apk/db/installed" > "$OUT/rebuild/installed-$arch"
    python3 - "$LOCK" "$OUT/rebuild/installed-$arch" "$arch" <<'PY'
import json, sys
lock, installed, arch = sys.argv[1:]
want = {(p["name"], p["version"], p["checksum"]) for p in json.load(open(lock))["contents"]["packages"] if p["architecture"] == arch}
got, cur = set(), {}
for line in open(installed).read().split("\n") + [""]:
    if not line:
        if cur:
            got.add((cur["P"], cur["V"], cur["C"]))
        cur = {}
    elif line[1:2] == ":" and line[0] in "PVC" and line[0] not in cur:
        cur[line[0]] = line[2:]
if got != want:
    sys.exit(f"{arch}: the rebuilt package set is not the lock's: missing {sorted(want - got)}, extra {sorted(got - want)}")
print(f"{arch}: {len(got)} packages installed, exactly the lock's (name, version, checksum)")
PY
  done
}

rebuild_base() {
  local digest=${1:?the digest of the copy, sha256:...}
  local ref=$WOLFI_PACKAGES_REPO@$digest work=$OUT/rebuild
  [ -f "$WOLFI_PACKAGES_PUBKEY" ] || { echo "no public key at $WOLFI_PACKAGES_PUBKEY (WOLFI_PACKAGES_PUBKEY)" >&2; return 1; }
  rm -rf "$work"
  mkdir -p "$work/keys" "$work/cache"
  verify_signature "$ref"
  oras pull "$ref" -o "$work/copy"
  # The copy is of this tree's lock, or it is the wrong copy.
  cmp "$work/copy/apko.lock.json" "$LOCK" || { echo "$ref holds another lock than $LOCK" >&2; return 1; }
  # Only our key: Wolfi's is not given, and the copy's index is signed
  # with ours.
  cp "$WOLFI_PACKAGES_PUBKEY" "$work/keys/"
  local pins
  pins=$(python3 - "$LOCK" <<'PY'
import json, sys
by = {}
for p in json.load(open(sys.argv[1]))["contents"]["packages"]:
    by.setdefault(p["architecture"], set()).add((p["name"], p["version"]))
sets = list(by.values())
if any(s != sets[0] for s in sets):
    sys.exit("the architectures' versions differ; build them one at a time")
for name, version in sorted(sets[0]):
    print(f"    - {name}={version}")
PY
)
  # apko.yaml with the copy for the repository, our key for the keyring and
  # every locked package at its locked version.
  awk -v repo="$work/copy" -v key="$work/keys/$(basename "$WOLFI_PACKAGES_PUBKEY")" -v pins="$pins" '
    /^  repositories:$/ { print; print "    - " repo; skip = 1; next }
    /^  keyring:$/ { print; print "    - " key; skip = 1; next }
    /^  packages:$/ { print; print pins; skip = 1; next }
    skip && /^    / { next }
    skip && /^ *#/ { next }
    { skip = 0; print }' "$CONFIG" > "$work/apko.yaml"
  if grep -q "https://packages.wolfi.dev" "$work/apko.yaml"; then
    echo "the rebuild's config still names packages.wolfi.dev" >&2
    return 1
  fi
  local_registry
  apko publish "$work/apko.yaml" "$WOLFI_BASE_REPO:rebuild-${digest#sha256:}" --cache-dir "$work/cache" --sbom-path "$work" >&2
  local base
  base=$(crane digest "$WOLFI_BASE_REPO:rebuild-${digest#sha256:}")
  same_as_lock "$WOLFI_BASE_REPO@$base"
  # The base as an OCI layout too, to take it to another registry with
  # crane push (the digest stays).
  crane pull --format oci "$WOLFI_BASE_REPO@$base" "$work/base-oci"
  echo "$base" > "$work/base.digest"
  echo "the base rebuilt from $ref alone: $WOLFI_BASE_REPO@$base"
}

cmd=${1:-}
shift || true
case "$cmd" in
  tools) tools ;;
  lock) tools; lock ;;
  base) tools; base ;;
  pin) tools; pin ;;
  image) tools; image "$@" ;;
  packages) tools; packages ;;
  push) tools; push ;;
  rebuild-base) tools; rebuild_base "$@" ;;
  rebuild)
    tools
    rebuild_base "$@"
    WALG_DIR=$OUT/rebuild/copy/wal-g image "$(cat "$OUT/rebuild/base.digest")"
    ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
