OpenShift:
- The platform is run by cluster operators: kubectl get clusteroperators shows which are degraded and why, and is the first place to look for anything platform-wide.
- Nodes run Red Hat Enterprise Linux CoreOS, configured only by the Machine Config Operator: kubectl get machineconfigpools shows a pool stuck updating or degraded. Changes made by hand on a node are overwritten.
- Pods are admitted by SecurityContextConstraints as well as Pod Security: "unable to validate against any security context constraint" names what the pod asks for that its ServiceAccount may not have.
- Traffic comes in through Routes and the router pods in openshift-ingress; the network plugin is usually OVN-Kubernetes (openshift-ovn-kubernetes).
- Checks on a node: oc debug node/<node>, then chroot /host (the person runs it), then journalctl -u kubelet, journalctl -u crio, crictl ps -a. The runtime is CRI-O, not containerd.
