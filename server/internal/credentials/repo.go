package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Repo keeps the credentials' list, without their secrets, which stay in
// the Secret under secret-<id> (CR-05, DB-60). Each operation reads or
// writes one credential, or the note of the upgrade, on its own: no
// operation rewrites the list. A change of one credential is made over
// that credential as it is kept when the change is made, so two writers
// of one credential take turns and neither loses the other's change.
type Repo interface {
	// List is every credential kept, in the order they were made.
	List(ctx context.Context) ([]Credential, error)
	// Get is one credential; ErrNotFound when it is not kept.
	Get(ctx context.Context, id string) (Credential, error)
	// Add keeps a new credential, under the id it has: refused (ErrInvalid)
	// when max are kept, when one of its name is, or one of its id.
	Add(ctx context.Context, c Credential, max int) error
	// Update changes one credential: change is given it as kept now, and
	// what it leaves is kept, under the same id. An error from change
	// changes nothing and is returned as it is.
	Update(ctx context.Context, id string, change func(c *Credential) error) (Credential, error)
	// Remove takes a credential out of the list; false when it was not
	// kept.
	Remove(ctx context.Context, id string) (bool, error)
	// Migration is the note of the upgrade to 0.12.1 (CR-12), and
	// UpdateMigration changes it over the note as kept now.
	Migration(ctx context.Context) (Migration, error)
	UpdateMigration(ctx context.Context, change func(m *Migration) error) error
}

// errTaken is a name another credential has.
func errTaken(name string) error { return invalid("there is a credential named %s already", name) }

// --- In memory ---

// MemoryRepo keeps the list in memory, for the tests (the console keeps
// it in the database since DB-97).
type MemoryRepo struct {
	mu   sync.Mutex
	list []Credential
	m    Migration
}

// NewMemoryRepo is an empty list in memory.
func NewMemoryRepo() *MemoryRepo { return &MemoryRepo{} }

// clone is a copy that shares nothing with what is kept.
func clone(c Credential) Credential {
	b, _ := json.Marshal(c)
	var out Credential
	_ = json.Unmarshal(b, &out)
	return out
}

func (r *MemoryRepo) List(context.Context) ([]Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Credential, len(r.list))
	for i, c := range r.list {
		out[i] = clone(c)
	}
	return out, nil
}

func (r *MemoryRepo) Get(_ context.Context, id string) (Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.list {
		if c.ID == id {
			return clone(c), nil
		}
	}
	return Credential{}, fmt.Errorf("%w %q: Administration › Credentials lists them", ErrNotFound, id)
}

func (r *MemoryRepo) Add(_ context.Context, c Credential, max int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.ID == "" {
		return errors.New("a credential without an id cannot be kept")
	}
	if len(r.list) >= max {
		return invalid("at most %d credentials; remove one first", max)
	}
	for _, o := range r.list {
		if o.ID == c.ID {
			return invalid("there is a credential %s already", c.ID)
		}
		if strings.EqualFold(o.Name, c.Name) {
			return errTaken(c.Name)
		}
	}
	r.list = append(r.list, clone(c))
	return nil
}

func (r *MemoryRepo) Update(_ context.Context, id string, change func(c *Credential) error) (Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.list, func(o Credential) bool { return o.ID == id })
	if i < 0 {
		return Credential{}, fmt.Errorf("%w %q", ErrNotFound, id)
	}
	next := clone(r.list[i])
	if err := change(&next); err != nil {
		return Credential{}, err
	}
	next.ID = id
	if slices.ContainsFunc(r.list, func(o Credential) bool { return o.ID != id && strings.EqualFold(o.Name, next.Name) }) {
		return Credential{}, errTaken(next.Name)
	}
	r.list[i] = clone(next)
	return next, nil
}

func (r *MemoryRepo) Remove(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.list)
	r.list = slices.DeleteFunc(r.list, func(o Credential) bool { return o.ID == id })
	return len(r.list) < n, nil
}

func (r *MemoryRepo) Migration(context.Context) (Migration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := json.Marshal(r.m)
	var out Migration
	_ = json.Unmarshal(b, &out)
	return out, nil
}

func (r *MemoryRepo) UpdateMigration(_ context.Context, change func(m *Migration) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := json.Marshal(r.m)
	var next Migration
	_ = json.Unmarshal(b, &next)
	if err := change(&next); err != nil {
		return err
	}
	r.m = next
	return nil
}

// --- In the database (DB-60, DB-75) ---

// PGRepo keeps the list as rows of credentials, one statement or one small
// transaction an operation. A change of one credential reads its row with
// a lock (FOR UPDATE), changes it, and writes it in the same transaction:
// a second writer of that credential waits for the first and changes what
// the first left. No two credentials of the list have one name, whatever
// its case: the table's unique index says so, and its refusal is the
// sentence the page shows. The count is held to the bound by a lock the
// adds take turns on.
type PGRepo struct{ d *db.DB }

// NewPGRepo keeps the credentials' list in d.
func NewPGRepo(d *db.DB) *PGRepo { return &PGRepo{d: d} }

