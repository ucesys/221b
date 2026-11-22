package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The settings table (Platform): one row per scope, a JSON document read
// whole and written rarely (model rule 14). Each scope belongs to the
// package that reads it; what is here is the reading and the writing,
// with the row's version to save against.

// AnyVersion saves a scope whatever version it is at.
const AnyVersion = -1

// LoadScope returns a scope's document and its version. A scope never
// saved is ErrNotFound, at version 0.
func LoadScope(ctx context.Context, q Querier, scope string) ([]byte, int, error) {
	var b []byte
	var version int
	err := q.QueryRow(ctx, "SELECT value, version FROM settings WHERE scope = $1", scope).Scan(&b, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	return b, version, err
}

// SaveScope writes a scope's document when the row is still at version
// (0 for a scope never saved, AnyVersion for whatever it is at), and
// returns the new version; ErrConflict when another writer came first.
func SaveScope(ctx context.Context, q Querier, scope string, value []byte, version int, by string) (int, error) {
	var next int
	var err error
	switch version {
	case AnyVersion:
		err = q.QueryRow(ctx, `INSERT INTO settings (scope, value, version, updated_by, updated_at) VALUES ($1, $2, 1, $3, now())
			ON CONFLICT (scope) DO UPDATE SET value = excluded.value, version = settings.version + 1,
				updated_by = excluded.updated_by, updated_at = now()
			RETURNING version`, scope, value, by).Scan(&next)
	case 0:
		err = q.QueryRow(ctx, `INSERT INTO settings (scope, value, version, updated_by, updated_at) VALUES ($1, $2, 1, $3, now())
			ON CONFLICT (scope) DO NOTHING RETURNING version`, scope, value, by).Scan(&next)
	default:
		err = q.QueryRow(ctx, `UPDATE settings SET value = $2, version = version + 1, updated_by = $4, updated_at = now()
			WHERE scope = $1 AND version = $3 RETURNING version`, scope, value, version, by).Scan(&next)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	return next, err
}
