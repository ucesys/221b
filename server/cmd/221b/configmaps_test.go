package main

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/sim"
)

// The ConfigMaps of a release before 0.13 that hold record sets, by their
// suffix after the release's name (the chart's templates/configmap.yaml).
var recordSetConfigMaps = []string{"", "-audit", "-sessions", "-ai", "-knowledge", "-automation", "-automation-runs", "-issues", "-proposals", "-cases", "-runs", "-casework"}

// beforeConfigMaps fills every ConfigMap of before with a record set, each
// holding a text that is nowhere else, and returns the ConfigMaps' data and
// those texts: what a 0.12 release leaves for its upgrade.
func beforeConfigMaps(t *testing.T) (map[string]map[string]string, []string) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	// The audit log and the Toolbox's session records as their stores of
	// before wrote them.
	mem := &kv.Memory{}
	old := audit.NewKVStore(mem, slog.Default())
	old.Add(audit.Event{Time: at, Action: "signin", Outcome: "ok", Detail: "markAuditQ2"})
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	auditLog, _, err := mem.LoadKey(ctx, audit.StoreKey)
	if err != nil {
		t.Fatal(err)
	}
	hist := &sessions.KVHistory{Backend: mem, Key: sessions.HistoryKey, Keep: 100, MaxBytes: historyMaxBytes, Logger: slog.Default()}
	if _, err := hist.Merge(ctx, []sessions.Record{{ID: "s-1", Cluster: "local", Tool: "shell", Summary: "markSessionQ3", Owner: "ann", Created: at, Started: at, Ended: at.Add(time.Minute), EndedBy: "ann"}}); err != nil {
		t.Fatal(err)
	}
	history, _, err := mem.LoadKey(ctx, sessions.HistoryKey)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]map[string]string{
			"":                 {"setup.json": `{"dismissed":["markSetupQ1"]}`},
			"-audit":           {audit.StoreKey: string(auditLog)},
			"-sessions":        {sessions.HistoryKey: string(history)},
			"-ai":              {"ai-pilot.json": `{"next":2,"entries":[{"n":1,"agent":"sherlock","version":"pilot","who":"bob","question":"Why?","answer":"markAIQ4"}]}`},
			"-knowledge":       {"knowledge-settings.json": `{"sources":[{"id":"ops","name":"markKnowQ5","kind":"git","url":"https://git.example.com/ops.git"}],"store":{}}`},
			"-automation":      {"groups.json": `[{"name":"markGroupQ6","desc":"Monthly"}]`},
			"-automation-runs": {"facts.json": `{"local/node-1":{"cluster":"local","node":"node-1","at":"2026-11-01T09:00:00Z","facts":{"os":"markFactQ7"}}}`},
			"-issues":          {issues.KnownKey: `{"next":2,"known":[{"id":"KI-1","title":"markKnownQ8","status":"investigating","created":"2026-11-01T09:00:00Z","updated":"2026-11-01T09:00:00Z"}]}`},
			"-proposals":       {cards.CardsKey: oneCard, cards.ChatsKey: oneChat},
			"-cases":           {},
			"-runs":            {"jobs.json": `{"jobs":{"nightly-audit":{"id":"nightly-audit","kind":"nightly-audit","name":"markJobQ9","when":{"schedule":{"kind":"daily","at":"01:00"}},"budget":{"runUSD":3},"on":true}}}`},
			"-casework":        {fixes.Key: oneFix},
		}, []string{"markSetupQ1", "markAuditQ2", "markSessionQ3", "markAIQ4", "markKnowQ5", "markGroupQ6", "markFactQ7", "markKnownQ8",
			"Restart shop/web", "It cannot sign in.", "markJobQ9", "Raise the limit", "markCaseQ10"}
}

