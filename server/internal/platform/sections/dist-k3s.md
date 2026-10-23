K3s:
- One k3s process per node runs the kubelet and containerd, and on servers the API server, scheduler, controller manager and datastore too: there are no control-plane pods. Their logs are in journalctl -u k3s on servers and -u k3s-agent on agents.
- The datastore is SQLite on a single server, embedded etcd with several servers, or an external database. A slow disk under it slows the whole API.
- Bundled components (CoreDNS, Traefik, ServiceLB, local-path-provisioner, metrics-server) come from manifests in /var/lib/rancher/k3s/server/manifests on the servers. Traefik is a HelmChart: change it with a HelmChartConfig in kube-system, never with helm upgrade.
- Certificates last a year and are renewed when k3s restarts close to expiry. x509 "expired" or "not yet valid" means an expired certificate or clock skew.
- Ports: 6443 for the API and nodes joining, 2379-2380 for embedded etcd, 10250 for the kubelet, UDP 8472 for Flannel.
- Node files: /etc/rancher/k3s/config.yaml, /etc/rancher/k3s/registries.yaml (registry mirrors and credentials).
- Checks on a node: journalctl -u k3s (or -u k3s-agent); /var/lib/rancher/k3s/agent/containerd/containerd.log; k3s crictl ps -a; on a server with etcd, k3s etcd-snapshot ls.
