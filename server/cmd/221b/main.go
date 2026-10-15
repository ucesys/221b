// Command 221b serves the 221B console.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/prefs"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/ui"
	"github.com/ucesys/221b/server/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := hashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, "221b:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "221b:", err)
		os.Exit(1)
	}
}

// hashPassword prints the hash of a password read from standard input, for
// the local accounts in the chart's values: 221b hash-password < file.
func hashPassword() error {
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(strings.TrimRight(string(b), "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func run(args []string) error {
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	// The audit log (P3-10) owns standard output; the ordinary logs go to
	// standard error.
	auditLog := audit.New(audit.Options{Out: os.Stdout, WebhookURL: cfg.AuditWebhookURL, WebhookToken: cfg.AuditWebhookToken, Logger: log.With("component", "audit")})
	authCfg := auth.Config{
		Mode:        auth.Mode(cfg.AuthMode),
		OIDC:        auth.OIDCConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, Scopes: cfg.OIDCScopes, RedirectURL: cfg.OIDCRedirectURL, UsernameClaim: cfg.OIDCUsernameClaim, GroupsClaim: cfg.OIDCGroupsClaim, ProviderName: cfg.OIDCProviderName},
		Admins:      cfg.Admins,
		AdminGroups: cfg.AdminGroups,
		SessionIdle: cfg.SessionIdle,
		SessionMax:  cfg.SessionMax,
		Title:       "221B · " + cfg.ClusterName,
	}
	if cfg.LocalUsersFile != "" {
		users, err := auth.ReadLocalUsers(cfg.LocalUsersFile)
		if err != nil {
			return err
		}
		authCfg.Local = users
	}
	authSvc, err := auth.New(authCfg, log.With("component", "auth"), auditLog, nil)
	if err != nil {
		return err
	}
	if authCfg.Mode == auth.ModeNone {
		log.Warn("sign-in is off: everyone who reaches the console is an administrator; set -auth-mode oidc or local before sharing it")
	}
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
	store := live.New(cluster, live.Options{Refresh: cfg.Refresh, Logger: log.With("component", "store"), Cluster: clusters.LocalID})
	cluster.OnChange(store.Notify)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The other clusters (P3-04 to P3-09): their connections live in
	// Secrets in the console's own namespace when running in a cluster,
	// otherwise in memory. The cluster the console was started against is
	// the connection "local".
	var connStore clusters.Store = clusters.NewMemoryStore()
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, ok := strings.Cut(cfg.ConnectionsSecrets, "/")
		if !ok || ns == "" || prefix == "" {
			return fmt.Errorf("-connections-secrets must be namespace/prefix, got %q", cfg.ConnectionsSecrets)
		}
		ss, err := clusters.NewSecretStore(restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		connStore = ss
	} else {
		log.Warn("cluster connections added in the console are kept in memory and lost on restart; set -connections-secrets namespace/prefix to keep them in Secrets")
	}
	manager := clusters.New(connStore, clusters.Options{Refresh: cfg.Refresh, HealthInterval: cfg.HealthInterval, KubeletStats: cfg.KubeletStats, SecretsRead: cfg.SecretsRead, Logger: log.With("component", "clusters")})
	if err := manager.SetLocal(ctx, localSpec(cfg, restCfg, mode), cluster, store); err != nil {
		return fmt.Errorf("local connection: %w", err)
	}

	// The shared settings: in the chart's ConfigMap when running in a
	// cluster, otherwise in memory. Every change reaches every browser
	// through the stores.
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
		manager.SetSettings(st.Refresh(), b)
	})
	go shared.Run(ctx, time.Minute)

	go cluster.Run(ctx)
	go store.Run(ctx)
	if err := manager.Start(ctx); err != nil {
		return fmt.Errorf("cluster connections: %w", err)
	}
	// What each person may see (P3-02): Kubernetes is asked and the answers
	// kept for a while; people who have not asked for an hour are forgotten.
	var people *access.Cache
	if authCfg.Mode != auth.ModeNone {
		people = access.NewCache(access.Options{TTL: cfg.AccessTTL})
	}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				authSvc.Sweep()
				if people != nil {
					people.Sweep(time.Hour)
				}
			}
		}
	}()

	// Each signed-in person's own preferences, next to the settings.
	var userPrefs *prefs.Store
	if cfg.UserPrefs && authCfg.Mode != auth.ModeNone {
		var backend prefs.Backend = &prefs.Memory{}
		if cm, ok := storage.(*kube.ConfigMapStorage); ok {
			backend = cm
		}
		userPrefs = prefs.New(backend)
	}

	// Sherlock (Phase 5): off for every cluster until an administrator turns
	// it on and gives it a Claude API key, which is kept in a Secret.
	var keyBackend sherlock.KeyBackend = &sherlock.MemoryKey{}
	secretRef := cfg.SherlockSecret
	if secretRef == "" && cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		secretRef = ns + "/" + prefix + "-sherlock"
	}
	if secretRef != "" {
		ns, name, ok := strings.Cut(secretRef, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("-sherlock-secret must be namespace/name, got %q", secretRef)
		}
		sk, err := kube.NewSecretKey(restCfg, ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "sherlock", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		keyBackend = sk
	}
	keys := sherlock.NewKeys(keyBackend, cfg.SherlockAPIKey, nil)
	if err := keys.Load(ctx); err != nil {
		log.Warn("reading the Claude API key failed; Sherlock has no key until it is saved again", "err", err)
	}
	var spendBackend prefs.Backend = &prefs.Memory{}
	if cm, ok := storage.(*kube.ConfigMapStorage); ok {
		spendBackend = cm
	}
	sherlockSvc := sherlock.New(sherlock.Options{
		Client: &sherlock.Client{BaseURL: cfg.SherlockAPIURL, Key: keys.Key, HTTP: &http.Client{Timeout: 5 * time.Minute}, Retries: 2, Backoff: 2 * time.Second},
		Ledger: sherlock.NewLedger(spendBackend, nil, log.With("component", "sherlock")),
		Settings: func() sherlock.Settings {
			sh := shared.Current().SherlockOrDefault()
			return sherlock.Settings{Model: sh.Model, Caps: sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}}
		},
		Secrets: func() []string { return []string{keys.Key()} },
		Audit:   auditLog,
		Logger:  log.With("component", "sherlock"),
	})
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sherlockSvc.Sweep(authSvc.Sessions().Alive)
			}
		}
	}()

	handler := api.New(api.Options{
		Sherlock:      sherlockSvc,
		SherlockKeys:  keys,
		Store:         store,
		Prefs:         userPrefs,
		Cluster:       cluster,
		Settings:      shared,
		Clusters:      manager,
		Auth:          authSvc,
		Audit:         auditLog,
		Access:        people,
		Impersonation: api.Impersonation{UserPrefix: cfg.ImpersonateUserPrefix, GroupPrefix: cfg.ImpersonateGroupPrefix},
		HTTPSRedirect: cfg.HTTPSRedirect,
		Ready:         cluster.Ready,
		UI:            ui.Handler(),
		Logger:        log.With("component", "http"),
		AllowedHosts:  cfg.AllowedHosts,
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
				{Name: "b221_sessions", Help: "Signed-in sessions.", Value: float64(authSvc.Sessions().Count())},
				{Name: "b221_people_access", Help: "People whose access to a cluster is kept, per person and cluster.", Value: float64(peopleCount(people))},
				{Name: "b221_audit_dropped_total", Help: "Audit records the webhook queue had no room for.", Value: float64(auditLog.Dropped()), Counter: true},
				{Name: "b221_sherlock_conversations", Help: "Sherlock conversations kept, one per session and cluster.", Value: float64(sherlockSvc.Count())},
				{Name: "b221_sherlock_answering", Help: "Sherlock answers in progress.", Value: float64(sherlockSvc.Running())},
				{Name: "b221_sherlock_spend_today_usd", Help: "What Sherlock has cost today (UTC), in US dollars.", Value: sherlockSvc.Ledger().Now(context.Background()).DayUSD},
				{Name: "b221_sherlock_spend_month_usd", Help: "What Sherlock has cost this month (UTC), in US dollars.", Value: sherlockSvc.Ledger().Now(context.Background()).MonthUSD},
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
			// The other clusters: one gauge each, 1 when its API server answers.
			conns := manager.List()
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
		log.Info("serving", "addr", cfg.Listen, "cluster", cfg.ClusterName, "mode", string(mode), "version", version.String(), "secrets_read", cfg.SecretsRead, "auth", cfg.AuthMode)
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
	err = srv.Shutdown(shutdownCtx)
	auditLog.Close(shutdownCtx)
	return err
}

// localSpec describes the cluster the console was started against as a
// connection: its address and how the console signs in, for the cluster
// page; it is not editable beyond its name, environment and description.
func localSpec(cfg config.Config, restCfg *rest.Config, mode kube.Mode) clusters.Spec {
	spec := clusters.Spec{Name: cfg.ClusterName, Env: clusters.EnvOther, Server: restCfg.Host, Mode: clusters.ModeRead}
	switch mode {
	case kube.ModeInCluster:
		spec.Auth = clusters.AuthInCluster
		spec.Description = "The cluster the console runs in."
	case kube.ModeKubeconfig:
		spec.Auth = clusters.AuthKubeconfig
		spec.Cred = clusters.CredInfo{Context: cfg.Context}
	default:
		spec.Auth = clusters.AuthNone
	}
	switch {
	case len(restCfg.TLSClientConfig.CAData) > 0, restCfg.TLSClientConfig.CAFile != "":
		spec.CA = clusters.CAPEM
	case restCfg.TLSClientConfig.Insecure:
		spec.CA = clusters.CASkip
	default:
		spec.CA = clusters.CASystem
	}
	return spec
}

func peopleCount(c *access.Cache) int {
	if c == nil {
		return 0
	}
	return c.Count()
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
