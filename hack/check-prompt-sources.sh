#!/usr/bin/env bash
# Checks the record of what the agents' prompts drew on (the AI agents
# design, Prompts and sources, step 5): every source: comment in a prompt
# file names a row of docs/prompt-sources.md whose commit is recorded, and
# every row of that table names a license. It reads only these files.
#   hack/check-prompt-sources.sh [prompts-dir] [sources-doc]
# A source comment is one line: <!-- source: <the row's first cell> -->.
set -euo pipefail
cd "$(dirname "$0")/.."
dir=${1:-server/internal/ai/prompts}
doc=${2:-docs/prompt-sources.md}
fail=0
err() { echo "check-prompt-sources: $*" >&2; fail=1; }

[ -f "$doc" ] || { err "$doc is missing"; exit 1; }
# The rows of the table under "## Sources": source, repository, commit,
# license, what we take.
rows=$(awk -F'|' '
	/^## / { insec = ($0 ~ /^## Sources[[:space:]]*$/) }
	insec && /^\|/ && $2 !~ /^[[:space:]]*-+[[:space:]]*$/ {
		for (i = 2; i <= 5; i++) { gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i) }
		if ($2 != "Source") print $2 "\t" $4 "\t" $5
	}' "$doc")
[ -n "$rows" ] || err "$doc has no table of sources under \"## Sources\""
while IFS=$'\t' read -r name commit license; do
	[ -n "$name" ] || continue
	case "$license" in
	"" | "-" | "—" | "to be recorded" | "unknown") err "$doc: the source \"$name\" names no license" ;;
	esac
	[ -n "$commit" ] || err "$doc: the source \"$name\" names no commit or version (write \"to be recorded\")"
done <<<"$rows"

# Every source: comment names a row whose commit is recorded.
n=0
for f in "$dir"/*.md; do
	[ -e "$f" ] || continue
	while IFS= read -r line; do
		src=$(printf '%s\n' "$line" | sed -n 's/.*source:[[:space:]]*\(.*[^[:space:]]\)[[:space:]]*-->.*/\1/p')
		[ -n "$src" ] || { err "$f: a source: comment that names nothing: $line"; continue; }
		n=$((n + 1))
		row=$(printf '%s\n' "$rows" | awk -F'\t' -v s="$src" '$1 == s')
		if [ -z "$row" ]; then
			err "$f: the source \"$src\" has no row in $doc"
		elif [ "$(printf '%s\n' "$row" | cut -f2)" = "to be recorded" ]; then
			err "$f: text copied from \"$src\", whose commit in $doc is still to be recorded"
		fi
	done < <(grep -E '<!--.*source:' "$f" || true)
done
[ "$fail" = 0 ] && echo "check-prompt-sources: $(printf '%s\n' "$rows" | grep -c .) sources recorded, $n source comments in $dir, all with a row and a license"
exit "$fail"
