#!/bin/bash
# Patroni runs this once, on the primary, after it initialized a new
# database: the console's role, which owns the console's database and
# nothing else. The console makes its tables itself (its migrations). After a
# restore the role and the database are already there; only the password is
# set again, to the one this installation's Secret holds.
set -euo pipefail

: "${DB_APP_USER:?}" "${DB_APP_DATABASE:?}" "${DB_APP_PASSWORD:?}"

psql -v ON_ERROR_STOP=1 -h /var/run/postgresql -U "${PATRONI_SUPERUSER_USERNAME:-postgres}" -d postgres \
  -v user="$DB_APP_USER" -v db="$DB_APP_DATABASE" -v pw="$DB_APP_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN', :'user')
  WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'user') \gexec
ALTER ROLE :"user" PASSWORD :'pw';
SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'user')
  WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'db') \gexec
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
SQL
