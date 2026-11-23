#!/usr/bin/env bash
# Builds 221b-postgres, the bundled database's image (deploy/postgres,
# docs/database.md, "The image"), from Wolfi's packages, and keeps a copy of
# those packages so that an old release can be built again once Wolfi has
# deleted them.
#
#   hack/wolfi-postgres.sh tools          the pinned apko, melange, crane, oras and cosign, into bin/tools
#   hack/wolfi-postgres.sh lock           Wolfi's current packages for the majors of apko.yaml, into apko.lock.json
#   hack/wolfi-postgres.sh base           the base from the lock, both architectures, pushed to WOLFI_BASE_REPO;
#                                         its digest must be the Dockerfile's WOLFI_BASE_DIGEST
#   hack/wolfi-postgres.sh pin            the same, writing the digest into the Dockerfile (after a new lock)
#   hack/wolfi-postgres.sh image [DIGEST] base (or the base at DIGEST), then the image for PLATFORM as
#                                         DB_IMAGE, in the local docker
#   hack/wolfi-postgres.sh publish REF... base, then the image for linux/amd64 and linux/arm64 as one index,
#                                         pushed under every REF (repository:tag); no emulation needed
#   hack/wolfi-postgres.sh keygen DIR NAME
#                                         a new RSA key pair for the index, DIR/NAME.rsa and DIR/NAME.rsa.pub
#   hack/wolfi-postgres.sh packages       the locked .apk files of both architectures, checked against the
#                                         lock, with an APKINDEX signed by WOLFI_PACKAGES_KEY, its public
#                                         key (WOLFI_PACKAGES_KEY.pub) and WAL-G's release files:
#                                         bin/wolfi/packages
#   hack/wolfi-postgres.sh push           that copy to WOLFI_PACKAGES_REF as one OCI artifact (oras), signed
#                                         with cosign: keyless (the release job's identity), or COSIGN_KEY
#   hack/wolfi-postgres.sh verify REF     the copy's signature: REF is a digest or a tag of WOLFI_PACKAGES_REPO
#   hack/wolfi-postgres.sh rebuild-base REF
#                                         the base again from the copy alone: the signature checked, the
#                                         artifact pulled by digest, apko run against it trusting only the
#                                         public key the copy carries; the package set must be the lock's
#   hack/wolfi-postgres.sh rebuild REF    rebuild-base, then the image from that base
#
# Variables:
#   WOLFI_BASE_REPO        where the base goes (the Dockerfile's default: a registry on 127.0.0.1:5123,
#                          started here from crane when nothing answers)
#   DB_IMAGE               the image's name for image and rebuild (ghcr.io/ucesys/221b-postgres:dev)
#   DB_LABELS              more labels for the image, one key=value a line
#   PLATFORM               linux/amd64 or linux/arm64, for image (linux/amd64); neither needs emulation
#   DOCKER                 the docker command ("sudo docker" where docker needs it)
#   WOLFI_PACKAGES_REF     repository:tag of the copy (push), e.g. ghcr.io/ucesys/221b-postgres-packages:0.13.0
#   WOLFI_PACKAGES_REPO    repository verify and the rebuild read (ghcr.io/ucesys/221b-postgres-packages)
#   WOLFI_PACKAGES_KEY     the RSA key that signs the index (packages); its public key is WOLFI_PACKAGES_KEY.pub.
#                          A release makes a new pair in its job and keeps neither (docs/releasing.md)
#   WOLFI_PACKAGES_PUBKEY  for tests: the key a rebuild trusts instead of the one in the copy
#   COSIGN_KEY, COSIGN_PUB for tests: a cosign key pair to sign and verify the copy with; empty signs
#                          keylessly and verifies the signer's certificate: COSIGN_IDENTITY (a regexp,
#                          the release workflow at a version tag) from COSIGN_ISSUER (GitHub Actions)
# Registry credentials come from the docker config (DOCKER_CONFIG), as for crane, oras and cosign.
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

