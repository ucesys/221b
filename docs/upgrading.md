# Upgrading 221B

An upgrade is a `helm upgrade`. Everything the console keeps in the cluster (its settings, the local accounts, the connections to other clusters, Sherlock's key and the signed-in sessions) lives in objects the chart does not overwrite, so it comes through. This page says how to upgrade, which upgrades are tested, what is migrated, and how to go back.

## How to upgrade

From the published chart:

```sh
helm upgrade k8s-console oci://ghcr.io/ucesys/charts/221b --version 0.9.1 -n 221b --reset-then-reuse-values
```

From a checkout of the repository, at the release's tag:

```sh
git checkout v0.9.1
helm upgrade k8s-console deploy/helm/221b -n 221b --reset-then-reuse-values
```

`--reset-then-reuse-values` keeps the values you installed with and takes the new chart's defaults for anything you never set. Add `-f my-values.yaml` or `--set` for what you want to change. Read the release's section below, and its notes in [CHANGELOG.md](../CHANGELOG.md), before upgrading.

The image follows the chart: a release's chart runs `ghcr.io/ucesys/221b:<its version>` unless `image.tag` says otherwise. If you pinned `image.tag` or `image.digest`, change it to the new release's.

## Which upgrades are tested

CI upgrades from each of the **two releases before** the one being built (P7-03), in kind: it installs the earlier release's chart and image, makes a local user on the Users page, saves settings, a fleet and an acknowledged alert, adds a cluster connection and Sherlock's key, then upgrades, checks that everything is there and that the user still signs in, rolls back, checks again, and upgrades once more (`hack/upgrade-kind.sh`). Upgrading from an older release works through the releases in between: from 0.6.5 to 0.9.1, go to 0.7.1 first.

## What is migrated

| Kept in | What | Format |
|---|---|---|
| The settings ConfigMap (`<release>`, key `settings.json`) | Refresh rate, date and time format, fleets, acknowledged alerts, Sherlock's and Watson's settings | Carries `"version"`; migrated when the console starts and saved in the new format on the next change |
| The accounts Secret (`<release>-users`, key `users.json`) | Local users made on the Users page, their groups and password hashes | Carries `"version"`, as the settings do |
| A Secret per connection (`<release>-cluster-<id>`, key `spec.json`) | The connection and its credential | Carries `"version"` since 0.6 |
| `<release>-sherlock` | Sherlock's API key: the default provider's credential since 0.12.0 | Unchanged |
| `<release>-ai` (from 0.12.0) | The AI agents: other providers, agents' settings, instructions and versions, test cases, the pilots' log; each provider's credential in `<release>-ai-<id>` | New in 0.12.0 |
| Session Secrets (from 0.7.0) | Who is signed in | New in 0.7.0 |
| The settings ConfigMap, key `toolbox-sessions.json` (from 0.9.1) | The Toolbox's open and ended sessions, for Administration › Sessions | New in 0.9.1 |
| The settings ConfigMap, keys `backup-s3.json` and `backup-s3-status.json` (from 0.12.0) | The scheduled backups to S3 and their newest runs | New in 0.12.0 |
| `<release>-backup` (from 0.12.0) | The S3 access key and the passphrase of backups encrypted on the console | New in 0.12.0 |
| `<release>-backup-key` (from 0.12.0) | The key every backup's secrets are sealed with (ADR-137); made at the first start, kept by `helm uninstall` | New in 0.12.0 |
| `<release>-casework` (from 0.12.0) | The cases of Cases › Cases, with their history, analyses and RCAs | New in 0.12.0 |
| The settings ConfigMap, key `helm-repositories.json`, and the Secret `<release>-helm-repositories` (from 0.12.0) | The Helm repositories and OCI registries charts are installed from, and their passwords and tokens | New in 0.12.0 |

A console reads every earlier format and migrates it in memory; nothing is rewritten until something is saved, so an upgrade alone changes nothing in the cluster. Fields a console does not know are kept when it saves, so a field a newer release added survives a rollback and a later upgrade.

## Going back

```sh
helm rollback k8s-console -n 221b    # to the previous revision
```

The earlier release reads what the newer one saved: it ignores fields it does not know. A console that finds a document saved in a **newer format than it knows** keeps reading it but refuses to save it, so it cannot destroy what the newer release wrote: the Settings page and the Users page say so until you upgrade again. CI checks a rollback from this release to each of the two before it.

The objects that are not the chart's (connections, accounts, Sherlock's key, sessions) survive `helm uninstall` too; [README.md](../README.md#1-deploy) says how to remove them.

## 0.12.0 (unreleased)

- **Helm repositories** (wave 5): a new key in the settings ConfigMap, `helm-repositories.json` (empty until a SuperAdmin adds one in Settings › Applications), and a Secret the console creates, `<release>-helm-repositories`, with their passwords and tokens (with `connections.store: secrets`, as `<release>-backup` is). A new chart value, `helmRepositories.egressCIDRs` (empty, with `egressPorts: [443]`): listed, the network policy lets the console reach the repositories there; empty, nothing changes. Applications from git are unchanged.
- **Jobs' instructions** (wave 5): each job runs with its kind's built-in instructions until someone changes them; a job's own are kept in its settings (the runs ConfigMap, `jobs.json`).

- **A new key in the settings ConfigMap**, `ssh-hosts.json`: the hosts outside Kubernetes a SuperAdmin adds (their names, addresses, ports, login users, labels and pinned host keys; never a credential). Nothing is written until the first host is added.
- **A new chart value**, `networkPolicy.hostPorts` (empty): SSH ports of hosts outside Kubernetes other than `toolbox.ssh.port`, allowed on the SSH egress rule. Hosts' addresses must be inside `networkPolicy.nodeCIDRs` when it lists any.
- **On a paired host**: what pairing puts on a node (`/etc/ssh/221b/`, the drop-in, the `Include`), and the login user when it was missing.
- **The AI agents need nothing to upgrade.** Sherlock's provider, key and model become the default provider and model, every agent starts on them, and Sherlock's caps are the overall limits. Two new ConfigMaps: `<release>-ai`, created empty, where the console keeps the providers added, the agents' settings, their instructions with their versions, test cases and the pilots' log (the Role gains it by name); and `<release>-agents`, the chart's own, with the baselines below. A provider added in the console keeps its credential in a Secret `<release>-ai-<id>`.
- **New chart values**, `agents.<agent>.instructions` (all empty) for `sherlock`, `troubleshooter`, `changePlanner`, `auditor`, `hostDoctor`, `automationAuthor`, `scriptReviewer`, `mycroft` and `summarizer`: each agent's baseline house instructions, at most 2,000 words, read at start (a change restarts the console). Long text is easiest from a file: `helm upgrade … --set-file agents.troubleshooter.instructions=troubleshooter.md`. An edit made in the console overrides the baseline and is flagged when an upgrade changes the baseline under it; nothing is replaced silently. A key that is no agent is refused.
- **Sherlock's instructions add to the built-in ones now.** Instructions saved in Sherlock's settings before replaced the built-in text; at the first start they are carried over as Sherlock's house instructions, which come after the fixed core, and the notes become the notes for every cluster. If you had pasted the whole built-in text and changed a part, shorten it to your changes in Sherlock's drawer.
- **Going back** to 0.11 finds Sherlock with its provider, key, model and caps; it ignores `<release>-ai` and the other providers' Secrets, which a later upgrade finds again.
- **Backups (ADR-80 to ADR-82).** The backup file keeps its format, `221b-backup/2`, and a restore still reads `221b-backup/1` and `/2`: its parts are now registered by the packages that keep them, and a file may hold parts an older console does not know, which that console names in the restore's report and leaves. Two new keys in the settings ConfigMap, written only once the scheduled backups to S3 are saved or run: `backup-s3.json` (where, when, how long, how encrypted; never a credential; at most 16 KiB) and `backup-s3-status.json` (the newest runs, at most 8 KiB). A new Secret, `<release>-backup` (with `connections.store=secrets`), holds the S3 access key and the passphrase for files encrypted on the console, once a SuperAdmin saves one; `helm uninstall` leaves it, like the other Secrets the console makes. Rolling back to 0.11 leaves the keys and the Secret unread, and no backup runs until the upgrade again.
- **Backups carry their secrets sealed (ADR-137, BK-15).** At its first start the console makes the Secret `<release>-backup-key` (with `connections.store=secrets`): 256 random bits that seal every secret a backup carries, connections' credentials, tokens, API keys, channel addresses and password hashes, with AES-256-GCM. A restore on the same install, or on one with the same key, brings them back; with another key the page asks first, restores the rest, and lists each secret to set again and where. Backups taken before, with placeholders, restore as they did. The console never shows the key: keep a copy where you keep secrets, since a lost key means the backups' secrets cannot be recovered (everything else still restores). See *The backup key* in [getting-started.md](getting-started.md#the-backup-key). The Role now names the Secret for `get`; `helm uninstall` leaves it, so a reinstall in the same namespace reads old backups. An older console restoring a new backup refuses the sealed values as secrets in clear and restores the rest.
- **A new chart value**, `backup.s3.egressCIDRs` (empty) with `backup.s3.egressPorts` (`[443]`): the S3 endpoint's addresses, which the network policy then lets the console reach. Empty adds no rule; with `sherlock.egress` on the console reaches any address on 443 already, which is enough for Amazon S3. Values from an older release (`--reuse-values`) without `backup` render as before.
- **A new ConfigMap**, `<release>-issues`, created empty by the chart, which the console's Role may now read and write: the issue service's reports, known issues, services, settings and API tokens' hashes. Nothing is written until the first report.
- **New chart values** under `issues`: `enabled` (on), `reportRoles` (all four roles), `operatorRoles` (`superAdmin`, `admin`) and `reporterGroups` (none). An upgrade with the defaults lets everyone who signs in report problems at `/report` and lets Admins triage every report; set `issues.enabled=false` to keep the service off, or `issues.operatorRoles=[superAdmin]`.
- **Local groups may have the role `reporter`**, with no namespaces. A rollback to 0.11.1 refuses to start with such a group in its values: remove it first.
- **A new route**, `/report`, served to everyone signed in; the console's own pages are unchanged. With `networkPolicy.enabled`, the issue webhook's address must be in `networkPolicy.outboundCIDRs` and its port in `outboundPorts`.
- **Administration › Approvals moved to Cases › Inbox** (ADR-88): rows of the Inbox's one table (kind Approval, IN-01), approved by the same people (a SuperAdmin) under the same rules. Bookmarks of `#/admin/approvals` open it there. The gear now always opens Administration; a yellow *i* beside it says what waits for you (IN-07), and it no longer flags decisions on your requests. The Inbox's old links (`?proposal=`, `?report=`, `#/cases/cases/<n>`) open the same drawers at their new addresses, `#/cases/inbox/<kind>/<id>`. Cases is a third area in the top bar, shown to anyone with something in it, Admins and Viewers who may report among them; nothing to configure.
- **Links into the report page** (Report a problem and My reports in the account menu, New report in Cases, Sherlock's Report this, the error screens) go to the issue settings' report page address, `/report` by default; a report page on another address than the console's gets its filled-in text in the fragment rather than through the browser's storage (ADR-89).
- **A new ConfigMap**, `<release>-cases`, created empty by the chart, which the console's Role may now read and write: the agents' proposals with their decisions, the standing approvals and the notification settings. A new Secret, `<release>-cases` (with `connections.store=secrets`), holds the notification channels' addresses once a SuperAdmin saves one. Nothing is written until the first proposal or setting.
- **A new chart value**, `cases.approverRoles` (`superAdmin`, `admin`): who may approve agents' proposals besides a SuperAdmin. Changes to hosts, fleets and Automation parameters need a SuperAdmin whatever it says. With `networkPolicy.enabled` and `sherlock.egress` off, list the notification webhooks' addresses under `networkPolicy.outboundCIDRs`.
- **Rolling back** to 0.11 leaves `<release>-cases` and its Secret unread; Watson's own cards work as before.
- **Jobs (ADR-90 to ADR-93).** A new ConfigMap, `<release>-runs`, created empty by the chart, which the console's Role may now read and write (`RUNS_CONFIGMAP`): the jobs' settings and their runs and findings. Nothing is written until a job is changed or runs. Every job is off after the upgrade; an agent runs on its own only once a SuperAdmin turns a job on in Cases › Jobs. The issue service's investigator is the *Report triage* job: until it is on, reports are not investigated, as before.
- **A new chart value**, `jobs.timeZone` (empty: UTC): the time zone a job's schedule starts in.
- **Agents on their own read as the user `221b:agents`** with the role Viewer by default, which sees no namespace until one is bound: bind the ClusterRole `221b-viewer` to that user in the namespaces agents may read (a RoleBinding with subject `kind: User, name: 221b:agents`), pick a group of the role Viewer in Settings › Jobs, or choose SuperViewer there. With `auth.oidc` and role lists, or local or LDAP groups, the console may impersonate that group already; nothing in the chart changes.
- **Going back** to 0.11 ignores `<release>-runs`, which a later upgrade finds again.
- **Findings, exceptions and RCAs need nothing to upgrade** (ADR-108 to ADR-110). Findings kept before have no priority and take their severity's; those acknowledged or dismissed stay so, shown as of before cases and exceptions, and are not turned into cases or exceptions. The accepted exceptions and RCAs are two new keys of the casework ConfigMap, `exceptions.json` and `rcas.json`, and a new backup part, *Exceptions and RCAs*; with a wiki store set they are also written as pages under `pages/exception/` and `pages/rca/`. The API's `POST /api/v1/findings/{id}/acknowledge` and `/dismiss` are gone: a script that used them calls `/ignore` with a reason, or makes a case. Cases › Runs moved to Cases › Jobs › Runs; old links still open it.
- **Mycroft's knowledge needs nothing to upgrade** (ADR-94 to ADR-98). A new ConfigMap, `<release>-knowledge`, created empty by the chart and added to the console's Role: the sources, the runs of wiki changes waiting for review, the syncs' status and the Confluence publisher's outbox and pages. Credentials go in a new Secret, `<release>-knowledge`, made by the console when the first is saved (with `connections.store=secrets`). Nothing is read or written until a SuperAdmin chooses where the wiki is kept and connects a source; until then `ask_knowledge` finds nothing.
- **A new volume**, an emptyDir `knowledge` at `/var/lib/221b/knowledge` (at most `knowledge.localCopy.sizeLimit`, 512 MiB): each replica's copy of the approved pages and of the source repositories, read again at start.
- **New chart values** under `knowledge`: `egressCIDRs` (empty) with `egressPorts` (`[443]`), the addresses of Confluence, the git hosts and the bucket, which the network policy then lets the console reach (empty adds no rule; with `sherlock.egress` on, port 443 is open already); `localCopy.sizeLimit`; `refresh.git` (`15m`) and `refresh.s3` (`5m`); `consoleURL` (empty: the ingress's host when `ingress.enabled`), for links back from Confluence pages. Values from an older release (`--reuse-values`) without `knowledge` render with the defaults.
- **New flags and variables**: `-knowledge-configmap` (`KNOWLEDGE_CONFIGMAP`), `-knowledge-dir` (`KNOWLEDGE_DIR`), `-knowledge-refresh-git`, `-knowledge-refresh-s3` and `-console-url` (`CONSOLE_URL`).
- **A temporary wiki store** (CF-57): a new ConfigMap, `<release>-wiki`, created empty by the chart and added to the console's Role (`WIKI_CONFIGMAP`, flag `-wiki-configmap`; chart value `knowledge.configMapStore.enabled`, on by default). While Settings › Knowledge names no store, the wiki is kept there, at most 900 KiB; it is for testing and will be removed, so move the wiki to Git or S3 before relying on it. With the value off, no store until one is chosen, as before.
- **A known issue may come from Confluence**: the issue API's `source` takes `confluence` too, for a page written by hand under the published tree and imported as a draft.
- **Getting started needs nothing to upgrade** (ADR-103). A new key in the settings ConfigMap, `setup.json` (at most 4 KiB): the steps a SuperAdmin dismissed and when a backup was first downloaded; every step is otherwise worked out from what the console has, so an upgraded console shows the steps it has done as done. A new backup part, *Getting started* (order 250). Going back to 0.11 ignores the key.
- **Sherlock answers SuperAdmins with one more tool while Getting started has a step open** (`setup_status`, read only), and one more part of its fixed instructions; both go once every step is done or dismissed (ADR-104).
- **Mycroft reads a new source at once** (GS-11), in the background, when the wiki has a store; before, the next sync did. Its inventory proposals may now carry hosts' labels and `fleet.create` actions.
- **Every agent has `ask_knowledge`** now, the Auditor, Change planner, Automation author, Script reviewer and Summarizer included. An agent whose tools a SuperAdmin narrowed keeps the list as saved; tick *Ask knowledge* in its drawer to add it.
- **Cases (work items) need nothing to upgrade** (ADR-105 to ADR-107). A new ConfigMap, `<release>-casework`, created empty by the chart, which the console's Role may now read and write (`CASEWORK_CONFIGMAP`, flag `-casework-configmap`): the cases, at most 512 KiB. Nothing is written until the first case. The people in `cases.approverRoles` open and complete cases; no new chart value. A new job, *Case analysis*, is off like every job until a SuperAdmin turns it on in Cases › Jobs; until then cases are not analysed. A new backup part, *Cases* (order 260). Going back to 0.11 ignores the ConfigMap, which a later upgrade finds again.
- **New report in Cases is New Case**: bookmarks of the report page (`/report`) and the account menu's *Report a problem* are unchanged.
- **AI settings, round 2 need nothing to upgrade** (ADR-120 to ADR-122). New fields in `<release>-ai`, all optional: a provider's context window and last test, the edits of an agent's built-in text in its live record, drafts and versions, an agent's own context window and Sherlock's compact choice. Until a context window is set (or listed by the endpoint and saved), Sherlock keeps its whole form; the listed Claude models count as 200,000 tokens. A provider that takes no key is usable once its card's *Test* passes. Going back to 0.11 ignores the new fields; an edit of a built-in text is then not used, and a later upgrade finds it again.
- **Cases › Knowledge and Known issues are Cases › Mycroft** (ADR-118): bookmarks of `#/cases/knowledge[/…]`, `#/cases/knowledge/exceptions/<id>`, `#/cases/knowledge/rcas/<id>` and `#/cases/known[/<id>]` open their new places. Knowledge's settings gain a built-in source, *Added in Mycroft* (uploaded documents), listed in Settings › Knowledge from the first start; it may be switched off, which stops Add. A web address added is read from the console's pod: with `networkPolicy.enabled`, put the site's addresses in `knowledge.egressCIDRs` (and `80` in `knowledge.egressPorts` for an http site). No chart value changes.
- **Accepted exceptions are no longer written to the wiki** (MY-09): pages `pages/exception/ex-<n>.md` written before stay in the wiki's store, and are marked removed when their exception is; delete them from the repository or bucket if you want them gone.
- **One flow for findings and cases (wave 6, ADR-126 to ADR-130): nothing is carried over.** Builds of 0.12.0 before wave 6 were never released; their Cases records (reports, wave-4 cases, findings with their own states, priorities P1 to P5) are not converted. A console that ran one of them is cleaned once by setting `cases.cleanStart.token` to any new string (`cases.cleanStart.dryRun: true` first logs what would go): the cases, known issues, exceptions, RCAs, runs and findings, agents' proposed changes for cases and runs, and Mycroft's pages, drafts and excerpts are deleted; settings, jobs, Watson's cards and approvals and Mycroft's sources stay, and the SLA's targets are renumbered P0 to P4 with their values. An upgrade from 0.11 has nothing to clean: leave the value empty.
- **Each case is a ConfigMap of its own**, `<release>-case-<n>`, labelled `221b.io/case`; the console's Role may now create, list and delete ConfigMaps in its namespace (names made at run time cannot be listed in `resourceNames`). Closed agent-found cases are deleted after 90 days (Settings › Cases).
- **Priorities are P0 to P4** (P0 outage, P1 blocking, P2 service degradation, P3 preemptive, P4 question), in the issue API too.
- **`<release>-issues` is made whatever `issues.enabled` says**, and the console's Role and `ISSUES_CONFIGMAP` always name it: `issues.enabled: false` now turns off the report page and the issue API only, and the cases keep their records there. The people in `issues.operatorRoles` work the cases besides `cases.approverRoles`.

## 0.11.1 (2026-11-04)

0.11.0 was tagged but never published; upgrade from 0.10.x to 0.11.1.

Things to check before this upgrade:

- **Sign-in off is Viewer now.** With `auth.mode: none` visitors were SuperAdmins; now they are Viewers (`auth.anonymousRole: admin` gives Admin, never more), and SSH, Automation and showing Secret values are off. Turn sign-in on to keep them.
- **Shared addresses lose their wider sign-in budget.** Every address gets 30 sign-in attempts a minute; `X-Forwarded-For` counts only from `auth.trustedProxies`.
- **A second SuperAdmin approves** taking a cluster off production and making a local user a SuperAdmin, once there is one; a single SuperAdmin's request applies at once, logged as such.
- **A new ConfigMap**, `<release>-automation-runs`, keeps Automation's runs and nodes' facts; the chart creates it and the Role may write it, and they move from `<release>-automation` on the first save.
- **Two more ConfigMaps**, `<release>-audit` and `<release>-sessions`, keep the audit log and the toolbox's session records; they move out of the settings ConfigMap on their first save. The chart names all four to the console (`AUDIT_CONFIGMAP`, `SESSIONS_CONFIGMAP`, `AUTOMATION_RUNS_CONFIGMAP`), and a console that cannot read one it is given does not start. Run without the chart, leave the flags out to keep everything where it was.
- **`auth.trustedProxies`** is new, and empty by default. Behind an Ingress, list your proxies' addresses as CIDRs, every proxy on the way: their `X-Forwarded-For` then gives the audit log's From and the limits on sign-in attempts the client's address. Without it the console believes nobody's header: From is the proxy's address, as before, and requests that came through a proxy count only against the limit per account.
- **Admin and Viewer groups in the console's namespaces are refused.** A group bound in the console's own namespace or the toolbox's (`<release>-privileged` unless `toolbox.nodeShell.namespace` says otherwise) makes the upgrade fail, with the group's name. Take the namespace off the group; people who manage the console hold superAdmin.
- **A token without the username claim is refused.** The console used to fall back to another claim; now the person sees an error. If your provider leaves `auth.oidc.usernameClaim` (`email` by default) out of some tokens, ask for it with `auth.oidc.scopes`, or set `usernameClaim` to one every token has. A nested claim (`corp.login`) now works as the values say.
- **A Helm dry run renders on the console**, also with `--dry-run=server`, so a card's dry run no longer checks for objects that already exist or reads the cluster through `lookup`. The objects and the release the card lists come from the cluster as before.
- **A card whose objects cannot be read cannot be approved.** A read that times out or is refused says so on Approve, and the card stays until they can be read or it lapses; a queued change in that state does not start.
- **A link with `?ask=` fills Sherlock's box.** A home card's Audit and Troubleshoot buttons open the cluster with the question ready, and the person presses Ask.

`localCluster.writeClusterRole` now defaults to `minimal`, a role the chart makes, in place of the built-in `edit`: with sign-in off and `localCluster.mode: write`, the console no longer reads every Secret or impersonates ServiceAccounts unless `secrets.read` or your own role says so. Helm's records need Secrets read, so Helm applications there need `secrets.read`. To keep the old behaviour, set `localCluster.writeClusterRole: edit`.

Two consoles in one cluster: `rbac.shared` says who installs the roles they share. `auto`, the default, is as before, and the install notes now say when another release holds them. To keep them from going with either console, install them on their own (`helm install 221b-roles <chart> --set rbac.shared=only`) and set `rbac.shared=none` on each console.

New chart values, all with defaults: `toolbox.ssh.enabled` (false), `toolbox.ssh.port` (22), `toolbox.ssh.loginUser` (root). SSH is off until you turn it on. What an operator may notice with it on:

- **A new Secret**, `<release>-ssh-ca`, made on the first pairing: the SSH certificate authority's key. The console's ServiceAccount already manages Secrets in its namespace when `connections.store` is `secrets`. Back it up with the namespace; a console that loses it must pair every node again.
- **A new key in the settings ConfigMap**, `ssh-pairings.json`.
- **A new egress rule in the NetworkPolicy**, only with SSH on: TCP to `toolbox.ssh.port` (22) anywhere, so the console reaches sshd on the nodes. Set `networkPolicy.nodeCIDRs` to the nodes' addresses to allow nothing else; `toolbox.ssh.enabled=false` removes the rule.
- **On a paired node**: `/etc/ssh/221b/`, `/etc/ssh/sshd_config.d/221b.conf`, and an `Include /etc/ssh/sshd_config.d/*.conf` line at the top of `sshd_config` when it had none (a failed pairing takes it out again). Several consoles may pair one node: each keeps its own line in `ca.pub` and the principals files, and *Unpair from SSH…* removes that console's lines, and the first two only with the last console's key.

Helm applications are held closer:

- **Rights per namespace.** Changing an application, its commands or `--app` now takes Admin or SuperAdmin and, in that namespace, Kubernetes' leave to list and create Secrets. An Admin whose bindings do not reach the application's namespace gets 403 where the console used to let them through.
- **A deploy runs what its card showed.** The run fetches the card's commit by its id if the branch has moved. A git server that does not serve commits by id (GitHub does; plain git needs `uploadpack.allowReachableSHA1InWant`) makes such a run refuse: send the command again.
- **Limits on a fetch and a render.** A repository past 64 MB sent, 128 MB unpacked or checked out, or 20,000 files is refused, as is a chart that renders to more than 32 MB or for more than a minute. Move a chart out of a very large repository if you meet one.
- **Chart paths** with spaces or other characters outside letters, digits and `. _ + @ -` are refused; such a path never deployed. Rename the directory.
- **Repository hosts** on loopback, link-local or cloud metadata addresses are refused. Private addresses work as before. With `HTTPS_PROXY` set, the address check at connection time sees the proxy's address, not the repository's.
- **Changing an application's repository removes its saved token** unless you give a new one.

### Also in 0.11.1: the 0.10.2 work, never released on its own

Nothing to do. The new value `replicas.warmStandby` defaults to `true`, which keeps every replica watching as before; only the leader now polls and serves the data (ADR-70), so a rolling upgrade behaves as it did. On a very large cluster, where a standby's memory matters, set it to `false`: the other replicas then hold nothing, and a failover costs one list.

What an operator may notice:

- **A new key in the settings ConfigMap**, `audit-log.json`: the newest 2,000 audit events, at most about 600 KiB, for Administration › Audit log. A rollback to 0.10.1 leaves it there, unread.
- **Application records gain `commands`**. A 0.10.1 console that saves an application's record drops them; finish a rollback before changing applications there.
- **A backup is a file you keep safe.** It holds no credential, but it names people, clusters, releases and what was asked of Sherlock; treat it like the audit log.

## 0.10.1 (2026-10-30)

No chart value changes. What an operator may notice:

- **New settings fields** in the settings ConfigMap: `fleetsPage`, `watson.approveAllMinutes`, `sherlock.notes`, `sherlock.instructions`, and `deploy` on a fleet. 0.10.0 reads the ConfigMap with them present and ignores them; a settings save from 0.10.0 drops them, so finish a rollback before saving settings there.
- **Application records** (the `<fullname>-app-<id>` Secrets) gain `valuesFile`, `ignoreDefaults`, `fleet`, `fleetLabel`, `matchedRevision` and `sourceDiff`. 0.10.0 ignores them and deploys without the second values file or the fleet's nodeSelector.
- **Standing approvals** are off until an administrator sets a limit in *Settings › Watson*; the audit log gains the `watson.approveAll` action.
- The page carries a per-load nonce in its Content-Security-Policy `style-src`; nothing to configure.

## 0.10.0 (2026-10-28)

- **`toolbox.nodeShell.namespace`** is empty by default and renders as `<release namespace>-privileged` (`221b-privileged` for a console in `221b`): the upgrade makes the new namespace and removes `221b-toolbox` once no shell runs there; set the value to `221b-toolbox` to keep the old name. A second console installed in another namespace now gets a namespace of its own instead of refusing to install.
- **Phase 10:** the Helm tab rebuilt around applications from git ( `apps/…` endpoints, records in Secrets `<fullname>-app-<id>` of the console's namespace) and Secret editing with the new `secrets.reveal` value (off by default).

## 0.9.1

The first release after 0.7.1: Phase 8 (Storage), Phase 9 (Toolbox) and the Phase 9.1 review. There is no 0.8.x or 0.9.0; upgrade from 0.7.1 directly.

- **The console reads volumes.** Its ClusterRole gains get, list and watch on PersistentVolumes, StorageClasses and VolumeAttachments, for the Storage tab. `hack/check-rbac.sh` checks them.
- **The roles for people grow.** With the full Admin template, Admin may delete PersistentVolumeClaims, forward ports and create Jobs; the Permissions page flags a create on pods or Jobs as `runs pods`, since a Job may run as any ServiceAccount of its namespace. `rbac.roles.admin.exec: true` (off by default) also lets Admin open shells in pods.
- **The Toolbox is on** (`toolbox.enabled: true`). Each tool runs as the person who asks, so their own RBAC decides who may use it; the `toolbox` block is the ceiling. Set `toolbox.enabled=false` to leave it out.
- **A namespace for node shells.** With the full SuperAdmin template, the chart makes the namespace `221b-toolbox` (`toolbox.nodeShell.namespace`), labelled Pod Security `privileged`, with a network policy that lets its pods reach only the API servers. A second console in the same cluster needs a namespace of its own there; `toolbox.nodeShell.enabled=false` leaves it out. The shared roles (`221b-viewer` and the others) are still left to the console that installed them.
- **Port forwards** reach your computer through the `221b` command, attached to the release; the console refuses one older than `toolbox.portForward.cliMinVersion` (0.8.0).
- **`antiAffinity`** (`soft` or `hard`, off by default) keeps several replicas on different nodes.
- **Formats:** the settings gain optional fields (fleets that are lists of nodes, the Pending alert grace, the alert banners switch) and the settings ConfigMap a `toolbox-sessions.json` key; the format version is unchanged. 0.7.1 ignores what it does not know, but it has no lists of nodes: after a rollback, such a fleet is not what it was until you upgrade again.

## 0.7.1

- The same as 0.7.0, which was released without an image: upgrade from 0.6.x to 0.7.1, and everything under 0.7.0 below applies.

## 0.7.0

- **Sessions survive restarts and upgrades.** They are kept in Secrets of the console's namespace instead of the pod's memory, so an upgrade no longer signs everyone out, and any replica serves a signed-in browser.
- **Several replicas** (`replicaCount: 2` or more). One replica, chosen through a Kubernetes Lease, runs Sherlock and Watson, and the others pass those requests to it. A PodDisruptionBudget keeps one replica up during node drains.
- **New permissions** in the console's namespace: `leases` (get, create, update) for choosing the replica that runs Sherlock and Watson, `watch` on Secrets for the sessions and connections other replicas save, and `update` on the settings ConfigMap for safe concurrent saves. `hack/check-rbac.sh` checks them.
- **The network policy** lets the console's replicas reach each other on the HTTP port, for the requests passed to the replica that runs Sherlock and Watson. With `networkPolicy.enabled=false` nothing changes.
- **Formats:** the settings and accounts gain a `"version"` field the first time they are saved. 0.6.5 and 0.6.6 ignore it, and CI checks that a rollback to either reads them.

## 0.6.6

- The read-only SuperAdmin role no longer grants `nodes/proxy`, `pods/exec`, `pods/attach`, `pods/portforward` or the proxy subresources. If you relied on it for any of them, bind another role.
- With `ingress.enabled` and a ClusterIP Service, the chart now refuses to render without `networkPolicy.ingressFrom`, since the policy would block the Ingress controller.
- A change to the chart's auth Secret now rolls the pod.
