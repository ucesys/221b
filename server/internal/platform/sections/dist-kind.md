kind:
- The nodes are containers on one host, made with kubeadm. The operating system and hardware are the host's: a full host disk, Docker's memory limit or the host's inotify limits ("too many open files") break the cluster. The node OS shown is the node image's.
- Images built on the host reach the nodes only through kind load docker-image: ImagePullBackOff for a local image means it was not loaded.
- There is no LoadBalancer implementation unless cloud-provider-kind or MetalLB is added: LoadBalancer Services stay pending.
- Checks on the host: docker exec <node> journalctl -u kubelet, docker exec <node> crictl ps -a, kind export logs, docker system df, df -h.
