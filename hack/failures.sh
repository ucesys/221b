#!/usr/bin/env bash
# Prints the failures from a Playwright log (the list reporter): each failed
# test's heading and the first lines of its error, so a failed check can be
# read from its annotations alone.
#   hack/failures.sh FILE [LINES_PER_FAILURE]
set -euo pipefail
file=$1 lines=${2:-30}
[ -s "$file" ] || exit 0
sed 's/\x1b\[[0-9;]*[A-Za-z]//g' "$file" | awk -v n="$lines" '
  /^ *[0-9]+\) \[/ { printing = n; print; next }
  printing > 0 { print; printing-- }
'
