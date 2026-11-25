package automation

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// decideChangeTx decides a variable change in one transaction of d: with
// the turns of the variables' and the changes' writers taken, it reads
// both, checks the change (waiting, not the decider's own when approved,
// not overtaken), applies what an approval sets and marks the decision,
// so a concurrent rejection, approval or newer change waits for it and
// then finds it decided (the review of November 24, 2026, finding 5).
func (st *Store) decideChangeTx(ctx context.Context, d *db.DB, cluster string, id int, approve bool, comment string, who Who) (VariableChange, error) {
	var out VariableChange
	was := map[string]map[string]bool{}
	sens := sensitiveFor(d)
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		was = map[string]map[string]bool{}
		if err := lockDocs(ctx, tx, VariablesKey, ChangesKey); err != nil {
			return err
		}
		changes, _, err := readChanges(ctx, tx, false)
		if err != nil {
			return err
		}
		var c VariableChange
		if approve {
			if c, err = approvable(changes, cluster, id, who); err != nil {
				return err
			}
			vars, _, err := readVariables(ctx, tx)
			if err != nil {
				return err
			}
			next := clone(vars)
			set, err := approveOver(&next, c)
			if err != nil {
				return err
			}
			if set {
				// The values it sets are the asker's (SetBy).
				if err := writeVariables(SetBy(ctx, c.By), tx, sens, vars, next); err != nil {
					return err
				}
				merge(was, liveIn(&vars))
				if err := d.Notify(ctx, tx, "doc/"+VariablesKey); err != nil {
					return err
				}
				// A mark it puts on hides the past changes' values as well
				// (sealPastTables): the decision is marked over them.
				merge(was, liveIn(&changes))
				if changes, _, err = readChanges(ctx, tx, false); err != nil {
					return err
				}
			}
		}
		next := clone(changes)
		found := false
		for i := range next.Changes {
			x := &next.Changes[i]
			if x.ID != id || x.Cluster != cluster {
				continue
			}
			if err := st.decide(x, approve, comment, who); err != nil {
				return err
			}
			found, out = true, *x
		}
		if !found {
			return ErrNotFound
		}
		if err := writeChanges(ctx, tx, sens, changes, next); err != nil {
			return err
		}
		merge(was, liveIn(&changes))
		return d.Notify(ctx, tx, "doc/"+ChangesKey)
	})
	if err != nil {
		return VariableChange{}, err
	}
	// What the change waited with leaves the Secret, unless something else
	// still names it (DB-71).
	st.collect(ctx, gone(was, nil))
	return out, nil
}

// merge adds the digests of b to a, by cluster.
func merge(a, b map[string]map[string]bool) {
	for cluster, ds := range b {
		if a[cluster] == nil {
			a[cluster] = map[string]bool{}
		}
		for x := range ds {
			a[cluster][x] = true
		}
	}
}
