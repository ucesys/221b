package automation

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Automation's script groups and execution profiles in the database
// (Phase 13, DB-30): each group a row of script_groups, each profile a row
// of run_profiles with the fleets it may run on as rows of
// run_profile_fleets. The store is unchanged and reaches them through
// db.Doc.

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// NewGroupsPG keeps the script groups in d.
func NewGroupsPG(d *db.DB) *db.Doc[[]Group] {
	return &db.Doc[[]Group]{DB: d, Key: GroupsKey,
		Read: func(ctx context.Context, q db.Querier) ([]Group, bool, error) {
			rows, err := q.Query(ctx, "SELECT name, description FROM script_groups ORDER BY position, name")
			if err != nil {
				return nil, false, err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Group, error) {
				var g Group
				err := row.Scan(&g.Name, &g.Desc)
				return g, err
			})
			return list, len(list) > 0, err
		},
		// The list is short and its order is the page's: a group keeps its
		// row, which scripts will point at, by its name.
		Write: func(ctx context.Context, tx pgx.Tx, old, next []Group) error {
			if sameJSON(old, next) {
				return nil
			}
			names := make([]string, 0, len(next))
			for i, g := range next {
				names = append(names, g.Name)
				tag, err := tx.Exec(ctx, "UPDATE script_groups SET position = $2, description = $3 WHERE name = $1", g.Name, i, g.Desc)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					if _, err := tx.Exec(ctx, "INSERT INTO script_groups (script_group_id, position, name, description) VALUES ($1, $2, $3, $4)", db.NewID(), i, g.Name, g.Desc); err != nil {
						return err
					}
				}
			}
			// A group scripts are in stays, as it does in the store's list.
			_, err := tx.Exec(ctx, `DELETE FROM script_groups g WHERE NOT (g.name = ANY($1))
				AND NOT EXISTS (SELECT 1 FROM scripts s WHERE s.script_group_id = g.script_group_id)`, names)
			return err
		}}
}

// NewProfilesPG keeps the execution profiles in d.
func NewProfilesPG(d *db.DB) *db.Doc[[]RunProfile] {
	return &db.Doc[[]RunProfile]{DB: d, Key: ProfilesKey,
		Read: func(ctx context.Context, q db.Querier) ([]RunProfile, bool, error) {
			rows, err := q.Query(ctx, `SELECT p.run_profile_id, p.name, p.kind, p.description, p.privilege, p.namespace, p.service_account, p.is_built_in,
					coalesce((SELECT array_agg(f.fleet_key ORDER BY f.fleet_key) FROM run_profile_fleets f WHERE f.run_profile_id = p.run_profile_id), '{}')
				FROM run_profiles p ORDER BY p.position, p.run_profile_id`)
			if err != nil {
				return nil, false, err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RunProfile, error) {
				var p RunProfile
				err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.Desc, &p.Privilege, &p.Namespace, &p.ServiceAccount, &p.Builtin, &p.Fleets)
				if len(p.Fleets) == 0 {
					p.Fleets = nil
				}
				return p, err
			})
			return list, len(list) > 0, err
		},
		Write: func(ctx context.Context, tx pgx.Tx, old, next []RunProfile) error {
			if sameJSON(old, next) {
				return nil
			}
			ids := make([]string, 0, len(next))
			for i, p := range next {
				ids = append(ids, p.ID)
				if _, err := tx.Exec(ctx, `INSERT INTO run_profiles (run_profile_id, position, name, kind, description, privilege, namespace, service_account, is_built_in)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
					ON CONFLICT (run_profile_id) DO UPDATE SET position = excluded.position, name = excluded.name, kind = excluded.kind,
						description = excluded.description, privilege = excluded.privilege, namespace = excluded.namespace,
						service_account = excluded.service_account, is_built_in = excluded.is_built_in`,
					p.ID, i, p.Name, p.Kind, p.Desc, p.Privilege, p.Namespace, p.ServiceAccount, p.Builtin); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "DELETE FROM run_profile_fleets WHERE run_profile_id = $1", p.ID); err != nil {
					return err
				}
				for _, f := range p.Fleets {
					if _, err := tx.Exec(ctx, "INSERT INTO run_profile_fleets (run_profile_id, fleet_key) VALUES ($1, $2) ON CONFLICT DO NOTHING", p.ID, f); err != nil {
						return err
					}
				}
			}
			_, err := tx.Exec(ctx, "DELETE FROM run_profiles WHERE NOT (run_profile_id = ANY($1))", ids)
			return err
		}}
}
