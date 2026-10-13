// Command 221b serves the 221B console.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/klog/v2"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/ui"
	"github.com/ucesys/221b/server/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "221b:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	// client-go logs through klog; send it to the same structured log.
	klog.SetSlogLogger(log.With("component", "client-go"))
	setMemoryLimit(log)

	restCfg, mode, err := kube.RestConfig(kube.ConnectOptions{Kubeconfig: cfg.Kubeconfig, Context: cfg.Context, APIServer: cfg.APIServer})
	if err != nil {
		return err
	}
	cluster, err := kube.New(restCfg, kube.Options{
		Name:           cfg.ClusterName,
		Mode:           mode,
		HealthInterval: cfg.HealthInterval,
		KubeletStats:   cfg.KubeletStats,
		SecretsRead:    cfg.SecretsRead,
		Protobuf:       mode != kube.ModeURL,
		Logger:         log.With("component", "cluster"),
	})
	if err != nil {
		return err
	}
	store := live.New(cluster, live.Options{Refresh: cfg.Refresh, Logger: log.With("component", "store")})
	cluster.OnChange(store.Notify)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The shared settings: in the chart's ConfigMap when running in a
	// cluster, otherwise in memory. Every change reaches every browser
	// through the store.
	var storage settings.Storage = &settings.Memory{}
	if cfg.SettingsConfigMap != "" {
		cm, err := kube.NewConfigMapStorage(restCfg, cfg.SettingsConfigMap)
		if err != nil {
			return err
		}
		storage = cm
	}
	shared := settings.NewService(ctx, storage, settings.Settings{RefreshSeconds: int(cfg.Refresh / time.Second)}, log.With("component", "settings"), func(st settings.State) {
		b, _ := json.Marshal(st)
		store.SetSettings(st.Refresh(), b)
	})
	go shared.Run(ctx, time.Minute)

	go cluster.Run(ctx)
	go store.Run(ctx)

	handler := api.New(api.Options{
		Store:        store,
		Cluster:      cluster,
		Settings:     shared,
		Ready:        cluster.Ready,
		UI:           ui.Handler(),
		Logger:       log.With("component", "http"),
		AllowedHosts: cfg.AllowedHosts,
		Gauges: func() []api.Gauge {
			info := cluster.Info()
			nodeCount, podCount, nodeEvents, podEvents := cluster.Counts()
			up := 0.0
			if info.State == kube.StateConnected {
				up = 1
			}
			metricsUp := 0.0
			if info.Metrics == kube.MetricsAvailable {
				metricsUp = 1
			}
			gauges := []api.Gauge{
				{Name: "b221_api_server_up", Help: "1 when the Kubernetes API server answered the last health check.", Value: up},
				{Name: "b221_metrics_available", Help: "1 when metrics-server answered the last read.", Value: metricsUp},
				{Name: "b221_refresh_seconds", Help: "The refresh interval in force: how often browsers are updated.", Value: store.RefreshInterval().Seconds()},
				{Name: "b221_cached_nodes", Help: "Nodes in the watch cache.", Value: float64(nodeCount)},
				{Name: "b221_cached_pods", Help: "Pods in the watch cache.", Value: float64(podCount)},
				{Name: "b221_watch_events_total", Help: "Node watch events received.", Value: float64(nodeEvents), Counter: true},
				{Name: "b221_pod_watch_events_total", Help: "Pod watch events received.", Value: float64(podEvents), Counter: true},
				{Name: "b221_go_memory_bytes", Help: "Memory the Go runtime has mapped, in bytes.", Value: goMemory()},
				{Name: "b221_resident_memory_bytes", Help: "Resident memory of the process, in bytes.", Value: residentMemory()},
			}
			cached := cluster.CachedObjects()
			kinds := make([]string, 0, len(cached))
			for k := range cached {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			for _, k := range kinds {
				gauges = append(gauges, api.Gauge{Name: "b221_cached_objects", Help: "Objects in the watch cache, by kind.", Labels: fmt.Sprintf("kind=%q", k), Value: float64(cached[k])})
			}
			return gauges
		},
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: event streams stay open; each write sets its own
		// deadline instead. Requests share ctx, so streams end on shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("serving", "addr", cfg.Listen, "cluster", cfg.ClusterName, "mode", string(mode), "version", version.String(), "secrets_read", cfg.SecretsRead)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
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
