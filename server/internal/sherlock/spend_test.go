package sherlock

import (
	"context"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// Two replicas count into one ConfigMap key: neither save puts back what
// the other one added (P7-02), and each sees the other's within
// ledgerFresh.
func TestLedgerAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	shared := &kv.Memory{}
	a := NewLedger(shared, clock, nil)
	b := NewLedger(shared, clock, nil)
	a.Now(ctx)
	b.Now(ctx) // both have read the empty spend
	a.Add(ctx, 1.0, true)
	b.Add(ctx, 0.5, true)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx); err != nil { // read before a's save: it reads again
		t.Fatal(err)
	}
	c := NewLedger(shared, clock, nil)
	if got := c.Now(ctx); got.DayUSD != 1.5 || got.DayAsked != 2 || got.MonthUSD != 1.5 {
		t.Fatalf("saved %+v", got)
	}
	// a's read of b's save waits for ledgerFresh, or a Reload.
	if got := a.Now(ctx); got.DayUSD != 1.0 {
		t.Fatalf("a re-read at once: %+v", got)
	}
	now = now.Add(ledgerFresh + time.Second)
	if got := a.Now(ctx); got.DayUSD != 1.5 {
		t.Fatalf("a after %v: %+v", ledgerFresh, got)
	}
	b.Add(ctx, 0.25, false)
	_ = b.Save(ctx)
	a.Reload()
	if got := a.Now(ctx); got.DayUSD != 1.75 || got.DayAsked != 2 {
		t.Fatalf("a after Reload: %+v", got)
	}
	// Nothing counted: nothing written.
	before, v1, _ := shared.LoadKey(ctx, SpendKey)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	after, v2, _ := shared.LoadKey(ctx, SpendKey)
	if v1 != v2 || string(before) != string(after) {
		t.Fatalf("an empty save wrote %s", after)
	}
}

// conflicted is a backend where another replica saves once between a
// save's read and its write.
type conflicted struct {
	kv.Memory
	other func()
}

func (c *conflicted) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	if f := c.other; f != nil {
		c.other = nil
		f()
	}
	return c.Memory.SaveKey(ctx, key, b, version)
}

func TestLedgerSaveRetriesAfterAConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := &conflicted{}
	a := NewLedger(c, clock, nil)
	other := NewLedger(&c.Memory, clock, nil)
	c.other = func() {
		other.Add(ctx, 2, true)
		if err := other.Save(ctx); err != nil {
			t.Error(err)
		}
	}
	a.Add(ctx, 1, true)
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	a.Reload()
	if got := a.Now(ctx); got.DayUSD != 3 || got.DayAsked != 2 {
		t.Fatalf("after a conflict %+v", got)
	}
}

func TestKeysReload(t *testing.T) {
	ctx := context.Background()
	shared := &MemoryKey{}
	a := NewKeys(shared, "", nil)
	b := NewKeys(shared, "", nil)
	_ = a.Load(ctx)
	_ = b.Load(ctx)
	if err := a.Set(ctx, "sk-ant-api03-abcdefgh", "ann"); err != nil {
		t.Fatal(err)
	}
	if b.Key() != "" {
		t.Fatal("b has the key before reloading")
	}
	if err := b.Reload(ctx); err != nil || b.Key() != "sk-ant-api03-abcdefgh" || b.Info().SetBy != "ann" {
		t.Fatalf("b after reload: %q %+v %v", b.Key(), b.Info(), err)
	}
	if err := a.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Reload(ctx); err != nil || b.Key() != "" || b.Info().Set {
		t.Fatalf("b after the key was removed: %q %+v %v", b.Key(), b.Info(), err)
	}
	// A key from the chart is not read.
	fixed := NewKeys(shared, "sk-fixed-0123456789", nil)
	if err := fixed.Reload(ctx); err != nil || fixed.Key() != "sk-fixed-0123456789" {
		t.Fatalf("fixed %q %v", fixed.Key(), err)
	}
}
