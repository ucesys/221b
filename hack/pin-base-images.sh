#!/usr/bin/env bash
# Writes hack/base-images.env: the base images of the console's Dockerfile
# and the Go image the bundled database's (deploy/postgres) builds its
# backup program with, the PostgreSQL CI
# runs the tests and the simulator's console against, and the BuildKit
# frontend, pinned to the digests their tags name today. CI builds and runs
# with these; a build on a laptop does not, and follows the tags. The
# workflows' PostgreSQL services name the image outright (a service cannot
# read a file), so their lines are rewritten to the new pin too. Run it, and
# commit what it changed, to move to newer base images; hack/check-base-images.sh
# checks that the pins and the tags agree. The database's base is no
# registry image: apko builds it from deploy/postgres/apko.lock.json, and
# the Dockerfile names it by digest (make postgres-lock moves it).
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
  pin POSTGRES_IMAGE "$(sed -n 's/^POSTGRES_IMAGE ?= //p' Makefile)"
} > hack/base-images.env
postgres=$(sed -n 's/^POSTGRES_IMAGE=//p' hack/base-images.env)
sed -i -E "s|^( +image: )(postgres|pgvector/pgvector):[^ ]+$|\1$postgres|" .github/workflows/*.yml
cat hack/base-images.env
hack/check-base-images.sh
