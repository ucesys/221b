package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Automation's runs and the requests for a run as one repository in the
// database (DB-87). Until now they were two documents (pg_runs.go,
// pg_changes.go) written into one table, each save reading every run or
// every request first. Each operation here reads and writes the rows of
// one run or one request: a run is started, a host's part is recorded
// with its whole output, a run is saved as it goes and when it ends, a
// cancel is asked for, a request is made, decided or expired.
//
// The two documents stay, for what reads them whole: the one-time copy
// from a ConfigMap, the move of sensitive values into their Secrets and
// the liveness of the values a waiting request keeps there (DB-71). An
// operation takes the turn of that document's writers (db.LockDoc), so
// a whole read and an operation never write over each other: the runs'
// document for a run, the changes' document for a request.
//
// A sensitive value is hidden as the documents hid it: a run's own value,
// what each node got and what each fleet had are their digests, and what
// a waiting request would use waits in the Secret, written before the
// commit (row, then Secret, then commit).

// RunRepo keeps the runs and the requests for a run (DB-87).
type RunRepo interface {
	// NewRun records a run and gives it its number: ErrSameRequest with
	// the run a request asked for again started, ErrBusy for a node a
	// live run has.
	NewRun(ctx context.Context, r Run, now time.Time) (Run, error)
	// PutRun saves a run as it goes, and as it ended; it says whether a
	// cancel was asked for through the record.
	PutRun(ctx context.Context, r Run) (bool, error)
	// PutNode records one host's part of a run as it ended, with its
	// whole output (masked: the engine never writes a secret into it).
	PutNode(ctx context.Context, id int, n NodeResult, output string) error
	// AskCancel marks a live run for cancelling.
	AskCancel(ctx context.Context, id int, now time.Time) (bool, error)
	// Runs lists every kept run, and Run gets one, as they are kept.
	Runs(ctx context.Context) ([]Run, error)
	Run(ctx context.Context, id int) (Run, bool, error)
	// Outputs is each host's whole output of a run, by host.
	Outputs(ctx context.Context, id int) (map[string]string, error)
	// NewRequest records a run that waits for approval, or answers the
	// one asked for already under the same request.
	NewRequest(ctx context.Context, w WaitingRun) (WaitingRun, error)
	// Requests lists the requests of a cluster (every cluster's for ""),
	// newest first.
	Requests(ctx context.Context, cluster string) ([]WaitingRun, error)
	// DecideRequest has decide change one request, and answers it as it
	// was and as it is.
	DecideRequest(ctx context.Context, cluster string, id int, decide func(*WaitingRun) error) (was, is WaitingRun, err error)
	// ExpireRequests marks those that waited longer than after expired,
	// as they were and as they are.
	ExpireRequests(ctx context.Context, after time.Duration, said string, now time.Time) (was, is []WaitingRun, err error)
}

// KeepRunsIn has the store keep its runs and run requests in d, through
// the repository; the documents of the runs and the changes stay for the
// copy and the sensitive values (see above).
func (st *Store) KeepRunsIn(d *db.DB) { st.runs = &pgRuns{d: d} }

type pgRuns struct{ d *db.DB }

// sameAs is the condition that the person kept in the columns name and
// user is the one in $n (the name) and $n+1 (the user), as Who.Same
// says.
func sameAs(name, user string, n int) string {
	return fmt.Sprintf("(CASE WHEN %s <> '' OR $%d::text <> '' THEN %s = $%d::text ELSE %s = $%d::text END)", user, n+1, user, n+1, name, n)
}

