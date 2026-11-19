#!/bin/bash
# The backup container of each database pod: once a day, at DB_BACKUP_AT
# (HH:MM, UTC), one member takes a base backup and removes the ones past
# DB_BACKUP_RETAIN. Which member is worked out from what Patroni says about
# the cluster, the same on every member: the standby whose name sorts
# first, or the primary when it is alone. Nothing is run in another pod.
set -uo pipefail

: "${PGDATA:?}"
at=${DB_BACKUP_AT:-02:00}
retain=${DB_BACKUP_RETAIN:-7}
me=${PATRONI_NAME:-$(hostname)}
export PGHOST=/var/run/postgresql PGUSER=${PATRONI_SUPERUSER_USERNAME:-postgres}

log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) backup: $*"; }

# Prints the member that takes today's backup.
chosen() {
  curl -fsS --max-time 5 http://127.0.0.1:8008/cluster | python3 -c '
import json, sys
members = json.load(sys.stdin).get("members", [])
standbys = sorted(m["name"] for m in members
                  if m.get("role") != "leader" and m.get("state") in ("streaming", "running"))
leaders = [m["name"] for m in members if m.get("role") == "leader"]
print((standbys or leaders or [""])[0])
'
}

last=
log "a base backup every day at $at UTC, keeping $retain"
while true; do
  sleep 30
  today=$(date -u +%Y-%m-%d)
  [ "$(date -u +%H:%M)" = "$at" ] || continue
  [ "$last" = "$today" ] && continue
  last=$today
  who=$(chosen) || { log "Patroni did not answer; no backup today from this member"; continue; }
  if [ "$who" != "$me" ]; then
    log "$who takes today's backup"
    continue
  fi
  log "taking a base backup"
  if wal-g backup-push "$PGDATA"; then
    log "the base backup is done"
    wal-g delete retain FULL "$retain" --confirm || log "removing old backups failed"
  else
    log "the base backup FAILED"
  fi
done
