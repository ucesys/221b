Flatcar nodes:
- Immutable /usr and automatic updates that reboot the node when applied: an unexpected reboot may be an update. Configuration comes from Ignition at first boot.
- Checks: journalctl -u kubelet, journalctl -u containerd, journalctl -k -b, df -h.
