package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// While the database is away, nothing a request does waits for it (DB-14).
//
// The health is the one thing every call looks at first (gate): while it
// says the database is away, a call returns ErrUnavailable at once, without
// taking a connection. Only a probe changes the health: a connection of the
// console's own, made for the question and closed, given probeTime. A call
// that did not reach the database (a connection refused or broken, or the
// call's own time run out) changes nothing by itself: it asks for a probe.
// So:
//
//   - a database that is gone is found away by the first probe that fails,
//     probeTime at most after the first call that met it, and from then on
//     no call waits. When the probe fails, every connection the console has
//     open is closed and every connection being made is given up, so the
//     calls that were already waiting return at once too, whatever time
//     they were given, or none;
//   - a slow query on a database that answers costs that query its own
//     time and a probe, which answers: the health does not change and no
//     other call is refused;
//   - while the database is away the probe is made again every probeEvery,
//     by the console and not by a request, and the first one that is
//     answered brings the database back: the calls go through again, the
//     pools make new connections, the listener listens again, and what
//     waited is told (OnAvailable).

const (
	// probeTime is the longest a probe may take: making a connection and
	// signing in. It is what the first request to meet a database that
	// swallows what it is sent may wait, after its own time.
	probeTime = 2 * time.Second
	// probeEvery is the pause between two probes while the database is
	// away, and the time a probe's answer stands: a call that fails within
	// it asks for no other.
	probeEvery = time.Second
)

// unavailableError is what a call returns while the database cannot be
// reached. It reads as ErrUnavailable and nothing else: the driver's text
// (the database's user, its name, its address) is for the log, never for
// the sentence a person is shown or an audit record keeps. The cause can
// still be asked about (errors.Is, for a time that ran out).
type unavailableError struct{ cause error }

func (e *unavailableError) Error() string { return ErrUnavailable.Error() }

func (e *unavailableError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrUnavailable}
	}
	return []error{ErrUnavailable, e.cause}
}

func unavailable(cause error) error { return &unavailableError{cause: cause} }

// gate is asked before every call: while the database is away it returns
// ErrUnavailable at once.
func (d *DB) gate() error {
	d.mu.Lock()
	ok := d.health.Available
	d.mu.Unlock()
	if !ok {
		return unavailable(nil)
	}
	return nil
}

// doubt is told of a call that did not reach the database: a probe finds
// out whether the database is there, unless one is at it or one was
// answered a moment ago.
func (d *DB) doubt(cause error) { d.doubtAfter(cause, probeEvery) }

// doubtAfter is doubt with how long a probe's answer stands.
func (d *DB) doubtAfter(cause error, stands time.Duration) {
	d.mu.Lock()
	if d.probing || d.closed || time.Since(d.answered) < stands {
		d.mu.Unlock()
		return
	}
	d.probing = true
	d.mu.Unlock()
	// The driver's own words, which no error's text carries, are here.
	d.log.Info("a call did not reach the database; asking whether it is there", "err", cause)
	go d.probeUntilAnswered()
}

// What a probe found.
const (
	answers = iota
	// silent: no connection could be made.
	silent
	// unsure: nothing can be said (the server takes no more connections,
	// which the ones the console holds may not feel).
	unsure
)

// probeUntilAnswered probes the database: once when it turns out to be
// there, and otherwise every probeEvery until it is.
func (d *DB) probeUntilAnswered() {
	for {
		found, err := d.probe()
		d.mu.Lock()
		away := !d.health.Available
		if found == answers {
			d.answered = time.Now()
		}
		if found == answers || (found == unsure && !away) || d.closed {
			d.probing = false
			d.mu.Unlock()
			if found == answers && away {
				d.allowConnections()
				d.set(true, "", "")
			}
			return
		}
		d.mu.Unlock()
		if found == silent {
			d.set(false, reason(err), describe(err).Error())
			if !away {
				d.dropConnections()
			}
		}
		select {
		case <-d.done:
			d.mu.Lock()
			d.probing = false
			d.mu.Unlock()
			return
		case <-time.After(probeEvery):
		}
	}
}

// probeKey marks the probe's own connection, which is made also while the
// database is away.
type probeKey struct{}

