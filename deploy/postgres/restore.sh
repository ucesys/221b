#!/bin/bash
# Patroni's bootstrap method for a restore (database.bundled.restore): the
# newest base backup that is not later than the time asked for is fetched,
# and PostgreSQL replays the archived log from there.
set -euo pipefail

: "${PGDATA:?}"
backup=LATEST
if [ -n "${DB_RESTORE_TARGET_TIME:-}" ]; then
  # The newest backup that had finished by then.
  backup=$(wal-g backup-list --detail --json | python3 -c '
import json, sys
target = sys.argv[1]
done = [b for b in json.load(sys.stdin) if b.get("finish_time", "") <= target]
if not done:
    sys.exit("no base backup finished before " + target)
print(max(done, key=lambda b: b["finish_time"])["backup_name"])
' "$DB_RESTORE_TARGET_TIME")
fi
mkdir -p "$PGDATA"
chmod 0700 "$PGDATA"
exec wal-g backup-fetch "$PGDATA" "$backup"
