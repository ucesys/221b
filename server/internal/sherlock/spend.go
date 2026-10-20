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
// restart does not reset it. With several replicas (P7-02) it never saves
// what it read earlier: what it counted is added to what the ConfigMap
// holds at the moment of saving, over that version, and another
// replica's count is read again within ledgerFresh.
type Ledger struct {
	backend kv.Backend
	now     func() time.Time
	log     *slog.Logger

	mu sync.Mutex
	// saved is what the backend held when last read or written, at readAt.
	saved  Spend
	readAt time.Time
	// pending is counted here and not saved yet; its Day and Month say
	// which day and month the counts are of.
	pending Spend
}

// ledgerFresh is how long a read of the saved spend stands.
const ledgerFresh = 10 * time.Second

// saveAttempts bounds the retries of a save that keeps meeting another
// replica's.
const saveAttempts = 5

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

// rolled is the spend as of t: a new day or month starts from zero.
func rolled(s Spend, t time.Time) Spend {
	t = t.UTC()
	day, month := t.Format("2006-01-02"), t.Format("2006-01")
	if s.Month != month {
		s.Month, s.MonthUSD, s.MonthAsked = month, 0, 0
	}
	if s.Day != day {
		s.Day, s.DayUSD, s.DayAsked = day, 0, 0
	}
	return s
}

// plus adds counts of the same day and month to s.
func plus(s, p Spend) Spend {
	if p.Day == s.Day {
		s.DayUSD += p.DayUSD
		s.DayAsked += p.DayAsked
	}
	if p.Month == s.Month {
		s.MonthUSD += p.MonthUSD
		s.MonthAsked += p.MonthAsked
	}
	return s
}

// read loads the saved spend and its version.
func (l *Ledger) read(ctx context.Context) (Spend, string, error) {
	b, version, err := l.backend.LoadKey(ctx, SpendKey)
	switch {
	case errors.Is(err, kv.ErrNotFound):
		return Spend{}, version, nil
	case err != nil:
		return Spend{}, "", err
	}
	var s Spend
	if err := json.Unmarshal(b, &s); err != nil {
		l.log.Warn("the saved Sherlock spend is not valid; counting from zero", "err", err)
		s = Spend{}
	}
	return s, version, nil
}

// refreshLocked reads the saved spend again once the last read is older
// than ledgerFresh. A failed read keeps the last one: until it works, the
// caps count what this process has seen.
func (l *Ledger) refreshLocked(ctx context.Context) {
	now := l.now()
	if !l.readAt.IsZero() && now.Sub(l.readAt) < ledgerFresh && !now.Before(l.readAt) {
		return
	}
	s, _, err := l.read(ctx)
	if err != nil {
		l.log.Warn("reading the Sherlock spend failed", "err", err)
		return
	}
	l.saved, l.readAt = s, now
}

// Reload forgets the last read, so the next use reads the saved spend:
// for a replica that has just become the one Sherlock runs on.
func (l *Ledger) Reload() {
	l.mu.Lock()
	l.readAt = time.Time{}
	l.mu.Unlock()
}

// Now returns the spend so far: what is saved, as read within the last
// few seconds, and what this process has counted since.
func (l *Ledger) Now(ctx context.Context) Spend {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked(ctx)
	return plus(rolled(l.saved, l.now()), l.pending)
}

// Add counts a cost; asked counts a new question. Save writes it.
func (l *Ledger) Add(ctx context.Context, usd float64, asked bool) Spend {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked(ctx)
	t := l.now()
	l.pending = rolled(l.pending, t)
	l.pending.DayUSD += usd
	l.pending.MonthUSD += usd
	if asked {
		l.pending.DayAsked++
		l.pending.MonthAsked++
	}
	return plus(rolled(l.saved, t), l.pending)
}

// Save adds what was counted since the last save to what is saved now,
// over that version, reading again when another replica saved first.
func (l *Ledger) Save(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == (Spend{Day: l.pending.Day, Month: l.pending.Month}) {
		return nil
	}
	for attempt := 1; ; attempt++ {
		saved, version, err := l.read(ctx)
		if err != nil {
			return err
		}
		t := l.now()
		next := plus(rolled(saved, t), l.pending)
		b, _ := json.Marshal(next)
		_, err = l.backend.SaveKey(ctx, SpendKey, b, version)
		if errors.Is(err, kv.ErrConflict) && attempt < saveAttempts {
			continue
		}
		if err != nil {
			return err
		}
		l.saved, l.readAt, l.pending = next, t, Spend{}
		return nil
	}
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
