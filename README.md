# 221B

A console for Kubernetes clusters with two assistants built in:

- **Sherlock** answers questions about the cluster: why a pod is pending, what blocks a drain, who can read Secrets.
- **Watson** runs `kubectl` and `helm` commands, and only after you approve each change. It shows their output the way a terminal would.

## Status

221B is built in seven phases, each ending in a review; see [docs/requirements.md](docs/requirements.md). **Phase 1** brought a Go server and a React app in one image, a Helm chart, and the **Nodes tab** working live against a real cluster, read-only. **Phase 2** adds the everyday read-only screens: **Namespaces** (what each one holds, by kind), **Pods** (with filters by node, namespace and status, and details with containers, environment, volumes, events and live logs), **Services** (with the External columns and the endpoints behind each one) and **Helm** (releases with their history, notes and values), details and a YAML view of every object, links between the tabs, and label and taint filters on Nodes. **Phase 3** made it safe to share: sign-in, what each person sees decided by Kubernetes RBAC, several clusters in one installation, and an audit log. **Phase 4** adds the analysis tabs: **Fleets** (nodes grouped by labels and taints), **Alerts** (pods failing to start or pending, node problems and warning events, by object), **Networks** (each Ingress route traced hop by hop to the pod that answers), **Access** (who can do what, with risky roles and bindings flagged) and **Policies** (network policies with a connection checker, which nodes can be drained, quotas, Pod Security, admission webhooks and priority classes), and a **home page** with every cluster's health and use at a glance. **Phase 5** adds **Sherlock**, which answers questions about a cluster from its live state through the Claude API, reading only what the person asking may read, never sending Secret values, and changing nothing; it docks at the bottom of every tab or moves to a window of its own, and an administrator turns it on per cluster with an API key and spending caps. **Phase 6** adds **Watson**, beside Sherlock: it runs kubectl and helm reads at once, printing what the CLIs print, and holds every change (cordon, drain, scale, restarts and rollbacks, pod deletes, labels, Helm rollbacks) on an approval card with the objects it touches, a server-side dry run and its impact, until someone approves it; the change then runs as the approver, never through a shell, read-only clusters refuse it, production clusters can require a second person, and everything is audited. How it fits together, and why, is in [docs/architecture.md](docs/architecture.md); the review guide for Phase 6 is [docs/phase-6-review.md](docs/phase-6-review.md).

The design prototype in `design/` remains the specification for every screen, including the ones later phases build.

## Try it on a cluster

The chart installs 221B in the cluster it shows, and from there it can show other clusters too. By default it is reached with `kubectl port-forward` rather than exposed.

```sh
kubectl create namespace 221b
kubectl label namespace 221b pod-security.kubernetes.io/enforce=restricted   # optional; 221B passes it

# The image is private while the repository is: give the cluster a token that can read packages.
kubectl -n 221b create secret docker-registry ghcr \
  --docker-server=ghcr.io --docker-username=<github-user> --docker-password=<token with read:packages>

# An account to sign in with: the console keeps its own (auth.mode=local), with a password hash it makes for you.
HASH=$(kubectl run hash --rm -i --quiet --restart=Never --image=ghcr.io/ucesys/221b:main --overrides='{"spec":{"imagePullSecrets":[{"name":"ghcr"}]}}' -- hash-password <<< 'a password of your choosing')

helm install console deploy/helm/221b -n 221b \
  --set clusterName=staging \
  --set image.tag=main \
  --set 'imagePullSecrets[0].name=ghcr' \
  --set 'auth.local.users[0].email=you@example.com' --set "auth.local.users[0].passwordHash=$HASH" \
  --set 'auth.admins[0]=you@example.com'

kubectl -n 221b port-forward svc/console-221b 8080:8081
```

Then open <http://localhost:8080> and sign in. `clusterName` is the name shown in the top bar. Images are tagged `main` and `sha-<commit>` from the main branch, and `pr-<number>` for each pull request. `main` and `pr-<number>` move to every new build: with one of those the chart pulls the image on every pod start and restarts the console on every `helm upgrade`, so an upgrade always runs the latest build; `kubectl -n 221b logs deploy/console-221b | grep -m1 serving` shows which commit is running. To pin one build, set `image.tag=sha-<commit>` or `image.digest=sha256:<digest>`.

