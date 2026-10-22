Cilium:
- The cilium agent runs on every node and programs eBPF; when it is not ready, pods there have no network. It may replace kube-proxy: then there is no kube-proxy, and Services are Cilium's.
- It needs a recent kernel: compare the node kernels above with what its version requires when only some nodes misbehave.
- Tunnel traffic is VXLAN on UDP 8472 or Geneve on UDP 6081, health checks on TCP 4240.
- Checks: the cilium agent pod's log; on the node or through the agent pod, cilium-dbg status (the person runs it).
