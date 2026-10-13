# 221B

A console for Kubernetes clusters with two assistants built in:

- **Sherlock** answers questions about the cluster: why a pod is pending, what blocks a drain, who can read Secrets.
- **Watson** runs `kubectl` and `helm` commands, and only after you approve each change. It shows their output the way a terminal would.

## Status

221B is built in seven phases, each ending in a review; see [docs/requirements.md](docs/requirements.md). **Phase 1** is here: a Go server and a React app in one image, a Helm chart, and the **Nodes tab** working live against a real cluster, read-only. How it fits together, and why, is in [docs/architecture.md](docs/architecture.md).

The design prototype in `design/` remains the specification for every screen, including the ones later phases build.

## Try it on a cluster

The chart installs 221B in the cluster it shows. Phase 1 has no sign-in, so it is reached with `kubectl port-forward` rather than an Ingress.

```sh
kubectl create namespace 221b
kubectl label namespace 221b pod-security.kubernetes.io/enforce=restricted   # optional; 221B passes it

# The image is private while the repository is: give the cluster a token that can read packages.
kubectl -n 221b create secret docker-registry ghcr \
  --docker-server=ghcr.io --docker-username=<github-user> --docker-password=<token with read:packages>

helm install console deploy/helm/221b -n 221b \
  --set clusterName=staging \
  --set image.tag=main \
  --set 'imagePullSecrets[0].name=ghcr'

kubectl -n 221b port-forward svc/console-221b 8080:80
```

Then open <http://localhost:8080>. `clusterName` is the name shown in the top bar. Images are tagged `main` and `sha-<commit>` from the main branch, and `pr-<number>` for each pull request.

To see what it may read: `kubectl auth can-i --list --as=system:serviceaccount:221b:console-221b`. To remove it: `helm uninstall console -n 221b`.

CPU and memory in use come from [metrics-server](https://github.com/kubernetes-sigs/metrics-server) when the cluster has it; without it, the tab says so and shows everything else.

## Develop

You need Go 1.26, Node.js 24 and make. Docker, kind, kubectl and helm are needed only for the image and the kind tests.

```sh
make dev-sim     # the console on http://127.0.0.1:8080 against a simulated cluster
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

**The simulator** (`bin/221b-sim`) serves enough of the Kubernetes API for the server's real informers. It takes `-nodes`, `-pods` and `-churn`, and `POST /sim/...` changes things: cordon a node, make it not ready, crash a pod, take the API server down. See `server/internal/sim`.

**The kind tests** that CI runs: `IMAGE=ghcr.io/ucesys/221b:dev hack/e2e-kind.sh 1.37` after `make image TAG=dev`. `hack/scale-kind.sh` runs the 500-node, 15,000-pod scale test with kwok.

## Layout

| Path | What it is |
|---|---|
| `server/` | The Go server: `cmd/221b` (the console) and `cmd/221b-sim` (the simulated API server), with packages under `internal/` |
| `web/` | The browser app (TypeScript, React, Vite), with unit tests next to the code and browser tests in `e2e/` |
| `deploy/helm/221b` | The Helm chart |
| `hack/` | Scripts CI runs: chart checks, the kind end-to-end run, the RBAC check and the scale test |
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
- **Every table** sorts by any column and exports to Excel.

The prototype's sample cluster, `lakehouse-prod`, runs Dremio, Nessie, MinIO, Superset, Longhorn, kube-prometheus-stack, cert-manager and ingress-nginx on 10 nodes, with a few realistic problems to find.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
