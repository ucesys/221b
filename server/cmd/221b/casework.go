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
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/mail"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Case work (Phase 12, CW-01 to CW-14, ADR-105 to ADR-107) ---
//
// The cases: their ConfigMap (<release>-casework), the case analysis job
// over the jobs' service, the cards' store for the changes they propose,
// and their part of a backup. The jobs' findings (casework.Findings) and
// the lessons' RCAs (casework.Lessons) are given with SetFindings and
// SetLessons by the packages that keep them.

// openCasework opens the cases' work: Administration › AI › Cases, the checks seen
// and the analyses under way where wrap keeps them, the database, with
// the exceptions and the fixes; and the case analysis job, registered.
// <release>-casework, when the chart names it, is the one-time copy's
// source and nothing else: where a release before 0.13 kept them. It is
// returned too, for the metrics of its size.
func openCasework(ctx context.Context, cfg config.Config, restCfg *rest.Config, manager *clusters.Manager, js *jobs.Service, iss *issues.Service, auditLog *audit.Log, leads func() bool, log *slog.Logger, wrap func(kv.Backend) kv.Backend) (*casework.Service, *kv.Limited, error) {
	var backend kv.Backend
	var stored *kv.Limited
	cm, err := openSource(ctx, restCfg, cfg.CaseworkConfigMap)
	if err != nil {
		return nil, nil, fmt.Errorf("the cases' ConfigMap: %w", err)
	}
	if cm != nil {
		stored = limited(cm, objCasework, log.With("component", "storage"))
		backend = stored
	}
	// Everything the service keeps is kept in the database; a caller that
	// gives no wrap (a test) keeps it in memory.
	if wrap != nil {
		backend = wrap(backend)
	} else if backend == nil {
		backend = &kv.Memory{}
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
// Administration › AI › Cases and the check ids seen (CF-22).
func registerCaseworkBackup(r *backup.Registry, svc *casework.Service) {
	if svc == nil {
		return
	}
	r.Register(backup.Part{Name: "casework", Label: "Cases", Order: 260,
		How:     "are added where missing: each case's ConfigMap whole, its record, analyses, proposed changes, fix, RCA, history and drafts; a case here stays as it is; Administration › AI › Cases are replaced",
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
	caseSvc, caseStored, err := openCasework(ctx, cfg, w.restCfg, w.manager, w.jobsW.svc, w.issueSvc, w.auditLog, w.elector.IsLeader, log, func(b kv.Backend) kv.Backend {
		// Administration › AI › Cases, with the counter and the analyses under
		// way: a scope of the database's settings (Phase 13). So are the
		// two marks of what ran once, which were the last of the case work
		// kept in its ConfigMap (DB-69): the clean start's, which says a
		// clean start with this token ran (without it the cases would be
		// deleted again), and the mark of the fixes' one import.
		w.caseBackend = w.inScopes(ctx, b, map[string]string{casework.SettingsKey: "cases", casework.CleanKey: "clean-start", casework.FixesImportKey: "fixes-import"})
		// The checks seen on each cluster, the validation rules and what
		// the runs found are rows (DB-43); so are the accepted exceptions
		// and the fixes, which the lessons and the fixes keep beside the
		// cases, in the same ConfigMap (DB-47).
		w.caseBackend = w.inTables(ctx, w.caseBackend, func(d *db.DB) map[string]movedDoc {
			return map[string]movedDoc{casework.ChecksKey: casework.NewChecksPG(d), casework.RulesKey: casework.NewRulesPG(d), casework.FoundKey: casework.NewFoundPG(d),
				lessons.ExceptionsKey: lessons.NewExceptionsPG(d), fixes.Key: fixes.NewPG(d)}
		})
		return w.caseBackend
	})
	if err != nil {
		return err
	}
	if caseStored != nil {
		w.stored = append(w.stored, caseStored)
	}
	caseSvc.SetCards(w.cardStore)
	// Email about cases (EM-01 to EM-10): Settings › Email beside
	// Administration › AI › Cases, the people's addresses from the cases, the local
	// accounts and the directory.
	{
		// Settings › Email: a scope of the database's settings (Phase 13),
		// copied once from the casework ConfigMap of a release before.
		var from kv.Backend
		if caseStored != nil {
			from = caseStored
		}
		mailBackend := w.inScopes(ctx, from, map[string]string{mail.ConfigKey: "email"})
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
	// Through the case work's backend, which keeps the exceptions and the
	// fixes in the database.
	lessonsBackend := w.caseBackend
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
	// cluster; the exceptions go stale after Administration › AI › Cases' days.
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
		// Through the backends the services read, so what the database
		// keeps is cleaned there, and what a release before 0.13 left in
		// a ConfigMap under a key of its own is deleted there.
		st := casework.CleanStores{Cases: w.issueSvc.Cases(), CasesInDatabase: true,
			Issues: w.issueBackend, Casework: w.caseBackend, Runs: w.jobsW.backend, Cards: w.cardsBackend, Knowledge: w.knowledgeBackend}
		// The clean start goes by its mark (casework.CleanKey), which was
		// kept in the casework ConfigMap. A start that could not copy the
		// mark into the database must not take it for missing: it would
		// delete the cases a second time. It waits for the next start.
		cleanStart := !w.notCopied[casework.CleanKey]
		if !cleanStart && cfg.CleanStartToken != "" {
			log.Error("the clean start is not run at this start: its mark of the last clean start could not be copied from the casework ConfigMap into the database, and without it the cases would be deleted again; the next start tries again")
		}
		go func() {
			if cleanStart {
				runCleanStart(ctx, cfg, st, w.knowledgeSvc, w.elector.IsLeader, w.auditLog, log)
			}
			// The fixes of the cases closed as Fixed before (CF-54), once,
			// after any clean start.
			if !w.notCopied[casework.FixesImportKey] {
				runFixesImport(ctx, caseSvc, w.elector.IsLeader, log)
			}
		}()
	}
	go caseSvc.Run(ctx)
	w.caseStored, w.caseSvc, w.lessonsSvc = caseStored, caseSvc, lessonsSvc
	return nil
}
