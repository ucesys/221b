NVIDIA GPUs:
- The device plugin advertises nvidia.com/gpu on nodes whose driver it finds. A GPU node with none means the driver is missing or not loaded, or containerd is not set up for the NVIDIA runtime (RuntimeClass nvidia); the device plugin pod's log says which.
- Checks on the node: nvidia-smi; dmesg -T for NVRM Xid errors, which point at the driver or the GPU itself.
