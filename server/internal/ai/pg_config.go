package ai

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// AI configuration's providers and agents in the database (Phase 13,
// DB-39): a provider is a row of ai_providers, known by the id the console
// gives it, with its headers in ai_provider_headers; an agent is a row of
// agents, known by its catalog id. The service is unchanged and reaches
// the rows through db.Doc.
//
// The default provider, Sherlock's of before Phase 12, has its endpoint
// and model in Sherlock's settings. What this document keeps of it (its
// name, context window, last test, limits, and that it was removed) is
// the row 'default'.

// aiScope holds the flags that are of no agent: one provider only, and
// that the instructions from before Phase 12 were carried over.
const aiScope = "ai"

func sameDoc(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// NewProvidersPG keeps the providers in d.
func NewProvidersPG(d *db.DB) *db.Doc[Providers] {
	return &db.Doc[Providers]{DB: d, Key: ProvidersKey, Read: readProviders, Write: writeProviders}
}

func readProviders(ctx context.Context, q db.Querier) (Providers, bool, error) {
	var ps Providers
	found := false
	at := map[string]int{}
	rows, err := q.Query(ctx, `SELECT ai_provider_id, name, kind, endpoint, auth, auth_header, auth_prefix, query, region, project, ca_cert, model,
			price_in_usd::float8, price_out_usd::float8, context_window, tested_at, tested_is_ok, tested_model, tested_is_keyless, tested_conn, tested_error,
			daily_limit_usd::float8, monthly_limit_usd::float8, is_default, is_removed, is_classifier
		FROM ai_providers ORDER BY position, ai_provider_id`)
	if err != nil {
		return ps, false, err
	}
	var p Provider
	var t ProviderTest
	var tested *time.Time
	var caps sherlock.Caps
	var isDefault, removed, classifier bool
	if _, err := pgx.ForEachRow(rows, []any{&p.ID, &p.Name, &p.Kind, &p.Endpoint, &p.Auth, &p.AuthHeaderName, &p.AuthPrefix, &p.Query, &p.Region, &p.Project,
		&p.CACert, &p.Model, &p.PriceIn, &p.PriceOut, &p.ContextWindow, &tested, &t.OK, &t.Model, &t.Keyless, &t.Conn, &t.Error,
		&caps.DailyUSD, &caps.MonthlyUSD, &isDefault, &removed, &classifier}, func() error {
		found = true
		var test *ProviderTest
		if tested != nil {
			x := t
			x.At = tested.UTC()
			test = &x
		}
		if caps.DailyUSD != 0 || caps.MonthlyUSD != 0 {
			if ps.Limits == nil {
				ps.Limits = map[string]sherlock.Caps{}
			}
			ps.Limits[p.ID] = caps
		}
		if p.ID == DefaultProvider {
			ps.DefaultName, ps.DefaultWindow, ps.DefaultTested, ps.DefaultRemoved = p.Name, p.ContextWindow, test, removed
			return nil
		}
		if isDefault {
			ps.DefaultID = p.ID
		}
		if classifier && ps.ClassifierID == "" {
			ps.ClassifierID = p.ID
		}
		x := p
		x.Tested, x.Headers = test, nil
		at[x.ID] = len(ps.List)
		ps.List = append(ps.List, x)
		return nil
	}); err != nil {
		return ps, false, err
	}
	rows, err = q.Query(ctx, "SELECT ai_provider_id, name, value FROM ai_provider_headers")
	if err != nil {
		return ps, false, err
	}
	var id, name, value string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name, &value}, func() error {
		if i, ok := at[id]; ok {
			if ps.List[i].Headers == nil {
				ps.List[i].Headers = map[string]string{}
			}
			ps.List[i].Headers[name] = value
		}
		return nil
	}); err != nil {
		return ps, false, err
	}
	return ps, found, nil
}

