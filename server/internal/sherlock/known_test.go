package sherlock

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hungRows is a spend kept in rows whose read waits until it is let go.
type hungRows struct {
	spend Spend
	asked chan struct{}

	mu   sync.Mutex
	hang chan struct{}
}

// hold makes the reads wait from here on, until what it returns is closed.
func (h *hungRows) hold() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hang = make(chan struct{})
	return h.hang
}

func (h *hungRows) Sum(ctx context.Context, _ time.Time) (Spend, error) {
	select {
	case h.asked <- struct{}{}:
	default:
	}
	h.mu.Lock()
	hang := h.hang
	h.mu.Unlock()
	if hang == nil {
		return h.spend, nil
	}
	select {
	case <-hang:
		return h.spend, nil
	case <-ctx.Done():
		return Spend{}, ctx.Err()
	}
}

func (h *hungRows) Add(context.Context, []Cost) error { return nil }

// What the metrics read of the spend (Known) is what the ledger last had:
// it waits for nothing, not even for a read of the saved spend that is
// under way and holds the ledger (DB-15: a scrape must be answered while
// the database is away).
func TestTheKnownSpendWaitsForNothing(t *testing.T) {
	var at atomic.Int64
	at.Store(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, at.Load()).UTC() }
	rows := &hungRows{spend: Spend{Day: "2026-11-20", Month: "2026-11", DayUSD: 1.5, MonthUSD: 4}, asked: make(chan struct{}, 1)}
	l := NewLedger(nil, now, nil)
	l.UseRows(rows)
	if s, unsaved := l.Known(); s.DayUSD != 0 || unsaved != "" {
		t.Fatalf("before anything was read: %+v %q", s, unsaved)
	}
	if s := l.Now(context.Background()); s.DayUSD != 1.5 {
		t.Fatalf("the spend read: %+v", s)
	}
	l.AddFor(context.Background(), "", "", 0.25, true)
	if s, _ := l.Known(); s.DayUSD != 1.75 || s.MonthUSD != 4.25 {
		t.Fatalf("the spend known after a cost: %+v", s)
	}
	// The saved spend is read again, and the read does not return.
	hang := rows.hold()
	at.Add(int64(time.Minute))
	select {
	case <-rows.asked:
	default:
	}
	done := make(chan struct{})
	go func() { l.Now(context.Background()); close(done) }()
	<-rows.asked
	start := time.Now()
	s, _ := l.Known()
	if took := time.Since(start); took > 100*time.Millisecond || s.DayUSD != 1.75 {
		t.Fatalf("the spend known while a read is under way: %+v in %s", s, took)
	}
	// A new day starts from zero there too.
	at.Add(int64(24 * time.Hour))
	if s, _ := l.Known(); s.DayUSD != 0 || s.MonthUSD != 4.25 {
		t.Fatalf("the spend known the next day: %+v", s)
	}
	close(hang)
	<-done
}
