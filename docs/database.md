# The database

221B is moving its records from ConfigMaps to PostgreSQL (Phase 13). ConfigMaps hold 1 MiB each, are read and written whole, and gave every record set its own cap; PostgreSQL holds the records as rows of the [data model](data-model.md)'s tables. This page says how the database is chosen and run, what the console does with it, and how a domain's records move.

**Where the move stands.** The database is optional until every domain has moved: `database.mode` is `none` by default, and a console without a database works as before. With one, the domains below keep their records in it and the rest stay in their ConfigMaps.

| Domain | In the database | Still in ConfigMaps |
| --- | --- | --- |
| Activity log | the audit log (`audit_events`) | the Toolbox's session records |
| Platform | the backups' settings (`settings`, scope `backup`) and runs (`backup_runs`) | settings, acknowledged alerts, applications, repositories, credentials' list, cluster connections |
| Identity, Approvals, Inventory, Automation, AI configuration, Jobs, Cases, Changes, Knowledge | | everything |

`GET /api/v1/database` and Administration's metrics say the same of a running console.

## Choosing a database

`database.mode` in the chart's values:

| Mode | What runs | For |
| --- | --- | --- |
| `none` | Nothing. | The default until the move is complete. |
| `bundled` | The chart's own PostgreSQL beside the console, from the image `221b-postgres`: one member, or three with `database.bundled.ha`. | An installation with no PostgreSQL to use. |
| `external` | Nothing of the chart's; the console connects to a PostgreSQL you run. | A managed service (RDS, Cloud SQL, Azure), your own Patroni or CloudNativePG cluster, a central database platform. |

The chart refuses what cannot work: a mode that is none of the three; `external` with no host, or with `sslMode: disable` and no `allowInsecure`; one bundled member in a namespace labelled `221b.io/environment=production` without `database.bundled.acknowledgeNoHA`; and a release whose mode changes between `bundled` and `external`, which is a migration (below), not a value.

## The bundled database

One release, two images: `221b` stays distroless and holds no database; `221b-postgres` holds PostgreSQL 18 from the PostgreSQL project's packages, [Patroni](https://patroni.readthedocs.io/) and [WAL-G](https://github.com/wal-g/wal-g) on Debian, and runs as user 999 with a read-only root filesystem.

- **Members.** A StatefulSet of one pod, or three with `ha: true`, each with its own volume (`storage.size`, 10 GiB by default). Three members are kept on different nodes, spread over zones where there are zones, with a disruption budget of one. Turning `ha` on later adds the two standbys while the primary keeps serving.
- **Who leads.** Patroni keeps its leader lock and the cluster's state in ConfigMaps of the console's namespace and labels each pod with its role; the Service `<release>-db` selects the pod labelled `primary`, and is the console's one address. A dead primary is replaced within about 30 seconds; a planned switch takes a few. While the Kubernetes API is away the primary keeps serving as long as it reaches every member (failsafe mode).
- **Commits.** With `synchronous: quorum` (the default) a commit waits for one of the two standbys, so a failover loses nothing committed, and losing one standby stops nothing. One member alone waits for nobody.
- **Access.** The chart makes three passwords once, in the Secret `<release>-db`, and keeps them through upgrades and an uninstall, as the volumes are kept: the superuser's and the replication role's are Patroni's, and the console is given only its own role's, `app_221b`, which owns the database `221b` and nothing else. Connections are TLS with a certificate the chart makes once (`<release>-db-tls`; or yours, `tls.secretName`), verified by the console (`verify-full`), and passwords are SCRAM. A network policy admits the console and the members to PostgreSQL, and the members alone to Patroni's API.
- **Kubernetes rights.** A Role in the namespace: ConfigMaps, and its own pods to label. Nothing cluster-wide, no CRDs, no operator.
- **Backups.** PostgreSQL archives every completed segment of its write-ahead log through WAL-G, and a `backup` container in each pod takes a base backup once a day (`backup.dailyAt`, UTC) on one member: the standby whose name sorts first, or the primary when it is alone. `backup.retainBaseBackups` (7) are kept, with the log between them, so the database can be restored to any moment since the oldest. They go to the S3 bucket `backup.s3` names, encrypted before they leave the pod with a key in the Secret; with no bucket they go to a second volume, and the chart's notes say that this does not survive losing the cluster's storage.
- **Restore.** A new installation starts from the archive with `database.bundled.restore.fromBackup=true`, to the newest point or to `restore.targetTime`; remove the value after the first install.

## An external database

The console needs, of a PostgreSQL you run:

