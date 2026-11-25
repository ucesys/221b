#!/bin/bash
# Patroni runs this when PostgreSQL starts and when a member changes role
# (postgresql.callbacks, entrypoint.sh): on the primary it creates the two
# extensions the knowledge index uses in the console's database, pg_trgm
# (fuzzy matching) and vector (pgvector, the passages' embeddings), as the
# superuser over the local socket. The console's role may create pg_trgm
# itself (it is trusted) but not vector. A database made before 0.13.2 gets
# them at its first start on this image; post-bootstrap.sh does the same for
# a new one. Standbys get them by replication. Patroni does not wait for
# it: it tries for two minutes, then gives up, and the console searches
# without what is missing (KN-51).
#   extensions.sh ACTION ROLE CLUSTER
set -uo pipefail

role=${2:-}
case "$role" in
  primary|master|promoted) ;;
  *) exit 0 ;;
esac
: "${DB_APP_DATABASE:?}"

for _ in $(seq 60); do
  if psql -v ON_ERROR_STOP=1 -qAt -h /var/run/postgresql -U "${PATRONI_SUPERUSER_USERNAME:-postgres}" -d "$DB_APP_DATABASE" \
      -c "CREATE EXTENSION IF NOT EXISTS pg_trgm" -c "CREATE EXTENSION IF NOT EXISTS vector" > /dev/null 2>&1; then
    exit 0
  fi
  sleep 2
done
echo "extensions.sh: pg_trgm and vector could not be created in $DB_APP_DATABASE" >&2
exit 1