// configMapNames are the ConfigMaps of the console's namespace.
func (c *simCluster) configMapNames(t *testing.T) []string {
	t.Helper()
	list, err := c.cs.CoreV1().ConfigMaps(simNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, cm := range list.Items {
		out = append(out, cm.Name)
	}
	sort.Strings(out)
	return out
}

// An upgrade from a release before 0.13 (DB-55, DB-72): with a record set
// in every ConfigMap of before and a case in its own, the first start
// copies every record into the database and deletes every ConfigMap of
// before and the case's, and no other; the configuration ConfigMaps (the
// agents' baselines, the sign-in's, the Toolbox's) and the temporary wiki
// store's stay. A ConfigMap made again empty, as a chart that still
// rendered it would make it, holds nothing to copy and is deleted again at
// the next start, and the database's records are as they were.
func TestAnUpgradeLeavesNoConfigMapOfBefore(t *testing.T) {
	direct := dbtest.Config(t)
	ctx := context.Background()
	data, marks := beforeConfigMaps(t)
	c := newSimClusterWith(t, data, []sim.SeedObject{
		{Name: simRelease + "-agents", Data: map[string]string{"troubleshooter.md": "Cite our runbooks."}},
		{Name: simRelease + "-auth-config", Data: map[string]string{"auth.json": "{}"}},
		{Name: simRelease + "-toolbox", Data: map[string]string{"toolbox.json": "{}"}},
		{Name: simRelease + "-case-1", Labels: map[string]string{"app.kubernetes.io/instance": simRelease, "221b.io/case": "1"},
			Data: map[string]string{issues.CaseKey: `{"number":1,"title":"markCaseQ10","reporter":{"id":"ann"},"status":"new","created":"2026-11-07T02:00:00Z","updated":"2026-11-07T02:00:00Z","statusAt":"2026-11-07T02:00:00Z","priority":3,"lastReporter":"2026-11-07T02:00:00Z"}`}},
	})
	var before []string
	for _, suffix := range recordSetConfigMaps {
		before = append(before, simRelease+suffix)
	}
	before = append(before, simRelease+"-case-1")
	stays := []string{simRelease + "-agents", simRelease + "-auth-config", simRelease + "-toolbox", simRelease + "-wiki"}
	if got := c.configMapNames(t); len(got) != len(before)+len(stays) {
		t.Fatalf("the cluster starts with %v", got)
	}
	c.written()
	con := c.start(t, consoleOptions{database: &direct})
	if con.url == "" {
		t.Fatalf("the console did not start: %v\n%s", con.stopped(time.Second), con.logs.String())
	}
	gone := func() bool {
		left := c.configMapNames(t)
		for _, name := range before {
			if slices.Contains(left, name) {
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(time.Minute); !gone(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("ConfigMaps of before are left a minute after the start: %v\n%s", c.configMapNames(t), con.logs.String())
		}
	}
	settled(t, c, direct)
	if got := c.configMapNames(t); !slices.Equal(got, stays) {
		t.Errorf("the ConfigMaps after the start: %v, want %v", got, stays)
	}
	for _, w := range c.written() {
		if w.Method == http.MethodDelete && strings.Contains(w.Path, "/configmaps/") && !slices.Contains(before, w.Path[strings.LastIndex(w.Path, "/")+1:]) {
			t.Errorf("a ConfigMap that is not of before was deleted: %s", w.Path)
		}
		if w.Method != http.MethodDelete && strings.Contains(w.Path, "/configmaps") {
			t.Errorf("a ConfigMap was written: %s %s", w.Method, w.Path)
		}
	}
	d := dbtest.OpenConfig(t, direct)
	for _, mark := range marks {
		if held := dbtest.Holding(t, d, mark); len(held) == 0 {
			t.Errorf("%q is in no table after the copy:\n%s", mark, con.logs.String())
		}
	}
	log := con.logs.String()
	if n := strings.Count(log, "deleted a ConfigMap of before: the database keeps every record set it held"); n != len(recordSetConfigMaps) {
		t.Errorf("the log names %d ConfigMaps deleted, want %d:\n%s", n, len(recordSetConfigMaps), log)
	}
	if strings.Contains(log, "a ConfigMap of before is kept") {
		t.Errorf("a ConfigMap of before was kept:\n%s", log)
	}
	if err := con.stop(); err != nil {
		t.Fatal(err)
	}

	// Made again empty: deleted again at the next start, nothing copied
	// over the database's records.
	imports := counts(tables(t, direct))["imports"]
	for _, suffix := range []string{"-ai", ""} {
		if _, err := c.cs.CoreV1().ConfigMaps(simNamespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: simRelease + suffix, Namespace: simNamespace}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	again := c.start(t, consoleOptions{database: &direct})
	if again.url == "" {
		t.Fatalf("the console did not start again: %v\n%s", again.stopped(time.Second), again.logs.String())
	}
	for deadline := time.Now().Add(time.Minute); !gone(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("a ConfigMap made again is left: %v\n%s", c.configMapNames(t), again.logs.String())
		}
	}
	settled(t, c, direct)
	if got := counts(tables(t, direct))["imports"]; got != imports {
		t.Errorf("the imports went from %s to %s rows", imports, got)
	}
	for _, mark := range marks {
		if held := dbtest.Holding(t, d, mark); len(held) == 0 {
			t.Errorf("%q is in no table after the next start", mark)
		}
	}
	if got := c.configMapNames(t); !slices.Equal(got, stays) {
		t.Errorf("the ConfigMaps after the next start: %v, want %v", got, stays)
	}
	// A start that finds no ConfigMap of before starts as any other.
	if err := again.stop(); err != nil {
		t.Fatal(err)
	}
	third := c.start(t, consoleOptions{database: &direct})
	if third.url == "" || strings.Contains(third.logs.String(), "ConfigMap of before") {
		t.Fatalf("a start with no ConfigMap of before: %v\n%s", third.stopped(time.Second), third.logs.String())
	}
}

// Automation turned off at the upgrade (decided by the owner on November
// 21, 2026): its ConfigMaps of before are copied into the database and
// deleted all the same, as every other, and nothing offers the records
// until Automation is turned on, when they are there.
func TestAutomationOffStillCopiesItsConfigMapsOfBefore(t *testing.T) {
	direct := dbtest.Config(t)
	c := newSimCluster(t, map[string]map[string]string{
		"-automation":      {"groups.json": `[{"name":"markGroupA1","desc":"Monthly"}]`},
		"-automation-runs": {"facts.json": `{"local/node-1":{"cluster":"local","node":"node-1","at":"2026-11-01T09:00:00Z","facts":{"os":"markFactA2"}}}`},
	})
	auto := []string{simRelease + "-automation", simRelease + "-automation-runs"}
	left := func() []string {
		var out []string
		for _, name := range c.configMapNames(t) {
			if slices.Contains(auto, name) {
				out = append(out, name)
			}
		}
		return out
	}
	if got := left(); len(got) != 2 {
		t.Fatalf("the cluster starts with %v", got)
	}
	off := c.start(t, consoleOptions{database: &direct, args: []string{"-automation=false"}})
	if off.url == "" {
		t.Fatalf("the console did not start: %v\n%s", off.stopped(time.Second), off.logs.String())
	}
	for deadline := time.Now().Add(time.Minute); len(left()) > 0; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("Automation's ConfigMaps of before are left a minute after a start with it off: %v\n%s", left(), off.logs.String())
		}
	}
	settled(t, c, direct)
	d := dbtest.OpenConfig(t, direct)
	for _, mark := range []string{"markGroupA1", "markFactA2"} {
		if held := dbtest.Holding(t, d, mark); len(held) == 0 {
			t.Errorf("%q is in no table after a start with Automation off:\n%s", mark, off.logs.String())
		}
	}
	b := browser()
	off.signIn(b)
	if code, body, _ := off.get(b, "/api/v1/automation"); code != http.StatusNotFound {
		t.Errorf("with Automation off, GET /api/v1/automation answers %d: %s", code, body)
	}
	if err := off.stop(); err != nil {
		t.Fatal(err)
	}

	on := c.start(t, consoleOptions{database: &direct})
	if on.url == "" {
		t.Fatalf("the console did not start with Automation on: %v\n%s", on.stopped(time.Second), on.logs.String())
	}
	b = browser()
	on.signIn(b)
	code, body, _ := on.get(b, "/api/v1/automation")
	if code != http.StatusOK || !strings.Contains(body, "markGroupA1") {
		t.Fatalf("with Automation turned on, the copied group is not there: %d %s", code, body)
	}
}
