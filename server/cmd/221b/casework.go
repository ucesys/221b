package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Case work (Phase 12, CW-01 to CW-14, ADR-105 to ADR-107) ---
//
// The cases: their ConfigMap (<release>-casework), the case analysis job
// over the jobs' service, the cards' store for the changes they propose,
// and their part of a backup. The jobs' findings (casework.Findings) and
// the lessons' RCAs (casework.Lessons) are given with SetFindings and
// SetLessons by the packages that keep them.

// openCasework opens the cases' work: Settings › Cases, the checks seen
// and the analyses under way in <release>-casework when the chart names
// it, in memory otherwise; the work on each case beside it in the case's
// own ConfigMap; the exceptions and RCAs (lessons) in the casework
// ConfigMap too; and the case analysis job, registered. It returns the
// ConfigMap too, for the metrics of its size and the lessons' records.
func openCasework(ctx context.Context, cfg config.Config, restCfg *rest.Config, manager *clusters.Manager, js *jobs.Service, iss *issues.Service, auditLog *audit.Log, leads func() bool, log *slog.Logger) (*casework.Service, *kv.Limited, error) {
	var backend kv.Backend = &kv.Memory{}
	var stored *kv.Limited
	if cfg.CaseworkConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.CaseworkConfigMap)
		if err != nil {
			return nil, nil, fmt.Errorf("the cases' ConfigMap: %w", err)
		}
		stored = limited(cm, objCasework, log.With("component", "storage"))
		backend = stored
	} else {
		log.Warn("the cases' settings are kept in memory and lost on restart (set -casework-configmap to keep them)")
	}
	svc := casework.New(casework.Options{
		Backend: backend,
		Jobs:    js,
		// The cases themselves are the issue service's (Phase 12, wave 5),
		// each in its own ConfigMap (wave 6).
		Issues: iss,
		Clusters: func() []jobs.Cluster {
			var out []jobs.Cluster
			for _, c := range manager.List() {
				out = append(out, jobs.Cluster{ID: c.ID, Name: c.Name})
			}
			return out
		},
		CommandOK: func(line string) error { _, err := watson.Parse(line); return err },
		Leads:     leads,
		Audit:     auditLog,
		Logger:    log.With("component", "casework"),
	})
	jobs.Register(svc.Kind())
	return svc, stored, nil
}

// registerCaseworkBackup adds the cases to every backup (order 260): each
// case's ConfigMap whole (the issue service's record and the work on it),
// Settings › Cases and the check ids seen (CF-22).
func registerCaseworkBackup(r *backup.Registry, svc *casework.Service) {
	if svc == nil {
		return
	}
	r.Register(backup.Part{Name: "casework", Label: "Cases", Order: 260,
		How:     "are added where missing: each case's ConfigMap whole, its record, analyses, proposed changes, fix, RCA, history and drafts; a case here stays as it is; Settings › Cases are replaced",
		LeftOut: []string{"the cases' analysis runs (a history in the jobs' runs; analyse a case again for a new one)", "the analyses under way"},
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			x, err := svc.Export(ctx)
			if err != nil || x == nil {
				return nil, err
			}
			return x, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			added, kept, err := svc.Import(ctx, raw, env.By)
			if err != nil {
				env.Note("cases: %v", err)
				return 0, backup.ErrNotApplied
			}
			if kept > 0 {
				env.Note("cases: %d were here already and stayed as they are", kept)
			}
			return added, nil
		},
	})
}

// --- end of case work.
