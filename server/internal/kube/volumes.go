package kube

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// VolumeStats is what a kubelet's Summary API says about one mounted
// volume claim (ST-08): pods[].volume[] matched by pvcRef. The server reads
// it with its own ServiceAccount under kubeletStats, as it reads boot
// times (ADR-17); nobody's request reaches a kubelet, and a person sees the
// figures only beside the claims they may list.
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

// volumePoll is how often the volumes are read. Each read is one Summary
// request per node that runs a pod with a claim, whatever the number of
// claims (ST-25); the kubelets measure volumes about once a minute, so
// reading more often than this shows nothing new.
const volumePoll = 30 * time.Second

// volumeSummary is the part of the Summary API that carries volume stats.
type volumeSummary struct {
	Pods []struct {
		Volume []struct {
			Time           time.Time `json:"time"`
			UsedBytes      *int64    `json:"usedBytes"`
			CapacityBytes  *int64    `json:"capacityBytes"`
			AvailableBytes *int64    `json:"availableBytes"`
			InodesUsed     *int64    `json:"inodesUsed"`
			Inodes         *int64    `json:"inodes"`
			PVCRef         *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

// VolumeStats returns what the kubelets last reported about a claim.
func (c *Cluster) VolumeStats(namespace, claim string) (VolumeStats, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.volumes[namespace+"/"+claim]
	return v, ok
}

// pollVolumes reads the volume stats of every node that runs a pod with a
// claim, at most every volumePoll, and marks the storage view when they
// change.
func (c *Cluster) pollVolumes(ctx context.Context) {
	now := time.Now()
	c.mu.RLock()
	due := now.Sub(c.volumesAt) >= volumePoll
	forbiddenUntil, state := c.bootForbiddenUntil, c.info.State
	c.mu.RUnlock()
	if !due || !c.Ready() || now.Before(forbiddenUntil) || state == StateDisconnected {
		return
	}
	c.mu.Lock()
	c.volumesAt = now
	c.mu.Unlock()
	c.ReadVolumesNow(ctx)
}

// ReadVolumesNow reads the volume stats without waiting for the next poll.
// Tests use it.
func (c *Cluster) ReadVolumesNow(ctx context.Context) {
	var todo []string
	for _, name := range c.NodeNames() {
		n := c.Node(name)
		if n == nil || !wantBoot(n) {
			continue
		}
		for _, p := range c.PodsOn(name) {
			if p.Status.Phase == corev1.PodRunning && len(podClaims(p)) > 0 {
				todo = append(todo, name)
				break
			}
		}
	}
	sort.Strings(todo)
	var (
		mu        sync.Mutex
		got       = map[string]VolumeStats{}
		forbidden error
	)
	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < bootWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for node := range work {
				stats, err := c.readVolumes(ctx, node)
				mu.Lock()
				if apierrors.IsForbidden(err) {
					forbidden = err
				}
				if err != nil {
					// A node that did not answer keeps what it said last.
					stats = c.volumesOf(node)
				}
				for k, v := range stats {
					// A claim mounted on several nodes is reported by each;
					// the latest report stands.
					if old, ok := got[k]; !ok || v.At.After(old.At) {
						got[k] = v
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, n := range todo {
		if ctx.Err() != nil {
			break
		}
		work <- n
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	if forbidden != nil {
		c.bootForbiddenUntil = time.Now().Add(bootForbidden)
	}
	changed := !reflect.DeepEqual(c.volumes, got)
	c.volumes = got
	c.mu.Unlock()
	if changed {
		ch := Change{}
		ch.Mark("storage", AllRows)
		c.emit(ch)
	}
}

// readVolumes asks one kubelet, through the API server, about the claims
// its pods mount.
func (c *Cluster) readVolumes(ctx context.Context, node string) (map[string]VolumeStats, error) {
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("/api/v1/nodes/"+node+"/proxy/stats/summary").SetHeader("Accept", "application/json").MaxRetries(0).Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	var s volumeSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errors.New("the kubelet's summary is not valid JSON")
	}
	out := map[string]VolumeStats{}
	val := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	for _, p := range s.Pods {
		for _, v := range p.Volume {
			if v.PVCRef == nil || v.CapacityBytes == nil {
				continue
			}
			out[v.PVCRef.Namespace+"/"+v.PVCRef.Name] = VolumeStats{UsedBytes: val(v.UsedBytes), CapacityBytes: val(v.CapacityBytes), AvailableBytes: val(v.AvailableBytes), InodesUsed: val(v.InodesUsed), Inodes: val(v.Inodes), Node: node, At: v.Time.UTC()}
		}
	}
	return out, nil
}

// volumesOf is what a node's kubelet last said about its volumes.
func (c *Cluster) volumesOf(node string) map[string]VolumeStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]VolumeStats{}
	for k, v := range c.volumes {
		if v.Node == node {
			out[k] = v
		}
	}
	return out
}
