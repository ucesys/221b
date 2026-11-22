package automation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// failingSensitive is a SensitiveStore whose writes fail while away is
// set, as a Secret the API server refuses.
type failingSensitive struct {
	*MemorySensitive
	mu   sync.Mutex
	away bool
}

var errAway = errors.New("the API server refused the write")

func (f *failingSensitive) set(away bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.away = away
}

func (f *failingSensitive) down() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.away
}

func (f *failingSensitive) Put(ctx context.Context, cluster string, values map[string]string) error {
	if f.down() {
		return errAway
	}
	return f.MemorySensitive.Put(ctx, cluster, values)
}

func (f *failingSensitive) Drop(ctx context.Context, cluster string, digests []string) error {
	if f.down() {
		return errAway
	}
	return f.MemorySensitive.Drop(ctx, cluster, digests)
}

// pgSensitiveStore is a store whose variables, changes and runs are rows
// of d, its sensitive values kept in sens.
func pgSensitiveStore(d *db.DB, sens SensitiveStore, log *slog.Logger) *Store {
	st := NewStore(&kv.Keyed{Keys: map[string]kv.Backend{VariablesKey: NewVariablesPG(d), ChangesKey: NewChangesPG(d), RunsKey: NewRunsPG(d), ScriptsKey: NewScriptsPG(d)}, Rest: &kv.Memory{}}, nil)
	st.SensitiveInTables(d, sens, log)
	st.KeepRunsIn(d)
	return st
}

