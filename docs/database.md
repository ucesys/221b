# The database

221B is moving its records from ConfigMaps to PostgreSQL (Phase 13). ConfigMaps hold 1 MiB each, are read and written whole, and gave every record set its own cap; PostgreSQL holds the records as rows of the [data model](data-model.md)'s tables. This page says how the database is chosen and run, what the console does with it, and how a domain's records move.

**Where the move stands.** The database is optional until every domain has moved: `database.mode` is `none` by default, and a console without a database works as before. With one, the domains below keep their records in it and the rest stay in their ConfigMaps.

| Domain | In the database | Still in ConfigMaps |
| --- | --- | --- |
| Activity log | the audit log (`audit_events`) and the Toolbox's session records (`toolbox_sessions`): the whole domain | |
| Platform | the shared settings (`settings`, scopes `general`, `sherlock`, `watson`) and the acknowledged alerts (`alert_acks`); the repositories (`repositories`); the backups' settings (scope `backup`) and runs (`backup_runs`); getting started's dismissed steps, Settings › Email and the case work's settings (scopes `setup`, `email`, `cases`) | what is kept in Secrets, not ConfigMaps: the applications, the credentials' list, the cluster connections. `clusters` and `credentials` hold a row by id for each one another table points at |
| Approvals | the requests that wait for a second person and their decisions (`approval_requests`, `approval_decisions`), the standing approvals (`approval_policies`), the notification channels (`notification_channels`): the whole domain | |
| Inventory | the hosts outside Kubernetes and the paired nodes (`hosts`, `host_logins`, `host_pairings`), the fleets and the members of listed fleets (`fleets`, `fleet_hosts`), what was last read from each node (`host_facts`) | `data_centers`, a host's rack and data center: nothing sets them yet |
| Identity | | the sign-in sessions (in a Secret); local accounts stay in their Secret for good |
| Automation | the scripts and their versions (`scripts`, `script_versions`, `script_version_parameters`, `script_version_kube_actions`, `script_version_findings`, `script_version_actions`), the script groups (`script_groups`), the execution profiles (`run_profiles`, `run_profile_fleets`) and the variables (`variables`, with columns of `fleets` and `hosts`) | the variables' changes, the runs |
| AI configuration, Jobs, Cases, Changes, Knowledge | | everything |

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
- **Backups.** PostgreSQL archives every completed segment of its write-ahead log through WAL-G, and a `backup` container in each pod takes a base backup once a day (from `backup.dailyAt`, UTC) on one member. `backup.retainBaseBackups` (7) are kept, with the log between them, so the database can be restored to any moment since the oldest. Both are encrypted before they leave the pod, with the key `backup-key` of the Secret `<release>-db`.
  - **To a bucket** (`backup.s3`): every member writes to the one archive. The standby whose name sorts first takes the base backup, or the primary when it is alone.
  - **To a volume** (no bucket): each member has a backup volume of its own, and the log goes to the primary's. So the primary takes the base backup, and a member that becomes the primary after another one was takes one at once: its volume has no log for the time in between. The first primary takes one as soon as it runs. After a failover the time before it can be restored only from the old primary's volume; with three members, a bucket is the better place. A volume does not survive losing the cluster's storage, and the chart's notes say so.
- **Restore.** A new installation starts from the archive with `database.bundled.restore.fromBackup=true`, to the newest point or to `restore.targetTime`; remove the value after the first install. A restore needs what the backups were made with:
  - **The key.** The archive is read with the `backup-key` of the old installation's Secret `<release>-db`. Under the same release name and namespace the kept Secret is found again and nothing is needed. Under another name, in another namespace or in another cluster, the chart would make a new key, and the restore would fail: copy the old Secret there first and name it in `database.bundled.existingSecret`. Copy it whole, since the restored database knows the old passwords.
  - **The archive.** A bucket is reached from anywhere with the same `backup.s3` values. A backup volume belongs to its release: it is found again only under the same release name and namespace, where the claim `backup-<release>-db-0` is kept. To restore there, uninstall, delete the data claim `data-<release>-db-0` (and those of the other members), keep the backup claim and the Secret, and install with `restore.fromBackup=true`.
  - Keep a copy of the Secret outside the cluster: without `backup-key` the backups cannot be read by anyone.

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