func writeProviders(ctx context.Context, tx pgx.Tx, old, next Providers) error {
	put := func(p Provider, position int, test *ProviderTest, isDefault, removed, classifier bool) error {
		var tested *time.Time
		var t ProviderTest
		if test != nil {
			t = *test
			at := t.At.UTC()
			tested = &at
		}
		caps := next.Limits[p.ID]
		if _, err := tx.Exec(ctx, `INSERT INTO ai_providers (ai_provider_id, position, name, kind, endpoint, auth, auth_header, auth_prefix, query, region,
				project, ca_cert, model, price_in_usd, price_out_usd, context_window, tested_at, tested_is_ok, tested_model, tested_is_keyless, tested_conn,
				tested_error, daily_limit_usd, monthly_limit_usd, is_default, is_removed, is_classifier)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
			ON CONFLICT (ai_provider_id) DO UPDATE SET position = excluded.position, name = excluded.name, kind = excluded.kind, endpoint = excluded.endpoint,
				auth = excluded.auth, auth_header = excluded.auth_header, auth_prefix = excluded.auth_prefix, query = excluded.query, region = excluded.region,
				project = excluded.project, ca_cert = excluded.ca_cert, model = excluded.model, price_in_usd = excluded.price_in_usd,
				price_out_usd = excluded.price_out_usd, context_window = excluded.context_window, tested_at = excluded.tested_at, tested_is_ok = excluded.tested_is_ok,
				tested_model = excluded.tested_model, tested_is_keyless = excluded.tested_is_keyless, tested_conn = excluded.tested_conn,
				tested_error = excluded.tested_error, daily_limit_usd = excluded.daily_limit_usd, monthly_limit_usd = excluded.monthly_limit_usd,
				is_default = excluded.is_default, is_removed = excluded.is_removed, is_classifier = excluded.is_classifier`,
			p.ID, position, p.Name, p.Kind, p.Endpoint, p.Auth, p.AuthHeaderName, p.AuthPrefix, p.Query, p.Region, p.Project, p.CACert, p.Model,
			p.PriceIn, p.PriceOut, p.ContextWindow, tested, t.OK, t.Model, t.Keyless, t.Conn, t.Error, caps.DailyUSD, caps.MonthlyUSD, isDefault, removed, classifier); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM ai_provider_headers WHERE ai_provider_id = $1", p.ID); err != nil {
			return err
		}
		for name, v := range p.Headers {
			if _, err := tx.Exec(ctx, "INSERT INTO ai_provider_headers (ai_provider_id, name, value) VALUES ($1, $2, $3)", p.ID, name, v); err != nil {
				return err
			}
		}
		return nil
	}
	kept := []string{}
	// What is kept of the default provider, when anything is.
	if c := next.Limits[DefaultProvider]; next.DefaultName != "" || next.DefaultWindow != 0 || next.DefaultTested != nil || next.DefaultRemoved || c.DailyUSD != 0 || c.MonthlyUSD != 0 {
		kept = append(kept, DefaultProvider)
		if err := put(Provider{ID: DefaultProvider, Name: next.DefaultName, ContextWindow: next.DefaultWindow}, -1, next.DefaultTested, false, next.DefaultRemoved, false); err != nil {
			return err
		}
	}
	before := map[string]Provider{}
	for _, p := range old.List {
		before[p.ID] = p
	}
	place := map[string]int{}
	for i, p := range old.List {
		place[p.ID] = i
	}
	for i, p := range next.List {
		kept = append(kept, p.ID)
		if b, had := before[p.ID]; had && place[p.ID] == i && sameDoc(b, p) && sameDoc(old.Limits[p.ID], next.Limits[p.ID]) && (old.DefaultID == p.ID) == (next.DefaultID == p.ID) &&
			(old.ClassifierID == p.ID) == (next.ClassifierID == p.ID) {
			continue
		}
		if err := put(p, i, p.Tested, next.DefaultID == p.ID, false, next.ClassifierID == p.ID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "DELETE FROM ai_providers WHERE NOT (ai_provider_id = ANY($1))", kept)
	return err
}

// NewAgentsPG keeps the agents' settings in d.
func NewAgentsPG(d *db.DB) *db.Doc[Agents] {
	return &db.Doc[Agents]{DB: d, Key: AgentsKey, Read: readAgents, Write: writeAgents}
}

// agentFlags is what the scope holds.
type agentFlags struct {
	SingleProvider bool `json:"singleProvider,omitempty"`
	Migrated       bool `json:"migrated,omitempty"`
}