func (p *pgRuns) NewRun(ctx context.Context, r Run, now time.Time) (Run, error) {
	var same Run
	err := p.d.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.LockDoc(ctx, tx, RunsKey); err != nil {
			return err
		}
		if r.Request != "" {
			runs, err := readRunsWhere(ctx, tx, false, "request_key = $1 AND "+sameAs("started_by", "started_by_user", 2), r.Request, r.By.Name, r.By.User)
			if err != nil {
				return err
			}
			if len(runs) > 0 {
				same = runs[0]
				return ErrSameRequest
			}
		}
		if !r.DryRun && r.Target != TargetKubernetes && len(r.Nodes) > 0 {
			nodes := make([]string, len(r.Nodes))
			for i, n := range r.Nodes {
				nodes[i] = n.Node
			}
			var host, script, by string
			var number, version int
			err := tx.QueryRow(ctx, `SELECT h.host_name, x.run_number, x.script_name, x.version_number, x.started_by
				FROM script_runs x JOIN script_run_hosts h USING (script_run_id)
				WHERE x.run_number IS NOT NULL AND x.cluster_id = $1 AND NOT x.is_dry_run AND x.target <> $2 AND x.state = $3 AND x.heartbeat_at > $4
					AND h.host_name = ANY($5)
				ORDER BY x.run_number, h.position LIMIT 1`, r.Cluster, TargetKubernetes, RunRunning, now.Add(-staleAfter), nodes).Scan(&host, &number, &script, &version, &by)
			if err == nil {
				return busyError(fmt.Sprintf("%s is in run #%d (%s v%d, by %s), which is still running: wait for it to end, cancel it, or leave the node out.", host, number, script, version, by))
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// The number: the counter, never below a run kept.
		next := 0
		if b, _, err := db.LoadScope(ctx, tx, runsScope); err == nil {
			var c struct {
				Next int `json:"next"`
			}
			if err := json.Unmarshal(b, &c); err != nil {
				return err
			}
			next = c.Next
		} else if !errors.Is(err, db.ErrNotFound) {
			return err
		}
		var top int
		if err := tx.QueryRow(ctx, "SELECT coalesce(max(run_number), 0) FROM script_runs").Scan(&top); err != nil {
			return err
		}
		r.ID = max(next, top+1, 1)
		b, _ := json.Marshal(map[string]int{"next": r.ID + 1})
		if _, err := db.SaveScope(ctx, tx, runsScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
		kept, err := sealedRun(ctx, tx, p.d, r)
		if err != nil {
			return err
		}
		if err := writeRun(ctx, tx, Run{}, kept, false); err != nil {
			return err
		}
		// The newest MaxRuns are kept, as Run History lists them.
		if _, err := tx.Exec(ctx, `DELETE FROM script_runs WHERE run_number IS NOT NULL AND run_number NOT IN (
				SELECT run_number FROM script_runs WHERE run_number IS NOT NULL ORDER BY run_number DESC LIMIT $1)`, MaxRuns); err != nil {
			return err
		}
		return p.d.Notify(ctx, tx, "doc/"+RunsKey)
	})
	if errors.Is(err, ErrSameRequest) {
		return same, ErrSameRequest
	}
	return r, err
}

// sealedRun is a copy of r as its rows keep it: the sensitive values it
// used are their digests (sealRuns). r keeps its values, which a run
// started with them still reads.
func sealedRun(ctx context.Context, tx pgx.Tx, d *db.DB, r Run) (Run, error) {
	names, err := namesIn(ctx, tx)
	if err != nil {
		return Run{}, err
	}
	rec := runsRecord{Runs: []Run{clone(r)}}
	if err := sealRuns(newRing(ctx, sensitiveFor(d)), &rec, names, nil); err != nil {
		return Run{}, err
	}
	return rec.Runs[0], nil
}

func (p *pgRuns) PutRun(ctx context.Context, r Run) (bool, error) {
	cancel := false
	err := p.d.Tx(ctx, func(tx pgx.Tx) error {
		cancel = false
		if err := db.LockDoc(ctx, tx, RunsKey); err != nil {
			return err
		}
		was, err := readRunsWhere(ctx, tx, false, "run_number = $1", r.ID)
		if err != nil {
			return err
		}
		had := len(was) > 0
		var old Run
		if had {
			old = was[0]
			// A run that ended stays as it ended, whatever a late save says.
			if old.State != RunRunning && r.State == RunRunning {
				return nil
			}
			r.CancelAsked = r.CancelAsked || old.CancelAsked
			cancel = r.CancelAsked
		}
		kept, err := sealedRun(ctx, tx, p.d, r)
		if err != nil {
			return err
		}
		if had && sameJSON(old, kept) {
			return nil
		}
		if err := writeRun(ctx, tx, old, kept, had); err != nil {
			return err
		}
		return p.d.Notify(ctx, tx, "doc/"+RunsKey)
	})
	return cancel, err
}

// cutMark is what a host's output starts with when its start was not
// kept; is_cut stands for it in the row.
const cutMark = "[earlier output not kept]\n"

// keptOutput is a host's output as its row keeps it: valid text, its
// newest OutputBytes, and whether what came before was cut.
func keptOutput(s string) (string, bool) {
	s, cut := strings.CutPrefix(s, cutMark)
	if len(s) > OutputBytes {
		s, cut = last(s, OutputBytes), true
	}
	return strings.ToValidUTF8(s, ""), cut
}

func (p *pgRuns) PutNode(ctx context.Context, id int, n NodeResult, output string) error {
	text, cut := keptOutput(output)
	return p.d.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.LockDoc(ctx, tx, RunsKey); err != nil {
			return err
		}
		var host string
		err := tx.QueryRow(ctx, `SELECT h.script_run_host_id::text FROM script_run_hosts h JOIN script_runs x USING (script_run_id)
			WHERE x.run_number = $1 AND h.host_name = $2`, id, n.Node).Scan(&host)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: run %d has no node %s", ErrNotFound, id, n.Node)
		}
		if err != nil {
			return err
		}
		var started, ended *time.Time
		if n.Started != nil {
			started = timeOrNil(*n.Started)
		}
		if n.Ended != nil {
			ended = timeOrNil(*n.Ended)
		}
		if _, err := tx.Exec(ctx, `UPDATE script_run_hosts SET state = $2, exit_code = $3, message = $4, error = $5, started_at = $6, ended_at = $7,
				output_tail = $8, is_cordoned = $9 WHERE script_run_host_id = $1`,
			host, n.State, n.Exit, n.Msg, n.Error, started, ended, n.Tail, n.Cordoned); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO script_run_outputs (script_run_host_id, output, is_cut) VALUES ($1, $2, $3)
			ON CONFLICT (script_run_host_id) DO UPDATE SET output = excluded.output, is_cut = excluded.is_cut`, host, text, cut); err != nil {
			return err
		}
		return p.d.Notify(ctx, tx, "doc/"+RunsKey)
	})
}

func (p *pgRuns) AskCancel(ctx context.Context, id int, now time.Time) (bool, error) {
	found := false
	err := p.d.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.LockDoc(ctx, tx, RunsKey); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "UPDATE script_runs SET is_cancel_asked = true WHERE run_number = $1 AND state = $2 AND heartbeat_at > $3", id, RunRunning, now.Add(-staleAfter))
		if err != nil {
			return err
		}
		if found = tag.RowsAffected() > 0; !found {
			return nil
		}
		return p.d.Notify(ctx, tx, "doc/"+RunsKey)
	})
	return found, err
}

func (p *pgRuns) Runs(ctx context.Context) ([]Run, error) {
	var out []Run
	err := p.d.Snapshot(ctx, func(q db.Querier) (err error) {
		out, err = readRunsWhere(ctx, q, false, "")
		return err
	})
	return out, err
}

func (p *pgRuns) Run(ctx context.Context, id int) (Run, bool, error) {
	var out []Run
	err := p.d.Snapshot(ctx, func(q db.Querier) (err error) {
		out, err = readRunsWhere(ctx, q, false, "run_number = $1", id)
		return err
	})
	if err != nil || len(out) == 0 {
		return Run{}, false, err
	}
	return out[0], true, nil
}

func (p *pgRuns) Outputs(ctx context.Context, id int) (map[string]string, error) {
	out := map[string]string{}
	err := p.d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT h.host_name, o.output, o.is_cut FROM script_run_outputs o JOIN script_run_hosts h USING (script_run_host_id)
			JOIN script_runs x USING (script_run_id) WHERE x.run_number = $1`, id)
		if err != nil {
			return err
		}
		var host, text string
		var cut bool
		_, err = pgx.ForEachRow(rows, []any{&host, &text, &cut}, func() error {
			if cut {
				out[host] = cutMark + text
			} else {
				out[host] = text
			}
			return nil
		})
		return err
	})
	return out, err
}

