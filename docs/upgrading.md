# Upgrading 221B

An upgrade is a `helm upgrade`. Everything the console keeps in the cluster (its settings, the local accounts, the connections to other clusters, Sherlock's key and the signed-in sessions) lives in objects the chart does not overwrite, so it comes through. This page says how to upgrade, which upgrades are tested, what is migrated, and how to go back.

## How to upgrade

From the published chart:

```sh
helm upgrade k8s-console oci://ghcr.io/ucesys/charts/221b --version 0.7.0 -n 221b --reset-then-reuse-values
```

From a checkout of the repository, at the release's tag:

```sh
git checkout v0.7.0
helm upgrade k8s-console deploy/helm/221b -n 221b --reset-then-reuse-values
```

`--reset-then-reuse-values` keeps the values you installed with and takes the new chart's defaults for anything you never set. Add `-f my-values.yaml` or `--set` for what you want to change. Read the release's section below, and its notes in [CHANGELOG.md](../CHANGELOG.md), before upgrading.

The image follows the chart: a release's chart runs `ghcr.io/ucesys/221b:<its version>` unless `image.tag` says otherwise. If you pinned `image.tag` or `image.digest`, change it to the new release's.

## Which upgrades are tested

CI upgrades from each of the **two releases before** the one being built (P7-03), in kind: it installs the earlier release's chart and image, makes a local user on the Users page, saves settings, a fleet and an acknowledged alert, adds a cluster connection and Sherlock's key, then upgrades, checks that everything is there and that the user still signs in, rolls back, checks again, and upgrades once more (`hack/upgrade-kind.sh`). Upgrading from an older release works through the releases in between: from 0.6.5 to 0.8.0, go to 0.7.x first.

## What is migrated

| Kept in | What | Format |
|---|---|---|
| The settings ConfigMap (`<release>`, key `settings.json`) | Refresh rate, date and time format, fleets, acknowledged alerts, Sherlock's and Watson's settings | Carries `"version"`; migrated when the console starts and saved in the new format on the next change |
| The accounts Secret (`<release>-users`, key `users.json`) | Local users made on the Users page, their groups and password hashes | Carries `"version"`, as the settings do |
| A Secret per connection (`<release>-cluster-<id>`, key `spec.json`) | The connection and its credential | Carries `"version"` since 0.6 |
| `<release>-sherlock` | Sherlock's API key | Unchanged |
| Session Secrets (from 0.7.0) | Who is signed in | New in 0.7.0 |

A console reads every earlier format and migrates it in memory; nothing is rewritten until something is saved, so an upgrade alone changes nothing in the cluster. Fields a console does not know are kept when it saves, so a field a newer release added survives a rollback and a later upgrade.

## Going back

```sh
helm rollback k8s-console -n 221b    # to the previous revision
```

The earlier release reads what the newer one saved: it ignores fields it does not know. A console that finds a document saved in a **newer format than it knows** keeps reading it but refuses to save it, so it cannot destroy what the newer release wrote: the Settings page and the Users page say so until you upgrade again. CI checks a rollback from this release to each of the two before it.

The objects that are not the chart's (connections, accounts, Sherlock's key, sessions) survive `helm uninstall` too; [README.md](../README.md#1-deploy) says how to remove them.

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
