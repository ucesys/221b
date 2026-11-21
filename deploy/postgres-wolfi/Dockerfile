# syntax=docker/dockerfile:1

# 221b-postgres, the Wolfi variant (docs/database.md, "The Wolfi variant"):
# the same database as deploy/postgres, PostgreSQL, Patroni and WAL-G, with
# PostgreSQL and Patroni as Wolfi packages them, from packages.wolfi.dev,
# assembled by apko (apko.yaml and its lock file, apko.lock.json, beside
# this file). Not the default: the chart, CI and the release build
# deploy/postgres, on Debian.
#
# The base is no registry image of anyone else's: apko builds it from the
# locked packages (the same lock gives the same digest), and this file
# starts from it by that digest and adds what 221B adds, the scripts and
# the backup program, and WAL-G (below), nothing else. There is no RUN
# step in the final stage, so every architecture builds without emulation.
#
#   make image-postgres-wolfi    (hack/wolfi-postgres.sh image)
#
# builds the base, pushes it to WOLFI_BASE_REPO (a registry on 127.0.0.1
# by default) and builds this from a context of deploy/postgres's scripts
# and backup program and WAL-G's release files (wal-g/).

ARG GO_IMAGE=golang:1.26.9
# Where hack/wolfi-postgres.sh base pushed the base, and the digest of the
# index apko builds from apko.lock.json (hack/wolfi-postgres.sh pin writes
# it here after a new lock; hack/check-base-images.sh checks its form).
ARG WOLFI_BASE_REPO=127.0.0.1:5123/221b-postgres-wolfi-base
ARG WOLFI_BASE_DIGEST=sha256:fc0cd48c53ff471dbc875559add33577b298dbd9206fa5bfb5c5d20d8747a364

# WAL-G: the release the Debian image takes. Wolfi's wal-g-pg is built
# without libsodium, and the chart encrypts every backup with it
# (WALG_LIBSODIUM_KEY), so it would refuse to archive anything. The script
# fetches the release files into the context (or takes them from the copy
# of the packages, on a rebuild); here they are checked again.
ARG WALG_VERSION=v3.0.10
ARG WALG_SHA256_AMD64=481204eec9ca4418be345eea39da8dc6ff9cbcb16c7c259294b50477dc62f377
ARG WALG_SHA256_ARM64=165c22724471127d2a08cbd3ff33503454f5252f5c5a8ab4aa0116895b880a40

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS walg
ARG TARGETARCH
ARG WALG_SHA256_AMD64
ARG WALG_SHA256_ARM64
COPY wal-g/ /in/
RUN set -eu; \
    case "${TARGETARCH:-amd64}" in \
      amd64) f=wal-g-pg-24.04-amd64; sum=$WALG_SHA256_AMD64 ;; \
      arm64) f=wal-g-pg-24.04-aarch64; sum=$WALG_SHA256_ARM64 ;; \
      *) echo "no WAL-G build for $TARGETARCH"; exit 1 ;; \
    esac; \
    echo "$sum  /in/$f" | sha256sum -c -; \
    install -D -m 0755 "/in/$f" /out/wal-g

# The backup container's program, built exactly as in deploy/postgres.
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS backup
ARG TARGETARCH
WORKDIR /src
COPY backup/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} GOFLAGS=-buildvcs=false \
    go build -trimpath -ldflags="-s -w" -o /out/221b-backup .

FROM ${WOLFI_BASE_REPO}@${WOLFI_BASE_DIGEST}
ARG WOLFI_BASE_REPO
ARG WOLFI_BASE_DIGEST

# The Debian image's contract: user 999, PGDATA, and every program on the
# PATH (Wolfi links PostgreSQL's programs into /usr/bin).
ENV LANG=C.UTF-8 \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PGDATA=/var/lib/postgresql/data/pgdata

# 221B's own, unchanged from deploy/postgres, and WAL-G where the Debian
# image has it.
COPY --chmod=0755 entrypoint.sh post-bootstrap.sh restore.sh /usr/local/bin/
COPY --from=backup --chmod=0755 /out/221b-backup /usr/local/bin/221b-backup
COPY --from=walg /out/wal-g /usr/local/bin/wal-g

# Built from Wolfi packages; not a Chainguard image, and neither Chainguard
# nor the Wolfi project made or endorses it. What 221B added is named.
LABEL org.opencontainers.image.title="221b-postgres (Wolfi variant)" \
      org.opencontainers.image.source="https://github.com/ucesys/221b" \
      org.opencontainers.image.description="PostgreSQL and Patroni from Wolfi packages (packages.wolfi.dev) assembled with apko, with WAL-G's release and 221B's scripts and backup program, for 221B's bundled database" \
      org.opencontainers.image.vendor="221B" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="${WOLFI_BASE_REPO}" \
      org.opencontainers.image.base.digest="${WOLFI_BASE_DIGEST}" \
      dev.221b.base="Wolfi packages from packages.wolfi.dev, assembled with apko from deploy/postgres-wolfi/apko.lock.json" \
      dev.221b.added="221B's: /usr/local/bin/entrypoint.sh post-bootstrap.sh restore.sh 221b-backup; WAL-G's release: /usr/local/bin/wal-g"

USER 999:999
EXPOSE 5432 8008
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
