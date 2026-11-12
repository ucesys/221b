package main

import (
	"context"
	"time"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/watson"
)

// Watson (Phase 6).

// wireWatson starts Watson.
func (w *wiring) wireWatson(ctx context.Context) {
	log := w.log
	// Watson (Phase 6): commands run as the person who approved them.
	watsonSvc := watson.New(watson.Options{
		Audit:  w.auditLog,
		Logger: log.With("component", "watson"),
		Apps:   w.appStore,
		Repos:  w.helmRepos,
		TwoPerson: func(c watson.Cluster) bool {
			return c.Env == clusters.EnvProduction && w.shared.Current().WatsonOrDefault().TwoPerson
		},
		ApproveAllMax: func(c watson.Cluster) time.Duration {
			return time.Duration(w.shared.Current().WatsonOrDefault().ApproveAllMinutes) * time.Minute
		},
	})
	go watsonSvc.Watch(ctx, func(id string) (watson.Cluster, bool) {
		sum, ok := w.manager.Summary(id)
		if !ok {
			return watson.Cluster{}, false
		}
		_, client, ok := w.manager.Lookup(id)
		if !ok || client == nil {
			return watson.Cluster{}, false
		}
		return watson.Cluster{ID: id, Name: sum.Name, Env: sum.Env, ReadOnly: sum.Mode != clusters.ModeWrite, Config: client.RestConfig()}, true
	})
	w.watsonSvc = watsonSvc
}