// requestsCounter reads and sets the requests' counter, which the
// changes' scope keeps beside the changes' own.
func requestsCounter(ctx context.Context, tx pgx.Tx) (changesRest, error) {
	var rest changesRest
	b, _, err := db.LoadScope(ctx, tx, changesScope)
	if errors.Is(err, db.ErrNotFound) {
		return rest, nil
	}
	if err != nil {
		return rest, err
	}
	return rest, json.Unmarshal(b, &rest)
}

// writeSealedRequest hides what a request's values hold, writes its rows
// and has the Secret keep what a waiting one would use, before the
// commit.
func (p *pgRuns) writeSealedRequest(ctx context.Context, tx pgx.Tx, w WaitingRun) error {
	names, err := namesIn(ctx, tx)
	if err != nil {
		return err
	}
	rec := changesRecord{Runs: []WaitingRun{clone(w)}}
	sens := sensitiveFor(p.d)
	sealed, err := sealChanges(newRing(ctx, sens), &rec, names, nil)
	if err != nil {
		return err
	}
	if err := writeRequest(ctx, tx, rec.Runs[0]); err != nil {
		return err
	}
	return sealed.put(ctx, sens)
}

func (p *pgRuns) NewRequest(ctx context.Context, w WaitingRun) (WaitingRun, error) {
	var out WaitingRun
	err := p.d.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.LockDoc(ctx, tx, ChangesKey); err != nil {
			return err
		}
		if w.Request != "" {
			same, err := readRequestsWhere(ctx, tx, false, "request_client_key = $1 AND cluster_id = $2 AND "+sameAs("requested_by", "requested_by_user", 3), w.Request, w.Cluster, w.By.Name, w.By.User)
			if err != nil {
				return err
			}
			if len(same) > 0 {
				out = same[0]
				return nil
			}
		}
		rest, err := requestsCounter(ctx, tx)
		if err != nil {
			return err
		}
		var top int
		if err := tx.QueryRow(ctx, "SELECT coalesce(max(request_number), 0) FROM script_runs").Scan(&top); err != nil {
			return err
		}
		rest.NextRun = max(rest.NextRun, top) + 1
		w.ID = rest.NextRun
		b, _ := json.Marshal(rest)
		if _, err := db.SaveScope(ctx, tx, changesScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
		if err := p.writeSealedRequest(ctx, tx, w); err != nil {
			return err
		}
		// The newest keptWaitingRuns decided requests are kept.
		rows, err := tx.Query(ctx, `SELECT request_number FROM script_runs WHERE request_number IS NOT NULL AND coalesce(request_state, '') <> $1
			ORDER BY request_number DESC OFFSET $2`, ChangeWaiting, keptWaitingRuns)
		if err != nil {
			return err
		}
		old, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return err
		}
		if len(old) > 0 {
			if err := dropRequests(ctx, tx, "request_number = ANY($1)", old); err != nil {
				return err
			}
		}
		list, err := readRequestsWhere(ctx, tx, false, "request_number = $1", w.ID)
		if err != nil {
			return err
		}
		if len(list) > 0 {
			out = list[0]
		}
		return p.d.Notify(ctx, tx, "doc/"+ChangesKey)
	})
	return out, err
}

