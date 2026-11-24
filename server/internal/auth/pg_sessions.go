package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/roles"
)

// The sign-in sessions in the database (Phase 13, DB-66): each session a
// row of auth_sessions, every field of its record a column, under the key
// the session's Secret was named for: the SHA-256 of the session's ID,
// never the ID. Nothing in a record is secret. PGSessions is the shared
// store (SharedStore) on that table; Sessions works over it as it does
// over the Secrets.

// SessionsImportID is the name the one-time copy of the sessions out of
// their Secrets is recorded under.
const SessionsImportID = "secret/auth-sessions"

const (
	// sessionsTopic is the notice a replica sends when it ends a session
	// or changes what one holds, and every replica hears.
	sessionsTopic = "auth-sessions"
	// SessionHeldFor is how long a replica answers for a session from the
	// row as it last read it, without asking the database again. It bounds
	// how long a session ended on another replica can still be taken here
	// when the notice of its end did not arrive.
	SessionHeldFor = 5 * time.Second
	// sessionMissFor is how long a key with no row is remembered as that,
	// so a cookie nobody issued costs one read every few seconds at most.
	sessionMissFor = 2 * time.Second
	// sessionCallTime is the longest one read or write of a session's row
	// may take. A database that swallows what it is sent holds a request
	// this long and no longer, and is then left alone (Sessions.reach).
	sessionCallTime = 2 * time.Second
	// sessionListTime is the longest a read of every session's row may
	// take: the sweep's, the Users page's, the metric's.
	sessionListTime = 5 * time.Second
	// maxHeld bounds the rows a replica holds copies of.
	maxHeld = 20000
)

// sessionKeyShape is a session's key: a SHA-256 in hexadecimal.
var sessionKeyShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sessionRow is a row of auth_sessions.
type sessionRow struct {
	Key        string    `col:"auth_session_id"`
	Version    int       `col:"record_version"`
	Subject    string    `col:"subject"`
	Username   string    `col:"username"`
	Email      string    `col:"email"`
	Name       string    `col:"display_name"`
	Groups     []string  `col:"groups"`
	KubeName   string    `col:"kube_name"`
	KubeGroups []string  `col:"kube_groups"`
	Admin      bool      `col:"is_admin"`
	Roles      []string  `col:"roles"`
	Super      []string  `col:"super_roles"`
	Method     string    `col:"method"`
	Ref        string    `col:"directory_ref"`
	GroupsAt   time.Time `col:"groups_read_at"`
	Created    time.Time `col:"created_at"`
	LastSeen   time.Time `col:"last_seen_at"`
	Expires    time.Time `col:"expires_at"`
}

var (
	sessionSelectSQL = db.SelectSQL("auth_sessions", &sessionRow{}, "")
	sessionInsertSQL = db.InsertSQL("auth_sessions", &sessionRow{})
	// The key is the first column, so the row's own $1 names it.
	sessionUpdateSQL = db.UpdateSQL("auth_sessions", &sessionRow{}, "auth_session_id = $1")
)

// stored is a time as the database keeps it: in UTC, to the microsecond.
func stored(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// column is a list as a column that is never null takes it, and listed a
// column's list as the record has it: none, not an empty one.
func column[T ~string](s []T) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = string(v)
	}
	return out
}

func listed[T ~string](s []string) []T {
	if len(s) == 0 {
		return nil
	}
	out := make([]T, len(s))
	for i, v := range s {
		out[i] = T(v)
	}
	return out
}

// rowOfRecord is a stored record as its row. A record this console cannot
// keep whole in columns is refused: one whose key is not a hash, one of a
// later format, and one with fields this version does not know.
func rowOfRecord(key string, b []byte) (sessionRow, error) {
	if !sessionKeyShape.MatchString(key) {
		return sessionRow{}, errors.New("a session's key is the SHA-256 of its ID")
	}
	var r sessionRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return sessionRow{}, fmt.Errorf("the session's record cannot be read: %w", err)
	}
	extra, err := unknownFields(b, sessionFields)
	if err != nil {
		return sessionRow{}, fmt.Errorf("the session's record cannot be read: %w", err)
	}
	if r.Version > sessionVersion || len(extra) > 0 {
		return sessionRow{}, errors.New("the session's record is of a later format than this console's")
	}
	u := r.User
	return sessionRow{Key: key, Version: sessionVersion,
		Subject: u.Subject, Username: u.Username, Email: u.Email, Name: u.Name, Groups: column(u.Groups), KubeName: u.KubeName, KubeGroups: column(u.KubeGroups),
		Admin: r.Admin, Roles: column(r.Roles), Super: column(r.Super), Method: r.Method, Ref: r.Ref,
		GroupsAt: stored(r.GroupsAt), Created: stored(r.Created), LastSeen: stored(r.LastSeen), Expires: stored(r.Expires)}, nil
}

