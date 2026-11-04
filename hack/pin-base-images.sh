#!/usr/bin/env bash
# Writes hack/base-images.env: the Dockerfile's base images and BuildKit
# frontend pinned to the digests their tags name today. CI builds with these;
# a build on a laptop does not, and follows the tags. Run it, and commit the
# file, to move to newer base images.
set -euo pipefail
cd "$(dirname "$0")/.."
pin() {
  docker pull -q "$2" > /dev/null
  printf '%s=%s@%s\n' "$1" "$2" "$(docker image inspect "$2" --format '{{index .RepoDigests 0}}' | sed 's/.*@//')"
}
{
  echo '# Made by hack/pin-base-images.sh: what CI builds with.'
  for arg in NODE_IMAGE GO_IMAGE RUN_IMAGE; do
    pin "$arg" "$(sed -n "s/^ARG $arg=//p" Dockerfile)"
  done
  pin BUILDKIT_SYNTAX "$(sed -n 's/^# syntax=//p' Dockerfile)"
} > hack/base-images.env
cat hack/base-images.env
