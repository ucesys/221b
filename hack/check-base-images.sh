#!/usr/bin/env bash
# Checks hack/base-images.env against what it pins: every base image of the
# console's Dockerfile and of deploy/postgres/Dockerfile, the BuildKit
# frontend both name, and the PostgreSQL of the Makefile and of the
# workflows' services, each pinned to a digest of the very tag the file
# names. A tag moved without hack/pin-base-images.sh, or a base image added
# without a pin, fails here. Needs no network.
set -euo pipefail
cd "$(dirname "$0")/.."
fail=0
bad() { echo "hack/base-images.env: $*" >&2; fail=1; }

declare -A PIN=()
while IFS='=' read -r name value; do
  [[ -z "$name" || "$name" == \#* ]] && continue
  [[ "$value" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] || bad "$name is not pinned to a digest: $value"
  PIN[$name]=$value
done < hack/base-images.env

# want NAME TAG WHERE: the pin of NAME is TAG at some digest.
want() {
  if [ -z "${PIN[$1]:-}" ]; then
    bad "$1 (${3}: $2) has no pin"
  elif [ "${PIN[$1]%@*}" != "$2" ]; then
    bad "$1 pins ${PIN[$1]%@*}, but $3 names $2: run hack/pin-base-images.sh"
  fi
}

for dockerfile in Dockerfile deploy/postgres/Dockerfile; do
  while IFS='=' read -r arg tag; do
    want "${arg#ARG }" "$tag" "$dockerfile"
  done < <(grep -E '^ARG [A-Z_]+_IMAGE=' "$dockerfile")
  want BUILDKIT_SYNTAX "$(sed -n 's/^# syntax=//p' "$dockerfile")" "$dockerfile"
done
want POSTGRES_IMAGE "$(sed -n 's/^POSTGRES_IMAGE ?= //p' Makefile)" Makefile

# The workflows' PostgreSQL services name the pin itself.
services=0
while IFS= read -r line; do
  services=$((services + 1))
  image=${line#*image: }
  [ "$image" = "${PIN[POSTGRES_IMAGE]:-}" ] || bad "${line%%:*} runs $image, not POSTGRES_IMAGE (${PIN[POSTGRES_IMAGE]:-none})"
done < <(grep -nE '^ +image: postgres' .github/workflows/*.yml)
[ "$services" -gt 0 ] || bad "no workflow runs a PostgreSQL service"

[ "$fail" = 0 ] || exit 1
echo "hack/base-images.env: ${#PIN[@]} pins, each a digest of the tag its source names; $services PostgreSQL services run the pin."
