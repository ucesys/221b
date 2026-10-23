Amazon VPC CNI (aws-node):
- Pods take addresses from the VPC subnets through the node's network interfaces. "failed to assign an IP address to container" means the subnet ran out of addresses or the instance type's interface and address limit was reached; that limit also caps pods per node.
- Its logs are the aws-node pod's, and on the node /var/log/aws-routed-eni/.
