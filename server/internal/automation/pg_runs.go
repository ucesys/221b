package automation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Automation's runs in the database (Phase 13, DB-35): a run is a row of
// script_runs, each of its nodes a row of script_run_hosts, the values the
// run gave itself and the values each node got rows of script_run_values,
// and who had approved the version rows of script_run_approvers. A run
// names its script's version, its profile, its fleet and the run it
// retries as its document did, and keys to their rows while they exist.
//
// The store is unchanged and reaches the rows through db.Doc, which reads
// every kept run for each save, as the ConfigMap's document was read: a
// repository that writes one node's row at a time is the rework due.
// script_run_fleets and script_run_host_fleets stay empty until the engine
// records a run's fleets (the code to-do list, item 11).

const runsScope = "automation-runs"

// NewRunsPG keeps the runs in d.
func NewRunsPG(d *db.DB) *db.Doc[runsRecord] {
	return &db.Doc[runsRecord]{DB: d, Key: RunsKey, Read: readRuns, Write: writeRuns}
}

func readRuns(ctx context.Context, q db.Querier) (runsRecord, bool, error) {
	rec := runsRecord{Runs: []Run{}}
	b, _, err := db.LoadScope(ctx, q, runsScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return rec, false, err
	}
	if found {
		var c struct {
			Next int `json:"next"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return rec, false, err
		}
		rec.Next = c.Next
	}
	at := map[string]int{}
	rows, err := q.Query(ctx, `SELECT script_run_id::text, run_number, script_name, version_number, version_hash, is_dry_run, privilege, cluster_id,
			cluster_name, fleet_key, fleet_name, rollout_mode, rollout_size, rollout_stop_after, rollout_timeout_minutes, started_by, started_by_user,
			state, target, coalesce(run_profile_id, ''), identity, basis, hook_names, hooks_before, hooks_after, coalesce(retry_of_number, 0), note,
			request_key, is_cancel_asked, values_request_number, values_approved_by, values_approved_by_user, values_approved_at,
			started_at, ended_at, heartbeat_at
		FROM script_runs ORDER BY run_number`)
	if err != nil {
		return rec, false, err
	}
	var id string
	var r Run
	var hooks, before, after []string
	var request *int
	var by, byUser *string
	var approved, started, ended, beat *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &r.ID, &r.Script, &r.Version, &r.Hash, &r.DryRun, &r.Privilege, &r.Cluster, &r.ClusterName, &r.Fleet,
		&r.FleetName, &r.Rollout.Mode, &r.Rollout.Size, &r.Rollout.StopAfter, &r.Rollout.TimeoutMinutes, &r.By.Name, &r.By.User, &r.State, &r.Target,
		&r.Config, &r.Identity, &r.Basis, &hooks, &before, &after, &r.RetryOf, &r.Note, &r.Request, &r.CancelAsked, &request, &by, &byUser, &approved,
		&started, &ended, &beat}, func() error {
		x := r
		// The row's own slices: the next row is read into the same variables.
		list := func(s []string) []string {
			if len(s) == 0 {
				return nil
			}
			return append([]string(nil), s...)
		}
		x.Hooks, x.HookRefs, x.Nodes = list(hooks), HookRefs{Before: list(before), After: list(after)}, []NodeResult{}
		x.Started, x.Beat = timeOf(started), timeOf(beat)
		if ended != nil {
			t := ended.UTC()
			x.Ended = &t
		}
		if request != nil {
			va := ValuesApproval{Request: *request, At: timeOf(approved)}
			if by != nil {
				va.By.Name = *by
			}
			if byUser != nil {
				va.By.User = *byUser
			}
			x.ValuesApproved = &va
		}
		at[id] = len(rec.Runs)
		rec.Runs = append(rec.Runs, x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	type place struct{ run, node int }
	nodes := map[string]place{}
	rows, err = q.Query(ctx, `SELECT script_run_host_id::text, script_run_id::text, host_name, batch, state, exit_code, message, error, started_at,
			ended_at, output_tail, coalesce(reboot_minutes, 0), is_cordoned
		FROM script_run_hosts ORDER BY script_run_id, position, host_name`)
	if err != nil {
		return rec, false, err
	}
	var hid string
	var n NodeResult
	var exit *int
	if _, err := pgx.ForEachRow(rows, []any{&hid, &id, &n.Node, &n.Batch, &n.State, &exit, &n.Msg, &n.Error, &started, &ended, &n.Tail, &n.RebootMinutes, &n.Cordoned}, func() error {
		i, ok := at[id]
		if !ok {
			return nil
		}
		x := n
		if exit != nil {
			e := *exit
			x.Exit = &e
		}
		if started != nil {
			t := started.UTC()
			x.Started = &t
		}
		if ended != nil {
			t := ended.UTC()
			x.Ended = &t
		}
		nodes[hid] = place{i, len(rec.Runs[i].Nodes)}
		rec.Runs[i].Nodes = append(rec.Runs[i].Nodes, x)
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, "SELECT script_run_id::text, coalesce(script_run_host_id::text, ''), scope, name, value FROM script_run_values WHERE scope IN ('run', 'host')")
	if err != nil {
		return rec, false, err
	}
	var scope, name, value string
	if _, err := pgx.ForEachRow(rows, []any{&id, &hid, &scope, &name, &value}, func() error {
		if scope == "run" {
			if i, ok := at[id]; ok {
				if rec.Runs[i].Overrides == nil {
					rec.Runs[i].Overrides = map[string]string{}
				}
				rec.Runs[i].Overrides[name] = value
			}
			return nil
		}
		if p, ok := nodes[hid]; ok {
			node := &rec.Runs[p.run].Nodes[p.node]
			if node.Values == nil {
				node.Values = map[string]string{}
			}
			node.Values[name] = value
		}
		return nil
	}); err != nil {
		return rec, false, err
	}

	rows, err = q.Query(ctx, "SELECT script_run_id::text, name, user_name FROM script_run_approvers ORDER BY script_run_id, position")
	if err != nil {
		return rec, false, err
	}
	var who Who
	if _, err := pgx.ForEachRow(rows, []any{&id, &who.Name, &who.User}, func() error {
		if i, ok := at[id]; ok {
			rec.Runs[i].Approvers = append(rec.Runs[i].Approvers, who)
		}
		return nil
	}); err != nil {
		return rec, false, err
	}
	return rec, found || len(rec.Runs) > 0, nil
}

func writeRuns(ctx context.Context, tx pgx.Tx, old, next runsRecord) error {
	if next.Next != old.Next {
		b, _ := json.Marshal(map[string]int{"next": next.Next})
		if _, err := db.SaveScope(ctx, tx, runsScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	before := map[int]Run{}
	for _, r := range old.Runs {
		before[r.ID] = r
	}
	numbers := make([]int, 0, len(next.Runs))
	for _, r := range next.Runs {
		numbers = append(numbers, r.ID)
	}
	// First, so that a run that goes frees its nodes' names and its number.
	if _, err := tx.Exec(ctx, "DELETE FROM script_runs WHERE NOT (run_number = ANY($1))", numbers); err != nil {
		return err
	}
	for _, r := range next.Runs {
		was, had := before[r.ID]
		if had && sameJSON(was, r) {
			continue
		}
		if err := writeRun(ctx, tx, was, r, had); err != nil {
			return err
		}
	}
	return nil
}

func writeRun(ctx context.Context, tx pgx.Tx, was, r Run, had bool) error {
	list := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	var config *string
	if r.Config != "" {
		config = &r.Config
	}
	var retry *int
	if r.RetryOf != 0 {
		retry = &r.RetryOf
	}
	var request *int
	var by, byUser *string
	var approved, ended *time.Time
	if va := r.ValuesApproved; va != nil {
		request, by, byUser, approved = &va.Request, &va.By.Name, &va.By.User, timeOrNil(va.At)
	}
	if r.Ended != nil {
		ended = timeOrNil(*r.Ended)
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO script_runs (script_run_id, run_number, script_name, version_number, version_hash, script_version_id,
			is_dry_run, privilege, cluster_id, cluster_name, fleet_key, fleet_name, fleet_id, rollout_mode, rollout_size, rollout_stop_after,
			rollout_timeout_minutes, started_by, started_by_user, state, target, run_profile_id, identity, basis, hook_names, hooks_before, hooks_after,
			retry_of_number, retry_of_script_run_id, note, request_key, is_cancel_asked, values_request_number, values_approved_by,
			values_approved_by_user, values_approved_at, started_at, ended_at, heartbeat_at)
		VALUES ($1, $2, $3, $4, $5,
			(SELECT v.script_version_id FROM script_versions v JOIN scripts s USING (script_id) WHERE s.name = $3 AND v.version_number = $4),
			$6, $7, $8, $9, $10, $11,
			(SELECT f.fleet_id FROM fleets f WHERE coalesce(f.cluster_id, '') = $8 AND f.fleet_key = $10),
			$12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26,
			(SELECT o.script_run_id FROM script_runs o WHERE o.run_number = $26),
			$27, $28, $29, $30, $31, $32, $33, $34, $35, $36)
		ON CONFLICT (run_number) DO UPDATE SET script_name = excluded.script_name, version_number = excluded.version_number,
			version_hash = excluded.version_hash, script_version_id = excluded.script_version_id, is_dry_run = excluded.is_dry_run,
			privilege = excluded.privilege, cluster_id = excluded.cluster_id, cluster_name = excluded.cluster_name, fleet_key = excluded.fleet_key,
			fleet_name = excluded.fleet_name, fleet_id = excluded.fleet_id, rollout_mode = excluded.rollout_mode, rollout_size = excluded.rollout_size,
			rollout_stop_after = excluded.rollout_stop_after, rollout_timeout_minutes = excluded.rollout_timeout_minutes,
			started_by = excluded.started_by, started_by_user = excluded.started_by_user, state = excluded.state, target = excluded.target,
			run_profile_id = excluded.run_profile_id, identity = excluded.identity, basis = excluded.basis, hook_names = excluded.hook_names,
			hooks_before = excluded.hooks_before, hooks_after = excluded.hooks_after, retry_of_number = excluded.retry_of_number,
			retry_of_script_run_id = excluded.retry_of_script_run_id, note = excluded.note, request_key = excluded.request_key,
			is_cancel_asked = excluded.is_cancel_asked, values_request_number = excluded.values_request_number,
			values_approved_by = excluded.values_approved_by, values_approved_by_user = excluded.values_approved_by_user,
			values_approved_at = excluded.values_approved_at, started_at = excluded.started_at, ended_at = excluded.ended_at,
			heartbeat_at = excluded.heartbeat_at
		RETURNING script_run_id::text`,
		db.NewID(), r.ID, r.Script, r.Version, r.Hash, r.DryRun, r.Privilege, r.Cluster, r.ClusterName, r.Fleet, r.FleetName,
		r.Rollout.Mode, r.Rollout.Size, r.Rollout.StopAfter, r.Rollout.TimeoutMinutes, r.By.Name, r.By.User, r.State, r.Target, config, r.Identity,
		r.Basis, list(r.Hooks), list(r.HookRefs.Before), list(r.HookRefs.After), retry, r.Note, r.Request, r.CancelAsked, request, by, byUser,
		approved, timeOrNil(r.Started), ended, timeOrNil(r.Beat)).Scan(&id); err != nil {
		return err
	}

	if !had || !sameJSON(was.Approvers, r.Approvers) {
		if _, err := tx.Exec(ctx, "DELETE FROM script_run_approvers WHERE script_run_id = $1", id); err != nil {
			return err
		}
		for i, a := range r.Approvers {
			if _, err := tx.Exec(ctx, "INSERT INTO script_run_approvers (script_run_id, position, name, user_name) VALUES ($1, $2, $3, $4)", id, i, a.Name, a.User); err != nil {
				return err
			}
		}
	}
	values := func(scope string, host *string, m map[string]string) error {
		for name, v := range m {
			if _, err := tx.Exec(ctx, "INSERT INTO script_run_values (script_run_value_id, script_run_id, scope, script_run_host_id, name, value) VALUES ($1, $2, $3, $4, $5, $6)",
				db.NewID(), id, scope, host, name, v); err != nil {
				return err
			}
		}
		return nil
	}
	if !had || !sameJSON(was.Overrides, r.Overrides) {
		if _, err := tx.Exec(ctx, "DELETE FROM script_run_values WHERE script_run_id = $1 AND scope = 'run'", id); err != nil {
			return err
		}
		if err := values("run", nil, r.Overrides); err != nil {
			return err
		}
	}

	type seen struct {
		node  NodeResult
		place int
	}
	kept := map[string]seen{}
	for i, n := range was.Nodes {
		kept[n.Node] = seen{n, i}
	}
	names := make([]string, 0, len(r.Nodes))
	for _, n := range r.Nodes {
		names = append(names, n.Node)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM script_run_hosts WHERE script_run_id = $1 AND NOT (host_name = ANY($2))", id, names); err != nil {
		return err
	}
	for i, n := range r.Nodes {
		o, known := kept[n.Node]
		if had && known && o.place == i && sameJSON(o.node, n) {
			continue
		}
		var minutes *int
		if n.RebootMinutes > 0 {
			minutes = &n.RebootMinutes
		}
		var started, ended *time.Time
		if n.Started != nil {
			started = timeOrNil(*n.Started)
		}
		if n.Ended != nil {
			ended = timeOrNil(*n.Ended)
		}
		var host string
		if err := tx.QueryRow(ctx, `INSERT INTO script_run_hosts (script_run_host_id, script_run_id, host_id, host_name, position, batch, state, exit_code,
				message, error, started_at, ended_at, output_tail, reboot_minutes, is_cordoned)
			VALUES ($1, $2, (SELECT h.host_id FROM hosts h WHERE coalesce(h.cluster_id, '') = $3 AND h.name = $4), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (script_run_id, host_name) DO UPDATE SET host_id = excluded.host_id, position = excluded.position, batch = excluded.batch,
				state = excluded.state, exit_code = excluded.exit_code, message = excluded.message, error = excluded.error,
				started_at = excluded.started_at, ended_at = excluded.ended_at, output_tail = excluded.output_tail,
				reboot_minutes = excluded.reboot_minutes, is_cordoned = excluded.is_cordoned
			RETURNING script_run_host_id::text`,
			db.NewID(), id, r.Cluster, n.Node, i, n.Batch, n.State, n.Exit, n.Msg, n.Error, started, ended, n.Tail, minutes, n.Cordoned).Scan(&host); err != nil {
			return err
		}
		if had && known && sameJSON(o.node.Values, n.Values) {
			continue
		}
		if _, err := tx.Exec(ctx, "DELETE FROM script_run_values WHERE script_run_host_id = $1", host); err != nil {
			return err
		}
		if err := values("host", &host, n.Values); err != nil {
			return err
		}
	}
	return nil
}
