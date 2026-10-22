SUSE nodes (SLES, SLE Micro, openSUSE):
- SLE Micro and openSUSE MicroOS have a read-only root changed through transactional-update: a change takes effect only after a reboot, and a node may reboot to apply updates.
- Checks: journalctl -k -b, df -h and df -i, free -m, zypper history in /var/log/zypp/history.
