# Phase 13 handover: storage in PostgreSQL

Written on November 19, 2026, for the session that continues Phase 13. It replaces the handover of November 18. Read [database.md](database.md) (how the database is run, and how to write a repository) and [data-model.md](data-model.md) (the 79 tables and their rules) first; the requirements are DB-01 to DB-38 in [requirements.md](requirements.md), and the decisions ADR-162 to ADR-164 in [architecture.md](architecture.md).

## Start here

- **The branch is `phase-13-store`**, pushed. Nothing is merged, there is no pull request and no release, and CI has not run on it.
- **The owner's rules for this work:** push to `phase-13-store` only; no merge, pull request, release or CI run until he says so; every commit signed off. The session that tests in his cluster builds, deploys and tests, and changes nothing in the repository: it writes findings to a file, which he passes on.
- **Most record sets are in the database** when one is configured: Activity log, Approvals and Inventory whole, Platform's ConfigMap-kept records, and Automation, AI configuration and Jobs whole. Cases, Changes, Knowledge and what is kept in Secrets have not moved. Without a database (`database.mode=none`, the default) nothing changes for an installation.
- **A fifth cluster test is running** on commit `6cb0fb8` (schema 26): the upgrade from schema 19, the run requests, AI configuration, Jobs, and three members with a switchover and a failover. It was sent before the three commits that followed (no spend history, step `0027`, a sensitive variable removed entirely), so those have not been in a cluster.
- **One member of the bundled database has run in a cluster**, four times. The first test found ten things, the second four more, the third nothing new, and the fourth (commit `37af7dd`, schema 19) one thing that matters with three members, fixed since, and a restore that worked. Three members and a failover have never run. Steps `0020` to `0027` (the run requests, AI configuration, Jobs) have not been in a cluster at all.
- **Do next, in this order:** the fifth cluster test's findings, when the owner passes them (the test was running on commit `6cb0fb8`, sent before step `0027`; its message is in this session's chat, not here); then iteration A under *Move the domains*: Changes and the stand-alone Knowledge keys, then `db.DocSet` and Cases with the known issues; then iteration B, what is kept in Secrets, on its own and with its own cluster test.

## Where the branch stands

| Commit | What |
| --- | --- |
| `Storage: the db package, the schema's 73 tables and the replicas' coordination` | `server/internal/db`: pool, transactions, health, UUIDv7 keys, the embedded migrations, advisory-lock sweeps, claims, `LISTEN`/`NOTIFY` with a poll; `dbtest`. |
| `Storage: the server opens the database it is given` | The `-db-*` flags, `cmd/221b/database.go`, `GET /api/v1/database`, the `b221_database_*` metrics. |
| `Storage: the 221b-postgres image and the chart's database modes` | `deploy/postgres` (Dockerfile, Patroni entrypoint, bootstrap, restore and backup scripts); `database.mode` none, bundled, external in the chart; `hack/check-chart.sh`. |
| `Activity log: the audit log is kept in the database when there is one` | `audit.PGStore`, `db.ImportOnce`, the `imports` table. |
| `Backups: the settings and the runs behind a repository…` | `backup.Repo` with `KVRepo` and `PGRepo`, `db.LoadScope`/`SaveScope`. The pattern for a repository. |
| `Activity log: the Toolbox's session records…` | `sessions.PGHistory` and `sessions.Archive`; the backup reads and merges through it. |
| `Platform: getting started, email and case settings…` | `db.ScopeKV`, `kv.Keyed`: a service's singleton documents as scopes of `settings`. |
| `Approvals: requests, decisions, standing approvals and channels…` | `db.Doc` and `db.Diff`, the interim step; `twoperson/pg.go`, `cards/pg_approvals.go`. |
| `Platform: the shared settings and the acknowledged alerts…`, `…the repositories…` | `settings/pg.go`, `apps/pg.go`. |
| `Hosts carry no labels (to-do 1, server side)` | The first of the code to-do items. |
| `Inventory: hosts, pairings, fleets and facts…` | Package `inventory`; `nodessh/pg.go`, `automation/pg_facts.go`, fleets in `settings/pg.go`. |
| `Automation: script groups and execution profiles…` | `automation/pg_defs.go`. |
| `Storage: what the first cluster test found (DB-17, DB-18, DB-31)` | The ten fixes of the cluster test; `db.ImportNewer`, step `0015`; `hack/check-db-backup.sh`; the chart and the app at 0.13.0. |
| `Automation: the scripts and their versions…(DB-32)` | `automation/pg_scripts.go`, step `0016`: three more tables, 76 in all. |
| `Automation: a node's own variables are the node's…(AU-23, DB-33)` | The service changed to fit the model: `variables.go` (`View`, `SetVariablesFor`, `nodesOwn`), the API's request to set a fleet's variables, the card action, the backup's restore; `automation/pg_variables.go`, step `0017`. |
| `Automation: a variable change is what it alters…(DB-34)` | `automation/changeitems.go` (`Items`, `Sides`, `weigh`, `applyItems`), `pg_changes.go`, step `0018`. |
| `Automation: the runs are rows…(DB-35)` | `automation/pg_runs.go`, step `0019`. |
| `Automation: the runs that wait for approval are rows of the runs…(DB-37, DB-38)` | `pg_changes.go` (`readRequests`, `writeRequests`), `pg_runs.go` (a run takes its request's row; a run's fleets), steps `0020`, `0021`; `FleetsOf` and the engine's `Spec.Fleets`. |
| `Storage: the bundled database's pods change only what is theirs (DB-36)` | The narrowed Role, and the decision to keep Patroni with one member. |
| `Storage: what the second cluster test found` | B11 to B14: the steps' checksums written down and tested, the backup container's look at once and its reading of the history files, two messages. |
| `AI configuration: providers, agents, instructions, tests, the pilot log and the spend are rows (DB-39, DB-40, DB-41)` | `ai/pg_config.go`, `ai/pg_prompts.go`, `sherlock/pgspend.go` and the Ledger's `UseRows`; steps `0022` to `0024`; `GET /api/v1/database` names every domain moved. |
| `Jobs: the jobs, their runs, the checks, the rules and what the runs found are rows (DB-42, DB-43)` | `jobs/pg.go`, `casework/pg_rules.go`; steps `0025`, `0026`; `make test-go-store` and its CI step. |
| `AI spend: no history is kept (DB-41)`; `Case work: the suggestions dismissed and the runs counted are rows; a sensitive variable removed entirely needs no second person` | What the owner decided after the fourth test: `SpendPG.Add` removes old rows; step `0027`; the API's rule for a sensitive mark. |
| `Storage: what the fourth cluster test found` | F1: the database pods' Role names Patroni's `-failover` ConfigMap too. F2: Run History shows a node's batch as the engine counts it. |
| `Docs: …` (five commits) | The documents, the changelog, the Go CI job's PostgreSQL service, this handover. |

