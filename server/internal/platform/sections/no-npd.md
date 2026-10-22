Without node-problem-detector:
- Kernel faults, read-only filesystems and failing disks do not appear as node conditions. They show only indirectly: a node NotReady or Unknown, pods failing to start or write, containerd errors in events, evictions, and DiskPressure, MemoryPressure or PIDPressure. Name the node checks that would confirm them.
