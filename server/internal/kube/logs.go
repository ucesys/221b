package kube

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/nodes"
)

// LogOptions select what to read from a pod's logs.
type LogOptions struct {
	Container string
	// Follow keeps the stream open as the container writes.
	Follow bool
	// Previous reads the last terminated container's logs instead.
	Previous bool
	// Tail limits the read to the last lines; 0 reads everything.
	Tail int64
	// Timestamps prefixes each line with its time.
	Timestamps bool
}

// Logs opens a pod's log stream through the API server (P2-04), as the
// person (P3-02). The caller closes it; ending ctx ends it too.
func (c *Cluster) Logs(ctx context.Context, p Person, namespace, name string, o LogOptions) (io.ReadCloser, error) {
	cs, err := c.as(p)
	if err != nil {
		return nil, err
	}
	opts := &corev1.PodLogOptions{Container: o.Container, Follow: o.Follow, Previous: o.Previous, Timestamps: o.Timestamps}
	if o.Tail > 0 {
		opts.TailLines = &o.Tail
	}
	return cs.CoreV1().Pods(namespace).GetLogs(name, opts).Stream(ctx)
}

// PodUsageOf returns what metrics-server last reported for one pod.
func (c *Cluster) PodUsageOf(namespace, name string) (nodes.Usage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	u, ok := c.podUsage[namespace+"/"+name]
	return u, ok
}
