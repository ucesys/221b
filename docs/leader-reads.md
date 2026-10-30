# Leader reads: one replica reads the cluster

A handoff for implementing, in the Claude Code CLI, the change agreed on October 30, 2026. Branch from `phase-10.1-maint`. Everything below was checked against that branch's code; file and line references are to it.

## Why

1. **Flicker.** With `replicaCount: 2`, the Helm release page shows CPU and memory "in use" and then "—", at random, every few seconds. The browser's event stream stays on one replica (A), which reads pod usage from metrics-server because that stream subscribes to `helm`. The page refetches `GET /api/v1/helm/{ns}/{name}` on every refresh tick (`useDetails`, `web/src/api/details.ts`), through a Service with no affinity, so half the fetches reach B. B has no subscriber, so it does not read pod usage, and `Cluster.Poll` (`server/internal/kube/cluster.go:429-445`) empties its list every poll. `release()` (`server/internal/api/details.go:100-107`) then sends no `usage`, and `ReleaseBody` shows "—". The pod drawer (`details.go:49`) and the object drawers (`details.go:146`) have the same problem.
2. **Load.** Every replica reads every cluster connection independently. Each one runs 32 cluster-wide informers, a full LIST of each at start, metrics-server reads every refresh, kubelet stats through the API server proxy (one request per node, `kube/volumes.go`, `kube/boot.go`), a health check, and a role-drift check (`api/roles.go:166`). The second replica doubles all of it. The informers' steady-state watches are cheap (the API server serves them from its watch cache, and etcd sees one watch per kind however many clients there are). The LISTs and the polling are what cost.

## What was decided

- **The leader does all the reading and serves all the data.** The other replicas sign people in and forward every other API request to the leader, as they already do for Sherlock, Watson, the Toolbox and Apps (ADR-60, `leaderOnly` in `server/internal/api/proxy.go:46`).
- **A chart value, `replicas.warmStandby`, default `true`.**
  - `true`: the other replicas keep their **watches** open, so failover is instant. They do **no polling** (metrics-server, kubelet stats, health checks, role checks) and serve no data.
  - `false`: the other replicas run no informers at all. A replica that wins the Lease starts them, and serves after its first LIST.
- `true` is the default because a rolling upgrade moves the leader every time. ADR-60 says the Lease is used even with one replica, because an upgrade runs two pods for a while. A cold standby would give every upgrade a "connecting" gap. `false` is for very large clusters, where the standby's memory matters.

## Behaviour by role

| | Leader | Warm standby (`true`) | Cold standby (`false`) |
|---|---|---|---|
| Informers (32 kinds × each connection) | run | run | **not started** |
| `live.Store` refresh: metrics-server, kubelet volume stats | run | **stopped** | stopped |
| Kubelet uptime loop (`bootLoop`) | run | **stopped** | stopped |
| Connection health check (`checkHealth`) | run | **stopped** | stopped |
| Role drift checks (`RunRoleChecks`) | run | **stopped** | stopped |
| Serves `/api/v1/*` data and the event streams | yes | forwards to the leader | forwards to the leader |
| Serves sign-in, `/api/v1/me`, `/api/v1/admin/users*`, `/api/v1/admin/ldap/refresh`, `/setup`, `/signin*`, `/signout`, `/healthz`, `/readyz`, `/metrics`, the UI | yes | yes, itself | yes, itself |
| Shared state from Kubernetes: sessions Secrets, settings ConfigMap (every 3 s), connection Secrets (every 5 s) | yes | yes (small, namespaced reads; leave as is) | yes |
| `/readyz` ready when | the local cluster is synced (as today) | the local cluster is synced (as today, so a rolling update waits for a warm standby) | a leader is known (`LeaderAddr() != ""`) |

With a single replica and no Lease (`leader.New` without a client, `Single()`), the replica always leads and nothing changes. Keep a test for that.

## Where the work lands

### 1. Configuration and chart

- `server/internal/config/config.go`: add `WarmStandby bool`, with flag `-warm-standby` and env `WARM_STANDBY`, default `true`.
- `deploy/helm/221b/values.yaml`: add a `replicas:` block holding `warmStandby: true`, with a comment that says in plain sentences what the table above says. Keep `replicaCount` where it is (CONTRIBUTING: rename nothing).
- `deploy/helm/221b/templates/deployment.yaml`: render `WARM_STANDBY` from `.Values.replicas.warmStandby`, falling back to `true` when the block is missing, so upgrades from older values work (`dig "replicas" "warmStandby" true .Values`).
- `hack/check-chart.sh`: render with `--set replicaCount=2 --set replicas.warmStandby=false` and check the env var.

