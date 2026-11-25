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
	"github.com/ucesys/221b/server/internal/db"
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

// jobsWiring is the jobs and the investigator, with the ConfigMap they are
// kept in, for the metrics.
type jobsWiring struct {
	svc    *jobs.Service
	inv    *investigate.Investigator
	stored *kv.Limited
	// backend is what the jobs read and write through: their ConfigMap,
	// with the keys the database keeps routed to it.
	backend kv.Backend

	mu     sync.Mutex
	issues *issues.Service
}

// openJobs builds the jobs: the console's own kinds; the investigator,
// whose kind is report triage. The agents and the backups' kind come
// later, once the console has them.
//
// moved keeps the jobs and their runs in the database (Phase 13) and is
// given the one-time copy's source and nothing else: <release>-runs, the
// ConfigMap a release before 0.13 kept them in, nil when the flags name
// none. A caller that gives no moved (a test) keeps them in memory.
func openJobs(ctx context.Context, cfg config.Config, restCfg *rest.Config, manager *clusters.Manager, auditLog *audit.Log, leads func() bool, moved func(kv.Backend) kv.Backend, log *slog.Logger) (*jobsWiring, error) {
	w := &jobsWiring{}
	var backend kv.Backend
	cm, err := openSource(ctx, restCfg, sourceRef(cfg, objRuns))
	if err != nil {
		return nil, fmt.Errorf("the jobs' ConfigMap: %w", err)
	}
	if cm != nil {
		w.stored = limited(cm, objRuns, log.With("component", "storage"))
		backend = w.stored
	}
	if moved != nil {
		backend = moved(backend)
	} else if backend == nil {
		backend = &kv.Memory{}
	}
	w.backend = backend
	w.svc = jobs.New(jobs.Options{
		Backend: backend,
		Kinds:   jobs.Default,
		Clusters: func() []jobs.Cluster {
			var out []jobs.Cluster
			for _, c := range manager.List() {
				out = append(out, jobs.Cluster{ID: c.ID, Name: c.Name, Env: c.Env})
			}
			return out
		},
		Alerts: alertsOf(manager),
		// A cluster's alerts fire once it has listed every kind they are
		// made of (kube#13).
		AlertsReady: func(id string) bool {
			st, _, ok := manager.Lookup(id)
			return ok && st != nil && st.Complete(live.ViewAlerts)
		},
		CommandOK: func(line string) error { _, err := watson.Parse(line); return err },
		Leads:     leads,
		Audit:     auditLog,
		TimeZone:  cfg.JobsTimeZone,
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
// false when the cluster is not served here, and complete says the rows
// were built once the cluster had listed every kind (kube#14).
func alertRows(manager *clusters.Manager, cluster string) (rows []views.AlertRow, ok, complete bool) {
	st, _, ok := manager.Lookup(cluster)
	if !ok || st == nil {
		return nil, false, false
	}
	var snap struct {
		Views map[string]struct {
			Rows []views.AlertRow `json:"rows"`
		} `json:"views"`
	}
	raw, complete := st.CompleteSnapshot([]string{live.ViewAlerts}, nil)
	if json.Unmarshal(raw, &snap) != nil {
		return nil, false, false
	}
	return snap.Views[live.ViewAlerts].Rows, true, complete
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
			rows, ok, _ := alertRows(manager, c.ID)
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
	// The list is pruned against the Alerts rows only when they were built
	// from every kind: before, an alert missing may only not be read yet,
	// and its acknowledgement would go for good (kube#14).
	var current func(key string) (string, bool)
	if rows, ok, complete := alertRows(a.manager, in.Cluster); ok && complete {
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

// SetupOf is an agent's model and effort as set now, for the jobs' list.
func (a agentRunner) SetupOf(ctx context.Context, agent string) (jobs.AgentSetup, bool) {
	st, ok := a.ai.SetupOf(ctx, agent)
	return jobs.AgentSetup{Model: st.Model, DefaultModel: st.DefaultModel, AutoFrom: st.AutoFrom, AutoTo: st.AutoTo, Provider: st.Provider, Effort: st.Effort, FloorEffort: st.FloorEffort}, ok
}

// Available says whether an agent can answer at all (JB-25): one turned
// off is jobs.AgentOff, in the agents' words.
func (a agentRunner) Available(ctx context.Context, agent string) error {
	err := a.ai.Available(ctx, agent)
	if errors.Is(err, ai.ErrUnavailable) {
		return jobs.AgentOff(err.Error())
	}
	return err
}

// Blind says whether the reader identity can read nothing of a cluster
// (JB-23), so an audit says so in place of finding nothing.
func (a agentRunner) Blind(ctx context.Context, cluster string, reader jobs.Settings) (bool, error) {
	sess, err := a.reader(reader)
	if err != nil {
		return false, err
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return a.handler.ReaderBlind(rctx, cluster, sess)
}

// reader is the session agents on their own read with; nil is the
// console itself, with sign-in off.
func (a agentRunner) reader(reader jobs.Settings) (*auth.Session, error) {
	if a.auth == nil {
		return nil, nil
	}
	s, on, err := a.auth.Reader(roles.ID(reader.Role()), reader.ReaderGroup)
	if err != nil {
		return nil, fmt.Errorf("the reader identity: %w", err)
	}
	if !on {
		return nil, nil
	}
	return &s, nil
}

func (a agentRunner) Run(ctx context.Context, req jobs.AgentRequest) jobs.AgentResult {
	sess, err := a.reader(req.Reader)
	if err != nil {
		return jobs.AgentResult{Err: err}
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
		// The run's budget holds within the agent's rounds and its
		// hand-overs (JB-10).
		Meter: req.Meter,
	}
	agent, err := a.ai.Agent(req.Agent, q)
	if err != nil {
		return jobs.AgentResult{Err: err}
	}
	// A run's step is picked by rules alone (the owner, November 22), from
	// what started the run and what the step is about (AI-45).
	movedFrom := ""
	if req.Auto != nil {
		_, movedFrom = a.ai.PickRun(ctx, &agent, req.Auto.Input, req.Auto.Model, req.Auto.MovedUp)
	}
	res := a.engine.RunAgent(ctx, agent, q, req.Task)
	return jobs.AgentResult{Text: res.Text, Model: res.Model, Tokens: jobs.Tokens{In: res.Usage.Prompt(), Out: res.Usage.OutputTokens}, CostUSD: res.CostUSD, Tools: res.Tools, Err: res.Err,
		Provider: agent.ProviderName, Effort: res.Effort, PickBy: res.PickBy, PickWhy: res.PickWhy, MovedUpFrom: movedFrom}
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
	// The jobs and their runs are rows of the database (DB-42).
	jobsW, err := openJobs(ctx, cfg, w.restCfg, w.manager, w.auditLog, w.elector.IsLeader, func(b kv.Backend) kv.Backend {
		w.inDatabase = append(w.inDatabase, "jobs")
		return w.inTables(ctx, b, func(d *db.DB) map[string]movedDoc {
			return map[string]movedDoc{jobs.JobsKey: jobs.NewJobsPG(d), jobs.RunsKey: jobs.NewRunsPG(d)}
		})
	}, log)
	if err != nil {
		return err
	}
	if jobsW.stored != nil {
		w.stored = append(w.stored, jobsW.stored)
	}
	w.jobsW = jobsW
	return nil
}
