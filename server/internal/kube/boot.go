package kube

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Uptime states reported in Info.Uptime.
const (
	UptimeUnknown     = "unknown"
	UptimeAvailable   = "available"
	UptimeUnavailable = "unavailable"
)

// Boot times are not in the Node object. Each kubelet reports its node's
// boot time in its Summary API, which the API server proxies at
// /api/v1/nodes/<name>/proxy/stats/summary (get on nodes/proxy). A boot
// time only changes when the node reboots, which changes its bootID, so
// each node is read once per bootID: a few requests per node for its
// whole life, however short the refresh interval.

const (
	bootPoll      = 30 * time.Second // how often nodes needing a read are looked for
	bootRetry     = 5 * time.Minute  // after a failed read of one node
	bootForbidden = 5 * time.Minute  // after the API server refused the proxy
	bootWorkers   = 4
	bootTimeout   = 10 * time.Second
)

type bootEntry struct {
	bootID string
	at     time.Time // zero when the read failed
	next   time.Time // when to read again after a failure
}

// summary is the part of the kubelet's Summary API that carries the boot
// time: node.startTime is the system boot time.
type summary struct {
	Node struct {
		StartTime time.Time `json:"startTime"`
	} `json:"node"`
}

// BootedAt returns when a node last booted, if its kubelet has said.
func (c *Cluster) BootedAt(node string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.boots[node]
	if !ok || e.at.IsZero() {
		return time.Time{}, false
	}
	return e.at, true
}

// bootLoop reads boot times for nodes that need one, as soon as the nodes
// are listed and then every bootPoll, until ctx is done.
func (c *Cluster) bootLoop(ctx context.Context) {
	for !c.Ready() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	t := time.NewTicker(bootPoll)
	defer t.Stop()
	for {
		c.readBoots(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ReadBootsNow reads boot times for the nodes that need one without
// waiting for the next poll. Tests use it.
func (c *Cluster) ReadBootsNow(ctx context.Context) { c.readBoots(ctx) }

// readBoots reads the boot time of every node whose bootID it has not seen,
// a few at a time, and tells the store which rows changed.
func (c *Cluster) readBoots(ctx context.Context) {
	if !c.Ready() {
		return
	}
	now := time.Now()
	c.mu.RLock()
	forbiddenUntil, state := c.bootForbiddenUntil, c.info.State
	c.mu.RUnlock()
	if now.Before(forbiddenUntil) || state == StateDisconnected {
		return
	}
	var todo []*corev1.Node
	for _, name := range c.NodeNames() {
		n := c.Node(name)
		if n == nil || !wantBoot(n) {
			continue
		}
		c.mu.RLock()
		e, seen := c.boots[name]
		c.mu.RUnlock()
		if seen && e.bootID == n.Status.NodeInfo.BootID && (!e.at.IsZero() || now.Before(e.next)) {
			continue
		}
		todo = append(todo, n)
	}
	if len(todo) == 0 {
		return
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Name < todo[j].Name })

	var (
		mu        sync.Mutex
		changed   []string
		forbidden error
		okCount   int
		lastErr   error
	)
	work := make(chan *corev1.Node)
	var wg sync.WaitGroup
	for i := 0; i < bootWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range work {
				at, err := c.readBoot(ctx, n.Name)
				mu.Lock()
				if apierrors.IsForbidden(err) {
					forbidden = err
				}
				if err == nil {
					okCount++
				} else if ctx.Err() == nil {
					lastErr = err
				}
				e := bootEntry{bootID: n.Status.NodeInfo.BootID, at: at}
				if err != nil {
					e.next = time.Now().Add(bootRetry)
				}
				c.mu.Lock()
				prev, had := c.boots[n.Name]
				c.boots[n.Name] = e
				c.mu.Unlock()
				if !had || !prev.at.Equal(e.at) {
					changed = append(changed, n.Name)
				}
				mu.Unlock()
			}
		}()
	}
	for _, n := range todo {
		if ctx.Err() != nil || forbiddenSeen(&mu, &forbidden) {
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
	prevState, prevMsg := c.info.Uptime, c.info.UptimeMessage
	switch {
	case forbidden != nil:
		c.info.Uptime = UptimeUnavailable
		c.info.UptimeMessage = "Not allowed to read the kubelets' stats (get on nodes/proxy), so uptime is not shown. The chart grants it when kubeletStats is on."
		c.bootForbiddenUntil = time.Now().Add(bootForbidden)
	case okCount > 0:
		c.info.Uptime = UptimeAvailable
		c.info.UptimeMessage = ""
	case lastErr != nil && c.info.Uptime != UptimeAvailable:
		c.info.Uptime = UptimeUnavailable
		c.info.UptimeMessage = "Uptime is not available: " + shortError(lastErr)
	}
	infoChanged := prevState != c.info.Uptime || prevMsg != c.info.UptimeMessage
	c.mu.Unlock()
	if infoChanged {
		if forbidden != nil {
			c.log.Warn("cannot read boot times from the kubelets", "err", forbidden)
		} else if okCount > 0 {
			c.log.Info("reading boot times from the kubelets")
		} else {
			c.log.Warn("cannot read boot times from the kubelets", "err", lastErr)
		}
	}
	if len(changed) > 0 || infoChanged {
		sort.Strings(changed)
		ch := Change{Info: infoChanged}
		if len(changed) > 0 {
			ch.Mark("nodes", changed...)
		}
		c.emit(ch)
	}
}

func forbiddenSeen(mu *sync.Mutex, err *error) bool {
	mu.Lock()
	defer mu.Unlock()
	return *err != nil
}

// wantBoot says whether a node's kubelet can be asked: it has reported a
// bootID (a node without one has no kubelet, as a simulated node) and is
// posting status.
func wantBoot(n *corev1.Node) bool {
	if n.Status.NodeInfo.BootID == "" {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// readBoot asks one kubelet, through the API server, when its node booted.
func (c *Cluster) readBoot(ctx context.Context, node string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("/api/v1/nodes/"+node+"/proxy/stats/summary").
		Param("only_cpu_and_memory", "true").SetHeader("Accept", "application/json").MaxRetries(0).Do(ctx).Raw()
	if err != nil {
		return time.Time{}, err
	}
	var s summary
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, errors.New("the kubelet's summary is not valid JSON")
	}
	if s.Node.StartTime.IsZero() {
		return time.Time{}, errors.New("the kubelet's summary has no start time")
	}
	return s.Node.StartTime.UTC(), nil
}