# Releases at least two weeks old, each checked by its SHA-256.
APKO_VERSION=v1.4.17
APKO_SHA256=cb5f43875c93664297d4d0196081c7149c46315d7ea6eb6acb5b1ebb0511d1cc
MELANGE_VERSION=v0.64.0
MELANGE_SHA256=d8ae32eea4e0dc390e4a1e7d7a7740cf10f4a84675b792a2ff5417097e4ce317
CRANE_VERSION=v0.22.1
CRANE_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
ORAS_VERSION=v1.3.6
ORAS_SHA256=66fb0ad7206f3f6fa77ed5b552049d364ccf4b50b1684554f731f76302b52ca0
COSIGN_VERSION=v3.1.4
COSIGN_SHA256=424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa

# Paths from the repository's root: apko writes the config's path into the
# lock, and it must not be anyone's home.
REL=deploy/postgres
DIR=$ROOT/$REL
CONFIG=$DIR/apko.yaml
LOCK=$DIR/apko.lock.json
DOCKERFILE=$DIR/Dockerfile
OUT=$ROOT/bin/wolfi
mkdir -p "$OUT"

# arg NAME: the default of ARG NAME in the Dockerfile.
arg() { sed -n "s/^ARG $1=//p" "$DOCKERFILE"; }

WOLFI_BASE_REPO=${WOLFI_BASE_REPO:-$(arg WOLFI_BASE_REPO)}
DB_IMAGE=${DB_IMAGE:-ghcr.io/ucesys/221b-postgres:dev}
DB_LABELS=${DB_LABELS:-}
PLATFORM=${PLATFORM:-linux/amd64}
DOCKER=${DOCKER:-docker}
WOLFI_PACKAGES_REPO=${WOLFI_PACKAGES_REPO:-ghcr.io/ucesys/221b-postgres-packages}
WOLFI_PACKAGES_PUBKEY=${WOLFI_PACKAGES_PUBKEY:-}
COSIGN_KEY=${COSIGN_KEY:-}
COSIGN_PUB=${COSIGN_PUB:-}
COSIGN_IDENTITY=${COSIGN_IDENTITY:-^https://github\.com/ucesys/221b/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$}
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

# The registry the base goes to on a laptop and in CI: crane's, on
# 127.0.0.1 (port 5123 unless WOLFI_BASE_REPO names another), its blobs on
# disk under bin/wolfi. Docker pulls from 127.0.0.1 over plain HTTP.
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
  (cd "$ROOT" && apko lock "$REL/apko.yaml" --output "$REL/apko.lock.json")
  echo "$LOCK: $(grep -c '"name"' "$LOCK") entries"
}

