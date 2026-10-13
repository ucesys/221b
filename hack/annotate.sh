#!/usr/bin/env bash
# Prints the end of a log as GitHub error annotations, so a failed check can
# be read from the pull request without opening the job log. GitHub cuts an
# annotation short at about 4 KB, so the tail goes out in pieces.
#   hack/annotate.sh TITLE FILE [BYTES]
set -euo pipefail
title=$1 file=$2 size=${3:-7000}
[ -s "$file" ] || exit 0
tail -c "$size" "$file" | sed 's/\x1b\[[0-9;]*[A-Za-z]//g' |
  python3 -c '
import sys
body = sys.stdin.read()
title = sys.argv[1]
for a, b in (("%", "%25"), ("\r", ""), ("\n", " "), (":", "%3A"), (",", "%2C")):
    title = title.replace(a, b)
pieces = [body[i:i + 3500] for i in range(0, len(body), 3500)] or [""]
for n, piece in enumerate(pieces, 1):
    for a, b in (("%", "%25"), ("\r", "%0D"), ("\n", "%0A")):
        piece = piece.replace(a, b)
    print("::error title=%s (%d of %d)::%s" % (title, n, len(pieces), piece))
' "$title"