// recordOfRow is a row as the record Sessions reads.
func recordOfRow(r sessionRow) ([]byte, error) {
	return json.Marshal(sessionRecord{Version: r.Version,
		User:  userRecord{Subject: r.Subject, Username: r.Username, Email: r.Email, Name: r.Name, Groups: listed[string](r.Groups), KubeName: r.KubeName, KubeGroups: listed[string](r.KubeGroups)},
		Admin: r.Admin, Roles: listed[roles.ID](r.Roles), Super: listed[roles.ID](r.Super), Method: r.Method, Ref: r.Ref,
		GroupsAt: r.GroupsAt.UTC(), Created: r.Created.UTC(), LastSeen: r.LastSeen.UTC(), Expires: r.Expires.UTC()})
}

// sameButSeen says whether two rows differ in nothing but the last
// activity: a write of that alone is not announced.
func sameButSeen(a, b sessionRow) bool {
	a.LastSeen, b.LastSeen = time.Time{}, time.Time{}
	for _, t := range []*time.Time{&a.GroupsAt, &a.Created, &a.Expires, &b.GroupsAt, &b.Created, &b.Expires} {
		*t = stored(*t)
	}
	return a.Key == b.Key && a.Version == b.Version && a.Subject == b.Subject && a.Username == b.Username && a.Email == b.Email && a.Name == b.Name &&
		slices.Equal(a.Groups, b.Groups) && a.KubeName == b.KubeName && slices.Equal(a.KubeGroups, b.KubeGroups) && a.Admin == b.Admin &&
		slices.Equal(a.Roles, b.Roles) && slices.Equal(a.Super, b.Super) && a.Method == b.Method && a.Ref == b.Ref &&
		a.GroupsAt.Equal(b.GroupsAt) && a.Created.Equal(b.Created) && a.Expires.Equal(b.Expires)
}

// PGSessions keeps the sign-in sessions in the table auth_sessions, where
// every replica sees them (DB-66).
//
// A replica does not ask the database on every request: it answers from
// the row as it last read it for SessionHeldFor, and drops what it holds
// at once when another replica announces that a session ended or changed
// (a NOTIFY the write sends in its own transaction). So a sign-out on
// another replica is refused here as soon as its notice arrives, and
// within SessionHeldFor at the latest when it does not. A write of the
// last activity alone is not announced: the others read it when their
// copy runs out.
//
// While the database cannot be reached every call returns an error that
// wraps db.ErrUnavailable (StoreAway), never a copy and never an empty
// list: Sessions then goes on from this replica's memory, and a sign-in
// gets a signed session (DB-65).
type PGSessions struct {
	d   *db.DB
	now func() time.Time

	mu sync.Mutex
	// gen moves on whenever what is held may be stale, so a read that was
	// on its way then is not kept.
	gen  uint64
	held map[string]heldRow
	// asked counts the reads of a row that went to the database.
	asked atomic.Int64
}

// heldRow is a row as this replica last read it; b is nil for no row.
type heldRow struct {
	b     []byte
	until time.Time
}

// NewPGSessions keeps the sessions in d. now is the clock the held copies
// run out by; nil is time.Now.
func NewPGSessions(d *db.DB, now func() time.Time) *PGSessions {
	if now == nil {
		now = time.Now
	}
	p := &PGSessions{d: d, now: now, held: map[string]heldRow{}}
	// Also called when the listener reconnects and at every poll: what was
	// announced meanwhile was missed.
	d.Subscribe(sessionsTopic, p.dropHeld)
	return p
}

// dropHeld forgets every copy this replica holds.
func (p *PGSessions) dropHeld() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	clear(p.held)
}

// drop forgets the copy of one row, which this replica is writing.
func (p *PGSessions) drop(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	delete(p.held, key)
}

