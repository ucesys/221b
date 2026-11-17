package sherlock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// Each provider's share is counted, saved, merged across replicas and
// rolled over at a new day and month as the agents' are (AI-36).
func TestLedgerCountsEachProvidersShare(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	shared := &kv.Memory{}
	a := NewLedger(shared, clock, nil)
	b := NewLedger(shared, clock, nil)
	a.Now(ctx)
	b.Now(ctx)
	a.AddFor(ctx, "auditor", "default", 1, true)
	b.AddFor(ctx, "sherlock", "default", 0.5, true)
	b.AddFor(ctx, "sherlock", "vllm", 0.25, false)
	b.AddFor(ctx, "", "vllm", 0.25, false) // a provider's connection test
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx); err != nil {
		t.Fatal(err)
	}
	got := NewLedger(shared, clock, nil).Now(ctx)
	if p := got.Provider("default"); p.DayUSD != 1.5 || p.DayAsked != 2 || p.MonthUSD != 1.5 {
		t.Fatalf("default %+v", p)
	}
	if p := got.Provider("vllm"); p.DayUSD != 0.5 || p.DayAsked != 0 || p.MonthUSD != 0.5 {
		t.Fatalf("vllm %+v", p)
	}
	if got.DayUSD != 2 || got.Agent("sherlock").DayUSD != 0.75 || got.Provider("other") != (AgentSpend{}) {
		t.Fatalf("total %+v", got)
	}
	// The next day: the month stays, the day starts from zero.
	now = now.Add(13 * time.Hour)
	b.Reload()
	got = b.Now(ctx)
	if p := got.Provider("default"); p.DayUSD != 0 || p.MonthUSD != 1.5 || p.MonthAsked != 2 {
		t.Fatalf("next day %+v", p)
	}
	// 2026-11-01 is a new month too: every share is gone.
	now = now.Add(24 * time.Hour)
	b.AddFor(ctx, "auditor", "vllm", 0.1, true)
	if err := b.Save(ctx); err != nil {
		t.Fatal(err)
	}
	got = NewLedger(shared, clock, nil).Now(ctx)
	if _, ok := got.Providers["default"]; ok || len(got.Providers) != 1 {
		t.Fatalf("new month %+v", got.Providers)
	}
	if p := got.Provider("vllm"); p.DayUSD != 0.1 || p.MonthUSD != 0.1 || p.DayAsked != 1 {
		t.Fatalf("new month vllm %+v", p)
	}
}

// The limits are checked in order, the total, the provider's, the
// agent's own, and each message says where it is changed (AI-37, AI-38).
func TestCheckAllOrderAndMessages(t *testing.T) {
	s := Spend{DayUSD: 10, MonthUSD: 10,
		Agents:    map[string]AgentSpend{"auditor": {DayUSD: 10, MonthUSD: 10}},
		Providers: map[string]AgentSpend{"vllm": {DayUSD: 10, MonthUSD: 10}}}
	a := &Agent{ID: "auditor", Name: "Auditor", Provider: "vllm", ProviderName: "Lab vLLM", ProviderCaps: Caps{DailyUSD: 5}, Caps: Caps{MonthlyUSD: 5}}
	var ce *ErrCap
	err := CheckAll(Caps{DailyUSD: 5}, s, a)
	if !errors.As(err, &ce) || ce.Agent != "" || ce.Provider != "" || ce.Which != "daily" {
		t.Fatalf("total first: %v", err)
	}
	if want := "All agents together have reached the total daily limit of $5.00 an administrator set, across all providers. Sherlock answers again tomorrow (UTC), or when an administrator raises it in Settings › Limits › Total."; err.Error() != want {
		t.Fatalf("total: %s", err)
	}
	err = CheckAll(Caps{}, s, a) // 0 is no limit
	if !errors.As(err, &ce) || ce.Provider != "Lab vLLM" || ce.Which != "daily" {
		t.Fatalf("provider second: %v", err)
	}
	if want := "The agents on the provider Lab vLLM have reached its daily limit of $5.00 an administrator set. They answer on it again tomorrow (UTC), or when an administrator raises it in Settings › Limits › Providers (Lab vLLM)."; err.Error() != want {
		t.Fatalf("provider: %s", err)
	}
	a.ProviderCaps = Caps{}
	err = CheckAll(Caps{DailyUSD: 50, MonthlyUSD: 500}, s, a)
	if !errors.As(err, &ce) || ce.Agent != "Auditor" || ce.Which != "monthly" {
		t.Fatalf("agent last: %v", err)
	}
	if want := "Auditor has reached its own monthly spending cap of $5.00 an administrator set. It works again next month, or when an administrator raises it in Settings › Agents › Auditor (its own cap)."; err.Error() != want {
		t.Fatalf("agent: %s", err)
	}
	a.Caps = Caps{}
	if err := CheckAll(Caps{}, s, a); err != nil {
		t.Fatalf("no limits: %v", err)
	}
	// Another provider's spend does not count against this one's limit.
	a.Provider, a.ProviderCaps = "default", Caps{DailyUSD: 5, MonthlyUSD: 5}
	if err := CheckAll(Caps{}, s, a); err != nil {
		t.Fatalf("another provider's spend: %v", err)
	}
	if err := CheckAll(Caps{MonthlyUSD: 10}, s, nil); !errors.As(err, &ce) || !strings.Contains(err.Error(), "total monthly limit") {
		t.Fatalf("nil agent: %v", err)
	}
}
