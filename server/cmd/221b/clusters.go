package main

import (
	"context"
	"fmt"
	"strings"

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
		ss, err := clusters.NewSecretStore(w.restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		connStore = ss
		as, err := apps.NewSecretStore(w.restCfg, ns, prefix)
		if err != nil {
			return err
		}
		appStore = as
	} else {
		log.Warn("cluster connections added in the console are kept in memory and lost on restart; set -connections-secrets namespace/prefix to keep them in Secrets")
	}
	manager := clusters.New(connStore, clusters.Options{Refresh: cfg.Refresh, HealthInterval: cfg.HealthInterval, KubeletStats: cfg.KubeletStats, SecretsRead: cfg.SecretsRead, Logger: log.With("component", "clusters"), Activity: w.activity})
	// SetLocal runs the local cluster's watches, pollers and store as the
	// role says, like every other connection's.
	if err := manager.SetLocal(ctx, localSpec(cfg, w.restCfg, w.mode), w.cluster, w.store); err != nil {
		return fmt.Errorf("local connection: %w", err)
	}
	w.appStore, w.manager = appStore, manager
	return nil
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
