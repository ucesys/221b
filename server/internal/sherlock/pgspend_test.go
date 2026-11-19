package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

func asJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

// The ledger over the database counts what it counts over a ConfigMap:
// the same costs give the same sums, through a new day and a new month,
// each cost is a row, and two replicas' rows never meet.
func TestTheLedgerOverTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	mem := NewLedger(&kv.Memory{}, clock, nil)
	open := func() *Ledger {
		l := NewLedger(nil, clock, nil)
		l.UseRows(NewSpendPG(d, clock))
		return l
	}
	a, b := open(), open()
	// Sums exact in binary, so the order they are added in does not show.
	spend := func(l *Ledger, agent, provider string, usd float64, asked bool) {
		t.Helper()
		mem.AddFor(ctx, agent, provider, usd, asked)
		l.AddFor(ctx, agent, provider, usd, asked)
		if err := mem.Save(ctx); err != nil {
			t.Fatal(err)
		}
		if err := l.Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	same := func(what string) {
		t.Helper()
		a.Reload()
		b.Reload()
		want := asJSON(mem.Now(ctx))
		if got := asJSON(a.Now(ctx)); got != want {
			t.Fatalf("%s: one replica reads\n%s\nthe ConfigMap's\n%s", what, got, want)
		}
		if got := asJSON(b.Now(ctx)); got != want {
			t.Fatalf("%s: the other replica reads\n%s\nthe ConfigMap's\n%s", what, got, want)
		}
	}
	same("nothing spent")
	spend(a, "sherlock", "default", 0.5, true)
	spend(b, "sherlock", "default", 0.25, false)
	spend(b, "auditor", "gateway", 1.5, true)
	spend(a, "", "", 0.125, true)
	same("the first day")
	var rows int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM ai_spend WHERE NOT is_share_only").Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("rows %d: %v", rows, err)
	}
	// Counted and not saved yet is in what the replica that counted it sees.
	a.AddFor(ctx, "sherlock", "default", 2, true)
	if got := a.Now(ctx); got.DayUSD != 4.375 || got.Agent("sherlock").DayAsked != 2 {
		t.Fatalf("before the save: %+v", got)
	}
	mem.AddFor(ctx, "sherlock", "default", 2, true)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	_ = mem.Save(ctx)
	// A new day: the day's sums start again, the month's go on.
	now = now.Add(2 * time.Hour)
	same("the next day, nothing spent")
	spend(a, "auditor", "default", 0.75, true)
	same("the next day")
	// A new month: everything starts again, and the rows of before stay.
	now = now.Add(24 * time.Hour)
	same("the next month, nothing spent")
	spend(b, "sherlock", "gateway", 0.5, true)
	same("the next month")
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM ai_spend").Scan(&rows); err != nil || rows != 7 {
		t.Fatalf("rows kept %d: %v", rows, err)
	}
}

// The sums a ConfigMap held are carried over once, and a backup put back
// replaces this month's: both read back as they were, and what is spent
// after counts on top.
func TestTheSpendCarriedOver(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	old := &kv.Memory{}
	was := NewLedger(old, clock, nil)
	was.AddFor(ctx, "sherlock", "default", 3, true)
	_ = was.Save(ctx)
	now = now.Add(24 * time.Hour)
	was.AddFor(ctx, "auditor", "gateway", 0.5, true)
	was.AddFor(ctx, "sherlock", "default", 0.25, false)
	_ = was.Save(ctx)
	pg := NewSpendPG(d, clock)
	if _, _, err := pg.LoadKey(ctx, SpendKey); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("before anything is spent: %v", err)
	}
	if copied, err := pg.Import(ctx, old, "configmap"); err != nil || !copied {
		t.Fatalf("copying: %v %v", copied, err)
	}
	l := NewLedger(nil, clock, nil)
	l.UseRows(pg)
	if got, want := asJSON(l.Now(ctx)), asJSON(was.Now(ctx)); got != want {
		t.Fatalf("carried over\n%s\nthe ConfigMap's\n%s", got, want)
	}
	for _, x := range []*Ledger{l, was} {
		x.AddFor(ctx, "sherlock", "default", 1, true)
		if err := x.Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := asJSON(l.Now(ctx)), asJSON(was.Now(ctx)); got != want || l.Now(ctx).MonthUSD != 4.75 {
		t.Fatalf("spent after\n%s\nthe ConfigMap's\n%s", got, want)
	}
	// Once: the ConfigMap's sums are not counted again at the next start.
	if copied, err := pg.Import(ctx, old, "configmap"); err != nil || copied {
		t.Fatalf("copying again: %v %v", copied, err)
	}
	// A backup holds the document; put back, it replaces the ledger here.
	backup, _, err := pg.LoadKey(ctx, SpendKey)
	if err != nil {
		t.Fatal(err)
	}
	l.AddFor(ctx, "auditor", "default", 8, true)
	_ = l.Save(ctx)
	if _, err := pg.SaveKey(ctx, SpendKey, backup, ""); err != nil {
		t.Fatal(err)
	}
	l.Reload()
	if got, want := asJSON(l.Now(ctx)), asJSON(was.Now(ctx)); got != want {
		t.Fatalf("a backup put back\n%s\nwant\n%s", got, want)
	}
	// One taken on another day keeps its month's sums and no day's.
	now = now.Add(48 * time.Hour)
	if _, err := pg.SaveKey(ctx, SpendKey, backup, ""); err != nil {
		t.Fatal(err)
	}
	l.Reload()
	if got := l.Now(ctx); got.DayUSD != 0 || got.MonthUSD != 4.75 || got.Agent("sherlock").MonthUSD != 4.25 || got.Provider("gateway").MonthAsked != 1 {
		t.Fatalf("an older backup put back: %+v", got)
	}
}
