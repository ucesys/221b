Bottlerocket nodes:
- No shell or package manager on the host and a read-only root: settings change through the API (apiclient), never files.
- Checks: open the admin container (through SSM), sudo sheltie, then journalctl -u kubelet, journalctl -u containerd, journalctl -k.
