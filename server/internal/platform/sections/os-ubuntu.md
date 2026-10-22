Ubuntu nodes:
- cgroup v2 (21.10 and later). A container OOMKilled below its own limit means the node ran out of memory.
- unattended-upgrades installs updates, and needrestart may restart services after them, including containerd; /var/run/reboot-required marks a pending kernel.
- systemd-resolved puts 127.0.0.53 in /etc/resolv.conf: the kubelet must use /run/systemd/resolve/resolv.conf, or CoreDNS forwards to itself and loops.
- AppArmor confines containers: "permission denied" with an apparmor="DENIED" line in the kernel log names the profile.
- Checks: journalctl -k -b, df -h and df -i, free -m, /var/log/apt/history.log.
