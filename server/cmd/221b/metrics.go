package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/kube"
)

// The console's metrics and memory.

// gauges are the console's metrics, read when they are scraped.
func (w *wiring) gauges() []api.Gauge {
	info := w.cluster.Info()
	nodeCount, podCount, nodeEvents, podEvents := w.cluster.Counts()
	up := 0.0
	if info.State == kube.StateConnected {
		up = 1
	}
	metricsUp := 0.0
	if info.Metrics == kube.MetricsAvailable {
		metricsUp = 1
	}
	gauges := []api.Gauge{}
	for _, o := range w.stored {
		sizes := o.Sizes()
		for _, key := range slices.Sorted(maps.Keys(sizes)) {
			gauges = append(gauges, api.Gauge{Name: "b221_configmap_bytes", Help: "Bytes each key of the console's ConfigMaps holds, as last read or saved; Kubernetes keeps at most 1 MiB in one ConfigMap.", Labels: fmt.Sprintf("configmap=%q,key=%q", o.Name, key), Value: float64(sizes[key])})
		}
	}
	unsaved := 0.0
	if w.sherlockSvc.Ledger().SaveError() != "" {
		unsaved = 1
	}
	gauges = append(gauges, api.Gauge{Name: "b221_sherlock_spend_unsaved", Help: "1 when Sherlock's spend could not be saved: it is counted on this replica only, and a restart forgets it.", Value: unsaved})
	gauges = append(gauges, []api.Gauge{
		{Name: "b221_api_server_up", Help: "1 when the Kubernetes API server answered the last health check.", Value: up},
		{Name: "b221_metrics_available", Help: "1 when metrics-server answered the last read.", Value: metricsUp},
		{Name: "b221_refresh_seconds", Help: "The refresh interval in force: how often browsers are updated.", Value: w.store.RefreshInterval().Seconds()},
		{Name: "b221_cached_nodes", Help: "Nodes in the watch cache.", Value: float64(nodeCount)},
		{Name: "b221_cached_pods", Help: "Pods in the watch cache.", Value: float64(podCount)},
		{Name: "b221_watch_events_total", Help: "Node watch events received.", Value: float64(nodeEvents), Counter: true},
		{Name: "b221_pod_watch_events_total", Help: "Pod watch events received.", Value: float64(podEvents), Counter: true},
		{Name: "b221_go_memory_bytes", Help: "Memory the Go runtime has mapped, in bytes.", Value: goMemory()},
		{Name: "b221_resident_memory_bytes", Help: "Resident memory of the process, in bytes.", Value: residentMemory()},
		{Name: "b221_sessions", Help: "Signed-in sessions.", Value: float64(w.authSvc.Sessions().Count())},
		{Name: "b221_people_access", Help: "People whose access to a cluster is kept, per person and cluster.", Value: float64(peopleCount(w.people))},
		{Name: "b221_audit_dropped_total", Help: "Audit records the webhook queue had no room for.", Value: float64(w.auditLog.Dropped()), Counter: true},
		{Name: "b221_sherlock_conversations", Help: "Sherlock conversations kept, one per session and cluster.", Value: float64(w.sherlockSvc.Count())},
		{Name: "b221_sherlock_answering", Help: "Sherlock answers in progress.", Value: float64(w.sherlockSvc.Running())},
		{Name: "b221_sherlock_spend_today_usd", Help: "What Sherlock has cost today (UTC), in US dollars.", Value: w.sherlockSvc.Ledger().Now(context.Background()).DayUSD},
		{Name: "b221_sherlock_spend_month_usd", Help: "What Sherlock has cost this month (UTC), in US dollars.", Value: w.sherlockSvc.Ledger().Now(context.Background()).MonthUSD},
	}...)
	cached := w.cluster.CachedObjects()
	kinds := make([]string, 0, len(cached))
	for k := range cached {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		gauges = append(gauges, api.Gauge{Name: "b221_cached_objects", Help: "Objects in the watch cache, by kind.", Labels: fmt.Sprintf("kind=%q", k), Value: float64(cached[k])})
	}
	// The other clusters: one gauge each, 1 when its API server answers.
	conns := w.manager.List()
	gauges = append(gauges, api.Gauge{Name: "b221_connections", Help: "Cluster connections, the local one included.", Value: float64(len(conns))})
	for _, c := range conns {
		if c.Local {
			continue
		}
		up := 0.0
		if c.State == kube.StateConnected {
			up = 1
		}
		gauges = append(gauges, api.Gauge{Name: "b221_connection_up", Help: "1 when the connection's API server answered the last health check.", Labels: fmt.Sprintf("cluster=%q", c.ID), Value: up})
	}
	return gauges
}

func peopleCount(c *access.Cache) int {
	if c == nil {
		return 0
	}
	return c.Count()
}

// setMemoryLimit leaves headroom under the container's memory limit, so the
// garbage collector works harder before the kernel would kill the process.
// GOMEMLIMIT, when set, wins.
func setMemoryLimit(log *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 {
		return // "max": no limit
	}
	limit := n / 10 * 9
	debug.SetMemoryLimit(limit)
	log.Info("memory limit set", "container_bytes", n, "go_bytes", limit)
}

func goMemory() float64 {
	sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return float64(sample[0].Value.Uint64())
}

// residentMemory reads the resident set size on Linux, and 0 elsewhere.
func residentMemory() float64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0
	}
	return float64(pages * int64(os.Getpagesize()))
}
