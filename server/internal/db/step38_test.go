package db_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// refusing is a store of sensitive values whose writes fail while away is
// set, as Secrets the API server refuses.
type refusing struct {
	*automation.MemorySensitive
	mu   sync.Mutex
	away bool
}

func (r *refusing) down() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.away
}

func (r *refusing) Put(ctx context.Context, cluster string, values map[string]string) error {
	if r.down() {
		return errors.New("the API server refused the write")
	}
	return r.MemorySensitive.Put(ctx, cluster, values)
}

// copyRows copies every row of from into the tables to has, by the
// columns to has: to is a database at an earlier step, and the rows are
// what a console of that step would have written.
func copyRows(t *testing.T, from, to *db.DB) {
	t.Helper()
	ctx := ctxT(t)
	rows, err := to.Pool().Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	err = to.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
			return err
		}
		for _, table := range tables {
			id := pgx.Identifier{table}.Sanitize()
			var all []byte
			if err := from.Pool().QueryRow(ctx, `SELECT coalesce(json_agg(r), '[]') FROM `+id+` r`).Scan(&all); err != nil {
				return err
			}
			// Only the columns both schemas have: a later step drops or
			// renames some, and the older schema's own take their defaults.
			var cols []string
			if err := from.Pool().QueryRow(ctx, `SELECT coalesce(array_agg(quote_ident(column_name::text) ORDER BY ordinal_position), '{}') FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1`, table).Scan(&cols); err != nil {
				return err
			}
			var both []string
			if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(quote_ident(column_name::text) ORDER BY ordinal_position), '{}') FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND quote_ident(column_name::text) = ANY($2)`, table, cols).Scan(&both); err != nil {
				return err
			}
			if len(both) == 0 {
				continue
			}
			list := strings.Join(both, ", ")
			if _, err := tx.Exec(ctx, `INSERT INTO `+id+` (`+list+`) SELECT `+list+` FROM json_populate_recordset(NULL::`+id+`, $1::json)`, string(all)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Step 0038 and the start that follows it (DB-71): a database of step
// 0037 holds a sensitive variable's values in variables, in past changes
// (applied and waiting), in past runs (their own, each fleet's and each
// node's values, and a node's output), in a run request and in the audit
// log. The step moves nothing. A start whose Secret cannot be written
// leaves every value where it was and names the variables, never a
// value; the next start moves them all into the Secret, and no table
// holds one; a start after that moves nothing. A cluster where the name
// is not sensitive keeps its value in clear.
func TestStep0038AndTheStartMoveTheSensitiveValuesOut(t *testing.T) {
	const marker = "MARKERs38Hx4q"
	ctx := ctxT(t)
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	// What a console of step 0037 wrote: TOKEN is not sensitive yet when
	// the rows are written, and the mark is put on its row afterwards, as
	// that console kept it beside the value.
	a := dbtest.Open(t)
	st := automation.NewStore(&kv.Keyed{Keys: map[string]kv.Backend{automation.VariablesKey: automation.NewVariablesPG(a), automation.ChangesKey: automation.NewChangesPG(a),
		automation.RunsKey: automation.NewRunsPG(a), automation.ScriptsKey: automation.NewScriptsPG(a)}, Rest: &kv.Memory{}}, nil)
	st.SensitiveInTables(a, automation.NewMemorySensitive(), quiet)
	st.KeepRunsIn(a)
	ann := automation.Who{Name: "Ann", User: "ann"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	gpu := []string{"n1"}
	must(st.SetVariablesFor(ctx, "c1", "gpu", automation.FleetVariables{Values: map[string]string{"TOKEN": marker + "-fleet", "PORT": "5432"},
		Nodes: map[string]map[string]string{"n1": {"TOKEN": marker + "-node"}}}, gpu))
	must(st.SetVariables(ctx, "c2", automation.AllNodes, automation.FleetVariables{Values: map[string]string{"TOKEN": "c2-keeps-PLAINc2"}}))
	vars, err := st.Variables(ctx, "c1")
	must(err)
	now := automation.View(vars, "gpu", gpu)
	old := automation.View(vars, "gpu", gpu)
	old.Values = map[string]string{"TOKEN": marker + "-old", "PORT": "5432"}
	_, err = st.NewChange(ctx, automation.VariableChange{Cluster: "c1", Fleet: "gpu", By: ann, Changed: []string{"TOKEN"}, Before: old, After: now, State: automation.ChangeApplied})
	must(err)
	next := automation.View(vars, "gpu", gpu)
	next.Values = map[string]string{"TOKEN": marker + "-pending", "PORT": "5432"}
	_, err = st.NewChange(ctx, automation.VariableChange{Cluster: "c1", Fleet: "gpu", By: ann, Changed: []string{"TOKEN"}, Before: now, After: next, State: automation.ChangeWaiting})
	must(err)
	_, err = st.Save(ctx, "uses", "", "echo hi\n", ann, true)
	must(err)
	at := time.Now().UTC().Truncate(time.Second)
	_, err = st.NewRun(ctx, automation.Run{Script: "uses", Version: 1, Cluster: "c1", Fleet: "gpu", By: ann, State: automation.RunSucceeded, Started: at, Beat: at, Ended: &at,
		Overrides: map[string]string{"TOKEN": marker + "-own"},
		Fleets:    []automation.RunFleet{{ID: "gpu", Values: map[string]string{"TOKEN": marker + "-fleet", "PORT": "5432"}}},
		Nodes:     []automation.NodeResult{{Node: "n1", State: automation.NodeChanged, Values: map[string]string{"TOKEN": marker + "-fleet"}, Tail: "logged in with " + marker + "-fleet\n"}}})
	must(err)
	_, err = st.NewWaitingRun(ctx, automation.WaitingRun{Cluster: "c1", Script: "uses", Version: 1, Fleet: "gpu", Nodes: gpu, Targets: gpu,
		Params: map[string]string{"TOKEN": marker + "-request"}, By: ann, Changed: []string{"TOKEN"}})
	must(err)
	if _, err := a.Pool().Exec(ctx, `INSERT INTO audit_events (audit_event_id, action, cluster_id, target, outcome, detail) VALUES ($1, 'automation.command', 'c1', 'uses', 'ok', $2)`,
		db.NewID(), "kubectl create secret generic x --from-literal=t="+marker+"-fleet"); err != nil {
		t.Fatal(err)
	}

	b := dbtest.OpenConfig(t, dbtest.Config(t))
	if n, err := b.MigrateTo(ctx, 37); err != nil || n != 37 {
		t.Fatalf("the steps before: applied %d: %v", n, err)
	}
	copyRows(t, a, b)
	if _, err := b.Pool().Exec(ctx, `UPDATE variables SET is_sensitive = true WHERE name = 'TOKEN' AND fleet_id IN (SELECT fleet_id FROM fleets WHERE cluster_id = 'c1' AND fleet_key = 'gpu')`); err != nil {
		t.Fatal(err)
	}
	before := dbtest.Holding(t, b, marker)
	for _, table := range []string{"variables", "variable_change_items", "script_run_values", "script_run_hosts", "audit_events"} {
		if before[table] == 0 {
			t.Fatalf("the database of step 0037 has no value in %s: %v", table, before)
		}
	}

	// The step shapes the tables and moves nothing.
	if _, err := b.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if after := dbtest.Holding(t, b, marker); len(after) != len(before) {
		t.Fatalf("the step itself moved values: %v, was %v", after, before)
	}

	// A start whose Secret cannot be written: everything stays, and the
	// log names the variables, never a value.
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	sens := &refusing{MemorySensitive: automation.NewMemorySensitive(), away: true}
	moved, left, err := automation.MoveSensitive(ctx, b, sens, log)
	if err == nil || left != 1 || moved != 0 {
		t.Fatalf("a start whose Secret refused the values: moved %d, left %d: %v", moved, left, err)
	}
	if strings.Contains(logs.String(), marker) || strings.Contains(err.Error(), marker) {
		t.Fatalf("the log or the error holds a value:\n%s\n%v", logs.String(), err)
	}
	if !strings.Contains(logs.String(), "TOKEN on fleet gpu") || !strings.Contains(logs.String(), "TOKEN on node n1") || !strings.Contains(logs.String(), "cluster=c1") {
		t.Fatalf("the log does not name the variables left:\n%s", logs.String())
	}
	if after := dbtest.Holding(t, b, marker); len(after) != len(before) {
		t.Fatalf("a failed move changed the rows: %v, was %v", after, before)
	}
	// Meanwhile nothing reads a value that is still in a row.
	read := automation.NewStore(&kv.Keyed{Keys: map[string]kv.Backend{automation.VariablesKey: automation.NewVariablesPG(b), automation.ChangesKey: automation.NewChangesPG(b),
		automation.RunsKey: automation.NewRunsPG(b), automation.ScriptsKey: automation.NewScriptsPG(b)}, Rest: &kv.Memory{}}, nil)
	read.SensitiveInTables(b, sens, quiet)
	read.KeepRunsIn(b)
	if v, _ := read.Variables(ctx, "c1"); v["gpu"].Values["TOKEN"] != automation.Unmoved || v["gpu"].Values["PORT"] != "5432" {
		t.Fatalf("before the move the variables read %+v", v["gpu"])
	}

	// The next start moves them.
	sens.mu.Lock()
	sens.away = false
	sens.mu.Unlock()
	logs.Reset()
	moved, left, err = automation.MoveSensitive(ctx, b, sens, log)
	if err != nil || left != 0 || moved == 0 {
		t.Fatalf("the move: moved %d, left %d: %v", moved, left, err)
	}
	if after := dbtest.Holding(t, b, marker); len(after) > 0 {
		t.Fatalf("after the move the database holds a value in %v", after)
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatalf("the log holds a value:\n%s", logs.String())
	}
	if h := dbtest.Holding(t, b, "c2-keeps-PLAINc2"); h["variables"] != 1 {
		t.Fatalf("a cluster where the name is not sensitive lost its value in clear: %v", h)
	}
	v, err := read.Variables(ctx, "c1")
	must(err)
	tok := v["gpu"].Values["TOKEN"]
	if !automation.IsHidden(tok) || tok == automation.Unmoved {
		t.Fatalf("after the move TOKEN reads %q", tok)
	}
	got, err := read.Reveal(ctx, "c1", []string{tok, v[automation.AllNodes].Nodes["n1"]["TOKEN"]})
	must(err)
	if got[tok] != marker+"-fleet" || got[v[automation.AllNodes].Nodes["n1"]["TOKEN"]] != marker+"-node" {
		t.Fatalf("the Secret keeps %v", got)
	}
	// What a waiting change would set, and a waiting run would use, wait in
	// the Secret; a decided change's and a run's values are digests alone.
	changes, err := read.Changes(ctx, "c1")
	must(err)
	var pending string
	for _, c := range changes {
		if c.State == automation.ChangeWaiting {
			pending = c.After.Values["TOKEN"]
		}
		if strings.Contains(c.Before.Values["TOKEN"]+c.After.Values["TOKEN"], marker) {
			t.Fatalf("a change reads %+v", c)
		}
	}
	waiting, err := read.WaitingRuns(ctx, "c1")
	must(err)
	if len(waiting) != 1 {
		t.Fatalf("waiting runs: %+v", waiting)
	}
	if got, _ := read.Reveal(ctx, "c1", []string{pending, waiting[0].Params["TOKEN"]}); got[pending] != marker+"-pending" || got[waiting[0].Params["TOKEN"]] != marker+"-request" {
		t.Fatalf("what waits is not in the Secret: %v", got)
	}
	runs, err := read.Runs(ctx)
	must(err)
	if len(runs) != 1 || !automation.IsHidden(runs[0].Overrides["TOKEN"]) || runs[0].Nodes[0].Values["TOKEN"] != tok || !strings.Contains(runs[0].Nodes[0].Tail, "logged in with "+automation.MaskText) {
		t.Fatalf("the run reads %+v", runs)
	}
	var events int
	if err := b.Pool().QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action = 'automation.command' AND detail LIKE '%--from-literal=t=' || $1 || '%'", automation.MaskText).Scan(&events); err != nil || events != 1 {
		t.Fatalf("the audit event: %d: %v", events, err)
	}

	// A start after that moves nothing.
	if moved, left, err := automation.MoveSensitive(ctx, b, sens, log); err != nil || moved != 0 || left != 0 {
		t.Fatalf("a second start: moved %d, left %d: %v", moved, left, err)
	}
	if noValue, noRow, err := automation.Unmatched(ctx, b, sens); err != nil || len(noValue) != 0 {
		t.Fatalf("unmatched after the move: %v %v: %v", noValue, noRow, err)
	}
}