// hold keeps a row as it was just read (b nil for none), unless what is
// held was dropped while it was being read.
func (p *PGSessions) hold(gen uint64, key string, b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.gen {
		return
	}
	now := p.now()
	if len(p.held) >= maxHeld {
		for k, h := range p.held {
			if !now.Before(h.until) {
				delete(p.held, k)
			}
		}
		if len(p.held) >= maxHeld {
			clear(p.held)
		}
	}
	lasts := SessionHeldFor
	if b == nil {
		lasts = sessionMissFor
	}
	p.held[key] = heldRow{b: b, until: now.Add(lasts)}
}

// heldNow is the copy of a row this replica may still answer from.
func (p *PGSessions) heldNow(key string) (b []byte, hit bool, gen uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.held[key]
	if !ok {
		return nil, false, p.gen
	}
	if !p.now().Before(h.until) {
		delete(p.held, key)
		return nil, false, p.gen
	}
	return h.b, true, p.gen
}

// rowCall gives one read or write of a row its time.
func rowCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, sessionCallTime)
}

// Get returns a session's record by key, from the copy this replica
// holds or from its row; ok is false when there is none.
func (p *PGSessions) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, hit, gen := p.heldNow(key)
	if hit {
		return b, b != nil, nil
	}
	p.asked.Add(1)
	ctx, cancel := rowCall(ctx)
	defer cancel()
	var row sessionRow
	err := p.d.Do(ctx, func(q db.Querier) error {
		return q.QueryRow(ctx, sessionSelectSQL+"WHERE auth_session_id = $1", key).Scan(db.Fields(&row)...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		p.hold(gen, key, nil)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading the session's row: %w", err)
	}
	if b, err = recordOfRow(row); err != nil {
		return nil, false, fmt.Errorf("reading the session's row: %w", err)
	}
	p.hold(gen, key, b)
	return b, true, nil
}

// Put writes a session's record as its row: it makes the row when there
// is none and replace, given nil, says so, and changes it when replace,
// given the record as the row has it now (the row is locked meanwhile),
// says so. It reports whether it wrote. A new row, and a change to anything
// but the last activity, is announced to the other replicas.
func (p *PGSessions) Put(ctx context.Context, key string, b []byte, replace func(current []byte) bool) (bool, error) {
	next, err := rowOfRecord(key, b)
	if err != nil {
		return false, err
	}
	ctx, cancel := rowCall(ctx)
	defer cancel()
	wrote := false
	err = p.d.Tx(ctx, func(tx pgx.Tx) error {
		wrote = false
		var cur sessionRow
		err := tx.QueryRow(ctx, sessionSelectSQL+"WHERE auth_session_id = $1 FOR UPDATE", key).Scan(db.Fields(&cur)...)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if replace != nil && !replace(nil) {
				return nil
			}
			if _, err := tx.Exec(ctx, sessionInsertSQL, db.Fields(&next)...); err != nil {
				return err
			}
			// A new row is announced: the replicas that looked for its key a
			// moment ago and found nothing must not go on answering from
			// that. That is the ending of a signed session that was never
			// exchanged, and the stored session a signed one is exchanged
			// for, whose key every replica shown the cookie looks up
			// (exchangeID); a sign-in's random key was looked up by nobody,
			// and sign-ins are rare enough that telling them apart is not
			// worth it.
			if err := p.d.Notify(ctx, tx, sessionsTopic); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			now, err := recordOfRow(cur)
			if err != nil {
				return err
			}
			if replace != nil && !replace(now) {
				return nil
			}
			if _, err := tx.Exec(ctx, sessionUpdateSQL, db.Fields(&next)...); err != nil {
				return err
			}
			if !sameButSeen(cur, next) {
				if err := p.d.Notify(ctx, tx, sessionsTopic); err != nil {
					return err
				}
			}
		}
		wrote = true
		return nil
	})
	p.drop(key)
	if err != nil {
		return false, fmt.Errorf("writing the session's row: %w", err)
	}
	return wrote, nil
}

// Delete removes a session's row and tells the other replicas; a row
// already gone is not an error, and is not announced.
func (p *PGSessions) Delete(ctx context.Context, key string) error {
	ctx, cancel := rowCall(ctx)
	defer cancel()
	err := p.d.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE auth_session_id = $1", key)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return p.d.Notify(ctx, tx, sessionsTopic)
	})
	p.drop(key)
	if err != nil {
		return fmt.Errorf("deleting the session's row: %w", err)
	}
	return nil
}

