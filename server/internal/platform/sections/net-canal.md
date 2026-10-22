Canal (Calico policy, Flannel VXLAN):
- Each node runs a canal pod with calico-node and kube-flannel containers; when it is not ready, pods on that node lose their network.
- Pod traffic between nodes is VXLAN on UDP 8472: pods that cannot reach pods on other nodes, while the nodes reach each other, point at that port being blocked, or at an MTU mismatch (small requests work, large ones hang).
- NetworkManager must leave the cali* and flannel* interfaces alone (unmanaged-devices in /etc/NetworkManager/conf.d/), and firewalld should be off: either breaks pod networking, often after a restart or reboot.
