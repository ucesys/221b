Flannel:
- Each node gets a pod subnet; pod traffic between nodes is VXLAN on UDP 8472, which firewalls must pass. /run/flannel/subnet.env on a node shows its subnet and MTU.
- Flannel alone does not enforce NetworkPolicies: they are stored and ignored.
