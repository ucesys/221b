package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// SpendKey is the key of the settings ConfigMap the spend is kept under.
const SpendKey = "sherlock-spend.json"

// Spend is what Sherlock has cost today and this month (UTC), in US
// dollars, as counted from the token use the API reports.
type Spend struct {
	Day        string  `json:"day"`
	DayUSD     float64 `json:"dayUSD"`
	DayAsked   int     `json:"dayAsked"`
	Month      string  `json:"month"`
	MonthUSD   float64 `json:"monthUSD"`
	MonthAsked int     `json:"monthAsked"`
}

// Ledger counts the spend and keeps it next to the settings, so a
// restart does not reset it.
type Ledger struct {
	backend kv.Backend
	now     func() time.Time
	log     *slog.Logger

	mu     sync.Mutex
	spend  Spend
	loaded bool
	dirty  bool
}

// NewLedger returns a ledger over the backend (memory when nil).
func NewLedger(b kv.Backend, now func() time.Time, log *slog.Logger) *Ledger {
	if b == nil {
		b = &kv.Memory{}
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Ledger{backend: b, now: now, log: log}
}

func (l *Ledger) roll() {
	t := l.now().UTC()
	day, month := t.Format("2006-01-02"), t.Format("2006-01")
	if l.spend.Month != month {
		l.spend.Month, l.spend.MonthUSD, l.spend.MonthAsked = month, 0, 0
	}
	if l.spend.Day != day {
		l.spend.Day, l.spend.DayUSD, l.spend.DayAsked = day, 0, 0
	}
}

func (l *Ledger) load(ctx context.Context) {
	if l.loaded {
		return
	}
	b, err := l.backend.LoadKey(ctx, SpendKey)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &l.spend); err != nil {
			l.log.Warn("the saved Sherlock spend is not valid; counting from zero", "err", err)
			l.spend = Spend{}
		}
		l.loaded = true
	case errors.Is(err, kv.ErrNotFound):
		l.loaded = true
	default:
		// Not loaded: tried again next time. Until then the caps count what
		// this process has seen.
		l.log.Warn("reading the Sherlock spend failed", "err", err)
	}
}

// Now returns the spend so far.
func (l *Ledger) Now(ctx context.Context) Spend {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load(ctx)
	l.roll()
	return l.spend
}

// Add counts a cost; asked counts a new question.
func (l *Ledger) Add(ctx context.Context, usd float64, asked bool) Spend {
	l.mu.Lock()
	l.load(ctx)
	l.roll()
	l.spend.DayUSD += usd
	l.spend.MonthUSD += usd
	if asked {
		l.spend.DayAsked++
		l.spend.MonthAsked++
	}
	l.dirty = true
	s := l.spend
	l.mu.Unlock()
	return s
}

// Save writes the spend if it changed.
func (l *Ledger) Save(ctx context.Context) error {
	l.mu.Lock()
	if !l.dirty {
		l.mu.Unlock()
		return nil
	}
	b, _ := json.Marshal(l.spend)
	l.dirty = false
	l.mu.Unlock()
	if err := l.backend.SaveKey(ctx, SpendKey, b); err != nil {
		l.mu.Lock()
		l.dirty = true
		l.mu.Unlock()
		return err
	}
	return nil
}

// Caps are the spending limits an administrator sets; 0 is no limit.
type Caps struct {
	DailyUSD   float64 `json:"dailyUSD"`
	MonthlyUSD float64 `json:"monthlyUSD"`
}

// ErrCap is the error for a question refused or stopped at a cap.
type ErrCap struct {
	Which string
	Cap   float64
}

func (e *ErrCap) Error() string {
	when := "tomorrow (UTC)"
	if e.Which == "monthly" {
		when = "next month"
	}
	return fmt.Sprintf("Sherlock has reached the %s spending cap of $%.2f an administrator set. It answers again %s, or when an administrator raises the cap in Settings.", e.Which, e.Cap, when)
}

// Check says whether the spend is under the caps.
func (c Caps) Check(s Spend) error {
	if c.DailyUSD > 0 && s.DayUSD >= c.DailyUSD {
		return &ErrCap{Which: "daily", Cap: c.DailyUSD}
	}
	if c.MonthlyUSD > 0 && s.MonthUSD >= c.MonthlyUSD {
		return &ErrCap{Which: "monthly", Cap: c.MonthlyUSD}
	}
	return nil
}
