package twoperson

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The requests in the database (Phase 13, DB-25): each a row of
// approval_requests, its decision a row of approval_decisions. The store
// above stays as it is and reads and writes them through NewPG, an
// interim step (db.Doc) until it is given a repository of its own; from
// then on a request can wait for more than one decision.

// counterScope is the settings row that keeps the next request's number,
// so a number is never given twice, whatever was pruned.
const counterScope = "approvals"

// asked is what a request's summary column holds: what was asked, as its
// kind has it.
type asked struct {
	ClusterName string   `json:"clusterName,omitempty"`
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
	User        string   `json:"user,omitempty"`
	Super       bool     `json:"super,omitempty"`
	Groups      []string `json:"groups,omitempty"`
	Agent       string   `json:"agent,omitempty"`
	AgentName   string   `json:"agentName,omitempty"`
	Rev         int      `json:"rev,omitempty"`
	Untested    string   `json:"untested,omitempty"`
}

// NewPG keeps the requests in d, behind the backend the store uses.
func NewPG(d *db.DB) *db.Doc[record] {
	return &db.Doc[record]{DB: d, Key: Key, Read: readRecord, Write: writeRecord}
}

func readRecord(ctx context.Context, q db.Querier) (record, bool, error) {
	var rec record
	b, _, err := db.LoadScope(ctx, q, counterScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return rec, false, err
	}
	if found {
		var c struct {
			Next int `json:"next"`
		}
		_ = json.Unmarshal(b, &c)
		rec.Next = c.Next
	}
	rows, err := q.Query(ctx, `SELECT r.request_number, r.kind, r.cluster_id, r.summary, r.requested_by, r.requested_by_key, r.created_at, r.state,
			d.decided_by, d.decided_by_key, d.decided_at, d.comment, d.verdict
		FROM approval_requests r
		LEFT JOIN approval_decisions d ON d.approval_request_id = r.approval_request_id
		WHERE r.kind IN ($1, $2, $3) ORDER BY r.request_number`, KindEnvironment, KindSuperAdmin, KindPrompt)
	if err != nil {
		return rec, false, err
	}
	rec.Requests, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Request, error) {
		var r Request
		var summary []byte
		var by, byKey, comment, verdict *string
		var decided *time.Time
		if err := row.Scan(&r.ID, &r.Kind, &r.Cluster, &summary, &r.By.Name, &r.By.Key, &r.At, &r.State, &by, &byKey, &decided, &comment, &verdict); err != nil {
			return r, err
		}
		var a asked
		_ = json.Unmarshal(summary, &a)
		r.ClusterName, r.From, r.To, r.User, r.Super, r.Groups = a.ClusterName, a.From, a.To, a.User, a.Super, a.Groups
		r.Agent, r.AgentName, r.Rev, r.Untested = a.Agent, a.AgentName, a.Rev, a.Untested
		r.At = r.At.UTC()
		if by != nil {
			r.DecidedBy = &Who{Key: *byKey, Name: *by}
			r.Comment, r.Withdrawn = *comment, *verdict == "withdrawn"
			if decided != nil {
				t := decided.UTC()
				r.DecidedAt = &t
			}
		}
		return r, nil
	})
	if len(rec.Requests) == 0 {
		rec.Requests = nil
	}
	return rec, found || len(rec.Requests) > 0, err
}

func writeRecord(ctx context.Context, tx pgx.Tx, old, next record) error {
	if next.Next != old.Next {
		b, _ := json.Marshal(map[string]int{"next": next.Next})
		if _, err := db.SaveScope(ctx, tx, counterScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	remove := func(r Request) error {
		_, err := tx.Exec(ctx, "DELETE FROM approval_requests WHERE request_number = $1 AND kind = $2", r.ID, r.Kind)
		return err
	}
	summaryOf := func(r Request) []byte {
		b, _ := json.Marshal(asked{ClusterName: r.ClusterName, From: r.From, To: r.To, User: r.User, Super: r.Super, Groups: r.Groups,
			Agent: r.Agent, AgentName: r.AgentName, Rev: r.Rev, Untested: r.Untested})
		return b
	}
	decision := func(id string, r Request) error {
		if r.DecidedBy == nil {
			return nil
		}
		verdict := r.State
		if r.Withdrawn {
			verdict = "withdrawn"
		}
		_, err := tx.Exec(ctx, `INSERT INTO approval_decisions (approval_decision_id, approval_request_id, decided_by, decided_by_key, verdict, comment, decided_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, db.NewID(), id, r.DecidedBy.Name, r.DecidedBy.Key, verdict, r.Comment, r.DecidedAt)
		return err
	}
	add := func(r Request) error {
		id := db.NewID()
		// One other person decides today; the count is here for the day a
		// request needs more.
		if _, err := tx.Exec(ctx, `INSERT INTO approval_requests
			(approval_request_id, request_number, kind, cluster_id, summary, requested_by, requested_by_key, required_count, state, created_at, decided_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $9, $10)`,
			id, r.ID, r.Kind, r.Cluster, summaryOf(r), r.By.Name, r.By.Key, r.State, r.At.UTC(), r.DecidedAt); err != nil {
			return err
		}
		return decision(id, r)
	}
	// A request changes once, when it is decided: its row is updated in
	// place, so that what points at it (a draft of instructions, DB-91)
	// keeps pointing at it, and its decision is written anew.
	change := func(_, r Request) error {
		var id string
		err := tx.QueryRow(ctx, `UPDATE approval_requests SET cluster_id = $3, summary = $4, state = $5, decided_at = $6
			WHERE request_number = $1 AND kind = $2 RETURNING approval_request_id::text`,
			r.ID, r.Kind, r.Cluster, summaryOf(r), r.State, r.DecidedAt).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return add(r)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM approval_decisions WHERE approval_request_id = $1", id); err != nil {
			return err
		}
		return decision(id, r)
	}
	key := func(r Request) string { return strconv.Itoa(r.ID) }
	return db.Diff(old.Requests, next.Requests, key, add, change, remove)
}
