package db

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/kv"
)

// ScopeKV keeps a service's singleton documents as scopes of the settings
// table (model rule 14), behind the kv.Backend the service already reads
// and writes them through: each key it is given is one scope, and the
// row's version is the version a save is made over. A service whose
// record is one document read whole needs no repository beyond this.
type ScopeKV struct {
	d *DB
	// scopes names the scope of each key.
	scopes map[string]string
}

// NewScopeKV keeps each key of scopes (key to scope) in d.
func NewScopeKV(d *DB, scopes map[string]string) *ScopeKV {
	return &ScopeKV{d: d, scopes: scopes}
}

// Has says whether key is one of the documents kept here.
func (s *ScopeKV) Has(key string) bool { _, ok := s.scopes[key]; return ok }

func (s *ScopeKV) scope(key string) (string, error) {
	scope, ok := s.scopes[key]
	if !ok {
		return "", errors.New("the database keeps no settings under " + key)
	}
	return scope, nil
}

// LoadKey returns the scope's document and its version; a scope never
// saved is kv.ErrNotFound at version "0".
func (s *ScopeKV) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	scope, err := s.scope(key)
	if err != nil {
		return nil, "", err
	}
	var b []byte
	var version int
	err = s.d.Do(ctx, func(q Querier) (err error) {
		b, version, err = LoadScope(ctx, q, scope)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return nil, "0", kv.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return b, strconv.Itoa(version), nil
}

// SaveKey writes the scope's document over version ("" for whatever it is
// at), announces the change to the other replicas, and returns the new
// version; kv.ErrConflict when another writer came first.
func (s *ScopeKV) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	scope, err := s.scope(key)
	if err != nil {
		return "", err
	}
	over := AnyVersion
	if version != "" {
		if over, err = strconv.Atoi(version); err != nil {
			return "", kv.ErrConflict
		}
	}
	var next int
	err = s.d.Tx(ctx, func(tx pgx.Tx) (err error) {
		if next, err = SaveScope(ctx, tx, scope, b, over, ""); err != nil {
			return err
		}
		return s.d.Notify(ctx, tx, "settings/"+scope)
	})
	if errors.Is(err, ErrConflict) {
		return "", kv.ErrConflict
	}
	return strconv.Itoa(next), err
}

// DeleteKey removes the scope.
func (s *ScopeKV) DeleteKey(ctx context.Context, key string) error {
	scope, err := s.scope(key)
	if err != nil {
		return err
	}
	return s.d.Do(ctx, func(q Querier) error {
		_, err := q.Exec(ctx, "DELETE FROM settings WHERE scope = $1", scope)
		return err
	})
}

// Import copies each key's document from another backend into its scope,
// once each (DB-17), and returns the keys it copied.
func (s *ScopeKV) Import(ctx context.Context, from kv.Backend, source string) ([]string, error) {
	var copied []string
	for key, scope := range s.scopes {
		ran, err := s.d.ImportOnce(ctx, key, source, func(ctx context.Context, tx pgx.Tx) (int, error) {
			b, _, err := from.LoadKey(ctx, key)
			if errors.Is(err, kv.ErrNotFound) {
				return 0, nil
			}
			if err != nil {
				return 0, err
			}
			_, err = SaveScope(ctx, tx, scope, b, AnyVersion, "")
			return 1, err
		})
		if err != nil {
			return copied, err
		}
		if ran {
			copied = append(copied, key)
		}
	}
	return copied, nil
}
