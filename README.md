# 221B

A console for Kubernetes clusters with two assistants built in:

- **Sherlock** answers questions about the cluster: why a pod is pending, what blocks a drain, who can read Secrets.
- **Watson** runs `kubectl` and `helm` commands, and only after you approve each change. It shows their output the way a terminal would.

## Status

This repository holds the **design prototype**. Everything in it runs on sample data; nothing connects to a real cluster yet. The prototype is the specification for the real build, which is planned in seven phases in [docs/requirements.md](docs/requirements.md).

## What the design covers

- **Clusters.** A cluster menu in the top bar switches between clusters and edits their connections: API server, CA certificate, sign-in (service account token, kubeconfig, OIDC or client certificate), and whether the console may only read or also change things. Connections can be tested, added and removed.
- **Tabs.** Nodes, Fleets, Namespaces, Pods, Helm and Alerts stay in the bar. Services, Networks, Access and Policies sit under **More**, which takes more tabs as the window narrows.
- **Sherlock and Watson** share a panel at the bottom of every tab. It can be resized, maximized or minimized to an icon.
- **Fleets** group nodes by their labels and taints, with one column per label and per taint.
- **Networks** traces a request from a hostname to the pod that answers it.
- **Access** reviews RBAC: who can do what, and each identity's effective permissions.
- **Policies** covers network policies (with a "can A connect to B" checker), disruption budgets and whether each node can be drained, quotas, Pod Security, admission webhooks and priority classes.
- **Every table** sorts by any column and exports to Excel.

The sample cluster, `lakehouse-prod`, runs Dremio, Nessie, MinIO, Superset, Longhorn, kube-prometheus-stack, cert-manager and ingress-nginx on 10 nodes, with a few realistic problems to find. Two more sample clusters appear in the cluster menu without data.

## Layout

| Path | What it is |
|---|---|
| `design/Main.dc.html` | The prototype: HTML template, logic and sample data in one file |
| `design/canvas.json` | Canvas settings for the design |
| `docs/requirements.md` | Requirements and the seven build phases, each ending in a review |
| `docs/images/` | The roadmap and architecture diagrams used by the requirements |

`Main.dc.html` is a Claude Design artboard. Its template uses `{{holes}}`, `sc-if` and `sc-for`, and a single component class computes everything the template shows. It renders in the Claude Design canvas.

## Planned build

- **Server:** Go, using client-go, Helm's Go SDK, kubectl's drain and describe packages, and the Pod Security admission library.
- **Browser:** TypeScript and React.
- **Sherlock:** the Claude API, called from the server so keys and cluster data stay off the browser.
- **Hosting:** inside the cluster it manages, or in a management cluster that connects to other clusters directly. An agent for clusters it cannot reach is an open question.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
