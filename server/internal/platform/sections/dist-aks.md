Azure AKS:
- Microsoft runs the control plane: it has no pods in the cluster, and its logs reach Log Analytics only through diagnostic settings.
- Node pools carry kubernetes.azure.com/agentpool; nodes are Virtual Machine Scale Set instances. Node image upgrades and the cluster autoscaler replace nodes.
- ImagePullBackOff with 401 from an Azure Container Registry usually means the kubelet identity lacks AcrPull on it.
- With Azure CNI without overlay, pods take subnet addresses: a full subnet stops pods starting and node pools growing.
- Checks on a node: kubectl debug node/<node> -it --image=mcr.microsoft.com/cbl-mariner/busybox:2.0, then chroot /host (the person runs it; Watson does not), then the commands of its operating system below.
