package dbtest

import (
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
)

// Away stands between a test and its PostgreSQL, so the test can take the
// database away in the middle and bring it back: a TCP relay the test's
// db.Config is pointed at. It starts relaying.
//
// Cut is a database that is not there: every connection is ended, and a
// new one is ended as soon as it is made. Hang is one that swallows what
// it is sent: the connections stay open and nothing is ever answered,
// which is what a network that drops packets looks like, so a call lasts
// until its own time runs out. Back relays again.
type Away struct {
	ln     net.Listener
	target string

	mu   sync.Mutex
	mode int
	// pairs are the relayed connections, client side to server side, and
	// held the client sides kept open and unanswered while hanging.
	pairs map[net.Conn]net.Conn
	held  []net.Conn
	done  bool
}

const (
	relaying = iota
	cut
	hanging
)

// NewAway starts a relay to the server cfg names and returns it with cfg
// pointed at the relay. It stops when the test ends.
func NewAway(t testing.TB, cfg db.Config) (*Away, db.Config) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the relay to the test database: %v", err)
	}
	a := &Away{ln: ln, target: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), pairs: map[net.Conn]net.Conn{}}
	cfg.Host, cfg.Port = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(a.stop)
	go a.accept()
	return a, cfg
}

func (a *Away) accept() {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		switch {
		case a.done || a.mode == cut:
			a.mu.Unlock()
			_ = c.Close()
			continue
		case a.mode == hanging:
			a.held = append(a.held, c)
			a.mu.Unlock()
			continue
		}
		a.mu.Unlock()
		s, err := net.Dial("tcp", a.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		a.mu.Lock()
		if a.done || a.mode != relaying {
			a.mu.Unlock()
			_ = c.Close()
			_ = s.Close()
			continue
		}
		a.pairs[c] = s
		a.mu.Unlock()
		go a.relay(c, s)
	}
}

func (a *Away) relay(c, s net.Conn) {
	go func() { _, _ = io.Copy(s, c); _ = s.Close() }()
	_, _ = io.Copy(c, s)
	// The server's side ended. While hanging the client is told nothing:
	// its side stays open (Hang keeps it).
	a.mu.Lock()
	_, mine := a.pairs[c]
	delete(a.pairs, c)
	a.mu.Unlock()
	if mine {
		_ = c.Close()
	}
	_ = s.Close()
}

// Cut takes the database away: nothing connects until Back.
func (a *Away) Cut() {
	a.mu.Lock()
	a.mode = cut
	for c, s := range a.pairs {
		_ = c.Close()
		_ = s.Close()
	}
	clear(a.pairs)
	held := a.held
	a.held = nil
	a.mu.Unlock()
	for _, c := range held {
		_ = c.Close()
	}
}

// Hang makes the database swallow what it is sent: connections open and
// stay open, and nothing is answered until Back.
func (a *Away) Hang() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = hanging
	for c, s := range a.pairs {
		// The client's side is kept, unanswered; relay finds it no longer
		// its own and leaves it open.
		a.held = append(a.held, c)
		_ = s.Close()
	}
	clear(a.pairs)
}

// Back brings the database back. The connections made meanwhile are
// ended; the pool makes new ones.
func (a *Away) Back() {
	a.mu.Lock()
	a.mode = relaying
	held := a.held
	a.held = nil
	a.mu.Unlock()
	for _, c := range held {
		_ = c.Close()
	}
}

func (a *Away) stop() {
	a.mu.Lock()
	a.done = true
	a.mu.Unlock()
	_ = a.ln.Close()
	a.Cut()
}
