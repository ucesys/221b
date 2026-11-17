package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// A provider's limits are kept with the providers, shown with its spend,
// and stop every agent on it, but not those on another provider (AI-36,
// AI-37). 0 is no limit; a provider removed takes its limits with it.
func TestProviderLimitsStopTheAgentsOnIt(t *testing.T) {
	w := setup(t, nil)
	ctx := context.Background()
	if err := w.ai.SaveProvider(ctx, Provider{ID: "second", Name: "Second", Provider: sherlock.Provider{Kind: sherlock.KindAnthropic}}, true); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.KeysFor("second").Set(ctx, "sk-ant-test-key-9876543210", "test"); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{AgentSettings: AgentSettings{Provider: "second"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]sherlock.Caps{
		{"nowhere": {DailyUSD: 1}},
		{"second": {DailyUSD: 10, MonthlyUSD: 5}},
		{"second": {DailyUSD: -1}},
		{"second": {MonthlyUSD: MaxCapUSD + 1}},
	} {
		if err := w.ai.SetLimits(ctx, Limits{DailyUSD: 20, MonthlyUSD: 200, Providers: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	if err := w.ai.SetLimits(ctx, Limits{DailyUSD: 20, MonthlyUSD: 200, Providers: map[string]sherlock.Caps{"second": {DailyUSD: 0.000001}, DefaultProvider: {MonthlyUSD: 100}}}); err != nil {
		t.Fatal(err)
	}
	a, err := w.ai.Agent(Auditor, sherlock.Question{})
	if err != nil || a.Provider != "second" || a.ProviderName != "Second" || a.ProviderCaps.DailyUSD != 0.000001 {
		t.Fatalf("%+v %v", a, err)
	}
	// A provider's connection test counts as its spend.
	w.engine.Ledger().AddFor(ctx, "", "second", 0.01, false)
	var capErr *sherlock.ErrCap
	if res := w.run(t, Auditor, "look"); !errors.As(res.Err, &capErr) || capErr.Provider != "Second" || !strings.Contains(res.Err.Error(), "Settings › Limits › Providers (Second)") {
		t.Fatalf("past the provider's limit: %v", res.Err)
	}
	// An agent on the default provider answers on.
	if res := w.run(t, Troubleshooter, "look"); res.Err != nil {
		t.Fatal(res.Err)
	}
	v := w.ai.View(ctx)
	for _, p := range v.Providers {
		switch p.ID {
		case "second":
			if p.Limit.DailyUSD != 0.000001 || p.Spend.DayUSD != 0.01 {
				t.Fatalf("second %+v %+v", p.Limit, p.Spend)
			}
		case DefaultProvider:
			if p.Limit.MonthlyUSD != 100 || p.Spend.DayUSD <= 0 {
				t.Fatalf("default %+v %+v", p.Limit, p.Spend)
			}
		}
	}
	// The total limits are left as they are, and 0 is no limit for both.
	if err := w.ai.SetLimits(ctx, Limits{Providers: map[string]sherlock.Caps{"second": {}}}); err != nil {
		t.Fatal(err)
	}
	if sh := w.set.Current().SherlockOrDefault(); sh.DailyUSD != 0 || sh.MonthlyUSD != 0 {
		t.Fatalf("total %+v", sh)
	}
	if res := w.run(t, Auditor, "look once more"); res.Err != nil {
		t.Fatalf("with no limits: %v", res.Err)
	}
	if ps := w.ai.read(ctx).providers; ps.Limits["second"] != (sherlock.Caps{}) || ps.Limits[DefaultProvider].MonthlyUSD != 100 {
		t.Fatalf("kept %+v", ps.Limits)
	}
	// A providers map left out keeps theirs.
	if err := w.ai.SetLimits(ctx, Limits{DailyUSD: 20, MonthlyUSD: 200}); err != nil {
		t.Fatal(err)
	}
	if w.ai.read(ctx).providers.Limits[DefaultProvider].MonthlyUSD != 100 {
		t.Fatal("a change of the total alone dropped a provider's limits")
	}
	if err := w.ai.SaveAgent(ctx, Auditor, AgentChange{}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.SetLimits(ctx, Limits{DailyUSD: 20, MonthlyUSD: 200, Providers: map[string]sherlock.Caps{"second": {DailyUSD: 3}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.ai.DeleteProvider(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.ai.read(ctx).providers.Limits["second"]; ok {
		t.Fatal("a removed provider's limits stayed")
	}
}