func held(t *testing.T, s SensitiveStore, cluster string) map[string]bool {
	t.Helper()
	all, err := s.Held(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, d := range all[cluster] {
		out[d] = true
	}
	return out
}

func reveal(t *testing.T, st *Store, cluster, digest string) (string, bool) {
	t.Helper()
	got, err := st.Reveal(context.Background(), cluster, []string{digest})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := got[digest]
	return v, ok
}

// A sensitive value is kept in the Secret, never in a row (DB-71): set,
// changed with approval (the change's rows hold digests; the value it
// would set waits in the Secret until it is decided), rejected (the
// value leaves the Secret), marked (the values of the name already in
// the rows, its past changes and runs, move to the Secret) and unmarked
// (its value comes back to the row: it is no longer a secret).
func TestASensitiveValueIsKeptInTheSecretAndNeverInARow(t *testing.T) {
	const marker = "MARKERv8Qk3sens"
	d := dbtest.Open(t)
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sens := NewMemorySensitive()
	st := pgSensitiveStore(d, sens, log)
	ann, sam := Who{Name: "Ann", User: "ann"}, Who{Name: "Sam", User: "sam"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	nowhere := func(what, text string) {
		t.Helper()
		if h := dbtest.Holding(t, d, text); len(h) > 0 {
			t.Fatalf("%s: the database holds %q in %v", what, text, h)
		}
	}
	gpu := []string{"n1"}
	must(st.SetVariablesFor(ctx, "c1", "gpu", FleetVariables{Values: map[string]string{"TOKEN": marker + "-fleet", "PORT": "5432"}, Sensitive: []string{"TOKEN"},
		Nodes: map[string]map[string]string{"n1": {"TOKEN": marker + "-node"}}}, gpu))
	nowhere("set", marker)
	vars, err := st.Variables(ctx, "c1")
	must(err)
	fleetTok, nodeTok := vars["gpu"].Values["TOKEN"], vars[AllNodes].Nodes["n1"]["TOKEN"]
	if !IsHidden(fleetTok) || !IsHidden(nodeTok) || vars["gpu"].Values["PORT"] != "5432" {
		t.Fatalf("the variables read %+v", vars)
	}
	if v, ok := reveal(t, st, "c1", fleetTok); !ok || v != marker+"-fleet" {
		t.Fatalf("the Secret keeps %q, %v", v, ok)
	}
	if v, ok := reveal(t, st, "c1", nodeTok); !ok || v != marker+"-node" {
		t.Fatalf("the Secret keeps %q, %v for the node", v, ok)
	}
	var rows int
	must(d.Pool().QueryRow(ctx, "SELECT count(*) FROM variables WHERE name = 'TOKEN' AND value IS NULL AND value_digest LIKE 'hidden:%'").Scan(&rows))
	if rows != 2 {
		t.Fatalf("%d rows of TOKEN keep a digest and no value, want 2", rows)
	}
	// Saved back as the page has it, digests and all, nothing changes.
	view := View(vars, "gpu", gpu)
	must(st.SetVariablesFor(ctx, "c1", "gpu", view, gpu))
	if again, _ := st.Variables(ctx, "c1"); again["gpu"].Values["TOKEN"] != fleetTok {
		t.Fatalf("saved back, TOKEN reads %q", again["gpu"].Values["TOKEN"])
	}
	// A digest that stands for nothing the console keeps is refused, and
	// so is a digest under a name that is not sensitive.
	forged := view
	forged.Values = map[string]string{"TOKEN": "hidden:0123456789abcdef01234567", "PORT": "5432"}
	if err := st.SetVariablesFor(ctx, "c1", "gpu", forged, gpu); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a digest the console does not keep: %v", err)
	}
	moved := view
	moved.Values = map[string]string{"TOKEN": fleetTok, "PORT": fleetTok}
	if err := st.SetVariablesFor(ctx, "c1", "gpu", moved, gpu); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a digest under a name that is not sensitive: %v", err)
	}

	// A change that waits: its rows have digests; what it would set is in
	// the Secret until it is decided.
	after := View(vars, "gpu", gpu)
	after.Values = map[string]string{"TOKEN": marker + "-new", "PORT": "5432"}
	c, err := st.NewChange(ctx, VariableChange{Cluster: "c1", Fleet: "gpu", By: ann, Changed: []string{"TOKEN"}, Before: View(vars, "gpu", gpu), After: after, State: ChangeWaiting})
	must(err)
	nowhere("a waiting change", marker)
	pending := c.After.Values["TOKEN"]
	if !IsHidden(pending) || pending == fleetTok {
		t.Fatalf("the waiting change answers %+v", c.After)
	}
	if v, ok := reveal(t, st, "c1", pending); !ok || v != marker+"-new" {
		t.Fatal("the value a waiting change would set is not in the Secret")
	}
	listed, err := st.Changes(ctx, "c1")
	must(err)
	if len(listed) != 1 || listed[0].After.Values["TOKEN"] != pending || listed[0].Before.Values["TOKEN"] != fleetTok {
		t.Fatalf("the change reads %+v", listed)
	}
	if _, err := st.DecideChange(ctx, "c1", c.ID, true, "", sam); err != nil {
		t.Fatal(err)
	}
	vars, err = st.Variables(ctx, "c1")
	must(err)
	if vars["gpu"].Values["TOKEN"] != pending {
		t.Fatalf("approved, TOKEN reads %q, want the change's %q", vars["gpu"].Values["TOKEN"], pending)
	}
	if h := held(t, sens, "c1"); h[fleetTok] || !h[pending] || !h[nodeTok] {
		t.Fatalf("after the approval the Secret keeps %v: the old value goes, the new and the node's stay", h)
	}
	nowhere("approved", marker)

	// A change that is rejected: its value leaves the Secret.
	after.Values = map[string]string{"TOKEN": marker + "-rejected", "PORT": "5432"}
	r, err := st.NewChange(ctx, VariableChange{Cluster: "c1", Fleet: "gpu", By: ann, Changed: []string{"TOKEN"}, Before: View(vars, "gpu", gpu), After: after, State: ChangeWaiting})
	must(err)
	rejected := r.After.Values["TOKEN"]
	if !held(t, sens, "c1")[rejected] {
		t.Fatal("the waiting change's value is not in the Secret")
	}
	if _, err := st.DecideChange(ctx, "c1", r.ID, false, "not now", sam); err != nil {
		t.Fatal(err)
	}
	if held(t, sens, "c1")[rejected] {
		t.Fatal("a rejected change's value stays in the Secret")
	}
	nowhere("rejected", marker)

	// A name marked sensitive later: its value in the variables, in a past
	// change and in a past run move out of every row.
	vars, _ = st.Variables(ctx, "c1")
	plain := View(vars, "gpu", gpu)
	plain.Values["PLAIN"] = marker + "-plain"
	must(st.SetVariablesFor(ctx, "c1", "gpu", plain, gpu))
	_, err = st.NewChange(ctx, VariableChange{Cluster: "c1", Fleet: "gpu", By: ann, Changed: []string{"PLAIN"}, Before: View(vars, "gpu", gpu), After: plain, State: ChangeApplied})
	must(err)
	_, err = st.Save(ctx, "uses", "", "echo hi\n", ann, true)
	must(err)
	at := time.Now().UTC().Truncate(time.Second)
	used, err := st.NewRun(ctx, Run{Script: "uses", Version: 1, Cluster: "c1", Fleet: "gpu", By: ann, State: RunSucceeded, Started: at, Beat: at, Ended: &at,
		Overrides: map[string]string{"PLAIN": marker + "-plain"},
		Fleets:    []RunFleet{{ID: "gpu", Values: map[string]string{"PLAIN": marker + "-plain", "PORT": "5432"}}},
		Nodes:     []NodeResult{{Node: "n1", State: NodeChanged, Values: map[string]string{"PLAIN": marker + "-plain"}, Tail: "connected with " + marker + "-plain\n"}}})
	must(err)
	// Its whole output quotes the value too (DB-87).
	must(st.PutNode(ctx, used.ID, used.Nodes[0], "starting\nconnected with "+marker+"-plain\n"))
	if h := dbtest.Holding(t, d, marker+"-plain"); len(h) == 0 {
		t.Fatal("a value that is not sensitive is not in the rows")
	}
	vars, _ = st.Variables(ctx, "c1")
	mark := View(vars, "gpu", gpu)
	mark.Sensitive = append(mark.Sensitive, "PLAIN")
	must(st.SetVariablesFor(ctx, "c1", "gpu", mark, gpu))
	nowhere("marked", marker)
	if out, err := st.Outputs(ctx, used.ID); err != nil || out["n1"] != "starting\nconnected with "+maskText+"\n" {
		t.Fatalf("the past run's output once the name is marked: %q: %v", out["n1"], err)
	}
	vars, _ = st.Variables(ctx, "c1")
	plainTok := vars["gpu"].Values["PLAIN"]
	if v, ok := reveal(t, st, "c1", plainTok); !ok || v != marker+"-plain" {
		t.Fatal("the value of a name marked sensitive is not in the Secret")
	}
	runs, err := st.Runs(ctx)
	must(err)
	if len(runs) != 1 || runs[0].Overrides["PLAIN"] != plainTok || runs[0].Nodes[0].Values["PLAIN"] != plainTok || runs[0].Fleets[0].Values["PLAIN"] != plainTok ||
		runs[0].Fleets[0].Values["PORT"] != "5432" || strings.Contains(runs[0].Nodes[0].Tail, marker) || !strings.Contains(runs[0].Nodes[0].Tail, "connected with "+maskText) {
		t.Fatalf("the past run reads %+v", runs)
	}

	// The mark taken off (the page asks a second admin first): the value
	// is no longer a secret, and comes back to its row.
	unmark := View(vars, "gpu", gpu)
	unmark.Sensitive = []string{"TOKEN"}
	must(st.SetVariablesFor(ctx, "c1", "gpu", unmark, gpu))
	vars, _ = st.Variables(ctx, "c1")
	if vars["gpu"].Values["PLAIN"] != marker+"-plain" {
		t.Fatalf("unmarked, PLAIN reads %q", vars["gpu"].Values["PLAIN"])
	}
	if held(t, sens, "c1")[plainTok] {
		t.Fatal("an unmarked value stays in the Secret too")
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatalf("the log holds the value:\n%s", logs.String())
	}
}

