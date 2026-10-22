Debian nodes:
- cgroup v2 (Debian 11 and later): a container OOMKilled below its own limit means the node ran out of memory; look for SystemOOM events and the kernel log.
- A -cloud kernel is built for virtual machines and leaves out many hardware drivers and sensors: on bare metal, hardware errors may go unreported, so check the server's management controller.
- Checks: journalctl -k -b (and -b -1 for the boot before a reboot), df -h and df -i, free -m, /var/log/apt/history.log and /var/log/unattended-upgrades/ for kernel or package changes before the trouble.
