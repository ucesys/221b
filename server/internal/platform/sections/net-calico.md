Calico:
- calico-node runs on every node; "BIRD is not ready" or "BGP not established" in its readiness means BGP peering (TCP 179) between nodes fails. Encapsulated traffic is IP-in-IP (IP protocol 4) or VXLAN (UDP 4789), which firewalls must pass.
- Pod addresses come from IP pools: a full pool or block stops pods starting on a node ("failed to allocate").
- NetworkManager must leave the cali* and tunl* or vxlan.calico interfaces alone; MTU mismatches make large requests hang.
