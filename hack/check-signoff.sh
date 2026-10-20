#!/usr/bin/env bash
# Checks that every commit of a pull request, other than merges, carries a
# Developer Certificate of Origin sign-off by its author (CONTRIBUTING.md):
# a "Signed-off-by:" line with the author's email. CI runs it on pull
# requests from forks.
#   hack/check-signoff.sh BASE_SHA HEAD_SHA
set -euo pipefail
base=$1
head=$2
missing=0
while read -r sha; do
  email=$(git log -1 --format=%ae "$sha")
  if ! git log -1 --format=%B "$sha" | grep -qiE "^Signed-off-by: .*<${email//./\\.}>\s*$"; then
    echo "::error::$(git log -1 --format='%h %s' "$sha"): no Signed-off-by line for $email. Sign off with: git rebase --signoff $base"
    missing=$((missing + 1))
  fi
done < <(git rev-list --no-merges "$base..$head")
if [ "$missing" -gt 0 ]; then
  echo "$missing commit(s) without a sign-off; see CONTRIBUTING.md."
  exit 1
fi
echo "ok: every commit is signed off"
