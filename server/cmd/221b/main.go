// Command 221b serves the 221B console.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	corev1 "k8s.io/api/core/v1"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/ldap"
	"github.com/ucesys/221b/server/internal/leader"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/twoperson"
	"github.com/ucesys/221b/server/internal/ui"
	"github.com/ucesys/221b/server/internal/version"
	"github.com/ucesys/221b/server/internal/watson"
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

const usersKey = "users.json"

// newDirectory builds the LDAP client from auth.ldap (RB-19).
func newDirectory(f auth.FileConfig, bindPassword string) (auth.Directory, time.Duration, error) {
	l := f.LDAP
	if l == nil {
		return nil, 0, errors.New("-auth-mode ldap needs auth.ldap in the auth config file")
	}
	every, err := l.SyncEvery()
	if err != nil {
		return nil, 0, err
	}
	o := ldap.Options{
		Config:       ldap.Config{URL: l.URL, StartTLS: l.StartTLS, Timeout: 15 * time.Second},
		BindDN:       l.BindDN,
		BindPassword: bindPassword,
		UserBase:     l.UserSearch.Base, UserFilter: l.UserSearch.Filter, UsernameAttr: l.UserSearch.UsernameAttr, NameAttr: l.UserSearch.NameAttr, EmailAttr: l.UserSearch.EmailAttr,
		GroupBase: l.GroupSearch.Base, GroupFilter: l.GroupSearch.Filter, GroupAttr: l.GroupSearch.NameAttr, MemberAttr: l.GroupSearch.MemberAttr,
		Nested:   l.GroupSearch.Nested == nil || *l.GroupSearch.Nested,
		PageSize: l.PageSize,
	}
	if l.CAFile != "" {
		pem, err := os.ReadFile(l.CAFile)
		if err != nil {
			return nil, 0, fmt.Errorf("reading the LDAP CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, 0, fmt.Errorf("%s holds no certificate", l.CAFile)
		}
		o.RootCAs = pool
	}
	d, err := ldap.New(o)
	return d, every, err
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
	// The roles, the groups and the directory (Phase 6.1) come in a file the
	// chart renders; the secrets come through the environment.
	authFile, err := auth.ReadFileConfig(cfg.AuthConfigFile)
	if err != nil {
		return err
	}
	authFile.Apply(&authCfg)
	authCfg.Prefixes.OIDCUser, authCfg.Prefixes.OIDCGroup = cfg.ImpersonateUserPrefix, cfg.ImpersonateGroupPrefix
	authCfg.SetupToken = cfg.SetupToken
	authCfg.AccessTTL = cfg.AccessTTL
	if authCfg.Mode == auth.ModeLDAP {
		dir, every, err := newDirectory(authFile, cfg.LDAPBindPassword)
		if err != nil {
			return err
		}
		authCfg.Directory, authCfg.SyncInterval = dir, every
	}
	authCfg.Anonymous = roles.ID(cfg.AnonymousRole)
	if authCfg.Mode == auth.ModeNone {
		// Nobody is known, so nothing that needs a known person runs: SSH,
		// Automation and showing Secret values stay off whatever the chart
		// says, and everyone holds the anonymous role.
		cfg.Automation, cfg.SecretsReveal = false, false
		log.Warn("sign-in is off: everyone who reaches the console holds the anonymous role, and SSH, Automation and Secret values are off; set -auth-mode oidc, ldap or local to use them", "anonymous_role", cfg.AnonymousRole)
	}
	// client-go logs through klog; send it to the same structured log.
	klog.SetSlogLogger(log.With("component", "client-go"))
	setMemoryLimit(log)

	restCfg, mode, err := kube.RestConfig(kube.ConnectOptions{Kubeconfig: cfg.Kubeconfig, Context: cfg.Context, APIServer: cfg.APIServer})
	if err != nil {
		return err
	}
	// The users created in the console are kept in a Secret of the console's
	// own namespace when running in a cluster, otherwise in memory (RB-46).
	usersRef := cfg.UsersSecret
	if usersRef == "" && cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		usersRef = ns + "/" + prefix + "-users"
	}
	if usersRef != "" && authCfg.Mode == auth.ModeLocal {
		ns, name, ok := strings.Cut(usersRef, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("-users-secret must be namespace/name, got %q", usersRef)
		}
		sk, err := kube.NewSecretKey(restCfg, ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "users", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		authCfg.Accounts = auth.SecretBackend(sk, usersKey)
	} else if authCfg.Mode == auth.ModeLocal {
		log.Warn("users created in the console are kept in memory and lost on restart; set -users-secret namespace/name, or -connections-secrets, to keep them in a Secret")
	}
	// Sign-ins are kept in Secrets of the console's namespace when running
	// in a cluster, one per session, so every replica knows them and they
	// survive a restart (P7-02); otherwise in memory.
	var sessionStore *kube.SessionStore
	if cfg.ConnectionsSecrets != "" && authCfg.Mode != auth.ModeNone {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sessionStore, err = kube.NewSessionStore(restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		authCfg.SessionStore = sessionStore
	}
	// What waits for a second SuperAdmin (review item 14), kept beside the
	// settings once their storage is open.
	twoPerson := twoperson.New(nil, nil)
	authCfg.TwoPerson = twoPerson
	authSvc, err := auth.New(authCfg, log.With("component", "auth"), auditLog, nil)
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
	// What this replica does with the clusters (ADR-70): the leader reads
	// them; the others keep their watches warm, or none, and poll nothing.
	activity := leader.NewActivity(cfg.WarmStandby)
	store := live.New(cluster, live.Options{Refresh: cfg.Refresh, Logger: log.With("component", "store"), Cluster: clusters.LocalID, Active: activity.Active})
	cluster.OnChange(store.Notify)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if sessionStore != nil {
		go sessionStore.Run(ctx)
		syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if !sessionStore.Synced(syncCtx) {
			log.Warn("the shared sessions are not cached yet; reading them one by one")
		}
		cancel()
	}
	// Accounts created, changed or removed through another replica take
	// effect here within seconds.
	go authSvc.RunAccounts(ctx, 5*time.Second)
	go authSvc.RunDirectory(ctx)

	// --- Several replicas (P7-02): the one holding the Lease runs Sherlock
	// and Watson, and the others forward those requests to it. Without a
	// Lease this is the only replica, and always the leader.
	// Becoming the leader is signalled here; the shared state Sherlock and
	// Watson rely on is read again before they serve (see below).
	leading := make(chan struct{}, 1)
	// A cold standby that loses the Lease cannot stand by again with its
	// watches stopped: it exits, and the pod starts afresh.
	lost := make(chan struct{})
	var lostOnce sync.Once
	onLead, onStop := roleSwitch(activity, func() { lostOnce.Do(func() { close(lost) }) })
	elector, err := newElector(cfg, restCfg, log, func() {
		onLead()
		select {
		case leading <- struct{}{}:
		default:
		}
	}, onStop)
	if err != nil {
		return err
	}
	if elector.Single() {
		activity.Set(true)
	}
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		if err := elector.Run(ctx); err != nil {
			log.Error("leader election stopped", "err", err)
		}
	}()
	// --- end of the replicas' election.

	// The other clusters (P3-04 to P3-09): their connections live in
	// Secrets in the console's own namespace when running in a cluster,
	// otherwise in memory. The cluster the console was started against is
	// the connection "local".
	var connStore clusters.Store = clusters.NewMemoryStore()
	// The Helm applications (Phase 10) are kept beside the connections.
	var appStore apps.Store = apps.NewMemoryStore()
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
		as, err := apps.NewSecretStore(restCfg, ns, prefix)
		if err != nil {
			return err
		}
		appStore = as
	} else {
		log.Warn("cluster connections added in the console are kept in memory and lost on restart; set -connections-secrets namespace/prefix to keep them in Secrets")
	}
	manager := clusters.New(connStore, clusters.Options{Refresh: cfg.Refresh, HealthInterval: cfg.HealthInterval, KubeletStats: cfg.KubeletStats, SecretsRead: cfg.SecretsRead, Logger: log.With("component", "clusters"), Activity: activity})
	// SetLocal runs the local cluster's watches, pollers and store as the
	// role says, like every other connection's.
	if err := manager.SetLocal(ctx, localSpec(cfg, restCfg, mode), cluster, store); err != nil {
		return fmt.Errorf("local connection: %w", err)
	}

	// The shared settings: in the chart's ConfigMap when running in a
	// cluster, otherwise in memory. Every change reaches every browser
	// through the stores.
	var storage settings.Storage = &settings.Memory{}
	// What the settings ConfigMap holds beside the settings, with the audit
	// log and the session records in ConfigMaps of their own when the chart
	// names them, and every key within its limit (#46).
	var spendBackend kv.Backend = &kv.Memory{}
	var stored storedObjects
	if cfg.SettingsConfigMap != "" {
		cm, err := kube.NewConfigMapStorage(restCfg, cfg.SettingsConfigMap)
		if err != nil {
			return err
		}
		if spendBackend, stored, err = settingsBackend(ctx, restCfg, cm, cfg.AuditConfigMap, cfg.SessionsConfigMap, log.With("component", "storage")); err != nil {
			return err
		}
		storage = kube.KeyStorage{Backend: spendBackend, Key: kube.SettingsKey}
	}
	shared := settings.NewService(ctx, storage, settings.Settings{RefreshSeconds: int(cfg.Refresh / time.Second)}, log.With("component", "settings"), func(st settings.State) {
		b, _ := json.Marshal(st)
		manager.SetSettings(st.Refresh(), b)
	})
	// Read again every few seconds: a change saved through another replica,
	// or by hand, reaches this one's browsers within seconds (P7-02).
	go shared.Run(ctx, 3*time.Second)

	if err := manager.Start(ctx); err != nil {
		return fmt.Errorf("cluster connections: %w", err)
	}
	// Connections added, changed or removed through another replica.
	go manager.Run(ctx, 5*time.Second)
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
	go keys.Run(ctx, 10*time.Second, func(err error) { log.Warn("reading the Claude API key failed", "err", err) })
	twoPerson.SetBackend(spendBackend)
	if auditStore := keepAudit(auditLog, spendBackend, log.With("component", "audit")); auditStore != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := auditStore.Close(ctx); err != nil {
				log.Warn("saving the audit log at shutdown failed", "err", err)
			}
		}()
	}
	// The chart may name Sherlock's provider (sherlock.provider), and lock it.
	var chartProvider sherlock.Provider
	if cfg.SherlockProvider != "" {
		if err := json.Unmarshal([]byte(cfg.SherlockProvider), &chartProvider); err != nil {
			return fmt.Errorf("-sherlock-provider is not valid JSON: %w", err)
		}
		if err := chartProvider.Validate(); err != nil {
			return fmt.Errorf("-sherlock-provider: %w", err)
		}
	}
	sherlockSvc := sherlock.New(sherlock.Options{
		CommandOK: func(line string) error { _, err := watson.Parse(line); return err },
		Client: &sherlock.Client{BaseURL: cfg.SherlockAPIURL, Key: keys.Key, HTTP: &http.Client{Timeout: 5 * time.Minute}, Retries: 2, Backoff: 2 * time.Second,
			Provider: func() sherlock.Provider {
				if p := shared.Current().SherlockOrDefault().Provider; p != nil && !cfg.SherlockLockProvider {
					return *p
				}
				return chartProvider
			}},
		Ledger: sherlock.NewLedger(spendBackend, nil, log.With("component", "sherlock")),
		Settings: func() sherlock.Settings {
			sh := shared.Current().SherlockOrDefault()
			return sherlock.Settings{Model: sh.Model, Caps: sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}, PriceIn: sh.PriceIn, PriceOut: sh.PriceOut}
		},
		Secrets: func() []string { return sherlock.SecretsOf(keys.Key()) },
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
	// A replica that becomes the leader reads again what Sherlock and
	// Watson rely on, which another replica may have changed: the spend so
	// far, the API key, the settings and each cluster's environment and
	// mode, which decide the two-person rule and read-only (P7-02).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-leading:
				sherlockSvc.Ledger().Reload()
				if err := keys.Reload(ctx); err != nil {
					log.Warn("reading the Claude API key failed", "err", err)
				}
				shared.Reload(ctx)
				if err := manager.Sync(ctx); err != nil {
					log.Warn("reading the cluster connections failed", "err", err)
				}
			}
		}
	}()

	// Watson (Phase 6): commands run as the person who approved them.
	watsonSvc := watson.New(watson.Options{
		Audit:  auditLog,
		Logger: log.With("component", "watson"),
		Apps:   appStore,
		TwoPerson: func(c watson.Cluster) bool {
			return c.Env == clusters.EnvProduction && shared.Current().WatsonOrDefault().TwoPerson
		},
		ApproveAllMax: func(c watson.Cluster) time.Duration {
			return time.Duration(shared.Current().WatsonOrDefault().ApproveAllMinutes) * time.Minute
		},
	})
	go watsonSvc.Watch(ctx, func(id string) (watson.Cluster, bool) {
		sum, ok := manager.Summary(id)
		if !ok {
			return watson.Cluster{}, false
		}
		_, client, ok := manager.Lookup(id)
		if !ok || client == nil {
			return watson.Cluster{}, false
		}
		return watson.Cluster{ID: id, Name: sum.Name, Env: sum.Env, ReadOnly: sum.Mode != clusters.ModeWrite, Config: client.RestConfig()}, true
	})
	// The toolbox (Phase 9): sessions open from approved Watson cards and
	// live on the leader, like the assistants (TB-14).
	toolboxCfg, err := toolbox.Read(cfg.ToolboxConfigFile)
	if err != nil {
		return err
	}
	if authCfg.Mode == auth.ModeNone {
		toolboxCfg.SSH.Enabled = false
	}
	// SSH to nodes (Phase 11, ADR-72): the console's certificate
	// authority lives in a Secret beside the connections, and the pairings
	// beside the settings.
	var caStore *kube.SecretKey
	var console string
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(restCfg, ns, prefix+"-ssh-ca", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "ssh-ca", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return fmt.Errorf("the SSH authority's Secret: %w", err)
		}
		caStore, console = sk, nodessh.ConsoleName(ns, prefix)
	} else {
		log.Warn("the SSH authority's key is kept in memory and made again on restart; paired nodes then need pairing again (set -connections-secrets to keep it in a Secret)")
	}
	sshSvc := nodessh.New(nodessh.Options{
		CA:       nodessh.NewCA(caStore),
		Pairings: nodessh.NewPairings(spendBackend),
		Console:  console,
		Logger:   log.With("component", "ssh"),
		NodeAddr: func(cluster, node string) (string, error) {
			_, client, ok := manager.Lookup(cluster)
			if !ok || client == nil {
				return "", fmt.Errorf("no connection %q", cluster)
			}
			nodes, ok := client.(interface{ Node(string) *corev1.Node })
			if !ok {
				return "", errors.New("this connection does not list nodes")
			}
			n := nodes.Node(node)
			if n == nil {
				return "", fmt.Errorf("node %s is not known here", node)
			}
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeInternalIP {
					return a.Address, nil
				}
			}
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeExternalIP || a.Type == corev1.NodeInternalDNS || a.Type == corev1.NodeHostName {
					return a.Address, nil
				}
			}
			return "", fmt.Errorf("node %s has no address", node)
		},
	})
	// Automation (Phase 11 part 2, ADR-73): scripts run over the SSH
	// above, with their records in a ConfigMap of their own, beside the
	// settings'. Runs live on the leader, like Watson's commands.
	var autoEngine *automation.Engine
	var autoStore *automation.Store
	if cfg.Automation {
		var autoBackend, runsBackend kv.Backend = &kv.Memory{}, nil
		if cfg.AutomationConfigMap != "" {
			cm, err := kube.NewConfigMapStorage(restCfg, cfg.AutomationConfigMap)
			if err != nil {
				return fmt.Errorf("automation's ConfigMap: %w", err)
			}
			l := limited(cm, objAutomation, log.With("component", "storage"))
			autoBackend, stored = l, append(stored, l)
			// The runs and the nodes' facts have a ConfigMap of their own when
			// the chart names one, so neither they nor the scripts press on the
			// other's 1 MiB; every replica is told the same, and one that
			// cannot read it does not start.
			if cfg.AutomationRunsConfigMap != "" {
				runs, err := openConfigMap(ctx, restCfg, cfg.AutomationRunsConfigMap)
				if err != nil {
					return fmt.Errorf("automation's runs: %w", err)
				}
				rl := limited(runs, objAutomationRuns, log.With("component", "storage"))
				runsBackend, stored = rl, append(stored, rl)
			}
		} else {
			log.Warn("Automation's scripts and runs are kept in memory and lost on restart (set -automation-configmap to keep them)")
		}
		outDir := ""
		if toolboxCfg.Transcripts == toolbox.TranscriptsPVC {
			outDir = toolboxCfg.TranscriptDir
		}
		autoStore = automation.NewStore(autoBackend, nil)
		if runsBackend != nil {
			autoStore.SetRunsBackend(runsBackend)
		}
		autoEngine = automation.NewEngine(automation.Options{
			Node: func(cluster, node string) (automation.NodeState, bool) {
				_, client, ok := manager.Lookup(cluster)
				if !ok || client == nil {
					return automation.NodeState{}, false
				}
				nodes, ok := client.(interface{ Node(string) *corev1.Node })
				if !ok {
					return automation.NodeState{}, false
				}
				n := nodes.Node(node)
				if n == nil {
					return automation.NodeState{}, false
				}
				st := automation.NodeState{}
				_, st.ControlPlane = n.Labels["node-role.kubernetes.io/control-plane"]
				if _, master := n.Labels["node-role.kubernetes.io/master"]; master {
					st.ControlPlane = true
				}
				for _, c := range n.Status.Conditions {
					if c.Type == corev1.NodeReady {
						st.Ready = c.Status == corev1.ConditionTrue
					}
				}
				return st, true
			},
			Store:       autoStore,
			Exec:        sshSvc,
			Unreachable: func(err error) bool { return errors.Is(err, nodessh.ErrUnreachable) },
			Logger:      log.With("component", "automation"),
			OutputDir:   outDir,
			OnCommand: func(r automation.Run, node, what, outcome string) {
				o := audit.OutcomeOK
				if outcome != "ok" {
					o = audit.OutcomeFailed
				}
				auditLog.Record(audit.Event{Action: audit.ActionAutomationCommand, Who: audit.Who{Subject: r.By.User, Name: r.By.Name}, Cluster: r.Cluster, Target: node, Outcome: o,
					Detail: fmt.Sprintf("%s: %s on %s, for %s: %s", r.Watson(), what, node, r.By.Name, r.Basis)})
			},
			OnEnd: func(r automation.Run) {
				c := r.Counts()
				outcome := audit.OutcomeFailed
				switch r.State {
				case automation.RunSucceeded:
					outcome = audit.OutcomeOK
				case automation.RunCancelled:
					outcome = audit.OutcomeStopped
				}
				auditLog.Record(audit.Event{Action: audit.ActionAutomationRun, Who: audit.Who{Subject: r.By.User, Name: r.By.Name}, Cluster: r.Cluster, Target: r.Script, Outcome: outcome,
					Detail: fmt.Sprintf("run %d %s: v%d on fleet %s, %d nodes: %d changed, %d no change, %d failed, %d unreachable, %d skipped, %d cancelled. %s", r.ID, r.State, r.Version, r.FleetName, len(r.Nodes),
						c[automation.NodeChanged], c[automation.NodeNoChange], c[automation.NodeFailed], c[automation.NodeUnreachable], c[automation.NodeSkipped], c[automation.NodeCancelled], r.Note)})
			},
		})
	}
	sessionsMgr := sessions.New(sessions.Options{
		Config:  toolboxCfg,
		SSH:     sshSvc,
		Audit:   auditLog,
		Logger:  log.With("component", "sessions"),
		History: &sessions.KVHistory{Backend: spendBackend, Key: sessions.HistoryKey, Keep: toolboxCfg.History.Keep, MaxBytes: historyMaxBytes, Logger: log.With("component", "sessions")},
	})
	go sessionsMgr.Run(ctx)
	if toolboxCfg.Transcripts == toolbox.TranscriptsPVC {
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				sessions.PruneTranscripts(toolboxCfg.TranscriptDir, toolboxCfg.Retention.D(), time.Now())
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	rolePrefixes := authCfg.Prefixes
	if def := roles.DefaultPrefixes(); rolePrefixes.Local == "" || rolePrefixes.LDAP == "" {
		if rolePrefixes.Local == "" {
			rolePrefixes.Local = def.Local
		}
		if rolePrefixes.LDAP == "" {
			rolePrefixes.LDAP = def.LDAP
		}
	}
	proxies, err := auth.ParseProxies(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	handler := api.New(api.Options{
		SSH:                     sshSvc,
		KV:                      spendBackend,
		Version:                 version.String(),
		Watson:                  watsonSvc,
		Apps:                    appStore,
		SecretsReveal:           cfg.SecretsRead && cfg.SecretsReveal,
		Sessions:                sessionsMgr,
		Automation:              autoEngine,
		AutomationStore:         autoStore,
		TwoPerson:               twoPerson,
		Sherlock:                sherlockSvc,
		SherlockKeys:            keys,
		SherlockDefaultURL:      cfg.SherlockAPIURL,
		SherlockProviderLocked:  cfg.SherlockLockProvider,
		SherlockDefaultProvider: chartProvider,
		Store:                   store,
		Cluster:                 cluster,
		Settings:                shared,
		Clusters:                manager,
		Auth:                    authSvc,
		Audit:                   auditLog,
		Access:                  people,
		Impersonation:           api.Impersonation{UserPrefix: cfg.ImpersonateUserPrefix, GroupPrefix: cfg.ImpersonateGroupPrefix},
		Roles:                   api.RolesOptions{Templates: authFile.RoleTemplates(), Secrets: cfg.SecretsRead, Exec: authFile.Templates.AdminExec, Enforce: authFile.RBACEnforce, Prefixes: rolePrefixes, Bindings: authFile.Bindings},
		HTTPSRedirect:           cfg.HTTPSRedirect,
		Leader:                  elector,
		Active:                  activity.Active,
		Standby:                 activity.Standby,
		Proxies:                 proxies,
		ProxyToken:              cfg.ProxyToken,
		Ready:                   cluster.Ready,
		UI:                      ui.Handler(),
		Logger:                  log.With("component", "http"),
		AllowedHosts:            cfg.AllowedHosts,
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
			gauges := []api.Gauge{}
			for _, o := range stored {
				sizes := o.Sizes()
				for _, key := range slices.Sorted(maps.Keys(sizes)) {
					gauges = append(gauges, api.Gauge{Name: "b221_configmap_bytes", Help: "Bytes each key of the console's ConfigMaps holds, as last read or saved; Kubernetes keeps at most 1 MiB in one ConfigMap.", Labels: fmt.Sprintf("configmap=%q,key=%q", o.Name, key), Value: float64(sizes[key])})
				}
			}
			unsaved := 0.0
			if sherlockSvc.Ledger().SaveError() != "" {
				unsaved = 1
			}
			gauges = append(gauges, api.Gauge{Name: "b221_sherlock_spend_unsaved", Help: "1 when Sherlock's spend could not be saved: it is counted on this replica only, and a restart forgets it.", Value: unsaved})
			gauges = append(gauges, []api.Gauge{
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
			}...)
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
	// The deployed roles are compared with the shipped ones every five
	// minutes, for the Permissions page, the audit log and rbac.enforce;
	// on the leader only, and at once when a replica becomes it.
	go handler.RunRoleChecks(ctx)
	activity.OnChange(func(active bool) {
		if active {
			handler.CheckRolesSoon()
		} else if autoEngine != nil {
			// Runs live on the leader: one that loses the lead stops its
			// runs, so the next leader never meets one still going.
			autoEngine.StopAll("the console running it lost the lead and stopped it; nodes mid-script were stopped, and nothing was retried")
		}
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
		log.Info("serving", "addr", cfg.Listen, "cluster", cfg.ClusterName, "mode", string(mode), "version", version.String(), "secrets_read", cfg.SecretsRead, "secrets_reveal", cfg.SecretsRead && cfg.SecretsReveal, "auth", cfg.AuthMode)
		errCh <- srv.ListenAndServe()
	}()
	lostLease := false
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	case <-lost:
		lostLease = true
		log.Error("the Lease was lost; a cold standby cannot watch again, so this replica exits and starts afresh (replicas.warmStandby=false)")
		stop()
	}
	log.Info("shutting down")
	workCtx, cancelWork := context.WithTimeout(context.Background(), workWithin(cfg.TerminationGrace))
	defer cancelWork()
	shutdownCtx, cancel := context.WithTimeout(workCtx, 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	// Answers and commands outlive their requests, and each writes its
	// audit record as it ends, so the log closes after them.
	var work sync.WaitGroup
	work.Go(func() { sherlockSvc.Drain(workCtx, settleWithin) })
	work.Go(func() { watsonSvc.Drain(workCtx, settleWithin) })
	work.Wait()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), closeWithin)
	defer cancelClose()
	auditLog.Close(closeCtx)
	// The leader gives its Lease up on the way out, so another replica
	// takes over at once.
	select {
	case <-leaderDone:
	case <-closeCtx.Done():
	}
	if lostLease {
		return errors.New("the Lease was lost; a cold standby exits to start afresh")
	}
	return err
}

// What a stopping console does inside the pod's termination grace period:
// answers and commands get what is left of it after these. settleWithin is
// for those still running to end once stopped (a helm upgrade marks its
// release failed, rather than leave it pending), closeWithin for the audit
// log to reach its store and webhook.
const (
	settleWithin = 2 * time.Second
	closeWithin  = 3 * time.Second
	stopMargin   = time.Second
)

// workWithin is how long a stopping console waits for answers and commands
// to finish by themselves, counted from the signal.
func workWithin(grace time.Duration) time.Duration {
	return max(grace-settleWithin-closeWithin-stopMargin, 0)
}

// keepAudit gives the log its store for Administration › Audit log, and
// returns it to close: beside the settings, or in memory on a laptop, where
// the page says so.
func keepAudit(l *audit.Log, b kv.Backend, logger *slog.Logger) *audit.KVStore {
	if _, inMemory := b.(*kv.Memory); inMemory {
		l.KeepInMemory(audit.DefaultKeep)
		return nil
	}
	st := audit.NewKVStore(b, logger)
	l.SetStore(st)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := st.Sweep(ctx); err != nil {
			logger.Warn("cleaning the kept audit log failed", "err", err)
		}
	}()
	return st
}

// roleSwitch is what the election drives (ADR-70): winning the Lease makes
// the replica active, and losing it makes it a standby. A cold standby
// cannot stand by again, since its informers cannot restart, so losing
// the Lease makes it exit instead; a warm one only stops its pollers.
func roleSwitch(act *leader.Activity, exit func()) (onLead, onStop func()) {
	return func() { act.Set(true) }, func() {
		act.Set(false)
		if !act.Warm() {
			exit()
		}
	}
}

// newElector joins the election of -leader-lease, or stands alone. The
// leader's address is the pod's IP and the port it listens on.
func newElector(cfg config.Config, restCfg *rest.Config, log *slog.Logger, onLead, onStop func()) (*leader.Elector, error) {
	if cfg.LeaderLease == "" {
		return leader.New(leader.Options{Logger: log.With("component", "leader"), OnStartedLeading: onLead, OnStoppedLeading: onStop}), nil
	}
	ns, name, _ := strings.Cut(cfg.LeaderLease, "/")
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("-listen %q: %w", cfg.Listen, err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return leader.New(leader.Options{Client: client, Namespace: ns, Name: name, Pod: cfg.PodName, Addr: net.JoinHostPort(cfg.PodIP, port), Logger: log.With("component", "leader"), OnStartedLeading: onLead, OnStoppedLeading: onStop}), nil
}

// localSpec describes the cluster the console was started against as a
// connection: its address and how the console signs in, for the cluster
// page; it is not editable beyond its name, environment and description.
func localSpec(cfg config.Config, restCfg *rest.Config, mode kube.Mode) clusters.Spec {
	spec := clusters.Spec{Name: cfg.ClusterName, Env: clusters.EnvOther, Server: restCfg.Host, Mode: cfg.LocalMode}
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
