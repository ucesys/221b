package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/leader"
)

// A single replica, with no Lease, always leads: nothing changes for it.
func TestASingleReplicaAlwaysReads(t *testing.T) {
	e := leader.New(leader.Options{})
	if !e.Single() || !e.IsLeader() {
		t.Fatal("a replica without a Lease client stands alone and leads")
	}
	act := leader.NewActivity(false)
	if e.Single() {
		act.Set(true)
	}
	if !act.Active() || act.Standby() {
		t.Fatal("a single replica reads from the start")
	}
}

// Losing the Lease makes a warm standby stop its pollers and nothing
// more; a cold standby, whose watches cannot start again, exits instead
// (ADR-70).
func TestLosingTheLeaseStopsAWarmStandbyAndEndsAColdOne(t *testing.T) {
	for _, warm := range []bool{true, false} {
		act := leader.NewActivity(warm)
		var states []bool
		act.OnChange(func(active bool) { states = append(states, active) })
		exits := 0
		onLead, onStop := roleSwitch(act, func() { exits++ })
		onLead()
		if !act.Active() {
			t.Fatalf("warm=%v: winning the Lease did not make the replica active", warm)
		}
		onStop()
		if act.Active() || len(states) != 2 || states[0] != true || states[1] != false {
			t.Fatalf("warm=%v: the readers saw %v", warm, states)
		}
		if warm && exits != 0 {
			t.Fatal("a warm standby exited on losing the Lease")
		}
		if !warm && exits != 1 {
			t.Fatalf("a cold standby exited %d times on losing the Lease, want once", exits)
		}
	}
}

// A stopping console is out before the kubelet kills it: the wait for
// answers and commands, their stop and closing the audit log all fit in
// the pod's termination grace period, and a short one drops the wait.
func TestStoppingFitsTheTerminationGracePeriod(t *testing.T) {
	for _, grace := range []time.Duration{20 * time.Second, 30 * time.Second, time.Hour} {
		if got := workWithin(grace) + settleWithin + closeWithin; got >= grace {
			t.Errorf("%s of grace: stopping takes %s", grace, got)
		}
	}
	if got := workWithin(20 * time.Second); got != 14*time.Second {
		t.Errorf("the chart's 20 seconds wait %s", got)
	}
	if got := workWithin(3 * time.Second); got != 0 {
		t.Errorf("3 seconds wait %s", got)
	}
}

// persistent is a storage that outlasts a restart, as the ConfigMap does.
type persistent struct{ kv.Memory }

