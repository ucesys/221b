package db

import (
	"context"
	"errors"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The replicas coordinate through the database, not through one of them
// (ADR-163): every replica runs the same loops, and the database says
// which of them does each piece of work.
//
//   - A piece of work that is due at a time is claimed by inserting its
//     row (Claim); the one whose insert goes in does it.
//   - A pass over everything (a sweep) runs behind an advisory lock of its
//     name (Sweep); a replica that does not get the lock skips the pass.
//   - A change other replicas should see is announced (Notify) and heard
//     (Subscribe); each replica also looks every Poll, so a notice is a
//     hint and never the only way a change is found.
//
// Advisory locks and notices belong to one PostgreSQL server. With two
// sites writing (pgEdge), each site coordinates its own replicas, and a
// duty for the whole installation has to be given to one site.

// channel is the one channel notices travel on; the topic is the payload.
const channel = "console"

// lockKey turns a lock's name into the 64-bit key PostgreSQL takes.
func lockKey(name string) int64 {
	h := fnv.New64a()
	h.Write([]byte("221b/" + name))
	return int64(h.Sum64())
}

// Lock is an advisory lock this replica holds. It is held by a session, so
// it is given back when Unlock is called or the connection ends, whichever
// comes first: a replica that dies never keeps one.
type Lock struct {
	conn *pgxpool.Conn
	key  int64
}

// TryLock takes the lock of that name if no session holds it, and says
// whether it did. It never waits.
func (d *DB) TryLock(ctx context.Context, name string) (*Lock, bool, error) {
	if err := d.gate(); err != nil {
		return nil, false, err
	}
	defer d.waiting()()
	conn, err := d.direct.Acquire(ctx)
	if err != nil {
		return nil, false, d.note(err)
	}
	key := lockKey(name)
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil || !got {
		conn.Release()
		return nil, false, d.note(err)
	}
	return &Lock{conn: conn, key: key}, true, nil
}

// Unlock gives the lock back.
func (l *Lock) Unlock() {
	if l == nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", l.key); err != nil {
		// The session may be gone, and the lock with it; a connection
		// whose state is unknown does not go back to the pool.
		_ = l.conn.Conn().Close(ctx)
	}
	l.conn.Release()
	l.conn = nil
}

// Sweep runs fn if no other replica is running the sweep of that name, and
// says whether it ran. A sweep does the same when run twice, so losing the
// lock half way (the connection dropped) does no harm.
func (d *DB) Sweep(ctx context.Context, name string, fn func(ctx context.Context) error) (bool, error) {
	l, ok, err := d.TryLock(ctx, name)
	if err != nil || !ok {
		return false, err
	}
	defer l.Unlock()
	return true, fn(ctx)
}

// Claim records that this replica takes the piece of work job has at slot,
// and says whether it was first: every replica tries, one insert goes in.
// The row stays for a while as the record of who ran it (the retention
// sweep removes old ones), so a replica that restarts does not run the
// slot again.
func (d *DB) Claim(ctx context.Context, q Querier, jobID string, slot time.Time, by string) (bool, error) {
	tag, err := q.Exec(ctx, `INSERT INTO schedule_claims (job_id, slot_at, claimed_by) VALUES ($1, $2, $3)
		ON CONFLICT (job_id, slot_at) DO NOTHING`, jobID, slot.UTC(), by)
	if err != nil {
		return false, d.note(err)
	}
	return tag.RowsAffected() == 1, nil
}

// ForgetClaims removes the claims older than before, and says how many.
func (d *DB) ForgetClaims(ctx context.Context, before time.Time) (int64, error) {
	if err := d.gate(); err != nil {
		return 0, err
	}
	tag, err := d.pool.Exec(ctx, "DELETE FROM schedule_claims WHERE slot_at < $1", before.UTC())
	return tag.RowsAffected(), d.note(err)
}

// Notify announces that something under topic changed. Sent on a
// transaction, it is delivered when the transaction commits and not at
// all when it does not.
func (d *DB) Notify(ctx context.Context, q Querier, topic string) error {
	_, err := q.Exec(ctx, "SELECT pg_notify($1, $2)", channel, topic)
	return err
}

// Subscribe calls fn when a notice for topic arrives, when the listener
// reconnects (notices sent meanwhile were missed), and every Poll anyway.
// fn is called on the listener's goroutine one at a time, so it should be
// quick: note that a reload is due and return.
func (d *DB) Subscribe(topic string, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs[topic] = append(d.subs[topic], fn)
}

func (d *DB) call(topic string) {
	d.mu.Lock()
	var fns []func()
	if topic == "" {
		for _, s := range d.subs {
			fns = append(fns, s...)
		}
	} else {
		fns = append(fns, d.subs[topic]...)
	}
	d.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Listen hears the notices until ctx ends, on one direct connection it
// opens again whenever it drops.
func (d *DB) Listen(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		if !d.Health().Available {
			// Nothing is tried while the database is away; it is tried at
			// once when it is back.
			select {
			case <-ctx.Done():
				return
			case <-time.After(probeEvery / 4):
			}
			wait = time.Second
			continue
		}
		err := d.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		if unreachable(err) {
			// The listener's connection is the one always open: its loss
			// is often the first sign that the database is gone.
			d.doubt(describe(err))
		}
		d.log.Warn("the database listener stopped; trying again", "err", err, "in", wait.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (d *DB) listen(ctx context.Context) error {
	conn, err := d.direct.Acquire(ctx)
	if err != nil {
		return err
	}
	// The connection is not put back listening: it is closed, so the pool
	// opens a clean one.
	defer func() {
		c := conn.Hijack()
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return err
	}
	// What was announced while nobody listened was missed.
	d.call("")
	for {
		wctx, cancel := context.WithTimeout(ctx, d.cfg.Poll)
		n, err := conn.Conn().WaitForNotification(wctx)
		cancel()
		switch {
		case err == nil:
			d.call(n.Payload)
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			// Nothing was announced for a while: look anyway, and make
			// sure the connection is still there.
			pctx, cancel := context.WithTimeout(ctx, probeTime)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
			d.call("")
		default:
			return err
		}
	}
}
