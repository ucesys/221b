#!/bin/bash
# The backup container of each database pod: once a day, from DB_BACKUP_AT
# (HH:MM, UTC), one member takes a base backup and removes the ones past
# DB_BACKUP_RETAIN. Which member is worked out from what Patroni says about
# the cluster, the same on every member. Nothing is run in another pod.
#
# With a bucket, every member writes to the one archive, so the standby
# whose name sorts first takes the backup and the primary is spared; alone,
# the primary takes it.
#
# With a volume (WALG_FILE_PREFIX), every member has a volume of its own and
# the write-ahead log goes to the primary's. A base backup is only of use
# next to the log that follows it, so there the primary takes it. A member
# that becomes the primary after another one was takes a base backup at
# once: its volume has no log for the time in between, so nothing older in
# it can be rolled forward to now. Whether another one was is read from the
# volume: the member that takes a timeline archives that timeline's history
# file, so a timeline whose history file is not here was another member's.
set -uo pipefail

: "${PGDATA:?}"
at=${DB_BACKUP_AT:-02:00}
retain=${DB_BACKUP_RETAIN:-7}
me=${PATRONI_NAME:-$(hostname)}
volume=${WALG_FILE_PREFIX:-}
marker=${volume:+$volume/.primary-timeline}
export PGHOST=/var/run/postgresql PGUSER=${PATRONI_SUPERUSER_USERNAME:-postgres}

log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) backup: $*"; }

# This script is the container's first process, which gets no default
# handling of a signal: without the trap the pod would take its whole grace
# period to stop. Whatever runs is run in the background and waited for, so
# the trap is not held up by it.
child=
stop() {
  log "stopping"
  [ -n "$child" ] && kill "$child" 2>/dev/null
  exit 0
}
trap stop TERM INT
run() {
  "$@" &
  child=$!
  wait "$child"
  local rc=$?
  child=
  return $rc
}

# Prints "<the member that takes the backup> <the primary> <its timeline>".
# Quiet when Patroni does not answer: the loop says so once.
cluster() {
  curl -fs --max-time 5 http://127.0.0.1:8008/cluster | python3 -c '
import json, sys
try:
    members = json.load(sys.stdin).get("members", [])
except ValueError:
    sys.exit(1)
standbys = sorted(m["name"] for m in members
                  if m.get("role") != "leader" and m.get("state") in ("streaming", "running"))
leaders = [m for m in members if m.get("role") == "leader"]
leader = leaders[0]["name"] if leaders else "-"
timeline = leaders[0].get("timeline", 0) if leaders else 0
by_volume = sys.argv[1] != ""
who = leader if by_volume else (standbys or [leader])[0]
print(who, leader, timeline)
' "$volume"
}

# Whether the archive this member sees holds a base backup started at or
# after the time given (RFC 3339, UTC).
backed_up_since() {
  wal-g backup-list --detail --json 2>/dev/null | python3 -c '
import json, sys
since = sys.argv[1]
try:
    backups = json.load(sys.stdin) or []
except ValueError:
    backups = []
sys.exit(0 if any((b.get("start_time") or b.get("time") or "") >= since for b in backups) else 1)
' "$1"
}

# Whether this member's volume has the history file of every timeline from
# $1 to $2, both included: it was the primary through all of them.
led() {
  local t
  for ((t = $1; t <= $2; t++)); do
    compgen -G "$volume/wal_005/$(printf '%08X' "$t").history*" > /dev/null || return 1
  done
  return 0
}

# After a failure the next try waits ten minutes.
hold=0
take() {
  log "taking a base backup${1:+: $1}"
  if run wal-g backup-push "$PGDATA"; then
    log "the base backup is done"
    run wal-g delete retain FULL "$retain" --confirm || log "removing old backups failed"
    return 0
  fi
  log "the base backup FAILED; trying again in ten minutes"
  hold=$(($(date +%s) + 600))
  return 1
}

last=
silent=
where="the bucket"
[ -z "$volume" ] || where="the volume of this member"
log "a base backup every day at $at UTC, keeping $retain, to $where"
first=1
while true; do
  # The first look is at once: a pod that lives less than the wait must
  # still write down the timeline it led.
  [ -n "$first" ] || run sleep 30
  first=
  if ! state=$(cluster) || [ -z "$state" ]; then
    # Not an answer about who takes it, so the day is not marked as done.
    [ -n "$silent" ] || log "Patroni does not answer; trying again"
    silent=1
    continue
  fi
  silent=
  read -r who leader timeline <<<"$state"
  [ "$(date +%s)" -ge "$hold" ] || continue

  if [ -n "$volume" ] && [ "$leader" = "$me" ] && [ "$timeline" -gt 0 ]; then
    seen=$(cat "$marker" 2>/dev/null || echo 0)
    if [ "$seen" != "$timeline" ]; then
      # This member led the timeline written down. It leads this one. If
      # it took every timeline in between, its volume has all the log: its
      # own restarts, also ones too short to be written down. Otherwise it
      # is here for the first time, or another member led in between.
      if [ "$seen" -eq 0 ] || [ "$timeline" -lt "$seen" ] || ! led $((seen + 1)) $((timeline - 1)); then
        take "this volume has no log up to timeline $timeline" || continue
        last=$(date -u +%Y-%m-%d)
      fi
      echo "$timeline" > "$marker"
    fi
  fi

  today=$(date -u +%Y-%m-%d)
  [ "$last" = "$today" ] && continue
  [[ "$(date -u +%H:%M)" < "$at" ]] && continue
  if [ "$who" != "$me" ]; then
    log "$who takes today's backup"
    last=$today
    continue
  fi
  # A restart after the hour does not take the day's backup a second time.
  if backed_up_since "${today}T${at}:00"; then
    last=$today
    continue
  fi
  take && last=$today
done
