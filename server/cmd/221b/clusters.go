package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/leader"
	"github.com/ucesys/221b/server/internal/live"
)

// The clusters: the one the console was started against, and the
// connections to the others.

// wireLocalCluster connects to the cluster the console was started
// against, with its store of what the tabs show.
func (w *wiring) wireLocalCluster() error {
	cfg, log := w.cfg, w.log
	cluster, err := kube.New(w.restCfg, kube.Options{
		Name:           cfg.ClusterName,
		Mode:           w.mode,
		HealthInterval: cfg.HealthInterval,
		KubeletStats:   cfg.KubeletStats,
		SecretsRead:    cfg.SecretsRead,
		Protobuf:       w.mode != kube.ModeURL,
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
	w.cluster, w.activity, w.store = cluster, activity, store
	return nil
}

// wireClusters opens the connections to the clusters, the local one
// among them, and the Helm applications kept beside them.
func (w *wiring) wireClusters(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// The other clusters (P3-04 to P3-09): their connections live in
	// Secrets in the console's own namespace when running in a cluster,
	// otherwise in memory; with a database as well, each is a row and its
	// Secret holds its credential alone (DB-64). The cluster the console
	// was started against is the connection "local".
	var connStore clusters.Store = clusters.NewMemoryStore()
	// The Helm applications (Phase 10) are kept beside the connections.
	var appStore apps.Store = apps.NewMemoryStore()
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, ok := strings.Cut(cfg.ConnectionsSecrets, "/")
		if !ok || ns == "" || prefix == "" {
			return fmt.Errorf("-connections-secrets must be namespace/prefix, got %q", cfg.ConnectionsSecrets)
		}
		ss, err := clusters.NewSecretStore(w.restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		connStore = w.connections(ctx, ss)
		as, err := apps.NewSecretStore(w.restCfg, ns, prefix)
		if err != nil {
			return err
		}
		appStore = w.applications(ctx, as)
	} else {
		// In memory, with a database or without: the record set's name is
		// still its own.
		w.claimImport(apps.ImportID)
		w.claimImport(clusters.ImportID)
		log.Warn("cluster connections added in the console are kept in memory and lost on restart; set -connections-secrets namespace/prefix to keep them in Secrets")
	}
	manager := clusters.New(connStore, clusters.Options{Refresh: cfg.Refresh, HealthInterval: cfg.HealthInterval, KubeletStats: cfg.KubeletStats, SecretsRead: cfg.SecretsRead, Logger: log.With("component", "clusters"), Activity: w.activity})
	// SetLocal runs the local cluster's watches, pollers and store as the
	// role says, like every other connection's.
	if err := manager.SetLocal(ctx, localSpec(cfg, w.restCfg, w.mode), w.cluster, w.store); err != nil {
		return fmt.Errorf("local connection: %w", err)
	}
	if _, ok := connStore.(*clusters.PGStore); ok {
		// What another replica saves or removes is here at once; the sync
		// every few seconds stays, so nothing depends on the notice.
		w.database.Subscribe(clusters.Topic, manager.SyncSoon)
	}
	w.appStore, w.manager = appStore, manager
	return nil
}

// moveAgainEvery is how often a copy of the connections that failed at
// the start is tried again while the console runs.
const moveAgainEvery = 30 * time.Second

// connections is where the cluster connections are kept when the console
// keeps them in Secrets: with a database each is a row of clusters
// (DB-64) and its Secret holds its credential alone; without one, the
// Secrets hold all of it, as before. What the Secrets held of the
// connections ("spec.json") is copied once, and then leaves the Secrets
// whose connection has a row: no Secret is deleted, no key of a credential
// is touched, and a Secret whose record could not be copied is left whole.
//
// A copy that fails does not stop the start, and the Secrets stay as they
// were; but the console would then show the local cluster alone until its
// next start, so the copy is tried again while it runs, and the
// connections start when it is made (Manager.Run).
//
// With a database and the connections in memory (no -connections-secrets)
// they stay in memory, as the credentials' list and the applications do.
func (w *wiring) connections(ctx context.Context, secrets *clusters.SecretStore) clusters.Store {
	if w.database == nil {
		w.claimImport(clusters.ImportID)
		return secrets
	}
	log := w.dbLog()
	const what = "the cluster connections"
	copy := func(ctx context.Context) (int, error) { return clusters.CopyFromSecrets(ctx, w.database, secrets, log) }
	drop := func(ctx context.Context) (int, error) { return clusters.DropSpecs(ctx, w.database, secrets, log) }
	w.moveFromSecrets(ctx, clusters.ImportID, what, copy, drop)
	connLog := log
	if w.log != nil {
		connLog = w.log.With("component", "clusters")
	}
	store := clusters.NewPGStore(w.database, secrets, connLog)
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if copied, err := w.database.Imported(cctx, clusters.ImportID); err != nil || !copied {
		// It failed, the database could not say, or another replica is at
		// it this moment: this one looks again until the copy is made.
		every := w.moveAgainEvery
		if every <= 0 {
			every = moveAgainEvery
		}
		go func() {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				tctx, cancel := context.WithTimeout(ctx, time.Minute)
				n, err := copy(tctx)
				copied, _ := w.database.Imported(tctx, clusters.ImportID)
				cancel()
				if err != nil || !copied {
					continue
				}
				log.Info("the cluster connections are in the database now; their credentials stay in their Secrets", "copied_here", n)
				w.removeCopiedFromSecrets(ctx, clusters.ImportID, what, drop)
				return
			}
		}()
		return store
	}
	// The two halves that do not go together, said once at the start: a
	// database restored to an earlier time, or a write cut short. Nothing
	// is removed on that guess.
	if noSecret, noRow, err := store.Unmatched(cctx); err != nil {
		log.Warn("the connections in the database and their Secrets could not be compared", "err", err)
	} else if len(noSecret) > 0 || len(noRow) > 0 {
		log.Warn("the connections in the database and their Secrets do not match: a connection without its Secret has nothing to sign in with until it is edited and given a credential, and a Secret without a connection is kept and never read",
			"connections_without_secret", noSecret, "secrets_without_connection", noRow)
	}
	return store
}

// applications is where the applications are kept when the console keeps
// its connections in Secrets: with a database, their records are rows
// (DB-62) and only their values, parameters and commands' parameters stay
// in their Secrets; without one, the Secrets hold all of it, as before.
// What the Secrets held of the records is copied once, and the Secrets are
// then rewritten to hold the secret part alone (DB-63): no Secret is
// deleted, and one whose record could not be copied is left whole.
//
// With a database and the connections in memory (no -connections-secrets:
// the simulator, a console run by hand) the applications stay in memory
// too, as the credentials' list does: rows that outlived the values they
// are deployed with would be applications nobody could deploy.
func (w *wiring) applications(ctx context.Context, secrets *apps.SecretStore) apps.Store {
	if w.database == nil {
		w.claimImport(apps.ImportID)
		return secrets
	}
	log := w.dbLog()
	w.moveFromSecrets(ctx, apps.ImportID, "the applications",
		func(ctx context.Context) (int, error) { return apps.CopyFromSecrets(ctx, w.database, secrets, log) },
		func(ctx context.Context) (int, error) { return apps.RewriteSecrets(ctx, w.database, secrets, log) })
	appLog := log
	if w.log != nil {
		appLog = w.log.With("component", "applications")
	}
	store := apps.NewPGStore(w.database, secrets, appLog)
	// The two halves that do not go together, said once at the start: a
	// database restored to an earlier time, or a write cut short. Nothing
	// is removed on that guess.
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if noSecret, noRow, err := store.Unmatched(cctx); err != nil {
		log.Warn("the applications in the database and their Secrets could not be compared", "err", err)
	} else if len(noSecret) > 0 || len(noRow) > 0 {
		log.Warn("the applications in the database and their Secrets do not match: an application without its Secret reads with no values until it is saved again, and a Secret without an application is kept and never read",
			"applications_without_secret", noSecret, "secrets_without_application", noRow)
	}
	return store
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
