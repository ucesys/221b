package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/schedule"
)

// The jobs and their runs in the database (Phase 13, DB-42): a job that
// was changed or added, or that has spent, is a row of jobs; a run a row
// of job_runs with its findings, facts and proposals in tables of their
// own. The service reaches the rows through db.Doc. A scheduled time a
// job handled is a row of schedule_claims, claimed with db.Claim (DB-80):
// the newest of a job's claims is the time it last handled. The jobs' own
// settings and the two counters, which are of no row, are settings
// scopes.

const (
	jobsScope = "jobs"
	runsScope = "job-runs"
)

// jobsKept is what the scope "jobs" holds.
type jobsKept struct {
	Settings Settings `json:"settings"`
	Next     int      `json:"next,omitempty"`
}

func same(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// A list as a column that is never null, and back: no elements is no list.
func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func listOf(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

func at(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func atOrNone(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return at(t)
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func loadScope(ctx context.Context, q db.Querier, scope string, v any) (bool, error) {
	b, _, err := db.LoadScope(ctx, q, scope)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func saveScope(ctx context.Context, tx pgx.Tx, scope string, v any) error {
	b, _ := json.Marshal(v)
	_, err := db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
	return err
}

// JobsPG keeps the jobs in the database: the document the service reads
// and saves, and the claims of their scheduled times (DB-80).
type JobsPG struct {
	*db.Doc[jobsRecord]
}

// NewJobsPG keeps the jobs in d.
func NewJobsPG(d *db.DB) *JobsPG {
	return &JobsPG{&db.Doc[jobsRecord]{DB: d, Key: JobsKey, Read: readJobs, Write: writeJobs}}
}

// keptClaims is how long a job's claims are kept before the newest it
// made: the record of which replica ran a time, for a while.
const keptClaims = 7 * 24 * time.Hour

// ClaimSlot claims the job's scheduled time slot for the replica by, and
// says whether it was first (db.Claim): every replica that leads tries,
// one insert goes in. A time no later than one the job claimed already is
// not claimed again. The job's claims older than a week before the time
// claimed go with it; the newest always stays.
func (p *JobsPG) ClaimSlot(ctx context.Context, job string, slot time.Time, by string) (bool, error) {
	claimed := false
	err := p.DB.Tx(ctx, func(tx pgx.Tx) error {
		claimed = false
		// The job's turn, so a later time claimed meanwhile is seen.
		if err := db.LockName(ctx, tx, "claim/"+job); err != nil {
			return err
		}
		var later bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schedule_claims WHERE job_id = $1 AND slot_at >= $2)", job, slot.UTC()).Scan(&later); err != nil || later {
			return err
		}
		ok, err := p.DB.Claim(ctx, tx, job, slot, by)
		if err != nil || !ok {
			return err
		}
		claimed = true
		_, err = tx.Exec(ctx, "DELETE FROM schedule_claims WHERE job_id = $1 AND slot_at < $2", job, slot.UTC().Add(-keptClaims))
		return err
	})
	return claimed, err
}

func readJobs(ctx context.Context, q db.Querier) (jobsRecord, bool, error) {
	var rec jobsRecord
	var kept jobsKept
	found, err := loadScope(ctx, q, jobsScope, &kept)
	if err != nil {
		return rec, false, err
	}
	rec.Settings, rec.Next = kept.Settings, kept.Next
	rows, err := q.Query(ctx, `SELECT job_id, is_kept, kind, name, task, has_schedule, schedule_kind, schedule_at, schedule_days, schedule_every, schedule_cron,
			schedule_time_zone, event, event_severity, cluster_ids, run_usd::float8, day_usd::float8, is_enabled, skips_overlap, catches_up, set_by, set_at, since_at,
			is_added, min_priority, spend_day, spend_usd, spend_stopped
		FROM jobs`)
	if err != nil {
		return rec, false, err
	}
	var j Job
	var sp schedule.Spec
	var has, isKept bool
	var days, clusters []string
	var setAt, since *time.Time
	var minPriority *int
	var day *string
	var spend daySpend
	if _, err := pgx.ForEachRow(rows, []any{&j.ID, &isKept, &j.Kind, &j.Name, &j.Task, &has, &sp.Kind, &sp.At, &days, &sp.Every, &sp.Cron, &sp.TimeZone, &j.When.Event,
		&j.When.Severity, &clusters, &j.Budget.RunUSD, &j.Budget.DayUSD, &j.On, &j.SkipOverlap, &j.CatchUp, &j.SetBy, &setAt, &since, &j.Added, &minPriority,
		&day, &spend.USD, &spend.Stopped}, func() error {
		found = true
		if isKept {
			x := j
			x.When.Schedule, x.Clusters, x.SetAt, x.Since, x.MinPriority = nil, listOf(clusters), timeOf(setAt), timeOf(since), nil
			if has {
				s := sp
				s.Days = listOf(days)
				x.When.Schedule = &s
			}
			if minPriority != nil {
				p := *minPriority
				x.MinPriority = &p
			}
			if rec.Jobs == nil {
				rec.Jobs = map[string]Job{}
			}
			rec.Jobs[x.ID] = x
		}
		if day != nil {
			if rec.Spend == nil {
				rec.Spend = map[string]daySpend{}
			}
			rec.Spend[j.ID] = daySpend{Day: *day, USD: spend.USD, Stopped: spend.Stopped}
		}
		return nil
	}); err != nil {
		return rec, false, err
	}
	// The newest time each job claimed is the time it last handled.
	rows, err = q.Query(ctx, "SELECT job_id, max(slot_at) FROM schedule_claims GROUP BY job_id")
	if err != nil {
		return rec, false, err
	}
	var id string
	var last time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &last}, func() error {
		found = true
		if rec.Handled == nil {
			rec.Handled = map[string]time.Time{}
		}
		rec.Handled[id] = last.UTC()
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found, nil
}

// jobRow is everything a row of jobs holds, of one job.
type jobRow struct {
	Job   *Job
	Spend *daySpend
}

func jobRowOf(rec jobsRecord, id string) jobRow {
	var r jobRow
	if j, ok := rec.Jobs[id]; ok {
		r.Job = &j
	}
	if s, ok := rec.Spend[id]; ok {
		r.Spend = &s
	}
	return r
}

func writeJobs(ctx context.Context, tx pgx.Tx, old, next jobsRecord) error {
	if !same(old.Settings, next.Settings) || old.Next != next.Next {
		if err := saveScope(ctx, tx, jobsScope, jobsKept{Settings: next.Settings, Next: next.Next}); err != nil {
			return err
		}
	}
	ids := map[string]bool{}
	for _, rec := range []jobsRecord{old, next} {
		for id := range rec.Jobs {
			ids[id] = true
		}
		for id := range rec.Handled {
			ids[id] = true
		}
		for id := range rec.Spend {
			ids[id] = true
		}
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	b := &pgx.Batch{}
	for _, id := range order {
		was, r := jobRowOf(old, id), jobRowOf(next, id)
		if same(was, r) {
			continue
		}
		b.Queue("DELETE FROM jobs WHERE job_id = $1", id)
		if r.Job == nil && r.Spend == nil {
			continue
		}
		var j Job
		if r.Job != nil {
			j = *r.Job
		}
		var sp schedule.Spec
		if j.When.Schedule != nil {
			sp = *j.When.Schedule
		}
		var day *string
		var spend daySpend
		if r.Spend != nil {
			spend = *r.Spend
			day = &spend.Day
		}
		b.Queue(`INSERT INTO jobs (job_id, is_kept, kind, name, task, has_schedule, schedule_kind, schedule_at, schedule_days, schedule_every, schedule_cron,
				schedule_time_zone, event, event_severity, cluster_ids, run_usd, day_usd, is_enabled, skips_overlap, catches_up, set_by, set_at, since_at, is_added,
				min_priority, spend_day, spend_usd, spend_stopped)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)`,
			id, r.Job != nil, j.Kind, j.Name, j.Task, j.When.Schedule != nil, sp.Kind, sp.At, list(sp.Days), sp.Every, sp.Cron, sp.TimeZone, j.When.Event, j.When.Severity,
			list(j.Clusters), j.Budget.RunUSD, j.Budget.DayUSD, j.On, j.SkipOverlap, j.CatchUp, j.SetBy, atOrNone(j.SetAt), atOrNone(j.Since), j.Added, j.MinPriority,
			day, spend.USD, spend.Stopped)
	}
	// The times handled are claims (DB-80): a time newer than the last
	// claimed is a claim (the copy from the ConfigMap of before), and a
	// job whose time is forgotten (its schedule restored, the job gone)
	// loses its claims.
	for _, id := range order {
		was, had := old.Handled[id]
		now, has := next.Handled[id]
		switch {
		case has && (!had || now.After(was)):
			b.Queue(`INSERT INTO schedule_claims (job_id, slot_at, claimed_by) VALUES ($1, $2, '') ON CONFLICT DO NOTHING`, id, now.UTC())
		case had && !has:
			b.Queue("DELETE FROM schedule_claims WHERE job_id = $1", id)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

// NewRunsPG keeps the jobs' runs in d.
func NewRunsPG(d *db.DB) *db.Doc[runsRecord] {
	// Automation's runs are runs.json too, in another ConfigMap: each is
	// copied under a name of its own (the fifth cluster test, F2).
	return &db.Doc[runsRecord]{DB: d, Key: RunsKey, ImportAs: "jobs/" + RunsKey, Read: readRuns, Write: writeRuns}
}

func readRuns(ctx context.Context, q db.Querier) (runsRecord, bool, error) {
	var rec runsRecord
	var n struct {
		Next int `json:"next"`
	}
	found, err := loadScope(ctx, q, runsScope, &n)
	if err != nil {
		return rec, false, err
	}
	rec.Next = n.Next
	findings := map[string][]Finding{}
	rows, err := q.Query(ctx, `SELECT job_run_id::text, finding_key, severity, check_name, title, detail, evidence, cluster_id, resource, has_key, key_cluster_id, key_object,
			key_check, invalid_reason, is_unverified, routing, case_key, known_issue_key, exception_key, is_new_check, is_expected, expected_why, confidence, check_why
		FROM findings ORDER BY job_run_id, position`)
	if err != nil {
		return rec, false, err
	}
	var run string
	var f Finding
	var k Key
	var hasKey bool
	var evidence []string
	if _, err := pgx.ForEachRow(rows, []any{&run, &f.ID, &f.Severity, &f.Check, &f.Title, &f.Detail, &evidence, &f.Cluster, &f.Resource, &hasKey, &k.Cluster, &k.Object, &k.Check,
		&f.InvalidReason, &f.Unverified, &f.Routing, &f.Case, &f.KnownIssue, &f.Exception, &f.NewCheck, &f.Expected, &f.ExpectedWhy, &f.Confidence, &f.CheckWhy}, func() error {
		x := f
		x.Evidence, x.Key = listOf(evidence), nil
		if hasKey {
			y := k
			x.Key = &y
		}
		findings[run] = append(findings[run], x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	facts := map[string][]Fact{}
	rows, err = q.Query(ctx, "SELECT job_run_id::text, label, body, href, tone FROM job_run_facts ORDER BY job_run_id, position")
	if err != nil {
		return rec, false, err
	}
	var fact Fact
	if _, err := pgx.ForEachRow(rows, []any{&run, &fact.Label, &fact.Text, &fact.Href, &fact.Tone}, func() error {
		facts[run] = append(facts[run], fact)
		return nil
	}); err != nil {
		return rec, false, err
	}
	titles := map[string]map[int]map[string]string{}
	rows, err = q.Query(ctx, "SELECT job_run_id::text, position, case_key, title FROM job_run_proposal_cases")
	if err != nil {
		return rec, false, err
	}
	var pos int
	var key, title string
	if _, err := pgx.ForEachRow(rows, []any{&run, &pos, &key, &title}, func() error {
		if titles[run] == nil {
			titles[run] = map[int]map[string]string{}
		}
		if titles[run][pos] == nil {
			titles[run][pos] = map[string]string{}
		}
		titles[run][pos][key] = title
		return nil
	}); err != nil {
		return rec, false, err
	}
	proposals := map[string][]ProposalRef{}
	rows, err = q.Query(ctx, "SELECT job_run_id::text, position, card_key, title, error, resolves FROM job_run_proposals ORDER BY job_run_id, position")
	if err != nil {
		return rec, false, err
	}
	var p ProposalRef
	var resolves []string
	if _, err := pgx.ForEachRow(rows, []any{&run, &pos, &p.ID, &p.Title, &p.Error, &resolves}, func() error {
		x := p
		x.Resolves, x.ResolvesTitles = listOf(resolves), titles[run][pos]
		proposals[run] = append(proposals[run], x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	rows, err = q.Query(ctx, `SELECT job_run_id::text, run_key, job_id, kind, label, coalesce(agent_id, ''), trigger_kind, trigger_what, trigger_set_by, trigger_by, trigger_slot_at,
			trigger_event, trigger_key, trigger_is_rebuild, subject, repeats, last_seen_at, severity, fingerprint, ref, cluster_ids, model, started_at, ended_at, status, note,
			report, routed_opened, routed_seen, routed_known, routed_excepted, routed_reopened, routed_regression, routed_verified, routed_capped, routed_invalid,
			routed_ignored, routed_expected, verified_cases, new_checks, failed_cluster_ids, has_left_out, left_out_count, left_out_cap, left_out_critical,
			left_out_warning, left_out_info, not_rechecked, tokens_in, tokens_out, cost_usd::float8, not_routed, is_reroute, instance, is_without_findings
		FROM job_runs ORDER BY position DESC`)
	if err != nil {
		return rec, false, err
	}
	var r Run
	var lo LeftOut
	var hasLeftOut bool
	var slot, lastSeen, started, ended *time.Time
	var clusters, verified, newChecks, failed []string
	if _, err := pgx.ForEachRow(rows, []any{&run, &r.ID, &r.Job, &r.Kind, &r.Label, &r.Agent, &r.Trigger.Kind, &r.Trigger.What, &r.Trigger.SetBy, &r.Trigger.By, &slot,
		&r.Trigger.Event, &r.Trigger.Key, &r.Trigger.Rebuild, &r.Subject, &r.Repeats, &lastSeen, &r.Severity, &r.Fingerprint, &r.Ref, &clusters, &r.Model, &started, &ended,
		&r.Status, &r.Note, &r.Report, &r.Routed.Opened, &r.Routed.Seen, &r.Routed.Known, &r.Routed.Excepted, &r.Routed.Reopened, &r.Routed.Regression, &r.Routed.Verified,
		&r.Routed.Capped, &r.Routed.Invalid, &r.Routed.Ignored, &r.Routed.Expected, &verified, &newChecks, &failed, &hasLeftOut, &lo.Count, &lo.Cap, &lo.Severities.Critical,
		&lo.Severities.Warning, &lo.Severities.Info, &r.NotRechecked, &r.Tokens.In, &r.Tokens.Out, &r.CostUSD, &r.NotRouted, &r.Reroute, &r.Instance, &r.NoFindings}, func() error {
		x := r
		x.Trigger.Slot, x.LastSeen, x.Started, x.Ended = timeOf(slot), timeOf(lastSeen), timeOf(started), timeOf(ended)
		x.Clusters, x.VerifiedCases, x.NewChecks, x.FailedClusters = listOf(clusters), listOf(verified), listOf(newChecks), listOf(failed)
		x.Findings, x.Facts, x.Proposals, x.LeftOut = findings[run], facts[run], proposals[run], nil
		if hasLeftOut {
			y := lo
			x.LeftOut = &y
		}
		rec.Runs = append(rec.Runs, x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Runs) > 0, nil
}

func queueRun(b *pgx.Batch, r Run, position int64) {
	id := db.NewID()
	var agent *string
	if r.Agent != "" {
		// A run points at its agent's row.
		b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", r.Agent)
		agent = &r.Agent
	}
	var lo LeftOut
	if r.LeftOut != nil {
		lo = *r.LeftOut
	}
	b.Queue(`INSERT INTO job_runs (job_run_id, run_key, position, job_id, kind, label, agent_id, trigger_kind, trigger_what, trigger_set_by, trigger_by, trigger_slot_at,
			trigger_event, trigger_key, trigger_is_rebuild, subject, repeats, last_seen_at, severity, fingerprint, ref, cluster_ids, model, started_at, ended_at, status, note,
			report, routed_opened, routed_seen, routed_known, routed_excepted, routed_reopened, routed_regression, routed_verified, routed_capped, routed_invalid,
			routed_ignored, routed_expected, verified_cases, new_checks, failed_cluster_ids, has_left_out, left_out_count, left_out_cap, left_out_critical,
			left_out_warning, left_out_info, not_rechecked, tokens_in, tokens_out, cost_usd, not_routed, is_reroute, instance, is_without_findings)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33,
			$34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44, $45, $46, $47, $48, $49, $50, $51, $52, $53, $54, $55, $56)`,
		id, r.ID, position, r.Job, r.Kind, r.Label, agent, r.Trigger.Kind, r.Trigger.What, r.Trigger.SetBy, r.Trigger.By, atOrNone(r.Trigger.Slot), r.Trigger.Event,
		r.Trigger.Key, r.Trigger.Rebuild, r.Subject, r.Repeats, atOrNone(r.LastSeen), r.Severity, r.Fingerprint, r.Ref, list(r.Clusters), r.Model, at(r.Started),
		atOrNone(r.Ended), r.Status, r.Note, r.Report, r.Routed.Opened, r.Routed.Seen, r.Routed.Known, r.Routed.Excepted, r.Routed.Reopened, r.Routed.Regression,
		r.Routed.Verified, r.Routed.Capped, r.Routed.Invalid, r.Routed.Ignored, r.Routed.Expected, list(r.VerifiedCases), list(r.NewChecks), list(r.FailedClusters),
		r.LeftOut != nil, lo.Count, lo.Cap, lo.Severities.Critical, lo.Severities.Warning, lo.Severities.Info, r.NotRechecked, r.Tokens.In, r.Tokens.Out, r.CostUSD,
		r.NotRouted, r.Reroute, r.Instance, r.NoFindings)
	for i, f := range r.Findings {
		var k Key
		if f.Key != nil {
			k = *f.Key
		}
		b.Queue(`INSERT INTO findings (finding_id, job_run_id, position, finding_key, severity, check_name, title, detail, evidence, cluster_id, resource, has_key,
				key_cluster_id, key_object, key_check, invalid_reason, is_unverified, routing, case_key, known_issue_key, exception_key, is_new_check, is_expected,
				expected_why, confidence, check_why, case_id, known_issue_id, exception_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26,
				(SELECT t.case_id FROM cases t WHERE $19 LIKE 'C-%' AND t.object_name = substr($19, 3)),
				(SELECT k.known_issue_id FROM known_issues k WHERE k.known_issue_key = $20),
				(SELECT e.exception_id FROM exceptions e WHERE e.exception_key = $21))`,
			db.NewID(), id, i, f.ID, f.Severity, f.Check, f.Title, f.Detail, list(f.Evidence), f.Cluster, f.Resource, f.Key != nil, k.Cluster, k.Object, k.Check,
			f.InvalidReason, f.Unverified, f.Routing, f.Case, f.KnownIssue, f.Exception, f.NewCheck, f.Expected, f.ExpectedWhy, f.Confidence, f.CheckWhy)
	}
	for i, f := range r.Facts {
		b.Queue("INSERT INTO job_run_facts (job_run_id, position, label, body, href, tone) VALUES ($1, $2, $3, $4, $5, $6)", id, i, f.Label, f.Text, f.Href, f.Tone)
	}
	for i, p := range r.Proposals {
		b.Queue("INSERT INTO job_run_proposals (job_run_id, position, card_key, title, error, resolves) VALUES ($1, $2, $3, $4, $5, $6)", id, i, p.ID, p.Title, p.Error, list(p.Resolves))
		for k, t := range p.ResolvesTitles {
			b.Queue("INSERT INTO job_run_proposal_cases (job_run_id, position, case_key, title) VALUES ($1, $2, $3, $4)", id, i, k, t)
		}
	}
}

func writeRuns(ctx context.Context, tx pgx.Tx, old, next runsRecord) error {
	if old.Next != next.Next {
		if err := saveScope(ctx, tx, runsScope, struct {
			Next int `json:"next"`
		}{next.Next}); err != nil {
			return err
		}
	}
	placed := map[string]int64{}
	top := int64(0)
	rows, err := tx.Query(ctx, "SELECT run_key, position FROM job_runs")
	if err != nil {
		return err
	}
	var key string
	var position int64
	if _, err := pgx.ForEachRow(rows, []any{&key, &position}, func() error {
		placed[key] = position
		top = max(top, position)
		return nil
	}); err != nil {
		return err
	}
	before := map[string]Run{}
	for _, r := range old.Runs {
		before[r.ID] = r
	}
	// The newest run has the greatest position. Runs come in at the front
	// and go at the end, which leaves the others' places as they are;
	// anything else gives every run its place again.
	places := make([]int64, len(next.Runs))
	fresh, last, inOrder := 0, int64(0), true
	for i, r := range next.Runs {
		p, had := placed[r.ID]
		switch {
		case !had && i == fresh:
			fresh++
		case !had, i > fresh && p >= last:
			inOrder = false
		}
		places[i], last = p, p
	}
	for i := range next.Runs {
		if !inOrder {
			places[i] = int64(len(next.Runs) - i)
		} else if i < fresh {
			places[i] = top + int64(fresh-i)
		}
	}
	b := &pgx.Batch{}
	kept := make([]string, 0, len(next.Runs))
	for i, r := range next.Runs {
		kept = append(kept, r.ID)
		p, had := placed[r.ID]
		if was, ok := before[r.ID]; had && ok && same(was, r) {
			if p != places[i] {
				b.Queue("UPDATE job_runs SET position = $2 WHERE run_key = $1", r.ID, places[i])
			}
			continue
		}
		// Whole: its findings, facts and proposals go with the row.
		b.Queue("DELETE FROM job_runs WHERE run_key = $1", r.ID)
		queueRun(b, r, places[i])
	}
	b.Queue("DELETE FROM job_runs WHERE NOT (run_key = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}