func (p *pgRuns) Requests(ctx context.Context, cluster string) ([]WaitingRun, error) {
	var list []WaitingRun
	err := p.d.Snapshot(ctx, func(q db.Querier) (err error) {
		if cluster == "" {
			list, err = readRequestsWhere(ctx, q, false, "")
		} else {
			list, err = readRequestsWhere(ctx, q, false, "cluster_id = $1", cluster)
		}
		return err
	})
	out := make([]WaitingRun, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out, err
}

func (p *pgRuns) DecideRequest(ctx context.Context, cluster string, id int, decide func(*WaitingRun) error) (was, is WaitingRun, err error) {
	err = p.d.Tx(ctx, func(tx pgx.Tx) error {
		if err := db.LockDoc(ctx, tx, ChangesKey); err != nil {
			return err
		}
		list, err := readRequestsWhere(ctx, tx, false, "request_number = $1 AND cluster_id = $2", id, cluster)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrNotFound
		}
		was, is = list[0], clone(list[0])
		if err := decide(&is); err != nil {
			return err
		}
		if err := p.writeSealedRequest(ctx, tx, is); err != nil {
			return err
		}
		return p.d.Notify(ctx, tx, "doc/"+ChangesKey)
	})
	return was, is, err
}

func (p *pgRuns) ExpireRequests(ctx context.Context, after time.Duration, said string, now time.Time) (was, is []WaitingRun, err error) {
	err = p.d.Tx(ctx, func(tx pgx.Tx) error {
		was, is = nil, nil
		if err := db.LockDoc(ctx, tx, ChangesKey); err != nil {
			return err
		}
		list, err := readRequestsWhere(ctx, tx, false, "request_state = $1 AND requested_at <= $2", ChangeWaiting, now.Add(-after))
		if err != nil || len(list) == 0 {
			return err
		}
		for _, w := range list {
			next := clone(w)
			at := next.At.Add(after)
			next.State, next.DecidedAt, next.Comment = ChangeExpired, &at, said
			if err := p.writeSealedRequest(ctx, tx, next); err != nil {
				return err
			}
			was, is = append(was, w), append(is, next)
		}
		return p.d.Notify(ctx, tx, "doc/"+ChangesKey)
	})
	return was, is, err
}