// The Audit log page says events are in memory exactly when they are.
func TestTheAuditStoreIsOnlyUsedOverStorageThatPersists(t *testing.T) {
	mem := audit.New(audit.Options{})
	if st := (&wiring{auditLog: mem}).keepAudit(t.Context(), &kv.Memory{}, slog.Default()); st != nil || mem.Stored() {
		t.Fatalf("over memory: store %v, stored %v", st, mem.Stored())
	}
	mem.Record(audit.Event{Action: "signin"})
	if got, _ := mem.Events(context.Background()); len(got) != 1 {
		t.Fatalf("the page lists %+v", got)
	}
	kept := audit.New(audit.Options{})
	st := (&wiring{auditLog: kept}).keepAudit(t.Context(), &persistent{}, slog.Default())
	if st == nil || !kept.Stored() {
		t.Fatalf("over a ConfigMap: store %v, stored %v", st, kept.Stored())
	}
	if err := st.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// With a database the audit log is kept in it (Phase 13), and what the
// ConfigMap held from the release before is copied over once.
func TestTheAuditLogMovesToTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	cm := &persistent{}
	old := audit.NewKVStore(cm, slog.Default())
	old.Add(audit.Event{Time: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), Action: "signin", Outcome: "ok"})
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	l := audit.New(audit.Options{})
	w := &wiring{auditLog: l, database: d}
	st := w.keepAudit(ctx, cm, slog.Default())
	if _, ok := st.(*audit.PGStore); !ok || !l.Stored() || !slices.Contains(w.inDatabase, "activity") {
		t.Fatalf("store %T, stored %v, domains %v", st, l.Stored(), w.inDatabase)
	}
	l.Record(audit.Event{Action: "settings.update", Outcome: "ok"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := l.Events(ctx)
		if err == nil && len(got) == 2 && got[0].Action == "settings.update" && got[1].Action == "signin" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page lists %+v: %v", got, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// What was copied leaves the ConfigMap (DB-55).
	for {
		if _, _, err := cm.LoadKey(ctx, audit.StoreKey); errors.Is(err, kv.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the audit log is still in the ConfigMap after its copy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// With a database a service's singleton settings are scopes of its
// settings table: copied from the ConfigMap once, read and written there
// from then on, while the service's other keys stay where they were.
func TestSingletonSettingsMoveToTheDatabase(t *testing.T) {
	ctx := t.Context()
	cm := &persistent{}
	if _, err := cm.SaveKey(ctx, "setup.json", []byte(`{"dismissed":["backup"]}`), ""); err != nil {
		t.Fatal(err)
	}
	none := &wiring{log: slog.Default()}
	if got := none.inScopes(ctx, cm, map[string]string{"setup.json": "setup"}); got != kv.Backend(cm) {
		t.Fatalf("without a database the backend became %T", got)
	}
	w := &wiring{log: slog.Default(), database: dbtest.Open(t)}
	b := w.inScopes(ctx, cm, map[string]string{"setup.json": "setup"})
	got, version, err := b.LoadKey(ctx, "setup.json")
	if err != nil || string(got) != `{"dismissed": ["backup"]}` {
		t.Fatalf("the copy reads %s: %v", got, err)
	}
	if _, err := b.SaveKey(ctx, "setup.json", []byte(`{"dismissed":["backup","sso"]}`), version); err != nil {
		t.Fatal(err)
	}
	// Once copied, the settings are gone from the ConfigMap (DB-55).
	if old, _, err := cm.LoadKey(ctx, "setup.json"); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("the ConfigMap still holds %s: %v", old, err)
	}
	if _, err := b.SaveKey(ctx, "other.json", []byte(`1`), ""); err != nil {
		t.Fatal(err)
	}
	if v, _, err := cm.LoadKey(ctx, "other.json"); err != nil || string(v) != "1" {
		t.Fatalf("another key did not stay in the ConfigMap: %s %v", v, err)
	}
	// A second start copies nothing over what was saved since.
	again := w.inScopes(ctx, cm, map[string]string{"setup.json": "setup"})
	if got, _, _ := again.LoadKey(ctx, "setup.json"); !strings.Contains(string(got), "sso") {
		t.Fatalf("a second start reads %s", got)
	}
}

// The database is opened before anything that keeps records is made
// (DB-59): the sign-in's stores, the local cluster, the election, the
// connections, the settings and the credentials are wired after it, so
// each can be given the database.
func TestTheDatabaseIsOpenedBeforeTheStoresAreMade(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(src), "\nfunc serve(")
	if !ok {
		t.Fatal("main.go has no run")
	}
	opened := strings.Index(body, "w.wireDatabase(ctx)")
	if opened < 0 {
		t.Fatal("run does not open the database")
	}
	for _, later := range []string{"w.wireAuth(ctx)", "w.wireLocalCluster()", "w.startAuth(ctx)", "w.wireLeader(ctx)", "w.wireClusters(ctx)", "w.wireSettings(ctx)", "w.wireCredentials(", "w.manager.Start(ctx)"} {
		if i := strings.Index(body, later); i < 0 || i < opened {
			t.Errorf("%s is not after the database is opened (at %d, the database at %d)", later, i, opened)
		}
	}
}

// No two record sets are copied into the database under one name: the
// jobs' and Automation's runs are both runs.json, each in its ConfigMap,
// and have names of their own; a name claimed twice stops the start.
func TestNoTwoRecordSetsAreCopiedUnderOneName(t *testing.T) {
	if a, j := automation.NewRunsPG(nil).ImportID(), jobs.NewRunsPG(nil).ImportID(); a == j || a == automation.RunsKey || j == jobs.RunsKey {
		t.Fatalf("automation's runs are copied as %q, the jobs' as %q", a, j)
	}
	w := &wiring{}
	w.claimImport("jobs/runs.json")
	w.claimImport("automation/runs.json")
	if len(w.importClash) != 0 {
		t.Fatalf("two names clash: %v", w.importClash)
	}
	w.claimImport("jobs/runs.json")
	if len(w.importClash) != 1 || w.importClash[0] != "jobs/runs.json" {
		t.Fatalf("a name claimed twice: %v", w.importClash)
	}
}