# build_base: the base from the lock, both architectures in one index,
# pushed; prints its digest. apko gives the same digest for the same lock.
build_base() {
  local_registry
  local tag
  tag=lock-$(sha256sum "$LOCK" | cut -c1-12)
  (cd "$ROOT" && apko publish --vcs=false "$REL/apko.yaml" "$WOLFI_BASE_REPO:$tag" --lockfile "$REL/apko.lock.json" --sbom-path "$OUT") >&2
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

# context: the build context in bin/wolfi/context, the scripts and the
# backup program beside the Dockerfile and WAL-G's release files.
context() {
  local ctx=$OUT/context
  rm -rf "$ctx"
  mkdir -p "$ctx"
  cp -r "$DIR/entrypoint.sh" "$DIR/post-bootstrap.sh" "$DIR/extensions.sh" "$DIR/restore.sh" "$DIR/backup" "$ctx/"
  walg "$ctx/wal-g"
}

# build_args DIGEST: sets BUILD_ARGS, what both builds pass: the Go image
# and the BuildKit frontend pinned in hack/base-images.env, the base and the
# labels of DB_LABELS.
build_args() {
  local line
  BUILD_ARGS=(-f "$DOCKERFILE"
    --build-arg GO_IMAGE="$(sed -n 's/^GO_IMAGE=//p' "$ROOT/hack/base-images.env")"
    --build-arg BUILDKIT_SYNTAX="$(sed -n 's/^BUILDKIT_SYNTAX=//p' "$ROOT/hack/base-images.env")"
    --build-arg WOLFI_BASE_REPO="$WOLFI_BASE_REPO" --build-arg WOLFI_BASE_DIGEST="$1")
  while IFS= read -r line; do
    [ -n "$line" ] && BUILD_ARGS+=(--label "$line")
  done <<< "$DB_LABELS"
  return 0
}

# docker_cmd: sets DOCKER_CMD, the docker command with BuildKit on, through
# sudo too.
docker_cmd() {
  local d
  read -ra d <<< "$DOCKER"
  DOCKER_CMD=(env DOCKER_BUILDKIT=1 "${d[@]}")
  if [ "${d[0]}" = sudo ]; then DOCKER_CMD=(sudo env DOCKER_BUILDKIT=1 "${d[@]:1}"); fi
}

# image [DIGEST]: the image for PLATFORM from the base at DIGEST (the
# Dockerfile's, checked by building it, when none is given).
image() {
  local digest=${1:-}
  if [ -z "$digest" ]; then
    digest=$(base)
    digest=${digest##*@}
  fi
  context
  build_args "$digest"
  docker_cmd
  "${DOCKER_CMD[@]}" build --platform "$PLATFORM" "${BUILD_ARGS[@]}" -t "$DB_IMAGE" "$OUT/context"
  echo "$DB_IMAGE ($PLATFORM) from $WOLFI_BASE_REPO@$digest"
}

# publish REF...: both architectures as one index, pushed under each REF.
# BuildKit runs in a container of its own on the host's network, so it
# reaches the base's registry on 127.0.0.1, told that it speaks plain HTTP.
publish() {
  [ $# -gt 0 ] || { echo "publish needs at least one repository:tag" >&2; return 1; }
  local digest builder=postgres-221b r
  digest=$(base)
  digest=${digest##*@}
  context
  build_args "$digest"
  docker_cmd
  printf '[registry."%s"]\n  http = true\n  insecure = true\n' "${WOLFI_BASE_REPO%%/*}" > "$OUT/buildkitd.toml"
  if ! "${DOCKER_CMD[@]}" buildx inspect "$builder" > /dev/null 2>&1; then
    "${DOCKER_CMD[@]}" buildx create --name "$builder" --driver docker-container \
      --driver-opt network=host --buildkitd-config "$OUT/buildkitd.toml" > /dev/null
  fi
  local tags=()
  for r in "$@"; do tags+=(-t "$r"); done
  "${DOCKER_CMD[@]}" buildx build --builder "$builder" --platform linux/amd64,linux/arm64 \
    "${BUILD_ARGS[@]}" "${tags[@]}" --push "$OUT/context"
  echo "$* (linux/amd64, linux/arm64) from $WOLFI_BASE_REPO@$digest"
}

keygen() {
  local dir=${1:?the directory of the new key pair} name=${2:?its name, e.g. 221b-packages-0.13.0}
  mkdir -p "$dir"
  (umask 077 && melange keygen "$dir/$name.rsa")
  echo "$dir/$name.rsa and $dir/$name.rsa.pub"
}

packages() {
  : "${WOLFI_PACKAGES_KEY:?the RSA key that signs the index (hack/wolfi-postgres.sh keygen); never committed}"
  [ -f "$WOLFI_PACKAGES_KEY.pub" ] || { echo "no public key at $WOLFI_PACKAGES_KEY.pub" >&2; return 1; }
  local repo=$OUT/packages
  rm -rf "$repo"
  mkdir -p "$repo/keys"
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
  # The index names its key by the public key's file name: the copy
  # carries it under that name, and a rebuild trusts it and nothing else.
  cp "$WOLFI_PACKAGES_KEY.pub" "$repo/keys/"
  walg "$repo/wal-g"
  cp "$LOCK" "$repo/apko.lock.json"
}

push() {
  : "${WOLFI_PACKAGES_REF:?repository:tag of the copy, e.g. ghcr.io/ucesys/221b-postgres-packages:0.13.0}"
  local repo=$OUT/packages digest
  [ -f "$repo/apko.lock.json" ] || { echo "run hack/wolfi-postgres.sh packages first" >&2; return 1; }
  # Directories go as one tar.gz layer each.
  digest=$(cd "$repo" && oras push "$WOLFI_PACKAGES_REF" --artifact-type application/vnd.ucesys.221b.postgres-packages.v1 \
    --annotation "org.opencontainers.image.source=https://github.com/ucesys/221b" \
    --annotation "org.opencontainers.image.description=The Wolfi packages of a 221b-postgres build, with an APKINDEX signed by the key under keys/" \
    --format go-template='{{.digest}}' \
    apko.lock.json:application/vnd.ucesys.221b.apko-lock.v1+json keys x86_64 aarch64 wal-g)
  echo "$digest" > "$OUT/packages.digest"
  local ref=${WOLFI_PACKAGES_REF%:*}@$digest
  if [ -n "$COSIGN_KEY" ]; then
    # A key pair of our own, for tests: a signing config without services,
    # so nothing goes to the public transparency log or a timestamp
    # authority.
    cosign signing-config create --out "$OUT/signing-config-keyed.json"
    cosign sign --yes --key "$COSIGN_KEY" --signing-config "$OUT/signing-config-keyed.json" "$ref"
  else
    # As the release job does: keyless, the workflow's identity in the
    # certificate, recorded in the transparency log.
    cosign sign --yes "$ref"
  fi
  echo "$ref"
}

# resolve REF: the digest of REF, a digest already or a tag of
# WOLFI_PACKAGES_REPO.
resolve() {
  local ref=${1:?a digest (sha256:...) or a tag of $WOLFI_PACKAGES_REPO}
  if [[ "$ref" == sha256:* ]]; then echo "$ref"; else crane digest "$WOLFI_PACKAGES_REPO:$ref"; fi
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
  local digest ref work=$OUT/rebuild key
  digest=$(resolve "${1:-}")
  ref=$WOLFI_PACKAGES_REPO@$digest
  rm -rf "$work"
  mkdir -p "$work/keys" "$work/cache"
  # The signature first: it vouches for the manifest, the manifest for
  # every layer by digest, and so for the key the copy carries.
  verify_signature "$ref"
  oras pull "$ref" -o "$work/copy"
  # The copy is of this tree's lock, or it is the wrong copy.
  cmp "$work/copy/apko.lock.json" "$LOCK" || { echo "$ref holds another lock than $LOCK" >&2; return 1; }
  if [ -n "$WOLFI_PACKAGES_PUBKEY" ]; then
    key=$WOLFI_PACKAGES_PUBKEY
  else
    local keys=("$work/copy/keys"/*.pub)
    if [ "${#keys[@]}" != 1 ] || [ ! -f "${keys[0]}" ]; then
      echo "$ref carries no single public key under keys/" >&2
      return 1
    fi
    key=${keys[0]}
  fi
  # Only that key: Wolfi's is not given, and the copy's index is signed
  # with it.
  cp "$key" "$work/keys/"
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
  # apko.yaml with the copy for the repository, the key for the keyring and
  # every locked package at its locked version.
  awk -v repo="$work/copy" -v key="$work/keys/$(basename "$key")" -v pins="$pins" '
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
  apko publish --vcs=false "$work/apko.yaml" "$WOLFI_BASE_REPO:rebuild-${digest#sha256:}" --cache-dir "$work/cache" --sbom-path "$work" >&2
  local base
  base=$(crane digest "$WOLFI_BASE_REPO:rebuild-${digest#sha256:}")
  same_as_lock "$WOLFI_BASE_REPO@$base"
  # The base as an OCI layout too, to take it to another registry with
  # crane push (the digest stays).
  crane pull --format oci "$WOLFI_BASE_REPO@$base" "$work/base-oci"
  echo "$base" > "$work/base.digest"
  echo "the base rebuilt from $ref alone, trusting $(basename "$key"): $WOLFI_BASE_REPO@$base"
}

cmd=${1:-}
shift || true
case "$cmd" in
  tools) tools ;;
  lock) tools; lock ;;
  base) tools; base ;;
  pin) tools; pin ;;
  image) tools; image "$@" ;;
  publish) tools; publish "$@" ;;
  keygen) tools; keygen "$@" ;;
  packages) tools; packages ;;
  push) tools; push ;;
  verify) tools; verify_signature "$WOLFI_PACKAGES_REPO@$(resolve "${1:-}")" ;;
  rebuild-base) tools; rebuild_base "$@" ;;
  rebuild)
    tools
    rebuild_base "$@"
    WALG_DIR=$OUT/rebuild/copy/wal-g image "$(cat "$OUT/rebuild/base.digest")"
    ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