// probe makes a connection of its own to the database, signs in and closes
// it. It does not use the pools: a pool whose connections are all waiting
// says nothing of the database.
func (d *DB) probe() (int, error) {
	cc := d.pool.Config().ConnConfig
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), probeKey{}, true), probeTime)
	defer cancel()
	if err := d.password(ctx, cc); err != nil {
		return unsure, err
	}
	cc.ConnectTimeout = probeTime
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err == nil {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
		_ = conn.Close(cctx)
		ccancel()
		return answers, nil
	}
	var pg *pgconn.PgError
	var dns *net.DNSError
	switch {
	case errors.As(err, &pg) && pg.Code == "53300":
		// It takes no more connections; the ones the console holds may
		// work as ever.
		return unsure, err
	case errors.As(err, &dns):
		// The name was not resolved: that says nothing of the database,
		// and the connections the console holds do not need the name.
		return unsure, err
	}
	// No connection, or a sign-in the server refuses (a password changed
	// under the console, a database that is not there): either way the
	// console has no database until a probe is answered.
	return silent, err
}

// reason says why the database is away in the console's words, for the
// health a page shows. The driver's own text is logged.
func reason(err error) string {
	var pg *pgconn.PgError
	var ne net.Error
	switch {
	case errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "28") || strings.HasPrefix(pg.Code, "3D")):
		// The server's own sentence names the user or the database.
		return fmt.Sprintf("it refuses the console's sign-in (PostgreSQL %s)", pg.Code)
	case errors.As(err, &pg):
		return fmt.Sprintf("%s (PostgreSQL %s)", pg.Message, pg.Code)
	case errors.Is(err, syscall.ECONNREFUSED):
		return "it refuses connections"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "it does not answer"
	}
	return "no connection can be made to it"
}

// errAwayDial is why no connection is made while the database is away.
var errAwayDial = errors.New("the database is away, and no connection is made until it answers a probe")

// dial is how the pools and the probe open a connection: through next,
// kept until it is closed so that it can be closed from here, and given up
// when the database turns out to be away while it is being made.
func (d *DB) dial(next pgconn.DialFunc) pgconn.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		probe := ctx.Value(probeKey{}) != nil
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		d.connMu.Lock()
		if d.dropped && !probe {
			d.connMu.Unlock()
			return nil, errAwayDial
		}
		d.dialSeq++
		id := d.dialSeq
		if !probe {
			d.dialing[id] = cancel
		}
		d.connMu.Unlock()
		c, err := next(ctx, network, addr)
		d.connMu.Lock()
		defer d.connMu.Unlock()
		delete(d.dialing, id)
		if err != nil {
			return nil, err
		}
		if d.dropped && !probe {
			_ = c.Close()
			return nil, errAwayDial
		}
		t := &trackedConn{Conn: c, d: d}
		d.conns[t] = struct{}{}
		return t, nil
	}
}

// trackedConn is a connection to the database the console can close from
// outside the call that uses it.
type trackedConn struct {
	net.Conn
	d *DB
}

func (t *trackedConn) Close() error {
	t.d.connMu.Lock()
	delete(t.d.conns, t)
	t.d.connMu.Unlock()
	return t.Conn.Close()
}

// dropConnections closes every connection to the database and gives up
// the ones being made, when a probe has found the database away: a call
// that was waiting on one returns at once, and none is made until the
// database is back (allowConnections).
func (d *DB) dropConnections() {
	d.connMu.Lock()
	d.dropped = true
	conns := make([]net.Conn, 0, len(d.conns))
	for t := range d.conns {
		conns = append(conns, t.Conn)
	}
	for _, cancel := range d.dialing {
		cancel()
	}
	d.connMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	d.pool.Reset()
	d.direct.Reset()
}

func (d *DB) allowConnections() {
	d.connMu.Lock()
	d.dropped = false
	d.connMu.Unlock()
}

// slowCall is how long a call may wait before a probe is asked whether the
// database is there at all, and again every slowCall for as long as it
// goes on waiting; slowAgain is how long a probe's answer then stands. So
// a query that is merely slow goes on, at the cost of a probe every
// slowAgain while it runs, and a database that is gone is found although
// the call was given no time of its own.
const (
	slowCall  = 2 * time.Second
	slowAgain = 3 * time.Second
)

var errSlow = errors.New("a call has waited for the database for " + slowCall.String())

// waiting is called when a call begins, and what it returns when the call
// ends.
func (d *DB) waiting() func() {
	var mu sync.Mutex
	var t *time.Timer
	ended := false
	var again func()
	again = func() {
		mu.Lock()
		over := ended
		mu.Unlock()
		if over {
			return
		}
		d.doubtAfter(errSlow, slowAgain)
		mu.Lock()
		if !ended {
			t = time.AfterFunc(slowCall, again)
		}
		mu.Unlock()
	}
	mu.Lock()
	t = time.AfterFunc(slowCall, again)
	mu.Unlock()
	return func() {
		mu.Lock()
		ended = true
		t.Stop()
		mu.Unlock()
	}
}
