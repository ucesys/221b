package automation

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The sensitive values and the database (DB-71). The three documents that
// can hold a variable's value (the variables, their changes with the runs
// that wait, and the runs) hide the sensitive ones in the transaction
// that writes their rows: the rows get the digests, then the Secret gets
// the values, then the transaction commits. A Secret that cannot be
// written leaves the rows as they were; a console that stops between the
// two leaves a value in the Secret that no row names. No statement sent
// to the database carries a sensitive value.

// SensitiveImportID is the name the move of the sensitive values out of
// the tables is recorded under.
const SensitiveImportID = "automation/sensitive-values"

// tableSensitive is where the documents of each database keep the
// sensitive values.
var (
	tableSensitive  sync.Map // *db.DB → SensitiveStore
	fallbackSecrets = NewMemorySensitive()
)

// UseSensitive has the documents kept in d keep the sensitive values in s.
func UseSensitive(d *db.DB, s SensitiveStore) { tableSensitive.Store(d, s) }

// sensitiveFor is where the documents kept in d keep the sensitive
// values: in memory when nothing was said (a test).
func sensitiveFor(d *db.DB) SensitiveStore {
	if s, ok := tableSensitive.Load(d); ok {
		return s.(SensitiveStore)
	}
	return fallbackSecrets
}

// SensitiveInTables tells the store that its documents are rows of d,
// which hide the sensitive values themselves and keep them in s.
func (st *Store) SensitiveInTables(d *db.DB, s SensitiveStore, log *slog.Logger) {
	UseSensitive(d, s)
	st.SetSensitive(s, true, log)
	st.decideIn = func(ctx context.Context, cluster string, id int, approve bool, comment string, who Who) (VariableChange, error) {
		return st.decideChangeTx(ctx, d, cluster, id, approve, comment, who)
	}
	st.collectIn = func(ctx context.Context, drop func(live map[string]map[string]bool) error) error {
		return d.Tx(ctx, func(tx pgx.Tx) error {
			if err := lockDocs(ctx, tx, VariablesKey, ChangesKey); err != nil {
				return err
			}
			vars, _, err := readVariables(ctx, tx)
			if err != nil {
				return err
			}
			changes, _, err := readChanges(ctx, tx, false)
			if err != nil {
				return err
			}
			return drop(liveDigests(&vars, &changes))
		})
	}
}

// lockDocs takes the turn of the writers of each document, in the order
// given: always the variables, then their changes, then the runs.
func lockDocs(ctx context.Context, tx pgx.Tx, keys ...string) error {
	for _, k := range keys {
		if err := db.LockDoc(ctx, tx, k); err != nil {
			return err
		}
	}
	return nil
}

