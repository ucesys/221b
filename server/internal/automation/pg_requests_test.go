package automation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// The runs that wait for approval read back from the database as from a
// ConfigMap, and they are rows of the runs' own table: one that was
// approved and the run it started are one row, and the rejected and the
// expired ones are rows with no run.
func TestRunRequestsAreRowsOfTheRuns(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	tick := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { tick = tick.Add(time.Second); return tick }
	open := func() *Store {
		return NewStore(&kv.Keyed{Keys: map[string]kv.Backend{RunsKey: NewRunsPG(d), ChangesKey: NewChangesPG(d)}, Rest: &kv.Memory{}}, now)
	}
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	ask := func(st *Store, request string) WaitingRun {
		t.Helper()
		w, err := st.NewWaitingRun(ctx, WaitingRun{Cluster: "c1", ClusterName: "Here", Script: "restart", Version: 3, Fleet: "gpu", FleetName: "GPU",
			Nodes: []string{"n1"}, Targets: []string{"n1", "n2"}, Params: map[string]string{"MODE": "fast", "EMPTY": ""},
			Rollout: Rollout{Mode: RolloutSize, Size: 1, StopAfter: 1, TimeoutMinutes: 10}, Config: "elevated", Hooks: HookRefs{Before: []string{"pre"}, After: []string{"post"}},
			RetryOf: 4, Request: request, By: ann, Person: Person{User: "ann@corp", Groups: []string{"ops", "admins"}}, Method: "ldap",
			Changed: []string{"MODE"}, Impact: []ImpactItem{{Script: "restart", Version: 3, Decision: ImpactApproval, Why: "a condition reads MODE", By: "rule"}}, Note: "Sherlock was off"})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	steps := func(st *Store) {
		t.Helper()
		first := ask(st, "req-a")
		if again := ask(st, "req-a"); again.ID != first.ID {
			t.Fatalf("the same request waits twice: %d and %d", first.ID, again.ID)
		}
		rejected := ask(st, "req-b")
		if _, err := st.DecideWaitingRun(ctx, "c1", rejected.ID, false, "not today", ann, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DecideWaitingRun(ctx, "c1", first.ID, true, "", ann, 0); !errors.Is(err, ErrOwnWork) {
			t.Fatalf("approved by who asked: %v", err)
		}
		// Approved: the run starts under the request's name, then the
		// request is marked with the run's number.
		at := now()
		run, err := st.NewRun(ctx, Run{Script: first.Script, Version: first.Version, Hash: "abc", Cluster: first.Cluster, ClusterName: first.ClusterName,
			Fleet: first.Fleet, FleetName: first.FleetName, Overrides: first.Params, Rollout: first.Rollout, By: ann, State: RunRunning, Config: first.Config,
			HookRefs: first.Hooks, RetryOf: first.RetryOf, Request: first.RequestKey(), ValuesApproved: &ValuesApproval{Request: first.ID, By: sam, At: at},
			Started: at, Beat: at, Nodes: []NodeResult{{Node: "n1", Batch: 1, State: NodeWaiting}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DecideWaitingRun(ctx, "c1", first.ID, true, "", sam, run.ID); err != nil {
			t.Fatal(err)
		}
		// One that nobody decides in time.
		ask(st, "req-c")
		tick = tick.Add(2 * time.Hour)
		if expired, err := st.ExpireWaitingRuns(ctx, time.Hour, "expired after 1 hour"); err != nil || len(expired) != 1 {
			t.Fatalf("expired %d: %v", len(expired), err)
		}
		ask(st, "req-d")
	}
	read := func(st *Store) string {
		t.Helper()
		waiting, err := st.WaitingRuns(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		runs, err := st.Runs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(map[string]any{"requests": waiting, "runs": runs}, "", " ")
		return string(b)
	}
	start := tick
	kept := NewStore(&kv.Memory{}, now)
	steps(kept)
	want := read(kept)
	tick = start
	steps(open())
	if got := read(open()); got != want {
		t.Fatalf("the database's requests and runs differ from the ConfigMap's.\nthe database:\n%s\nthe ConfigMap:\n%s", got, want)
	}
	for _, part := range []string{`"approved"`, `"rejected"`, `"expired"`, `"waiting"`, `"not today"`, `"expired after 1 hour"`, `"ann@corp"`, `"a condition reads MODE"`, `"valuesApproved"`, `"run": 1`} {
		if !contains(want, part) {
			t.Errorf("the scenario has no %s", part)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := d.Pool().QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Four requests, one of which is also the run: four rows.
	if n := count("SELECT count(*) FROM script_runs"); n != 4 {
		t.Errorf("%d rows for four requests and the run one of them started, want 4", n)
	}
	if n := count("SELECT count(*) FROM script_runs WHERE request_number = 1 AND run_number = 1 AND request_state = 'approved' AND started_run_number = 1 AND state = 'running' AND decided_by = 'Sam'"); n != 1 {
		t.Errorf("the approved request and its run as one row: %d", n)
	}
	// One list has every run: the states of the ones that never ran.
	if n := count("SELECT count(*) FROM script_runs WHERE run_number IS NULL AND state IN ('rejected', 'expired', 'waiting')"); n != 3 {
		t.Errorf("the requests that started no run: %d rows", n)
	}
	if n := count("SELECT count(*) FROM script_run_values WHERE scope = 'request'"); n != 8 {
		t.Errorf("the requests' values: %d rows, want 8", n)
	}
}

// A build of November 19, 2026 kept the requests whole in the settings
// scope: they are read from there, and the next save makes them rows.
func TestRunRequestsKeptInTheScopeBecomeRows(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	old, _ := json.Marshal(changesRest{Next: 3, NextRun: 1, Runs: []WaitingRun{{ID: 1, Cluster: "c1", Script: "restart", Version: 2, Fleet: "gpu",
		Targets: []string{"n1"}, Params: map[string]string{"MODE": "fast"}, By: Who{Name: "Ann", User: "ann"}, At: at, Changed: []string{"MODE"}, Impact: []ImpactItem{}, State: ChangeWaiting}}})
	if err := d.Tx(ctx, func(tx pgx.Tx) error {
		_, err := db.SaveScope(ctx, tx, changesScope, old, db.AnyVersion, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(&kv.Keyed{Keys: map[string]kv.Backend{ChangesKey: NewChangesPG(d)}, Rest: &kv.Memory{}}, func() time.Time { return at.Add(time.Minute) })
	list, err := st.WaitingRuns(ctx, "c1")
	if err != nil || len(list) != 1 || list[0].Params["MODE"] != "fast" {
		t.Fatalf("read from the scope: %+v: %v", list, err)
	}
	if _, err := st.DecideWaitingRun(ctx, "c1", 1, false, "no", Who{Name: "Sam", User: "sam"}, 0); err != nil {
		t.Fatal(err)
	}
	var rows int
	var scope string
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM script_runs WHERE request_number = 1 AND request_state = 'rejected'), (SELECT value::text FROM settings WHERE scope = $1)", changesScope).Scan(&rows, &scope); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || contains(scope, "runs") {
		t.Fatalf("%d rows; the scope still has %s", rows, scope)
	}
	if list, _ := st.WaitingRuns(ctx, "c1"); len(list) != 1 || list[0].State != ChangeRejected || list[0].Params["MODE"] != "fast" {
		t.Fatalf("read from the rows: %+v", list)
	}
}

// A waiting run names the asker as the cluster knows them with the
// API's names, "user" and "groups", not Go's; a record kept with Go's
// names reads the same (the eighth cluster test's N2).
func TestAWaitingRunsPersonHasJSONNames(t *testing.T) {
	b, err := json.Marshal(WaitingRun{ID: 3, Person: Person{User: "ann@corp", Groups: []string{"ops"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"person":{"user":"ann@corp","groups":["ops"]}`) {
		t.Fatalf("the JSON: %s", b)
	}
	var old WaitingRun
	if err := json.Unmarshal([]byte(`{"id":3,"person":{"User":"bob@corp","Groups":["admins","ops"]}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Person.User != "bob@corp" || len(old.Person.Groups) != 2 {
		t.Fatalf("the shape of before: %+v", old.Person)
	}
}
