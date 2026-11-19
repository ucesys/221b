package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// kept is what a backend holds under a key, as the service reads it.
func kept[T any](t *testing.T, b kv.Backend, key string) string {
	t.Helper()
	var v T
	if _, err := load(context.Background(), b, key, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(v, "", " ")
	return string(out)
}

// A providers document with every field set reads back from the database
// as it was saved, and so does each change to it: the order, a header, a
// provider removed, the default provider's own.
func TestProvidersInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	doc := NewProvidersPG(d)
	if _, _, err := doc.LoadKey(ctx, ProvidersKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("before anything is saved: %v", err)
	}
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	ps := Providers{
		DefaultName: "Anthropic direct", DefaultWindow: 200000, DefaultTested: &ProviderTest{At: at, OK: true, Model: "claude-opus-5-5", Conn: "c0"}, DefaultID: "gateway",
		List: []Provider{
			{ID: "gateway", Name: "Gateway", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gateway.example/v1", Auth: sherlock.AuthHeader, AuthHeaderName: "X-Key",
				AuthPrefix: "Token ", Headers: map[string]string{"X-Team": "platform", "X-Empty": ""}, Query: "api-version=2026-01-01", Region: "eu-west-1", Project: "p-1", CACert: "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n"},
				Model: "gpt-5", PriceIn: 1.25, PriceOut: 10.5, ContextWindow: 128000,
				Tested: &ProviderTest{At: at.Add(time.Minute), OK: false, Model: "gpt-5", Keyless: true, Conn: "c1", Error: "401 from the endpoint"}},
			{ID: "local", Name: "Local", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "http://ollama.ai.svc:11434/v1"}, Model: "llama"},
		},
		Limits: map[string]sherlock.Caps{"gateway": {DailyUSD: 5, MonthlyUSD: 50.5}, DefaultProvider: {MonthlyUSD: 100}},
	}
	save := func() {
		t.Helper()
		_, version, err := doc.LoadKey(ctx, ProvidersKey)
		if err != nil && !errors.Is(err, kv.ErrNotFound) {
			t.Fatal(err)
		}
		b, _ := json.Marshal(ps)
		if _, err := doc.SaveKey(ctx, ProvidersKey, b, version); err != nil {
			t.Fatal(err)
		}
		want, _ := json.MarshalIndent(ps, "", " ")
		if got := kept[Providers](t, NewProvidersPG(d), ProvidersKey); got != string(want) {
			t.Fatalf("read back\n%s\nsaved\n%s", got, want)
		}
	}
	save()
	// Every field is a column: nothing of a provider is kept as JSON.
	var rows, headers int
	var prefix, conn string
	var daily float64
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM ai_providers), (SELECT count(*) FROM ai_provider_headers), auth_prefix, tested_conn, daily_limit_usd::float8
		FROM ai_providers WHERE ai_provider_id = 'gateway' AND is_default AND tested_is_keyless AND NOT tested_ok`).Scan(&rows, &headers, &prefix, &conn, &daily); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || headers != 2 || prefix != "Token " || conn != "c1" || daily != 5 {
		t.Fatalf("rows %d, headers %d, prefix %q, conn %q, daily %v", rows, headers, prefix, conn, daily)
	}
	// The order changes, a header goes, and the default is the built-in again.
	ps.List[0], ps.List[1] = ps.List[1], ps.List[0]
	ps.List[1].Headers = map[string]string{"X-Team": "data"}
	ps.List[1].Tested = nil
	ps.DefaultID = ""
	save()
	// A provider goes, with its limits and headers; the default provider is removed.
	ps.List = ps.List[:1]
	delete(ps.Limits, "gateway")
	ps.DefaultRemoved, ps.DefaultTested, ps.DefaultID = true, nil, "local"
	save()
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM ai_providers), (SELECT count(*) FROM ai_provider_headers)").Scan(&rows, &headers); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || headers != 0 {
		t.Fatalf("after a provider went: rows %d, headers %d", rows, headers)
	}
	// Nothing kept of the default provider: its row goes too.
	ps.DefaultName, ps.DefaultWindow, ps.DefaultRemoved, ps.Limits = "", 0, false, nil
	save()
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM ai_providers").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows %d: %v", rows, err)
	}
}

// The agents' settings likewise: what an administrator chose for an agent,
// the flags that are of no agent, and the baselines tested.
func TestAgentsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	doc := NewAgentsPG(d)
	on, off := true, false
	a := Agents{
		Agents: map[string]AgentSettings{
			Auditor:        {On: &on, Provider: "gateway", Model: "gpt-5", DailyUSD: 2.5, MonthlyUSD: 20, Clusters: []string{"prod", "dev"}, Tools: []string{"review_cluster"}, ContextWindow: 64000, Compact: "auto"},
			Troubleshooter: {On: &off},
			ChangePlanner:  {Model: "claude-haiku-5-5"},
		},
		SingleProvider: true, Migrated: true,
		BaselineTested: map[string]string{Auditor: "h1", Sherlock: "h2"},
	}
	save := func() {
		t.Helper()
		_, version, err := doc.LoadKey(ctx, AgentsKey)
		if err != nil && !errors.Is(err, kv.ErrNotFound) {
			t.Fatal(err)
		}
		b, _ := json.Marshal(a)
		if _, err := doc.SaveKey(ctx, AgentsKey, b, version); err != nil {
			t.Fatal(err)
		}
		want, _ := json.MarshalIndent(a, "", " ")
		if got := kept[Agents](t, NewAgentsPG(d), AgentsKey); got != string(want) {
			t.Fatalf("read back\n%s\nsaved\n%s", got, want)
		}
	}
	save()
	// Every field is a column, and an agent that is neither on nor off by
	// an administrator's choice has none there.
	var chosen, unset bool
	var clusters []string
	if err := d.Pool().QueryRow(ctx, `SELECT (SELECT is_enabled IS NULL FROM agents WHERE agent_id = $1), NOT is_enabled, (SELECT cluster_ids FROM agents WHERE agent_id = $3)
		FROM agents WHERE agent_id = $2`, ChangePlanner, Troubleshooter, Auditor).Scan(&unset, &chosen, &clusters); err != nil || !unset || !chosen || len(clusters) != 2 {
		t.Fatalf("on unset %v, off chosen %v, clusters %v: %v", unset, chosen, clusters, err)
	}
	// An agent back at its defaults keeps its row and is in the document no more.
	delete(a.Agents, Troubleshooter)
	a.Agents[Auditor] = AgentSettings{Clusters: []string{"prod"}}
	a.SingleProvider = false
	delete(a.BaselineTested, Sherlock)
	save()
	var rows int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM agents").Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("rows %d: %v", rows, err)
	}
	// A row made for what points at an agent says nothing of its settings.
	if err := EnsureAgent(ctx, d.Pool(), AutomationAuthor); err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(a, "", " ")
	if got := kept[Agents](t, doc, AgentsKey); got != string(want) {
		t.Fatalf("after a row was made\n%s\nwant\n%s", got, want)
	}
}

// The service over the database does what it does over a ConfigMap: the
// same steps leave the same providers and agents, and another replica
// reads them.
func TestTheAgentsServiceOverTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	open := func(b kv.Backend) *Service {
		// Whole seconds: the database keeps microseconds.
		tick := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
		set := &fakeSettings{sh: settings.Sherlock{Model: "claude-opus-5-5", DailyUSD: 20, MonthlyUSD: 200, Clusters: map[string]bool{"local": true}}}
		keys := sherlock.NewKeys(nil, "", nil)
		if err := keys.Set(ctx, "sk-ant-test-key-0123456789", "test"); err != nil {
			t.Fatal(err)
		}
		return New(Options{Backend: b, Settings: set, Keys: keys, DefaultURL: "http://127.0.0.1:1", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now: func() time.Time { tick = tick.Add(time.Second); return tick }})
	}
	inDB := func() kv.Backend {
		return &kv.Keyed{Keys: map[string]kv.Backend{ProvidersKey: NewProvidersPG(d), AgentsKey: NewAgentsPG(d)}, Rest: &kv.Memory{}}
	}
	steps := func(s *Service) {
		t.Helper()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(s.SaveProvider(ctx, Provider{ID: "gateway", Name: "Gateway", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://gateway.example/v1", Headers: map[string]string{"X-Team": "platform"}}, Model: "gpt-5", PriceIn: 1, PriceOut: 4}, true))
		must(s.SaveProvider(ctx, Provider{ID: "second", Name: "Second", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://second.example/v1"}, Model: "gpt-5-mini"}, true))
		must(s.SaveProvider(ctx, Provider{ID: "third", Name: "Third", Provider: sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://third.example/v1"}, Model: "gpt-5-nano"}, true))
		must(s.RecordTest(ctx, "gateway", "gpt-5", errors.New("401 Unauthorized")))
		must(s.RecordTest(ctx, DefaultProvider, "claude-opus-5-5", nil))
		must(s.SetLimits(ctx, Limits{DailyUSD: 20, MonthlyUSD: 200, Providers: map[string]sherlock.Caps{"gateway": {DailyUSD: 3, MonthlyUSD: 30}, DefaultProvider: {MonthlyUSD: 150}}}))
		must(s.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "gateway", Tools: []string{"review_cluster"}, DailyUSD: 2, MonthlyUSD: 20, Clusters: []string{"prod"}}}))
		off := false
		must(s.SaveAgent(ctx, Troubleshooter, AgentChange{AgentSettings: AgentSettings{On: &off}}))
		must(s.SaveAgent(ctx, ScriptReviewer, AgentChange{AgentSettings: AgentSettings{Provider: "third"}}))
		must(s.MakeDefault(ctx, "second"))
		must(s.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Model: "claude-haiku-5-5"}}))
		must(s.DeleteProvider(ctx, "gateway"))
	}
	mem := &kv.Memory{}
	steps(open(mem))
	first := open(inDB())
	steps(first)
	other := inDB()
	if got, want := kept[Providers](t, other, ProvidersKey), kept[Providers](t, mem, ProvidersKey); got != want {
		t.Fatalf("the providers in the database\n%s\nin a ConfigMap\n%s", got, want)
	}
	if got, want := kept[Agents](t, other, AgentsKey), kept[Agents](t, mem, AgentsKey); got != want {
		t.Fatalf("the agents in the database\n%s\nin a ConfigMap\n%s", got, want)
	}
	// Each service has a key of its own, set at its own time.
	seen := func(s *Service) string {
		v := s.View(ctx)
		for i := range v.Providers {
			v.Providers[i].Key = sherlock.KeyInfo{}
		}
		out, _ := json.Marshal(v.Providers)
		return string(out)
	}
	x, y := seen(first), seen(open(other))
	if x != y || len(first.View(ctx).Providers) != 3 {
		t.Fatalf("another replica's providers\n%s\nthis one's\n%s", y, x)
	}
	// What the ConfigMap held is copied once, and what was saved since stays.
	fresh := dbtest.Open(t)
	p, g := NewProvidersPG(fresh), NewAgentsPG(fresh)
	for _, doc := range []interface {
		Import(context.Context, kv.Backend, string) (bool, error)
	}{p, g} {
		if copied, err := doc.Import(ctx, mem, "configmap"); err != nil || !copied {
			t.Fatalf("copying: %v %v", copied, err)
		}
		if copied, err := doc.Import(ctx, mem, "configmap"); err != nil || copied {
			t.Fatalf("copying again: %v %v", copied, err)
		}
	}
	if got, want := kept[Providers](t, p, ProvidersKey), kept[Providers](t, mem, ProvidersKey); got != want {
		t.Fatalf("the providers copied\n%s\nwant\n%s", got, want)
	}
	if got, want := kept[Agents](t, g, AgentsKey), kept[Agents](t, mem, AgentsKey); got != want {
		t.Fatalf("the agents copied\n%s\nwant\n%s", got, want)
	}
}
