#!/usr/bin/env bash
# Checks the bundled database's backup container (deploy/postgres/backup.sh)
# without a database: Patroni's answer, WAL-G and the clock's 30 seconds are
# stand-ins, and the script is run as the container runs it. What is checked
# is who takes the base backup and when, and that it stops when told to.
#   hack/check-db-backup.sh
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
script=$PWD/deploy/postgres/backup.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/vol"
fail=0

cat > "$tmp/bin/curl" <<EOF
#!/bin/bash
cat "$tmp/cluster.json" 2>/dev/null || exit 7
EOF
cat > "$tmp/bin/wal-g" <<EOF
#!/bin/bash
echo "\$1" >> "$tmp/calls"
case "\$1" in
backup-list) cat "$tmp/list.json" 2>/dev/null || echo '[]' ;;
backup-push)
	/bin/sleep "\${PUSH_SECONDS:-0}"
	echo "[{\"start_time\":\"\$(date -u +%Y-%m-%dT%H:%M:%S.5Z)\"}]" > "$tmp/list.json"
	;;
esac
EOF
# The loop's 30 seconds pass in a fraction of one.
cat > "$tmp/bin/sleep" <<'EOF'
#!/bin/bash
exec /bin/sleep "${NAP:-0.2}"
EOF
chmod +x "$tmp"/bin/*

alone='{"members":[{"name":"db-0","role":"leader","state":"running","timeline":TL}]}'
three='{"members":[{"name":"db-0","role":"leader","state":"running","timeline":TL},{"name":"db-1","role":"sync_standby","state":"streaming","timeline":TL},{"name":"db-2","role":"replica","state":"streaming","timeline":TL}]}'
cluster() { printf '%s\n' "${1//TL/$2}" > "$tmp/cluster.json"; }
reset() { rm -f "$tmp/calls" "$tmp/list.json" "$tmp/out"; }

# run <seconds> [VAR=value...]: the container, stopped with SIGTERM after
# that long. The time it took to stop is left in $took, in milliseconds.
run() {
	local secs=$1
	shift
	env PATH="$tmp/bin:$PATH" PGDATA=/nowhere PATRONI_NAME=db-0 "$@" bash "$script" > "$tmp/out" 2>&1 &
	local pid=$!
	/bin/sleep "$secs"
	local t0
	t0=$(date +%s%N)
	kill -TERM "$pid"
	wait "$pid"
	code=$?
	took=$((($(date +%s%N) - t0) / 1000000))
}
pushes() {
	[ -f "$tmp/calls" ] || { echo 0; return; }
	grep -c '^backup-push$' "$tmp/calls" || true
}
check() { # check <what> <got> <want>
	if [ "$2" = "$3" ]; then
		echo "ok    $1"
	else
		echo "FAIL  $1: got $2, want $3"
		sed 's/^/        /' "$tmp/out"
		fail=1
	fi
}

never=23:59 # not yet due, unless the check runs in that minute
due=00:00   # due all day

# To a volume: the first primary takes a base backup as soon as it runs.
reset
rm -f "$tmp/vol/.primary-timeline"
cluster "$alone" 1
run 1.5 WALG_FILE_PREFIX="$tmp/vol" DB_BACKUP_AT=$never
check "a volume: the first primary takes a base backup at once" "$(pushes)" 1
check "a volume: the timeline it led is written down" "$(cat "$tmp/vol/.primary-timeline" 2>/dev/null)" 1

# Its own restart is one timeline on, and its volume has all the log.
reset
cluster "$alone" 2
run 1.5 WALG_FILE_PREFIX="$tmp/vol" DB_BACKUP_AT=$never
check "a volume: no base backup after the primary's own restart" "$(pushes)" 0

# Another member led in between: this volume has no log for that time.
reset
cluster "$alone" 4
run 1.5 WALG_FILE_PREFIX="$tmp/vol" DB_BACKUP_AT=$never
check "a volume: a base backup at once when another member led in between" "$(pushes)" 1

# To a volume with standbys: the primary takes the day's, where the log is.
reset
cluster "$three" 4
run 1.5 WALG_FILE_PREFIX="$tmp/vol" DB_BACKUP_AT=$due
check "a volume: the primary takes the day's base backup" "$(pushes)" 1

# To a bucket with standbys: the first standby takes it, not the primary.
reset
cluster "$three" 1
run 1.5 DB_BACKUP_AT=$due
check "a bucket: the primary leaves the day's base backup to a standby" "$(pushes)" 0

# To a bucket alone: once, and not again after a restart the same day.
reset
cluster "$alone" 1
run 1.5 DB_BACKUP_AT=$due
check "a bucket: one member takes the day's base backup once" "$(pushes)" 1
rm -f "$tmp/calls"
run 1.5 DB_BACKUP_AT=$due
check "a restart after the hour does not take it again" "$(pushes)" 0

# Patroni does not answer at the hour: the day is not given up.
reset
rm -f "$tmp/cluster.json"
(
	/bin/sleep 1
	cluster "$alone" 1
) &
run 2.5 DB_BACKUP_AT=$due
wait
check "Patroni silent at the hour: the base backup is taken once it answers" "$(pushes)" 1

# Told to stop, it stops: while it waits, and during a base backup.
reset
cluster "$three" 1
run 1 DB_BACKUP_AT=$never NAP=30
check "it ends with 0 when told to stop" "$code" 0
check "it stops within a second while it waits" "$([ "$took" -lt 1000 ] && echo yes || echo "no, ${took} ms")" yes
reset
cluster "$alone" 1
run 1 DB_BACKUP_AT=$due PUSH_SECONDS=30
check "it stops within a second during a base backup" "$([ "$took" -lt 1000 ] && echo yes || echo "no, ${took} ms")" yes

exit $fail
