package sherlock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// SpendPG is the spend in the database (Phase 13, DB-41): each cost a row
// of ai_spend, only ever added, and the day's and the month's sums read
// from the rows. It is the Ledger's SpendRows. It is also the document
// under SpendKey for a backup and for the copy from the ConfigMap, which
// hold sums and no rows: read, it is the sums of now; written, the month's
// rows give way to rows that carry those sums over.
type SpendPG struct {
	d   *db.DB
	now func() time.Time
}

// NewSpendPG keeps the spend in d; now is the clock (time.Now when nil).
func NewSpendPG(d *db.DB, now func() time.Time) *SpendPG {
	if now == nil {
		now = time.Now
	}
	return &SpendPG{d: d, now: now}
}

// spans are the starts of t's day and month (UTC), and of the next ones.
func spans(t time.Time) (day, nextDay, month, nextMonth time.Time) {
	t = t.UTC()
	day = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	month = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return day, day.AddDate(0, 0, 1), month, month.AddDate(0, 1, 0)
}

// Sum is the spend of t's day and month.
func (p *SpendPG) Sum(ctx context.Context, t time.Time) (Spend, error) {
	var s Spend
	err := p.d.Do(ctx, func(q db.Querier) (err error) {
		s, err = sumSpend(ctx, q, t)
		return err
	})
	return s, err
}

func sumSpend(ctx context.Context, q db.Querier, t time.Time) (Spend, error) {
	day, nextDay, month, nextMonth := spans(t)
	s := Spend{Day: day.Format("2006-01-02"), Month: month.Format("2006-01")}
	rows, err := q.Query(ctx, `SELECT coalesce(agent_id, ''), coalesce(ai_provider_id, ''), is_share_only,
			coalesce(sum(usd) FILTER (WHERE spent_at >= $1 AND spent_at < $2), 0), coalesce(sum(question_count) FILTER (WHERE spent_at >= $1 AND spent_at < $2), 0)::int,
			coalesce(sum(usd), 0), coalesce(sum(question_count), 0)::int
		FROM ai_spend WHERE spent_at >= $3 AND spent_at < $4 GROUP BY 1, 2, 3`, day, nextDay, month, nextMonth)
	if err != nil {
		return s, err
	}
	var agent, provider string
	var shareOnly bool
	var a AgentSpend
	share := func(m *map[string]AgentSpend, id string) {
		if id == "" || a == (AgentSpend{}) {
			return
		}
		if *m == nil {
			*m = map[string]AgentSpend{}
		}
		x := (*m)[id]
		x.DayUSD, x.DayAsked, x.MonthUSD, x.MonthAsked = x.DayUSD+a.DayUSD, x.DayAsked+a.DayAsked, x.MonthUSD+a.MonthUSD, x.MonthAsked+a.MonthAsked
		(*m)[id] = x
	}
	_, err = pgx.ForEachRow(rows, []any{&agent, &provider, &shareOnly, &a.DayUSD, &a.DayAsked, &a.MonthUSD, &a.MonthAsked}, func() error {
		if !shareOnly {
			s.DayUSD, s.DayAsked, s.MonthUSD, s.MonthAsked = s.DayUSD+a.DayUSD, s.DayAsked+a.DayAsked, s.MonthUSD+a.MonthUSD, s.MonthAsked+a.MonthAsked
		}
		share(&s.Agents, agent)
		share(&s.Providers, provider)
		return nil
	})
	return s, err
}

func orNone(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Add keeps costs, each a row.
func (p *SpendPG) Add(ctx context.Context, costs []Cost) error {
	if len(costs) == 0 {
		return nil
	}
	return p.d.Tx(ctx, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		agents := map[string]bool{}
		for _, c := range costs {
			// What an agent spent points at the agent's row.
			if c.Agent != "" && !agents[c.Agent] {
				agents[c.Agent] = true
				b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", c.Agent)
			}
			asked := 0
			if c.Asked {
				asked = 1
			}
			b.Queue("INSERT INTO ai_spend (ai_spend_id, spent_at, agent_id, ai_provider_id, usd, question_count) VALUES ($1, $2, $3, $4, $5, $6)",
				db.NewID(), c.At.UTC(), orNone(c.Agent), orNone(c.Provider), c.USD, asked)
		}
		return tx.SendBatch(ctx, b).Close()
	})
}

