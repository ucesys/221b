package casework

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- The simulator's cases (Phase 12, wave 6) ---

// Seed is the cases the simulator starts with, as the ConfigMaps hold
// them, for the browser tests and a first look: by ConfigMap (issues,
// casework, runs) the keys and their values, and each case's ConfigMap
// by its number.
type Seed struct {
	Issues   map[string]string
	Casework map[string]string
	Runs     map[string]string
	Cases    map[string]map[string]string
}

// MakeSeed makes the simulator's cases on the cluster "local", through
// the services themselves over memory, so they are what the console
// writes: a person's report, New; a case by hand, New; an agent-found
// case In progress, seen ×3; one closed as fixed with its RCA, verified
// by the last run; one closed as an exception; one closed as a known
// issue that agents still see; one closed as needing no fix; and three
// runs of the nightly audit, the last with every kind of routing (an
// information below the job's lowest priority ignored, wave 6c). The
// report has an incident number and a type, and the last agent-found case,
// raised to P2, waits in the queue for its analysis (CF-32 to CF-37).
func MakeSeed(now time.Time) (Seed, error) {
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuesKV, caseworkKV := &kv.Memory{}, &kv.Memory{}
	objs := &kv.MemoryObjects{}
	clock := now.Add(-72 * time.Hour)
	at := func() time.Time { return clock }
	iss := issues.New(issues.Options{Backend: issuesKV, Cases: objs, Now: at, Logger: quiet})
	ls := lessons.New(lessons.Options{Backend: caseworkKV, Now: at, Logger: quiet})
	svc := New(Options{Backend: caseworkKV, Issues: iss, Lessons: ls, Now: at, Logger: quiet,
		Clusters: func() []jobs.Cluster { return []jobs.Cluster{{ID: "local", Name: "simulated"}} }})
	admin := Caller{Person: Person{Subject: "admin", Email: "admin@corp.example", Name: "Ada Admin"}, ID: "admin@corp.example", Approver: true, Approve: true, Admin: true}
	dev := issues.Caller{Person: issues.Person{ID: "dev@corp.example", Name: "Dev Person", Email: "dev@corp.example"}, Report: true}
	var runs []jobs.Run
	var lowest *int
	run := func(fs ...jobs.Finding) (jobs.RouteResult, error) {
		id := fmt.Sprintf("run-%d", len(runs)+1)
		for i := range fs {
			fs[i].ID = fmt.Sprintf("%s.%d", id, i+1)
		}
		res := svc.Route(ctx, jobs.RouteInput{Run: id, Job: jobs.KindAudit, Kind: jobs.KindAudit, Label: "Nightly audit", Clusters: []string{"local"}, OK: true, Findings: fs,
			Agent: "Auditor", MinPriority: lowest})
		r := jobs.Run{ID: id, Job: jobs.KindAudit, Kind: jobs.KindAudit, Label: "Nightly audit", Agent: "auditor",
			Trigger: jobs.Trigger{Kind: jobs.TriggerSchedule, What: "Every day at 02:00, UTC", SetBy: "admin@corp.example"}, Clusters: []string{"local"},
			Started: clock, Ended: clock.Add(3 * time.Minute), Status: jobs.StatusOK, Model: "claude-opus-5-5", Tokens: jobs.Tokens{In: 41000, Out: 2300}, CostUSD: 0.42,
			Report: "Reviewed the cluster's RBAC, workloads, storage and nodes.", Findings: res.Findings, Routed: res.Routed, VerifiedCases: res.Verified, NewChecks: res.NewChecks}
		runs = append(runs, r)
		return res, nil
	}
	f := func(sev, check, object, title string) jobs.Finding {
		return jobs.Finding{Severity: sev, Check: check, Title: title, Detail: "Seen by the nightly audit.", Cluster: "local", Resource: object, Key: &jobs.Key{Cluster: "local", Object: object, Check: check}}
	}
	limits := f(jobs.FindingWarning, "pod.no-limits", "shop/Deployment/checkout", "checkout has no memory limit")
	latest := f(jobs.FindingWarning, "image.latest-tag", "shop/Deployment/web", "web uses the latest tag")
	priv := f(jobs.FindingCritical, "pod.privileged", "ci/DaemonSet/runner", "The CI runner runs privileged")
	cert := f(jobs.FindingWarning, "cert.expiring", "shop/Certificate/shop-tls", "shop-tls expires in 12 days")
	disk := f(jobs.FindingInfo, "node.disk-pressure", "Node/node-2", "node-2 reported disk pressure")
	netpol := f(jobs.FindingWarning, "netpol.missing", "Namespace/analytics", "analytics has no network policy")
	invalid := jobs.Finding{Severity: jobs.FindingWarning, Check: "rbac.everything", Title: "A check not in the catalogue", Cluster: "local", Resource: "crb/everyone",
		Routing: jobs.RoutedInvalid, InvalidReason: `the check "rbac.everything" is not one of the nightly audit's, nor other.<slug> (lower case, digits, dashes, at most 48)`}
	// Day one: the first run opens a case of each.
	res, err := run(limits, latest, priv, cert, disk)
	if err != nil {
		return Seed{}, err
	}
	id := func(i int) string { return res.Findings[i].Case }
	if _, err := svc.Update(ctx, id(0), Change{Assignee: &issues.Person{ID: "me"}}, admin); err != nil {
		return Seed{}, err
	}
	if _, err := svc.Exception(ctx, id(2), ExceptionInput{Reason: "The CI runner builds container images and needs privileges; it runs on its own nodes."}, admin); err != nil {
		return Seed{}, err
	}
	if _, err := svc.Known(ctx, id(3), KnownLink{Create: &NewKnown{Title: "Certificates are renewed by hand", Workaround: "Renew with the runbook until cert-manager is in."}}, admin); err != nil {
		return Seed{}, err
	}
	if _, err := svc.Close(ctx, id(4), CloseInput{Outcome: issues.OutcomeNoAction, Explanation: "A log burst during the backup; it cleared by itself."}, admin); err != nil {
		return Seed{}, err
	}
	// Day two: the same again; then the tag is pinned, which the next run
	// verifies.
	clock = clock.Add(24 * time.Hour)
	if _, err := run(limits); err != nil {
		return Seed{}, err
	}
	if _, err := svc.Fix(ctx, id(1), FixInput{Text: "Pinned web to 2.4.1.", RCA: &Completion{Cause: "The deployment tracked latest, so a new upstream image rolled out unreviewed.", Prevention: "Admission policy refusing the latest tag."}}, admin); err != nil {
		return Seed{}, err
	}
	// Day three: every kind of routing; the job's lowest priority is P3
	// by now, so the information on probes is ignored.
	clock = clock.Add(24 * time.Hour)
	lowest = intPtr(jobs.DefaultMinPriority)
	probes := f(jobs.FindingInfo, "pod.no-probes", "shop/Deployment/web", "web has no readiness probe")
	day3, err := run(limits, priv, cert, netpol, invalid, probes)
	if err != nil {
		return Seed{}, err
	}
	// The analytics case is raised to P2, and waits for its analysis.
	if nc := day3.Findings[3].Case; nc != "" {
		if _, err := svc.Update(ctx, nc, Change{Priority: intPtr(2)}, admin); err != nil {
			return Seed{}, err
		}
		n, _ := number(nc)
		if _, err := iss.Apply(ctx, n, func(tx *issues.Tx, r *issues.Report) error {
			tx.As(issues.SystemActor)
			at := tx.Now()
			r.AnalysisQueued = &at
			return nil
		}); err != nil {
			return Seed{}, err
		}
	}
	// People's cases: a report, and one by hand.
	clock = clock.Add(2 * time.Hour)
	rep, err := iss.Submit(ctx, dev, issues.Submission{Text: "The shop's checkout page is slow since this morning\nIt takes ten seconds to load the basket."})
	if err != nil {
		return Seed{}, err
	}
	perf, inc := issues.TypePerformance, "INC0012345"
	if _, err := svc.Update(ctx, caseID(rep.Report.Number), Change{Type: &perf, Incident: &inc}, admin); err != nil {
		return Seed{}, err
	}
	if _, err := svc.Create(ctx, NewCase{Title: "Plan the node pool upgrade", Detail: "Kubernetes 1.36 is out; the node pool should follow.", Cluster: "local", Priority: intPtr(3), Type: issues.TypeCapacity}, admin); err != nil {
		return Seed{}, err
	}
	out := Seed{Issues: map[string]string{}, Casework: map[string]string{}, Runs: map[string]string{}, Cases: map[string]map[string]string{}}
	for _, k := range []string{issues.IndexKey, issues.KnownKey, issues.ConfigKey} {
		if b, _, err := issuesKV.LoadKey(ctx, k); err == nil {
			out.Issues[k] = string(b)
		}
	}
	for _, k := range []string{SettingsKey, ChecksKey, lessons.ExceptionsKey, lessons.RCAsKey} {
		if b, _, err := caseworkKV.LoadKey(ctx, k); err == nil {
			out.Casework[k] = string(b)
		}
	}
	// The runs, newest first, as the runs' record keeps them.
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	b, _ := json.Marshal(map[string]any{"next": len(runs), "runs": runs})
	out.Runs[jobs.RunsKey] = string(b)
	names, _ := objs.List(ctx)
	for _, n := range names {
		if _, err := strconv.Atoi(n); err == nil {
			out.Cases[n] = objs.Keys(n)
		}
	}
	return out, nil
}

func intPtr(n int) *int { return &n }
