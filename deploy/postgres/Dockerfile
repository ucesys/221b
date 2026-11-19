# syntax=docker/dockerfile:1

# 221b-postgres: the database the chart can run beside the console
# (database.mode: bundled, docs/database.md). PostgreSQL from the PostgreSQL
# project's own packages, Patroni, which elects the primary through
# Kubernetes objects, and WAL-G, which archives the write-ahead log and takes
# the base backups. The console's own image stays distroless and holds none
# of this.
#
#   docker build -t ghcr.io/ucesys/221b-postgres:dev deploy/postgres

# A tag here, for builds on a laptop; CI passes it pinned to a digest, from
# hack/base-images.env, as it does for the console's image.
ARG BASE_IMAGE=debian:13-slim
FROM ${BASE_IMAGE}

# One PostgreSQL major version per 221B release line; a new one is a new
# line, with pg_upgrade (docs/upgrading.md).
ARG PG_MAJOR=18
ARG PATRONI_VERSION=4.1.5
ARG WALG_VERSION=v3.0.10
ARG TARGETARCH

ENV LANG=C.UTF-8 \
    PATH=/usr/lib/postgresql/${PG_MAJOR}/bin:/opt/patroni/bin:/usr/local/bin:/usr/bin:/bin \
    PGDATA=/var/lib/postgresql/data/pgdata

# The postgres user is 999 whatever the package would pick, so the chart's
# securityContext and the volume's owner agree across image builds.
RUN set -eux; \
    groupadd -r -g 999 postgres; \
    useradd -r -u 999 -g postgres -d /var/lib/postgresql -s /bin/bash postgres; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg python3 python3-venv; \
    install -d /usr/share/postgresql-common/pgdg /etc/postgresql-common; \
    curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc https://www.postgresql.org/media/keys/ACCC4CF8.asc; \
    . /etc/os-release; \
    echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt ${VERSION_CODENAME}-pgdg main" > /etc/apt/sources.list.d/pgdg.list; \
    # The package would make and start a cluster of its own; Patroni makes it.
    echo "create_main_cluster = false" > /etc/postgresql-common/createcluster.conf; \
    apt-get update; \
    apt-get install -y --no-install-recommends "postgresql-${PG_MAJOR}"; \
    python3 -m venv /opt/patroni; \
    /opt/patroni/bin/pip install --no-cache-dir "patroni[kubernetes,psycopg3]==${PATRONI_VERSION}"; \
    case "${TARGETARCH:-amd64}" in amd64) arch=amd64 ;; arm64) arch=aarch64 ;; *) echo "no WAL-G build for ${TARGETARCH}"; exit 1 ;; esac; \
    curl -fsSL "https://github.com/wal-g/wal-g/releases/download/${WALG_VERSION}/wal-g-pg-24.04-${arch}.tar.gz" | tar -xz -C /usr/local/bin; \
    mv "/usr/local/bin/wal-g-pg-24.04-${arch}" /usr/local/bin/wal-g; \
    chmod 0755 /usr/local/bin/wal-g; \
    wal-g --version; patroni --version; postgres --version; \
    apt-get purge -y --auto-remove gnupg; \
    rm -rf /var/lib/apt/lists/* /root/.cache

COPY --chmod=0755 entrypoint.sh post-bootstrap.sh restore.sh backup.sh /usr/local/bin/

LABEL org.opencontainers.image.source="https://github.com/ucesys/221b" \
      org.opencontainers.image.description="PostgreSQL, Patroni and WAL-G for 221B's bundled database" \
      org.opencontainers.image.licenses="Apache-2.0"

USER 999:999
EXPOSE 5432 8008
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
