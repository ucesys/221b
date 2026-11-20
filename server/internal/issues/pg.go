package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// The cases, the known issues and the service catalogue in the database
// (Phase 13, DB-52 to DB-54), every field a column. The service is
// unchanged.
//
//   - A case is an object with several keys and one version (kv.Objects):
//     CasesPG keeps it as a row of cases with a version column, the case's
//     messages and people in tables of their own. The keys other packages
//     keep in the object (the work on the case, its conversation with
//     Sherlock) are CaseParts they give.
//   - The index, the known issues and the settings are read and saved at
//     one version: a db.DocSet. The index's lines are not kept: every line
//     is lineOf(case), so they are read from the cases' rows, and the
//     index's document writes only its counter and the archived counts.

const (
	indexScope  = "case-index"
	knownScope  = "known-issues"
	issuesScope = "issues"
)

func pgSame(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func pgList[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func pgListOf[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return append([]T(nil), s...)
}

// Times as columns take them and give them: UTC; none for a zero time
// where the document leaves it out.
func pgAt(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func pgAtOrNone(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return pgAt(t)
}

func pgAtPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return pgAt(*t)
}

func pgTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// A person as four columns, the first none for no person, and back.
func pgPerson(p *Person) (*string, string, string, bool) {
	if p == nil {
		return nil, "", "", false
	}
	id := p.ID
	return &id, p.Name, p.Email, p.System
}

func pgPersonOf(key *string, name, email string, system bool) *Person {
	if key == nil {
		return nil
	}
	return &Person{ID: *key, Name: name, Email: email, System: system}
}

func pgActor(a *Actor) (*string, string) {
	if a == nil {
		return nil, ""
	}
	n := a.Name
	return &n, a.Kind
}

func pgActorOf(name *string, kind string) *Actor {
	if name == nil {
		return nil
	}
	return &Actor{Name: *name, Kind: kind}
}

func pgLoadScope(ctx context.Context, q db.Querier, scope string, v any) (bool, error) {
	b, _, err := db.LoadScope(ctx, q, scope)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func pgSaveScope(ctx context.Context, tx pgx.Tx, scope string, v any) error {
	b, _ := json.Marshal(v)
	_, err := db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
	return err
}

// ---------- a case's row ----------

// caseRow is the columns of cases that hold a case, in the order they
// are written.
type caseRow struct {
	Number       int      `col:"case_number"`
	Title        string   `col:"title"`
	LineTitle    string   `col:"line_title"`
	Involved     []string `col:"involved"`
	Source       string   `col:"source"`
	Cluster      string   `col:"cluster_id"`
	Object       string   `col:"object"`
	OpenedKey    *string  `col:"opened_by_key"`
	OpenedName   string   `col:"opened_by_name"`
	OpenedEmail  string   `col:"opened_by_email"`
	OpenedSystem bool     `col:"opened_by_is_system"`
	KeyCluster   *string  `col:"key_cluster_id"`
	KeyObject    string   `col:"key_object"`
	KeyCheck     string   `col:"key_check"`
	Job          string   `col:"job_id"`
	Run          string   `col:"run_key"`
	Finding      string   `col:"finding_key"`
	Severity     string   `col:"severity"`

	SeenCount   *int       `col:"seen_count"`
	SeenFirst   *time.Time `col:"seen_first_at"`
	SeenLast    *time.Time `col:"seen_last_at"`
	SeenRun     string     `col:"seen_run_key"`
	SeenFinding string     `col:"seen_finding_key"`
	SeenAs      string     `col:"seen_as"`

	RegressionOf int   `col:"regression_of_number"`
	SameAs       int   `col:"same_as_number"`
	PossibleDup  int   `col:"possible_duplicate_of_number"`
	DuplicateOf  int   `col:"duplicate_of_number"`
	Similar      []int `col:"similar_numbers"`

	PrevCase    *int       `col:"previous_case_number"`
	PrevOutcome string     `col:"previous_outcome"`
	PrevExplain string     `col:"previous_explanation"`
	PrevBy      string     `col:"previous_by"`
	PrevAt      *time.Time `col:"previous_at"`

	Exception     string     `col:"exception_key"`
	RCA           string     `col:"rca_key"`
	RCACause      string     `col:"rca_cause"`
	VerifyPending bool       `col:"is_verify_pending"`
	Verified      *time.Time `col:"verified_at"`
	VerRun        *string    `col:"verified_run_key"`
	VerJob        string     `col:"verified_job_id"`
	VerAt         *time.Time `col:"verified_run_at"`
	VerEvidence   string     `col:"verified_evidence"`
	Unchecked     int        `col:"unchecked_count"`
	UncheckedWhy  string     `col:"unchecked_why"`
	UncheckedRun  string     `col:"unchecked_run_key"`
	SLAFrom       *time.Time `col:"sla_from_at"`

	ClosedKey    *string `col:"closed_by_key"`
	ClosedName   string  `col:"closed_by_name"`
	ClosedEmail  string  `col:"closed_by_email"`
	ClosedSystem bool    `col:"closed_by_is_system"`
	FixProposed  bool    `col:"is_fix_proposed"`

	AppliedCard  *string    `col:"applied_change_key"`
	AppliedTitle string     `col:"applied_title"`
	AppliedBy    string     `col:"applied_by"`
	AppliedAt    *time.Time `col:"applied_at"`
	Verification string     `col:"verification"`
	Type         string     `col:"type"`
	Incident     string     `col:"incident"`

	CreatedByName *string    `col:"created_by_name"`
	CreatedByKind string     `col:"created_by_kind"`
	LastByName    *string    `col:"last_by_name"`
	LastByKind    string     `col:"last_by_kind"`
	LastAt        *time.Time `col:"last_at"`
	Queued        *time.Time `col:"analysis_queued_at"`

	Text     string `col:"body"`
	Scrubbed bool   `col:"is_scrubbed"`
	Pruned   bool   `col:"is_pruned"`

	ReporterKey    string  `col:"reporter_key"`
	ReporterName   string  `col:"reporter_name"`
	ReporterEmail  string  `col:"reporter_email"`
	ReporterSystem bool    `col:"reporter_is_system"`
	FiledKey       *string `col:"filed_by_key"`
	FiledName      string  `col:"filed_by_name"`
	FiledEmail     string  `col:"filed_by_email"`
	FiledSystem    bool    `col:"filed_by_is_system"`

	Created     *time.Time `col:"created_at"`
	Updated     *time.Time `col:"updated_at"`
	Status      string     `col:"status"`
	StatusAt    *time.Time `col:"status_at"`
	Outcome     string     `col:"outcome"`
	Explanation string     `col:"explanation"`
	Priority    int        `col:"priority"`
	PriorityBy  string     `col:"priority_by"`

	OwnerKey    *string `col:"owner_key"`
	OwnerName   string  `col:"owner_name"`
	OwnerEmail  string  `col:"owner_email"`
	OwnerSystem bool    `col:"owner_is_system"`

	Service           string   `col:"service_key"`
	ServiceConfidence int      `col:"service_confidence"`
	ServiceBy         string   `col:"service_by"`
	KnownIssue        string   `col:"known_issue_key"`
	MatchConfidence   int      `col:"match_confidence"`
	Rejected          []string `col:"rejected"`

	Waiting   *time.Time `col:"waiting_at"`
	Technical string     `col:"technical"`
	Plain     string     `col:"plain"`
	Cause     string     `col:"cause"`
	Evidence  []string   `col:"evidence"`

	Acknowledged  *time.Time `col:"acknowledged_at"`
	FirstResponse *time.Time `col:"first_response_at"`
	Resolved      *time.Time `col:"resolved_at"`
	Closed        *time.Time `col:"closed_at"`
	LastReporter  *time.Time `col:"last_reporter_at"`
	Reopened      int        `col:"reopened_count"`
}

func caseToRow(c *Case) caseRow {
	r := caseRow{Number: c.Number, Title: c.Title, Source: c.Source, Cluster: c.Cluster, Object: c.Object, Job: c.Job, Run: c.Run, Finding: c.Finding, Severity: c.Severity,
		RegressionOf: c.RegressionOf, SameAs: c.SameAs, PossibleDup: c.PossibleDuplicateOf, DuplicateOf: c.DuplicateOf, Similar: pgList(c.Similar),
		Exception: c.Exception, RCA: c.RCA, RCACause: c.RCACause, VerifyPending: c.VerifyPending, Verified: pgAtPtr(c.Verified), Unchecked: c.Unchecked,
		UncheckedWhy: c.UncheckedWhy, UncheckedRun: c.UncheckedRun, SLAFrom: pgAtPtr(c.SLAFrom), FixProposed: c.FixProposed, Verification: c.Verification, Type: c.Type,
		Incident: c.Incident, LastAt: pgAtPtr(c.LastAt), Queued: pgAtPtr(c.AnalysisQueued), Text: c.Text, Scrubbed: c.Scrubbed, Pruned: c.Pruned,
		ReporterKey: c.Reporter.ID, ReporterName: c.Reporter.Name, ReporterEmail: c.Reporter.Email, ReporterSystem: c.Reporter.System,
		Created: pgAt(c.Created), Updated: pgAt(c.Updated), Status: string(c.Status), StatusAt: pgAt(c.StatusAt), Outcome: string(c.Outcome), Explanation: c.Explanation,
		Priority: c.Priority, PriorityBy: c.PriorityBy, Service: c.Service, ServiceConfidence: c.ServiceConfidence, ServiceBy: c.ServiceBy, KnownIssue: c.KnownIssue,
		MatchConfidence: c.MatchConfidence, Rejected: pgList(c.Rejected), Waiting: pgAtPtr(c.Waiting), Technical: c.Technical, Plain: c.Plain, Cause: c.Cause,
		Evidence: pgList(c.Evidence), Acknowledged: pgAtPtr(c.Acknowledged), FirstResponse: pgAtPtr(c.FirstResponse), Resolved: pgAtPtr(c.Resolved), Closed: pgAtPtr(c.Closed),
		LastReporter: pgAt(c.LastReporter), Reopened: c.Reopened}
	r.OpenedKey, r.OpenedName, r.OpenedEmail, r.OpenedSystem = pgPerson(c.OpenedBy)
	r.ClosedKey, r.ClosedName, r.ClosedEmail, r.ClosedSystem = pgPerson(c.ClosedBy)
	r.FiledKey, r.FiledName, r.FiledEmail, r.FiledSystem = pgPerson(c.FiledBy)
	r.OwnerKey, r.OwnerName, r.OwnerEmail, r.OwnerSystem = pgPerson(c.Owner)
	r.CreatedByName, r.CreatedByKind = pgActor(c.CreatedBy)
	r.LastByName, r.LastByKind = pgActor(c.LastBy)
	if k := c.Key; k != nil {
		r.KeyCluster, r.KeyObject, r.KeyCheck = &k.Cluster, k.Object, k.Check
	}
	if s := c.Seen; s != nil {
		r.SeenCount, r.SeenFirst, r.SeenLast, r.SeenRun, r.SeenFinding, r.SeenAs = &s.Count, pgAt(s.First), pgAt(s.Last), s.Run, s.Finding, s.As
	}
	if p := c.Previous; p != nil {
		r.PrevCase, r.PrevOutcome, r.PrevExplain, r.PrevBy, r.PrevAt = &p.Case, string(p.Outcome), p.Explanation, p.By, pgAt(p.At)
	}
	if v := c.VerifiedBy; v != nil {
		r.VerRun, r.VerJob, r.VerAt, r.VerEvidence = &v.Run, v.Job, pgAt(v.At), v.Evidence
	}
	if a := c.AppliedBy; a != nil {
		r.AppliedCard, r.AppliedTitle, r.AppliedBy, r.AppliedAt = &a.Card, a.Title, a.By, pgAt(a.At)
	}
	// What the case's line in the list shows that the row's other columns
	// do not say: the title worked out from the text, and who is involved,
	// worked out from the messages.
	line := lineOf(c)
	r.LineTitle, r.Involved = line.Title, pgList(line.Involved)
	return r
}

// rowToCase is the case a row holds, without its messages, watchers and
// approvers.
func rowToCase(r *caseRow) Case {
	c := Case{Number: r.Number, Title: r.Title, Source: r.Source, Cluster: r.Cluster, Object: r.Object, Job: r.Job, Run: r.Run, Finding: r.Finding, Severity: r.Severity,
		RegressionOf: r.RegressionOf, SameAs: r.SameAs, PossibleDuplicateOf: r.PossibleDup, DuplicateOf: r.DuplicateOf, Similar: pgListOf(r.Similar),
		Exception: r.Exception, RCA: r.RCA, RCACause: r.RCACause, VerifyPending: r.VerifyPending, Verified: pgAtPtr(r.Verified), Unchecked: r.Unchecked,
		UncheckedWhy: r.UncheckedWhy, UncheckedRun: r.UncheckedRun, SLAFrom: pgAtPtr(r.SLAFrom), FixProposed: r.FixProposed, Verification: r.Verification, Type: r.Type,
		Incident: r.Incident, LastAt: pgAtPtr(r.LastAt), AnalysisQueued: pgAtPtr(r.Queued), Text: r.Text, Scrubbed: r.Scrubbed, Pruned: r.Pruned,
		Reporter: Person{ID: r.ReporterKey, Name: r.ReporterName, Email: r.ReporterEmail, System: r.ReporterSystem},
		Created:  pgTime(r.Created), Updated: pgTime(r.Updated), Status: Status(r.Status), StatusAt: pgTime(r.StatusAt), Outcome: Outcome(r.Outcome), Explanation: r.Explanation,
		Priority: r.Priority, PriorityBy: r.PriorityBy, Service: r.Service, ServiceConfidence: r.ServiceConfidence, ServiceBy: r.ServiceBy, KnownIssue: r.KnownIssue,
		MatchConfidence: r.MatchConfidence, Rejected: pgListOf(r.Rejected), Waiting: pgAtPtr(r.Waiting), Technical: r.Technical, Plain: r.Plain, Cause: r.Cause,
		Evidence: pgListOf(r.Evidence), Acknowledged: pgAtPtr(r.Acknowledged), FirstResponse: pgAtPtr(r.FirstResponse), Resolved: pgAtPtr(r.Resolved), Closed: pgAtPtr(r.Closed),
		LastReporter: pgTime(r.LastReporter), Reopened: r.Reopened}
	c.OpenedBy = pgPersonOf(r.OpenedKey, r.OpenedName, r.OpenedEmail, r.OpenedSystem)
	c.ClosedBy = pgPersonOf(r.ClosedKey, r.ClosedName, r.ClosedEmail, r.ClosedSystem)
	c.FiledBy = pgPersonOf(r.FiledKey, r.FiledName, r.FiledEmail, r.FiledSystem)
	c.Owner = pgPersonOf(r.OwnerKey, r.OwnerName, r.OwnerEmail, r.OwnerSystem)
	c.CreatedBy, c.LastBy = pgActorOf(r.CreatedByName, r.CreatedByKind), pgActorOf(r.LastByName, r.LastByKind)
	if r.KeyCluster != nil {
		c.Key = &Key{Cluster: *r.KeyCluster, Object: r.KeyObject, Check: r.KeyCheck}
	}
	if r.SeenCount != nil {
		c.Seen = &Seen{Count: *r.SeenCount, First: pgTime(r.SeenFirst), Last: pgTime(r.SeenLast), Run: r.SeenRun, Finding: r.SeenFinding, As: r.SeenAs}
	}
	if r.PrevCase != nil {
		c.Previous = &PreviousClose{Case: *r.PrevCase, Outcome: Outcome(r.PrevOutcome), Explanation: r.PrevExplain, By: r.PrevBy, At: pgTime(r.PrevAt)}
	}
	if r.VerRun != nil {
		c.VerifiedBy = &VerifiedBy{Run: *r.VerRun, Job: r.VerJob, At: pgTime(r.VerAt), Evidence: r.VerEvidence}
	}
	if r.AppliedCard != nil {
		c.AppliedBy = &AppliedChange{Card: *r.AppliedCard, Title: r.AppliedTitle, By: r.AppliedBy, At: pgTime(r.AppliedAt)}
	}
	return c
}

// The two roles of case_people.
const (
	roleWatcher  = "watcher"
	roleApprover = "approver"
)

// readCase is the case a row holds, whole.
func readCase(ctx context.Context, q db.Querier, id string) (Case, error) {
	var row caseRow
	if err := q.QueryRow(ctx, db.SelectSQL("cases", &row, "WHERE case_id = $1"), id).Scan(db.Fields(&row)...); err != nil {
		return Case{}, err
	}
	c := rowToCase(&row)
	rows, err := q.Query(ctx, `SELECT message_number, at, kind, from_key, from_name, from_email, from_is_system, body, is_draft, draft_by, is_internal,
			sent_by_key, sent_by_name, sent_by_email, sent_by_is_system
		FROM case_messages WHERE case_id = $1 ORDER BY position`, id)
	if err != nil {
		return c, err
	}
	var m Message
	var when time.Time
	var kind, sentName, sentEmail string
	var sentKey *string
	var sentSystem bool
	if _, err := pgx.ForEachRow(rows, []any{&m.ID, &when, &kind, &m.From.ID, &m.From.Name, &m.From.Email, &m.From.System, &m.Text, &m.Draft, &m.DraftBy, &m.Internal,
		&sentKey, &sentName, &sentEmail, &sentSystem}, func() error {
		x := m
		x.At, x.Kind, x.SentBy = when.UTC(), MessageKind(kind), pgPersonOf(sentKey, sentName, sentEmail, sentSystem)
		c.Messages = append(c.Messages, x)
		return nil
	}); err != nil {
		return c, err
	}
	rows, err = q.Query(ctx, "SELECT role, person_key, person_name, person_email, person_is_system FROM case_people WHERE case_id = $1 ORDER BY role, position", id)
	if err != nil {
		return c, err
	}
	var role string
	var p Person
	if _, err := pgx.ForEachRow(rows, []any{&role, &p.ID, &p.Name, &p.Email, &p.System}, func() error {
		if role == roleWatcher {
			c.Watchers = append(c.Watchers, p)
		} else {
			c.Approvers = append(c.Approvers, p)
		}
		return nil
	}); err != nil {
		return c, err
	}
	return c, nil
}

// writeCase changes a case's row and what hangs from it to hold c.
func writeCase(ctx context.Context, tx pgx.Tx, id string, c *Case) error {
	row := caseToRow(c)
	b := &pgx.Batch{}
	n := len(db.Columns(&row))
	b.Queue(strings.Replace(db.UpdateSQL("cases", &row, "case_id = $"+strconv.Itoa(n+1)), " WHERE ", ", is_case_kept = true WHERE ", 1), append(db.Fields(&row), id)...)
	b.Queue("DELETE FROM case_messages WHERE case_id = $1", id)
	b.Queue("DELETE FROM case_people WHERE case_id = $1", id)
	for i, m := range c.Messages {
		sk, sn, se, ss := pgPerson(m.SentBy)
		b.Queue(`INSERT INTO case_messages (case_id, position, message_number, at, kind, from_key, from_name, from_email, from_is_system, body, is_draft, draft_by, is_internal,
				sent_by_key, sent_by_name, sent_by_email, sent_by_is_system)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
			id, i, m.ID, pgAt(m.At), string(m.Kind), m.From.ID, m.From.Name, m.From.Email, m.From.System, m.Text, m.Draft, m.DraftBy, m.Internal, sk, sn, se, ss)
	}
	for role, people := range map[string][]Person{roleWatcher: c.Watchers, roleApprover: c.Approvers} {
		for i, p := range people {
			b.Queue("INSERT INTO case_people (case_id, role, position, person_key, person_name, person_email, person_is_system) VALUES ($1, $2, $3, $4, $5, $6, $7)",
				id, role, i, p.ID, p.Name, p.Email, p.System)
		}
	}
	return tx.SendBatch(ctx, b).Close()
}

// ---------- the case objects ----------

// CasePart is a key of a case's object that another package keeps: the
// work on the case, its conversation with Sherlock. name is the object's
// name, the case's number.
type CasePart interface {
	// Read returns the key's document, and whether the case keeps one.
	Read(ctx context.Context, q db.Querier, caseID, name string) ([]byte, bool, error)
	// Write changes the rows to hold doc.
	Write(ctx context.Context, tx pgx.Tx, caseID, name string, doc []byte) error
	// Drop removes what the part keeps that does not go with the case's row.
	Drop(ctx context.Context, tx pgx.Tx, caseID, name string) error
}

// CasesPG keeps the case objects in the database: one row of cases an
// object, with one version for all its keys.
type CasesPG struct {
	DB *db.DB
	// Parts are the keys other packages keep in a case's object.
	Parts map[string]CasePart
}

// NewCasesPG keeps the case objects in d.
func NewCasesPG(d *db.DB, parts map[string]CasePart) *CasesPG { return &CasesPG{DB: d, Parts: parts} }

func (s *CasesPG) partKeys() []string {
	keys := make([]string, 0, len(s.Parts))
	for k := range s.Parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Load returns every key of an object and its version.
func (s *CasesPG) Load(ctx context.Context, name string) (map[string][]byte, string, error) {
	out := map[string][]byte{}
	version := ""
	err := s.DB.Snapshot(ctx, func(q db.Querier) error {
		var id string
		var v int64
		var kept bool
		err := q.QueryRow(ctx, "SELECT case_id::text, version, is_case_kept FROM cases WHERE object_name = $1", name).Scan(&id, &v, &kept)
		if errors.Is(err, pgx.ErrNoRows) {
			return kv.ErrNotFound
		}
		if err != nil {
			return err
		}
		version = strconv.FormatInt(v, 10)
		if kept {
			c, err := readCase(ctx, q, id)
			if err != nil {
				return err
			}
			if out[CaseKey], err = json.Marshal(&c); err != nil {
				return err
			}
		}
		for _, key := range s.partKeys() {
			b, ok, err := s.Parts[key].Read(ctx, q, id, name)
			if err != nil {
				return err
			}
			if ok {
				out[key] = b
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, version, nil
}

// Save writes one key of an object over the version read: "" makes the
// object, and is refused when it is there; any other version is refused
// unless the object is still at it.
func (s *CasesPG) Save(ctx context.Context, name, key string, b []byte, version string) (string, error) {
	part, isPart := s.Parts[key]
	if key != CaseKey && !isPart {
		return "", fmt.Errorf("a case's object has no key %s in the database", key)
	}
	out := ""
	err := s.DB.Tx(ctx, func(tx pgx.Tx) error {
		var id string
		var v int64
		err := tx.QueryRow(ctx, "SELECT case_id::text, version FROM cases WHERE object_name = $1 FOR UPDATE", name).Scan(&id, &v)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if (version == "" && found) || (version != "" && (!found || strconv.FormatInt(v, 10) != version)) {
			return kv.ErrConflict
		}
		if !found {
			id = db.NewID()
			number, _ := strconv.Atoi(name)
			if _, err := tx.Exec(ctx, "INSERT INTO cases (case_id, object_name, case_number) VALUES ($1, $2, $3)", id, name, number); err != nil {
				return err
			}
		}
		if key == CaseKey {
			var c Case
			if err := json.Unmarshal(b, &c); err != nil {
				return err
			}
			if err := writeCase(ctx, tx, id, &c); err != nil {
				return err
			}
		} else if err := part.Write(ctx, tx, id, name, b); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "UPDATE cases SET version = version + 1 WHERE case_id = $1 RETURNING version", id).Scan(&v); err != nil {
			return err
		}
		out = strconv.FormatInt(v, 10)
		return nil
	})
	// Two writers that both make the object: the second finds its name taken.
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return "", kv.ErrConflict
	}
	return out, err
}

// Delete removes an object with every key; one that is not there is no error.
func (s *CasesPG) Delete(ctx context.Context, name string) error {
	return s.DB.Tx(ctx, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, "SELECT case_id::text FROM cases WHERE object_name = $1 FOR UPDATE", name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, key := range s.partKeys() {
			if err := s.Parts[key].Drop(ctx, tx, id, name); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "DELETE FROM cases WHERE case_id = $1", id)
		return err
	})
}

// List returns the names of the objects, sorted as text.
func (s *CasesPG) List(ctx context.Context) ([]string, error) {
	var out []string
	err := s.DB.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT object_name FROM cases ORDER BY object_name")
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if out == nil {
		out = []string{}
	}
	return out, err
}

// CasesImportID is the name the one-time copy of the case objects is
// recorded under.
const CasesImportID = "cases/objects"

// ImportID is the name the copy is recorded under.
func (s *CasesPG) ImportID() string { return CasesImportID }

// Import copies the case objects another store kept, once (DB-17), and
// says how many. An object the database already holds is left alone.
func (s *CasesPG) Import(ctx context.Context, from kv.Objects, source string) (int, error) {
	copied := 0
	_, err := s.DB.ImportOnce(ctx, s.ImportID(), source, func(ctx context.Context, tx pgx.Tx) (int, error) {
		copied = 0
		names, err := from.List(ctx)
		if err != nil {
			return 0, err
		}
		for _, name := range names {
			keys, _, err := from.Load(ctx, name)
			if errors.Is(err, kv.ErrNotFound) {
				continue
			}
			if err != nil {
				return 0, err
			}
			var held int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM cases WHERE object_name = $1", name).Scan(&held); err != nil {
				return 0, err
			}
			if held > 0 {
				continue
			}
			id := db.NewID()
			number, _ := strconv.Atoi(name)
			if _, err := tx.Exec(ctx, "INSERT INTO cases (case_id, object_name, case_number, version) VALUES ($1, $2, $3, 1)", id, name, number); err != nil {
				return 0, err
			}
			if b := keys[CaseKey]; len(b) > 0 {
				var c Case
				// What cannot be read was not readable before either.
				if json.Unmarshal(b, &c) == nil {
					if err := writeCase(ctx, tx, id, &c); err != nil {
						return 0, err
					}
				}
			}
			for _, key := range s.partKeys() {
				if b := keys[key]; len(b) > 0 && json.Valid(b) {
					if err := s.Parts[key].Write(ctx, tx, id, name, b); err != nil {
						return 0, err
					}
				}
			}
			copied++
		}
		return copied, nil
	})
	return copied, err
}

// ---------- the index ----------

// indexKept is what the scope "case-index" holds: what the index says
// that no case does.
type indexKept struct {
	Next     int            `json:"next"`
	Archived map[string]int `json:"archived,omitempty"`
}

func readIndex(ctx context.Context, q db.Querier) (indexDoc, bool, error) {
	var doc indexDoc
	var kept indexKept
	found, err := pgLoadScope(ctx, q, indexScope, &kept)
	if err != nil {
		return doc, false, err
	}
	doc.Next, doc.Archived = kept.Next, kept.Archived
	watchers := map[string][]Person{}
	rows, err := q.Query(ctx, "SELECT case_id::text, person_key FROM case_people WHERE role = $1 ORDER BY case_id, position", roleWatcher)
	if err != nil {
		return doc, false, err
	}
	var id, who string
	if _, err := pgx.ForEachRow(rows, []any{&id, &who}, func() error {
		watchers[id] = append(watchers[id], Person{ID: who})
		return nil
	}); err != nil {
		return doc, false, err
	}
	// Every column of a case but its text, which a line does not show.
	var row caseRow
	cols := db.Columns(&row)
	for i, c := range cols {
		if c == "body" {
			cols[i] = "'' AS body"
		}
	}
	rows, err = q.Query(ctx, "SELECT case_id::text, "+strings.Join(cols, ", ")+" FROM cases WHERE is_case_kept ORDER BY case_number, object_name")
	if err != nil {
		return doc, false, err
	}
	if _, err := pgx.ForEachRow(rows, append([]any{&id}, db.Fields(&row)...), func() error {
		c := rowToCase(&row)
		c.Watchers = watchers[id]
		l := lineOf(&c)
		l.Title, l.Involved = row.LineTitle, pgListOf(row.Involved)
		doc.Lines = append(doc.Lines, l)
		return nil
	}); err != nil {
		return doc, false, err
	}
	return doc, found || len(doc.Lines) > 0, nil
}

// writeIndex writes the counter and the archived counts. The lines are
// the cases' own rows, written when a case is saved.
func writeIndex(ctx context.Context, tx pgx.Tx, old, next indexDoc) error {
	was, now := indexKept{old.Next, old.Archived}, indexKept{next.Next, next.Archived}
	if pgSame(was, now) {
		return nil
	}
	return pgSaveScope(ctx, tx, indexScope, now)
}

// ---------- the known issues ----------

func readKnown(ctx context.Context, q db.Querier) (knownDoc, bool, error) {
	var doc knownDoc
	var n struct {
		Next int `json:"next"`
	}
	found, err := pgLoadScope(ctx, q, knownScope, &n)
	if err != nil {
		return doc, false, err
	}
	doc.Next = n.Next
	var id string
	var when time.Time
	updates := map[string][]Update{}
	rows, err := q.Query(ctx, "SELECT known_issue_id::text, at, by_key, by_name, by_email, by_is_system, body, status FROM known_issue_updates ORDER BY known_issue_id, position")
	if err != nil {
		return doc, false, err
	}
	var u Update
	var status string
	if _, err := pgx.ForEachRow(rows, []any{&id, &when, &u.By.ID, &u.By.Name, &u.By.Email, &u.By.System, &u.Text, &status}, func() error {
		x := u
		x.At, x.Status = when.UTC(), KnownStatus(status)
		updates[id] = append(updates[id], x)
		return nil
	}); err != nil {
		return doc, false, err
	}
	followers := map[string][]Person{}
	rows, err = q.Query(ctx, "SELECT known_issue_id::text, person_key, person_name, person_email, person_is_system FROM known_issue_followers ORDER BY known_issue_id, position")
	if err != nil {
		return doc, false, err
	}
	var p Person
	if _, err := pgx.ForEachRow(rows, []any{&id, &p.ID, &p.Name, &p.Email, &p.System}, func() error {
		followers[id] = append(followers[id], p)
		return nil
	}); err != nil {
		return doc, false, err
	}
	keys := map[string][]KnownFound{}
	rows, err = q.Query(ctx, "SELECT known_issue_id::text, cluster_id, object, check_name, job_id FROM known_issue_keys ORDER BY known_issue_id, position")
	if err != nil {
		return doc, false, err
	}
	var kf KnownFound
	if _, err := pgx.ForEachRow(rows, []any{&id, &kf.Cluster, &kf.Object, &kf.Check, &kf.Job}, func() error {
		keys[id] = append(keys[id], kf)
		return nil
	}); err != nil {
		return doc, false, err
	}
	rows, err = q.Query(ctx, `SELECT known_issue_id::text, known_issue_key, title, symptoms, services, status, workaround, expected, owner_key, owner_name, owner_email, owner_is_system,
			kind, window_start_at, window_end_at, source, is_draft, link, created_by_key, created_by_name, created_by_email, created_by_is_system, created_at, updated_at,
			resolved_at, still_present_count, still_present_last_at, still_present_run_key, verify_jobs, is_pending, is_inactive, duplicate_of_known_issue_key
		FROM known_issues ORDER BY position`)
	if err != nil {
		return doc, false, err
	}
	var k KnownIssue
	var symptoms, services, verify []string
	var ownerKey, byKey *string
	var ownerName, ownerEmail, byName, byEmail, stillRun string
	var ownerSystem, bySystem bool
	var winStart, winEnd, resolved, stillLast *time.Time
	var created, updated time.Time
	var stillCount *int
	if _, err := pgx.ForEachRow(rows, []any{&id, &k.ID, &k.Title, &symptoms, &services, &status, &k.Workaround, &k.Expected, &ownerKey, &ownerName, &ownerEmail, &ownerSystem,
		&k.Kind, &winStart, &winEnd, &k.Source, &k.Draft, &k.Link, &byKey, &byName, &byEmail, &bySystem, &created, &updated,
		&resolved, &stillCount, &stillLast, &stillRun, &verify, &k.Pending, &k.Inactive, &k.DuplicateOf}, func() error {
		x := k
		x.Symptoms, x.Services, x.VerifyJobs, x.Status = pgListOf(symptoms), pgListOf(services), pgListOf(verify), KnownStatus(status)
		x.Owner, x.CreatedBy = pgPersonOf(ownerKey, ownerName, ownerEmail, ownerSystem), pgPersonOf(byKey, byName, byEmail, bySystem)
		x.Created, x.Updated, x.Resolved = created.UTC(), updated.UTC(), pgAtPtr(resolved)
		x.Window, x.StillPresent = nil, nil
		if winStart != nil {
			x.Window = &Window{Start: winStart.UTC(), End: pgTime(winEnd)}
		}
		if stillCount != nil {
			x.StillPresent = &StillPresent{Count: *stillCount, Last: pgTime(stillLast), Run: stillRun}
		}
		x.Updates, x.Followers, x.Keys = updates[id], followers[id], keys[id]
		doc.Known = append(doc.Known, x)
		return nil
	}); err != nil {
		return doc, false, err
	}
	return doc, found || len(doc.Known) > 0, nil
}

func writeKnown(ctx context.Context, tx pgx.Tx, old, next knownDoc) error {
	if old.Next != next.Next {
		if err := pgSaveScope(ctx, tx, knownScope, struct {
			Next int `json:"next"`
		}{next.Next}); err != nil {
			return err
		}
	}
	placed, err := db.Placed(ctx, tx, "known_issues", "known_issue_key")
	if err != nil {
		return err
	}
	before := map[string]KnownIssue{}
	for _, k := range old.Known {
		before[k.ID] = k
	}
	kept := make([]string, len(next.Known))
	for i, k := range next.Known {
		kept[i] = k.ID
	}
	places := db.Places(kept, placed)
	b := &pgx.Batch{}
	for i, k := range next.Known {
		if was, had := before[k.ID]; had && pgSame(was, k) {
			if placed[k.ID] != places[i] {
				b.Queue("UPDATE known_issues SET position = $2 WHERE known_issue_key = $1", k.ID, places[i])
			}
			continue
		}
		// Whole: its updates, followers and keys go with the row.
		b.Queue("DELETE FROM known_issues WHERE known_issue_key = $1", k.ID)
		id := db.NewID()
		ok, on, oe, os := pgPerson(k.Owner)
		ck, cn, ce, cs := pgPerson(k.CreatedBy)
		var winStart, winEnd, stillLast *time.Time
		var stillCount *int
		stillRun := ""
		if w := k.Window; w != nil {
			winStart, winEnd = pgAt(w.Start), pgAt(w.End)
		}
		if sp := k.StillPresent; sp != nil {
			stillCount, stillLast, stillRun = &sp.Count, pgAt(sp.Last), sp.Run
		}
		b.Queue(`INSERT INTO known_issues (known_issue_id, known_issue_key, position, title, symptoms, services, status, workaround, expected, owner_key, owner_name, owner_email,
				owner_is_system, kind, window_start_at, window_end_at, source, is_draft, link, created_by_key, created_by_name, created_by_email, created_by_is_system, created_at,
				updated_at, resolved_at, still_present_count, still_present_last_at, still_present_run_key, verify_jobs, is_pending, is_inactive, duplicate_of_known_issue_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33)`,
			id, k.ID, places[i], k.Title, pgList(k.Symptoms), pgList(k.Services), string(k.Status), k.Workaround, k.Expected, ok, on, oe,
			os, k.Kind, winStart, winEnd, k.Source, k.Draft, k.Link, ck, cn, ce, cs, pgAt(k.Created),
			pgAt(k.Updated), pgAtPtr(k.Resolved), stillCount, stillLast, stillRun, pgList(k.VerifyJobs), k.Pending, k.Inactive, k.DuplicateOf)
		for n, u := range k.Updates {
			b.Queue("INSERT INTO known_issue_updates (known_issue_id, position, at, by_key, by_name, by_email, by_is_system, body, status) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
				id, n, pgAt(u.At), u.By.ID, u.By.Name, u.By.Email, u.By.System, u.Text, string(u.Status))
		}
		for n, p := range k.Followers {
			b.Queue("INSERT INTO known_issue_followers (known_issue_id, position, person_key, person_name, person_email, person_is_system) VALUES ($1, $2, $3, $4, $5, $6)",
				id, n, p.ID, p.Name, p.Email, p.System)
		}
		for n, f := range k.Keys {
			b.Queue("INSERT INTO known_issue_keys (known_issue_id, position, cluster_id, object, check_name, job_id) VALUES ($1, $2, $3, $4, $5, $6)", id, n, f.Cluster, f.Object, f.Check, f.Job)
		}
	}
	b.Queue("DELETE FROM known_issues WHERE NOT (known_issue_key = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}

// ---------- the settings and the services ----------

// issuesKept is what the scope "issues" holds. The intake's API tokens
// from before 0.12.1, which nothing reads, are not kept in the database.
type issuesKept struct {
	Settings *Settings `json:"settings,omitempty"`
}

func readConfig(ctx context.Context, q db.Querier) (configDoc, bool, error) {
	var doc configDoc
	var kept issuesKept
	found, err := pgLoadScope(ctx, q, issuesScope, &kept)
	if err != nil {
		return doc, false, err
	}
	doc.Settings = kept.Settings
	runsOn := map[string][]RunsOn{}
	rows, err := q.Query(ctx, "SELECT service_id, kind, value, cluster_id FROM service_runs_on ORDER BY service_id, position")
	if err != nil {
		return doc, false, err
	}
	var id string
	var ro RunsOn
	if _, err := pgx.ForEachRow(rows, []any{&id, &ro.Kind, &ro.Value, &ro.Cluster}, func() error {
		runsOn[id] = append(runsOn[id], ro)
		return nil
	}); err != nil {
		return doc, false, err
	}
	rows, err = q.Query(ctx, "SELECT service_id, name, description, contacts, aliases, created_at, updated_at FROM services ORDER BY position")
	if err != nil {
		return doc, false, err
	}
	var s ServiceDef
	var contacts, aliases []string
	var created, updated time.Time
	if _, err := pgx.ForEachRow(rows, []any{&s.ID, &s.Name, &s.Description, &contacts, &aliases, &created, &updated}, func() error {
		x := s
		x.Contacts, x.Aliases, x.Created, x.Updated, x.RunsOn = pgListOf(contacts), pgListOf(aliases), created.UTC(), updated.UTC(), runsOn[x.ID]
		doc.Services = append(doc.Services, x)
		return nil
	}); err != nil {
		return doc, false, err
	}
	return doc, found || len(doc.Services) > 0, nil
}

func writeConfig(ctx context.Context, tx pgx.Tx, old, next configDoc) error {
	if !pgSame(old.Settings, next.Settings) {
		if err := pgSaveScope(ctx, tx, issuesScope, issuesKept{next.Settings}); err != nil {
			return err
		}
	}
	before, places := map[string]ServiceDef{}, map[string]int{}
	for i, s := range old.Services {
		before[s.ID], places[s.ID] = s, i
	}
	b := &pgx.Batch{}
	kept := make([]string, 0, len(next.Services))
	for i, s := range next.Services {
		kept = append(kept, s.ID)
		if was, had := before[s.ID]; had && pgSame(was, s) {
			if places[s.ID] != i {
				b.Queue("UPDATE services SET position = $2 WHERE service_id = $1", s.ID, i)
			}
			continue
		}
		b.Queue("DELETE FROM services WHERE service_id = $1", s.ID)
		b.Queue("INSERT INTO services (service_id, position, name, description, contacts, aliases, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			s.ID, i, s.Name, s.Description, pgList(s.Contacts), pgList(s.Aliases), pgAt(s.Created), pgAt(s.Updated))
		for n, r := range s.RunsOn {
			b.Queue("INSERT INTO service_runs_on (service_id, position, kind, value, cluster_id) VALUES ($1, $2, $3, $4, $5)", s.ID, n, r.Kind, r.Value, r.Cluster)
		}
	}
	b.Queue("DELETE FROM services WHERE NOT (service_id = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}

// NewSetPG keeps the index, the known issues and the settings in d, at
// one version.
func NewSetPG(d *db.DB) *db.DocSet {
	return &db.DocSet{DB: d, Name: "issues", Members: map[string]db.SetMember{
		IndexKey:  db.Member(readIndex, writeIndex),
		KnownKey:  db.Member(readKnown, writeKnown),
		ConfigKey: db.Member(readConfig, writeConfig),
	}}
}
