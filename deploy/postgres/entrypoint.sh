#!/bin/bash
# Starts Patroni, which starts PostgreSQL. Everything Patroni needs comes
# from the environment the chart sets (PATRONI_*); this writes the rest, the
# part that has no variable, to a file. Nothing here is specific to one
# member: the first pod of a new installation initializes the database, and
# every later one copies it from the primary.
set -euo pipefail

: "${PATRONI_KUBERNETES_POD_IP:?the chart sets it from the status of the pod}"
: "${PGDATA:?}"

# database.bundled.synchronous: quorum makes a commit wait for one standby
# when there is one, and for none when there is none, so a single member is
# not held up and a failover never loses a committed transaction.
sync_mode=${DB_SYNCHRONOUS:-quorum}

# Where a new replica comes from, and what a restore reads.
archive=off
archive_command=/bin/true
restore_command=
if [ "${DB_BACKUP:-false}" = true ]; then
  archive=on
  archive_command='wal-g wal-push "%p"'
  restore_command='wal-g wal-fetch "%f" "%p"'
fi

conf=/run/patroni/patroni.yml
cat > "$conf" <<__EOF__
bootstrap:
  dcs:
    # The primary keeps serving while the Kubernetes API is away, as long
    # as it reaches every member itself.
    failsafe_mode: true
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    synchronous_mode: ${sync_mode}
    synchronous_node_count: 1
    postgresql:
      use_pg_rewind: true
      parameters:
        track_commit_timestamp: 'on'
        wal_level: replica
        hot_standby: 'on'
        max_connections: ${DB_MAX_CONNECTIONS:-100}
        shared_buffers: ${DB_SHARED_BUFFERS:-128MB}
        password_encryption: scram-sha-256
        ssl: 'on'
        ssl_cert_file: /etc/221b-db/tls/tls.crt
        ssl_key_file: /etc/221b-db/tls/tls.key
        ssl_min_protocol_version: TLSv1.2
        archive_mode: '${archive}'
        archive_command: '${archive_command}'
        archive_timeout: 60
      pg_hba:
      - local all all trust
      - hostssl all all 0.0.0.0/0 scram-sha-256
      - hostssl all all ::/0 scram-sha-256
      - hostssl replication ${PATRONI_REPLICATION_USERNAME} 0.0.0.0/0 scram-sha-256
      - hostssl replication ${PATRONI_REPLICATION_USERNAME} ::/0 scram-sha-256
$(if [ -n "$restore_command" ]; then cat <<__RECOVERY__
      recovery_conf:
        restore_command: '${restore_command}'
__RECOVERY__
fi)
$(if [ "${DB_RESTORE:-false}" = true ]; then cat <<__RESTORE__
  # A new installation that starts from the archive (database.bundled.restore).
  method: restore
  restore:
    command: /usr/local/bin/restore.sh
    keep_existing_recovery_conf: false
    no_params: true
    recovery_conf:
      restore_command: 'wal-g wal-fetch "%f" "%p"'
      recovery_target_action: promote
$(if [ -n "${DB_RESTORE_TARGET_TIME:-}" ]; then echo "      recovery_target_time: '${DB_RESTORE_TARGET_TIME}'"; else echo "      recovery_target_timeline: latest"; fi)
__RESTORE__
fi)
  initdb:
  - auth-host: scram-sha-256
  - auth-local: trust
  - encoding: UTF8
  - locale: C.UTF-8
  - data-checksums
  # Makes the console's role and database, once, on the first primary.
  post_bootstrap: /usr/local/bin/post-bootstrap.sh
restapi:
  listen: '0.0.0.0:8008'
  connect_address: '${PATRONI_KUBERNETES_POD_IP}:8008'
postgresql:
  listen: '0.0.0.0:5432'
  connect_address: '${PATRONI_KUBERNETES_POD_IP}:5432'
  data_dir: '${PGDATA}'
  pgpass: /run/patroni/pgpass
  # The knowledge index's extensions in the console's database, on the
  # primary (extensions.sh, KN-51).
  callbacks:
    on_start: /usr/local/bin/extensions.sh
    on_role_change: /usr/local/bin/extensions.sh
  parameters:
    # Where post-bootstrap.sh, PGHOST and kubectl exec psql look: Debian's
    # PostgreSQL puts its socket there anyway, Wolfi's in /tmp unless told.
    unix_socket_directories: /var/run/postgresql
  create_replica_methods:
  - basebackup
  basebackup:
    checkpoint: fast
__EOF__
chmod 0600 "$conf"

# The passwords are not in the file: Patroni reads PATRONI_SUPERUSER_PASSWORD
# and PATRONI_REPLICATION_PASSWORD from the environment, where the chart puts
# them from the Secret.

mkdir -p "$(dirname "$PGDATA")"
exec patroni "$conf"
