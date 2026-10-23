GCE persistent disks:
- A zonal disk belongs to one zone: "volume node affinity conflict" means no node in that zone can take the pod. A ReadWriteOnce disk still attached elsewhere gives Multi-Attach errors.
