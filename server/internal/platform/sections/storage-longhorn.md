Longhorn:
- Volumes are replicated across nodes by instance-manager pods; a volume is degraded with too few healthy replicas and faulted with none. Every node needs open-iscsi (iscsid) running; multipathd claiming Longhorn's devices breaks mounts.
