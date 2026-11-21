package ai

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The agents' instructions, tests and pilot log in the database (Phase 13,
// DB-40). An agent's instructions are rows of prompt_versions, each in
// one state; their notes, parts and test runs hang from the row. The
// service is unchanged and reaches the rows through db.Doc. A row keeps
// its id for as long as what it holds is kept (DB-81): the row in force
// and the draft, one an agent, a version by its number and a revision by
// its own; a row that changed is updated in place, and of what hangs from
// it only what changed is written, a test run's result added being one
// insert. So a row can be pointed at (approval_request_id, item 17).

// The states of a row of prompt_versions.
const (
	rowInForce  = "in_force"
	rowVersion  = "version"
	rowDraft    = "draft"
	rowRevision = "revision"
)

// The scopes of the counters that are of no row.
const (
	testsScope = "ai-tests"
	pilotScope = "ai-pilot"
)

type counter struct {
	Next int `json:"next"`
}

func loadCounter(ctx context.Context, q db.Querier, scope string) (int, bool, error) {
	b, _, err := db.LoadScope(ctx, q, scope)
	if errors.Is(err, db.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var c counter
	if err := json.Unmarshal(b, &c); err != nil {
		return 0, false, err
	}
	return c.Next, true, nil
}

func saveCounter(ctx context.Context, tx pgx.Tx, scope string, next int) error {
	b, _ := json.Marshal(counter{Next: next})
	_, err := db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
	return err
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

// NewPromptsPG keeps the agents' instructions in d.
func NewPromptsPG(d *db.DB) *db.Doc[Prompts] {
	return &db.Doc[Prompts]{DB: d, Key: PromptsKey, Read: readPrompts, Write: writePrompts}
}

// readTestRuns reads every test run, by the row of prompt_versions it is of.
func readTestRuns(ctx context.Context, q db.Querier) (map[string]*TestRun, error) {
	checks := map[string][]Check{}
	rows, err := q.Query(ctx, "SELECT test_case_result_id::text, what, is_ok FROM test_case_result_checks ORDER BY test_case_result_id, position")
	if err != nil {
		return nil, err
	}
	var id string
	var c Check
	if _, err := pgx.ForEachRow(rows, []any{&id, &c.What, &c.OK}, func() error {
		checks[id] = append(checks[id], c)
		return nil
	}); err != nil {
		return nil, err
	}
	results := map[string][]TestResult{}
	rows, err = q.Query(ctx, `SELECT test_case_result_id::text, test_run_id::text, test_case_id, case_name, is_built_in, is_passed, why, answer, tools, hand_overs, cost_usd::float8,
			question, has_expect, expect_tools, expect_hand_overs, expect_is_refusal, expect_points, expect_secret_count
		FROM test_case_results ORDER BY test_run_id, position`)
	if err != nil {
		return nil, err
	}
	var run string
	var r TestResult
	var e ExpectView
	var why, tools, hands, etools, ehands, epoints []string
	var has bool
	if _, err := pgx.ForEachRow(rows, []any{&id, &run, &r.ID, &r.Name, &r.Base, &r.Passed, &why, &r.Answer, &tools, &hands, &r.CostUSD, &r.Question, &has, &etools, &ehands, &e.Refuse, &epoints, &e.Secrets}, func() error {
		x := r
		x.Why, x.Tools, x.HandOvers, x.Expect, x.Checks = listOf(why), listOf(tools), listOf(hands), nil, checks[id]
		if has {
			v := e
			v.Tools, v.HandOvers, v.Points = listOf(etools), listOf(ehands), listOf(epoints)
			x.Expect = &v
		}
		results[run] = append(results[run], x)
		return nil
	}); err != nil {
		return nil, err
	}
	planned := map[string][]CaseState{}
	rows, err = q.Query(ctx, "SELECT test_run_id::text, test_case_id, name, is_built_in, state FROM test_run_cases ORDER BY test_run_id, position")
	if err != nil {
		return nil, err
	}
	var p CaseState
	if _, err := pgx.ForEachRow(rows, []any{&run, &p.ID, &p.Name, &p.Base, &p.State}, func() error {
		planned[run] = append(planned[run], p)
		return nil
	}); err != nil {
		return nil, err
	}
	replays := map[string][]ReplayResult{}
	rows, err = q.Query(ctx, "SELECT test_run_id::text, question, cluster_id, answer_before, answer_after, is_same, cost_usd::float8, error FROM test_run_replays ORDER BY test_run_id, position")
	if err != nil {
		return nil, err
	}
	var rp ReplayResult
	if _, err := pgx.ForEachRow(rows, []any{&run, &rp.Question, &rp.Cluster, &rp.Before, &rp.After, &rp.Same, &rp.CostUSD, &rp.Error}, func() error {
		replays[run] = append(replays[run], rp)
		return nil
	}); err != nil {
		return nil, err
	}
	out := map[string]*TestRun{}
	rows, err = q.Query(ctx, `SELECT test_run_id::text, prompt_version_id::text, revision_number, started_at, started_by, case_count, passed_count, same_count, cost_usd::float8,
			estimate_usd::float8, error, state, replays_planned, stopped_at_case
		FROM test_runs`)
	if err != nil {
		return nil, err
	}
	var of string
	var t TestRun
	var started *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&run, &of, &t.Rev, &started, &t.By, &t.Cases, &t.Passed, &t.Same, &t.CostUSD, &t.Estimate, &t.Error, &t.State, &t.ReplaysPlanned, &t.StoppedAt}, func() error {
		x := t
		x.At, x.Results, x.Replays, x.Planned = timeOf(started), results[run], replays[run], planned[run]
		out[of] = &x
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func readPrompts(ctx context.Context, q db.Querier) (Prompts, bool, error) {
	var ps Prompts
	runs, err := readTestRuns(ctx, q)
	if err != nil {
		return ps, false, err
	}
	notes := map[string]map[string]string{}
	rows, err := q.Query(ctx, "SELECT prompt_version_id::text, cluster_id, body FROM prompt_version_notes")
	if err != nil {
		return ps, false, err
	}
	var id, k, v string
	if _, err := pgx.ForEachRow(rows, []any{&id, &k, &v}, func() error {
		if notes[id] == nil {
			notes[id] = map[string]string{}
		}
		notes[id][k] = v
		return nil
	}); err != nil {
		return ps, false, err
	}
	parts, bases := map[string]map[string]string{}, map[string]map[string]string{}
	rows, err = q.Query(ctx, "SELECT prompt_version_id::text, part, body, base_hash FROM prompt_version_parts")
	if err != nil {
		return ps, false, err
	}
	var body, base *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &k, &body, &base}, func() error {
		for _, x := range []struct {
			m map[string]map[string]string
			v *string
		}{{parts, body}, {bases, base}} {
			if x.v == nil {
				continue
			}
			if x.m[id] == nil {
				x.m[id] = map[string]string{}
			}
			x.m[id][k] = *x.v
		}
		return nil
	}); err != nil {
		return ps, false, err
	}
	rows, err = q.Query(ctx, `SELECT prompt_version_id::text, agent_id, state, version_number, revision_number, body, why, created_by, created_at, base_hash, base_body,
			stage, request_number, restored_version, pilot_started_at, pilot_until, pilot_by, pilot_people, pilot_cluster_ids, is_discarded,
			tested_at, tested_cases, tested_passed, tested_state
		FROM prompt_versions ORDER BY agent_id, state, position`)
	if err != nil {
		return ps, false, err
	}
	all := map[string]*AgentPrompt{}
	var agent, state, why, by, baseHash, baseBody, stage, pilotBy string
	var version, rev, request, restored int
	var created, pilotStarted, pilotUntil, tested *time.Time
	var people, clusters []string
	var discarded bool
	var brief TestBrief
	if _, err := pgx.ForEachRow(rows, []any{&id, &agent, &state, &version, &rev, &body, &why, &by, &created, &baseHash, &baseBody, &stage, &request, &restored,
		&pilotStarted, &pilotUntil, &pilotBy, &people, &clusters, &discarded, &tested, &brief.Cases, &brief.Passed, &brief.State}, func() error {
		ap := all[agent]
		if ap == nil {
			ap = &AgentPrompt{}
			all[agent] = ap
		}
		house := ""
		if body != nil {
			house = *body
		}
		var test *TestBrief
		if tested != nil {
			b := brief
			b.At = tested.UTC()
			test = &b
		}
		switch state {
		case rowInForce:
			if body != nil {
				ap.House = &house
			}
			ap.BaseHash, ap.Base, ap.Live, ap.Revs = baseHash, baseBody, version, rev
			ap.Notes, ap.Parts, ap.PartBase, ap.BaselineCheck = notes[id], parts[id], bases[id], runs[id]
			if ps.Agents == nil {
				ps.Agents = map[string]*AgentPrompt{}
			}
			ps.Agents[agent] = ap
		case rowVersion:
			ap.Versions = append(ap.Versions, Version{N: version, House: house, Notes: notes[id], By: by, At: timeOf(created), Why: why, Parts: parts[id], Rev: rev, Test: test})
		case rowDraft:
			d := &Draft{Rev: rev, House: house, Notes: notes[id], Stage: stage, By: by, At: timeOf(created), Test: runs[id], Request: request, Restored: restored, Parts: parts[id]}
			if pilotStarted != nil {
				d.Pilot = &Pilot{People: listOf(people), Clusters: listOf(clusters), Started: pilotStarted.UTC(), Until: timeOf(pilotUntil), By: pilotBy}
			}
			ap.Draft = d
		case rowRevision:
			ap.Revisions = append(ap.Revisions, Revision{Rev: rev, House: house, Notes: notes[id], Parts: parts[id], By: by, At: timeOf(created), Note: why, Discarded: discarded, Test: test})
		}
		return nil
	}); err != nil {
		return ps, false, err
	}
	return ps, len(ps.Agents) > 0, nil
}

// promptRow is one row of prompt_versions with what hangs from it.
type promptRow struct {
	agent, state           string
	position, version, rev int
	body                   *string
	why, by                string
	at                     *time.Time
	baseHash, base, stage  string
	request, restored      int
	pilot                  *Pilot
	discarded              bool
	test                   *TestBrief
	notes, parts, bases    map[string]string
	run                    *TestRun
}

// promptColumns are the columns of a row of prompt_versions after its key,
// in the order promptValues gives them.
const promptColumns = `agent_id, state, position, version_number, revision_number, body, why, created_by, created_at, base_hash,
	base_body, stage, request_number, restored_version, pilot_started_at, pilot_until, pilot_by, pilot_people, pilot_cluster_ids, is_discarded,
	tested_at, tested_cases, tested_passed, tested_state`

func promptValues(r promptRow) []any {
	var pilotStarted, pilotUntil, tested *time.Time
	var pilot Pilot
	if r.pilot != nil {
		pilot = *r.pilot
		pilotStarted, pilotUntil = at(pilot.Started), at(pilot.Until)
	}
	var brief TestBrief
	if r.test != nil {
		brief = *r.test
		tested = at(brief.At)
	}
	return []any{r.agent, r.state, r.position, r.version, r.rev, r.body, r.why, r.by, r.at, r.baseHash, r.base, r.stage, r.request, r.restored,
		pilotStarted, pilotUntil, pilot.By, list(pilot.People), list(pilot.Clusters), r.discarded, tested, brief.Cases, brief.Passed, brief.State}
}

// queuePrompt queues a new row of prompt_versions under the key id, with
// what hangs from it.
func queuePrompt(b *pgx.Batch, id string, r promptRow) {
	b.Queue(`INSERT INTO prompt_versions (prompt_version_id, `+promptColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
		append([]any{id}, promptValues(r)...)...)
	queueNotes(b, id, r.notes)
	queueParts(b, id, r.parts, r.bases)
	if r.run != nil {
		queueTestRun(b, id, *r.run)
	}
}

func queueNotes(b *pgx.Batch, id string, notes map[string]string) {
	for k, v := range notes {
		b.Queue("INSERT INTO prompt_version_notes (prompt_version_id, cluster_id, body) VALUES ($1, $2, $3)", id, k, v)
	}
}

func queueParts(b *pgx.Batch, id string, parts, bases map[string]string) {
	names := map[string]bool{}
	for k := range parts {
		names[k] = true
	}
	for k := range bases {
		names[k] = true
	}
	for k := range names {
		var body, base *string
		if v, ok := parts[k]; ok {
			body = &v
		}
		if v, ok := bases[k]; ok {
			base = &v
		}
		b.Queue("INSERT INTO prompt_version_parts (prompt_version_id, part, body, base_hash) VALUES ($1, $2, $3, $4)", id, k, body, base)
	}
}

// queuePromptChange queues what changes the row id from was to r (DB-81):
// the row updated in place when a column changed, its notes and parts
// written again when they changed, and its test run as queueRunChange
// writes it.
func queuePromptChange(b *pgx.Batch, id, runID string, was, r promptRow) {
	if !sameDoc(promptValues(was), promptValues(r)) {
		b.Queue(`UPDATE prompt_versions SET (`+promptColumns+`) =
			($2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25) WHERE prompt_version_id = $1`,
			append([]any{id}, promptValues(r)...)...)
	}
	if !sameDoc(was.notes, r.notes) {
		b.Queue("DELETE FROM prompt_version_notes WHERE prompt_version_id = $1", id)
		queueNotes(b, id, r.notes)
	}
	if !sameDoc(was.parts, r.parts) || !sameDoc(was.bases, r.bases) {
		b.Queue("DELETE FROM prompt_version_parts WHERE prompt_version_id = $1", id)
		queueParts(b, id, r.parts, r.bases)
	}
	queueRunChange(b, id, runID, was.run, r.run)
}

// queueRunChange queues what changes the test run of the row of. A run
// that goes, or another run in its place (another start), is deleted with
// what hangs from it; the same run (its row runID) is updated in place,
// a result or a replay added at the end is one insert, and the cases'
// states are written again when one changed.
func queueRunChange(b *pgx.Batch, of, runID string, was, now *TestRun) {
	switch {
	case sameDoc(was, now):
		return
	case now == nil || was == nil || runID == "" || !was.At.Equal(now.At) || was.Rev != now.Rev:
		if was != nil {
			b.Queue("DELETE FROM test_runs WHERE prompt_version_id = $1", of)
		}
		if now != nil {
			queueTestRun(b, of, *now)
		}
		return
	}
	t := *now
	b.Queue(`UPDATE test_runs SET started_by = $2, case_count = $3, passed_count = $4, same_count = $5, cost_usd = $6, estimate_usd = $7, error = $8, state = $9,
			replays_planned = $10, stopped_at_case = $11
		WHERE test_run_id = $1`, runID, t.By, t.Cases, t.Passed, t.Same, t.CostUSD, t.Estimate, t.Error, t.State, t.ReplaysPlanned, t.StoppedAt)
	if !sameDoc(was.Planned, t.Planned) {
		b.Queue("DELETE FROM test_run_cases WHERE test_run_id = $1", runID)
		queueCases(b, runID, t.Planned)
	}
	from := 0
	if n := len(was.Results); n > len(t.Results) || !sameDoc(was.Results, t.Results[:n]) {
		b.Queue("DELETE FROM test_case_results WHERE test_run_id = $1", runID)
	} else {
		from = n
	}
	queueResults(b, runID, t.Results, from)
	from = 0
	if n := len(was.Replays); n > len(t.Replays) || !sameDoc(was.Replays, t.Replays[:n]) {
		b.Queue("DELETE FROM test_run_replays WHERE test_run_id = $1", runID)
	} else {
		from = n
	}
	queueReplays(b, runID, t.Replays, from)
}

func queueTestRun(b *pgx.Batch, of string, t TestRun) {
	id := db.NewID()
	b.Queue(`INSERT INTO test_runs (test_run_id, prompt_version_id, revision_number, started_at, started_by, case_count, passed_count, same_count, cost_usd, estimate_usd,
			error, state, replays_planned, stopped_at_case)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		id, of, t.Rev, at(t.At), t.By, t.Cases, t.Passed, t.Same, t.CostUSD, t.Estimate, t.Error, t.State, t.ReplaysPlanned, t.StoppedAt)
	queueCases(b, id, t.Planned)
	queueResults(b, id, t.Results, 0)
	queueReplays(b, id, t.Replays, 0)
}

func queueCases(b *pgx.Batch, id string, planned []CaseState) {
	for i, c := range planned {
		b.Queue("INSERT INTO test_run_cases (test_run_id, position, test_case_id, name, is_built_in, state) VALUES ($1, $2, $3, $4, $5, $6)", id, i, c.ID, c.Name, c.Base, c.State)
	}
}

func queueResults(b *pgx.Batch, id string, results []TestResult, from int) {
	for i := from; i < len(results); i++ {
		r := results[i]
		rid := db.NewID()
		var e ExpectView
		if r.Expect != nil {
			e = *r.Expect
		}
		b.Queue(`INSERT INTO test_case_results (test_case_result_id, test_run_id, position, test_case_id, case_name, is_built_in, is_passed, why, answer, tools, hand_overs,
				cost_usd, question, has_expect, expect_tools, expect_hand_overs, expect_is_refusal, expect_points, expect_secret_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			rid, id, i, r.ID, r.Name, r.Base, r.Passed, list(r.Why), r.Answer, list(r.Tools), list(r.HandOvers), r.CostUSD, r.Question, r.Expect != nil,
			list(e.Tools), list(e.HandOvers), e.Refuse, list(e.Points), e.Secrets)
		for j, c := range r.Checks {
			b.Queue("INSERT INTO test_case_result_checks (test_case_result_id, position, what, is_ok) VALUES ($1, $2, $3, $4)", rid, j, c.What, c.OK)
		}
	}
}

func queueReplays(b *pgx.Batch, id string, replays []ReplayResult, from int) {
	for i := from; i < len(replays); i++ {
		r := replays[i]
		b.Queue(`INSERT INTO test_run_replays (test_run_replay_id, test_run_id, position, question, cluster_id, answer_before, answer_after, is_same, cost_usd, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, db.NewID(), id, i, r.Question, r.Cluster, r.Before, r.After, r.Same, r.CostUSD, r.Error)
	}
}

// promptRows are an agent's rows of prompt_versions, each under the name
// it keeps its row by: in force and the draft one each, a version by its
// number and a revision by its own (with its place among those of one
// number, should two share it).
func promptRows(id string, ap *AgentPrompt) map[string]promptRow {
	out := map[string]promptRow{}
	if ap == nil {
		return out
	}
	seen := map[string]int{}
	name := func(kind string, n int) string {
		k := kind + "/" + strconv.Itoa(n)
		seen[k]++
		if seen[k] > 1 {
			k += "#" + strconv.Itoa(seen[k])
		}
		return k
	}
	out[rowInForce] = promptRow{agent: id, state: rowInForce, version: ap.Live, rev: ap.Revs, body: ap.House, baseHash: ap.BaseHash, base: ap.Base,
		notes: ap.Notes, parts: ap.Parts, bases: ap.PartBase, run: ap.BaselineCheck}
	for i, v := range ap.Versions {
		house := v.House
		out[name(rowVersion, v.N)] = promptRow{agent: id, state: rowVersion, position: i, version: v.N, rev: v.Rev, body: &house, why: v.Why, by: v.By, at: at(v.At),
			notes: v.Notes, parts: v.Parts, test: v.Test}
	}
	if d := ap.Draft; d != nil {
		house := d.House
		out[rowDraft] = promptRow{agent: id, state: rowDraft, rev: d.Rev, body: &house, by: d.By, at: at(d.At), stage: d.Stage, request: d.Request,
			restored: d.Restored, pilot: d.Pilot, notes: d.Notes, parts: d.Parts, run: d.Test}
	}
	for i, r := range ap.Revisions {
		house := r.House
		out[name(rowRevision, r.Rev)] = promptRow{agent: id, state: rowRevision, position: i, rev: r.Rev, body: &house, why: r.Note, by: r.By, at: at(r.At),
			discarded: r.Discarded, notes: r.Notes, parts: r.Parts, test: r.Test}
	}
	return out
}

// rowKeys reads the keys of an agent's rows of prompt_versions and of
// their test runs, under the names promptRows gives them.
func rowKeys(ctx context.Context, tx pgx.Tx, agent string) (map[string]string, map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT p.prompt_version_id::text, p.state, p.version_number, p.revision_number, coalesce(t.test_run_id::text, '')
		FROM prompt_versions p LEFT JOIN test_runs t USING (prompt_version_id) WHERE p.agent_id = $1 ORDER BY p.state, p.position`, agent)
	if err != nil {
		return nil, nil, err
	}
	ids, runs := map[string]string{}, map[string]string{}
	seen := map[string]int{}
	var id, state, run string
	var version, rev int
	_, err = pgx.ForEachRow(rows, []any{&id, &state, &version, &rev, &run}, func() error {
		k := state
		switch state {
		case rowVersion:
			k += "/" + strconv.Itoa(version)
		case rowRevision:
			k += "/" + strconv.Itoa(rev)
		}
		if state == rowVersion || state == rowRevision {
			seen[k]++
			if seen[k] > 1 {
				k += "#" + strconv.Itoa(seen[k])
			}
		}
		ids[k], runs[k] = id, run
		return nil
	})
	return ids, runs, err
}

func writePrompts(ctx context.Context, tx pgx.Tx, old, next Prompts) error {
	ids := map[string]bool{}
	for id := range old.Agents {
		ids[id] = true
	}
	for id := range next.Agents {
		ids[id] = true
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	b := &pgx.Batch{}
	for _, id := range order {
		was, ap := old.Agents[id], next.Agents[id]
		if was != nil && ap != nil && sameDoc(was, ap) {
			continue
		}
		if ap == nil {
			b.Queue("DELETE FROM prompt_versions WHERE agent_id = $1", id)
			continue
		}
		if was == nil {
			b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", id)
		}
		// Each row keeps its key while what it holds is kept (DB-81).
		keys, runs, err := rowKeys(ctx, tx, id)
		if err != nil {
			return err
		}
		before, after := promptRows(id, was), promptRows(id, ap)
		names := make([]string, 0, len(after))
		for k := range after {
			names = append(names, k)
		}
		sort.Strings(names)
		var gone []string
		for k, row := range keys {
			if _, ok := after[k]; !ok {
				gone = append(gone, row)
			}
		}
		if len(gone) > 0 {
			sort.Strings(gone)
			b.Queue("DELETE FROM prompt_versions WHERE prompt_version_id = ANY($1::uuid[])", gone)
		}
		for _, k := range names {
			r := after[k]
			row, had := keys[k]
			if !had {
				queuePrompt(b, db.NewID(), r)
				continue
			}
			w, known := before[k]
			if !known {
				// A row the document did not read: written again, under its key.
				b.Queue("DELETE FROM prompt_version_notes WHERE prompt_version_id = $1", row)
				b.Queue("DELETE FROM prompt_version_parts WHERE prompt_version_id = $1", row)
				w = promptRow{run: nil}
				if runs[k] != "" {
					b.Queue("DELETE FROM test_runs WHERE prompt_version_id = $1", row)
				}
				w.agent = "\x00"
			}
			queuePromptChange(b, row, runs[k], w, r)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

// NewTestsPG keeps the agents' test cases in d.
func NewTestsPG(d *db.DB) *db.Doc[Tests] {
	return &db.Doc[Tests]{DB: d, Key: TestsKey, Read: readTests, Write: writeTests}
}

func readTests(ctx context.Context, q db.Querier) (Tests, bool, error) {
	var ts Tests
	next, found, err := loadCounter(ctx, q, testsScope)
	if err != nil {
		return ts, false, err
	}
	ts.Next = next
	recorded := map[string][]sherlock.Recorded{}
	rows, err := q.Query(ctx, "SELECT test_case_id, tool, input, result, is_error FROM test_case_recordings ORDER BY test_case_id, position")
	if err != nil {
		return ts, false, err
	}
	var id string
	var r sherlock.Recorded
	var input *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &r.Tool, &input, &r.Result, &r.Error}, func() error {
		x := r
		x.Input = nil
		if input != nil {
			x.Input = json.RawMessage(*input)
		}
		recorded[id] = append(recorded[id], x)
		return nil
	}); err != nil {
		return ts, false, err
	}
	rows, err = q.Query(ctx, `SELECT test_case_id, agent_id, name, question, cluster_id, expect_tools, expect_hand_overs, expect_is_refusal, expect_points, expect_secrets,
			is_built_in, created_by, created_at
		FROM test_cases ORDER BY position, test_case_id`)
	if err != nil {
		return ts, false, err
	}
	var c TestCase
	var tools, hands, points, secrets []string
	var created *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&c.ID, &c.Agent, &c.Name, &c.Question, &c.Cluster, &tools, &hands, &c.Expect.Refuse, &points, &secrets, &c.Base, &c.By, &created}, func() error {
		x := c
		x.Recorded, x.At = recorded[x.ID], timeOf(created)
		x.Expect.Tools, x.Expect.HandOvers, x.Expect.Points, x.Expect.Secrets = listOf(tools), listOf(hands), listOf(points), listOf(secrets)
		ts.List = append(ts.List, x)
		return nil
	}); err != nil {
		return ts, false, err
	}
	return ts, found || len(ts.List) > 0, nil
}

func writeTests(ctx context.Context, tx pgx.Tx, old, next Tests) error {
	if old.Next != next.Next {
		if err := saveCounter(ctx, tx, testsScope, next.Next); err != nil {
			return err
		}
	}
	before, place := map[string]TestCase{}, map[string]int{}
	for i, c := range old.List {
		before[c.ID], place[c.ID] = c, i
	}
	b := &pgx.Batch{}
	kept := []string{}
	for i, c := range next.List {
		kept = append(kept, c.ID)
		if was, had := before[c.ID]; had && place[c.ID] == i && sameDoc(was, c) {
			continue
		}
		b.Queue("DELETE FROM test_cases WHERE test_case_id = $1", c.ID)
		b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", c.Agent)
		b.Queue(`INSERT INTO test_cases (test_case_id, position, agent_id, name, question, cluster_id, expect_tools, expect_hand_overs, expect_is_refusal, expect_points,
				expect_secrets, is_built_in, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			c.ID, i, c.Agent, c.Name, c.Question, c.Cluster, list(c.Expect.Tools), list(c.Expect.HandOvers), c.Expect.Refuse, list(c.Expect.Points),
			list(c.Expect.Secrets), c.Base, c.By, atOrNone(c.At))
		for j, r := range c.Recorded {
			var input *string
			if r.Input != nil {
				s := string(r.Input)
				input = &s
			}
			b.Queue("INSERT INTO test_case_recordings (test_case_id, position, tool, input, result, is_error) VALUES ($1, $2, $3, $4, $5, $6)", c.ID, j, r.Tool, input, r.Result, r.Error)
		}
	}
	b.Queue("DELETE FROM test_cases WHERE NOT (test_case_id = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}

// NewPilotPG keeps the pilots' answers in d.
func NewPilotPG(d *db.DB) *db.Doc[PilotLog] {
	return &db.Doc[PilotLog]{DB: d, Key: PilotKey, Read: readPilot, Write: writePilot}
}

func readPilot(ctx context.Context, q db.Querier) (PilotLog, bool, error) {
	var l PilotLog
	next, found, err := loadCounter(ctx, q, pilotScope)
	if err != nil {
		return l, false, err
	}
	l.Next = next
	// In the order they were logged: a key made later sorts later.
	rows, err := q.Query(ctx, `SELECT entry_number, agent_id, version, asked_at, asked_by, cluster_id, question, answer, model, cost_usd::float8
		FROM pilot_entries ORDER BY pilot_entry_id`)
	if err != nil {
		return l, false, err
	}
	var e PilotEntry
	var asked *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&e.N, &e.Agent, &e.Version, &asked, &e.Who, &e.Cluster, &e.Question, &e.Answer, &e.Model, &e.CostUSD}, func() error {
		x := e
		x.At = timeOf(asked)
		l.Entries = append(l.Entries, x)
		return nil
	}); err != nil {
		return l, false, err
	}
	return l, found || len(l.Entries) > 0, nil
}

func writePilot(ctx context.Context, tx pgx.Tx, old, next PilotLog) error {
	if old.Next != next.Next {
		if err := saveCounter(ctx, tx, pilotScope, next.Next); err != nil {
			return err
		}
	}
	// The log grows at its end and is cut at its start: what both hold in
	// the same order stays as it is.
	drop := 0
	for ; drop <= len(old.Entries); drop++ {
		if rest := old.Entries[drop:]; len(rest) == 0 || len(rest) <= len(next.Entries) && sameDoc(rest, next.Entries[:len(rest)]) {
			break
		}
	}
	b := &pgx.Batch{}
	if drop > 0 {
		b.Queue("DELETE FROM pilot_entries WHERE pilot_entry_id IN (SELECT pilot_entry_id FROM pilot_entries ORDER BY pilot_entry_id LIMIT $1)", drop)
	}
	for _, e := range next.Entries[len(old.Entries)-drop:] {
		b.Queue(`INSERT INTO pilot_entries (pilot_entry_id, entry_number, agent_id, version, asked_at, asked_by, cluster_id, question, answer, model, cost_usd)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, db.NewID(), e.N, e.Agent, e.Version, at(e.At), e.Who, e.Cluster, e.Question, e.Answer, e.Model, e.CostUSD)
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}
