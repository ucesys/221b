package automation

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/inventory"
)

// What is known of each node, in the database (Phase 13, DB-28): the
// facts last read from a node or a host are the row of host_facts that
// points at its row of hosts, the facts themselves one JSON object read
// whole (model rule 9: observed state is a fact, not a column). The store
// is unchanged and reaches them through db.Doc.

// NewFactsPG keeps the nodes' facts in d.
func NewFactsPG(d *db.DB) *db.Doc[map[string]NodeFacts] {
	return &db.Doc[map[string]NodeFacts]{DB: d, Key: FactsKey, Read: readFacts, Write: writeFacts}
}

func readFacts(ctx context.Context, q db.Querier) (map[string]NodeFacts, bool, error) {
	rows, err := q.Query(ctx, `SELECT coalesce(h.cluster_id, ''), h.name, f.read_at, f.facts, f.error
		FROM host_facts f JOIN hosts h ON h.host_id = f.host_id`)
	if err != nil {
		return nil, false, err
	}
	m := map[string]NodeFacts{}
	var cluster, node, problem string
	var at *time.Time
	var raw []byte
	_, err = pgx.ForEachRow(rows, []any{&cluster, &node, &at, &raw, &problem}, func() error {
		f := NodeFacts{Cluster: cluster, Node: node, Error: problem}
		if at != nil {
			f.At = at.UTC()
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &f.Facts); err != nil {
				return err
			}
		}
		if len(f.Facts) == 0 {
			f.Facts = nil
		}
		m[factKey(cluster, node)] = f
		return nil
	})
	return m, len(m) > 0, err
}

func writeFacts(ctx context.Context, tx pgx.Tx, old, next map[string]NodeFacts) error {
	if err := inventory.Lock(ctx, tx); err != nil {
		return err
	}
	keys := func(m map[string]NodeFacts) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for _, k := range keys(old) {
		if _, ok := next[k]; ok {
			continue
		}
		f := old[k]
		if _, err := tx.Exec(ctx, "DELETE FROM host_facts WHERE host_id IN (SELECT host_id FROM hosts WHERE coalesce(cluster_id, '') = $1 AND name = $2)", f.Cluster, f.Node); err != nil {
			return err
		}
	}
	for _, k := range keys(next) {
		f := next[k]
		if o, ok := old[k]; ok && reflect.DeepEqual(o, f) {
			continue
		}
		id, err := inventory.EnsureHost(ctx, tx, f.Cluster, f.Node)
		if err != nil {
			return err
		}
		var raw []byte
		if len(f.Facts) > 0 {
			raw, _ = json.Marshal(f.Facts)
		}
		var at *time.Time
		if !f.At.IsZero() {
			t := f.At.UTC()
			at = &t
		}
		if _, err := tx.Exec(ctx, `INSERT INTO host_facts (host_id, facts, read_at, error) VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id) DO UPDATE SET facts = excluded.facts, read_at = excluded.read_at, error = excluded.error`, id, raw, at, f.Error); err != nil {
			return err
		}
	}
	return inventory.Collect(ctx, tx)
}
