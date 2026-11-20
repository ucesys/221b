package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/roles"
)

// issueLimits are what the issue service fits its records to: the limits
// of its ConfigMap's keys.
func issueLimits() issues.Limits {
	l := layout[objIssues]
	return issues.Limits{IndexBytes: l[issues.IndexKey].Bytes, KnownBytes: l[issues.KnownKey].Bytes, ConfigBytes: l[issues.ConfigKey].Bytes}
}

// openIssues builds the issue service (Phase 12, docs/issues.md): its
// records in a ConfigMap of its own when the chart names one, each case
// in a ConfigMap of its own the console makes (<release>-case-<n>, Phase
// 12 wave 6, CF-20), in memory otherwise, and its records of who did what
// in the console's audit log.
// It returns the ConfigMap too, for the metrics of its size. inv is the
// investigator (the report triage job, Phase 12); nil finds nothing. The
// manager's clusters are the ones a reporter may say a report is about
// (CF-64).
//
// moved keeps the records in the database when there is one (Phase 13);
// nil leaves them where they are.
func openIssues(ctx context.Context, cfg config.Config, restCfg *rest.Config, manager *clusters.Manager, auditLog *audit.Log, inv issues.Investigator, moved func(kv.Backend, kv.Objects) (kv.Backend, kv.Objects), log *slog.Logger) (*issues.Service, *kv.Limited, error) {
	var backend kv.Backend = &kv.Memory{}
	var cases kv.Objects = &kv.MemoryObjects{}
	var stored *kv.Limited
	if cfg.CaseConfigMaps != "" {
		objs, err := kube.NewConfigMapObjects(restCfg, cfg.CaseConfigMaps, cfg.CaseInstance, "221b.io/case")
		if err != nil {
			return nil, nil, err
		}
		cases = objs
	}
	if cfg.IssuesConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.IssuesConfigMap)
		if err != nil {
			return nil, nil, fmt.Errorf("the issue service's ConfigMap: %w", err)
		}
		stored = limited(cm, objIssues, log.With("component", "storage"))
		backend = stored
	} else {
		log.Warn("the issue service's reports are kept in memory and lost on restart (set -issues-configmap to keep them)")
	}
	if moved != nil {
		backend, cases = moved(backend, cases)
	}
	svc := issues.New(issues.Options{
		Backend: backend,
		Cases:   cases,
		Limits:  issueLimits(),
		Audit: func(a issues.AuditRecord) {
			auditLog.Record(audit.Event{Action: a.Action, Who: audit.Who{Subject: a.Who.ID, Email: a.Who.Email, Name: a.Who.Name}, Target: a.Target, Detail: a.Detail, Outcome: a.Outcome, From: a.From})
		},
		Investigator: inv,
		Clusters: func() []issues.Cluster {
			var out []issues.Cluster
			for _, c := range manager.List() {
				out = append(out, issues.Cluster{ID: c.ID, Name: c.Name})
			}
			return out
		},
		ReportURL: cfg.IssuesReportURL,
		Logger:    log.With("component", "issues"),
	})
	return svc, stored, nil
}

// issueRoles are the chart's issues.reportRoles and issues.operatorRoles.
func issueRoles(cfg config.Config) api.IssueRoles {
	var out api.IssueRoles
	for _, r := range cfg.IssuesReportRoles {
		out.Report = append(out.Report, roles.ID(r))
	}
	for _, r := range cfg.IssuesOperatorRoles {
		out.Operate = append(out.Operate, roles.ID(r))
	}
	return out
}

// wireIssues opens the issue service, which keeps the cases.
func (w *wiring) wireIssues(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// The issue service (Phase 12, docs/issues.md): reports, known issues
	// and services in a ConfigMap of its own; its timers run on the leader.
	// Cases replace reports (Phase 12, wave 5, ADR-113): the cases are
	// kept there whether or not people report problems (issues.enabled,
	// which offers the report page and the issue API, issueAPI below).
	var issueSvc, issueAPI *issues.Service
	var issueStored *kv.Limited
	svc, l, err := openIssues(ctx, cfg, w.restCfg, w.manager, w.auditLog, w.jobsW.investigator(), w.casesInDatabase(ctx), log)
	if err != nil {
		return err
	}
	if l != nil {
		w.stored = append(w.stored, l)
	}
	issueStored = l
	issueSvc = svc
	w.knownSvc = svc
	w.jobsW.setIssues(issueSvc)
	if w.keepsRecords() {
		go issueSvc.Run(ctx, 5*time.Minute, w.activity.Active)
	}
	if cfg.Issues {
		issueAPI = issueSvc
	}
	w.issueSvc, w.issueAPI, w.issueStored = issueSvc, issueAPI, issueStored
	return nil
}

// casesInDatabase keeps the issue service's records in the database when
// there is one (Phase 13, DB-52 to DB-54): the index, the known issues and
// the settings as one set of documents at one version, and each case as a
// row of cases with the work on it and its conversation with Sherlock,
// which the case work keeps in the same object. What the ConfigMaps held
// is copied once: the case objects under one name, the three documents
// under another.
func (w *wiring) casesInDatabase(ctx context.Context) func(kv.Backend, kv.Objects) (kv.Backend, kv.Objects) {
	return func(b kv.Backend, objs kv.Objects) (kv.Backend, kv.Objects) {
		w.issueBackend = b
		if w.database == nil {
			return b, objs
		}
		w.inDatabase = append(w.inDatabase, "cases")
		log := w.log.With("component", "database")
		cases := issues.NewCasesPG(w.database, casework.CaseParts())
		w.claimImport(cases.ImportID())
		set := issues.NewSetPG(w.database)
		w.claimImport(set.ImportID())
		// The three documents first, then the cases: the index's lines are
		// read from the cases' rows, so an index copied after them would
		// look kept already, and its counter would not be copied. The cases
		// wait for the documents' copy for the same reason.
		setCopied := true
		if _, inMemory := b.(*kv.Memory); !inMemory && w.keepsRecords() {
			ictx, cancel := context.WithTimeout(ctx, time.Minute)
			copied, err := set.Import(ictx, b, "configmap")
			cancel()
			if err != nil {
				setCopied = false
				log.Warn("copying records from their ConfigMap into the database failed; the next start tries again", "keys", "the issue service's", "err", err)
			}
			for _, key := range copied {
				log.Info("copied records from their ConfigMap into the database", "key", key)
			}
			if err == nil {
				for key := range set.Members {
					w.removeCopied(ctx, b, set.ImportID(), key)
				}
			}
		}
		if _, inMemory := objs.(*kv.MemoryObjects); !inMemory && setCopied && w.keepsRecords() {
			// One read of the cluster for each case: allow for many.
			ictx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			n, err := cases.Import(ictx, objs, "configmap")
			cancel()
			if err != nil {
				log.Warn("copying the cases from their ConfigMaps into the database failed; the next start tries again", "err", err)
			} else if n > 0 {
				log.Info("copied the cases from their ConfigMaps into the database", "cases", n)
			}
			if err == nil {
				w.removeCopiedObjects(ctx, objs, cases.ImportID())
			}
		}
		keys := map[string]kv.Backend{}
		for key := range set.Members {
			keys[key] = set
		}
		w.issueBackend = &kv.Keyed{Keys: keys, Rest: b}
		return w.issueBackend, cases
	}
}
