package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/investigate"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/views"
	"github.com/ucesys/221b/server/internal/watson"
)

// Jobs (Phase 12, JB-01 to JB-24): the agents on their own. Their jobs,
// runs and findings live in a ConfigMap of their own (<release>-runs);
// the issue service's investigator is the report triage job's; the
// backups to S3 show as a job; the agents read as the reader identity.

// runsLimit is the runs key's limit in its ConfigMap, which the runs fit
// to.
func runsLimit() int { return layout[objRuns][jobs.RunsKey].Bytes }

// jobsWiring is the jobs and the investigator, with the ConfigMap they are
// kept in, for the metrics.
type jobsWiring struct {
	svc    *jobs.Service
	inv    *investigate.Investigator
	stored *kv.Limited

	mu     sync.Mutex
	issues *issues.Service
}

// openJobs builds the jobs: their ConfigMap when the chart names one,
// memory otherwise; the console's own kinds; the investigator, whose kind
// is report triage. The agents and the backups' kind come later, once
// the console has them.
func openJobs(ctx context.Context, cfg config.Config, restCfg *rest.Config, manager *clusters.Manager, auditLog *audit.Log, leads func() bool, log *slog.Logger) (*jobsWiring, error) {
	w := &jobsWiring{}
	var backend kv.Backend = &kv.Memory{}
	if cfg.RunsConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.RunsConfigMap)
		if err != nil {
			return nil, fmt.Errorf("the jobs' ConfigMap: %w", err)
		}
		w.stored = limited(cm, objRuns, log.With("component", "storage"))
		backend = w.stored
	} else {
		log.Warn("the jobs' settings and runs are kept in memory and lost on restart (set -runs-configmap to keep them)")
	}
	w.svc = jobs.New(jobs.Options{
		Backend: backend,
		Kinds:   jobs.Default,
		Clusters: func() []jobs.Cluster {
			var out []jobs.Cluster
			for _, c := range manager.List() {
				out = append(out, jobs.Cluster{ID: c.ID, Name: c.Name})
			}
			return out
		},
		Alerts:    alertsOf(manager),
		CommandOK: func(line string) error { _, err := watson.Parse(line); return err },
		Leads:     leads,
		Audit:     auditLog,
		TimeZone:  cfg.JobsTimeZone,
		// The runs fit a little below their key's limit, so the save that
		// trims them never meets it.
		RunsBytes: runsLimit() - 16<<10,
		Logger:    log.With("component", "jobs"),
	})
	w.svc.RegisterBuiltins()
	w.inv = investigate.New(investigate.Options{
		Jobs: w.svc,
		Services: func(ctx context.Context) ([]issues.ServiceDef, error) {
			if s := w.issueService(); s != nil {
				return s.Services(ctx)
			}
			return nil, nil
		},
		Known: func(ctx context.Context) ([]issues.KnownIssue, error) {
			if s := w.issueService(); s != nil {
				return s.Known(ctx, issues.Caller{Person: issues.Person{ID: "system:investigator", System: true}, Operator: true}, false)
			}
			return nil, nil
		},
		CommandOK: func(line string) error { _, err := watson.Parse(line); return err },
		Logger:    log.With("component", "investigate"),
	})
	jobs.Register(w.inv.Kind())
	return w, nil
}

// investigator is the issue service's investigator: the report triage job.
func (w *jobsWiring) investigator() issues.Investigator {
	if w == nil {
		return nil
	}
	return w.inv
}

func (w *jobsWiring) issueService() *issues.Service {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.issues
}

// setIssues gives the investigator the issue service it works for.
func (w *jobsWiring) setIssues(s *issues.Service) {
	w.mu.Lock()
	w.issues = s
	w.mu.Unlock()
}

// alertRows are a cluster's Alerts tab as the console sees it; ok is
// false when the cluster is not served here.
func alertRows(manager *clusters.Manager, cluster string) ([]views.AlertRow, bool) {
	st, _, ok := manager.Lookup(cluster)
	if !ok || st == nil {
		return nil, false
	}
	var snap struct {
		Views map[string]struct {
			Rows []views.AlertRow `json:"rows"`
		} `json:"views"`
	}
	if json.Unmarshal(st.Snapshot([]string{live.ViewAlerts}, nil), &snap) != nil {
		return nil, false
	}
	return snap.Views[live.ViewAlerts].Rows, true
}

// alertsOf reads every cluster's Alerts tab, as the console sees it,
// without the alerts resolved or acknowledged by a person: what the alert
// jobs start on, and the cases' safety net watches (CF-78). One a job
// acknowledged is marked so (CF-79).
func alertsOf(manager *clusters.Manager) func(ctx context.Context) []jobs.Alert {
	return func(ctx context.Context) []jobs.Alert {
		var out []jobs.Alert
		for _, c := range manager.List() {
			if ctx.Err() != nil {
				break
			}
			rows, ok := alertRows(manager, c.ID)
			if !ok {
				continue
			}
			for _, r := range rows {
				if r.Severity == views.SevRecovered || r.Ack != nil && r.Ack.Run == "" {
					continue
				}
				sev := jobs.SeverityWarning
				if r.Severity == views.SevFailed || r.Severity == views.SevError {
					sev = jobs.SeverityError
				}
				reason := firstNonEmpty(r.Waiting, r.LastReason)
				msg := firstNonEmpty(r.WaitingMessage, r.Why, r.LastMessage)
				if r.Kind == views.AlertNode && len(r.Problems) > 0 {
					reason, msg = firstNonEmpty(reason, r.Problems[0].Title), firstNonEmpty(msg, r.Problems[0].Summary)
				}
				if reason == "" && len(r.Events) > 0 {
					reason, msg = r.Events[0].Reason, firstNonEmpty(msg, r.Events[0].Message)
				}
				if len(msg) > 500 {
					msg = msg[:500]
				}
				out = append(out, jobs.Alert{Cluster: c.ID, Key: r.Key, Kind: r.Kind, Severity: sev, Name: r.Name, Namespace: r.Namespace, Status: r.Status, Reason: reason, Message: msg,
					Node: r.Node, Fingerprint: views.AlertFingerprint(r), JobAck: r.Ack != nil})
			}
		}
		return out
	}
}

