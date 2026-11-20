package db

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/kv"
)

// DocSet keeps several of a service's JSON documents as rows, as Doc keeps
// one, behind one version: the set's counter. It is for a service that
// reads its documents at one version and saves them passing one version
// along, as it did when they were keys of one ConfigMap (the issue
// service: its index, the known issues and its settings).
//
// Every key of the set is at the set's version, whether anything is kept
// under it or not. A read returns the document and the counter as they
// were at one moment; a save of any document is refused unless the counter
// is still at the version given, and moves the counter on in the
// transaction that writes, so what was read of the other documents before
// it is known to be older. Writers of one set take turns.
type DocSet struct {
	DB *DB
	// Name names the set: its counter, its lock, and the one-time copy
	// of its documents.
	Name string
	// Members are the set's documents, by the key the service knows each by.
	Members map[string]SetMember
}

// SetMember is one document of a set: how it is read from the rows and
// how the rows are changed to hold another.
type SetMember interface {
	load(ctx context.Context, q Querier) ([]byte, bool, error)
	save(ctx context.Context, tx pgx.Tx, doc []byte) error
}

type member[T any] struct {
	read  func(ctx context.Context, q Querier) (T, bool, error)
	write func(ctx context.Context, tx pgx.Tx, old, next T) error
}

// Member is a document of a set, with the Read and Write a Doc has.
func Member[T any](read func(ctx context.Context, q Querier) (T, bool, error), write func(ctx context.Context, tx pgx.Tx, old, next T) error) SetMember {
	return member[T]{read, write}
}

func (m member[T]) load(ctx context.Context, q Querier) ([]byte, bool, error) {
	doc, found, err := m.read(ctx, q)
	if err != nil || !found {
		return nil, false, err
	}
	b, err := json.Marshal(doc)
	return b, true, err
}

// save changes the rows to hold doc; nil is nothing kept.
func (m member[T]) save(ctx context.Context, tx pgx.Tx, doc []byte) error {
	var next T
	if doc != nil {
		if err := json.Unmarshal(doc, &next); err != nil {
			return err
		}
	}
	old, _, err := m.read(ctx, tx)
	if err != nil {
		return err
	}
	return m.write(ctx, tx, old, next)
}

// The set's counter is the version of a settings row that holds nothing
// else.
func (s *DocSet) scope() string { return "docset/" + s.Name }

func (s *DocSet) version(ctx context.Context, q Querier) (string, error) {
	_, v, err := LoadScope(ctx, q, s.scope())
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	return strconv.Itoa(v), nil
}

// LoadKey returns a document of the set as JSON, and the set's version; a
// document nothing is kept of is kv.ErrNotFound, at the set's version
// too. A key the set does not have is kv.ErrNotFound.
func (s *DocSet) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	m, ok := s.Members[key]
	var b []byte
	var found bool
	var version string
	err := s.DB.Snapshot(ctx, func(q Querier) (err error) {
		if version, err = s.version(ctx, q); err != nil || !ok {
			return err
		}
		b, found, err = m.load(ctx, q)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, version, kv.ErrNotFound
	}
	return b, version, nil
}

// change runs fn on the set when it is still at version ("" is whatever
// it is at), moves the counter on, and returns the new version.
func (s *DocSet) change(ctx context.Context, version string, fn func(tx pgx.Tx) error) (string, error) {
	out := ""
	err := s.DB.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey("docset/"+s.Name)); err != nil {
			return err
		}
		have, err := s.version(ctx, tx)
		if err != nil {
			return err
		}
		if version != "" && version != have {
			return kv.ErrConflict
		}
		if err := fn(tx); err != nil {
			return err
		}
		next, err := SaveScope(ctx, tx, s.scope(), []byte("{}"), AnyVersion, "")
		if err != nil {
			return err
		}
		out = strconv.Itoa(next)
		return s.DB.Notify(ctx, tx, "docset/"+s.Name)
	})
	return out, err
}

// SaveKey writes a document of the set when the set is still at version
// ("" writes whatever it is at), and returns the set's new version;
// kv.ErrConflict when any document of the set was saved since.
func (s *DocSet) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	m, ok := s.Members[key]
	if !ok {
		return "", errors.New("the document set " + s.Name + " has no key " + key)
	}
	if b == nil {
		b = []byte("null")
	}
	return s.change(ctx, version, func(tx pgx.Tx) error { return m.save(ctx, tx, b) })
}

// DeleteKey removes every row of a document of the set.
func (s *DocSet) DeleteKey(ctx context.Context, key string) error {
	m, ok := s.Members[key]
	if !ok {
		return nil
	}
	_, err := s.change(ctx, "", func(tx pgx.Tx) error { return m.save(ctx, tx, nil) })
	return err
}

// ImportID is the name the one-time copy of the set is recorded under.
func (s *DocSet) ImportID() string { return "docset/" + s.Name }

// Import copies the documents another backend kept under the set's keys,
// once and together (DB-17), and says which it copied. Like a Doc with
// ImportAs, a document the database already holds is left alone.
func (s *DocSet) Import(ctx context.Context, from kv.Backend, source string) ([]string, error) {
	keys := make([]string, 0, len(s.Members))
	for key := range s.Members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var copied []string
	_, err := s.DB.ImportOnce(ctx, s.ImportID(), source, func(ctx context.Context, tx pgx.Tx) (int, error) {
		copied = nil
		for _, key := range keys {
			b, _, err := from.LoadKey(ctx, key)
			if errors.Is(err, kv.ErrNotFound) {
				continue
			}
			if err != nil {
				return 0, err
			}
			if !json.Valid(b) {
				// What cannot be read was not readable before either.
				continue
			}
			if _, held, err := s.Members[key].load(ctx, tx); err != nil {
				return 0, err
			} else if held {
				continue
			}
			if err := s.Members[key].save(ctx, tx, b); err != nil {
				return 0, err
			}
			copied = append(copied, key)
		}
		if len(copied) > 0 {
			if _, err := SaveScope(ctx, tx, s.scope(), []byte("{}"), AnyVersion, ""); err != nil {
				return 0, err
			}
		}
		return len(copied), nil
	})
	if err != nil {
		return nil, err
	}
	return copied, nil
}
