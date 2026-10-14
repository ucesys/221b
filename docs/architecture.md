# 221B Architecture

This document describes 221B as built through Phase 3: its parts, how data moves through them, the security model and the ways it can be deployed. The decision records at the end explain each choice in the Architecture table of the [requirements](requirements.md#architecture), plus the decisions each phase made. It was the document for review item P1-01 and is kept current for every review since.

## In one paragraph

221B is one Go server and a browser app, shipped as one container image and installed with a Helm chart. The server keeps a watched, in-memory copy of the cluster's nodes, pods, workloads, Services, Helm releases and the other kinds each namespace lists, trimmed to what the screens use; it builds the rows each tab shows as *views*, and on a shared refresh tick sends every open browser the changes to the views it is showing, at the same moment, over Server-Sent Events. Browsers never see a Kubernetes object, only rows, except in the YAML view, which the server serves with managed fields removed and Secret values replaced by their sizes. Secrets are read only to list their keys and sizes and to decode Helm's release records, with credential-looking values masked before they enter the cache; a chart setting withholds Secrets altogether. The server runs inside the cluster it shows, with a ServiceAccount that can read the kinds the tabs show, pod logs, metrics and the kubelets' stats, and write two things in its own namespace: its settings ConfigMap and the Secrets that hold its connections to other clusters, each of which it watches the same way, as one more cluster in the menu (Phase 3). People sign in with the organization's OpenID Connect provider (or local accounts, for evaluations) and open it through `kubectl port-forward`, an exposed Service or an Ingress with TLS; every sign-in and every change to a connection is written to an audit log.

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
  srv -- "list + watch 27 kinds\nGET /version every 10 s" --> api
  srv -- "GET metrics.k8s.io nodes every refresh, pods while shown\nGET nodes/proxy stats once per node and boot\nGET pods/log while a log is open\nGET one object for the YAML view\nGET + PATCH its settings ConfigMap" --> api
  api -.-> ms
  api -. "stats/summary, logs" .-> kubelets[kubelets]
```

| Part | Where | What it does |
| --- | --- | --- |
| Entry point | `server/cmd/221b` | Reads flags and environment, connects, wires the parts below together, serves HTTP and shuts down cleanly on SIGTERM. |
| Configuration | `server/internal/config` | Flags with environment fallbacks (`CLUSTER_NAME`, `LISTEN`, `KUBECONFIG_PATH`, `KUBE_CONTEXT`, `API_SERVER`, `REFRESH`, `SETTINGS_CONFIGMAP`, `CONNECTIONS_SECRETS`, `KUBELET_STATS`, `SECRETS_READ`, `HEALTH_INTERVAL`, `LOG_LEVEL`, `LOG_FORMAT`, and for Phase 3 `AUTH_MODE`, `OIDC_*`, `LOCAL_USERS_FILE`, `ADMINS`, `ADMIN_GROUPS`, `SESSION_IDLE`, `SESSION_MAX`, `AUDIT_WEBHOOK_URL`, `AUDIT_WEBHOOK_TOKEN`, `HTTPS_REDIRECT`). |
| Shared settings | `server/internal/settings` | The settings everyone shares, today the refresh rate: validated, kept in a ConfigMap in the cluster (or in memory on a laptop), re-read every minute so a `kubectl edit` takes effect, and pushed to every browser when they change. |
| Sign-in and sessions | `server/internal/auth` | The sign-in page, the OIDC relying party (discovery, the code flow with PKCE and a nonce, ID token verification against the provider's keys), local accounts with PBKDF2 password hashes, sessions in memory with idle and absolute expiry, the two roles, and the gate every request passes (P3-01, P3-03). `server/internal/oidcsim` is the simulated provider the tests sign in through. |
| Audit log | `server/internal/audit` | One JSON record per sign-in, sign-out, settings change and connection change on standard output, and the same records to a webhook from a bounded queue (P3-10). |
| Cluster connections | `server/internal/clusters` | The connections to other clusters (P3-05 to P3-09): their specs and credentials in Secrets of the console's namespace (or in memory), validation of a draft and what each credential says about itself (a token's account and expiry, a kubeconfig's context, a certificate's subject), the five-step connection test, and a manager that runs one `kube.Cluster` and one live store per connection, the console's own cluster among them as `local`. |
| Kind registry and cluster connection | `server/internal/kube` | `kinds.go` lists every kind the console watches, with its API group, how it is cached (a typed informer with a transform, or metadata only) and its indexes. `cluster.go` chooses in-cluster, kubeconfig or URL mode, runs one informer per kind, checks the API server's health, reads node usage from metrics-server on every poll and pod usage while a tab wants it, reads each node's boot time from its kubelet once per boot, and reads and patches the settings ConfigMap. `secrets.go` turns Secrets into key names and sizes and decodes Helm release records with credentials masked; `logs.go` streams pod logs; `yaml.go` serves one object as YAML with managed fields removed and Secret values hidden. |
| Row builders | `server/internal/nodes`, `server/internal/views` | `nodes` turns one node and the pods on it into a Nodes row (Phase 1). `views` builds the Phase 2 rows: pods, namespaces and what each holds by kind, Services with their endpoints, Helm releases with their history, and the details behind each row (a pod's containers, environment, volumes, events and owner chain; a Service's ports, endpoints and the pods behind them; a release's resources, values and notes). |
| Live store | `server/internal/live` | Keeps the rows of every *view* as encoded JSON with a version per view, collects change notices from the informers, and on each refresh tick polls the cluster, recomputes the rows the changes touched and publishes one delta per changed view to the browsers showing it, followed by a refresh event. Views nobody shows are not built; per-namespace views are built when a browser opens them and dropped a minute after the last browser leaves. |
| HTTP API | `server/internal/api` | The JSON API: snapshots and the event stream by view, details for pods, Services and releases, pod logs, the YAML view, the settings endpoint, the cluster connections, health, readiness and Prometheus metrics, and the security headers on every response. Every data route exists twice: under `/api/v1/` for the console's own cluster and under `/api/v1/clusters/{id}/` for any connection. |
| Browser app files | `server/internal/ui` | Serves the built browser app, embedded in the binary. |
| Simulator | `server/internal/sim`, `server/cmd/221b-sim` | A fake Kubernetes API server serving every kind the console watches, with namespaces full of workloads, Services, Helm release records and planted Secrets, pod logs and metrics, an admin API to break things, and outages on demand. Used for development, tests and the scale test. |
| Browser app | `web/` | TypeScript and React. `lib/router.ts` maps the URL hash to a cluster, a tab and its filters (`#/pods` is the console's own cluster, `#/c/<id>/pods` another); `api/live.ts` subscribes to the views a screen shows and follows the event stream of the cluster open; `api/clusters.ts` and `components/ClusterMenu.tsx`, `ClusterPage.tsx` and `ClusterEditor.tsx` are the cluster menu, page and connection editor; `api/details.ts` fetches the details behind a row and refetches them on every refresh; `lib/table.ts` and `lib/useTable.ts` hold the one table model every tab uses (sorting, search terms with suggestions, column choices, exports); `columns/` defines each table's columns; `components/` holds the screens, the shared `DataTable`, `SearchBox`, `ColumnsMenu` and `ExportMenu`, the details drawers, the log view and the YAML view. |
| Chart | `deploy/helm/221b` | Deployment, Service, ServiceAccount, ClusterRole and binding, the settings ConfigMap with a Role and binding for it and for the connection Secrets, the sign-in Secret (local accounts, the OIDC client secret, the webhook token), an optional Ingress, NetworkPolicy. `secrets.read` decides whether Secrets are granted and read at all; `connections.store` whether connections live in Secrets or in memory. |
| CI scripts | `hack/` | Chart checks, the kind end-to-end run, the RBAC check (with and without Secrets) and the kwok scale run. |

## Data flow

```mermaid
sequenceDiagram
  participant K as API server
  participant I as Informers (cache)
  participant S as Live store
  participant H as Event stream
  participant B as Browser
  B->>H: GET /api/v1/events?views=pods,namespaces
  K->>I: list, then watch, one informer per kind
  I->>I: trim each object (transform); Secrets to keys and sizes
  I->>S: changed keys per view (collected until the tick)
  Note over S: refresh tick, every 3 s by default
  S->>K: GET node metrics; pod metrics while a tab wants them
  S->>S: rebuild the changed rows of the views shown, compare JSON
  S->>H: rows v+1 per changed view, usage, then refresh
  H->>B: event: rows, event: refresh
  B->>B: apply if v+1 for that view, else reopen for a snapshot
```

1. **Listing and watching.** client-go informers list and watch every kind in the registry: 16 kinds in full through typed informers (nodes, namespaces, pods, Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs, Services, EndpointSlices, Ingresses, PersistentVolumeClaims, ConfigMaps, Secrets and events), and 11 kinds that the Namespaces tab only lists by name as metadata-only informers (ControllerRevisions, Endpoints, NetworkPolicies, ServiceAccounts, Roles, RoleBindings, PodDisruptionBudgets, HorizontalPodAutoscalers, ResourceQuotas, LimitRanges and Leases), which the API server serves as `PartialObjectMetadata` so their bodies never leave it. On Kubernetes 1.35 and later lists arrive as streams (`sendInitialEvents` with a bookmark marking the end); older servers fall back to an ordinary list. Watches resume from the last resource version after a drop and relist by themselves after a `410 Gone`.
2. **Trimming.** A transform drops what no screen uses before an object enters the cache: managed fields, the last-applied annotation, and most of each spec. A pod keeps what the pod details show: containers with their images, environment (a variable taken from a Secret keeps the reference, never a value), volumes, conditions and container states, plus the resources needed to compute requests and limits. A ConfigMap keeps only its key names. A Secret keeps only its key names and each value's size, and a Helm release record (a Secret of type `helm.sh/release.v1`) is decoded into the release's chart, status, history and values, with every string under a credential-looking key (`password`, `token`, `secret`, `apiKey`, `privateKey`, `cert`, `auth`, `license` and the like) and every PEM private key replaced by a marker before caching (P2-07). This is what keeps the server under 1 GiB at Phase 1 scale with every kind watched (X-09; ADR-18, ADR-20).
3. **Views and change notices.** The store keeps *views*: `nodes`, `pods`, `namespaces`, `services`, `helm` and one `ns:<name>` view per namespace. An event on an object marks the views and keys it affects: a pod marks its own row in `pods`, its node's row in `nodes`, its namespace's rows in `namespaces` and `ns:<name>`, the Services selecting it and the release owning it. Health, metrics and boot-time changes mark the cluster state. The `pods` and `ns:<name>` views are *lazy*: nothing is built for them until a browser subscribes, and a namespace's view is dropped a minute after the last browser leaves it (ADR-19).
4. **The refresh tick.** Every refresh interval (3 seconds by default; the Settings dialog changes it for everyone, from 1 second to 10 minutes) the store polls what is not watched: node usage from metrics-server always, and pod usage only while some browser shows the Pods tab or a Helm release. It then rebuilds the marked rows of the views that have subscribers, encodes each as JSON and compares it with the previous encoding. Only rows that changed go into a view's delta, so a pod restarting sends one row to the Pods tab, one to the Nodes tab and one to its namespace. Pod usage goes out apart from the rows, as a `usage` map, so a change in CPU does not re-send a row. Every 30 seconds the tick also rebuilds every row, for problems that depend on time. One `rows` event per changed view, and then a `refresh` event stamped with the tick's time and every view's version, go to every browser at once. News about the connection to the API server does not wait for the tick; it goes out within 250 ms as a `meta` event, as do the counts in the tab bar.
5. **Requests and limits** follow the scheduler's rules (`k8s.io/component-helpers/resource`): the larger of the init containers and the app containers, sidecar init containers added to both, plus pod overhead, and during an in-place resize the larger of what is asked for and what the kubelet has allocated. Terminated pods do not count, as in `kubectl describe node`.
6. **Deltas.** Each view's delta has a version one higher than that view's last. A browser that misses one (a version jumps) reopens the stream and gets a fresh snapshot of its views. A browser that falls 64 events behind is sent a snapshot instead of the backlog, so one slow tab cannot hold memory on the server.
7. **The event stream.** `GET /api/v1/events?views=nodes,pods` (at most 8 views) starts with a `snapshot` event (the rows and version of each view, pod usage where it applies, the cluster state, the settings, the counts for the tab bar and the time of the last refresh), then `rows` events per view, `meta` events for the cluster state and counts, and a `refresh` event on every tick, a comment every 15 seconds so proxies keep it open, and `retry: 3000` so a browser reconnects by itself. The browser opens one stream for the views its screen shows and reopens it when it moves to a tab that needs others. It is gzip-compressed when the browser accepts it; the snapshot of 500 nodes is about 850 KB of JSON and 75 KB compressed, and of 15,000 pods about 8.7 MB and 1 MB.
8. **Details.** Opening a row fetches its details once (`/api/v1/pods/…`, `/api/v1/services/…`, `/api/v1/helm/…`) and again on every refresh tick while it is open, so an open drawer follows the cluster; a 404 while open means the object is gone, and the drawer says so (P2-04). Pod logs stream from the API server's `pods/log` endpoint through the server to the browser, batched into events of up to 500 lines every 200 ms, with at most 50 streams open at once per server and lines over 1 MiB split (ADR-21). The YAML view asks the API server for the one object live, so it is as fresh as `kubectl get -o yaml`, with `managedFields` removed and, for a Secret, every value replaced by `<N bytes, hidden by 221B>` (ADR-22).
9. **The browser** keeps the rows of each view in memory, sorts and filters them on the client, and renders every table with one component: an ARIA grid whose rows are focusable, with the arrow keys moving between rows and into a row's links, that renders only the rows in view (plus a margin) once a table passes 100 rows, so the Pods tab with 15,000 pods costs what a screenful does (ADR-23, which supersedes ADR-12). Every table has the same Columns and Export (Excel, CSV, JSON) menus and the same search box, which suggests names, labels, nodes, namespaces and statuses as you type (ADR-24).

**Connection status.** The server asks the API server for `/version` every 10 seconds with a 5-second timeout. One failure turns the status red with the cause in plain words, within about 15 seconds of the API server going away (P1-11). While it is away, the informers wait for it, checking every second, instead of retrying with client-go's backoff, so they resume or relist within a second or two of its return (P1-10). The last known rows stay on screen throughout, marked as such.

**Metrics.** On every refresh the server reads `metrics.k8s.io/v1beta1/nodes`, and `…/pods` while a browser shows the Pods tab or a Helm release; one request each however many browsers are open. If metrics-server is missing or failing, the tables still work and say why usage is not shown (P1-12).

**Uptime.** The Node object does not say when a node booted. Each kubelet does, in its Summary API, which the API server proxies at `/api/v1/nodes/<name>/proxy/stats/summary`; the server reads it once per node and bootID (ADR-17). The read needs `get` on `nodes/proxy`, which the chart grants when `kubeletStats` is on (the default).

**Helm releases.** Helm 3 keeps each release revision as a Secret named `sh.helm.release.v1.<release>.v<n>` holding a base64-encoded, gzip-compressed JSON record: the chart's metadata, the release's status, notes, the values the user supplied and the rendered manifest. The server decodes these as they enter the cache, keeps the chart, status, times, notes, masked values and the kinds and names in the manifest, and discards the manifest's bodies. The Helm tab lists one row per release (its latest revision) and the details show the revision history as `helm history` does, the notes and the user-supplied values with credentials masked (P2-06, P2-07). The pods of a release are matched by the labels Helm charts set (`app.kubernetes.io/instance` and `app.kubernetes.io/managed-by`) and the `meta.helm.sh/release-name` annotation, and, for charts that set neither on their pods, by their owners: a pod whose owner chain reaches a workload named in the release manifest belongs to the release (asked at the Phase 2 review, when a release showed no pods that `kubectl get pods -n <ns>` listed). With `secrets.read=false` there are no release records, so the Helm tab is hidden (P2-10).

**Shared settings.** The refresh rate is a setting everyone shares, so it lives on the server: in the cluster, as `settings.json` in a ConfigMap the chart creates empty and never writes, so `helm upgrade` keeps what was saved and `helm uninstall` removes it; on a laptop or with the simulator, in memory. The theme is per browser and stays in `localStorage`, as do each table's column choices.

## API

All responses carry the security headers below. Every path needs a signed-in session (P3-01) except the health probes, `/metrics`, the sign-in pages and the app's static files; without one the API answers 401 and a page shows the sign-in page. Every endpoint is `GET` except the sign-in forms, sign-out and `PUT /api/v1/settings`, which changes the shared settings, the only thing 221B writes to the cluster (X-03); every request that changes anything is refused when the browser says another site made it (X-06).

| Path | Returns |
| --- | --- |
| `/signin` | The sign-in page: a button to the OIDC provider, or the local account form (`POST` with `email`, `password` and `next`). `/signin/start?next=…` begins the OIDC flow (state, nonce and PKCE in a short-lived cookie) and `/signin/callback` ends it; both bring the person back to `next`. `POST /signout` ends the session. |
| `/api/v1/me` | Who is signed in: the sign-in mode, the person (subject, username, email, name, groups), whether they are an administrator (P3-03), how they signed in and the idle lifetime of sessions. |
| `/api/v1/snapshot?views=nodes,pods` | The rows and version of each view asked for (`nodes` by default; `pods`, `namespaces`, `services`, `helm`, `ns:<namespace>`), pod usage where it applies, the cluster state, the settings, the counts for the tab bar and the time of the last refresh. |
| `/api/v1/nodes/{name}` | One Nodes row. |
| `/api/v1/pods/{namespace}/{name}` | A pod's details: containers and their states, environment (Secret references, never values), volumes, events, owner chain, conditions, scheduling and usage. 404 once the pod is gone. |
| `/api/v1/pods/{namespace}/{name}/log` | The pod's log: `container`, `previous`, `tail` and `timestamps` as `kubectl logs` takes them; `download=1` sends it as a file; `follow=1` streams it as Server-Sent Events (`log` events carrying arrays of lines, then `end` with the reason). |
| `/api/v1/services/{namespace}/{name}` | A Service's details: ports, endpoints with the pods and nodes behind them, Ingresses publishing it, events, and why a LoadBalancer is pending. |
| `/api/v1/helm/{namespace}/{name}` | A release's details: history, notes, user-supplied values with credentials masked (and the list of masked keys), resources by kind with their state, pods and their usage. A pod belongs to the release by Helm's labels or annotation, or when its owners lead to a workload in the release manifest. 404 when Secrets are not read. |
| `/api/v1/objects/{kind}/{namespace}/{name}` | Any other object's details, from the cache: the row, facts by kind (replicas and strategy, schedule, rules, capacity, keys and sizes, …), conditions, labels, annotations, owner chain, events, the Helm release it belongs to, and for a workload the pods it controls with their usage. `kind` is the registry name or the Kind (`deployments` or `Deployment`). 404 for an unknown kind, a missing object, or a Secret when they are not read. |
| `/api/v1/yaml/{kind}/{name}`, `/api/v1/yaml/{kind}/{namespace}/{name}` | The object as YAML (`application/yaml`), read live, with `managedFields` removed and Secret values replaced by their sizes; `download=1` sends it as a file. `kind` is the registry name (`pods`, `deployments`, …). 404 for Secrets when they are not read. |
| `/api/v1/settings` | `GET`: the settings in force, where they are kept (`configmap` or `memory`) and when they last changed. `PUT` with `{"refreshSeconds": n}` (1 to 600, JSON only), for administrators, saves and applies them for everyone and writes an audit record. |
| `/api/v1/clusters` | `GET`: every connection as the cluster menu shows it (name, environment, description, API server, how the certificate is checked and the console signs in, what the credential says about itself, the state of its watches, version, node and pod counts, whether the credential expires within a week or has, the last test). Never a credential. `POST` with a draft (the same fields plus the credential: `token`, `kubeconfig` and `context`, `certPem` and `keyPem`, `caPem`), for administrators, validates it, inspects the credential, saves it, starts the connection and answers 201 with its summary, or 400 with `{"error", "field"}`. |
| `/api/v1/clusters/test` | `POST` with a draft (and `id` when it edits a saved connection, whose credential is used for fields left empty): runs the five-step test (address, TLS, sign-in, Kubernetes, permissions) and answers the steps, a verdict, the version and node count, who the credential signs in as and whether it may impersonate. For administrators. |
| `/api/v1/clusters/{id}` | `GET`: one connection's summary. `PUT`: a change; credential fields left empty keep the saved credential, a changed method needs a new one. `DELETE`: stops the connection and deletes its Secret (`local`, the console's own cluster, cannot be removed; only its name, environment and description can change). `POST …/test` tests the saved connection and keeps the result for its page. Administrators only; everything is audited. |
| `/api/v1/clusters/{id}/…` | Every data route above (`snapshot`, `events`, `nodes`, `pods`, `services`, `helm`, `objects`, `yaml`, pod logs) for that connection; `local` is the console's own cluster. 404 for an unknown connection, 503 for one that could not start. |
| `/api/v1/events?views=…` | The event stream described above, for up to 8 views. |
| `/healthz` | `ok` while the process runs (liveness). |
| `/readyz` | `ok` once nodes, pods and namespaces have been listed the first time (readiness); the other kinds fill in behind it. It stays ready during a later API server outage, so the console stays reachable to say so. |
| `/metrics` | Prometheus text: HTTP requests by route and code, change delay from watch event to browser (histogram), rows and version per view, browsers per view, cached objects per kind, open event and log streams, API server and metrics-server state, and memory. |
| `/` | The browser app. Hashed assets are cached for a year; `index.html` is never cached. |