// List returns every session's record, by key, as the table has them
// now. A table that cannot be read is an error, never an empty list.
func (p *PGSessions) List(ctx context.Context) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionListTime)
	defer cancel()
	out := map[string][]byte{}
	err := p.d.Do(ctx, func(q db.Querier) error {
		clear(out)
		rows, err := q.Query(ctx, sessionSelectSQL)
		if err != nil {
			return err
		}
		var row sessionRow
		_, err = pgx.ForEachRow(rows, db.Fields(&row), func() error {
			// The record is made of the row's values at once: the next row is
			// read into the same variables.
			b, err := recordOfRow(row)
			if err != nil {
				return err
			}
			out[row.Key] = b
			return nil
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("listing the sessions' rows: %w", err)
	}
	return out, nil
}

// --- The move out of the Secrets (DB-66) ---

// copyable says whether a record read from a session's Secret is one the
// copy takes, and returns it as its row: one this console reads whole,
// under a key that is a hash, that has not outlived its idle or its
// maximum time. idle is the sessions' idle limit; the write-back interval
// is allowed for, as Sweep allows for it.
func copyable(key string, b []byte, idle time.Duration, now time.Time) (row sessionRow, readable, live bool) {
	row, err := rowOfRecord(key, b)
	if err != nil {
		return sessionRow{}, false, false
	}
	return row, true, !(now.Sub(row.LastSeen) > idle+seenEvery || now.After(row.Expires))
}

// CopySessions copies the sessions their Secrets hold into auth_sessions,
// once, in the transaction that writes the copy down under
// SessionsImportID: whoever is signed in stays signed in across the move.
// read lists the Secrets' records by key, and is not called when the copy
// was made before. A session that has expired is not copied, and neither
// is a record this console cannot read whole (its Secret is then left as
// it is, SessionsToEmpty). It says how many it copied; an error copies
// nothing, and the next start tries again.
func CopySessions(ctx context.Context, d *db.DB, read func(ctx context.Context) (map[string][]byte, error), idle time.Duration, now time.Time) (int, error) {
	copied := 0
	_, err := d.ImportOnce(ctx, SessionsImportID, "secret", func(ctx context.Context, tx pgx.Tx) (int, error) {
		copied = 0
		recs, err := read(ctx)
		if err != nil {
			return 0, err
		}
		batch := &pgx.Batch{}
		for key, b := range recs {
			row, readable, live := copyable(key, b, idle, now)
			if !readable || !live {
				continue
			}
			batch.Queue(sessionInsertSQL+" ON CONFLICT (auth_session_id) DO NOTHING", db.Fields(&row)...)
			copied++
		}
		if copied == 0 {
			return 0, nil
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return 0, err
		}
		return copied, nil
	})
	if err != nil {
		return 0, err
	}
	return copied, nil
}

// SessionsToEmpty names, of the records the session Secrets still hold
// (by key), the ones whose Secret is to be emptied now that the copy is
// recorded: a session the copy made a row of, and one that has expired.
// A session the copy took is told by its row or, when the row is gone
// since (a sign-out, its time run out), by having been started before the
// copy was made: left whole, its Secret would let a console from before
// the move take up a session that was ended since. What is left as it is:
// a record this console cannot read whole, and a session a console from
// before the move started after the copy, which this console never had.
//
// It is an error, and nothing is named, when the database cannot say.
func SessionsToEmpty(ctx context.Context, d *db.DB, recs map[string][]byte, idle time.Duration, now time.Time) ([]string, error) {
	var keys []string
	for key := range recs {
		if sessionKeyShape.MatchString(key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rows := map[string]bool{}
	var copiedAt time.Time
	err := d.Snapshot(ctx, func(q db.Querier) error {
		clear(rows)
		if err := q.QueryRow(ctx, "SELECT imported_at FROM imports WHERE import_id = $1", SessionsImportID).Scan(&copiedAt); err != nil {
			return err
		}
		found, err := q.Query(ctx, "SELECT auth_session_id FROM auth_sessions WHERE auth_session_id = ANY($1)", keys)
		if err != nil {
			return err
		}
		var key string
		_, err = pgx.ForEachRow(found, []any{&key}, func() error { rows[key] = true; return nil })
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("the sessions' copy is not recorded")
	}
	if err != nil {
		return nil, fmt.Errorf("reading which sessions were copied: %w", err)
	}
	var out []string
	slices.Sort(keys)
	for _, key := range keys {
		row, readable, live := copyable(key, recs[key], idle, now)
		if !readable {
			continue
		}
		if rows[key] || !live || !row.Created.After(copiedAt) {
			out = append(out, key)
		}
	}
	return out, nil
}

// --- After a rollback (DB-66; the owner, November 20, 2026, answer 2) ---

// RollbackGrace is how long after the sessions' copy a session a console
// from before the move started is taken for one of the rolling upgrade
// itself (a replica of the release before, still serving while the new
// ones start), and not for the sign of a rollback.
const RollbackGrace = 10 * time.Minute

// rollbackPrefix names, in imports, each ending of the stored sessions
// after a rollback: the prefix and the time the newest sign was started,
// so the same sign is acted on once.
const rollbackPrefix = "auth/sessions-ended-after-rollback/"

// AfterRollback is what EndAfterRollback found and did.
type AfterRollback struct {
	// Signs is how many sessions a console without the database started
	// after the copy (and its grace), in the Secrets now.
	Signs int
	// Ended is how many stored sessions were ended; Done says they were
	// ended now, and not before for the same signs.
	Ended int
	Done  bool
}

// EndAfterRollback ends every session the table keeps when the session
// Secrets show that a console without the database ran since the copy
// was recorded: a Secret holding a session it started more than
// RollbackGrace after the copy, whether or not that session has expired.
// The rows from before the rollback would otherwise be sessions again,
// though a sign-out or a password reset made on the rolled-back console
// did not reach them. Ending is deleting the rows, in one transaction
// with a notice to every replica; the rows that say a signed session was
// ended (DB-65) stay, so it stays ended. It is done once for a given
// newest sign, recorded in imports under rollbackPrefix and the sign's
// time; a later rollback, with a newer sign, ends them again, but a newer
// sign within RollbackGrace of the last one ended for is the same roll
// forward (a replica of the release before not replaced yet) and ends
// nothing again. It reads
// the Secrets' records by key, as SessionsToEmpty does, and must be given
// them before they are emptied. An error ends nothing.
func EndAfterRollback(ctx context.Context, d *db.DB, recs map[string][]byte) (AfterRollback, error) {
	var out AfterRollback
	var copiedAt time.Time
	if err := d.Do(ctx, func(q db.Querier) error {
		return q.QueryRow(ctx, "SELECT imported_at FROM imports WHERE import_id = $1", SessionsImportID).Scan(&copiedAt)
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, errors.New("the sessions' copy is not recorded")
		}
		return out, fmt.Errorf("reading when the sessions were copied: %w", err)
	}
	var newest time.Time
	for key, b := range recs {
		row, err := rowOfRecord(key, b)
		if err != nil {
			continue
		}
		if row.Created.After(copiedAt.Add(RollbackGrace)) {
			out.Signs++
			if row.Created.After(newest) {
				newest = row.Created
			}
		}
	}
	if out.Signs == 0 {
		return out, nil
	}
	id := rollbackPrefix + newest.UTC().Format(time.RFC3339Nano)
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		out.Ended, out.Done = 0, false
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", rollbackPrefix); err != nil {
			return err
		}
		// Done for this sign, or a newer one, before.
		rows, err := tx.Query(ctx, "SELECT import_id FROM imports WHERE starts_with(import_id, $1)", rollbackPrefix)
		if err != nil {
			return err
		}
		var done string
		before := false
		var last time.Time
		if _, err := pgx.ForEachRow(rows, []any{&done}, func() error {
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(done, rollbackPrefix)); err == nil {
				before = before || !t.Before(newest.UTC())
				if t.After(last) {
					last = t
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if before {
			return nil
		}
		if !last.IsZero() && !newest.After(last.Add(RollbackGrace)) {
			// The same roll forward: a replica of the release before, not
			// replaced yet, signed someone in a moment after the sign the
			// ending was done for. What was from before the rollback was
			// ended then; the sessions since are the upgraded replicas'.
			// The newer sign is recorded, and nothing is ended again.
			_, err := tx.Exec(ctx, "INSERT INTO imports (import_id, source, record_count) VALUES ($1, $2, 0)", id, "secret")
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE method <> $1", endedMethod)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO imports (import_id, source, record_count) VALUES ($1, $2, $3)", id, "secret", tag.RowsAffected()); err != nil {
			return err
		}
		if err := d.Notify(ctx, tx, sessionsTopic); err != nil {
			return err
		}
		out.Ended, out.Done = int(tag.RowsAffected()), true
		return nil
	})
	if err != nil {
		return AfterRollback{Signs: out.Signs}, fmt.Errorf("ending the sessions from before a rollback: %w", err)
	}
	return out, nil
}
