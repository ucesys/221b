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
		LeftOut: []string{"the cases' analysis runs (a history in the jobs' runs; analyze a case again for a new one)", "the analyses under way"},
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

// wireCasework opens the case work, with the exceptions, the RCAs and
// the fixes, and the clean start.
func (w *wiring) wireCasework(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Case work (Phase 12, ADR-105 to ADR-107; wave 6): the cases'
	// settings in <release>-casework, each case in its own ConfigMap; their
	// analysis is a job, and the changes it proposes are cards. The runs'
	// findings are routed into cases (jobs.Router), and the exceptions and
	// RCAs are the lessons'.
	caseSvc, caseStored, err := openCasework(ctx, cfg, w.restCfg, w.manager, w.jobsW.svc, w.issueSvc, w.auditLog, w.elector.IsLeader, log)
	if err != nil {
		return err
	}
	if caseStored != nil {
		w.stored = append(w.stored, caseStored)
	}
	caseSvc.SetCards(w.cardStore)
	// Email about cases (EM-01 to EM-10): Settings › Email beside
	// Settings › Cases, the people's addresses from the cases, the local
	// accounts and the directory.
	{
		var mailBackend kv.Backend
		if caseStored != nil {
			mailBackend = caseStored
		}
		w.mailSvc = openMail(cfg, w.restCfg, mailBackend, w.cardStore, w.auditLog, log)
		caseSvc.SetMail(w.mailSvc, func(id string) string {
			if w.authSvc == nil {
				return ""
			}
			return w.authSvc.EmailOf(id)
		})
	}
	// A proposed change names the cases it resolves (Phase 12, wave
	// 6b, CF-29): a card applied, failed, rejected or expired acts on the
	// cases it resolves; the cases' leader tick catches up with what this
	// hook misses. The cases check what a run's proposals name
	// (jobs.Resolver, the router set below).
	w.cardStore.OnChange(caseSvc.CardChanged)
	// Findings, exceptions and RCAs (Phase 12, ADR-108 to ADR-110):
	// the accepted exceptions the runs leave out, and the cases' RCAs, in
	// the casework ConfigMap beside the cases; written to the wiki too when
	// one is kept.
	var lessonsBackend kv.Backend
	if caseStored != nil {
		lessonsBackend = caseStored
	}
	lessonsSvc := openLessons(lessonsBackend, w.auditLog, w.knowledgeSvc, log)
	// Fixes (wave 6g, CF-50 to CF-55): beside the exceptions; the
	// cases save them, the agents read the active ones, the daily
	// housekeeping keeps them from repeating.
	w.fixesSvc = openFixes(lessonsBackend, w.auditLog, log)
	w.exceptionsSvc = lessonsSvc
	caseSvc.SetFixes(w.fixesSvc)
	// Mycroft is given the active fixes on the subject of what it writes
	// (KN-33), and the housekeeping offers it the fixes proven in cases
	// for a runbook (KN-34).
	w.knowledgeSvc.SetFixes(knowledgeFixes{w.fixesSvc})
	caseSvc.SetRunbooks(w.knowledgeSvc)
	jobs.Register(caseSvc.HousekeepingKind())
	// One flow for findings and cases (Phase 12, wave 6): each finding
	// of a run becomes a case or joins one; its object is checked on its
	// cluster; the exceptions go stale after Settings › Cases' days.
	caseSvc.SetLessons(lessonsSvc)
	lessonsSvc.SetStaleDays(caseSvc.StaleDays)
	// Mycroft › Exceptions names each exception's cluster, and New
	// exception offers the connected clusters with the validation rules
	// seen on each (build 14).
	lessonsSvc.SetClusters(caseSvc.ClusterList, caseSvc.ChecksSeen)
	w.jobsW.svc.SetRouter(caseSvc)
	// The validation rules as they stand (ADR-138): what the agents are
	// offered and their findings are checked against.
	w.jobsW.svc.SetRules(caseSvc.Rules().Book)
	jobs.SetRuleMeaning(caseSvc.Rules().Meaning)
	w.jobsW.svc.SetObjects(objectsOf(w.manager))
	// Mycroft's inventory and drift check leave out what an open known
	// issue or an active exception covers (KN-31).
	w.knowledgeSvc.SetCovers(knowledgeCovers{issues: w.issueSvc, lessons: lessonsSvc, rules: caseSvc.Rules().Book})
	// Alerts and cases (CF-77 to CF-79): an alert run that sent its
	// findings to a case acknowledges its alert in the job's name, and the
	// cases' safety net watches the alerts the jobs' leader reads.
	w.jobsW.svc.SetAcker(alertAcker{manager: w.manager, settings: w.shared})
	w.jobsW.svc.SetAlertWatcher(caseSvc.WatchAlerts)
	// The clean start (CF-23) runs once on the leader, before the cases'
	// loops start.
	{
		st := casework.CleanStores{Cases: w.issueSvc.Cases()}
		if w.issueStored != nil {
			st.Issues = w.issueStored
		}
		if caseStored != nil {
			st.Casework = caseStored
		}
		if w.jobsW.stored != nil {
			st.Runs = w.jobsW.stored
		}
		if w.cardsStored != nil {
			st.Cards = w.cardsStored
		}
		if w.knowledgeStored != nil {
			st.Knowledge = w.knowledgeStored
		}
		go func() {
			runCleanStart(ctx, cfg, st, w.knowledgeSvc, w.elector.IsLeader, w.auditLog, log)
			// The fixes of the cases closed as Fixed before (CF-54), once,
			// after any clean start.
			runFixesImport(ctx, caseSvc, w.elector.IsLeader, log)
		}()
	}
	go caseSvc.Run(ctx)
	w.caseStored, w.caseSvc, w.lessonsSvc = caseStored, caseSvc, lessonsSvc
	return nil
}
