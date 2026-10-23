Amazon Linux 2 nodes:
- cgroup v1; the kubelet is the systemd service kubelet, set up by /etc/eks/bootstrap.sh. Amazon Linux 2 has reached end of support: say so when it matters, and suggest Amazon Linux 2023 or Bottlerocket.
- Checks: journalctl -u kubelet, journalctl -u containerd, dmesg -T, df -h and df -i, free -m.
