# Changelog

What changed in each release of 221B, for people who use it and people who install it. Versions follow [semantic versioning](https://semver.org/): while the major version is 0, a minor version (0.7.0) may change behaviour and chart values, and a patch version (0.7.1) only fixes. [docs/upgrading.md](docs/upgrading.md) says how to upgrade and what each upgrade changes.

## 0.10.0 (unreleased)

### Changed

- **Every change goes through Watson and an approval again.** The Tools menus' cordon, uncordon, drain and delete, every tool session (shell, port forward, debug container, debug pod, node shell) and every Helm change from the Helm tab are cards the person reads and approves in Watson, as a typed command is; a second person where the two-person rule applies. This reverses the October 26 choice to run them at once.
- A namespace's wrench on the Namespaces tab offers **Delete namespace…**, through Watson, with a card that says what goes with it.

### Added

- **Helm: applications from a git repository.** The Helm tab is new: it lists every release in one table, with its chart, app version, revision, status, drift, source and update time. *New application…* fetches a chart from a git repository (GitHub, GitLab, Gitea) at a branch or a tag and installs it; a release's tools upgrade it, switch its branch or tag, change its values file and parameters, set or forget its source, and uninstall it. Each change runs through Watson as you, with a card where the two-person rule applies. A release's panel shows its source and commit, its drift (each object compared with what Helm applied), its values file beside the values as deployed, its history with a diff of any two revisions or of the deployed one against the source and a notice when a newer tag or commit exists, and the images every revision runs. It shows everything the former Helm tab did (pods, the objects by kind, values, notes, history) and replaces it; the addresses are unchanged. Each revision in the history has a diff icon that opens the diff in a large dialog.
- **Secret editing.** On the Namespaces tab an Admin or SuperAdmin can edit a Secret (replace a value, add or remove a key) and make a new one, written as them, so Kubernetes decides. By default they do it blind. Chart: `secrets.reveal` (off by default) lets them see a value, one key at a time, read live as them and recorded in the audit log; the cache, exports, logs, the YAML view and Sherlock still never hold a value.
- **New namespace…** on the Namespaces tab makes a namespace, written as you.
- Chart: `toolbox.nodeShell.namespace` is empty by default and becomes the release's namespace with `-privileged` (`221b-privileged` for a console in `221b`), named for what it is, so a second console installed in another namespace no longer tries to take over the first one's. An upgrade moves node shells from `221b-toolbox` to the new namespace; set the value to keep the old name.
- Watson runs `helm install`, `helm upgrade` and `helm uninstall` with a git source (`--git`, `--path`, `--ref`, `--tag`) and an application's saved values (`--app`).


## 0.9.1 (2026-10-27)

The first release since 0.7.1, with Phase 8 (Storage), Phase 9 (Toolbox) and the Phase 9.1 review; there is no 0.8.x or 0.9.0. [docs/upgrading.md](https://github.com/ucesys/221b/blob/v0.9.1/docs/upgrading.md#091) says what an upgrade from 0.7.1 changes.

### Added

- **Toolbox.** A Tools button beside each pod and node opens a shell, forwards a port to your computer, adds a debug container, starts a debug pod, or opens a shell on a node, and cordons, drains or deletes. Each tool asks through a dialog, and its button opens the session as a tab in the dock, at once: it runs as the person who asked, and ends after 10 minutes without activity, at its limit (Extend adds 10 minutes, up to the console's maximum), or when its pod is gone. Cordon runs at once; Drain and Delete ask first; Watson shows them running. On production where the two-person rule is on, another SuperAdmin approves through Watson, since the session opens as the person who asked, with their rights; node shells follow the same rule (`toolbox.nodeShell.approval: always` brings back a second SuperAdmin everywhere). Pods on a node that stopped reporting show Unknown, in a status segment of their own; the Pods segments show only when they have pods. A pod raises a Pending alert only after a grace period, 2 minutes by default, set on the Settings page. Port forwards reach your computer through the new `221b` command, attached to each release; it asks for the forward's token, so the token stays out of your shell's history. Administration gains Sessions (open and ended) and Toolbox (what the chart allows). Sherlock can open a tool's dialog filled in. A debug pod carries no ServiceAccount token unless a SuperAdmin names the account. Chart: a `toolbox` block, `rbac.roles.admin.exec`, and the `221b-toolbox` namespace for node shells; the full Admin role gains port forwards and Jobs, which the Permissions page flags `runs pods`, since a Job may run as any ServiceAccount of its namespace.
- **Storage tab.** Storage classes, persistent volumes and claims, and the pods that mount them, in one table beside a filter sidebar by namespace, node and storage class, as on the Pods tab, with how full each volume is (from the kubelets), a Needs attention filter, and a drawer that traces a claim from its class to its node and says where it breaks. A class has a drawer of its own. Expand a claim, delete a claim, and set or remove the default class through Watson, each after an approval. People bound to namespaces see their claims only. Alerts, the pod drawer and the banner link to it. Full Admin gains `delete` on claims, and the chart's ClusterRole reads VolumeAttachments.
- **Fleets by name.** A fleet can now be a list of nodes kept in 221B, as well as a set of labels and taints. A list changes nothing in the cluster, a node may be on any number of lists, and a new node joins one only when someone adds it; the editor offers both kinds.
- **Fleets: pick nodes and put them in a fleet.** Administrators tick nodes in the matrix, then make a new fleet of them (a list, by default), add them to a fleet or remove them from one, choosing the fleet in the dialog; Cancel drops the picks. For a fleet of labels and taints the dialog lists the `kubectl label` and `kubectl taint` commands, and what else they change (fleets the nodes leave, pods a taint keeps off or evicts), and sends them to Watson, where each waits for approval.
- Watson runs `kubectl taint` on nodes. Its card says what changes and, for NoExecute, which pods are evicted.
- **Alerts by node.** Each pod's alert sits under the node it runs on, below the node's own alert when it has one, and a node whose pods fail comes near the top even when the node itself is fine. Pods that no node took and volumes keep their own sections; By kind shows the alerts as before.

### Changed

- **Tabs open as they were left.** A tab in the bar opens with the filters, selection and sort it had, per cluster, for the rest of the session; a details pane over it is not kept.

- **Sherlock knows the cluster it is asked about.** Its prompt now names the cluster's Kubernetes distribution (RKE2, K3s, kubeadm, kind, EKS, GKE, AKS, OpenShift, Talos), the operating system and kernel of each node, and its network plugin, load balancer, storage and GPU plugin, and gives the guidance for those alone: the distribution's own services, log files and commands, and each operating system's. It troubleshoots down to the node's operating system and hardware, and gives the checks to run on a node in a block of their own. A console with several clusters gives each its own prompt. Node details are included only for people who may list nodes.
- **Namespaces: a tab per kind.** A namespace's kinds are tabs with their counts, as on the Helm tab, Pods first and Helm releases last, in place of a table per kind one under another and the kinds funnel.
- **Alert banners can be hidden.** Settings › General › Alert banners: Hide takes the alert banners off the top of every tab, for everyone; the Alerts tab's name still turns red or amber.
- **Home: figures only, and Storage.** A cluster card's tiles show only their figure: nodes ready, pods ready of those not finished (94/104; completed pods are left out), alerts, and a new Storage tile, claims bound, in place of Namespaces. The health lines no longer say that a node not ready also has errors, and call the pods on a node that stopped reporting Unknown rather than failing to start.
- **Home: Troubleshoot or Audit.** A cluster card's Sherlock button reads Troubleshoot while the cluster has errors or warnings, and asks Sherlock why and how to fix each, most severe first; while it is healthy it reads Audit and asks for the review of its setup, as before.
- Sherlock's prompt is cached for longer: the time comes last, so asking again a minute later costs less.
- Chart: `antiAffinity` (`soft` or `hard`, off by default) keeps several replicas on different nodes: soft prefers it more strongly than the spread the chart already asks for, hard requires it.

- **Nodes and Fleets: All, With problems, With errors.** The Nodes with errors checkbox is now a switch of three: every node, the nodes with an error or a warning (cordoned, under pressure, pods not ready), or only those with an error. Links with err=1 still show the nodes with errors.

### Fixed

- **The Tools menu and its confirmations had no background.** They are drawn outside the table, and took the theme's colours with them only on the page itself.
- **A bound volume's drawer said its claim was none.** It now shows the claim's chain, with the claim as a link back to its drawer; the Volume link in the Storage table opens the volume's drawer and the Claim link the claim's, where both used to open the claim's.
- The filter sidebars of the Pods and Storage tabs no longer show an "only" button on hover; the checkboxes and Select all do the same.
- **The home page counted a cordoned node as not ready.**
- **An acknowledgement took a second or two to show.** It is saved with the settings, and the replica serving the page, when it is not the one that saved it, reads them every 3 seconds. The Alerts tab, its badge and its colour now show it at once, and the server's rows take over when they come.
- **The home page's pod counts did not add up**, and differed from the Pods tab's: "109 pods · 106 running · 10 not ready" counted the not-ready pods among the running ones. The tile now reads "96 running · 10 not ready · 3 completed", and the Pods and Helm tabs have a Not ready status beside Running: pods running but failing a readiness check, or on a node that stopped reporting. The Nodes, Pods and Alerts boxes stay plain when nothing is wrong.
- **Copy buttons copied nothing** when the console is reached over plain HTTP, as at http://<address>: browsers offer their clipboard API only to HTTPS and localhost. Copying now falls back to the older way, which works there too.
- The refresh ring kept the old pace after the refresh rate changed, until the next refresh. It now starts again at the new rate at once, as the server does.
- **"Show me the nodes with problems" showed every node.** Sherlock could not set the Nodes and Fleets tabs' filter or the Alerts tab's Hide acknowledged, so it guessed at taints and search words and then cleared the filters; and with only an errors filter, a node with warnings alone left it no filter that fit. It now shows the nodes with problems, or with errors, as asked, leaves a filtered view empty when nothing matches, and cannot take off a filter it set while the filter shows something.

## 0.7.1 (2026-10-20)

### Fixed

- **0.7.0 has no image.** Its release ran, but the image was held back by the Firefox browser tests, which stall in CI. 0.7.1 is 0.7.0 with an image: install or upgrade to 0.7.1.
- CI: Firefox's browser tests still run and show their failures, but no longer hold back a release; Chromium and WebKit must pass. The upgrade test skips a release that has no image.

## 0.7.0 (2026-10-20)

### Added

- **Several replicas** (P7-02): `replicaCount: 2` or more, with a PodDisruptionBudget. One replica, chosen through a Kubernetes Lease, runs Sherlock and Watson, and the others pass those requests to it; sign-ins, settings and accounts are shared, so losing a replica costs a viewer a reconnect.
- **Upgrades from the two previous releases** (P7-03) are tested in CI: upgrade, roll back and upgrade again, keeping settings, users, connections and Sherlock's key. Stored settings and local accounts carry a format version; a console migrates older formats, keeps fields it does not know when it saves, and refuses to overwrite a format newer than it knows.
- **Releases** (P7-08): semantic versions, this changelog, the chart published to `oci://ghcr.io/ucesys/charts/221b`, and a GitHub release for every version tag ([docs/releasing.md](docs/releasing.md)). A contributing guide, a code of conduct, a security policy, issue and pull request templates, and a Developer Certificate of Origin check for pull requests from forks.

### Changed

- Everyone signs in once more after upgrading to 0.7.0: sign-ins used to live in the console's memory and are now kept in the cluster, where they survive restarts and upgrades.

### Fixed

- Sherlock on the home page and on a connection's page: *Send to Watson* shows under a command, and a cited object (*Open deployment … →*) opens on the cluster's tabs.

## 0.6.6 (2026-10-18)

### Security

- The YAML view refuses names with `/`, `..` or `%`, which could reach any API path, and hides anything that comes back as a Secret.
- A kubeconfig that names a token, certificate or key file is refused, so the console never sends its own token to another server.
- Watson refuses to print Secrets in any type spelling (`secrets.`, `secret.v1.`), shows prefixed label removals on its card, acts only on the cluster a card was made for, and checks a selector's matched set and a queued change's objects again before it runs.
- The read-only SuperAdmin no longer gets `nodes/proxy`, `pods/exec`, `pods/attach`, `pods/portforward` or the proxy subresources; the README's read-only recipe sets `kubeletStats: false`.
- Sign-in no longer redirects off the site through `\`; OIDC refuses reserved user names and ignores an unverified email; event streams end with the session or a change of the person's groups.

### Changed

- One date and time format everywhere, in the browser's time zone with its short name (`2026-05-03 1:34pm PDT`); Settings sets the date style and the clock for everyone.
- Copy icons beside names in the tables and side lists. The top bar no longer shows the refresh time; acknowledged alerts say who and when; the home card's review button is *Audit*.
- A pod that runs but is not ready stays an alert (*Not ready*).
- Permissions and Roles open for every signed-in person.

### Fixed

- One unreadable connection Secret no longer hides the others; a kubeconfig without `current-context` starts.
- The assistants' event stream reconnects after a proxy error; a modal drawer keeps and returns focus; CSV cells that would run as formulas stay text.
- The image builds again after the README screenshots were added.

## 0.6.5 (2026-10-18)

### Changed

- Alerts: an alert on an object that is healthy again is *Resolved* and counts nowhere; acknowledged alerts follow the unacknowledged ones, then the resolved; each alert has a border in its colour.
- Password changes from the sign-in page, and by a SuperAdmin on the Users page after entering their own password.
- Nodes: *master* for control-plane nodes, a "Nodes with errors" filter on Nodes and Fleets; icon-only Export and Columns menus.

## Earlier

Phases 1 to 6.4 were developed before releases were versioned; [docs/requirements.md](docs/requirements.md) describes what each phase added.
