package cards

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Approvals' part of this package in the database (Phase 13, DB-25): the
// standing approvals as rows of approval_policies and the notification
// channels as rows of notification_channels, through db.Doc, an interim
// step until the store is given repositories of its own.

// NewStandingPG keeps the standing approvals in d.
func NewStandingPG(d *db.DB) *db.Doc[standingRecord] {
	return &db.Doc[standingRecord]{DB: d, Key: StandingKey, Read: readStanding, Write: writeStanding}
}

func readStanding(ctx context.Context, q db.Querier) (standingRecord, bool, error) {
	rows, err := q.Query(ctx, `SELECT approval_policy_id, name, is_enabled, agent_id, cluster_ids, actions, namespaces, condition, per_hour,
			run_as_user, run_as_groups, updated_by, updated_by_key, updated_by_kind, created_at, updated_at, applied_at
		FROM approval_policies ORDER BY position, approval_policy_id`)
	if err != nil {
		return standingRecord{}, false, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Standing, error) {
		var st Standing
		var created, updated *time.Time
		err := row.Scan(&st.ID, &st.Name, &st.On, &st.Agent, &st.Clusters, &st.Actions, &st.Namespaces, &st.Condition, &st.PerHour,
			&st.RunAs.User, &st.RunAs.Groups, &st.By.Name, &st.By.Key, &st.By.Kind, &created, &updated, &st.Applied)
		if created != nil {
			st.Created = created.UTC()
		}
		if updated != nil {
			st.Updated = updated.UTC()
		}
		for i := range st.Applied {
			st.Applied[i] = st.Applied[i].UTC()
		}
		if len(st.Namespaces) == 0 {
			st.Namespaces = nil
		}
		if len(st.RunAs.Groups) == 0 {
			st.RunAs.Groups = nil
		}
		if len(st.Applied) == 0 {
			st.Applied = nil
		}
		return st, err
	})
	if len(list) == 0 {
		return standingRecord{}, false, err
	}
	return standingRecord{Standing: list}, true, err
}

// list is a slice as an array column takes it: never NULL.
func list[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func writeStanding(ctx context.Context, tx pgx.Tx, old, next standingRecord) error {
	place := map[string]int{}
	for i, st := range next.Standing {
		place[st.ID] = i
	}
	// The place in the list is a column, so a record that only moved is
	// written too.
	was := map[string]int{}
	for i, st := range old.Standing {
		was[st.ID] = i
	}
	put := func(st Standing) error {
		_, err := tx.Exec(ctx, `INSERT INTO approval_policies (approval_policy_id, position, name, is_enabled, agent_id, cluster_ids, actions, namespaces,
				condition, per_hour, run_as_user, run_as_groups, updated_by, updated_by_key, updated_by_kind, created_at, updated_at, applied_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (approval_policy_id) DO UPDATE SET position = excluded.position, name = excluded.name, is_enabled = excluded.is_enabled,
				agent_id = excluded.agent_id, cluster_ids = excluded.cluster_ids, actions = excluded.actions, namespaces = excluded.namespaces,
				condition = excluded.condition, per_hour = excluded.per_hour, run_as_user = excluded.run_as_user, run_as_groups = excluded.run_as_groups,
				updated_by = excluded.updated_by, updated_by_key = excluded.updated_by_key, updated_by_kind = excluded.updated_by_kind,
				created_at = excluded.created_at, updated_at = excluded.updated_at, applied_at = excluded.applied_at`,
			st.ID, place[st.ID], st.Name, st.On, st.Agent, list(st.Clusters), list(st.Actions), list(st.Namespaces), st.Condition, st.PerHour,
			st.RunAs.User, list(st.RunAs.Groups), st.By.Name, st.By.Key, st.By.Kind, pgTime(st.Created), pgTime(st.Updated), list(st.Applied))
		return err
	}
	for _, st := range next.Standing {
		if i, ok := was[st.ID]; ok && i != place[st.ID] {
			if _, err := tx.Exec(ctx, "UPDATE approval_policies SET position = $2 WHERE approval_policy_id = $1", st.ID, place[st.ID]); err != nil {
				return err
			}
		}
	}
	return db.Diff(old.Standing, next.Standing, func(st Standing) string { return st.ID }, put,
		func(_, st Standing) error { return put(st) },
		func(st Standing) error {
			_, err := tx.Exec(ctx, "DELETE FROM approval_policies WHERE approval_policy_id = $1", st.ID)
			return err
		})
}

// pgTime is a time as a nullable column takes it.
func pgTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// notifyScope is the settings row with what the notifications share
// besides their channels: the link the messages carry.
const notifyScope = "notifications"

// NewNotifyPG keeps the notification channels in d.
func NewNotifyPG(d *db.DB) *db.Doc[NotifySettings] {
	return &db.Doc[NotifySettings]{DB: d, Key: NotifyKey, Read: readNotify, Write: writeNotify}
}

func readNotify(ctx context.Context, q db.Querier) (NotifySettings, bool, error) {
	var set NotifySettings
	b, _, err := db.LoadScope(ctx, q, notifyScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return set, false, err
	}
	if found {
		var s struct {
			Link string `json:"link"`
		}
		_ = json.Unmarshal(b, &s)
		set.Link = s.Link
	}
	rows, err := q.Query(ctx, "SELECT notification_channel_id, kind, name, is_enabled FROM notification_channels ORDER BY position, notification_channel_id")
	if err != nil {
		return set, false, err
	}
	set.Channels, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Channel, error) {
		var c Channel
		err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.On)
		return c, err
	})
	return set, found || len(set.Channels) > 0, err
}

func writeNotify(ctx context.Context, tx pgx.Tx, old, next NotifySettings) error {
	if next.Link != old.Link || (old.Channels == nil && old.Link == "") {
		b, _ := json.Marshal(map[string]string{"link": next.Link})
		if _, err := db.SaveScope(ctx, tx, notifyScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	// Few, and their order matters: written anew.
	if _, err := tx.Exec(ctx, "DELETE FROM notification_channels"); err != nil {
		return err
	}
	for i, c := range next.Channels {
		if _, err := tx.Exec(ctx, "INSERT INTO notification_channels (notification_channel_id, position, kind, name, is_enabled, secret_ref) VALUES ($1, $2, $3, $4, $5, $6)",
			c.ID, i, c.Kind, c.Name, c.On, c.ID); err != nil {
			return err
		}
	}
	return nil
}
