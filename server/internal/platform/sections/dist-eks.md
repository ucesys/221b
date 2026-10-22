Amazon EKS:
- AWS runs the control plane: no etcd or API server pods exist in the cluster, and their logs are in CloudWatch when control plane logging is on. API server errors, throttling or webhooks timing out point there.
- Node groups carry eks.amazonaws.com/nodegroup; Karpenter's nodes carry karpenter.sh/nodepool. A node that disappears may have been scaled in, replaced or interrupted (Spot), not failed: check the node's events and the autoscaler first.
- A node that never joins usually lacks an access entry or aws-auth mapping for its IAM role, or cannot reach the API endpoint.
- An application's AccessDenied from AWS means its ServiceAccount's IAM role (IRSA or EKS Pod Identity), not RBAC.
- Checks on a node: through SSM Session Manager (nodes rarely allow SSH), the commands of its operating system below.
