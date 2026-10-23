K3s ServiceLB:
- Each LoadBalancer Service gets a svclb-<service> DaemonSet whose pods take the Service's ports on the nodes' own addresses. A port already used on a node leaves its svclb pod Pending, and that node does not serve the Service.
