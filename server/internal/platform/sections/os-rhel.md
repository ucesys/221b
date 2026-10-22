Red Hat-like nodes (RHEL, Rocky, Alma, CentOS Stream, Oracle Linux):
- cgroup v1 by default on version 8, v2 on 9: memory accounting differs between them, so the same pod can be OOMKilled on one and not the other.
- SELinux is usually enforcing: "permission denied" on a hostPath or local volume points at its labels (ausearch -m avc -ts recent).
- firewalld and NetworkManager are on by default and can break pod networking (see the network plugin above); nm-cloud-setup must be off for RKE2 and K3s.
- Checks: journalctl -k -b, df -h and df -i, free -m, dnf history (kernel or package changes), needs-restarting -r, getenforce, systemctl status firewalld NetworkManager.