The first two commits on the branch are not this work's: `CI: cache Playwright browsers…` (the owner's other session; reviewed, no problems found) and `docs: refresh the README screenshots` (a bot's, the one commit without a sign-off).

## The cluster test of November 19, 2026

A second session built both images and upgraded a 0.12.0 release in place on the owner's RKE2 cluster (namespace `221b-github`, release `221b-github`), on commit `dc0fe32`. Its report is `phase-13-findings.md` (with the owner; the raw logs are in `~/p13` on `rke2cp1`). The report ends with the commands that remove everything the test made. As the report left it, the release is at revision 17 in `external` mode against a throwaway PostgreSQL in the same namespace, on the test's image, and the bundled database's volumes and Secrets are still there.

**What held:** the upgrade to `bundled` (primary labelled, the Service, `verify-full` over TLS 1.3, the role and database, schema 14, `GET /api/v1/database`); the copy of the audit log (126 events, once, ConfigMaps untouched, nothing twice after a restart); new audit events in the database; the database pod deleted and back on its volume with the console reconnecting by itself; a base backup to the volume; the rollback to 0.12.0; `external` with `verify-full` and a CA, as a role that is not a superuser.

**What the first test found, and what was done about each:**

| # | Found | Done |
| --- | --- | --- |
| B1 | `make image` and `make image-postgres` need BuildKit; Docker 20.10 does not use it unasked. | The Makefile sets `DOCKER_BUILDKIT=1`. |
| B2 | The database image never built: its `PATH` had no `sbin`, so `groupadd` was not found. | `PATH` has the `sbin` directories. With that the image built there: PostgreSQL 18.7, Patroni 4.1.5, WAL-G v3.0.10. |
| B3 | The backup container ignored SIGTERM, so every stop of a database pod took the whole 60-second grace period (47 s of a 68 s outage). | `backup.sh` traps TERM and INT and waits on what it runs, also during a base backup. |
| B4 | The console waits for the database before it listens, and the liveness probe killed it after 60 s. The wait was also up to eight minutes, not two. | A startup probe (seven and a half minutes with a database, one without); the wait is two minutes by the clock (`databaseWait`). |
| B5 | A day's base backup was given up if Patroni did not answer in the one minute it was due. | Due from `dailyAt` until the day's end; a day is marked done only once someone took it; a restart after the hour does not take it twice; a failed one is tried again after ten minutes. |
| B6 | To a volume with three members, the base backup went to a standby's volume and the log to the primary's, so no volume could restore. | To a volume the primary takes it, and a member that becomes the primary after another one was takes one at once. To a bucket nothing changes. Still true: after a failover the time before it is only in the old primary's volume, so the docs recommend a bucket with `ha`. |
| B7 | The docs did not say that a restore needs the old Secret's `backup-key` and, to a volume, the old backup volume. | `database.md`, *Restore*, and the values file say what to keep and copy. |
| B8 | The chart still said 0.12.1. | 0.13.0 in `Chart.yaml`, `web/package.json` and the lock file; the changelog's section is `0.13.0 (unreleased)`. |
| B9 | After `helm rollback` to a revision from before the database, the mode guard saw no annotation and let `external` through, leaving the bundled database's records behind. | The guard also looks for the bundled database's kept Secret, and refuses any mode but `bundled` while it is there (or `allowModeChange`). An external database leaves nothing to go by; the docs say so. |
| B10 | After upgrade, rollback, upgrade again, what the rolled-back console wrote to the ConfigMaps was never copied. | The audit log and the Toolbox's session records are read at every start and what is newer than the last copy is copied (`db.ImportNewer`, `imports.newest_at`, step `0015`). Record sets edited in place are not merged; the docs say the database's count. The backups' runs are among those. |

**The second test, the same day**, on commit `79440d3` (report: `~/phase-13-findings-2.md` on `rke2cp1`, logs in `~/p13-2`): all ten fixes held. Both images build with no patch and BuildKit unset. After a rollback, `external` and `none` are refused with the sentence that names the Secret, and `allowModeChange` lets them through. After a rollback and a roll forward, exactly the six events the rolled-back console wrote were copied, once. The database pod was gone in 2.8 s and the console had its database back at 9.4 s (was 68 s). A console that started with the database away for 100 s was not restarted; away for 200 s, it stopped by itself at about 116 s and was started again once. The backup container took its first base backup at once, none after its own restart, one at `dailyAt`. A script made, read and deleted through the API was rows of `scripts`, `script_versions` and `script_groups`. The release was left at revision 25: `bundled`, the images of `79440d3`, schema 16.

It found four more things, fixed in `Storage: what the second cluster test found`:

| # | Found | Done |
| --- | --- | --- |
| B11 | The first test's database (schema 14, from `dc0fe32`) could not be opened: steps `0001`, `0003`, `0004`, `0006` and `0011` had been edited after that build, while the domains moved, and the checksum check refused them. The handover of that morning was wrong to say such a database would get the later steps. The test went on with a new database. | No step is edited any more: `internal/db/testdata/steps.sum` holds every step's checksum and `TestAppliedStepsAreNotEdited` fails on an edit. The refusal names both checksums and says that a development build's database is made again. `database.md` has the rule. Steps `0001` to `0016` are as the cluster's database applied them. |
| B12 | The backup container wrote down the timeline it led only after its first 30-second wait, so pods that lived less never did, and the next start took a base backup that was not needed. | It looks at once. And whether another member led in between is read from the volume: the member that takes a timeline archives its history file, so with the history files of every timeline in between, the volume has all the log. |
| B13 | The mode guard's refusal named the Secrets and the volume claims to delete, not Patroni's ConfigMaps. | It names them. |
| B14 | After the two-minute wait the console's last message did not say that it had waited and was stopping to be started again. | It says so. |

Also seen, and left: the image's version comes from `git describe` and says `v0.12.1-NN` until a `v0.13.0` tag exists, while the chart says 0.13.0.

**The third test, the same day**, on commit `1077a44` (report: `~/phase-13-findings-3.md` on `rke2cp1`, logs in `~/p13-3`): everything held and no new bug was found.

- The second test's database (schema 16) took only step `0017`, with no crash loop: no step had been edited (B11).
- The variables (AU-23, DB-33): a value of All nodes and a node's own value and reboot time, set through the API, were a row of `fleets` of kind `all`, a `variables` row on the fleet, one on the host, and `hosts.reboot_minutes`; no row was on both. Taken back, the rows went, the node's row with them.
- B12: a database pod deleted four seconds after it was running left its timeline written down, and the next start took no base backup. B13 and B14: both messages as worded. B10 again: exactly the four events written while rolled back were copied.
- Not run there: a fleet of its own setting a member's value, and refusing one that is not a member (the release has only All nodes); the Variables page in a browser.
- One nit, fixed since: `curl` wrote its own error to the backup container's log before the script's line when Patroni did not answer.
- The release was left at revision 29: `bundled`, the images of `1077a44`, schema 17, and `localCluster.mode=write`, set at the owner's word for the test.
- For the next test: Helm keeps ten revisions, so `helm rollback 221b-github 13` no longer works; revisions 22 and 28 are copies of 13 (0.12.0, no database).

**The fourth test, the same day**, on commit `37af7dd` (report: `~/phase-13-findings-4.md` on `rke2cp1`, logs in `~/p13-4`). It left the release at revision 32: bundled, images `p13-37af7dd`, `localCluster.mode=write`, schema 19.

What held: the upgrade of the schema-17 database applied steps `0018` and `0019` only, and nothing at the next start. The narrowed Role (DB-36): no `403` in Patroni's log after the upgrade or after a pod was deleted, every `kubectl auth can-i` answer as expected, and a deleted pod was the primary again in 4.6 seconds and ready in 8. Variable changes (DB-34): the two changes of `changes.json` copied once, item for item; a change that needs approval waited with its items and impact rows while another value of the fleet was set; rejected, it kept its state, who decided and the comment; the change that put things back waited too and the owner approved it in the console. Approving as a second person through the API was not run. Runs (DB-35): a dry run gave the right rows of `script_runs` and `script_run_hosts`, read the same after a restart, and stayed after its script was deleted, with the script's name kept. Variables and Run History look and work as before in a browser. **A restore in place ran for the first time and worked**: recovery went from the base backup of timeline 7 through the history files 8 to 17, and 79 of 80 tables were the same by checksum (the other, `audit_events`, had the restored console's own first event). The third test's nit is fixed.

| # | Found | Done |
| --- | --- | --- |
| F1 | With three members only: the Role's ConfigMap names lacked `<release>-db-failover`, which Patroni writes for a switchover or a failover. | Added to the Role, to the names the chart's mode guard lists, and to the docs; `hack/check-chart.sh` checks it. **Not seen in a cluster**: check with `patronictl switchover` in the test with three members. |
| F2 | From before Phase 13: Run History showed a node's batch one too high (the engine counts batches from 1, the page added 1). | Fixed in `RunsSection.tsx`; the page's test fixture counts from 1 now. |
| N2 | `imports.record_count` is 1 for a document copied through `db.Doc`, whatever it holds (`changes.json` held two changes). | Left: a `db.Doc` copies one document and does not know its records. The count is of documents there. |
| N3 | Removing a sensitive variable entirely, value and mark, still waited for a second person. | The owner decided it should not: a removal that leaves no value under the name, the fleet's or a member's own, goes at once (AU-20). |
| N4 | From before Phase 13: a run's full output is lost on a restart unless the Toolbox's transcripts are on a volume; the database keeps `output_tail` only. | Noted. A run's output could become rows or an object when the runs get a repository. |
| — | `variables.updated_by` is empty for values set through the API. | Left: the service's document does not say who set a value (the audit log and `variable_changes` do). With the variables' repository. |

**To see in a cluster next:**

0. **First, since nothing but a cluster shows it: three members and a switchover.** The database pods' Role lets them change only the ConfigMaps `<release>-db-config`, `-leader`, `-failover`, `-sync` and `-failsafe` and the pods `<release>-db-0` to `-2`. The fourth test saw this hold with one member; `-failover` was added after it (F1) and only `patronictl switchover` and a failover with `ha: true` show whether the list is complete. After the upgrade Patroni's log must have no `403` or `Forbidden`; delete the database pod and see it come back as the primary. If Patroni needs a verb or a name that is missing, the database does not start: put the old rule back (`database.yaml`, the Role) and report which call was refused.

1. The cluster's database (schema 17) gets steps `0018` to `0021` at start; the runs of the automation ConfigMap (`runs.json`), if any, are copied once into `script_runs` and Run History shows them as before; `changes.json` of the automation ConfigMap (the third test left two changes there) is copied once into `variable_changes`, each change with what it altered in `variable_change_items`, and Automation › Variables lists them as before.
2. With a fleet made first: a request for that fleet sets a member's own value (200, and All nodes shows it), and is refused for a node that is not a member (400).
3. On a production connection: ask for a variable change that needs approval, set another value of the same fleet meanwhile, approve the change as a second person. Both values are in afterwards.
4. **AI configuration** (steps `0022` to `0024`): at the first start with these steps the AI ConfigMap's five keys are copied once (the log names each key), and Administration › AI shows the same providers, agents, instructions with their versions, test cases and pilot answers as before. Save a provider, change an agent's model, save a draft and run its tests: each shows after a restart of the console. A provider's credential stays in its Secret (`<release>-ai-<id>`). The spend: `sherlock-spend.json` of the settings ConfigMap is carried over once (the totals on Administration › AI are the same before and after); ask Sherlock a question, and `ai_spend` has a row more with `question_count` 1; with two replicas both add rows and the totals are their sum. The test console never had an API key, so the spend needs one added first.
5. **Jobs** (steps `0025` to `0027`): the runs ConfigMap's `jobs.json` and `runs.json` and the cases ConfigMap's `cases-checks.json`, `cases-rules.json` and `cases-rules-found.json` are copied once; AIOps › Jobs shows the same jobs and runs, a run's panel its findings, facts and proposed changes; Administration › AI › Validation rules the same rules, suggestions and counts. Turn a scheduled job on and let its time pass with two replicas: one run, and `jobs.handled_at` set. Change a job and put it back to its kind's defaults: its row stays only while it has a handled time or a day's spend.
6. **Still never run:** three members (`ha: true`; the images must be in a registry or on three nodes, since the members are kept on different nodes), a switchover, a failover and its time, a restore to a time (one to the newest point ran in the fourth test), a client certificate, a pooler with `listenerHost`, a new Toolbox session record.

## What was tested without a cluster

Go 1.26.9, PostgreSQL 16.15, Helm 4.3.1, on a 2-core machine.

- **The full Go suite with the database**, `TEST_DATABASE=127.0.0.1:55432 go test -race -timeout 30m ./...` in `server/`: all 55 packages that have tests pass. It was run after each record set moved, the last time on the commit that moved Jobs.
- **`make test-go-store`**: every test of the agents' (`internal/ai`), the jobs' (`internal/jobs`) and the case work's (`internal/casework`) packages again with `TEST_STORE=database`, which puts the keys those services keep into the database's tables (`testStore` in each package's tests). All pass. This is the strongest evidence that the services behave the same over the tables: several hundred tests written for the ConfigMaps, none changed but four lines that looked into the store directly.
- `make lint-go` (gofmt, `go vet ./...`, the prompt sources, `hack/check-db-backup.sh`), and `shellcheck` on `hack/` and `deploy/postgres/`: clean.
- `hack/check-chart.sh`: all 107 checks pass, the database's included, and every render is valid against the Kubernetes 1.35 and 1.37 schemas.
- `hack/check-db-backup.sh` runs the backup container against stand-ins for Patroni and WAL-G: who takes the base backup, when, and that it stops within a second of being told to.
- Before the cluster test, the real server was started against the simulator with its ConfigMaps and a database: it migrated, copied each of the 13 record sets moved by then once, served `GET /api/v1/database`, logged no error, and at a second start copied nothing. This was not repeated after the scripts moved; their copy is covered by `TestScriptsInTheDatabase` and re-test item 3.
- The Patroni file the entrypoint writes parses as YAML in its three shapes (plain, with backups, restoring), and `post-bootstrap.sh` was run twice against a real PostgreSQL.

**Not tested anywhere:**

- `hack/e2e-kind.sh`, `ha-kind.sh`, `upgrade-kind.sh` were not run; each needs a database variant.
- The browser app was not changed, and its tests (`npm test`, `make e2e-sim`) were not run. Its version is 0.13.0 now. AU-23 kept the API's shape and the app's own rule (it edits a node's own values under All nodes, `web/src/lib/variables.ts`), so the Variables page should need no change; it has not been looked at in a browser since.
- CI has not run on the branch. Its chart job has a new step (the backup container's check) and runs `shellcheck` on `deploy/postgres/` too, and its Go job has a `postgres:18` service and a new step, `make test-go-store`.

**Two notes on the Go suite:**

- **One test is flaky under load: `TestACapLoweredDuringARunHolds` in `internal/ai`**, in code this branch does not touch. It failed in one full run and passed in every one since. It expects some test cases to be still waiting when a cap stops the run, and under the race detector on two busy cores sometimes none are. On `main`'s code it failed 1 time in 50 under the same load and 0 in 19 without. Fix it on its own (make the waiting cases wait on the test's own gate, not on the scheduler); if it fails in CI, run it again before looking further.
- **A second one failed once under the same load: `TestTransformsKeepTheCacheSmall` in `internal/kube`**, also untouched by this branch. It found the informers' cache not filled yet (most kinds at zero) in one full run, and passed three times of three alone under the race detector. It needs to wait for the cache instead of expecting it.
- **`internal/api` takes about 570 seconds** under the race detector on two cores, close to `go test`'s 10-minute limit per package. CI's runners are faster; on a small machine pass `-timeout 30m`.

## What the owner decided on November 19

Asked how the variables should move, since the model's tables did not hold the service's documents as they were:

1. **Variables: follow the table design and change the service.** Done (AU-23, DB-33): a node's own values and reboot time are the node's, a variable is on one fleet or one host, the ceiling and reboot times are columns of `fleets` and `hosts`.
2. **Variable changes: keep items** (name, old value, new value), as the model has them, not the fleet's whole variables before and after. Done (DB-34): approving a change sets what it alters, with or without a database.
3. **Patroni stays with one member too**, for this release (asked whether a single member needs it: it does not, but it is the one path that has run in a cluster, and turning `ha` on stays a values change). Decide again once three members have run in a cluster. Agreed with it: the database pods' permissions are narrowed to Patroni's own ConfigMaps and their own pods, as far as Kubernetes allows.
4. **The runs that wait for approval go in `script_runs`**, the same table as the runs, so that one page can show every run, the rejected ones too. Done (DB-37), in the tables; the page that shows them together is not built.
5. **The Claude docs** (data model, deployment design) are updated with the next round of work, from the table of decisions below.
6. **CI runs when the branch is ready for release**, not before.

## Decisions made for the owner to confirm

These differ from the design document *221B storage deployment design*, or were not in it. The last sixteen rows are from November 19.

| Decision | Why | To undo |
| --- | --- | --- |
| `database.mode` defaults to **`none`**, not `bundled`. | Six domains are still in ConfigMaps; a default that adds a StatefulSet, a volume and a second image to every upgrade, before the records use it, would break upgrades and CI. | Flip the default when the move is complete. |
| The base backup runs at a **time of day** (`backup.dailyAt: "02:00"`), not a cron expression. | A shell loop in the pod cannot parse cron. | Replace `backup.sh` with a small program. |
| **`iamAuth`** (RDS, Cloud SQL tokens) is not implemented. | Time. | Add a token source in `db.open`'s `BeforeConnect`. |
| **Quorum commit is on with one member too** (it waits for nobody then). | So turning `ha` on needs no change to Patroni's stored configuration. | — |
| The audit log's **retention is a year**, a constant. | It needs a setting and a place on the page. | `auditRetention` in `cmd/221b/main.go`. |
| The import is written down in a table, **`imports`**; the ConfigMap is not annotated. | One transaction with the copy. | — |
| Columns **`check` and `window` are `check_name` and `time_window`**: both are reserved words in SQL. `audit_events.detail` is text. | — | — |
| **The Lease stays.** Claims, sweeps and notices are there and tested, but Sherlock's conversations and Watson's cards still live in the leader's memory. | ADR-163. | Remove it when cards, conversations and runs are rows. |
| Tables that point at a **cluster or a credential** get that row made with only its id. | The clusters' and credentials' own records are in Secrets and have not moved. | Fill the rows when those move (order 9). |
| Ids the console already gives are kept as **text keys**: Toolbox sessions, standing approvals, channels, credentials; a fleet keeps `fleet_key` beside its UUID. | The audit log, the Secrets and other records carry those ids. | — |
| The **Secret-kept record sets** (applications, credentials, connections, sessions) are left for last. | See order 9. | — |
| PostgreSQL **18** in the bundled image; **16** the minimum for an external one. | Open question 1 of the design document. | `PG_MAJOR` in the Dockerfile; `db.MinServerVersion`. |
| The member that takes the backup is **picked from Patroni's `/cluster`**, not by an advisory lock: to a bucket the first standby by name, else the primary; to a volume always the primary, where the log is, and a new primary takes one at once. | Nothing is run in another pod either way. The volume's rule came from the cluster test (B6). | — |
| **Only the audit log and the session records are copied again after a rollback** (what is newer than the last copy). Everything edited in place is copied once, and the database's records count from then on. | A log can be merged by time; a settings document changed on both sides cannot. | Say which side wins per record set, if another answer is wanted. |
| **While the bundled database's Secret exists, any mode but `bundled` is refused** without `allowModeChange`, also on a first install under a name used before. | The Secret and the volumes are kept on purpose; a rollback leaves nothing else to go by. | Delete the Secret, or drop the check in `b221.validateDatabase`. |
| **The schema has 76 tables, not the model's 73.** Every field a column means a version's lists of records are tables: `script_version_kube_actions`, `script_version_findings`, `script_version_actions`. A list of plain values is an array column (`script_versions.facts`). | The owner's rule for the move: no record kept as JSON beside its keys. | Fold them back into JSON columns in a new step. |
| **Approving a variable change sets what it alters**, also without a database: a value the change does not name, set meanwhile, no longer holds the approval. | The owner chose items; a change kept as items cannot replace the fleet's variables whole. | `weigh` and `applyItems` in `automation/changeitems.go`. |
| **The schema has 77 tables**: `variable_change_impacts` holds what was `variable_changes.impact` as JSON. `variable_changes` names its fleet by `cluster_id` and `fleet_key`, not by a key to `fleets`. | Every field a column; a change outlives its fleet. | — |
| **The runs that wait for approval are JSON in a settings scope for now.** | They are in the changes' document and belong with the runs, which have not moved. | Order 3. |
| **A run names its profile by id, not by a key** (`script_runs.run_profile_id` has no foreign key), and its key to its script's version is optional. | A run is kept after its profile and its script's version are gone; the model's keys would have stopped them being removed. | — |
| **The schema has 79 tables**: `script_run_approvers`, `script_run_impacts`. | Every field a column. | — |
| **A request for a run and the run are one row with two numbers** (`request_number`, `run_number`), and `run_number` may be empty. | The owner chose one table; requests and runs are numbered apart, both from 1. | — |
| **A run's document grew**: `fleets`, and `fleets` of each node (to-do 11). A ConfigMap's runs are a little larger for it. | So that a run says where its values came from, with or without a database. | `FleetsOf` in `automation/variables.go`. |
| **The database pods' Role is narrowed**: get, patch, update and delete only on Patroni's five ConfigMaps by name (`-failover` added after the fourth test), patch and update only on the database's own pods. They can still create, list and watch ConfigMaps and list pods in the namespace: Kubernetes cannot limit those to names. | The pods could change every ConfigMap in the console's namespace, the console's own records among them. Not yet run in a cluster. | The Role in `deploy/helm/221b/templates/database.yaml`. |
| **A script may be in no group**, and a group a script names gets its row when the script is written. | The store allows both; `scripts.script_group_id` was required. | — |
| **Where two fleets kept different own values for one node, the first by fleet id is used** (All nodes' before any), and the longer of two reboot times. | Such a node could not run a script that read the variable; one of the two values a person set for it is closer to what was meant than the fleet's. `upgrading.md` says so. | `nodesOwn` in `automation/variables.go`. |
| **`fleets.kind` has `all` and `unlisted`**, rows the settings' list of fleets leaves out: All nodes, and a fleet that variables name before the list has it. | A variable must be on a fleet's row or a host's; All nodes has variables and a ceiling and was not a row. | — |
| **At most 5,000 nodes of a cluster may have own values** (was 500 a fleet). | They are kept once a cluster now. In a ConfigMap the document's 64 KiB is still the limit. | `maxNodeOverride`. |
| **The schema has 98 tables.** AI configuration added 6 (`ai_provider_headers`, `prompt_version_notes`, `prompt_version_parts`, `test_run_cases`, `test_case_result_checks`, `test_case_recordings`), Jobs 11 (`job_run_facts`, `job_run_proposals`, `job_run_proposal_cases`, `checked_clusters`, `rule_suggestions`, `rule_suggestion_descriptions`, `check_found` and its four), and step `0027` two more (`dismissed_checks`, `counted_runs`). | Every field a column: what was JSON, or a list of records inside a document, is a table. | — |
| **Ids the console gives are text keys**: `ai_providers.ai_provider_id`, `test_cases.test_case_id`, `jobs.job_id`. The model had them as UUIDs. | Agents, Secrets, runs and backups name these by the console's id; a second id would have to be mapped everywhere. | — |
| **Several of the model's keys between tables are names now, not keys**: an agent's and a spend row's provider; a result's test case; a pilot entry's instructions; a run's and a claim's job; a finding's rule, case, known issue and exception; a rule's replacement. | What they name may have no row (the default provider, a built-in test case or rule, a job nobody changed, a domain not moved yet) or may go while the row that names it stays. | Cases and Knowledge, when they move, can make a finding's three a key again. |
| **An agent's instructions are rows of `prompt_versions` in four states** (`in_force`, `version`, `draft`, `revision`), and an agent whose instructions changed has its rows written again, whole, with new row ids. | The service keeps what is in force, the versions, one draft and its revisions as one document an agent; the model had one row a version with a state from draft to live. | A repository would keep a row's id; `approval_request_id` stays empty until then. |
| **A provider's credential reference (`ai_providers.secret_ref`) is not used.** | The Secret's name follows from the provider's id. | Drop the column with the repository. |
| **`ai_spend` is a ledger now, and the service was changed for it** (`Ledger.UseRows`): a row a cost, summed when read. The model's `provider_name`, `tokens_in`, `tokens_out` are gone; `usd` is a float; `is_share_only` marks the rows that carry over the sums a ConfigMap or a backup held. | The engine counts cost, not tokens. A backup holds sums, not rows, so putting one back replaces this month's rows with rows of its sums. | No history, the owner decided on November 19: rows from before the month before this one are removed once a day, when a cost is added (`SpendPG.Add`). |
| **Costs kept as `numeric(14,6)` are rounded to a millionth of a dollar** (a test's, a run's, a pilot answer's, a provider's prices and limits, an agent's budgets). The spend ledger and a job's day spend are floats, as the service adds them. | The model's type. | — |
| **The jobs' schedules do not use `schedule_claims`.** A scheduled time is claimed by setting the job's `handled_at` in the transaction that read it, which is what the service did on the ConfigMap's version. The owner was told the schedules would go through `db.Claim`; they do not yet. | `db.Doc` keeps the service unchanged, and its claim is already safe between replicas. `schedule_claims` is for the repository, where a claim is one insert and no document is read. | With the jobs' repository. |
| **`cluster_checks` lost the model's counts** (`first_seen_at`, `last_seen_at`, `finding_count`, `sample_title`); what the runs found is kept by check, in `check_found` and its four tables, not by cluster and check. | That is what the service counts. | — |
| **Counters and flags that are of no row are JSON in settings scopes**: the jobs' own settings and counter (`jobs`), the counters of runs, test cases and pilot answers (`job-runs`, `ai-tests`, `ai-pilot`), the agents' two flags (`ai`), and whether counting what the runs found began (`case-found`). The two lists of ids that were there too, the suggestions dismissed and the runs counted, are tables since step `0027`, as the owner decided on November 19. | Each is a singleton. | — |
| **An agent with nothing chosen for it is not in the agents' document read back**, and a list of tools or clusters that is empty reads back as none. | A row exists for every agent that something points at; the document's JSON drops empty values anyway. | — |

The Claude docs *221B storage deployment design* and *221B data model: domains and tables* (not in the repository) were brought up to date from this table on November 19, as the owner decided; *221B storage engine options* was not touched. What follows in this paragraph is how it stood before: the owner asked that changes to the data model be collected and made when he says.

## Decided by the owner after the fourth test

1. **No history of what the agents spend**: `ai_spend` keeps this month's and last month's rows (done).
2. **The suggestions dismissed and the runs counted are tables**, not JSON in settings scopes (done, step `0027`).
3. **Removing a sensitive variable entirely needs no second person** (N3; done in the API's request that sets a fleet's variables, AU-20). Taking the mark off a value that stays still waits.

4. **A Sherlock message is a row with its parts as one JSON value** (see the survey under *Move the domains*).
5. **Cases, Changes and Knowledge are done by a new code-writing session**, from this handover: the session that wrote it had grown long.

Nothing waits for the owner now.

## What to do next, in order

### 1. Verify and ship what is here

- The list under *To see in a cluster next*, and what was never run (three members, a switchover, a failover, a restore to a time).
- **CI and release**: build, scan, sign and publish `221b-postgres` beside `221b` in `ci.yml` and `release.yml` (same tag, SBOM and provenance as the console's image); pin `debian:13-slim` and the Go job's `postgres:18` service by digest in `hack/base-images.env`, as the other images are; check that the licence step accepts pgx and puddle (MIT) and pgpassfile, pgservicefile (MIT).
- **The app**: a banner on pages that need the database while `GET /api/v1/database` says it is away, and the database's state on Administration's home (DB-15).
- A database variant of `hack/e2e-kind.sh`, `ha-kind.sh` and `upgrade-kind.sh`.

### 2. Move the domains

**Moved** (each with tests over the database and a one-time copy; see the table at the top of [database.md](database.md)): Activity log, Approvals and Inventory whole; of Platform everything kept in a ConfigMap; Automation, AI configuration and Jobs whole.

**How each moved, and what that leaves to do.**

| Record set | How | Rework still due |
| --- | --- | --- |
| The audit log; the Toolbox's session records; the backups' settings and runs | A repository (`audit.PGStore`, `sessions.PGHistory`, `backup.PGRepo`). | None. |
| Getting started, Settings › Email, the case work's settings | Scopes of `settings` (`db.ScopeKV`). | None: they are singletons. |
| Approval requests and decisions (`twoperson`); standing approvals and channels (`cards`); the shared settings and acknowledged alerts (`settings`); the repositories (`apps`); hosts and pairings (`nodessh`); fleets (`settings`); node facts, script groups, execution profiles, the scripts with their versions, the variables, their changes and the runs (`automation`) | `db.Doc`: the service's document is rows with every field a column. The service is unchanged, except for the variables and their changes, where it was changed to fit the tables (AU-23, DB-34). The runs and the requests for a run are two documents that write one table (`pg_runs.go`, `pg_changes.go`): a request's row gets its run's number from the runs' document, and the changes' document writes the columns a request shares with its run only until the run has started. | A repository for each service, with operations of its own (`Ack`, `Unack`, `SaveFleet`, `PairHost`…), so that one record is read and written without the others and writers no longer take turns per document. `twoperson.prune` and the stores' byte limits are the ConfigMap's and can go then. `settings.Service` should reload on `db.Subscribe` instead of every 3 seconds. |
| The providers, the agents' settings, their instructions, test cases and pilot log (`ai`); the jobs and their runs (`jobs`); the checks, rules and what the runs found (`casework`) | `db.Doc`, the service unchanged. An agent's instructions, a job's run and a check's counts are written again whole when they change; a run that did not change is left alone. Writes go as one batch. | A repository for each: a test run saves the agent's instructions once a case, a job's run saves the runs' document a few times, and each such save reads every row of the document first. Fine at the ConfigMap's sizes (a few hundred runs), not beyond them. The stores' byte limits (`fitRuns`, `fitFound`) are the ConfigMap's and can go then. |
| What the agents spend (`sherlock.Ledger`) | A repository: `sherlock.SpendPG`, rows only ever added; this month's and last month's are kept. | None. |

**The iteration of November 19 is done**: (1) the runs that wait for approval into `script_runs`, with to-do 11 (DB-37, DB-38); (2) AI configuration (DB-39 to DB-41); (3) Jobs (DB-42, DB-43); (4) the Claude docs brought up to date from the table of decisions. **Two iterations are left**, as agreed with the owner: **A**, Cases, Changes and Knowledge, with one cluster test for the three; **B**, the record sets kept in Secrets, apart, since the secret part stays in the Secret and a mistake in sign-in sessions or cluster connections locks people out. The link from a run, a variable change or a draft's instructions to Approvals stays empty until Approvals has a repository (agreed).

**Still to move**, in this order. The owner's rule: **every field a column** (no record kept as JSON beside its keys), `db.Doc` allowed as the first step where a service edits its whole document in many places.

| Order | Record set (key, package) | Tables | Notes |
| --- | --- | --- | --- |
| 6 | `issues.IndexKey` and the case ConfigMaps (`kv.Objects`); `issues.ConfigKey`; the case work; the RCAs | the 8 Cases tables | `kv.Objects` needs its own adapter or, better, a repository. `casework/clean.go` has the old shapes. |
| 7 | `cards.CardsKey`, `ChatsKey` | the 5 Changes tables | `cards.StandingKey` and `NotifyKey` are done; extend `cmd/221b/cards.go`'s `wrap`. |
| 8 | `knowledge.SettingsKey`, `DraftsKey`, `StatusKey`; `fixes.Key`; `lessons.ExceptionsKey`; `issues.KnownKey` | the 10 Knowledge tables | Wiki bodies stay in git or S3. |
| 9 | What is kept in **Secrets**: the applications (`apps.Store`), the credentials' list (`credentials`), the cluster connections (`clusters.Manager`), the sign-in sessions (`kube.SessionStore`) | `applications`, `credentials`, `clusters`, `auth_sessions` | Skipped on purpose, with the owner told: they have no 1 MiB problem, each needs a store that keeps the metadata in the table and the secret part in the Secret, and sessions touch sign-in (to-do 12, 13). `wireDatabase` must then run before `wireClusters`. Fill the rows of `clusters` and `credentials` that today hold only an id, and remove a cluster's row when its connection goes (to-do 3). |

**What a survey of the three domains found** (November 19, before any of their code was written). They are larger than AI configuration and Jobs together, and two things about them are not the pattern used so far:

- **Cases needs documents that share one version.** `issues` reads `issues-index.json`, `issues-known.json` and `issues-config.json` at one version of their ConfigMap (`load` in `issues/store.go` reads again until the three versions are equal) and saves them passing one version along. Three independent `db.Doc`s have three versions, so the load never settles and the save conflicts. What is needed is a set of documents behind one counter (a row whose `version` is compared and set in the transaction that writes), read in one transaction: a `db.DocSet`, to be written. **The known issues (Knowledge) therefore move with Cases**, not after.
- **The index is a projection.** Every line of `issues-index.json` is `lineOf(case)` and nothing changes a line directly, so with the cases in a table the lines are read from the cases' rows and the index's document writes only its counter and the archived counts. `lineOf` reads the case's text (for a title) and its messages (for who is involved); keep those two as columns worked out when the case is saved, so that reading the index does not read every message. No second table of lines.
- **A case is an object with three keys and one version** (`kv.Objects`): `issues.CaseKey` (the case, about 80 fields with its messages, watchers and approvers), `issues.WorkKey` (`casework.Work`: the analysis, runs, proposals, the RCA, the history, drafts, commands) and `casework.ChatKey` (the conversation with the Troubleshooter). It needs an adapter of its own, with a version column on the case's row.
- **Changes** (`cards.CardsKey`, `ChatsKey`): a card has about 35 fields and nine lists (evidence, actions, plans, results, approvals, history, commands, the cases it resolves and who linked each). An action's and a condition's `args` are whatever the action's kind takes (`map[string]any`): they stay one JSON value, as the model has them (`change_actions.args`).
- **Sherlock's messages are in both** (`sherlock.Msg`: in a case's chat and in a change's chat): text, steps, references, view changes, things shown, token use, cost and hand-overs, which nest. The model keeps a chat message as time, role and body only. **Decided by the owner on November 19:** a row a message, with columns for what is asked of a message (its number, role, text, note, status, error, who asked, the agent, the model, the times, the cost and the token counts) and **one JSON value for its parts** (steps, references, view changes, things shown, hand-overs). A chat is read and written whole and never asked for by its parts, and the parts change shape as Sherlock gains features. This is the one place a record's parts are JSON by decision; write the tables once, for both chats.
- **Suggested order:** Changes and the stand-alone Knowledge keys (`knowledge.SettingsKey`, `DraftsKey`, `StatusKey`; `fixes.Key`; `lessons.ExceptionsKey`) first, each a `db.Doc` as Jobs were; then `db.DocSet`, and Cases with the known issues, as the last and largest piece.

**How to add one.** Write `NewXPG(d) *db.Doc[T]` with `Read` and `Write` beside the service, settle the table's columns in a new migration step, and add the key to the map in the service's wiring (`w.inTables` in `cmd/221b/settings.go`, `cards.go`, `automation.go`, `sherlock.go`, `jobs.go`, `casework.go`). Where the package's tests build their service in one place, let that place put the keys into the database when `TEST_STORE=database` (`testStore` in `ai`, `jobs`; `setupOn` in `casework`) and add the package to `make test-go-store`: every test the service already has then runs over the tables. The scripts are the fullest model of a document moved as it is: `automation/pg_scripts.go`, step `0016`, and `TestScriptsInTheDatabase`, which runs the same steps on a ConfigMap and on the database and compares every field of what they keep. The variables are the model of a service changed to fit the tables: the change was made in the service first, for both ways of keeping the records, with the ConfigMap's document left readable by the release before (`nodesOwn`), and the tables then held it as it was. Write each new test as those two are. Three things to know:

- A time read back from PostgreSQL has microseconds, not nanoseconds: tests use whole seconds, and a comparison with a stored time goes through `db.After`.
- `pgx.ForEachRow` reads every row into the same variables: copy a slice before keeping it.
- A parameter that may be null inside `coalesce` needs its type (`coalesce($4::timestamptz, now())`).
- In `automation`, a nil list of nodes means every node (`View`, `SetVariablesFor`); `NamedNodes` never returns nil. Pass an empty list, not nil, for no node.
- Many inserts of one write go as a `pgx.Batch` (`tx.SendBatch(ctx, b).Close()`), parents queued before their children.
- A value the document's JSON leaves out when empty (`omitempty`) must read back as none, not as empty: `listOf` in `ai/pg_prompts.go`. A time the JSON always writes is stored even when zero (year 1), one it leaves out when zero is stored as null.

For each: the backup's export and restore read through the service or through `w.spendBackend`, which is wrapped once so they find the moved keys; check them. Remove a domain's ConfigMap from the chart one release after its move.

When the last domain has moved: default `database.mode` to `bundled`; remove the Lease and the forwarding to the leader (ADR-163); remove `kv.Limited` and the size metrics; let the app say `database` where Settings says where the settings are kept (`storage` is still `configmap`).

### 3. The code to-do list agreed with the owner

Agreed during the data model's review on November 18, 2026. **Done: item 1, the server's side** (DB-29); its browser side is open: `HostDialog.tsx` and `HostPanel.tsx` still show label fields, which now do nothing. **Items 2 and 3 are not done**: Inventory's tables allow them (`hosts.cluster_id` and `fleets.cluster_id` may be empty), but the services still file every host and fleet under one cluster, and removing a cluster's connection does not yet touch Inventory's rows. Item 11 goes with Automation's runs, 12 and 13 with the sign-in sessions.

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
11. *(Done, DB-38.)* **The run engine writes a run's hosts, fleets and values together**, so the rows of `script_run_hosts`, `script_run_fleets`, `script_run_values` and `script_run_host_fleets` never mix two runs.
12. **Local accounts stay in their Secret**, written conditionally; they are not moved.
13. **Sign-in during a database outage**: a short-lived session signed with a key from a Secret, which any replica verifies without the database and which cannot be revoked early; pages that need the database say it is unavailable.

## Working on this

- Go tests that need PostgreSQL: `make test-db` (Docker), or `TEST_DATABASE=host:port go test ./...` against any PostgreSQL 16 or later that lets `postgres` in without a password (`TEST_DATABASE_USER`, `TEST_DATABASE_PASSWORD` otherwise). Each test makes and drops its own database. Without `TEST_DATABASE` the database tests are skipped, so a run without it proves little.
- **Migrations: a change is a new step, always.** Steps `0001`, `0003`, `0004`, `0006` and `0011` were edited while the domains moved, up to `a04cd87`; a database made by a build before that is refused and is made again (B11). Since then none is edited: `internal/db/testdata/steps.sum` and `TestAppliedStepsAreNotEdited` hold that. Add a new step's line to that file. A step may reshape an earlier step's tables while they hold no rows, as `0016` and `0017` do. `TestMigrate` counts the tables (79): change the number with the step.
- Both images need BuildKit; the Makefile turns it on. The bundled database's members are kept on different nodes, so three members need the image in a registry or on three nodes.
- A Helm `lookup` (the mode guard, the generated passwords and certificates) returns nothing in `helm template`: only a cluster exercises it.
- Every commit is signed off (`git commit -s`), names its requirement, and keeps `docs/requirements.md`, `docs/architecture.md`, `docs/data-model.md` and `CHANGELOG.md` in step (CONTRIBUTING.md).