// DB is the database the list is kept in.
func (r *PGRepo) DB() *db.DB { return r.d }

var (
	keptSQL    = db.SelectSQL("credentials", &credRow{}, "WHERE is_kept ORDER BY created_at NULLS FIRST, credential_id")
	oneSQL     = db.SelectSQL("credentials", &credRow{}, "WHERE credential_id = $1 AND is_kept")
	lockOneSQL = oneSQL + " FOR UPDATE"
)

func (r *PGRepo) List(ctx context.Context) ([]Credential, error) {
	var out []Credential
	err := r.d.Do(ctx, func(q db.Querier) error {
		out = nil
		var err error
		out, err = listRows(ctx, q)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reading the credentials: %w", err)
	}
	return out, nil
}

// listRows is every kept credential, in the order they were made.
func listRows(ctx context.Context, q db.Querier) ([]Credential, error) {
	rows, err := q.Query(ctx, keptSQL)
	if err != nil {
		return nil, err
	}
	var out []Credential
	var row credRow
	_, err = pgx.ForEachRow(rows, db.Fields(&row), func() error {
		out = append(out, credentialOf(row))
		return nil
	})
	return out, err
}

func (r *PGRepo) Get(ctx context.Context, id string) (Credential, error) {
	var row credRow
	err := r.d.Do(ctx, func(q db.Querier) error {
		return q.QueryRow(ctx, oneSQL, id).Scan(db.Fields(&row)...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, fmt.Errorf("%w %q: Administration › Credentials lists them", ErrNotFound, id)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("reading the credential: %w", err)
	}
	return credentialOf(row), nil
}

// putRow writes a credential over whatever row has its id: one with only
// its id is filled, a kept one changed in place, so a key that points at
// it stays.
func putRow(ctx context.Context, tx pgx.Tx, c Credential) error {
	if c.ID == "" {
		return errors.New("a credential without an id cannot be kept")
	}
	row := rowOf(c)
	if _, err := tx.Exec(ctx, upsertSQL, db.Fields(&row)...); err != nil {
		if db.IsUnique(err) {
			return errTaken(c.Name)
		}
		return err
	}
	return nil
}

func (r *PGRepo) Add(ctx context.Context, c Credential, max int) error {
	return r.d.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('credentials/add'))"); err != nil {
			return err
		}
		var n int
		var kept bool
		if err := tx.QueryRow(ctx, "SELECT count(*) FILTER (WHERE is_kept), coalesce(bool_or(is_kept AND credential_id = $1), false) FROM credentials", c.ID).Scan(&n, &kept); err != nil {
			return err
		}
		if kept {
			return invalid("there is a credential %s already", c.ID)
		}
		if n >= max {
			return invalid("at most %d credentials; remove one first", max)
		}
		return putRow(ctx, tx, c)
	})
}

func (r *PGRepo) Update(ctx context.Context, id string, change func(c *Credential) error) (Credential, error) {
	var next Credential
	err := r.d.Tx(ctx, func(tx pgx.Tx) error {
		var row credRow
		err := tx.QueryRow(ctx, lockOneSQL, id).Scan(db.Fields(&row)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w %q", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		next = credentialOf(row)
		if err := change(&next); err != nil {
			return err
		}
		next.ID = id
		return putRow(ctx, tx, next)
	})
	if err != nil {
		return Credential{}, err
	}
	return next, nil
}

func (r *PGRepo) Remove(ctx context.Context, id string) (bool, error) {
	removed := false
	err := r.d.Tx(ctx, func(tx pgx.Tx) error {
		removed = false
		var one int
		err := tx.QueryRow(ctx, "SELECT 1 FROM credentials WHERE credential_id = $1 AND is_kept FOR UPDATE", id).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		removed = true
		return removeRow(ctx, tx, id)
	})
	if err != nil {
		return false, fmt.Errorf("removing the credential: %w", err)
	}
	return removed, nil
}

func (r *PGRepo) Migration(ctx context.Context) (Migration, error) {
	var m Migration
	err := r.d.Do(ctx, func(q db.Querier) error {
		m = Migration{}
		b, _, err := db.LoadScope(ctx, q, migrationScope)
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &m)
	})
	if err != nil {
		return Migration{}, fmt.Errorf("reading the note of the upgrade: %w", err)
	}
	return m, nil
}

func (r *PGRepo) UpdateMigration(ctx context.Context, change func(m *Migration) error) error {
	return r.d.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", migrationScope); err != nil {
			return err
		}
		var m Migration
		b, _, err := db.LoadScope(ctx, tx, migrationScope)
		switch {
		case errors.Is(err, db.ErrNotFound):
		case err != nil:
			return err
		default:
			if err := json.Unmarshal(b, &m); err != nil {
				return err
			}
		}
		if err := change(&m); err != nil {
			return err
		}
		next, _ := json.Marshal(m)
		if string(next) == string(b) {
			return nil
		}
		_, err = db.SaveScope(ctx, tx, migrationScope, next, db.AnyVersion, "")
		return err
	})
}