// alertAcker keeps a job's acknowledgement of the alert its run handled
// with the people's, in the settings (CF-79): never over a person's of
// the alert as it is, within the list's limit, the lapsed ones let go as
// a person's acknowledgement lets them go.
type alertAcker struct {
	manager  *clusters.Manager
	settings *settings.Service
}

func (a alertAcker) Ack(ctx context.Context, in jobs.AlertAck) (bool, error) {
	var current func(key string) (string, bool)
	if rows, ok := alertRows(a.manager, in.Cluster); ok {
		now := map[string]string{}
		for _, r := range rows {
			if r.Severity != views.SevRecovered {
				now[r.Key] = views.AlertFingerprint(r)
			}
		}
		current = func(key string) (string, bool) { fp, ok := now[key]; return fp, ok }
	}
	kept := false
	_, err := a.settings.UpdateAcks(ctx, in.Cluster, func(list []acks.Ack) []acks.Ack {
		if current != nil {
			list = acks.Prune(list, current)
		}
		list, kept = acks.SetByJob(list, acks.Ack{Key: in.Key, Fingerprint: in.Fingerprint, By: in.By, At: in.At, Run: in.Run, Case: in.Case,
			Exception: in.Exception, Expected: in.Expected, Why: in.Why})
		return list
	})
	return kept && err == nil, err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// agentRunner runs agents on their own (JB-08): each agent as package ai
// resolves it, over Sherlock's engine, reading the cluster through the
// console's read-only tools as the reader identity, never as a person.
type agentRunner struct {
	ai      *ai.Service
	engine  *sherlock.Service
	handler *api.Server
	auth    *auth.Service
}

func (a agentRunner) Usable(agent, cluster string) string { return a.ai.Usable(agent, cluster) }

func (a agentRunner) Run(ctx context.Context, req jobs.AgentRequest) jobs.AgentResult {
	var sess *auth.Session
	if a.auth != nil {
		s, on, err := a.auth.Reader(roles.ID(req.Reader.Role()), req.Reader.ReaderGroup)
		if err != nil {
			return jobs.AgentResult{Err: fmt.Errorf("the reader identity: %w", err)}
		}
		if on {
			sess = &s
		}
	}
	rd, err := a.handler.ReaderFor(ctx, req.Cluster, sess)
	if err != nil {
		return jobs.AgentResult{Err: fmt.Errorf("%s: %w", req.Cluster, err)}
	}
	setBy := req.SetBy
	if setBy == "" {
		setBy = "nobody"
	}
	q := sherlock.Question{
		Key:  sherlock.Key{Session: "job:" + req.RunID, Cluster: req.Cluster},
		Text: req.Task, Cluster: rd.Name, Version: rd.Version, Platform: rd.Platform, Tools: rd.Tools,
		// The audit record of the agent's call names the job and who set
		// its trigger, and the reader it read as.
		Who: audit.Who{Subject: auth.ReaderName, Name: fmt.Sprintf("job %s (%s, %s), trigger set by %s", req.Job, req.Label, req.RunID, setBy)},
		// A job's findings go to those who see the cases, who read the
		// exceptions; the report triage's answer reaches the reporter,
		// who does not (KN-32).
		Exceptions: !req.ForReporters,
	}
	agent, err := a.ai.Agent(req.Agent, q)
	if err != nil {
		return jobs.AgentResult{Err: err}
	}
	res := a.engine.RunAgent(ctx, agent, q, req.Task)
	return jobs.AgentResult{Text: res.Text, Model: res.Model, Tokens: jobs.Tokens{In: res.Usage.Prompt(), Out: res.Usage.OutputTokens}, CostUSD: res.CostUSD, Tools: res.Tools, Err: res.Err}
}

// --- One flow for findings and cases (Phase 12, wave 6, CF-05) ---

// objectsOf says whether a finding's object is on its cluster: read as
// the console itself, within a few seconds; unanswered when the cluster
// is not connected, the kind is not one the console reads, or the read
// fails otherwise.
func objectsOf(manager *clusters.Manager) jobs.ObjectCheck {
	type reader interface {
		ObjectYAML(ctx context.Context, p kube.Person, k kube.Kind, namespace, name string) (string, error)
	}
	return func(ctx context.Context, cluster string, o jobs.Object) (bool, bool) {
		_, client, ok := manager.Lookup(cluster)
		if !ok || client == nil {
			return false, false
		}
		cl, ok := client.(reader)
		k, known := kube.KindOf(o.Kind)
		if !ok || !known {
			return false, false
		}
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := cl.ObjectYAML(rctx, kube.Person{}, k, o.Namespace, o.Name)
		switch {
		case err == nil:
			return true, true
		case errors.Is(err, kube.ErrNotFound):
			return false, true
		}
		return false, false
	}
}

// wireJobs opens the jobs.
func (w *wiring) wireJobs(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Jobs (Phase 12, ADR-90 to ADR-93): the agents on their own, with
	// their runs in <release>-runs; the report triage job is the issue
	// service's investigator.
	jobsW, err := openJobs(ctx, cfg, w.restCfg, w.manager, w.auditLog, w.elector.IsLeader, log)
	if err != nil {
		return err
	}
	if jobsW.stored != nil {
		w.stored = append(w.stored, jobsW.stored)
	}
	w.jobsW = jobsW
	return nil
}
