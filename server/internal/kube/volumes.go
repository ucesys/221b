package kube

import "time"

// VolumeStats is what a kubelet's Summary API says about one mounted
// volume claim (ST-08): pods[].volume[] matched by pvcRef. The server reads
// it with its own ServiceAccount under kubeletStats, as it reads boot
// times; nobody's request reaches a kubelet.
type VolumeStats struct {
	UsedBytes      int64
	CapacityBytes  int64
	AvailableBytes int64
	InodesUsed     int64
	Inodes         int64
	// Node is the node whose kubelet reported it; At when.
	Node string
	At   time.Time
}