## Security model

**What the server can read.** The chart's ClusterRole allows `get`, `list` and `watch` on the 27 kinds above, `get` on `pods/log`, `get` and `list` on `metrics.k8s.io` nodes and pods, and, with `kubeletStats` on, `get` on `nodes/proxy`. Secrets are in that list only while `secrets.read` is on (the default); off, the ClusterRole names no Secrets, the server starts no Secret informer, the Namespaces tab lists none, the YAML view refuses them and the Helm tab is hidden, since Helm keeps its release records in Secrets (P2-10). A namespaced Role allows `get` and `patch` on its own settings ConfigMap, by name, and `get`, `list`, `create`, `update` and `delete` on the Secrets of its own namespace, where the connections to other clusters are kept (`connections.store: memory` drops that grant and keeps them in memory). Kubernetes cannot grant Secrets by name prefix, so the console should have a namespace of its own, as the chart's notes say. Nothing else: no `exec`, no writes to workloads, no ClusterRoles, PersistentVolumes or CRDs. CI checks the grants with `kubectl auth can-i` against the installed ServiceAccount in both settings, checks that the API server refuses a pod deletion, and with Secrets off that it refuses listing Secrets (`hack/check-rbac.sh`).

**What the server keeps of a Secret.** The Secret transform runs inside the informer before an object is stored, so what reaches the cache is the Secret's name, type, labels, key names and each value's size, and for a Helm release record its decoded contents with credential-looking values masked. Annotations can carry values too: `kubectl apply` keeps the whole object, values included, in `kubectl.kubernetes.io/last-applied-configuration`, and tools such as kapp keep their own copy. That annotation, and any annotation holding one of the Secret's values in raw or base64 form, is replaced by its size, in the cache and in the YAML view alike. The server never holds a Secret value in memory after the transform returns, never logs one (the logs name objects, not their contents) and never serves one: rows and details carry keys and sizes, the YAML view replaces every value with `<N bytes, hidden by 221B>`, and an environment variable taken from a Secret shows the Secret's name and key. CI plants a Secret with a known value and a Helm release with a known password in the simulated cluster and checks that neither value appears in any API response, event stream, export or the server's log (P2-07, `web/e2e/secrets.spec.ts`).

