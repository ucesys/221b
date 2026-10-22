Amazon EBS CSI:
- A volume belongs to one availability zone: "volume node affinity conflict" means no node in that zone can take the pod. Instance types limit how many volumes attach, and a ReadWriteOnce volume still attached elsewhere gives Multi-Attach errors.