func readAgents(ctx context.Context, q db.Querier) (Agents, bool, error) {
	var a Agents
	b, _, err := db.LoadScope(ctx, q, aiScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return a, false, err
	}
	if found {
		var f agentFlags
		if err := json.Unmarshal(b, &f); err != nil {
			return a, false, err
		}
		a.SingleProvider, a.Migrated = f.SingleProvider, f.Migrated
	}
	// A row is every agent's: only those an administrator chose something
	// for are in the document.
	rows, err := q.Query(ctx, `SELECT agent_id, is_enabled, coalesce(ai_provider_id, ''), model, daily_usd::float8, monthly_usd::float8, cluster_ids, tools,
			context_window, compact_mode, baseline_tested, effort, floor_effort, floor_model,
			(is_enabled IS NOT NULL OR ai_provider_id IS NOT NULL OR model <> '' OR daily_usd <> 0 OR monthly_usd <> 0 OR cluster_ids <> '{}'
				OR tools <> '{}' OR context_window <> 0 OR compact_mode <> '' OR effort <> '' OR floor_effort <> '' OR floor_model <> '')
		FROM agents`)
	if err != nil {
		return a, false, err
	}
	var id, baseline string
	var s AgentSettings
	var on *bool
	var clusters, tools []string
	var chosen bool
	if _, err := pgx.ForEachRow(rows, []any{&id, &on, &s.Provider, &s.Model, &s.DailyUSD, &s.MonthlyUSD, &clusters, &tools, &s.ContextWindow, &s.Compact, &baseline, &s.Effort, &s.FloorEffort, &s.FloorModel,
		&chosen}, func() error {
		if baseline != "" {
			if a.BaselineTested == nil {
				a.BaselineTested = map[string]string{}
			}
			a.BaselineTested[id] = baseline
			found = true
		}
		if !chosen {
			return nil
		}
		x := s
		x.On, x.Clusters, x.Tools = nil, nil, nil
		if on != nil {
			v := *on
			x.On = &v
		}
		// The row's own slices: the next row is read into the same variables.
		if len(clusters) > 0 {
			x.Clusters = append([]string(nil), clusters...)
		}
		if len(tools) > 0 {
			x.Tools = append([]string(nil), tools...)
		}
		if a.Agents == nil {
			a.Agents = map[string]AgentSettings{}
		}
		a.Agents[id] = x
		found = true
		return nil
	}); err != nil {
		return a, false, err
	}
	return a, found, nil
}

func writeAgents(ctx context.Context, tx pgx.Tx, old, next Agents) error {
	if old.SingleProvider != next.SingleProvider || old.Migrated != next.Migrated {
		b, _ := json.Marshal(agentFlags{SingleProvider: next.SingleProvider, Migrated: next.Migrated})
		if _, err := db.SaveScope(ctx, tx, aiScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	ids := map[string]bool{}
	for _, m := range []map[string]AgentSettings{old.Agents, next.Agents} {
		for id := range m {
			ids[id] = true
		}
	}
	for _, m := range []map[string]string{old.BaselineTested, next.BaselineTested} {
		for id := range m {
			ids[id] = true
		}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	for _, id := range list {
		was, s := old.Agents[id], next.Agents[id]
		if sameDoc(was, s) && old.BaselineTested[id] == next.BaselineTested[id] {
			continue
		}
		var provider *string
		if s.Provider != "" {
			provider = &s.Provider
		}
		// No tools named is every tool of the agent's purpose.
		clusters, tools := s.Clusters, s.Tools
		if clusters == nil {
			clusters = []string{}
		}
		if tools == nil {
			tools = []string{}
		}
		// An agent with nothing chosen keeps its row, at the defaults:
		// its instructions, tests and spending point at it.
		if _, err := tx.Exec(ctx, `INSERT INTO agents (agent_id, is_enabled, ai_provider_id, model, daily_usd, monthly_usd, cluster_ids, tools, context_window,
				compact_mode, baseline_tested, effort, floor_effort, floor_model)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (agent_id) DO UPDATE SET is_enabled = excluded.is_enabled, ai_provider_id = excluded.ai_provider_id, model = excluded.model,
				daily_usd = excluded.daily_usd, monthly_usd = excluded.monthly_usd, cluster_ids = excluded.cluster_ids, tools = excluded.tools,
				context_window = excluded.context_window, compact_mode = excluded.compact_mode, baseline_tested = excluded.baseline_tested,
				effort = excluded.effort, floor_effort = excluded.floor_effort, floor_model = excluded.floor_model`,
			id, s.On, provider, s.Model, s.DailyUSD, s.MonthlyUSD, clusters, tools, s.ContextWindow, s.Compact, next.BaselineTested[id], s.Effort, s.FloorEffort,
			s.FloorModel); err != nil {
			return err
		}
	}
	return nil
}

// EnsureAgent makes an agent's row when it has none, so that what is kept
// of the agent elsewhere can point at it.
func EnsureAgent(ctx context.Context, q db.Querier, id string) error {
	_, err := q.Exec(ctx, "INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", id)
	return err
}