### 2. One switch for the replica's role

Add a small type to `server/internal/leader` (or `cmd/221b`), for example `Activity`. It holds `active bool` (this replica leads) and the warm or cold mode, and lets the readers register start and stop hooks. Wire it in `cmd/221b/main.go`:

- `newElector` already takes `onLead`. Add `OnStoppedLeading` (the `leader.Options` field exists, `leader.go:37`, but `newElector` doesn't pass it) and drive the switch from both.
- `Single()` means the replica is always active.
- Keep the existing reloads on `leading` (`main.go`, ~line 405).

### 3. `kube.Cluster`: split watching from polling

`Cluster.Run` (`kube/cluster.go:376`) does three things today:
- starts the informers;
- starts the health loop (`c.loop(ctx, c.healthInterval, c.checkHealth)`);
- starts the kubelet uptime loop (`bootLoop`).

`Poll` (`cluster.go:429`) does metrics-server and volumes. Change it to:

- `RunWatches(ctx)`: the informers and the "synced" bookkeeping only.
- `RunPollers(ctx)`: the health loop and `bootLoop`, started on becoming active and cancelled on losing it.
- `Poll` does nothing while inactive. Simplest is to gate it in `live.Store.Refresh` (below), but make sure the cluster's pod-usage map is *kept*, not cleared, while inactive.
- Keep `Run` as a wrapper calling both for the tests and for the single-replica case, or update its callers. The callers are `main.go:313` and `clusters/manager.go:334`.

**Cold mode gotcha.** A stopped `SharedIndexInformer` cannot be run again. So in cold mode:
- On **gaining** the Lease, call `RunWatches` (the informers were built in `kube.New` but never run, which is fine).
- On **losing** the Lease, the simplest correct behaviour is to **exit the process** (log it, then `os.Exit(1)` after the HTTP server's graceful shutdown) so the pod restarts as a fresh cold standby. Losing the Lease without shutting down is rare (a partition, or a renew timeout).
- Do not try to rebuild the informers in place: the `Store`, the API and Watson hold references to the `Cluster`.
- In warm mode, losing the Lease only stops the pollers.

### 4. `live.Store`

- `Store.Run` (`live/store.go:509`) keeps rebuilding its views on every replica in warm mode. That is CPU only and keeps a promoted standby's views ready. But `Refresh` must call `s.src.Poll` only while active. Add an `Active func() bool` to `live.Options`, or a setter.
- On becoming active, call `Refresh` at once rather than waiting for the timer, so the new leader's usage figures appear within a second.
- `wantUsageLocked` (`store.go:~1205`) still decides whether pod usage is read on the leader. With every stream on the leader it is now correct, and the flicker goes away.

### 5. `clusters.Manager`

- Each connection's `kube.Cluster` and `live.Store` are started in `Manager.start` (`clusters/manager.go:297-338`). The local one is started in `main.go:313-314`. Both need the same role handling.
- Give the `Manager` the `Activity`, apply it in `start`, including connections added later through `manager.Run` (connections saved through another replica), and fan the role changes out to every instance.
- In cold mode, `start` builds the instance but does not run its informers until active.

### 6. The API: forward the data to the leader

- `leaderOnly` (`api/proxy.go:46`) becomes "everything under `/api/v1/` except the replica-local routes".
  - The local routes are those in the table above. The auth package registers them (`internal/auth`; list them with `grep -rn '"GET /\|"POST /\|"PUT /\|"DELETE /' internal/auth`).
  - Use an allowlist of local routes, not a list of data routes, so a new data route is forwarded by default.
  - Keep the existing per-cluster form `/api/v1/clusters/{cluster}/…`.
  - `/api/v1/clusters` itself (`GET`, `POST`, `PUT`, `DELETE`, `…/test`) is forwarded: its summaries carry the connection state the leader's health check measures.
- `leaderUnavailable` (`proxy.go:173`) says "Sherlock and Watson are moving to another replica". Make it "The console is moving to another replica; try again in a moment".
- `readyz` (`api/server.go:826`) follows the table. Give `Options` a `Standby func() bool` (cold and not leader), or make `Ready` role-aware in `main.go`.
- `RunRoleChecks` (`api/roles.go:166`) runs only while active.
  - Its result feeds the Permissions page and `rbac.enforce: strict`, which Watson reads on the leader, and its audit records would otherwise be written once per replica.
  - On becoming active, run one check at once (the timer's first tick is 15 s).
- Streams: every browser's event stream now ends on the leader. `MaxStreams` (`api/server.go:278`, default 200) is per replica, so it becomes the console-wide cap; say so in the chart comment and in ADR-70. A follower's forwarded stream holds a connection on both replicas; that is fine.
- `/metrics` on a follower reports its own (empty) view gauges. Add `b221_leader 0|1`, so dashboards can pick the leader's figures.

### 7. Things that already work and need no change

- The reverse proxy passes streams event by event, cancels them when the leader moves (`forwardToLeader`, `leaderChecks`), passes WebSocket upgrades (the Toolbox terminals use this today), and carries the person's address under the shared token (`trustForwarded`).
- The leader checks the session again, so a forwarded request is authorized twice, as today.
- Sherlock's tools call the console's own `GET` handlers in-process on the leader (ADR-43), so they read the leader's cache.
- Settings, acknowledgements and fleets are written to the ConfigMap by whichever replica serves the `PUT`, which is now the leader. Followers still reread every 3 s.

## Requirements and records to update (CONTRIBUTING)

- `docs/requirements.md`:
  - Amend **P7-02**'s acceptance: "Deleting a replica costs viewers no more than a reconnect; with `replicas.warmStandby: false`, a reconnect and the new leader's first list."
  - Add **P7-09**: "With several replicas, only the leader reads the clusters: it lists and watches them, polls metrics-server and the kubelets, checks connection health and role drift, and serves every data request; the other replicas sign people in and forward the rest. Warm standbys keep their watches open and poll nothing." Acceptance: as in the tests below.
- `docs/architecture.md`:
  - **ADR-70**, "Only the leader reads the clusters; standbys keep watches warm, or none": context (the flicker and the load), decision (the tables above), consequences (Kubernetes' load is independent of `replicaCount`; one extra hop; the stream cap becomes console-wide; cold failover costs a LIST).
  - Mark ADR-60 as changed by ADR-70 in its Status line.
- `CHANGELOG.md`, under *Unreleased*: a **Changed** line for operators (only the leader reads the clusters; `replicas.warmStandby`) and a **Fixed** line ("With two replicas, CPU and memory in use flickered to — on the Helm release page and in the pod and object drawers").
- `docs/upgrading.md`: nothing to do for upgrades (the value defaults); mention `replicas.warmStandby` for large clusters.
- `README.md`: the replicas section, one sentence.

## Tests

Each fix has a test that fails without it.

- **Go, `internal/api`:**
  - A table test of `leaderOnly`: every route registered by `New` either stays local (the allowlist) or is forwarded. Walk the mux patterns so a new route cannot slip through.
  - Extend the proxy tests: a follower forwards `GET /api/v1/helm/{ns}/{name}`, `GET /api/v1/events` and `GET /api/v1/clusters/{id}/pods/{ns}/{name}`, and serves `GET /api/v1/me` and `POST /signin` itself.
  - `readyz` per role and mode.
- **Go, `internal/live`:** `fake.Source` already counts polls (`internal/fake/source.go`, `Poll`). An inactive store does not poll. Becoming active polls at once. While inactive, the pod usage the source holds is not cleared.
- **Go, `internal/kube`:** inactive, `RunPollers` has not run (no health checks against an `httptest` server that counts requests), and `RunWatches` has (warm) or has not (cold).
- **Go, `cmd/221b` or a small integration test:** losing the Lease in cold mode ends the process; in warm mode it stops the pollers only.
- **Regression for the flicker:** two `api.Server`s over the same fake cluster, one leader holding the subscription, one follower forwarding. Fetch the release details 20 times through the follower; `usage` is present every time.
- **kind, `hack/ha-kind.sh`:**
  - With two replicas, the follower's `/metrics` shows `b221_leader 0`. Its port-forwarded API answers data through the leader, and the API server audit, or a counter of requests from the follower's ServiceAccount, shows no LIST or WATCH from it in cold mode.
  - Delete the leader in warm mode: the page reconnects within the usual time.
  - The same in cold mode: it reconnects after the first LIST.
- `make test`, `make lint`, `make chart-lint`, `hack/check-chart.sh`.

## Out of scope (noted in the review, not this change)

- `readPodMetrics` (`kube/cluster.go:1095-1126`) replaces the whole pod usage map with an empty one when a single metrics-server read fails. That still blanks the figures once in a while, even with one replica. Keeping the last good map on an error is a separate fix.
- Keeping a pod's last known usage in the browser when a response lacks it.

## Done when

- With `replicaCount: 2`, the Helm release page's CPU and memory never show "—" while metrics-server answers.
- Over five minutes, the API server sees LIST, WATCH and metrics requests from one replica only (cold mode), or only watches from the standby (warm mode).
- Deleting either replica costs viewers a reconnect (warm), or a reconnect and one LIST (cold).
- CI is green, and the docs above are updated.
