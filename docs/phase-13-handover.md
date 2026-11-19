# Phase 13 handover: storage in PostgreSQL

Written on November 18, 2026, at the end of the session that started Phase 13, for the session that continues it. Read [database.md](database.md) (how the database is run, and how to write a repository) and [data-model.md](data-model.md) (the 73 tables and their rules) first; the requirements are DB-01 to DB-22 in [requirements.md](requirements.md), and the decisions ADR-162 to ADR-164 in [architecture.md](architecture.md).

## Where the branch stands

Everything is on `phase-13-store`, pushed. Nothing is merged, there is no pull request, no release, and CI has not run (the owner asked for none yet; CI runs on a pull request or a push to `main`).

| Commit | What |
| --- | --- |
| `Storage: the db package, the schema's 73 tables and the replicas' coordination` | `server/internal/db`: pool, transactions, health, UUIDv7 keys, the embedded migrations, advisory-lock sweeps, claims, `LISTEN`/`NOTIFY` with a poll; `dbtest`. |
| `Storage: the server opens the database it is given` | The `-db-*` flags, `cmd/221b/database.go`, `GET /api/v1/database`, the `b221_database_*` metrics. |
| `Storage: the 221b-postgres image and the chart's database modes` | `deploy/postgres` (Dockerfile, Patroni entrypoint, bootstrap, restore and backup scripts); `database.mode` none, bundled, external in the chart; `hack/check-chart.sh`. |
| `Activity log: the audit log is kept in the database when there is one` | `audit.PGStore`, `db.ImportOnce`, the `imports` table. |
| `Backups: the settings and the runs behind a repository…` | `backup.Repo` with `KVRepo` and `PGRepo`, `db.LoadScope`/`SaveScope`. The pattern for every other domain. |
| `Activity log: the Toolbox's session records…` | `sessions.PGHistory` and `sessions.Archive`; the backup reads and merges through it. Activity log is the first domain wholly moved. |
| `Docs: Phase 13…` | The documents, the changelog, the Go CI job's PostgreSQL service, this handover. |