**What a browser can see.** Rows and details, and YAML without managed fields or Secret values. The pods on a node, the pods behind a Service and the pods of a release are the same pod rows. Logs are what `kubectl logs` would show the console's ServiceAccount, and the console shows them to anyone who can reach it.

**Who can open it (P3-01, P3-03).** Everyone signs in: with an OpenID Connect provider (`auth.mode: oidc`), or with accounts the console keeps itself (`auth.mode: local`), for evaluations and clusters without a provider. The sign-in page is served by the server, with no app code; the OIDC flow is the authorization code flow with PKCE and a nonce, and the ID token is verified against the provider's published keys (RSA and ECDSA signatures; never a shared secret). A session is a random 256-bit identifier in an `HttpOnly`, `SameSite=Lax` cookie, marked `Secure` when the request came over TLS (directly, or through a proxy that sets `X-Forwarded-Proto`), and kept in the server's memory: it ends after `auth.sessionIdle` without a request (8 hours by default), after `auth.sessionMax` whatever happens (7 days), at sign-out, or when the server restarts, which signs everyone out. Administrators (`auth.admins` by username, `auth.adminGroups` by group) change the settings and manage cluster connections; everyone else uses the tabs and sees the cluster menu without Edit, Add or Remove. `auth.mode: none` keeps the console open as it was before Phase 3, with everyone an anonymous administrator, for development and the simulator; the chart's notes and the server's log warn about it. The chart can still expose the Service as a LoadBalancer or NodePort (`service.type`), or through an Ingress with TLS (`ingress`), behind which plain HTTP is redirected (P3-11).

