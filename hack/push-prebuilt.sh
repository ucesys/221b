#!/usr/bin/env bash
# Builds the image here and pushes it as prebuilt-<tree>, named after the
# commit's tree. CI then checks and tests that build instead of making its
# own, and publishes it as pr-<number> and sha-<commit> (linux/amd64 only;
# releases are always built by CI). Only a committed tree is pushed, so the
# build is exactly what CI tests.
#
#   hack/push-prebuilt.sh                    # docker logged in to ghcr.io
#   DOCKER="sudo DOCKER_BUILDKIT=1 docker" hack/push-prebuilt.sh
#
# Needs a token with write:packages for ghcr.io.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=${IMAGE:-ghcr.io/ucesys/221b}
DOCKER=${DOCKER:-docker}

if [ -n "$(git status --porcelain)" ]; then
  echo "The working tree has changes: commit them first, since CI takes a build only for the tree it was made from." >&2
  exit 1
fi
tree=$(git rev-parse 'HEAD^{tree}')
commit=$(git rev-parse HEAD)
ref="$IMAGE:prebuilt-${tree::12}"

DOCKER_BUILDKIT=1 $DOCKER build \
  --build-arg VERSION="prebuilt-${tree::12}" --build-arg COMMIT="${commit::12}" \
  --label dev.221b.tree="$tree" --label org.opencontainers.image.revision="$commit" \
  -t "$ref" .
$DOCKER push "$ref"
echo "Pushed $ref, the tree of ${commit::12}."