- **PostgreSQL 16 or later.** The console checks at start and does not start on an older one.
- **A database and a role that owns it**, made beforehand (`database.external.database`, `user`; `221b` and `app_221b` by default). The console makes and changes its own tables as that role; it needs no superuser, no replication right and nothing outside its database. No extension is needed.
- **One address that reaches the primary** (`host`, `port`): for CloudNativePG the cluster's `-rw` Service, for Patroni the primary's Service or a load balancer that follows it. The console connects again when the primary changes.
- **TLS.** `sslMode` is `verify-full` by default, with the CA in `caSecret` (key `ca.crt`) unless a public CA signed the server's certificate. `clientCertSecret` (`tls.crt`, `tls.key`) is for a server that asks for a client certificate.
- **The password** in a Secret (`passwordSecret.name` and `key`). It is read at every new connection, so a rotated Secret needs no restart.
- **Connections.** `maxConnections` (20) is what all the console's replicas open between them; each replica also holds up to four direct sessions for its listener and its locks.
- **`track_commit_timestamp = on`** is recommended; the console warns when it is off.

**Poolers.** The console listens for notices (`LISTEN`) and takes session advisory locks; PgBouncer in transaction mode keeps neither. Session mode, or no pooler, needs nothing. With a pooler in transaction mode, give `listenerHost`, a direct address: the listener, the locks and the migrations use it, and everything else goes through the pooler. The console's own pool is small; most installations need no pooler.

Availability and backups of an external database are yours. The console's own backup (Administration › Backup) works in every mode and is what moves an installation between modes.

## What the console does with it

- **At start** it connects (trying for two minutes, since a bundled database may still be starting), checks the version, and brings the schema to its own: the steps are embedded in the binary and applied in order behind one advisory lock, so replicas that start together migrate one after another. Within a release line the steps only add, so the replicas of the release before go on working during a rollout, and going back one release is safe.
- **No replica leads the database's work.** A scheduled time is claimed by inserting its row, and the replica whose insert goes in runs it; a pass over everything runs behind an advisory lock of its name; a change the other replicas should see is announced with `NOTIFY` and looked for anyway every `database.pollInterval` (30 seconds), so a lost notice delays a change and never loses it. The Lease that elects the replica reading the clusters stays until the conversations and cards it protects have moved too.
- **While the database is away** the console stays ready and goes on serving what does not need it: sign-in, and every page read live from Kubernetes. A page that needs the database says it is unavailable; a write fails with a sentence and is never queued in memory, except the audit log, whose newest 2,000 events wait. `b221_database_up` is 0, and the log says when it left and when it came back.
- **The move from ConfigMaps** happens once per record set, at the first start with a database: the records are copied in one transaction that also writes the copy down (the `imports` table), so records deleted later do not come back. The ConfigMap's key is left as it was at the copy, for one release, for a rollback; changes made after the copy are not carried back.

## Changing mode

- **None to bundled or external**: set the mode. Each domain that has moved copies its records at the next start.
- **One member to three** (`database.bundled.ha: true`): an upgrade; the standbys copy from the primary while it serves. Three to one is allowed and pauses writes briefly.
- **Bundled to external, or back**: take a backup in Administration › Backup, install with the new mode (the chart asks for `database.allowModeChange=true` on a release that had a database), and restore the backup there. Plan it with a maintenance window.

## Upgrades

| What | How |
| --- | --- |
| The console | A rolling update; the schema's steps run at start. |
| PostgreSQL's minor version | A new `221b-postgres` image with the release. With three members the pods restart one at a time, and Patroni hands the primary's role over before its pod stops; with one, a restart of under a minute. |
| PostgreSQL's major version | A 221B release line names one major version. Moving to the next is a release's own step, with `pg_upgrade`, described in that release's notes; it is never automatic. |
| An external database | Yours; the console checks the minimum version at start. |

## Writing a domain's repository

Every domain keeps its records through an interface of its own, defined in its package, with what the services need and nothing of where the records are (ADR-162). `backup.Repo` is the pattern:

1. **Define the interface** from what the service does, not from the tables: `Claim(due, missed)` and `AddRun(run)`, not "load the status, change it, save it".
2. **Keep today's storage as one implementation** (`KVRepo`, on `kv.Backend`), so a console without a database, the simulator and the tests work as before.
3. **Write the PostgreSQL implementation** on `internal/db`: `d.Do` for one statement, `d.Tx` for several, `db.LoadScope` and `db.SaveScope` for a singleton, `db.NewID` for keys. Only `internal/db` and the repositories contain SQL.
4. **Test both against one set of tests** (`repo_test.go`); `dbtest.Open(t)` gives each test an empty, migrated database on the server `TEST_DATABASE` names (`make test-db` starts one), and skips without it.
5. **Copy the old records once** with `d.ImportOnce`, and add the domain to the table at the top of this page.
6. **Settle the domain's columns** in its migration file while no release has shipped it.
