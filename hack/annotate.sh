#!/usr/bin/env bash
# Prints the end of a log as a GitHub error annotation, so a failed check can
# be read from the pull request without opening the job log.
#   hack/annotate.sh TITLE FILE [BYTES]
set -euo pipefail
title=$1 file=$2 size=${3:-6000}
[ -s "$file" ] || exit 0
tail -c "$size" "$file" | sed 's/\x1b\[[0-9;]*[A-Za-z]//g' |
  python3 -c '
import sys
body = sys.stdin.read()
for a, b in (("%", "%25"), ("\r", "%0D"), ("\n", "%0A")):
    body = body.replace(a, b)
title = sys.argv[1]
for a, b in (("%", "%25"), ("\r", ""), ("\n", " "), (":", "%3A"), (",", "%2C")):
    title = title.replace(a, b)
print("::error title=%s::%s" % (title, body))
' "$title"