The first commit on the branch, `CI: cache Playwright browsers…` (pushed by the owner's other session), was reviewed and left as it is: no problems found.

## What was tested, and what was not

**Tested here** (Go 1.26.9, PostgreSQL 16.15, Helm 4.3.1):

- `go build ./...`, `gofmt`, `go vet` on the changed packages: clean.
- `go test -race` with `TEST_DATABASE` set, on `internal/db`, `internal/audit`, `internal/backup`, `internal/sessions`, `cmd/221b`: pass. The whole suite without `-race` passed before the changes (54 packages); see *Test results* at the end for the last full run.
- `hack/check-chart.sh`: all 107 checks pass, the database's included, and every render is valid against the Kubernetes 1.35 and 1.37 schemas.
- The Patroni file the entrypoint writes parses as YAML in its three shapes (plain, with backups, restoring), and `post-bootstrap.sh` was run twice against a real PostgreSQL (it makes the role and the database once, then only resets the password).

**Not tested: do these first.**

1. **The `221b-postgres` image has never been built.** There was no Docker daemon. Run `make image-postgres` and fix what fails; the likeliest spots are the pgdg package step and the WAL-G file name (`wal-g-pg-24.04-{amd64,aarch64}.tar.gz` of v3.0.10 exist; that the tarball holds a file of the same name without `.tar.gz` is assumed).
2. **The bundled database has never run.** Install the chart in kind with `database.mode=bundled`, then with `database.bundled.ha=true`, and check: the first pod initializes and is labelled `role=primary`; the Service `<release>-db` reaches it; the console connects with `verify-full`; `app_221b` and the database `221b` exist; deleting the primary's pod promotes a standby and the console reconnects; `ha` turned on later adds two standbys; the backup container takes a base backup (set `dailyAt` a minute ahead); `restore.fromBackup` brings a new installation back. Things to watch: Patroni's label value for the leader (`PATRONI_KUBERNETES_LEADER_LABEL_VALUE=primary` is set explicitly), the TLS key's file mode under `fsGroup` (0440, owned by root: PostgreSQL accepts that), and `readOnlyRootFilesystem` (writable paths are the data volume, `/var/run/postgresql`, `/run/patroni`, `/tmp`).
3. **External mode against a real server with TLS** (`verify-full` with a CA, a client certificate, a pooler with `listenerHost`). Only `sslmode=disable` ran here.
4. **`hack/e2e-kind.sh`, `ha-kind.sh`, `upgrade-kind.sh`** were not run; add a database variant to each.
5. **The browser app was not touched**, and its tests were not run.

## Decisions made in this session for the owner to confirm

These differ from the design document *221B storage deployment design*, or were not in it.

| Decision | Why | To undo |
| --- | --- | --- |
| `database.mode` defaults to **`none`**, not `bundled`. | Only three record sets have moved; a default that adds a StatefulSet, a volume and a second image to every upgrade, before the records use it, would break upgrades and CI. | Flip the default when the move is complete. |
| The base backup runs at a **time of day** (`backup.dailyAt: "02:00"`), not a cron expression. | A shell loop in the pod cannot parse cron. | Replace `backup.sh` with a small program. |
| The member that takes the backup is **picked from Patroni's `/cluster`** (first standby by name, else the primary), not by an advisory lock. | Simpler, and nothing is run in another pod either way. | — |
| **`iamAuth`** (RDS, Cloud SQL tokens) is not implemented. | Time. | Add a token source in `db.open`'s `BeforeConnect`. |
| **Quorum commit is on with one member too** (it waits for nobody then). | So turning `ha` on needs no change to Patroni's stored configuration. | — |
| The audit log's **retention is a year**, a constant. | It needs a setting and a place on the page. | `auditRetention` in `cmd/221b/main.go`. |
| The import is written down in a table, **`imports`**; the ConfigMap is not annotated. | One transaction with the copy. | — |
| Columns **`check` and `window` are `check_name` and `time_window`**: both are reserved words in SQL. `audit_events.detail` is text. | — | — |
| **The Lease stays.** Claims, sweeps and notices are there and tested, but Sherlock's conversations and Watson's cards still live in the leader's memory. | ADR-163. | Remove it when cards, conversations and runs are rows. |
| PostgreSQL **18** in the bundled image; **16** the minimum for an external one. | Open question 1 of the design document. | `PG_MAJOR` in the Dockerfile; `db.MinServerVersion`. |

The design document and *221B storage engine options* (Claude docs, not in the repository) have not been updated with these.

## What to do next, in order

### 1. Verify and ship what is here

- The five untested items above.
- **CI and release**: build, scan, sign and publish `221b-postgres` beside `221b` in `ci.yml` and `release.yml` (same tag, SBOM and provenance as the console's image); pin `debian:13-slim` and the Go job's `postgres:18` service by digest in `hack/base-images.env`, as the other images are; check that the licence step accepts pgx and puddle (MIT) and pgpassfile, pgservicefile (MIT).
- **The app**: a banner on pages that need the database while `GET /api/v1/database` says it is away, and the database's state on Administration's home (DB-15).
- **`-race` on CI's two cores**: see *Test results*.

### 2. Move the domains

One repository at a time, as [database.md](database.md) describes, in this order: what is simple and foundation first. The table is a starting point made from `cmd/221b/storage.go`; check each line against the code before starting it.

| Order | Record set (key, package) | Tables | Notes |
| --- | --- | --- | --- |
| done | `audit.StoreKey`, `audit`; `sessions.HistoryKey`, `sessions`; `backup.SettingsKey` and `StatusKey`, `backup` | `audit_events`; `toolbox_sessions`; `settings` scope `backup`, `backup_runs` | Moved in this session. |
| 2 | `api.SetupKey`; `mail.ConfigKey`; `casework.SettingsKey` | `settings` scopes `setup`, `email`, `cases` | Singletons: `db.LoadScope`/`SaveScope`, as `backup.PGRepo.Settings`. |
| 3 | `kube.SettingsKey`, `settings.Service` | `settings` scopes `general`, `sherlock`, `watson`; `alert_acks`; selector `fleets` | The hardest of Platform: one document today, three tables. Give `settings.Service` a repository with `SaveGeneral`, `SaveSherlock`, `SaveWatson`, `Ack`, `Unack`, `SaveFleets`; it keeps its in-memory `State` and reloads on a notice (`db.Subscribe("settings", …)`) instead of every 3 seconds. Fleets need Inventory's decisions (to-do 1 to 3 below). |
| 4 | `apps.RepositoriesKey`, `apps.Repos`; the applications' records; the credentials' index; the cluster connections' metadata | `repositories`, `applications`, `credentials`, `clusters` | Secrets stay in Secrets; the tables hold the metadata and `secret_ref`. `clusters` must exist before Inventory's and Platform's keys into it mean anything. |
| 5 | `twoperson.Key`; `cards.StandingKey`; `cards.NotifyKey` | `approval_requests`, `approval_decisions`, `approval_policies`, `notification_channels` | Approvals is what the other domains' `approval_request_id` points to. |
| 6 | The sign-in sessions (`kube.SessionStore`) | `auth_sessions` | With to-do 13: a signed session when the database is away. |
| 7 | `nodessh.HostsKey`, `nodessh.PairingsKey`, `automation.GroupsKey`, `automation.FactsKey` | `hosts`, `host_pairings`, `host_logins`, `fleets`, `fleet_hosts`, `host_facts`, `data_centers` | After to-do 1 to 3: `nodessh.Host` carries labels, the login user and the pinned key in one struct, and the model splits them and has no labels. |
| 8 | `automation.ScriptsKey`, `ChangesKey`, `VariablesKey`, `ProfilesKey`, `RunsKey` | the 14 Automation tables | With to-do 11. Runs are the largest record set. |
| 9 | `ai.ProvidersKey`, `AgentsKey`, `PromptsKey`, `TestsKey`, `PilotKey`; `sherlock.SpendKey` | the 9 AI configuration tables | `agents` rows are seeded from the catalog; `ai_spend` is a ledger (insert a row a call, sum on read). |
| 10 | `jobs.JobsKey`, `RunsKey`; `casework.ChecksKey`, `RulesKey`, `FoundKey` | the 6 Jobs tables | `schedule_claims` replaces "on the leader" for schedules (`db.Claim`). |
| 11 | `issues.IndexKey` and the case ConfigMaps (`kv.Objects`); `issues.ConfigKey`; the casework's work; the RCAs | the 8 Cases tables | The case ConfigMaps go; `casework/clean.go` has the old shapes. |
| 12 | `cards.CardsKey`, `ChatsKey` | the 5 Changes tables | The console calls a change a card. |
| 13 | `knowledge.SettingsKey`, `DraftsKey`, `StatusKey`; `fixes.Key`; `lessons.ExceptionsKey`; `issues.KnownKey` | the 10 Knowledge tables | Wiki bodies stay in git or S3. |

For each: the backup's export and restore (`cmd/221b/backupparts.go`, `internal/api/backup.go`) read through the service, so they follow the repository; check them. Remove a domain's ConfigMap from the chart one release after its move.

When the last domain has moved: default `database.mode` to `bundled`; remove the Lease and the forwarding to the leader (ADR-163); remove `kv.Limited` and the size metrics.

### 3. The code to-do list agreed with the owner

Agreed during the data model's review on November 18, 2026; none of it is done. Items 1 to 3 come before Inventory moves, 11 with Automation, 12 and 13 with Identity.

1. **Drop host labels**: the field on `nodessh.Host`, the `host.label` card action, and hosts matching selector fleets. A host is in a fleet by being listed.
2. **Runs and fleets with no cluster**: a list fleet or a host whose cluster is gone shows under every cluster, and a run may target it.
3. **Removing a cluster**: ask, for each node, whether to delete it or keep it as a host (capturing its address first); delete the selector fleets; keep the list fleets and hosts with their cluster cleared.
4. **Network facts** in the facts script, under `net`, `time` and `lldp`: interfaces with MAC, state, MTU, speed, driver and firmware; bonds with their members' state; addresses and CIDRs; the gateway and routes; the VLANs the host sees; DNS resolvers and search domains; the full host name; the time source and offset; listening ports; whether a firewall is on; proxy settings; the LLDP switch, port and VLAN.
5. **`maxFacts`** (60): raise it, or nest the network facts under one key.
6. **Optional privileged facts**: LLDP through `lldpctl` (needs the lldpd group or root), firewall rules, which process holds each port.
7. **Measured by the console**: SSH connect time and failures, reverse DNS.
8. **Rack suggestions** from a shared switch and subnet, and drift when a host's facts no longer match its rack (`hosts.rack_name`, `data_centers`).
9. **New validation rules**: a degraded bond, a slow link, clock offset, an MTU that differs within a rack, an unexpected listening port, resolvers that differ within a rack.
10. **`node_facts` for the Troubleshooter and the Auditor**, with the tool's description updated.
11. **The run engine writes a run's hosts, fleets and values together**, so the rows of `script_run_hosts`, `script_run_fleets`, `script_run_values` and `script_run_host_fleets` never mix two runs.
12. **Local accounts stay in their Secret**, written conditionally; they are not moved.
13. **Sign-in during a database outage**: a short-lived session signed with a key from a Secret, which any replica verifies without the database and which cannot be revoked early; pages that need the database say it is unavailable.

## Working on this

- Go tests that need PostgreSQL: `make test-db` (Docker), or `TEST_DATABASE=host:port go test ./...` against any PostgreSQL 16 or later that lets `postgres` in without a password (`TEST_DATABASE_USER`, `TEST_DATABASE_PASSWORD` otherwise). Each test makes and drops its own database.
- A migration file may be edited in place until a release ships it; after that a change is a new file, and the checksum check refuses an edited one. Editing one makes existing development databases refuse to start: drop them.
- Every commit is signed off (`git commit -s`), names its requirement, and keeps `docs/requirements.md`, `docs/architecture.md` and `CHANGELOG.md` in step (CONTRIBUTING.md).

## Test results

The last full run in this session, on a 2-core machine: `TEST_DATABASE=127.0.0.1:55432 go test -race ./...` in `server/`, after every code change on the branch but the last (the Toolbox's session records, whose packages, `internal/sessions`, `cmd/221b` and the backup tests of `internal/api`, were run again after it and pass).

- **54 packages passed**, `internal/db`, `internal/audit`, `internal/backup` and `cmd/221b` with their database tests among them. `make lint-go` (gofmt, `go vet ./...`, the prompt sources) is clean.
- **One test failed: `TestACapLoweredDuringARunHolds` in `internal/ai`**, a package this branch does not touch. It is sensitive to timing: it expects some test cases to be still waiting when a cap stops the run, and under the race detector on two busy cores none were. On `main`'s code it failed 1 time in 50 under the same load and 0 in 19 without; on the branch 2 in 10 in one batch and 0 in 25 in another. Treat it as a flaky test to fix on its own (make the waiting cases wait on the test's own gate, not on the scheduler), not as a regression; if it fails in CI, run it again before looking further.
- **`internal/api` took 566 seconds** under the race detector here, close to `go test`'s 10-minute limit per package. CI's runners are faster; on a small machine pass `-timeout 30m`.
- Before the changes, `go test ./...` without `-race` passed all 54 packages on the branch's first commit.
- The browser app's tests (`npm test`, `make e2e-sim`) were not run: the app was not changed.
