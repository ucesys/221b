package main

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// Automation's ConfigMap of before is copied with its variables first
// (DB-71; the review of server/internal/automation, finding 4): the
// changes and the run requests are hidden by the names the variables mark
// sensitive, so a sensitive value of theirs is never written to a row in
// clear, not even for the time between two copies.
func TestAutomationsVariablesAreCopiedBeforeWhatTheyHide(t *testing.T) {
	ctx := t.Context()
	docs := func(d *db.DB) map[string]movedDoc {
		return map[string]movedDoc{automation.VariablesKey: automation.NewVariablesPG(d), automation.ChangesKey: automation.NewChangesPG(d),
			automation.RunsKey: automation.NewRunsPG(d), automation.ScriptsKey: automation.NewScriptsPG(d)}
	}
	order := copyOrder(docs(nil))
	if slices.Index(order, automation.ChangesKey) < slices.Index(order, automation.VariablesKey) || slices.Index(order, automation.RunsKey) < slices.Index(order, automation.VariablesKey) {
		t.Errorf("copied in the order %v", order)
	}
	const (
		old  = "MARKERcopy4Old7tok"
		next = "MARKERcopy4New7tok"
		own  = "MARKERcopy4Own7tok"
	)
	keys := map[string]string{
		automation.VariablesKey: `{"clusters":{"local":{"_all":{"values":{"TOKEN":"` + old + `"},"sensitive":["TOKEN"]}}}}`,
		automation.ChangesKey: `{"next":1,"changes":[{"id":1,"cluster":"local","fleet":"_all","by":{"name":"Ann"},"at":"2026-11-01T09:00:00Z","changed":["TOKEN"],` +
			`"before":{"values":{"TOKEN":"` + old + `"},"sensitive":["TOKEN"]},"after":{"values":{"TOKEN":"` + next + `"},"sensitive":["TOKEN"]},"impact":[],"state":"waiting"}],` +
			`"nextRun":1,"runs":[{"id":1,"cluster":"local","script":"s","version":1,"fleet":"_all","targets":["n1"],"params":{"TOKEN":"` + own + `"},` +
			`"rollout":{"mode":"all"},"hooks":{},"by":{"name":"Ann"},"person":{"user":""},"at":"2026-11-01T09:00:00Z","changed":["TOKEN"],"impact":[],"state":"waiting"}]}`,
	}
	clear := func(d *db.DB, when string) {
		t.Helper()
		for _, mark := range []string{old, next, own} {
			if held := dbtest.Holding(t, d, mark); len(held) > 0 {
				t.Errorf("%s, a sensitive value is in clear in %v", when, held)
			}
		}
	}
	// The variables' copy fails: what they hide waits in the ConfigMap.
	d := dbtest.Open(t)
	automation.UseSensitive(d, automation.NewMemorySensitive())
	(&wiring{log: slog.Default(), database: d}).inTables(ctx, newOldConfigMap("r-automation", keys), func(d *db.DB) map[string]movedDoc {
		m := docs(d)
		m[automation.VariablesKey] = uncopied{m[automation.VariablesKey]}
		return m
	})
	if copied, _ := d.Imported(ctx, automation.ChangesKey); copied {
		t.Error("the changes were copied without the variables")
	}
	clear(d, "with the variables not copied")
	// Both copied.
	d2 := dbtest.Open(t)
	automation.UseSensitive(d2, automation.NewMemorySensitive())
	(&wiring{log: slog.Default(), database: d2}).inTables(ctx, newOldConfigMap("r-automation", keys), docs)
	if copied, _ := d2.Imported(ctx, automation.ChangesKey); !copied {
		t.Fatal("the changes were not copied")
	}
	clear(d2, "once copied")
}