**Sign-in** is on from Phase 3: every page and API call needs a signed-in person (the health probes and `/metrics` excepted). `auth.mode=oidc` sends people to your OpenID Connect provider (Okta, Microsoft Entra ID, Google, Keycloak, Dex, …): set `auth.oidc.issuer`, `clientID` and `clientSecret` (or `existingSecret`), register `https://<the console's host>/signin/callback` with the provider, and add `groups` to `auth.oidc.scopes` if it needs that to send groups. `auth.mode=local` (the default) keeps accounts in the chart's values with password hashes, which `221b hash-password` prints, for evaluations and clusters without a provider. Administrators (`auth.admins`, `auth.adminGroups`) change the settings and manage cluster connections; everyone else uses the tabs. Sessions end after 8 hours without a request (`auth.sessionIdle`). Every sign-in, sign-out and change is in the audit log, one JSON line on the console's standard output (`kubectl -n 221b logs deploy/console-221b | grep '"action"'`), and goes to a webhook if `audit.webhook.url` is set. `auth.mode=none` turns sign-in off, as the console was before Phase 3; everyone who reaches it is then an administrator.

**What each person sees** is what Kubernetes lets them see: the console reads logs and YAML as them (by impersonation) and, before a row reaches them, asks Kubernetes whether they may list that kind in that namespace, so write RBAC for your people as usual (`kubectl create rolebinding ... --clusterrole=view --user=alice@corp.example`) and the console follows it, within `auth.kubernetes.accessTTL` (2 minutes) of a change. If your API servers add a prefix to OIDC names (`--oidc-username-prefix`, `--oidc-groups-prefix`), set `auth.kubernetes.userPrefix` and `groupPrefix` to the same. A person with no RBAC at all sees empty tabs; the console's own account reads everything, and grants only what the tabs show.

**Other clusters** are added in the console, from the cluster menu in the top bar (administrators only): the cluster's API server address, its CA certificate (or the image's trust store), and how the console signs in there, with a ServiceAccount token, a kubeconfig context or a client certificate. The editor shows the RBAC to grant that account and tests the connection step by step (address, TLS, sign-in, Kubernetes, permissions) before saving. Each connection is kept as a Secret in the console's namespace (`kubectl -n 221b get secrets -l app.kubernetes.io/component=cluster-connection`), the credential never comes back out, and removing the cluster deletes it. The cluster the console runs in is the connection named `local`, with its own credentials. A token that expires within a week is flagged in the menu and on the cluster page. Give the console a namespace of its own: the Role that lets it keep connections covers every Secret in that namespace.

**To put it on the network** instead, add `--set service.type=LoadBalancer`. With a load balancer controller (MetalLB, kube-vip, a cloud provider) the Service gets an address that answers on port 8081; `kubectl -n 221b get svc console-221b` shows it. Without one, every node still answers on the node port shown there, and `--set 'service.externalIPs={10.0.0.21,10.0.0.22}'` with your nodes' addresses makes them answer on 8081 as well. The network policy lets anyone reach an exposed Service unless `networkPolicy.ingressFrom` lists who may. For TLS, publish it through an Ingress: `--set ingress.enabled=true --set ingress.host=console.example.com --set ingress.tls.enabled=true --set ingress.tls.secretName=<certificate Secret>`; plain HTTP is then redirected and the session cookie marked Secure. Without TLS, sessions travel in the clear.

To upgrade after a `git pull`, keep your settings and take the chart's new defaults: `helm upgrade console deploy/helm/221b -n 221b --reset-then-reuse-values` (plain `--reuse-values` keeps the old chart's defaults too, so new ones never apply). To see what it may read: `kubectl auth can-i --list --as=system:serviceaccount:221b:console-221b`. To remove it: `helm uninstall console -n 221b`.

