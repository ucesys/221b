package db

import (
	"log/slog"
	"sync"
)

// Quiet keeps a failure that repeats while the database is away out of
// the log after its first time (DB-15): a part that reads the database
// every few seconds would otherwise say the same line every few seconds
// for as long as the outage lasts (about 200 lines a minute in the ninth
// cluster test). The first failure is said, then nothing until the read
// works again, which is said once. Any other error is said each time.
// The zero value is ready; it is safe for concurrent use.
type Quiet struct {
	mu   sync.Mutex
	away bool
}

// Failed says msg with err at Warn: each time for an error that is not
// the database being away, and for that one only the first time since it
// last worked (then at Debug).
func (q *Quiet) Failed(log *slog.Logger, msg string, err error, args ...any) {
	args = append(args, "err", err)
	if !IsUnavailable(err) {
		log.Warn(msg, args...)
		return
	}
	q.mu.Lock()
	first := !q.away
	q.away = true
	q.mu.Unlock()
	if first {
		log.Warn(msg, append(args, "repeats", "said again only once it works")...)
		return
	}
	log.Debug(msg, args...)
}

// Worked says msg at Info once after Failed kept the database's absence
// quiet; otherwise nothing.
func (q *Quiet) Worked(log *slog.Logger, msg string, args ...any) {
	q.mu.Lock()
	was := q.away
	q.away = false
	q.mu.Unlock()
	if was {
		log.Info(msg, args...)
	}
}
