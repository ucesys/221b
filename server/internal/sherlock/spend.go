package sherlock

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ucesys/221b/server/internal/db"
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
	// Providers is each provider's share, by its id (Phase 12.1): what
	// every agent spent while on it, counted against its limits.
	Providers map[string]AgentSpend `json:"providers,omitempty"`
}

// AgentSpend is one agent's, or one provider's, share of the spend, of the
// same day and month.
type AgentSpend struct {
	DayUSD     float64 `json:"dayUSD"`
	DayAsked   int     `json:"dayAsked"`
	MonthUSD   float64 `json:"monthUSD"`
	MonthAsked int     `json:"monthAsked"`
}

// Agent is an agent's share; nothing spent is zero.
func (s Spend) Agent(id string) AgentSpend { return s.Agents[id] }

// Provider is a provider's share; nothing spent is zero.
func (s Spend) Provider(id string) AgentSpend { return s.Providers[id] }

// Ledger counts the spend and keeps it next to the settings, so a
// restart does not reset it. With several replicas (P7-02) it never saves
// what it read earlier: what it counted is added to what the ConfigMap
// holds at the moment of saving, over that version, and another
// replica's count is read again within ledgerFresh.
type Ledger struct {
	backend kv.Backend
	now     func() time.Time
	log     *slog.Logger

	// quiet keeps the reads' failures while the database is away to one
	// line (DB-15).
	quiet db.Quiet

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
	// rows, when set, is where the spend is kept in place of the backend:
	// each cost a row (Phase 13), and costs are what was counted here and
	// is not saved yet, as rows.
	rows  SpendRows
	costs []Cost

	// known is the spend and the save's error as the ledger last had them,
	// for whoever must not wait for the ledger (Known).
	known atomic.Pointer[knownSpend]
}

// knownSpend is what Known returns.
type knownSpend struct {
	spend   Spend
	unsaved string
}

// remember keeps what the ledger has now for Known; called with mu held.
func (l *Ledger) remember() {
	l.known.Store(&knownSpend{spend: plus(rolled(l.saved, l.now()), l.pending), unsaved: l.unsaved})
}

// Known is the spend so far and why it could not be saved ("" when it
// was), as the ledger last had them: it reads nothing and waits for
// nothing, not even for a read of the saved spend that is under way. The
// metrics read it; Now is the spend a cap is checked against.
func (l *Ledger) Known() (Spend, string) {
	k := l.known.Load()
	if k == nil {
		return Spend{}, ""
	}
	return rolled(k.spend, l.now()), k.unsaved
}

// Cost is one cost counted: of an agent and the provider it was spent on,
// "" for none, and whether it is a new question's.
type Cost struct {
	At       time.Time
	Agent    string
	Provider string
	USD      float64
	Asked    bool
}

// SpendRows keeps the spend as rows that are only ever added, one a cost,
// and sums them (the database's ai_spend). No save meets another's: two
// replicas add their own rows.
type SpendRows interface {
	// Sum is the spend of t's day and month (UTC).
	Sum(ctx context.Context, t time.Time) (Spend, error)
	// Add keeps costs, all or none.
	Add(ctx context.Context, costs []Cost) error
}

// UseRows keeps the spend in rows from here on; before the ledger is used.
func (l *Ledger) UseRows(rows SpendRows) {
	l.mu.Lock()
	l.rows = rows
	l.mu.Unlock()
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
// agents' and providers' shares too.
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
	if newMonth || newDay {
		s.Agents = rolledShares(s.Agents, newDay, newMonth)
		s.Providers = rolledShares(s.Providers, newDay, newMonth)
	}
	return s
}

// rolledShares starts the shares' new day or month from zero, dropping
// those left with nothing.
func rolledShares(m map[string]AgentSpend, newDay, newMonth bool) map[string]AgentSpend {
	if m == nil {
		return nil
	}
	out := make(map[string]AgentSpend, len(m))
	for id, a := range m {
		if newMonth {
			a.MonthUSD, a.MonthAsked = 0, 0
		}
		if newDay {
			a.DayUSD, a.DayAsked = 0, 0
		}
		if a != (AgentSpend{}) {
			out[id] = a
		}
	}
	return out
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
	s.Agents = plusShares(s.Agents, p.Agents, p.Day == s.Day, p.Month == s.Month)
	s.Providers = plusShares(s.Providers, p.Providers, p.Day == s.Day, p.Month == s.Month)
	return s
}

// plusShares adds shares of the same day and month, in a new map so s's
// own is never changed.
func plusShares(s, p map[string]AgentSpend, day, month bool) map[string]AgentSpend {
	if len(p) == 0 {
		return s
	}
	out := make(map[string]AgentSpend, len(s)+len(p))
	for id, a := range s {
		out[id] = a
	}
	for id, a := range p {
		x := out[id]
		if day {
			x.DayUSD += a.DayUSD
			x.DayAsked += a.DayAsked
		}
		if month {
			x.MonthUSD += a.MonthUSD
			x.MonthAsked += a.MonthAsked
		}
		out[id] = x
	}
	return out
}

// empty says whether nothing was counted.
func (s Spend) empty() bool {
	return s.DayUSD == 0 && s.DayAsked == 0 && s.MonthUSD == 0 && s.MonthAsked == 0 && len(s.Agents) == 0 && len(s.Providers) == 0
}