CPU and memory in use come from [metrics-server](https://github.com/kubernetes-sigs/metrics-server) when the cluster has it; without it, the tabs say so and show everything else. Each node's uptime comes from its kubelet, read through the API server once per boot; that needs `get` on `nodes/proxy`, which the chart grants unless you set `kubeletStats=false`.

**Secrets** are read by default (`secrets.read`), so that each namespace lists its Secrets by key name and size and the Helm tab can read release records, which Helm keeps in Secrets. The server keeps only key names and sizes, the names, issuer and validity of a TLS Secret's certificate (which the Networks tab checks; `tls.key` is never read), decodes release records with credential-looking values masked, and never shows, logs or exports a Secret's value; CI plants a known value and checks it appears nowhere. `--set secrets.read=false` grants the console nothing on Secrets at all: namespaces then list no Secrets and there is no Helm tab.

**Watson** runs reads in any cluster. It changes the cluster the console runs in only when installed with `--set localCluster.mode=write`, and another cluster only when its connection is set to *Read and change*. With sign-in on, each change runs as the person who approves it, so their own RBAC decides; with `auth.mode=none`, changes run as the console's ServiceAccount, which the chart then binds to the built-in `edit` ClusterRole (`localCluster.writeClusterRole`) and a small role to cordon and drain nodes.

**Every table** sorts by any column, picks columns and exports what is on screen to Excel, CSV or JSON, with the same controls everywhere. The search box suggests names, labels, nodes, namespaces and statuses as you type. Rows are keyboard-navigable: Enter opens a row's details, the arrow keys move between rows and into a row's links, and Escape closes the details.

**Settings** (the gear in the top bar) hold the theme, for your browser, and the refresh rate, for everyone: the server reads the cluster and updates every open console at the same moment, 3 seconds apart by default, and keeps the choice in its ConfigMap (`console-221b`) so it survives restarts and upgrades. Nodes and pods are watched, so the rate adds no requests to the API server for them.

## Develop

You need Go 1.26, Node.js 24 and make. Docker, kind, kubectl and helm are needed only for the image and the kind tests.

```sh
make dev-sim     # the console on http://127.0.0.1:8080 against a simulated cluster, with a second one to add (http://127.0.0.1:7444)
make test        # Go and browser unit tests
make lint        # gofmt, go vet, tsc and eslint
make e2e-sim     # browser end-to-end tests against the simulator (Chromium; set E2E_BROWSERS for more)
make image       # the container image
make help        # everything else
```

**Against a real cluster from a laptop**, with your own kubeconfig and permissions:

```sh
make build
bin/221b -kubeconfig ~/.kube/config -context my-cluster -cluster-name my-cluster
```

**The browser app with hot reload:** run `make dev-sim` in one terminal and `npm run dev` in `web/` in another, then open <http://localhost:5173>; Vite sends API calls to the server on port 8080.

**The simulator** (`bin/221b-sim`) serves enough of the Kubernetes API for the server's real informers: every kind the console watches, with namespaces full of workloads, Services, Ingresses, ConfigMaps, Secrets and Helm release records, pod logs and metrics, plus the kubelets' boot times through the node proxy and a ConfigMap for the settings. It takes `-nodes`, `-pods` and `-churn`, and `POST /sim/...` changes things: cordon a node, make it not ready, reboot it, crash a pod, delete a pod, give a Service an external IP, take the API server down. See `server/internal/sim`.

**The kind tests** that CI runs: `IMAGE=ghcr.io/ucesys/221b:dev hack/e2e-kind.sh 1.37` after `make image TAG=dev`. `hack/scale-kind.sh` runs the 500-node, 15,000-pod scale test with kwok.

## Layout

| Path | What it is |
|---|---|
| `server/` | The Go server: `cmd/221b` (the console) and `cmd/221b-sim` (the simulated API server), with packages under `internal/` |
| `web/` | The browser app (TypeScript, React, Vite), with unit tests next to the code and browser tests in `e2e/` |
| `deploy/helm/221b` | The Helm chart |
| `hack/` | Scripts CI runs: chart checks, the kind end-to-end run, the RBAC check (with and without Secrets) and the scale test |
| `design/` | The prototype: `Main.dc.html` is a Claude Design artboard with the template, logic and sample data in one file |
| `docs/` | Requirements, architecture and decision records, and their images |

## What the design covers

- **Clusters.** A cluster menu in the top bar switches between clusters and edits their connections: API server, CA certificate, sign-in (service account token, kubeconfig, OIDC or client certificate), and whether the console may only read or also change things. Connections can be tested, added and removed.
- **Tabs.** Nodes, Fleets, Namespaces, Pods, Helm and Alerts stay in the bar. Services, Networks, Access and Policies sit under **More**, which takes more tabs as the window narrows.
- **Sherlock and Watson** share a panel at the bottom of every tab. It can be resized, maximized or minimized to an icon.
- **Fleets** group nodes by their labels and taints, with one column per label and per taint.
- **Networks** traces a request from a hostname to the pod that answers it.
- **Access** reviews RBAC: who can do what, and each identity's effective permissions.
- **Policies** covers network policies (with a "can A connect to B" checker), disruption budgets and whether each node can be drained, quotas, Pod Security, admission webhooks and priority classes.
- **Every table** sorts by any column and exports to Excel, CSV or JSON.

The prototype's sample cluster, `lakehouse-prod`, runs Dremio, Nessie, MinIO, Superset, Longhorn, kube-prometheus-stack, cert-manager and ingress-nginx on 10 nodes, with a few realistic problems to find.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
