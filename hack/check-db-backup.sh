#!/usr/bin/env bash
# Checks the bundled database's backup container (deploy/postgres/backup,
# the program 221b-backup, DB-84) without a database: its Go tests run it
# with stand-ins for Patroni and WAL-G and check who takes the base backup
# and when, the schedule, and that it stops within a second when told to.
# They replace the checks this script made of backup.sh, the shell script
# of before. BACKUP_BIN runs the checks of the program against a
# 221b-backup built elsewhere, the image's:
#   docker create --name b <image>; docker cp b:/usr/local/bin/221b-backup /tmp/; docker rm b
#   BACKUP_BIN=/tmp/221b-backup hack/check-db-backup.sh
set -euo pipefail
cd "$(dirname "$0")/../deploy/postgres/backup"
gofmt_out=$(gofmt -l .)
[ -z "$gofmt_out" ] || { echo "not gofmt'd: $gofmt_out"; exit 1; }
go vet ./...
go test -race -count=1 ./...
