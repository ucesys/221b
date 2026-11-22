package automation

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Automation's scripts in the database (Phase 13, DB-32): a script a row
// of scripts, each of its versions a row of script_versions with what its
// header declares in columns, its parameters, the Kubernetes actions it
// may take, what the checks and Sherlock found in it, and what people did
// with it as rows of script_version_parameters, script_version_kube_actions,
// script_version_findings and script_version_actions. The store is
// unchanged and reaches them through db.Doc.

// The kinds of script_version_actions rows.
const (
	actEdited    = "edited"
	actSubmitted = "submitted"
	actApproved  = "approved"
	actRevoked   = "revoked"
	actWithdrawn = "withdrawn"
	actRejected  = "rejected"
)

// The sources of script_version_findings rows.
const (
	foundByChecks = "checks"
	foundByReview = "review"
)

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// NewScriptsPG keeps the scripts and their versions in d.
func NewScriptsPG(d *db.DB) *db.Doc[scriptsRecord] {
	return &db.Doc[scriptsRecord]{DB: d, Key: ScriptsKey, Read: readScripts, Write: writeScripts}
}

func readScripts(ctx context.Context, q db.Querier) (scriptsRecord, bool, error) {
	rec := scriptsRecord{Scripts: map[string]Script{}}
	// A version by its row's id, to hang its parts on.
	type place struct {
		script string
		i      int
	}
	names := map[string]string{}
	rows, err := q.Query(ctx, `SELECT s.script_id::text, s.name, coalesce(g.name, ''), s.created_at, s.updated_at
		FROM scripts s LEFT JOIN script_groups g ON g.script_group_id = s.script_group_id`)
	if err != nil {
		return rec, false, err
	}
	var id, name, group string
	var created, updated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &name, &group, &created, &updated}, func() error {
		names[id] = name
		rec.Scripts[name] = Script{Name: name, Group: group, Created: timeOf(created), Updated: timeOf(updated), Versions: []Version{}}
		return nil
	}); err != nil {
		return rec, false, err
	}
	if len(rec.Scripts) == 0 {
		return scriptsRecord{}, false, nil
	}
	at := map[string]place{}
	version := func(id string) *Version {
		p, ok := at[id]
		if !ok {
			return nil
		}
		return &rec.Scripts[p.script].Versions[p.i]
	}
	rows, err = q.Query(ctx, `SELECT script_version_id::text, script_id::text, version_number, body, hash, state, description, privilege, has_dry_run,
			target, runs_as, facts, author, author_user, created_at, same_as_version, review_summary, reviewed_at
		FROM script_versions ORDER BY script_id, version_number`)
	if err != nil {
		return rec, false, err
	}
	var vid, sid string
	var v Version
	var sameAs *int
	var summary *string
	var reviewed *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&vid, &sid, &v.N, &v.Text, &v.Hash, &v.State, &v.Header.Describe, &v.Header.Privilege, &v.Header.DryRun,
		&v.Header.Target, &v.Header.RunsAs, &v.Header.Facts, &v.Author.Name, &v.Author.User, &created, &sameAs, &summary, &reviewed}, func() error {
		s, ok := rec.Scripts[names[sid]]
		if !ok {
			return nil
		}
		w := v
		w.Created, w.Header.Params, w.Findings = timeOf(created), []Param{}, []Finding{}
		// The row's own slice: the next row is read into the same variable.
		w.Header.Facts = append([]string(nil), v.Header.Facts...)
		if sameAs != nil {
			w.SameAs = *sameAs
		}
		if summary != nil {
			w.Review = &Review{Summary: *summary, At: timeOf(reviewed)}
		}
		at[vid] = place{s.Name, len(s.Versions)}
		s.Versions = append(s.Versions, w)
		rec.Scripts[s.Name] = s
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, "SELECT script_version_id::text, name, is_required, is_secret, description FROM script_version_parameters ORDER BY script_version_id, position, name")
	if err != nil {
		return rec, false, err
	}
	var p Param
	if _, err := pgx.ForEachRow(rows, []any{&vid, &p.Name, &p.Required, &p.Secret, &p.Desc}, func() error {
		if v := version(vid); v != nil {
			v.Header.Params = append(v.Header.Params, p)
		}
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, "SELECT script_version_id::text, verb, resource FROM script_version_kube_actions ORDER BY script_version_id, position")
	if err != nil {
		return rec, false, err
	}
	var k KubeAction
	if _, err := pgx.ForEachRow(rows, []any{&vid, &k.Verb, &k.Resource}, func() error {
		if v := version(vid); v != nil {
			v.Header.K8s = append(v.Header.K8s, k)
		}
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, "SELECT script_version_id::text, source, level, line, code, message FROM script_version_findings ORDER BY script_version_id, source, position")
	if err != nil {
		return rec, false, err
	}
	var source string
	var f Finding
	if _, err := pgx.ForEachRow(rows, []any{&vid, &source, &f.Level, &f.Line, &f.Code, &f.Msg}, func() error {
		v := version(vid)
		switch {
		case v == nil:
		case source == foundByChecks:
			v.Findings = append(v.Findings, f)
		case v.Review != nil:
			v.Review.Findings = append(v.Review.Findings, ReviewFinding{Level: f.Level, Line: f.Line, Msg: f.Msg})
		}
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, "SELECT script_version_id::text, kind, by_name, by_user, at, comment FROM script_version_actions ORDER BY script_version_id, kind, position")
	if err != nil {
		return rec, false, err
	}
	var kind string
	var a Approval
	var when *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&vid, &kind, &a.By.Name, &a.By.User, &when, &a.Comment}, func() error {
		v := version(vid)
		if v == nil {
			return nil
		}
		b := a
		b.At = timeOf(when)
		switch kind {
		case actEdited:
			v.Editors = append(v.Editors, b.By)
		case actSubmitted:
			v.Submitted = &b
		case actApproved:
			v.Approvals = append(v.Approvals, b)
		case actRevoked:
			v.Revoked = append(v.Revoked, b)
		case actWithdrawn:
			v.Withdrawn = append(v.Withdrawn, b)
		case actRejected:
			v.Rejected = &b
		}
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, true, nil
}

// writeScripts writes the scripts that changed. A script keeps its row,
// and a version its row by its number, since runs point at versions.
func writeScripts(ctx context.Context, tx pgx.Tx, old, next scriptsRecord) error {
	names := make([]string, 0, len(next.Scripts))
	for name := range next.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s, was := next.Scripts[name], old.Scripts[name]
		if _, had := old.Scripts[name]; had && sameJSON(was, s) {
			continue
		}
		if err := writeScript(ctx, tx, was, s); err != nil {
			return err
		}
	}
	gone := []string{}
	for name := range old.Scripts {
		if _, ok := next.Scripts[name]; !ok {
			gone = append(gone, name)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, "DELETE FROM scripts WHERE name = ANY($1)", gone)
	return err
}

func writeScript(ctx context.Context, tx pgx.Tx, was, s Script) error {
	// A group a script names exists from then on, as the store keeps it.
	var group *string
	if s.Group != "" {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO script_groups (script_group_id, position, name)
				VALUES ($1, (SELECT coalesce(max(position), -1) + 1 FROM script_groups), $2)
			ON CONFLICT (name) DO UPDATE SET name = excluded.name RETURNING script_group_id::text`, db.NewID(), s.Group).Scan(&id); err != nil {
			return err
		}
		group = &id
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO scripts (script_id, script_group_id, name, created_at, updated_at) VALUES ($1, $2, $3, coalesce($4::timestamptz, now()), $5)
		ON CONFLICT (name) DO UPDATE SET script_group_id = excluded.script_group_id, created_at = excluded.created_at, updated_at = excluded.updated_at
		RETURNING script_id::text`, db.NewID(), group, s.Name, timeOrNil(s.Created), timeOrNil(s.Updated)).Scan(&id); err != nil {
		return err
	}
	before := map[int]Version{}
	for _, v := range was.Versions {
		before[v.N] = v
	}
	kept := make([]int, 0, len(s.Versions))
	for _, v := range s.Versions {
		kept = append(kept, v.N)
		if b, had := before[v.N]; had && sameJSON(b, v) {
			continue
		}
		if err := writeVersion(ctx, tx, id, v); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "DELETE FROM script_versions WHERE script_id = $1 AND NOT (version_number = ANY($2))", id, kept)
	return err
}

func writeVersion(ctx context.Context, tx pgx.Tx, script string, v Version) error {
	facts := v.Header.Facts
	if facts == nil {
		facts = []string{}
	}
	var sameAs *int
	if v.SameAs != 0 {
		sameAs = &v.SameAs
	}
	var summary *string
	var reviewed *time.Time
	if v.Review != nil {
		summary, reviewed = &v.Review.Summary, timeOrNil(v.Review.At)
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO script_versions (script_version_id, script_id, version_number, body, hash, state, description, privilege,
			has_dry_run, target, runs_as, facts, author, author_user, created_at, same_as_version, review_summary, reviewed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, coalesce($15::timestamptz, now()), $16, $17, $18)
		ON CONFLICT (script_id, version_number) DO UPDATE SET body = excluded.body, hash = excluded.hash, state = excluded.state,
			description = excluded.description, privilege = excluded.privilege, has_dry_run = excluded.has_dry_run, target = excluded.target,
			runs_as = excluded.runs_as, facts = excluded.facts, author = excluded.author, author_user = excluded.author_user,
			created_at = excluded.created_at, same_as_version = excluded.same_as_version, review_summary = excluded.review_summary,
			reviewed_at = excluded.reviewed_at
		RETURNING script_version_id::text`,
		db.NewID(), script, v.N, v.Text, v.Hash, v.State, v.Header.Describe, v.Header.Privilege, v.Header.DryRun, v.Header.Target, v.Header.RunsAs,
		facts, v.Author.Name, v.Author.User, timeOrNil(v.Created), sameAs, summary, reviewed).Scan(&id); err != nil {
		return err
	}
	for _, table := range []string{"script_version_parameters", "script_version_kube_actions", "script_version_findings", "script_version_actions"} {
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE script_version_id = $1", id); err != nil {
			return err
		}
	}
	for i, p := range v.Header.Params {
		if _, err := tx.Exec(ctx, `INSERT INTO script_version_parameters (script_version_id, position, name, is_required, is_secret, description)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (script_version_id, name) DO NOTHING`, id, i, p.Name, p.Required, p.Secret, p.Desc); err != nil {
			return err
		}
	}
	for i, k := range v.Header.K8s {
		if _, err := tx.Exec(ctx, "INSERT INTO script_version_kube_actions (script_version_id, position, verb, resource) VALUES ($1, $2, $3, $4)", id, i, k.Verb, k.Resource); err != nil {
			return err
		}
	}
	finding := func(source string, i int, level string, line int, code, msg string) error {
		_, err := tx.Exec(ctx, `INSERT INTO script_version_findings (script_version_id, source, position, level, line, code, message)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, source, i, level, line, code, msg)
		return err
	}
	for i, f := range v.Findings {
		if err := finding(foundByChecks, i, f.Level, f.Line, f.Code, f.Msg); err != nil {
			return err
		}
	}
	if v.Review != nil {
		for i, f := range v.Review.Findings {
			if err := finding(foundByReview, i, f.Level, f.Line, "", f.Msg); err != nil {
				return err
			}
		}
	}
	action := func(kind string, i int, a Approval) error {
		_, err := tx.Exec(ctx, `INSERT INTO script_version_actions (script_version_id, kind, position, by_name, by_user, at, comment)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, kind, i, a.By.Name, a.By.User, timeOrNil(a.At), a.Comment)
		return err
	}
	for i, e := range v.Editors {
		if err := action(actEdited, i, Approval{By: e}); err != nil {
			return err
		}
	}
	for kind, list := range map[string][]Approval{actApproved: v.Approvals, actRevoked: v.Revoked, actWithdrawn: v.Withdrawn} {
		for i, a := range list {
			if err := action(kind, i, a); err != nil {
				return err
			}
		}
	}
	if v.Submitted != nil {
		if err := action(actSubmitted, 0, *v.Submitted); err != nil {
			return err
		}
	}
	if v.Rejected != nil {
		if err := action(actRejected, 0, *v.Rejected); err != nil {
			return err
		}
	}
	return nil
}