// namesIn gives the names sensitive on each cluster, as the rows say.
func namesIn(ctx context.Context, q db.Querier) (func(cluster string) map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT coalesce(f.cluster_id, ''), v.name FROM variables v JOIN fleets f ON f.fleet_id = v.fleet_id WHERE v.is_sensitive`)
	if err != nil {
		return nil, err
	}
	all := map[string]map[string]bool{}
	var cluster, name string
	if _, err := pgx.ForEachRow(rows, []any{&cluster, &name}, func() error {
		if all[cluster] == nil {
			all[cluster] = map[string]bool{}
		}
		all[cluster][name] = true
		return nil
	}); err != nil {
		return nil, err
	}
	return func(cluster string) map[string]bool { return all[cluster] }, nil
}

// pastPlain adds to plain the values the changes and the runs of the past
// hold in clear under a name that is sensitive, so that the same value is
// found under any other name too: a script's parameter bound to the
// variable recorded it under the parameter's name.
func pastPlain(changes *changesRecord, runs *runsRecord, names func(cluster string) map[string]bool, only string, plain map[string]map[string]bool) {
	saw := func(cluster, name, v string, marked bool) {
		if cluster != only || IsHidden(v) || v == "" || !(marked || names(cluster)[name]) {
			return
		}
		if plain[cluster] == nil {
			plain[cluster] = map[string]bool{}
		}
		plain[cluster][v] = true
	}
	for _, c := range changes.Changes {
		marks := changeMarks(c)
		for i, fp := range []FleetVariables{c.Before, c.After} {
			for name, v := range fp.Values {
				// What a waiting change found under a name it would mark is
				// still what the variable reads as.
				saw(c.Cluster, name, v, marks[name] && (i == 1 || c.State != ChangeWaiting))
			}
			for _, m := range fp.Nodes {
				for name, v := range m {
					saw(c.Cluster, name, v, marks[name] && (i == 1 || c.State != ChangeWaiting))
				}
			}
		}
	}
	for _, w := range changes.Runs {
		for name, v := range w.Params {
			saw(w.Cluster, name, Constant(v), false)
		}
	}
	for _, r := range runs.Runs {
		for name, v := range r.Overrides {
			saw(r.Cluster, name, Constant(v), false)
		}
		for _, n := range r.Nodes {
			for name, v := range n.Values {
				saw(r.Cluster, name, v, false)
			}
		}
		for _, f := range r.Fleets {
			for name, v := range f.Values {
				saw(r.Cluster, name, v, false)
			}
		}
	}
}

// sealPastTables hides, in the changes, the runs that wait, the runs and
// the audit log's rows of Automation, the values of the names sensitive
// now and the values among s.plain; only names the clusters it does it
// for, every cluster when empty. What a waiting change or run would use
// is added to s, to be kept. It answers how many records it changed. The
// caller holds the turn of the variables' writers; this takes the
// changes' and the runs'.
func sealPastTables(ctx context.Context, tx pgx.Tx, r *ring, s *sealing, only string) (int, error) {
	if err := lockDocs(ctx, tx, ChangesKey, runsTurn); err != nil {
		return 0, err
	}
	all, err := namesIn(ctx, tx)
	if err != nil {
		return 0, err
	}
	names := func(cluster string) map[string]bool {
		if only != "" && cluster != only {
			return nil
		}
		return all(cluster)
	}
	changes, _, err := readChanges(ctx, tx, true)
	if err != nil {
		return 0, err
	}
	runs, _, err := readRuns(ctx, tx, true)
	if err != nil {
		return 0, err
	}
	if only != "" {
		pastPlain(&changes, &runs, names, only, s.plain)
	} else {
		// A save of the variables: the clusters on which a name became
		// sensitive with it are looked through as the move does, so a value
		// recorded under the name is found under any other name too.
		for _, cluster := range sortedClusters(s.became) {
			pastPlain(&changes, &runs, names, cluster, s.plain)
		}
	}
	changed := 0
	nextChanges := clone(changes)
	more, err := sealChanges(r, &nextChanges, names, s.plain)
	if err != nil {
		return 0, err
	}
	for cluster, m := range more.add {
		for d, v := range m {
			s.keep(cluster, d, v)
		}
	}
	if !sameJSON(changes, nextChanges) {
		changed += differing(changes.Changes, nextChanges.Changes) + differing(changes.Runs, nextChanges.Runs)
		if err := writeChangesRows(ctx, tx, changes, nextChanges); err != nil {
			return 0, err
		}
	}
	nextRuns := clone(runs)
	if err := sealRuns(r, &nextRuns, names, s.plain); err != nil {
		return 0, err
	}
	if !sameJSON(runs, nextRuns) {
		changed += differing(runs.Runs, nextRuns.Runs)
		if err := writeRunsRows(ctx, tx, runs, nextRuns); err != nil {
			return 0, err
		}
	}
	n, err := redactAudit(ctx, tx, s.plain)
	if err != nil {
		return 0, err
	}
	m, err := redactOutputs(ctx, tx, s.plain)
	return changed + n + m, err
}

// redactOutputs blanks, in the runs' whole outputs (DB-87), the values
// among plain on each one's cluster, as sealRuns blanks them in the
// tails. The texts are compared here, so no statement carries a value.
func redactOutputs(ctx context.Context, tx pgx.Tx, plain map[string]map[string]bool) (int, error) {
	n := 0
	for _, cluster := range sortedClusters(plain) {
		values := plain[cluster]
		if len(values) == 0 {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT o.script_run_host_id::text, o.output FROM script_run_outputs o JOIN script_run_hosts h USING (script_run_host_id)
			JOIN script_runs x USING (script_run_id) WHERE x.cluster_id = $1`, cluster)
		if err != nil {
			return 0, err
		}
		masked := map[string]string{}
		var id, text string
		if _, err := pgx.ForEachRow(rows, []any{&id, &text}, func() error {
			if m := maskPlain(text, values); m != text {
				masked[id] = m
			}
			return nil
		}); err != nil {
			return 0, err
		}
		for id, text := range masked {
			if _, err := tx.Exec(ctx, "UPDATE script_run_outputs SET output = $2 WHERE script_run_host_id = $1", id, text); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

func differing[T any](a, b []T) int {
	n := 0
	for i := range a {
		if i < len(b) && !sameJSON(a[i], b[i]) {
			n++
		}
	}
	return n
}

// redactAudit replaces the audit log's rows of Automation that quote one
// of the values given: a row that quotes one is written again with the
// value blanked, under a new id and with everything else as it was, and
// the row that quoted it is deleted. The ledger refuses a change of a
// row, and this is not one: nothing but the quoted value differs. The
// texts are compared here, so no statement carries a value.
func redactAudit(ctx context.Context, tx pgx.Tx, plain map[string]map[string]bool) (int, error) {
	n := 0
	for _, cluster := range sortedClusters(plain) {
		values := plain[cluster]
		long := false
		for v := range values {
			long = long || len(v) >= minMasked
		}
		if !long {
			continue
		}
		rows, err := tx.Query(ctx, "SELECT audit_event_id::text, target, detail FROM audit_events WHERE cluster_id = $1 AND action LIKE 'automation.%'", cluster)
		if err != nil {
			return n, err
		}
		type fix struct{ id, target, detail string }
		var fixes []fix
		var id, target, detail string
		if _, err := pgx.ForEachRow(rows, []any{&id, &target, &detail}, func() error {
			if t, d := maskPlain(target, values), maskPlain(detail, values); t != target || d != detail {
				fixes = append(fixes, fix{id, t, d})
			}
			return nil
		}); err != nil {
			return n, err
		}
		for _, f := range fixes {
			if _, err := tx.Exec(ctx, `INSERT INTO audit_events (audit_event_id, at, actor, actor_subject, actor_email, action, cluster_id, target, outcome, detail, source_ip, restored_at)
				SELECT $2, at, actor, actor_subject, actor_email, action, cluster_id, $3, outcome, $4, source_ip, restored_at FROM audit_events WHERE audit_event_id = $1`,
				f.id, db.NewID(), f.target, f.detail); err != nil {
				return n, err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM audit_events WHERE audit_event_id = $1", f.id); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// MoveSensitive moves the sensitive values that are still in the tables
// into the Secrets, and puts their digests in their place (DB-71): the
// values of the variables whose name a fleet of their cluster marks
// sensitive, and, in the changes and the runs of the past, the values
// under those names, the values of a name a change itself marked, and
// any of those values where it stands under another name or is quoted by
// a run's output or the audit log's rows of Automation. It is what the
// start after step 0038 does, and what any later start does again when
// it finds such a value (a database restored to an earlier time).
//
// A cluster is one transaction: its rows are changed, its Secret is
// written, and the transaction commits, so a value leaves its row only
// when the Secret keeps it; a Secret that cannot be written leaves the
// cluster's rows as they were, said in the log by the variables' names,
// never their values, and the next start tries again. moved is how many
// records were changed, left how many clusters were left.
func MoveSensitive(ctx context.Context, d *db.DB, s SensitiveStore, log *slog.Logger) (moved, left int, err error) {
	if log == nil {
		log = slog.Default()
	}
	var clusters []string
	err = d.Snapshot(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT DISTINCT coalesce(f.cluster_id, '') FROM variables v JOIN fleets f ON f.fleet_id = v.fleet_id WHERE v.is_sensitive
			UNION SELECT DISTINCT c.cluster_id FROM variable_changes c JOIN variable_change_items i USING (variable_change_id) WHERE i.kind = 'sensitive'`)
		if err != nil {
			return err
		}
		var cluster string
		_, err = pgx.ForEachRow(rows, []any{&cluster}, func() error {
			clusters = append(clusters, cluster)
			return nil
		})
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	sort.Strings(clusters)
	for _, cluster := range clusters {
		var names []string
		n := 0
		err := d.Tx(ctx, func(tx pgx.Tx) error {
			names, n = nil, 0
			if err := lockDocs(ctx, tx, VariablesKey); err != nil {
				return err
			}
			r := newRing(ctx, s)
			sl := newSealing()
			rows, err := tx.Query(ctx, `SELECT v.variable_id::text, v.name, v.value, coalesce(f.fleet_key, ''), coalesce(h.name, '')
				FROM variables v LEFT JOIN fleets f ON f.fleet_id = v.fleet_id LEFT JOIN hosts h ON h.host_id = v.host_id
				WHERE v.value IS NOT NULL AND coalesce(f.cluster_id, h.cluster_id, '') = $1 AND v.name IN (
					SELECT s.name FROM variables s JOIN fleets sf ON sf.fleet_id = s.fleet_id WHERE s.is_sensitive AND coalesce(sf.cluster_id, '') = $1)
				ORDER BY v.name, f.fleet_key, h.name`, cluster)
			if err != nil {
				return err
			}
			type row struct{ id, name, value, digest string }
			var todo []row
			var id, name, value, fleet, node string
			if _, err := pgx.ForEachRow(rows, []any{&id, &name, &value, &fleet, &node}, func() error {
				todo = append(todo, row{id: id, name: name, value: value})
				where := "fleet " + fleet
				if node != "" {
					where = "node " + node
				}
				names = append(names, name+" on "+where)
				return nil
			}); err != nil {
				return err
			}
			for i, t := range todo {
				digest, err := r.digest(cluster, t.name, t.value)
				if err != nil {
					return err
				}
				sl.keep(cluster, digest, t.value)
				sl.saw(cluster, t.value)
				todo[i].digest = digest
			}
			for _, t := range todo {
				if _, err := tx.Exec(ctx, "UPDATE variables SET value = NULL, value_digest = $2 WHERE variable_id = $1", t.id, t.digest); err != nil {
					return err
				}
			}
			past, err := sealPastTables(ctx, tx, r, sl, cluster)
			if err != nil {
				return err
			}
			n = len(todo) + past
			// The rows are changed; the Secret now, and then the commit.
			return sl.put(ctx, s)
		})
		if err != nil {
			left++
			log.Warn("the sensitive values of a cluster could not be moved out of the database into their Secret; they are left where they were, nothing reads them, and the next start tries again",
				"cluster", cluster, "variables", strings.Join(names, ", "), "err", err)
			continue
		}
		moved += n
		if n > 0 {
			log.Info("moved the sensitive values of a cluster out of the database into their Secret; the tables keep their digests", "cluster", cluster, "records", n)
		}
	}
	if left > 0 {
		return moved, left, fmt.Errorf("the sensitive values of %d clusters are still in the database", left)
	}
	return moved, 0, nil
}

// Unmatched compares the rows with the Secrets, for the log of a start:
// the variables whose value the Secret does not keep, by cluster and
// name, never by value, and how many values each cluster's Secret keeps
// that no row names. Neither is changed: a value without a row is kept,
// and a row without a value reads as not set.
func Unmatched(ctx context.Context, d *db.DB, s SensitiveStore) (noValue []string, noRow map[string]int, err error) {
	held, err := s.Held(ctx)
	if err != nil {
		return nil, nil, err
	}
	kept := map[string]map[string]bool{}
	for cluster, ds := range held {
		kept[cluster] = map[string]bool{}
		for _, x := range ds {
			kept[cluster][x] = true
		}
	}
	var live map[string]map[string]bool
	err = d.Snapshot(ctx, func(q db.Querier) error {
		vars, _, err := readVariables(ctx, q)
		if err != nil {
			return err
		}
		changes, _, err := readChanges(ctx, q, false)
		if err != nil {
			return err
		}
		live = liveDigests(&vars, &changes)
		for _, cluster := range sortedClusters(vars.Clusters) {
			_ = eachValue(vars.Clusters[cluster], func(p place, v string) (string, error) {
				if IsHidden(v) && v != Unmoved && !kept[cluster][v] {
					where := "fleet " + p.fleet
					if p.node != "" {
						where = "node " + p.node
					}
					noValue = append(noValue, fmt.Sprintf("%s on %s of cluster %s", p.name, where, cluster))
				}
				return v, nil
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	noRow = map[string]int{}
	for cluster, ds := range kept {
		for x := range ds {
			if !live[cluster][x] {
				noRow[cluster]++
			}
		}
	}
	return noValue, noRow, nil
}