// A Secret that cannot be written leaves the rows as they were, and a
// row whose value the Secret lost reads as a digest that stands for
// nothing: Unmatched names it, by cluster and name alone.
func TestASecretThatCannotBeWrittenLeavesTheRowsAsTheyWere(t *testing.T) {
	const marker = "MARKERz1Wq7fail"
	d := dbtest.Open(t)
	ctx := context.Background()
	sens := &failingSensitive{MemorySensitive: NewMemorySensitive()}
	st := pgSensitiveStore(d, sens, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := st.SetVariablesFor(ctx, "c1", "gpu", FleetVariables{Values: map[string]string{"TOKEN": marker + "-a", "PORT": "1"}, Sensitive: []string{"TOKEN"}}, []string{}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Variables(ctx, "c1")
	sens.set(true)
	err := st.SetVariablesFor(ctx, "c1", "gpu", FleetVariables{Values: map[string]string{"TOKEN": marker + "-b", "PORT": "2"}, Sensitive: []string{"TOKEN"}}, []string{})
	if err == nil {
		t.Fatal("a save whose Secret cannot be written went through")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("the error holds the value: %v", err)
	}
	sens.set(false)
	after, _ := st.Variables(ctx, "c1")
	if after["gpu"].Values["TOKEN"] != before["gpu"].Values["TOKEN"] || after["gpu"].Values["PORT"] != "1" {
		t.Fatalf("a failed save changed the rows: %+v, was %+v", after, before)
	}
	if h := dbtest.Holding(t, d, marker); len(h) > 0 {
		t.Fatalf("the database holds the value in %v", h)
	}
	// The Secret loses the value: the row stays, and is named.
	if err := sens.Drop(ctx, "c1", []string{after["gpu"].Values["TOKEN"]}); err != nil {
		t.Fatal(err)
	}
	noValue, _, err := Unmatched(ctx, d, sens)
	if err != nil || len(noValue) != 1 || !strings.Contains(noValue[0], "TOKEN on fleet gpu of cluster c1") || strings.Contains(noValue[0], marker) {
		t.Fatalf("unmatched: %v: %v", noValue, err)
	}
	if v, ok := reveal(t, st, "c1", after["gpu"].Values["TOKEN"]); ok {
		t.Fatalf("a lost value reads %q", v)
	}
}

// Without a database the store hides the values itself before it saves a
// document (a ConfigMap, or memory): the same steps leave the same
// variables, and no document holds a sensitive value.
func TestADocumentKeptElsewhereHidesTheValuesToo(t *testing.T) {
	const marker = "MARKERc4Pn2cm"
	ctx := context.Background()
	mem := &kv.Memory{}
	sens := NewMemorySensitive()
	st := NewStore(mem, nil)
	st.SetSensitive(sens, false, nil)
	if err := st.SetVariables(ctx, "c1", AllNodes, FleetVariables{Values: map[string]string{"TOKEN": marker, "ZONE": "eu"}, Sensitive: []string{"TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	vars, _ := st.Variables(ctx, "c1")
	tok := vars[AllNodes].Values["TOKEN"]
	if !IsHidden(tok) || vars[AllNodes].Values["ZONE"] != "eu" {
		t.Fatalf("%+v", vars)
	}
	_, err := st.NewChange(ctx, VariableChange{Cluster: "c1", Fleet: AllNodes, By: Who{Name: "Ann"}, Changed: []string{"TOKEN"}, Before: vars[AllNodes],
		After: FleetVariables{Values: map[string]string{"TOKEN": marker + "-next", "ZONE": "eu"}, Sensitive: []string{"TOKEN"}}, State: ChangeWaiting})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{VariablesKey, ChangesKey, RunsKey} {
		b, _, err := mem.LoadKey(ctx, key)
		if err != nil && !errors.Is(err, kv.ErrNotFound) {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(marker)) {
			t.Fatalf("%s holds the value: %s", key, b)
		}
	}
	if v, ok := reveal(t, st, "c1", tok); !ok || v != marker {
		t.Fatal("the Secret does not keep the value")
	}
}
