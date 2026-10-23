kubeadm:
- The control plane runs as static pods in kube-system (etcd-<node>, kube-apiserver-<node>, …) from /etc/kubernetes/manifests on the control-plane nodes; the kubelet is the systemd service kubelet, set up from /var/lib/kubelet/config.yaml.
- Certificates last a year and are renewed by kubeadm upgrade or kubeadm certs renew; kubeadm certs check-expiration lists them. x509 "expired" or "not yet valid" means an expired certificate or clock skew.
- etcd keeps its data in /var/lib/etcd: a slow disk there slows the whole API ("apply request took too long" in the etcd pod's log).
- Checks on a node: journalctl -u kubelet, journalctl -u containerd, crictl ps -a, kubeadm certs check-expiration on a control-plane node.
