Google GKE:
- Google runs the control plane: it has no pods in the cluster, and its logs are in Cloud Logging.
- Node pools carry cloud.google.com/gke-nodepool. Auto-repair and auto-upgrade recreate nodes: a node that disappears and comes back with a new boot may have been repaired or upgraded, not failed; the cluster's operations in Google Cloud say which.
- On Autopilot, Google manages the nodes: no access to them, and pods' requests are adjusted to Autopilot's rules.
- Pod addresses come from the subnet's secondary range: a node pool that cannot grow may have run out of them.
- Checks on a node (Standard clusters): gcloud compute ssh to it, then the commands of its operating system below.