// carry puts sums in place of the rows of t's month: the total as rows
// of no agent and no provider, and each share as rows that count for the
// share alone. A sum of the day is a row at the day's start, the rest of
// the month's a row at the month's start.
func carry(ctx context.Context, tx pgx.Tx, s Spend, t time.Time) (int, error) {
	s = rolled(s, t)
	day, _, month, nextMonth := spans(t)
	b := &pgx.Batch{}
	b.Queue("DELETE FROM ai_spend WHERE spent_at >= $1 AND spent_at < $2", month, nextMonth)
	n := 0
	put := func(agent, provider string, a AgentSpend, shareOnly bool) {
		if agent != "" {
			b.Queue("INSERT INTO agents (agent_id) VALUES ($1) ON CONFLICT (agent_id) DO NOTHING", agent)
		}
		type row struct {
			at    time.Time
			usd   float64
			asked int
		}
		rows := []row{{day, a.DayUSD, a.DayAsked}}
		// On the month's first day the month's sum is the day's.
		if !day.Equal(month) {
			rows = append(rows, row{month, a.MonthUSD - a.DayUSD, a.MonthAsked - a.DayAsked})
		}
		for _, r := range rows {
			if r.usd == 0 && r.asked == 0 {
				continue
			}
			n++
			b.Queue("INSERT INTO ai_spend (ai_spend_id, spent_at, agent_id, ai_provider_id, usd, question_count, is_share_only) VALUES ($1, $2, $3, $4, $5, $6, $7)",
				db.NewID(), r.at, orNone(agent), orNone(provider), r.usd, r.asked, shareOnly)
		}
	}
	put("", "", AgentSpend{DayUSD: s.DayUSD, DayAsked: s.DayAsked, MonthUSD: s.MonthUSD, MonthAsked: s.MonthAsked}, false)
	for _, m := range []struct {
		shares   map[string]AgentSpend
		provider bool
	}{{s.Agents, false}, {s.Providers, true}} {
		ids := make([]string, 0, len(m.shares))
		for id := range m.shares {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if m.provider {
				put("", id, m.shares[id], true)
			} else {
				put(id, "", m.shares[id], true)
			}
		}
	}
	return n, tx.SendBatch(ctx, b).Close()
}

// LoadKey is the sums of now, as the ConfigMap kept them; nothing spent
// this month is kv.ErrNotFound.
func (p *SpendPG) LoadKey(ctx context.Context, _ string) ([]byte, string, error) {
	s, err := p.Sum(ctx, p.now())
	if err != nil {
		return nil, "", err
	}
	if s.empty() {
		return nil, "", kv.ErrNotFound
	}
	b, err := json.Marshal(s)
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:8]), err
}

// SaveKey puts the sums b holds in place of this month's rows, whatever
// they hold: a backup put back replaces the ledger.
func (p *SpendPG) SaveKey(ctx context.Context, key string, b []byte, _ string) (string, error) {
	var s Spend
	if err := json.Unmarshal(b, &s); err != nil {
		return "", err
	}
	if err := p.d.Tx(ctx, func(tx pgx.Tx) error {
		_, err := carry(ctx, tx, s, p.now())
		return err
	}); err != nil {
		return "", err
	}
	_, version, err := p.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		err = nil
	}
	return version, err
}

// DeleteKey forgets this month's spend.
func (p *SpendPG) DeleteKey(ctx context.Context, _ string) error {
	return p.d.Tx(ctx, func(tx pgx.Tx) error {
		_, err := carry(ctx, tx, Spend{}, p.now())
		return err
	})
}

// Import carries the sums from holds under SpendKey over, once, and says
// whether it did.
func (p *SpendPG) Import(ctx context.Context, from kv.Backend, source string) (bool, error) {
	return p.d.ImportOnce(ctx, SpendKey, source, func(ctx context.Context, tx pgx.Tx) (int, error) {
		b, _, err := from.LoadKey(ctx, SpendKey)
		if errors.Is(err, kv.ErrNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		var s Spend
		if err := json.Unmarshal(b, &s); err != nil {
			// As the ledger reads it: not valid counts from zero.
			return 0, nil
		}
		return carry(ctx, tx, s, p.now())
	})
}
