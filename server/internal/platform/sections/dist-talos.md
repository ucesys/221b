Talos Linux:
- Nodes have no SSH or shell and an immutable root filesystem: everything goes through talosctl and the machine configuration. Nothing is fixed by hand on a node.
- etcd and the kubelet are Talos services, not pods; the rest of the control plane runs as static pods.
- Checks (the person runs talosctl): talosctl -n <node ip> health, services, logs kubelet, logs etcd, dmesg, etcd members, get disks.