**Other clusters (P3-05 to P3-09).** An administrator adds a cluster in the console: its API server address, how its certificate is checked (a pasted CA certificate, the image's trust store, or none, with a warning), how the console signs in (a ServiceAccount token, a kubeconfig context, or a client certificate and key; OIDC is reserved and refused for now), and whether the console may only read there or also change things in later phases. The server validates the draft, reads what the credential says about itself (the token's account and expiry, the kubeconfig's user and cluster, the certificate's subject and expiry), saves the spec and the credential in one Secret named `<release>-cluster-<id>` in the console's namespace, and starts the same informers and live store for it as for the cluster it runs in. The credential never returns to the browser: the API and the editor show only what it says about itself, and replacing it is the only way to touch it. A token or certificate that expires within a week is flagged in the menu, on the cluster page and in a banner, and an expired or failing connection shows why on its page with a *Fix the connection* action (P3-07). Removing a connection asks again (and, for a production cluster, for its name typed) and deletes the Secret with it (P3-08). The connection test runs the five steps the prototype shows, each against the real address: TCP and TLS to the API server, `/version` with the credential, the version and node count, and `SelfSubjectAccessReview`s for what the console will read, for impersonation and, in read-and-change mode, for the writes (P3-06). Every add, change, test and removal is audited with the cluster's id.

**Who did what (P3-10).** Every sign-in, failed sign-in, sign-out, settings change and connection change is one JSON record on the server's standard output, naming the person (subject, email, name), the action, its target and outcome, the time and the address it came from; the ordinary logs go to standard error, so the two can be routed apart. The same records go to a webhook when `audit.webhook.url` is set, with a bearer token, three tries each, and a bounded queue that drops with a warning and a counter on `/metrics` rather than block the console. No record carries a credential or a password.

**Network.** The NetworkPolicy allows no inbound traffic except from peers listed in `networkPolicy.ingressFrom`, or from anyone when the Service is exposed and no peers are listed (port-forward and kubelet probes are not affected by NetworkPolicy), and outbound traffic only on the API server's ports (443 and 6443 by default). With OIDC sign-in or an audit webhook it also allows DNS and the provider's or webhook's HTTPS port (`networkPolicy.outboundPorts`, narrowed by `networkPolicy.outboundCIDRs`); other clusters' API servers are reached through the same ports, so add theirs when they listen elsewhere. 221B makes no other outbound calls: no telemetry, and the fonts are bundled (X-16).

**Container.** A distroless static image with no shell or package manager, running as user 65532 with a read-only root filesystem, no privilege escalation, all capabilities dropped and the RuntimeDefault seccomp profile. It passes the Pod Security `restricted` profile, which CI enforces on the test namespace (X-05). The server sizes Go's memory limit to 90% of the container's.

**Browser.** A strict Content Security Policy (`default-src 'self'`, no inline scripts or styles, `frame-ancestors 'none'`), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and cross-origin opener and resource policies (X-06). The server answers only to `localhost`, IP addresses, its Service's DNS names, the Ingress host and names set in `allowedHosts`; any other `Host` is refused, so a web page cannot read the console through DNS rebinding while someone has a port-forward open. Every request that changes anything (the settings, sign-out, the local sign-in form) is refused when the browser's `Sec-Fetch-Site` or `Origin` header says another site made it, and the session cookie is `SameSite=Lax`, so another site cannot ride a session either (X-06). Log and YAML downloads are plain `GET`s of what the page already shows. Password guessing against local accounts is slowed to five attempts a minute per address and per account, and unknown accounts take as long to refuse as wrong passwords.

**Supply chain.** GitHub Actions are pinned to commit SHAs and downloaded tools to checksums. CI checks dependency licenses (no GPL, AGPL or LGPL), audits the shipped npm packages, and scans the image with Grype, failing on any high or critical finding (P1-04, P1-05).

## Deployment modes

| Mode | How | Used for |
| --- | --- | --- |
| In the cluster | `helm install`; the in-cluster ServiceAccount | The test deployment and production. |
| On a laptop, against a real cluster | `221b -kubeconfig ~/.kube/config -context <name> -cluster-name <name>` | Development. Shows the same cluster as the in-cluster mode (P1-08), with your own permissions; `-secrets-read=false` runs it without touching Secrets. |
| On a laptop, simulated | `make dev-sim` | Development and the browser tests, with no cluster at all. |
| Management mode | `helm install`, then *Add a cluster* in the console | One installation connected to several clusters with stored credentials (P3-09); the cluster it runs in is the connection `local`. On a laptop the same works against the simulator, with connections kept in memory unless `-connections-secrets` names a namespace. |

The image is published to `ghcr.io/ucesys/221b` for `linux/amd64` and `linux/arm64`: `main` and `sha-<commit>` from main, and `pr-<number>` from this repository's pull requests. The package is private while the repository is, so a cluster pulling it needs an image pull secret (see the [README](../README.md#try-it-on-a-cluster)). `main` and `pr-<number>` are moving tags, so for them the chart sets `imagePullPolicy: Always` and stamps the pod template with the release revision, which makes every `helm upgrade` restart the console on the tag's latest build; a release, a `sha-` tag or `image.digest` names one build for good and is pulled once.