- **At start** it connects (trying for two minutes, since a bundled database may still be starting), checks the version, and brings the schema to its own: the steps are embedded in the binary and applied in order behind one advisory lock, so replicas that start together migrate one after another. Within a release line the steps only add, so the replicas of the release before go on working during a rollout, and going back one release is safe. The console does not listen until this is done, and the chart's startup probe allows for it (seven and a half minutes with a database), so the liveness probe does not stop a console that is waiting for its database.
- **No replica leads the database's work.** A scheduled time is claimed by inserting its row, and the replica whose insert goes in runs it; a pass over everything runs behind an advisory lock of its name; a change the other replicas should see is announced with `NOTIFY` and looked for anyway every `database.pollInterval` (30 seconds), so a lost notice delays a change and never loses it. The Lease that elects the replica reading the clusters stays until the conversations and cards it protects have moved too.
- **While the database is away** the console stays ready and goes on serving what does not need it: sign-in, and every page read live from Kubernetes. A page that needs the database says it is unavailable; a write fails with a sentence and is never queued in memory, except the audit log, whose newest 2,000 events wait. `b221_database_up` is 0, and the log says when it left and when it came back.
- **The move from ConfigMaps** happens once per record set, at the first start with a database: the records are copied in one transaction that also writes the copy down (the `imports` table), so records deleted later do not come back. The ConfigMap's key is left as it was at the copy, for one release, for a rollback; changes made after the copy are not carried back.
- **After a rollback and a roll forward.** A console rolled back to a release without a database works from its ConfigMaps again, and what it records goes there. Brought forward again to the same database, it copies what only grows: the audit events and the Toolbox's session records newer than the last copy (`imports.newest_at`), at every start. Everything else was copied once and is not merged: for the settings, approvals, inventory and the other records edited in place, the database's are the ones that count, and what the rolled-back console changed in a ConfigMap meanwhile is not read.

## Changing mode

- **None to bundled or external**: set the mode. Each domain that has moved copies its records at the next start.
- **One member to three** (`database.bundled.ha: true`): an upgrade; the standbys copy from the primary while it serves. Three to one is allowed and pauses writes briefly.
- **Bundled to external, or back**: take a backup in Administration › Backup, install with the new mode (the chart asks for `database.allowModeChange=true` on a release that had a database), and restore the backup there. Plan it with a maintenance window.
- **What the chart goes by.** The Deployment carries its mode in the annotation `221b.io/database-mode`. A `helm rollback` to a revision from before the database removes it, so the chart also looks for the bundled database's Secret `<release>-db`, which a rollback and an uninstall keep with the volumes: while it is there, any mode but `bundled` is refused, because the records in that database would be left behind. When the bundled database is no longer wanted, delete the Secrets `<release>-db` and `<release>-db-tls`, the claims `data-<release>-db-*` and `backup-<release>-db-*`, and Patroni's ConfigMaps `<release>-db-config`, `-leader` and `-sync` (and `-failsafe` where there is one); or set `allowModeChange=true`. An external database leaves nothing in the cluster to go by: after a rollback to a revision from before it, the chart cannot tell that there was one.
- **Stopping a pod of the bundled database** does not wait out its grace period: Patroni hands over or shuts PostgreSQL down, and the backup container ends when it is told to.

## Upgrades

| What | How |
| --- | --- |
| The console | A rolling update; the schema's steps run at start. |
| PostgreSQL's minor version | A new `221b-postgres` image with the release. With three members the pods restart one at a time, and Patroni hands the primary's role over before its pod stops; with one, a restart of under a minute. |
| PostgreSQL's major version | A 221B release line names one major version. Moving to the next is a release's own step, with `pg_upgrade`, described in that release's notes; it is never automatic. |
| An external database | Yours; the console checks the minimum version at start. |

## Writing a domain's repository

Three shapes, by what the service is like:

- **A singleton document** (a page's settings, read whole): a scope of `settings` through `db.ScopeKV`, which is the `kv.Backend` the service already uses. Nothing else to write.
- **A service with few, clear operations**: a repository, as below. This is the end state for every service.
- **A service that edits one JSON document in many places**: `db.Doc`, an interim step. The service stays as it is; a `Read` builds its document from the model's tables and a `Write` changes the rows from what they hold to what the service saved, every field a column (`twoperson/pg.go`, `cards/pg_approvals.go`). Its records are then rows, keyed and joinable, but the service still reads them all to change one, and writers of one document take turns. Each `db.Doc` is listed in the handover for a repository of its own.

Every domain keeps its records through an interface of its own, defined in its package, with what the services need and nothing of where the records are (ADR-162). `backup.Repo` is the pattern:

1. **Define the interface** from what the service does, not from the tables: `Claim(due, missed)` and `AddRun(run)`, not "load the status, change it, save it".
2. **Keep today's storage as one implementation** (`KVRepo`, on `kv.Backend`), so a console without a database, the simulator and the tests work as before.
3. **Write the PostgreSQL implementation** on `internal/db`: `d.Do` for one statement, `d.Tx` for several, `db.LoadScope` and `db.SaveScope` for a singleton, `db.NewID` for keys. Only `internal/db` and the repositories contain SQL.
4. **Test both against one set of tests** (`repo_test.go`); `dbtest.Open(t)` gives each test an empty, migrated database on the server `TEST_DATABASE` names (`make test-db` starts one), and skips without it.
5. **Copy the old records once** with `d.ImportOnce`, and add the domain to the table at the top of this page.
6. **Settle the domain's columns** in its migration file while no release has shipped it.