// read loads the saved spend and its version.
func (l *Ledger) read(ctx context.Context) (Spend, string, error) {
	if l.rows != nil {
		s, err := l.rows.Sum(ctx, l.now())
		return s, "", err
	}
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
		l.quiet.Failed(l.log, "reading the Sherlock spend failed", err)
		return
	}
	l.quiet.Worked(l.log, "the Sherlock spend is read again")
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
	defer l.remember()
	l.refreshLocked(ctx)
	return plus(rolled(l.saved, l.now()), l.pending)
}

// Add counts a cost; asked counts a new question. Save writes it.
func (l *Ledger) Add(ctx context.Context, usd float64, asked bool) Spend {
	return l.AddFor(ctx, "", "", usd, asked)
}

// AddFor counts a cost as an agent's too (Phase 12), and as the provider's
// it was spent on (Phase 12.1); "" is no agent's, or no provider's.
func (l *Ledger) AddFor(ctx context.Context, agent, provider string, usd float64, asked bool) Spend {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer l.remember()
	l.refreshLocked(ctx)
	t := l.now()
	l.pending = rolled(l.pending, t)
	l.pending.DayUSD += usd
	l.pending.MonthUSD += usd
	if asked {
		l.pending.DayAsked++
		l.pending.MonthAsked++
	}
	l.pending.Agents = addShare(l.pending.Agents, agent, usd, asked)
	l.pending.Providers = addShare(l.pending.Providers, provider, usd, asked)
	if l.rows != nil {
		l.costs = append(l.costs, Cost{At: t, Agent: agent, Provider: provider, USD: usd, Asked: asked})
	}
	return plus(rolled(l.saved, t), l.pending)
}

// addShare counts a cost in id's share; "" is nobody's.
func addShare(m map[string]AgentSpend, id string, usd float64, asked bool) map[string]AgentSpend {
	if id == "" {
		return m
	}
	if m == nil {
		m = map[string]AgentSpend{}
	}
	a := m[id]
	a.DayUSD += usd
	a.MonthUSD += usd
	if asked {
		a.DayAsked++
		a.MonthAsked++
	}
	m[id] = a
	return m
}

// Save adds what was counted since the last save to what is saved now,
// over that version, reading again when another replica saved first.
func (l *Ledger) Save(ctx context.Context) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	defer l.remember()
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
	if l.rows != nil {
		// Rows are added, never written over: what another replica added
		// meanwhile is read at the next refresh.
		if err := l.rows.Add(ctx, l.costs); err != nil {
			return err
		}
		t := l.now()
		l.saved, l.pending, l.costs = plus(rolled(l.saved, t), rolled(l.pending, t)), Spend{}, nil
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

// The places in Settings where each limit is changed, as the messages
// name them (Phase 12.1).
const (
	WhereTotal    = "Administration › AI › Limits › Total"
	WhereProvider = "Administration › AI › Limits › Providers"
	WhereAgents   = "Administration › AI › Agents"
)

// ErrCap is the error for a question refused or stopped at a cap.
type ErrCap struct {
	Which string
	Cap   float64
	// Agent names the agent whose own cap it is; Provider the provider
	// whose limit it is (Phase 12.1); both "" is the total limit, of every
	// agent on every provider together.
	Agent    string
	Provider string
}

func (e *ErrCap) Error() string {
	when := "tomorrow (UTC)"
	if e.Which == "monthly" {
		when = "next month"
	}
	// Each message says which limit it is and where it is changed (AI-28,
	// AI-38): the total, a provider's, or the agent's own.
	switch {
	case e.Agent != "":
		return fmt.Sprintf("%s has reached its own %s spending cap of $%.2f an administrator set. It works again %s, or when an administrator raises it in %s › %s (its own cap).", e.Agent, e.Which, e.Cap, when, WhereAgents, e.Agent)
	case e.Provider != "":
		return fmt.Sprintf("The agents on the provider %s have reached its %s limit of $%.2f an administrator set. They answer on it again %s, or when an administrator raises it in %s (%s).", e.Provider, e.Which, e.Cap, when, WhereProvider, e.Provider)
	}
	return fmt.Sprintf("All agents together have reached the total %s limit of $%.2f an administrator set, across all providers. Sherlock answers again %s, or when an administrator raises it in %s.", e.Which, e.Cap, when, WhereTotal)
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

// CheckProvider says whether a provider's share of the spend is under its
// limits (Phase 12.1); name names it in the message.
func (c Caps) CheckProvider(s Spend, id, name string) error {
	p := s.Provider(id)
	if c.DailyUSD > 0 && p.DayUSD >= c.DailyUSD {
		return &ErrCap{Which: "daily", Cap: c.DailyUSD, Provider: name}
	}
	if c.MonthlyUSD > 0 && p.MonthUSD >= c.MonthlyUSD {
		return &ErrCap{Which: "monthly", Cap: c.MonthlyUSD, Provider: name}
	}
	return nil
}

// CheckAll checks the limits an agent answers under, in order: the total,
// its provider's, then its own (AI-37). A nil agent is checked against
// the total alone.
func CheckAll(total Caps, s Spend, a *Agent) error {
	if err := total.Check(s); err != nil {
		return err
	}
	if a == nil {
		return nil
	}
	if a.Provider != "" {
		if err := a.ProviderCaps.CheckProvider(s, a.Provider, cmp.Or(a.ProviderName, a.Provider)); err != nil {
			return err
		}
	}
	return a.Caps.CheckAgent(s, a.ID, a.Name)
}
