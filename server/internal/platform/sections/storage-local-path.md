local-path:
- A volume is a directory on one node's disk (where, the local-path-config ConfigMap says). Its pod can only run on that node, an unreachable node makes the volume unavailable, and it fills that node's disk, bringing DiskPressure and evictions for everything there.
