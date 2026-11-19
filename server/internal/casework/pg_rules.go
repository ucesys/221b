package casework

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The checks seen on each cluster, the validation rules with their
// suggestions, and what the runs found under each check, in the database
// (Phase 13, DB-43). The service is unchanged and reaches the rows through
// db.Doc. Three lists that are of no row are settings scopes: the
// suggestions dismissed, and the runs counted with whether counting began.

const (
	rulesScope = "case-rules"
	foundScope = "case-found"
)

type rulesKept struct {
	Dismissed []string `json:"dismissed,omitempty"`
}

type foundKept struct {
	Counted []string `json:"counted,omitempty"`
	Tracked bool     `json:"tracked,omitempty"`
}

func sameDoc(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func notNull(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

func utc(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func utcOrNone(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return utc(t)
}

func fromUTC(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func loadKept(ctx context.Context, q db.Querier, scope string, v any) (bool, error) {
	b, _, err := db.LoadScope(ctx, q, scope)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func saveKept(ctx context.Context, tx pgx.Tx, scope string, v any) error {
	b, _ := json.Marshal(v)
	_, err := db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
	return err
}

func sorted[V any](maps ...map[string]V) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NewChecksPG keeps the checks seen on each cluster in d.
func NewChecksPG(d *db.DB) *db.Doc[checksRecord] {
	return &db.Doc[checksRecord]{DB: d, Key: ChecksKey, Read: readChecks, Write: writeChecks}
}

func readChecks(ctx context.Context, q db.Querier) (checksRecord, bool, error) {
	var rec checksRecord
	rows, err := q.Query(ctx, "SELECT cluster_id, has_checks, first_run_key FROM checked_clusters")
	if err != nil {
		return rec, false, err
	}
	var cluster, check string
	var has bool
	var first *string
	found := false
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &has, &first}, func() error {
		found = true
		if has {
			if rec.Clusters == nil {
				rec.Clusters = map[string][]string{}
			}
			rec.Clusters[cluster] = []string{}
		}
		if first != nil {
			if rec.FirstRun == nil {
				rec.FirstRun = map[string]string{}
			}
			rec.FirstRun[cluster] = *first
		}
		return nil
	}); err != nil {
		return rec, false, err
	}
	rows, err = q.Query(ctx, "SELECT cluster_id, check_name FROM cluster_checks ORDER BY cluster_id, position")
	if err != nil {
		return rec, false, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &check}, func() error {
		if _, ok := rec.Clusters[cluster]; ok {
			rec.Clusters[cluster] = append(rec.Clusters[cluster], check)
		}
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found, nil
}

func writeChecks(ctx context.Context, tx pgx.Tx, old, next checksRecord) error {
	b := &pgx.Batch{}
	for _, c := range sorted(old.Clusters, next.Clusters) {
		was, had := old.Clusters[c]
		list, has := next.Clusters[c]
		if had == has && sameDoc(was, list) {
			continue
		}
		b.Queue("DELETE FROM cluster_checks WHERE cluster_id = $1", c)
		seen := map[string]bool{}
		for i, check := range list {
			// A check is in a cluster's list once.
			if !seen[check] {
				seen[check] = true
				b.Queue("INSERT INTO cluster_checks (cluster_id, check_name, position) VALUES ($1, $2, $3)", c, check, i)
			}
		}
	}
	for _, c := range sorted(sortedSet(old.Clusters, old.FirstRun), sortedSet(next.Clusters, next.FirstRun)) {
		_, had := old.Clusters[c]
		before, wasRun := old.FirstRun[c]
		_, has := next.Clusters[c]
		first, ran := next.FirstRun[c]
		if had == has && wasRun == ran && before == first {
			continue
		}
		if !has && !ran {
			b.Queue("DELETE FROM checked_clusters WHERE cluster_id = $1", c)
			continue
		}
		var run *string
		if ran {
			run = &first
		}
		b.Queue(`INSERT INTO checked_clusters (cluster_id, has_checks, first_run_key) VALUES ($1, $2, $3)
			ON CONFLICT (cluster_id) DO UPDATE SET has_checks = excluded.has_checks, first_run_key = excluded.first_run_key`, c, has, run)
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

// sortedSet is the keys of both maps, as a set.
func sortedSet(a map[string][]string, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// NewRulesPG keeps the validation rules and their suggestions in d.
func NewRulesPG(d *db.DB) *db.Doc[rulesRecord] {
	return &db.Doc[rulesRecord]{DB: d, Key: RulesKey, Read: readRules, Write: writeRules}
}

func readDescriptions(ctx context.Context, q db.Querier, table, key string) (map[string][]Description, error) {
	out := map[string][]Description{}
	rows, err := q.Query(ctx, "SELECT "+key+"::text, description_key, job_id, kind, body, run_key, described_at FROM "+table+" ORDER BY "+key+", position")
	if err != nil {
		return nil, err
	}
	var of string
	var d Description
	var when *time.Time
	_, err = pgx.ForEachRow(rows, []any{&of, &d.ID, &d.Job, &d.Kind, &d.Text, &d.Run, &when}, func() error {
		x := d
		x.At = fromUTC(when)
		out[of] = append(out[of], x)
		return nil
	})
	return out, err
}

func queueDescriptions(b *pgx.Batch, table, key string, of any, list []Description) {
	for i, d := range list {
		b.Queue("INSERT INTO "+table+" ("+key+", position, description_key, job_id, kind, body, run_key, described_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			of, i, d.ID, d.Job, d.Kind, d.Text, d.Run, utc(d.At))
	}
}

func readRules(ctx context.Context, q db.Querier) (rulesRecord, bool, error) {
	var rec rulesRecord
	var kept rulesKept
	found, err := loadKept(ctx, q, rulesScope, &kept)
	if err != nil {
		return rec, false, err
	}
	rec.Dismissed = kept.Dismissed
	rows, err := q.Query(ctx, `SELECT validation_rule_id, says, case_type, job_kinds, control, NOT is_built_in, is_auto, state = 'retired',
			coalesce(replaced_by_validation_rule_id, ''), updated_at, updated_by
		FROM validation_rules`)
	if err != nil {
		return rec, false, err
	}
	var id string
	var r storedRule
	var kinds []string
	var updated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &r.Says, &r.Type, &kinds, &r.Control, &r.Added, &r.Auto, &r.Retired, &r.ReplacedBy, &updated, &r.By}, func() error {
		x := r
		x.Jobs, x.Updated = nil, fromUTC(updated)
		if kinds != nil {
			k := append([]string{}, kinds...)
			x.Jobs = &k
		}
		if rec.Rules == nil {
			rec.Rules = map[string]*storedRule{}
		}
		rec.Rules[id] = &x
		return nil
	}); err != nil {
		return rec, false, err
	}
	described, err := readDescriptions(ctx, q, "rule_suggestion_descriptions", "rule_suggestion_id")
	if err != nil {
		return rec, false, err
	}
	rows, err = q.Query(ctx, `SELECT rule_suggestion_id::text, suggestion_key, says, case_type, job_kinds, from_checks, run_count, cluster_count, created_at, why
		FROM rule_suggestions ORDER BY position`)
	if err != nil {
		return rec, false, err
	}
	var s Suggestion
	var from []string
	var created *time.Time
	var row string
	if _, err := pgx.ForEachRow(rows, []any{&row, &s.ID, &s.Says, &s.Type, &kinds, &from, &s.Runs, &s.Clusters, &created, &s.Why}, func() error {
		x := s
		x.Jobs, x.From, x.Created, x.Descriptions = nil, nil, fromUTC(created), described[row]
		if kinds != nil {
			x.Jobs = append([]string{}, kinds...)
		}
		if from != nil {
			x.From = append([]string{}, from...)
		}
		rec.Suggestions = append(rec.Suggestions, x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Rules) > 0 || len(rec.Suggestions) > 0, nil
}

func writeRules(ctx context.Context, tx pgx.Tx, old, next rulesRecord) error {
	if !sameDoc(old.Dismissed, next.Dismissed) {
		if err := saveKept(ctx, tx, rulesScope, rulesKept{Dismissed: next.Dismissed}); err != nil {
			return err
		}
	}
	b := &pgx.Batch{}
	for _, id := range sorted(old.Rules, next.Rules) {
		was, r := old.Rules[id], next.Rules[id]
		if r == nil {
			b.Queue("DELETE FROM validation_rules WHERE validation_rule_id = $1", id)
			continue
		}
		if was != nil && sameDoc(was, r) {
			continue
		}
		// As the document has it: no list, or a list that is none, keeps
		// the built-in rule's own job kinds.
		var kinds []string
		if r.Jobs != nil {
			kinds = *r.Jobs
		}
		state := "active"
		if r.Retired {
			state = "retired"
		}
		var replaced *string
		if r.ReplacedBy != "" {
			replaced = &r.ReplacedBy
		}
		b.Queue(`INSERT INTO validation_rules (validation_rule_id, says, case_type, job_kinds, control, is_built_in, is_auto, state, replaced_by_validation_rule_id,
				updated_at, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (validation_rule_id) DO UPDATE SET says = excluded.says, case_type = excluded.case_type, job_kinds = excluded.job_kinds, control = excluded.control,
				is_built_in = excluded.is_built_in, is_auto = excluded.is_auto, state = excluded.state,
				replaced_by_validation_rule_id = excluded.replaced_by_validation_rule_id, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			id, r.Says, r.Type, kinds, r.Control, !r.Added, r.Auto, state, replaced, utcOrNone(r.Updated), r.By)
	}
	// The suggestions are few and change together: written whole.
	if !sameDoc(old.Suggestions, next.Suggestions) {
		b.Queue("DELETE FROM rule_suggestions")
		for i, s := range next.Suggestions {
			id := db.NewID()
			b.Queue(`INSERT INTO rule_suggestions (rule_suggestion_id, position, suggestion_key, says, case_type, job_kinds, from_checks, run_count, cluster_count, created_at, why)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, i, s.ID, s.Says, s.Type, s.Jobs, s.From, s.Runs, s.Clusters, utc(s.Created), s.Why)
			queueDescriptions(b, "rule_suggestion_descriptions", "rule_suggestion_id", id, s.Descriptions)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

// NewFoundPG keeps what the runs found under each check in d.
func NewFoundPG(d *db.DB) *db.Doc[foundRecord] {
	return &db.Doc[foundRecord]{DB: d, Key: FoundKey, Read: readFound, Write: writeFound}
}

func readFound(ctx context.Context, q db.Querier) (foundRecord, bool, error) {
	var rec foundRecord
	var kept foundKept
	found, err := loadKept(ctx, q, foundScope, &kept)
	if err != nil {
		return rec, false, err
	}
	rec.Counted, rec.Tracked = kept.Counted, kept.Tracked
	titles := map[string][]TitleCount{}
	rows, err := q.Query(ctx, "SELECT check_name, title, title_count FROM check_found_titles ORDER BY check_name, position")
	if err != nil {
		return rec, false, err
	}
	var check string
	var tc TitleCount
	if _, err := pgx.ForEachRow(rows, []any{&check, &tc.Text, &tc.N}, func() error {
		titles[check] = append(titles[check], tc)
		return nil
	}); err != nil {
		return rec, false, err
	}
	kinds := map[string]map[string]int{}
	rows, err = q.Query(ctx, "SELECT check_name, kind, kind_count FROM check_found_kinds")
	if err != nil {
		return rec, false, err
	}
	var kind string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&check, &kind, &n}, func() error {
		if kinds[check] == nil {
			kinds[check] = map[string]int{}
		}
		kinds[check][kind] = n
		return nil
	}); err != nil {
		return rec, false, err
	}
	latest := map[string][]FoundFinding{}
	rows, err = q.Query(ctx, `SELECT check_name, run_key, finding_key, found_at, found_check, cluster_id, object, title, severity, routing, case_key, known_issue_key, exception_key
		FROM check_found_findings ORDER BY check_name, position`)
	if err != nil {
		return rec, false, err
	}
	var f FoundFinding
	var when *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&check, &f.Run, &f.ID, &when, &f.Check, &f.Cluster, &f.Object, &f.Title, &f.Severity, &f.Routing, &f.Case, &f.KnownIssue, &f.Exception}, func() error {
		x := f
		x.At = fromUTC(when)
		latest[check] = append(latest[check], x)
		return nil
	}); err != nil {
		return rec, false, err
	}
	whys, err := readDescriptions(ctx, q, "check_found_descriptions", "check_name")
	if err != nil {
		return rec, false, err
	}
	rows, err = q.Query(ctx, "SELECT check_name, finding_count, run_count, cluster_ids, job_ids, first_found_at, last_found_at, run_keys FROM check_found")
	if err != nil {
		return rec, false, err
	}
	var st foundStat
	var clusters, jobIDs, runKeys []string
	var first, last *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&check, &st.Findings, &st.Runs, &clusters, &jobIDs, &first, &last, &runKeys}, func() error {
		x := st
		x.Clusters, x.Jobs, x.RunIDs, x.First, x.Last = orNil(clusters), orNil(jobIDs), orNil(runKeys), fromUTC(first), fromUTC(last)
		x.Titles, x.Kinds, x.Latest, x.Whys = titles[check], kinds[check], latest[check], whys[check]
		if rec.IDs == nil {
			rec.IDs = map[string]*foundStat{}
		}
		rec.IDs[check] = &x
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.IDs) > 0, nil
}

func writeFound(ctx context.Context, tx pgx.Tx, old, next foundRecord) error {
	if !sameDoc(old.Counted, next.Counted) || old.Tracked != next.Tracked {
		if err := saveKept(ctx, tx, foundScope, foundKept{Counted: next.Counted, Tracked: next.Tracked}); err != nil {
			return err
		}
	}
	b := &pgx.Batch{}
	for _, id := range sorted(old.IDs, next.IDs) {
		was, st := old.IDs[id], next.IDs[id]
		if was != nil && st != nil && sameDoc(was, st) {
			continue
		}
		// Whole: what hangs from the row goes with it.
		b.Queue("DELETE FROM check_found WHERE check_name = $1", id)
		if st == nil {
			continue
		}
		b.Queue(`INSERT INTO check_found (check_name, finding_count, run_count, cluster_ids, job_ids, first_found_at, last_found_at, run_keys)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, st.Findings, st.Runs, notNull(st.Clusters), notNull(st.Jobs), utc(st.First), utc(st.Last), notNull(st.RunIDs))
		for i, t := range st.Titles {
			b.Queue("INSERT INTO check_found_titles (check_name, position, title, title_count) VALUES ($1, $2, $3, $4)", id, i, t.Text, t.N)
		}
		for k, n := range st.Kinds {
			b.Queue("INSERT INTO check_found_kinds (check_name, kind, kind_count) VALUES ($1, $2, $3)", id, k, n)
		}
		for i, f := range st.Latest {
			b.Queue(`INSERT INTO check_found_findings (check_name, position, run_key, finding_key, found_at, found_check, cluster_id, object, title, severity, routing, case_key,
					known_issue_key, exception_key)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
				id, i, f.Run, f.ID, utc(f.At), f.Check, f.Cluster, f.Object, f.Title, f.Severity, f.Routing, f.Case, f.KnownIssue, f.Exception)
		}
		queueDescriptions(b, "check_found_descriptions", "check_name", id, st.Whys)
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}
