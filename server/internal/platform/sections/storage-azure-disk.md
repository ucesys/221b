Azure Disk CSI:
- A disk belongs to one zone and a VM size limits how many attach. A disk still attached to a failed node gives Multi-Attach errors until Azure detaches it.
