Container-Optimized OS nodes:
- Read-only root and no package manager; GKE updates the node image. toolbox gives a shell with debugging tools.
- Checks: sudo journalctl -u kubelet, sudo journalctl -u containerd, sudo journalctl -k, df -h.
