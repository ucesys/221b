package main

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/watson"
)

// DB-74: the Watson events recorded before the parameters were hidden are
// written again with them hidden, once, keeping their time; an event of
// another kind, and one with nothing to hide, are left as they are; the
// second start changes nothing, and a copy from the ConfigMap after a
// rollback is hidden again.
func TestTheEventsRecordedBeforeAreMasked(t *testing.T) {
	ctx := t.Context()
	d := dbtest.Open(t)
	st := audit.NewPGStore(d, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = st.Close(ctx) })
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	add := func(action, detail string) {
		st.Add(audit.Event{Time: at, Action: action, Detail: detail, Outcome: audit.OutcomeOK})
		if err := st.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	add("watson.command", `command="helm upgrade shop -n web --app --set 'token=MARK74A'" kind=change from="Helm1"`)
	add("watson.approve", `command="helm upgrade shop -n web --app --set 'token=MARK74A'" kind=change`)
	add("watson.command", `command="kubectl get pods -n web --selector app=x" kind=read`)
	add("automation.run", `script --set token=MARK74B`)
	details := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		rows, err := d.Pool().Query(ctx, "SELECT action, detail, at FROM audit_events")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var action, detail string
			var when time.Time
			if err := rows.Scan(&action, &detail, &when); err != nil {
				t.Fatal(err)
			}
			if !when.Equal(at) {
				t.Fatalf("the event %s moved to %s", action, when)
			}
			out[action+" "+detail]++
		}
		return out
	}
	var logged lockedBuffer
	w := &wiring{database: d}
	log := slog.New(slog.NewTextHandler(&logged, nil))
	for range 2 {
		w.hideCommandParameters(ctx, log, false)
	}
	got := details()
	want := map[string]int{
		`watson.command command="helm upgrade shop -n web --app --set 'token=***'" kind=change from="Helm1"`: 1,
		`watson.approve command="helm upgrade shop -n web --app --set 'token=***'" kind=change`:              1,
		`watson.command command="kubectl get pods -n web --selector app=x" kind=read`:                        1,
		`automation.run script --set token=MARK74B`:                                                          1,
	}
	if len(got) != len(want) {
		t.Fatalf("the events: %v", got)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("the events: %v", got)
		}
	}
	if done, err := d.Imported(ctx, watson.MaskedImportID); err != nil || !done {
		t.Fatalf("the hiding is not recorded: %v %v", done, err)
	}
	if !strings.Contains(logged.String(), "events=2") || strings.Count(logged.String(), "hid the values") != 1 {
		t.Fatalf("the log: %s", logged.String())
	}
	// An event a console from before wrote, copied after a rollback.
	add("watson.command", `command="helm upgrade shop -n web --app --set 'token=MARK74C'" kind=change`)
	w.hideCommandParameters(ctx, log, true)
	for k := range details() {
		if strings.Contains(k, "MARK74C") || strings.Contains(k, "MARK74A") {
			t.Fatalf("a value is left: %s", k)
		}
	}
}
