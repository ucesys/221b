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
| `<release>-sherlock` | Sherlock's API key | Unchanged |
| Session Secrets (from 0.7.0) | Who is signed in | New in 0.7.0 |
| The settings ConfigMap, key `toolbox-sessions.json` (from 0.9.1) | The Toolbox's open and ended sessions, for Administration › Sessions | New in 0.9.1 |

A console reads every earlier format and migrates it in memory; nothing is rewritten until something is saved, so an upgrade alone changes nothing in the cluster. Fields a console does not know are kept when it saves, so a field a newer release added survives a rollback and a later upgrade.

## Going back

```sh
helm rollback k8s-console -n 221b    # to the previous revision
```

The earlier release reads what the newer one saved: it ignores fields it does not know. A console that finds a document saved in a **newer format than it knows** keeps reading it but refuses to save it, so it cannot destroy what the newer release wrote: the Settings page and the Users page say so until you upgrade again. CI checks a rollback from this release to each of the two before it.

The objects that are not the chart's (connections, accounts, Sherlock's key, sessions) survive `helm uninstall` too; [README.md](../README.md#1-deploy) says how to remove them.

## 0.11.0 (unreleased)

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

## 0.10.2 (unreleased)

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
