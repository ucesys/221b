package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/kv"
)

// Doc keeps one of a service's JSON documents as rows of the model's
// tables, behind the kv.Backend the service reads and writes it through
// (ADR-162, the interim step): the service's logic stays as it is, and its
// records are rows with every field a column. Read builds the document
// from the rows; Write changes the rows from what they hold to what the
// service saved, so a save touches only the records that changed.
//
// It is for a service that edits its whole document in many places, until
// that service is given a repository of its own. The document is small
// (the ConfigMap it came from held 1 MiB at most), so reading it whole
// costs little; what a Doc does not give is a query or a write that
// touches one record without reading the others.
type Doc[T any] struct {
	DB *DB
	// Key is the key the service knows the document by.
	Key string
	// Read returns the document and whether anything is kept at all.
	Read func(ctx context.Context, q Querier) (T, bool, error)
	// Write changes the rows from old to new, inside tx.
	Write func(ctx context.Context, tx pgx.Tx, old, new T) error
}

// notSaved is the version of a document nothing is kept of.
const notSaved = "0"

func docVersion(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// LoadKey returns the document as JSON and its version, a digest of it; a
// document nothing is kept of is kv.ErrNotFound.
func (d *Doc[T]) LoadKey(ctx context.Context, _ string) ([]byte, string, error) {
	var doc T
	var found bool
	err := d.DB.Do(ctx, func(q Querier) (err error) {
		doc, found, err = d.Read(ctx, q)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, notSaved, kv.ErrNotFound
	}
	b, err := json.Marshal(doc)
	return b, docVersion(doc), err
}

// SaveKey writes the document when the rows still hold what version was
// read from ("" writes whatever they hold), and returns the new version;
// kv.ErrConflict when another writer came first. Writers of one document
// take turns for the length of the transaction, as they did on the
// ConfigMap's version.
func (d *Doc[T]) SaveKey(ctx context.Context, _ string, b []byte, version string) (string, error) {
	var next T
	if err := json.Unmarshal(b, &next); err != nil {
		return "", err
	}
	out := ""
	err := d.DB.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey("doc/"+d.Key)); err != nil {
			return err
		}
		old, found, err := d.Read(ctx, tx)
		if err != nil {
			return err
		}
		have := notSaved
		if found {
			have = docVersion(old)
		}
		if version != "" && version != have {
			return kv.ErrConflict
		}
		if err := d.Write(ctx, tx, old, next); err != nil {
			return err
		}
		// What a reader will find, which is what the next save is over.
		kept, found, err := d.Read(ctx, tx)
		if err != nil {
			return err
		}
		out = notSaved
		if found {
			out = docVersion(kept)
		}
		return d.DB.Notify(ctx, tx, "doc/"+d.Key)
	})
	return out, err
}

// DeleteKey removes every row of the document.
func (d *Doc[T]) DeleteKey(ctx context.Context, _ string) error {
	return d.DB.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey("doc/"+d.Key)); err != nil {
			return err
		}
		old, _, err := d.Read(ctx, tx)
		if err != nil {
			return err
		}
		var none T
		return d.Write(ctx, tx, old, none)
	})
}

// Import copies the document another backend kept, once (DB-17), and says
// whether it copied.
func (d *Doc[T]) Import(ctx context.Context, from kv.Backend, source string) (bool, error) {
	copied := false
	_, err := d.DB.ImportOnce(ctx, d.Key, source, func(ctx context.Context, tx pgx.Tx) (int, error) {
		b, _, err := from.LoadKey(ctx, d.Key)
		if errors.Is(err, kv.ErrNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		var doc, none T
		if err := json.Unmarshal(b, &doc); err != nil {
			// What cannot be read was not readable before either.
			return 0, nil
		}
		copied = true
		return 1, d.Write(ctx, tx, none, doc)
	})
	return copied, err
}

// Diff walks two lists of records matched by key, and calls add for a
// record only in next, remove for one only in old, and change for one in
// both that differs. It is what a Doc's Write is made of.
func Diff[V any](old, next []V, key func(V) string, add func(V) error, change func(old, next V) error, remove func(V) error) error {
	was := make(map[string]V, len(old))
	for _, v := range old {
		was[key(v)] = v
	}
	seen := make(map[string]bool, len(next))
	for _, v := range next {
		k := key(v)
		seen[k] = true
		o, ok := was[k]
		switch {
		case !ok:
			if err := add(v); err != nil {
				return err
			}
		case !reflect.DeepEqual(o, v):
			if err := change(o, v); err != nil {
				return err
			}
		}
	}
	for _, v := range old {
		if !seen[key(v)] {
			if err := remove(v); err != nil {
				return err
			}
		}
	}
	return nil
}
