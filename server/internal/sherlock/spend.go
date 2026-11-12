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
// dollars, as counted from the token use the API reports, or estimated
// where it reports none.
type Spend struct {
	Day        string  `json:"day"`
	DayUSD     float64 `json:"dayUSD"`
	DayAsked   int     `json:"dayAsked"`
	Month      string  `json:"month"`
	MonthUSD   float64 `json:"monthUSD"`
	MonthAsked int     `json:"monthAsked"`
	// Agents is each agent's share of the figures above, by its id (Phase
	// 12): what an agent spends counts against its own caps and, with
	// every other agent's, against the overall ones.
	Agents map[string]AgentSpend `json:"agents,omitempty"`
}

// AgentSpend is one agent's share of the spend, of the same day and month.
type AgentSpend struct {
	DayUSD     float64 `json:"dayUSD"`
	DayAsked   int     `json:"dayAsked"`
	MonthUSD   float64 `json:"monthUSD"`
	MonthAsked int     `json:"monthAsked"`
}

// Agent is an agent's share; nothing spent is zero.
func (s Spend) Agent(id string) AgentSpend { return s.Agents[id] }

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
	// unsaved is why the last save failed, "" once one succeeds: what is
	// counted here is lost on a restart until then.
	unsaved string
}

// SaveError says why the spend could not be saved, "" when it was.
func (l *Ledger) SaveError() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unsaved
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

// rolled is the spend as of t: a new day or month starts from zero, the
// agents' shares too.
func rolled(s Spend, t time.Time) Spend {
	t = t.UTC()
	day, month := t.Format("2006-01-02"), t.Format("2006-01")
	newMonth, newDay := s.Month != month, s.Day != day
	if newMonth {
		s.Month, s.MonthUSD, s.MonthAsked = month, 0, 0
	}
	if newDay {
		s.Day, s.DayUSD, s.DayAsked = day, 0, 0
	}
	if (newMonth || newDay) && s.Agents != nil {
		agents := make(map[string]AgentSpend, len(s.Agents))
		for id, a := range s.Agents {
			if newMonth {
				a.MonthUSD, a.MonthAsked = 0, 0
			}
			if newDay {
				a.DayUSD, a.DayAsked = 0, 0
			}
			if a != (AgentSpend{}) {
				agents[id] = a
			}
		}
		s.Agents = agents
	}
	return s
}

// plus adds counts of the same day and month to s.
func plus(s, p Spend) Spend {
	if len(p.Agents) > 0 {
		agents := make(map[string]AgentSpend, len(s.Agents)+len(p.Agents))
		for id, a := range s.Agents {
			agents[id] = a
		}
		s.Agents = agents
	}
	if p.Day == s.Day {
		s.DayUSD += p.DayUSD
		s.DayAsked += p.DayAsked
		for id, a := range p.Agents {
			x := s.Agents[id]
			x.DayUSD += a.DayUSD
			x.DayAsked += a.DayAsked
			s.Agents[id] = x
		}
	}
	if p.Month == s.Month {
		s.MonthUSD += p.MonthUSD
		s.MonthAsked += p.MonthAsked
		for id, a := range p.Agents {
			x := s.Agents[id]
			x.MonthUSD += a.MonthUSD
			x.MonthAsked += a.MonthAsked
			s.Agents[id] = x
		}
	}
	return s
}

// empty says whether nothing was counted.
func (s Spend) empty() bool {
	return s.DayUSD == 0 && s.DayAsked == 0 && s.MonthUSD == 0 && s.MonthAsked == 0 && len(s.Agents) == 0
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
	return l.AddFor(ctx, "", usd, asked)
}

// AddFor counts a cost as an agent's too (Phase 12); "" is no agent's.
func (l *Ledger) AddFor(ctx context.Context, agent string, usd float64, asked bool) Spend {
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
	if agent != "" {
		if l.pending.Agents == nil {
			l.pending.Agents = map[string]AgentSpend{}
		}
		a := l.pending.Agents[agent]
		a.DayUSD += usd
		a.MonthUSD += usd
		if asked {
			a.DayAsked++
			a.MonthAsked++
		}
		l.pending.Agents[agent] = a
	}
	return plus(rolled(l.saved, t), l.pending)
}

// Save adds what was counted since the last save to what is saved now,
// over that version, reading again when another replica saved first.
func (l *Ledger) Save(ctx context.Context) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer func() {
		if err != nil {
			l.unsaved = err.Error()
		} else {
			l.unsaved = ""
		}
	}()
	if l.pending.empty() {
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
	// Agent names the agent whose own cap it is; "" is the overall cap.
	Agent string
}

func (e *ErrCap) Error() string {
	when := "tomorrow (UTC)"
	if e.Which == "monthly" {
		when = "next month"
	}
	if e.Agent != "" {
		return fmt.Sprintf("%s has reached its %s spending cap of $%.2f an administrator set. It works again %s, or when an administrator raises the cap in Settings › Agents.", e.Agent, e.Which, e.Cap, when)
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

// CheckAgent says whether an agent's share of the spend is under its own
// caps; name names it in the message.
func (c Caps) CheckAgent(s Spend, id, name string) error {
	a := s.Agent(id)
	if c.DailyUSD > 0 && a.DayUSD >= c.DailyUSD {
		return &ErrCap{Which: "daily", Cap: c.DailyUSD, Agent: name}
	}
	if c.MonthlyUSD > 0 && a.MonthUSD >= c.MonthlyUSD {
		return &ErrCap{Which: "monthly", Cap: c.MonthlyUSD, Agent: name}
	}
	return nil
}
