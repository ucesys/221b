# 221B Architecture

This document describes 221B as built in Phase 1: its parts, how data moves through them, the security model and the ways it can be deployed. The decision records at the end explain each choice in the Architecture table of the [requirements](requirements.md#architecture), plus the decisions Phase 1 itself made. It is the document for review item P1-01.

## In one paragraph

221B is one Go server and a browser app, shipped as one container image and installed with a Helm chart. The server keeps a watched, in-memory copy of the cluster's nodes and pods, builds the rows the Nodes tab shows, and streams changes to every open browser over Server-Sent Events. Browsers never see a Kubernetes object, only rows. In Phase 1 the server runs inside the cluster it shows, with a ServiceAccount that can only read nodes, pods and node metrics, and people open it with `kubectl port-forward`.

## Components

```mermaid
flowchart LR
  subgraph cluster[Kubernetes cluster]
    api[API server]
    ms[metrics-server]
    subgraph ns[namespace 221b]
      srv[221B server]
    end
  end
  browser[Browser] -- "kubectl port-forward" --> srv
  srv -- "list + watch nodes, pods\nGET /version every 10 s" --> api
  srv -- "GET metrics.k8s.io nodes every 15 s" --> api
  api -.-> ms
```

| Part | Where | What it does |
| --- | --- | --- |
| Entry point | `server/cmd/221b` | Reads flags and environment, connects, wires the parts below together, serves HTTP and shuts down cleanly on SIGTERM. |
| Settings | `server/internal/config` | Flags with environment fallbacks (`CLUSTER_NAME`, `LISTEN`, `KUBECONFIG_PATH`, `KUBE_CONTEXT`, `API_SERVER`, `METRICS_INTERVAL`, `HEALTH_INTERVAL`, `LOG_LEVEL`, `LOG_FORMAT`). |
| Cluster connection | `server/internal/kube` | Chooses in-cluster, kubeconfig or URL mode. Runs two informers (nodes, and pods indexed by node), trims every object as it enters the cache, checks the API server's health, reads node usage from metrics-server, and turns watch errors into warnings people can act on. |
| Row builder | `server/internal/nodes` | Turns one node and the pods on it into a table row: status, problems with their causes, CPU and memory requested, limits, allocatable and in use, pod counts, taints, zone, kubelet, age and the drawer's details. |
| Live store | `server/internal/live` | Keeps every row as encoded JSON, recomputes the rows a change touches, and publishes versioned deltas to subscribers. |
| HTTP API | `server/internal/api` | The JSON API, the event stream, health, readiness and Prometheus metrics, and the security headers on every response. |
| Browser app files | `server/internal/ui` | Serves the built browser app, embedded in the binary. |
| Simulator | `server/internal/sim`, `server/cmd/221b-sim` | A fake Kubernetes API server with any number of nodes and pods, an admin API to break things, and outages on demand. Used for development, tests and the scale test. |
| Browser app | `web/` | TypeScript and React. `api/live.ts` follows the event stream; `lib/` holds formatting, the table logic and the Excel writer; `components/` holds the screens. |
| Chart | `deploy/helm/221b` | Deployment, Service, ServiceAccount, ClusterRole and binding, NetworkPolicy. |
| CI scripts | `hack/` | Chart checks, the kind end-to-end run, the RBAC check and the kwok scale run. |

## Data flow

```mermaid
sequenceDiagram
  participant K as API server
  participant I as Informers (cache)
  participant S as Live store
  participant H as Event stream
  participant B as Browser
  K->>I: list, then watch (streaming list with bookmarks)
  I->>I: trim each object (transform)
  I->>S: changed nodes (batched for 250 ms)
  S->>S: rebuild those rows, compare JSON
  S->>H: delta v+1 (upserts, removes, cluster state)
  H->>B: event: delta
  B->>B: apply if v+1, else reopen for a snapshot
```

1. **Listing and watching.** client-go informers list and watch nodes and pods. On Kubernetes 1.35 and later the list arrives as a stream (`sendInitialEvents` with a bookmark marking its end) instead of one large response; older servers fall back to an ordinary list. Watches resume from the last resource version after a drop, and relist by themselves after a `410 Gone`.
2. **Trimming.** A transform drops what no screen uses before an object enters the cache: managed fields, annotations, container images, volumes, environment and most of the pod spec. A pod keeps its name, namespace, node, phase, conditions, container states and the resources needed to compute requests and limits. This is what keeps the server under 1 GiB at Phase 1 scale (X-09).
3. **Change notices.** An event on a node marks that node; an event on a pod marks the node it runs on, and both nodes when it moves. Health and metrics changes mark the cluster state.
4. **Rows.** The store collects notices for 250 ms, then rebuilds the marked rows with `nodes.BuildRow`, encodes each as JSON and compares it with the previous encoding. Only rows that changed go into the delta, so a pod restarting on one node sends one row. Every 30 seconds the store also rebuilds every row, for problems that depend on time, such as a pod that has been starting for more than two minutes.
5. **Requests and limits** follow the scheduler's rules (`k8s.io/component-helpers/resource`): the larger of the init containers and the app containers, sidecar init containers added to both, plus pod overhead. Terminated pods do not count, as in `kubectl describe node`.
6. **Deltas.** Each delta has a version one higher than the last. A browser that misses one (its version jumps) reopens the stream and gets a fresh snapshot. A browser that falls 64 deltas behind is sent a snapshot instead of the backlog, so one slow tab cannot hold memory on the server.
7. **The event stream.** `GET /api/v1/events` starts with a `snapshot` event, then sends `delta` events as they happen, a comment every 15 seconds so proxies keep it open, and `retry: 3000` so a browser reconnects by itself. It is gzip-compressed when the browser accepts it; the snapshot of 500 nodes is about 850 KB of JSON and 75 KB compressed.
8. **The browser** keeps the rows in memory, sorts and filters them on the client, and renders them with React. Rows off screen skip layout and paint (`content-visibility: auto`), so re-sorting 500 rows only lays out what is in view.

**Connection status.** The server asks the API server for `/version` every 10 seconds with a 5-second timeout. One failure turns the status red with the cause in plain words ("The connection was refused.", "It did not answer within 5 seconds."), within about 15 seconds of the API server going away (P1-11). While it is away, the informers wait for it, checking every second, instead of retrying with client-go's backoff, which grows to 30 to 60 seconds between attempts. So they resume or relist within a second or two of its return, well inside P1-10's 30 seconds, and the status turns green at the next check. The last known rows stay on screen throughout, marked as such.

**Metrics.** Every 15 seconds the server reads `metrics.k8s.io/v1beta1/nodes`. If metrics-server is missing or failing, the table and cards still work and say why usage is not shown (P1-12).

## API

All responses carry the security headers below. Every endpoint is `GET`; Phase 1 has nothing that changes state.

| Path | Returns |
| --- | --- |
| `/api/v1/snapshot` | Every row, the cluster state and the version, as JSON. |
| `/api/v1/nodes/{name}` | One row. |
| `/api/v1/events` | The event stream described above. |
| `/healthz` | `ok` while the process runs (liveness). |
| `/readyz` | `ok` once nodes and pods have been listed the first time (readiness). It stays ready during a later API server outage, so the console stays reachable to say so. |
| `/metrics` | Prometheus text: HTTP requests by route and code, change delay from watch event to browser (histogram), rows, cached nodes and pods, watch events, open streams, API server and metrics-server state, and memory. |
| `/` | The browser app. Hashed assets are cached for a year; `index.html` is never cached. |

## Security model

**What the server can read.** The chart's ClusterRole allows `get`, `list` and `watch` on nodes and pods, and `get` and `list` on `metrics.k8s.io` nodes. Nothing else: no Secrets, no ConfigMaps, no writes, no `exec` or logs. CI checks this with `kubectl auth can-i` against the installed ServiceAccount, and checks that listing Secrets is refused by the API server itself (P1-09). The requirement also allows pod metrics; the Nodes tab does not use them, so Phase 1 does not ask for them.

**What a browser can see.** Rows only. A row carries a node's details and, for pods with problems, their namespace, name, reason and message. The pod transform removes environment variables before anything is cached, so not even literal values in a pod spec reach the server's memory, let alone a browser. Phase 1 never reads a Secret.

**Who can open it.** Phase 1 has no sign-in, so the chart creates only a ClusterIP Service and documents `kubectl port-forward`. Opening the console therefore needs the right to port-forward to a pod in its namespace, which the cluster's own RBAC grants. Anyone with that right sees what the console's ServiceAccount can read: node details and the names of failing pods across namespaces. Keep port-forward rights in the 221B namespace to the people who should see that. Phase 3 adds sign-in and per-person access.

**Network.** The NetworkPolicy allows no inbound traffic except from peers listed in `networkPolicy.ingressFrom` (port-forward and kubelet probes are not affected by NetworkPolicy), and outbound traffic only to DNS and the API server ports (443 and 6443). 221B makes no other outbound calls: no telemetry, and the fonts are bundled (X-16).

**Container.** A distroless static image with no shell or package manager, running as user 65532 with a read-only root filesystem, no privilege escalation, all capabilities dropped and the RuntimeDefault seccomp profile. It passes the Pod Security `restricted` profile, which CI enforces on the test namespace (X-05). The server sizes Go's memory limit to 90% of the container's.

**Browser.** A strict Content Security Policy (`default-src 'self'`, no inline scripts or styles, `frame-ancestors 'none'`), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and cross-origin opener and resource policies (X-06). The app sets no cookies and has no state-changing requests, so there is nothing to forge yet.

**Supply chain.** GitHub Actions are pinned to commit SHAs and downloaded tools to checksums. CI checks dependency licenses (no GPL, AGPL or LGPL), audits the shipped npm packages, and scans the image with Grype, failing on any high or critical finding (P1-04, P1-05).

## Deployment modes

| Mode | How | Used for |
| --- | --- | --- |
| In the cluster | `helm install`; the in-cluster ServiceAccount | Phase 1's test deployment and production. |
| On a laptop, against a real cluster | `221b -kubeconfig ~/.kube/config -context <name> -cluster-name <name>` | Development. Shows the same nodes as the in-cluster mode (P1-08), with your own permissions. |
| On a laptop, simulated | `make dev-sim` | Development and the browser tests, with no cluster at all. |
| Management mode | Phase 3 | One installation connected to several clusters with stored credentials. |

The image is published to `ghcr.io/ucesys/221b` for `linux/amd64` and `linux/arm64`: `main` and `sha-<commit>` from main, and `pr-<number>` from this repository's pull requests. The package is private while the repository is, so a cluster pulling it needs an image pull secret (see the [README](../README.md#try-it-on-a-cluster)).

## Operations

- **Probes.** Liveness on `/healthz`, readiness on `/readyz` (see above).
- **Logs.** Structured JSON on standard error, one line per event; client-go's own logging goes to the same log.
- **Metrics.** `/metrics`, listed above. `b221_change_delay_seconds` measures X-07's freshness from the server's side; `b221_api_server_up` and `b221_metrics_available` show the connection.
- **Resources.** Requests 100m CPU and 256 MiB; limits 1 CPU and 1 GiB. At 500 nodes and 15,000 pods the server uses about 170 MiB.
- **Shutdown.** On SIGTERM the server stops accepting connections, ends the event streams and exits within 10 seconds.

## Scale

Phase 1's target is 500 nodes and 15,000 pods (P1-18). Measured with the simulator on a 2-core machine: the first screen in about 1 second, a sort in about 70 ms, and 170 MiB of server memory. CI repeats the measurement on every pull request with kwok in kind, and fails if the first screen takes 2 seconds, a sort 200 ms, or the server 1 GiB.

Phase 7's 5,000 nodes will need two changes the design allows for: virtualizing the table (rendering only the rows in view) and sending each browser only the rows its screen shows.

## Testing

| Level | What | Where |
| --- | --- | --- |
| Unit | Row building, problem detection, the store, the API, settings; the table logic, formatting, Excel writer and live-state logic in the browser | `go test ./...`, `npm test` |
| Integration | The real client-go informers against the simulator: streaming lists, watch recovery, outages, metrics on and off | `server/internal/kube` |
| Browser, simulated | Every Nodes tab behavior, live updates, outage and recovery, accessibility (axe, WCAG 2.2 AA) and keyboard use, in Chromium, Firefox and WebKit | `make e2e-sim`, `web/e2e` |
| Browser, real cluster | The chart in kind on Kubernetes 1.35, 1.36 and 1.37 with metrics-server: install within 60 seconds, permissions, port-forward, the same browser tests with kubectl and docker making the changes, then upgrade and uninstall | `hack/e2e-kind.sh` |
| Scale | 500 nodes and 15,000 pods with kwok | `hack/scale-kind.sh`, `web/e2e/scale.spec.ts` |

## Decision records

Records 1 to 9 cover the Architecture table in the requirements; 10 to 15 were made while building Phase 1. Proposed records are not decided yet; they are listed so the review can see where they fit.

### ADR-1: Go for the server

- **Status:** Accepted.
- **Context:** The server talks to Kubernetes all day, and later phases need Helm releases, kubectl's drain and describe logic, and Pod Security checks.
- **Decision:** Go, with client-go.
- **Consequences:** The libraries Kubernetes itself uses (client-go, the Helm SDK, kubectl's packages, the Pod Security admission library) are available as code rather than as shell-outs. Phase 1 uses client-go and `component-helpers`; the binary is about 29 MB.

### ADR-2: TypeScript and React for the browser app

- **Status:** Accepted.
- **Context:** The prototype is a set of screens built from repeated parts: tables, cards, drawers, menus.
- **Decision:** TypeScript and React 19, built with Vite. No UI component library: the prototype's stylesheet is used as is (`web/src/styles/design.css`), so screens match it by construction.
- **Consequences:** The bundle is small (about 80 KB of script, compressed, plus fonts). Matching the prototype is a matter of markup, not of overriding a library's styles.

### ADR-3: One image, with the browser app inside the Go binary

- **Status:** Accepted.
- **Context:** Two images would mean two things to version, scan and keep in step.
- **Decision:** The browser app is built first and embedded in the binary with `go:embed`; the image holds only that binary.
- **Consequences:** One version, one scan, one thing to deploy (P1-05). The image is about 31 MB. The app and the API always come from the same build, so their data shapes cannot drift apart in production.

### ADR-4: A Helm chart for deployment

- **Status:** Accepted.
- **Context:** Test and production installs should differ only in settings.
- **Decision:** A Helm chart in `deploy/helm/221b`, tested by `helm lint`, schema validation against Kubernetes 1.35 and 1.37, and real installs in kind.
- **Consequences:** `helm install`, `upgrade` and `uninstall` are the supported lifecycle; CI proves uninstall leaves nothing behind (P1-06). The chart requires `clusterName`, since the API server has no reliable name for its cluster.

### ADR-5: Informers on the server, Server-Sent Events to the browser

- **Status:** Accepted.
- **Context:** Changes must show within 5 seconds (X-07), and many people may watch the same cluster.
- **Decision:** One informer per resource type per cluster, shared by every viewer; deltas go to browsers over Server-Sent Events.
- **Alternatives:** Browser polling (slower and heavier on the API server); WebSockets (two-way, which Phase 1 does not need, and harder through proxies). SSE reconnects by itself and works through ordinary HTTP proxies.
- **Consequences:** The API server's load does not grow with viewers. A change typically reaches the screen in well under a second; batching changes for 250 ms bounds the update rate during bursts.

### ADR-6: Rows are built on the server

- **Status:** Accepted.
- **Context:** Raw objects are large and can carry sensitive values.
- **Decision:** The server turns objects into rows; browsers receive only rows.
- **Consequences:** Secret handling (X-01) has one place to get right in every phase. Payloads are small, and the problem rules live in one tested place (`server/internal/nodes`). Sorting and filtering stay in the browser, where they are instant at Phase 1 scale.

### ADR-7: In-cluster ServiceAccount with a read-only ClusterRole (Phase 1)

- **Status:** Accepted.
- **Context:** Phase 1 shows the cluster it runs in and changes nothing.
- **Decision:** The chart creates a ServiceAccount bound to a ClusterRole with read verbs on nodes, pods and node metrics only.
- **Consequences:** No credentials to store. Pod metrics, allowed by P1-09, are left out because the Nodes tab does not use them. Later phases widen the role only by what their screens need (X-02), and a chart setting will keep Secrets unreadable (P2-10).

### ADR-8: Sign-in and per-person access by impersonation (Phase 3)

- **Status:** Proposed; an open question in the requirements.
- **Proposal:** OIDC sign-in, and calls to Kubernetes made as the signed-in person through impersonation, so Kubernetes RBAC decides what each person sees and the audit log names them.
- **Phase 1 impact:** None in code. Until then, access is gated by who may port-forward (see the security model).

### ADR-9: The Claude API is called only from the server (Phase 5)

- **Status:** Proposed for Phase 5.
- **Proposal:** Sherlock's calls go from the server; the key lives in a Kubernetes Secret; browsers never hold the key or receive raw cluster data for prompts.
- **Phase 1 impact:** None. Phase 1 makes no outbound calls except to the API server.

### ADR-10: Distroless static base image

- **Status:** Accepted.
- **Context:** The image must be small, non-root and clean of vulnerabilities (P1-05, X-05).
- **Decision:** `gcr.io/distroless/static-debian13:nonroot`, with a statically linked binary (`CGO_ENABLED=0`), built for amd64 and arm64 by cross-compiling.
- **Consequences:** No shell, so debugging uses `kubectl debug` with an ephemeral container. The scan has almost nothing to find besides the Go binary itself, which stays current by building with the latest Go patch release.

### ADR-11: A simulated API server for development and tests

- **Status:** Accepted.
- **Context:** Many behaviors (an API server outage, a node that stops reporting, 15,000 pods) are slow or awkward to produce on a real cluster, and development should not need one.
- **Decision:** `221b-sim` implements just enough of the Kubernetes API (list and watch with resource versions, streaming lists, bookmarks, `410 Gone`, node metrics) for client-go's real informers to run against it, plus an admin API to make changes and outages.
- **Consequences:** Integration tests use the real client-go code paths in seconds, and the browser tests run without a cluster in every browser. The same tests run against kind with real Kubernetes, so the simulator cannot hide a difference for long.

### ADR-12: No table virtualization in Phase 1

- **Status:** Accepted for Phase 1; revisit in Phase 7.
- **Context:** The Architecture table counts on virtualization for large tables. At 500 rows, the measured cost of rendering every row is small once off-screen rows skip layout and paint.
- **Decision:** Render every row, with `content-visibility: auto` on rows.
- **Consequences:** Browser find, screen readers and keyboard navigation see the whole table, and tests can count rows. A sort of 500 rows takes about 70 ms. Phase 7's 5,000 nodes will need virtualization.

### ADR-13: Excel export written in the browser, without a library

- **Status:** Accepted.
- **Context:** Every table exports to Excel (X-13); spreadsheet libraries are large, and some carry license or security baggage.
- **Decision:** A small writer (`web/src/lib/xlsx.ts`, ported from the prototype) that produces a valid `.xlsx`: an uncompressed ZIP of five XML parts, with typed numeric cells, a frozen header and filters.
- **Consequences:** No dependency, a few kilobytes of code, and the export contains exactly the rows, columns and order on screen, which the browser tests check.

### ADR-14: Bundled fonts

- **Status:** Accepted.
- **Context:** The prototype loads IBM Plex from Google Fonts. That is a third-party request at runtime, which X-06 and X-16 rule out.
- **Decision:** Bundle IBM Plex Sans and Mono (Latin and Latin Extended) from the `@fontsource` packages, served by 221B itself.
- **Consequences:** The app looks the same with no outbound requests and works in clusters with no internet access. The fonts are under the SIL Open Font License.

### ADR-15: Kubernetes versions under test

- **Status:** Accepted (open question 3 in the requirements, settled with the proposed answer).
- **Decision:** CI installs the chart in kind on every minor release maintained upstream, today 1.35, 1.36 and 1.37, and validates the chart's manifests against the oldest and newest.
- **Consequences:** When a new minor release ships and the oldest goes out of support, the matrix in `hack/lib.sh` moves with them.