## Operations

- **Probes.** Liveness on `/healthz`, readiness on `/readyz` (see above).
- **Logs.** Structured JSON on standard error, one line per event; client-go's own logging goes to the same log. The log names objects and errors, never their contents: no Secret value, no pod log line.
- **Metrics.** `/metrics`, listed above. `b221_change_delay_seconds` measures X-07's freshness from the server's side; `b221_rows{view}` and `b221_view_subscribers{view}` show what is built and watched; `b221_cached_objects{kind}` the size of each cache; `b221_log_streams` the logs open; `b221_api_server_up` and `b221_metrics_available` the connection.
- **Settings.** `kubectl -n 221b get configmap console-221b -o jsonpath='{.data.settings\.json}'` shows what is saved; editing it takes effect within a minute, and deleting the key returns to the chart's `refresh`.
- **Connections.** `kubectl -n 221b get secrets -l app.kubernetes.io/component=cluster-connection` lists the clusters added in the console, one Secret each (`spec.json` and the credential's keys); they survive upgrades and go with `helm uninstall` only if deleted first, since the chart does not own them. `b221_connections` and `b221_connection_up{cluster}` on `/metrics` say how many there are and which answer.
- **Resources.** Requests 100m CPU and 256 MiB; limits 1 CPU and 1 GiB. At 500 nodes and 15,000 pods with every kind watched the server uses about 360 MiB, and about 450 MiB while a browser has the Pods tab open, since that view's rows are kept as JSON ready to send.
- **Shutdown.** On SIGTERM the server stops accepting connections, ends the event and log streams and exits within 10 seconds.
- **Tabs.** Each open tab holds one HTTP connection for its event stream, and one more while a log is open, and browsers allow six connections per host over plain HTTP. Through one port-forward, a seventh waits until another closes. A Phase 3 Ingress with HTTP/2 removes the limit.

## Scale

Phase 1's target is 500 nodes and 15,000 pods (P1-18). CI measures it on every pull request with kwok in kind, on a 2-core runner that is also running the cluster: the first screen of nodes shows in about 130 ms, a sort takes about 30 ms (median), and the server uses about 300 MiB with every Phase 2 kind watched (against the simulator, whose pods carry more, about 360 MiB, and 450 MiB with the Pods tab open). The job fails if the first screen takes 2 seconds, a sort 200 ms, or the server 1 GiB. Against the simulator with the same numbers, the Pods tab shows its first rows 1.1 seconds after opening, a sort of 15,000 pods takes about 65 ms, a search narrows them in about 200 ms, and the page holds about 35 MB of JavaScript heap, because only the rows in view are rendered.

Phase 7's 5,000 nodes and 150,000 pods will need the second change the design allows for: sending each browser only the rows its screen shows, instead of every row of a view.

## Testing

| Level | What | Where |
| --- | --- | --- |
| Unit | Row building and problem detection for every view, Secret summaries and Helm decoding with masking, the store's views, subscriptions, lazy views and tick paths, the API including details, logs, YAML, the settings endpoint's checks and the cluster connection endpoints with their role checks, configuration, the settings service and its ConfigMap storage, the OIDC flow and every token refusal, local accounts, sessions, connection specs and credentials, the Secret store against the simulator, the connection test's steps and the manager; the table model, exports (Excel, CSV, JSON), search suggestions, the router with its cluster prefix, the live store, the grid's keyboard behaviour, credentials as the editor reads them, formatting and the Excel writer in the browser | `go test ./...`, `npm test` |
| Integration | The real client-go informers against the simulator: streaming lists, metadata-only lists, watch recovery, outages, metrics on and off, boot times and a reboot, the proxy refused, the caches holding only what the transforms keep | `server/internal/kube` |
| Browser, simulated | Every tab's behaviour: navigation and the More menu at every width, the Nodes tab with the label and taint picker, the Pods tab with its filters, details, logs and a pod deleted while open, the Namespaces tab against `kubectl get` counts, the Services tab with a pending LoadBalancer, the Helm tab against `helm list` and `helm history`, the YAML view, the planted Secret appearing nowhere, the same Columns, Export and search controls on every table, the Settings dialog, live updates, outage and recovery, sign-in and sign-out through the simulated provider with the audit records they write, a viewer's limits, a second simulated cluster added through the editor with a bad address and a wrong token refused at their steps, fixed, switched to with the tab and filters kept, tested from its page and removed with its Secret, accessibility (axe, WCAG 2.2 AA on every screen in both themes) and keyboard use, in Chromium, Firefox and WebKit | `make e2e-sim`, `web/e2e` |
| Browser, real cluster | The chart in kind on Kubernetes 1.35, 1.36 and 1.37 with metrics-server: install within 60 seconds with local accounts, permissions, port-forward, sign-in and the audit log, settings saved in the ConfigMap, boot times from every kubelet, the console's own Helm release shown with its values, the same browser tests with kubectl and helm making the changes (the kind cluster added a second time as another connection through its in-cluster address, with a real token and CA), an upgrade that keeps the settings, an upgrade to `secrets.read=false` checked against the API server and the console, and uninstall | `hack/e2e-kind.sh` |
| Scale | 500 nodes and 15,000 pods with kwok | `hack/scale-kind.sh`, `web/e2e/scale.spec.ts` |

## Decision records

Records 1 to 9 cover the Architecture table in the requirements; 10 to 17 were made while building Phase 1, 18 to 24 while building Phase 2 and 25 to 30 while building Phase 3. Proposed records are not decided yet; they are listed so the review can see where they fit.

### ADR-1: Go for the server

- **Status:** Accepted.
- **Context:** The server talks to Kubernetes all day, and later phases need Helm releases, kubectl's drain and describe logic, and Pod Security checks.
- **Decision:** Go, with client-go.
- **Consequences:** The libraries Kubernetes itself uses (client-go, the Helm SDK, kubectl's packages, the Pod Security admission library) are available as code rather than as shell-outs. Phases 1 and 2 use client-go and `component-helpers`; Helm's release records are decoded without the Helm SDK (ADR-20).

### ADR-2: TypeScript and React for the browser app

- **Status:** Accepted.
- **Context:** The prototype is a set of screens built from repeated parts: tables, cards, drawers, menus.
- **Decision:** TypeScript and React 19, built with Vite. No UI component library: the prototype's stylesheet is used as is (`web/src/styles/design.css`), so screens match it by construction.
- **Consequences:** The bundle is small. Matching the prototype is a matter of markup, not of overriding a library's styles.

### ADR-3: One image, with the browser app inside the Go binary

- **Status:** Accepted.
- **Context:** Two images would mean two things to version, scan and keep in step.
- **Decision:** The browser app is built first and embedded in the binary with `go:embed`; the image holds only that binary.
- **Consequences:** One version, one scan, one thing to deploy (P1-05). The app and the API always come from the same build, so their data shapes cannot drift apart in production.

### ADR-4: A Helm chart for deployment

- **Status:** Accepted.
- **Context:** Test and production installs should differ only in settings.
- **Decision:** A Helm chart in `deploy/helm/221b`, tested by `helm lint`, schema validation against Kubernetes 1.35 and 1.37, and real installs in kind.
- **Consequences:** `helm install`, `upgrade` and `uninstall` are the supported lifecycle; CI proves uninstall leaves nothing behind (P1-06). The chart requires `clusterName`, since the API server has no reliable name for its cluster.

### ADR-5: Informers on the server, Server-Sent Events to the browser

- **Status:** Accepted.
- **Context:** Changes must show within 5 seconds (X-07), and many people may watch the same cluster.
- **Decision:** One informer per resource type per cluster, shared by every viewer; deltas go to browsers over Server-Sent Events.
- **Alternatives:** Browser polling (slower and heavier on the API server); WebSockets (two-way, which nothing yet needs, and harder through proxies). SSE reconnects by itself and works through ordinary HTTP proxies.
- **Consequences:** The API server's load does not grow with viewers. Changes reach the screen on the next refresh tick (ADR-16), so within 3 seconds by default. Phase 2 kept the one stream per tab and made it carry several views (ADR-19).

### ADR-6: Rows are built on the server

- **Status:** Accepted.
- **Context:** Raw objects are large and can carry sensitive values.
- **Decision:** The server turns objects into rows; browsers receive only rows.
- **Consequences:** Secret handling (X-01) has one place to get right in every phase. Payloads are small, and the problem rules live in one tested place. Sorting and filtering stay in the browser, where they are instant at Phase 1 scale. The YAML view (ADR-22) is the one place a browser sees an object, and it passes through the same filter.

### ADR-7: In-cluster ServiceAccount with a read-only ClusterRole

- **Status:** Accepted; widened in Phase 2 by what its screens need.
- **Context:** The console shows the cluster it runs in and changes nothing.
- **Decision:** The chart creates a ServiceAccount bound to a ClusterRole with read verbs on exactly the kinds the tabs show. Phase 1 granted nodes, pods and node metrics; Phase 2 adds the 25 other kinds, pod logs and pod metrics, and Secrets behind the `secrets.read` setting (ADR-20).
- **Consequences:** No credentials to store. Every widening is listed in the chart and checked by CI in both Secret settings (X-02, P2-10). The uptime in the node details added `get` on `nodes/proxy` (ADR-17), and the shared settings a Role on one ConfigMap (ADR-16).

### ADR-8: Sign-in and per-person access by impersonation (Phase 3)

- **Status:** Accepted in Phase 3: sign-in, sessions, roles and the audit log are ADR-25 to ADR-28; per-person access by impersonation follows in the same phase.
- **Decision:** OIDC sign-in, and calls to Kubernetes made as the signed-in person through impersonation, so Kubernetes RBAC decides what each person sees and the audit log names them.

### ADR-9: The Claude API is called only from the server (Phase 5)

- **Status:** Proposed for Phase 5.
- **Proposal:** Sherlock's calls go from the server; the key lives in a Kubernetes Secret; browsers never hold the key or receive raw cluster data for prompts.
- **Impact so far:** None. The server makes no outbound calls except to the API server.

### ADR-10: Distroless static base image

- **Status:** Accepted.
- **Context:** The image must be small, non-root and clean of vulnerabilities (P1-05, X-05).
- **Decision:** `gcr.io/distroless/static-debian13:nonroot`, with a statically linked binary (`CGO_ENABLED=0`), built for amd64 and arm64 by cross-compiling.
- **Consequences:** No shell, so debugging uses `kubectl debug` with an ephemeral container. The scan has almost nothing to find besides the Go binary itself, which stays current by building with the latest Go patch release.

### ADR-11: A simulated API server for development and tests

- **Status:** Accepted; extended in Phase 2 to every kind the console watches.
- **Context:** Many behaviors (an API server outage, a node that stops reporting, 15,000 pods, a Helm release with a failed revision, a planted Secret) are slow or awkward to produce on a real cluster, and development should not need one.
- **Decision:** `221b-sim` implements just enough of the Kubernetes API (list and watch with resource versions, streaming lists, metadata-only lists, bookmarks, `410 Gone`, node and pod metrics, pod logs with follow) for client-go's real informers to run against it, with namespaces full of workloads, Services, Ingresses, ConfigMaps, Secrets and Helm release records, plus an admin API to make changes and outages.
- **Consequences:** Integration tests use the real client-go code paths in seconds, and the browser tests run without a cluster in every browser. The same tests run against kind with real Kubernetes, so the simulator cannot hide a difference for long.

### ADR-12: No table virtualization in Phase 1

- **Status:** Superseded by ADR-23 in Phase 2.
- **Context:** At 500 rows, the measured cost of rendering every row was small once off-screen rows skipped layout and paint.
- **Decision then:** Render every row, with `content-visibility: auto` on rows.
- **What changed:** The Pods tab holds 15,000 rows at Phase 1 scale, and rendering them all took seconds and hundreds of megabytes of browser memory. ADR-23 renders only the rows in view.

### ADR-13: Excel export written in the browser, without a library

- **Status:** Accepted; CSV added in Phase 2 (ADR-24).
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

### ADR-16: One refresh rate for everyone, kept on the server

- **Status:** Accepted.
- **Context:** The console should update at a rate people can choose, every open console should update at the same moment, and the choice must not multiply requests to the Kubernetes API with the number of browsers.
- **Decision:** The server publishes on a shared tick: it polls metrics-server, rebuilds the changed rows and sends every browser the same deltas and a `refresh` event. The rate is a shared setting, changed in the Settings dialog through `PUT /api/v1/settings` and kept in a ConfigMap in the console's namespace (the chart creates it empty and never writes its data, so upgrades keep it); the server re-reads it every minute and pushes changes to every browser through the stream. The theme stays per browser.
- **Alternatives:** A per-browser rate (browsers would disagree, and nothing would stop one from polling fast); a rate in `values.yaml` only (a change would need a `helm upgrade` and a restart); settings in the Deployment's environment (the server cannot write its own Deployment without far wider permissions).
- **Consequences:** The Kubernetes API sees one metrics read per tick and the watches, whatever the number of viewers; changes wait up to one tick (3 seconds by default, so still within X-07's 5 seconds) while connection news goes out at once. The console gained its first write, scoped by a Role to one named ConfigMap (X-03), and its first state-changing endpoint, protected against cross-site requests by taking JSON only and checking `Sec-Fetch-Site` and `Origin` (X-06). Anyone who can reach the console can change the rate, until Phase 3 brings sign-in.

### ADR-17: Node uptime from the kubelets' Summary API

- **Status:** Accepted; `kubeletStats` in the chart turns it off.
- **Context:** The node details should show how long a node has been up. Kubernetes keeps no boot time in the Node object, and metrics-server does not report one. A kubelet reports its node's boot time in its Summary API, which the API server proxies.
- **Decision:** Read `/api/v1/nodes/<name>/proxy/stats/summary` once per node and bootID, a few nodes at a time, and show the uptime from it. Grant `get` on `nodes/proxy` in the ClusterRole, behind `kubeletStats: true`.
- **Alternatives:** The Ready condition's transition time (restarts of the kubelet and brief outages reset it, so it is not uptime); node events such as `Rebooted` (expire after an hour); talking to the kubelets directly (blocked by the network policy and the kubelets' certificates).
- **Consequences:** A handful of requests per node over its whole life, none while the API server is down or a kubelet is not reporting. `get` on `nodes/proxy` is a wider grant than the rest of the role, since it reaches a kubelet's other read-only endpoints; it is what monitoring agents hold, and `--set kubeletStats=false` withholds it at the cost of the uptime, which the node details then say.

### ADR-18: A kind registry, with typed informers for what screens read and metadata-only informers for the rest

- **Status:** Accepted.
- **Context:** Phase 2 shows 27 kinds. The Namespaces tab lists every object of every kind, but for most kinds it needs only a name, an age and a count; reading whole Roles, Leases or ControllerRevisions into memory would cost memory for nothing and widen what the server holds.
- **Decision:** One registry (`kube.Kinds`) names every kind with its API group, resource and how it is cached. Kinds whose contents a screen shows run a typed informer with a transform that keeps the fields the screens use. Kinds listed by name only run a metadata informer: the API server is asked for `PartialObjectMetadata`, so their bodies never leave it. Every view, detail, index and the YAML view go through the registry, so adding a kind is one line and one transform.
- **Alternatives:** Dynamic (unstructured) informers for everything (every object held as a map, several times the memory of a typed struct, and no transforms); listing on demand instead of watching (slower screens, and a load on the API server that grows with viewers).
- **Consequences:** At 500 nodes and 15,000 pods with every kind watched the server stays at about 360 MiB, 450 MiB with the Pods tab open (X-09). The metadata-only kinds cost a few hundred bytes each. A new screen that needs a kind's contents promotes it to a typed informer with a transform, and the chart's ClusterRole lists the same registry.

### ADR-19: One live store with named views, built only while shown

- **Status:** Accepted.
- **Context:** Phase 1 had one table. Phase 2 has five tabs and one table per namespace, each with its own rows, and a browser shows one or two of them at a time. Building every view for every tick would cost most of the server's time on rows nobody is looking at, and sending every browser every view would multiply the stream's size.
- **Decision:** The store keeps named views (`nodes`, `pods`, `namespaces`, `services`, `helm`, `ns:<name>`), each with its own rows, version and change set. A browser subscribes to the views its screen shows, over its one stream, and gets a snapshot of those and deltas for those only. `pods` and the per-namespace views are lazy: built when the first browser subscribes, kept while any browser shows them, dropped a minute after the last leaves. Pod metrics are polled only while a view that shows them has a subscriber.
- **Alternatives:** One stream per view (more connections per tab, against the six-per-host limit through a port-forward); building every view always (simple, but the server's tick would scale with the number of namespaces rather than with what is shown); fetching per-namespace tables with plain requests instead of the stream (no live updates in the Namespaces tab).
- **Consequences:** The tick costs what the open tabs cost. A browser moving between tabs reopens its stream once with a new view list, which takes a snapshot of the new views (about a second for 15,000 pods). The counts in the tab bar come from the caches, not from views, so they are right even for views nobody shows. The metrics name views (`b221_rows{view}`, `b221_view_subscribers{view}`), so an operator can see what is being built.

### ADR-20: Secrets are read, reduced to keys and sizes as they enter the cache, and Helm release records are decoded with credentials masked

- **Status:** Accepted (open question 4 in the requirements, settled at the Phase 1 review); `secrets.read` in the chart turns it off.
- **Context:** The Namespaces tab lists Secrets by key name and size, and the Helm tab needs release records, which Helm 3 keeps in Secrets. X-01 forbids a Secret value from being shown, logged, exported or kept in the browser, and the safest way to meet that is for the server not to hold values either.
- **Decision:** The Secret informer's transform replaces each Secret with a summary (name, type, key names, value sizes) before it is stored, and decodes a Helm release record into the release's chart, status, history, notes and values, with every string under a credential-looking key and every PEM private key replaced by a marker, listing the keys it masked. The rendered manifest keeps only the kinds and names of the objects. Annotations that can carry a value (kubectl's last-applied configuration, and any annotation holding one of the values) are replaced by their size. The YAML view, which reads live, replaces every value with `<N bytes, hidden by 221B>` and hides the same annotations. `secrets.read=false` removes Secrets from the ClusterRole, starts no Secret informer and hides the Helm tab.
- **Alternatives:** Not reading Secrets at all (no Helm tab, and `kubectl get secrets` is how people check a Secret's keys); reading them on demand without caching (every list of a namespace would fetch its Secrets, with the values passing through the server each time); masking values by key name only (a value that is a PEM key under an innocent name would show).
- **Consequences:** The server's memory holds no Secret value after the transform, and the planted-value test (P2-07) checks every API response, the stream, the exports and the log. Masking is by key name and content, so a credential under an unusual key (`dbPass` is caught by `pass`; `connectionString` is not) can show in the Helm values; the masked keys are listed in the details so the gap is visible, and the pattern is one regular expression to extend. With Secrets off, the Helm tab is gone rather than empty, since the console cannot know the releases exist.

### ADR-21: Pod logs stream through the server, batched, with a cap on open streams

- **Status:** Accepted.
- **Context:** The pod details show logs live (P2-04). A browser cannot read the API server's `pods/log` endpoint itself (no credentials, and the network policy), and a busy pod can log thousands of lines a second.
- **Decision:** `GET /api/v1/pods/{ns}/{name}/log` proxies `kubectl logs`' options (container, previous, tail, timestamps), as one response for a download or as Server-Sent Events with `follow=1`, where lines are batched into events of up to 500 lines every 200 ms. The server allows 50 open log streams at once and splits lines over 1 MiB. The stream ends when the browser closes it, the pod goes, or the API server ends it, with an `end` event that says why.
- **Alternatives:** WebSockets (no advantage for a one-way stream); polling with `sinceTime` (gaps and duplicates); keeping log history on the server (X-16 says logs are read live, and it would be the first store of workload data).
- **Consequences:** Logs start within a second of opening the tab and cost one request on the API server per open log, which the cap bounds. Each open log holds one browser connection, which counts against the six-per-host limit through a port-forward.

### ADR-22: The YAML view reads the object live and filters it on the way out

- **Status:** Accepted.
- **Context:** The YAML view must match `kubectl get -o yaml` apart from managed fields and Secret values (P2-08). The caches hold trimmed objects (ADR-18), so they cannot serve it.
- **Decision:** `GET /api/v1/yaml/{kind}/…` fetches the one object from the API server when asked, removes `managedFields`, replaces a Secret's `data` and `stringData` values with their sizes, and renders it as YAML in the field order `kubectl` uses. Metadata-only kinds are fetched in full for the view, since the request is for one object.
- **Alternatives:** Serving the cached object (incomplete by design); caching full objects for the view (the memory ADR-18 saves).
- **Consequences:** One `get` per view opened, which the ClusterRole's `get` verb already covers. The YAML is as fresh as the request, and the only transformation is the two the requirement names; the browser tests compare it with `kubectl get -o yaml`.

### ADR-23: One table component, an ARIA grid that renders only the rows in view

- **Status:** Accepted; supersedes ADR-12.
- **Context:** Every tab shows a table, several of them inside one screen, and the Pods tab holds 15,000 rows at Phase 1 scale. The tables must sort, filter, pick columns and export the same way everywhere (X-13), and work by keyboard and screen reader (X-12). Rows now hold links (a pod's namespace, node, Service and release), and a row that is itself a button must not contain other controls.
- **Decision:** `DataTable` renders every table: an ARIA `grid` with a header row of `columnheader`s carrying `aria-sort`, rows that are focusable `row`s with `aria-selected` and the first cell as `rowheader`, Enter opening a row, the arrow keys moving between rows and into a row's links, and Home and End jumping to the ends. Tables above 100 rows render only the rows in view plus a margin, with spacers keeping the scroll height, so 15,000 pods cost what a screenful does; the grid carries `aria-rowcount` and each row `aria-rowindex`, so assistive technology knows the table's true size. `useTable` holds the model every tab shares: sort, search, hidden columns (remembered per table in `localStorage`) and the rows on screen, which is what the exports take.
- **Alternatives:** A table library (the prototype's markup and styles would be replaced by the library's, and the grid and virtualization each come with opinions); rendering every row with `content-visibility` (ADR-12; too slow and too much memory at 15,000 rows).
- **Consequences:** The scale test's first screen of nodes shows in 170 ms and the Pods tab's first rows in about a second with 15,000 pods. Browser find and a screen reader's table navigation see only the rendered rows; the search box and the grid's own navigation cover the rest. Tests count a table's rows through its `data-total` rather than its DOM.

### ADR-24: Search suggestions, CSV export and the same controls on every table

- **Status:** Accepted (asked for at the Phase 1 review).
- **Context:** The Phase 1 search box took free text and the Export menu offered Excel and JSON. The review asked for the search to fill in as you type, for CSV, and for every table's Columns and Export controls to be the same.
- **Decision:** Each table's columns declare the *terms* a row can be found by (its name, labels, taints, node, namespace, status, chart…), and the search box suggests the terms that match what is typed, from the rows on screen, as a listbox the keyboard and mouse can pick from; a picked term is added as a word, so several can be combined. Export offers Excel, CSV (UTF-8 with a byte-order mark and CRLF line ends, so Excel opens it directly) and JSON, of exactly the rows and columns on screen, from one `ExportMenu` and one `ColumnsMenu` used by every table.
- **Consequences:** The same filter, column and export behaviour on every tab, with one set of tests. Suggestions come from what is loaded, so they are instant and never ask the server.

### ADR-25: OIDC sign-in without a client library, and sessions in the server's memory

- **Status:** Accepted (Phase 3).
- **Context:** P3-01 asks for sign-in with any OIDC provider and sessions in Secure, HttpOnly, SameSite cookies with an idle expiry. The usual Go library brings a JOSE dependency the module cannot build offline, and the part of OIDC a relying party needs is small.
- **Decision:** The server implements the relying party itself on the standard library and `golang.org/x/oauth2`, which client-go already brings: discovery, the authorization code flow with PKCE (S256) and a nonce, a JWKS cache that refetches on an unknown key, and ID token verification that accepts only asymmetric signatures (RS, PS and ES families), checks issuer, audience, expiry, issue time and nonce, and reads the username and groups from configurable claims. Sessions are random identifiers kept in memory with their user, groups and role, swept every minute; the cookie carries only the identifier.
- **Consequences:** No new dependency and a small, readable verifier with tests for every refusal (another key, wrong issuer or audience, expired, wrong nonce, `alg: none`, a tampered payload). A restart signs everyone out, which a sign-in puts right in seconds; the chart runs one replica. The sign-in page is server-rendered, so no app code loads before sign-in, and it starts the OIDC flow from a link because Chrome applies the page's `form-action` policy to a form's redirects.

### ADR-26: Two console roles, from the provider's claims

- **Status:** Accepted (Phase 3).
- **Context:** P3-03 asks for admins who manage connections and settings, and everyone else.
- **Decision:** A person is an administrator when their username or subject is in `auth.admins` or one of their groups in `auth.adminGroups`, decided at sign-in and kept in the session. The settings endpoint checks the role; the browser disables what a viewer cannot do and says why. With sign-in off everyone is an anonymous administrator.
- **Consequences:** Roles follow the provider: moving someone out of the admin group takes effect at their next sign-in, within the idle lifetime at most. What a person may see in a cluster is not a console role but Kubernetes RBAC, applied by impersonation (ADR-8).

### ADR-27: An audit log on standard output, and a webhook

- **Status:** Accepted (Phase 3).
- **Context:** P3-10 asks for a record of sign-ins and connection changes (and later Sherlock questions and Watson commands) naming who, what, when and which cluster: JSON lines on standard output, plus an optional webhook.
- **Decision:** An `audit` package writes one JSON object per event to standard output, under a mutex, while the ordinary structured logs stay on standard error. A webhook receives the same events from a bounded queue, three tries each with back-off, and drops with a warning and a metric rather than hold up a request. Records name the person (subject, email, name), the action, target, detail, outcome, time and source address, and never a credential.
- **Consequences:** `kubectl logs` and any log pipeline see the records; a SIEM gets them pushed. Later phases add their actions to the same stream.

### ADR-28: Local accounts for evaluations, and a simulated provider for the tests

- **Status:** Accepted (Phase 3).
- **Context:** Reviews and evaluations happen on clusters without an identity provider, and CI must exercise the OIDC flow without one either.
- **Decision:** `auth.mode: local` keeps accounts in the chart's values (or an existing Secret), with passwords as PBKDF2-SHA256 hashes (600,000 iterations, from the standard library) that `221b hash-password` prints; the server never sees or stores a plain password. The simulator serves a small OIDC provider at `/oidc` (discovery, keys, a sign-in form with fixed accounts, the code flow with PKCE, RS256 ID tokens), which the browser tests sign in through, and which the Go tests use in-process; the kind tests install the chart with local accounts.
- **Consequences:** A console can be reviewed with nothing but the chart; production is expected to use a provider. The simulated provider is not for anything but tests, and says so on its page.

### ADR-29: Connections to other clusters are Secrets in the console's namespace, run like the cluster it lives in

- **Status:** Accepted (Phase 3).
- **Context:** P3-07 asks for credentials stored in Kubernetes Secrets in 221B's namespace that never return to the browser; P3-09 for one installation that connects directly to other clusters' API servers, with the cluster it runs in as one connection among them.
- **Decision:** Each connection is one Secret, `<release>-cluster-<id>`, labelled as a cluster connection, holding the spec as JSON and the credential under its own keys (`token`, `kubeconfig` and `context`, `tls.crt` and `tls.key`, `ca.crt`). The chart's Role grants the console `get`, `list`, `create`, `update` and `delete` on Secrets in its namespace, and nothing on Secrets elsewhere beyond what `secrets.read` already allows; the console should therefore have a namespace to itself. A manager starts the same `kube.Cluster` and live store for every connection as for the local one, with its own `rest.Config` built from the spec and credential, and registers the cluster the server was started against as the connection `local`, whose credentials are the console's own and cannot be replaced, only its name, environment and description. The API serves every data route under `/api/v1/clusters/{id}/` as well as unprefixed for `local`, and the browser keeps one event stream per cluster, opening the cluster's as it is switched to. A credential is read once, when saved, for what it says about itself (account, expiry, context, subject), and only that summary is ever served; a draft's empty credential fields mean "keep the saved one". Removing a connection deletes its Secret. On a laptop, or with `connections.store: memory`, the same manager keeps connections in memory.
- **Consequences:** No database and nothing new to back up: the Secrets go where the namespace goes, survive upgrades (the chart does not own them) and are readable to whoever can read Secrets in that namespace, which is the usual Kubernetes stance on credentials. Every connection costs what the local cluster costs in memory, so the limits scale with the clusters added; Phase 7's scale work applies per connection. OIDC as a cluster sign-in method is stored but refused until the renewal flow is built; tokens, kubeconfigs and certificates cover the clusters at hand.

### ADR-30: The connection test runs on the server, step by step, against the real address

- **Status:** Accepted (Phase 3).
- **Context:** P3-06 asks for the prototype's five steps (address, TLS, sign-in, Kubernetes, permissions), each failure reported at its step with its cause.
- **Decision:** `POST /api/v1/clusters/test` builds the connection from the draft (or the saved credential for fields left empty) and runs the steps in order on the server, where the console will connect from: a TCP dial of the address, a TLS handshake checked the way the connection will check it (pasted CA, system trust store, or not at all, which is reported as a warning), `/version` with the credential (a 401 or 403 is the sign-in step's failure) and a `SelfSubjectReview` for who the account is, the version and node count, and `SelfSubjectAccessReview`s for every verb the console will use, for impersonation of users and groups, and in read-and-change mode for the writes later phases make. Steps after a failure are reported as not reached. The browser reveals the steps one by one and keeps the last test of a saved connection on its page.
- **Consequences:** The test says what the connection will do, not what the browser's network allows, which is what matters when the console runs in a cluster and the person's laptop does not. A failed test does not block saving: a cluster may be down for a while, and the page then shows why with the way to fix it.
