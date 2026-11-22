package db_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
)

// A failure that repeats while the database is away is said once, and
// once when it works again; another error is said each time (DB-15).
func TestQuietSaysAnOutageOnce(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var q db.Quiet
	q.Worked(log, "read again")
	for range 5 {
		q.Failed(log, "reading failed", fmt.Errorf("reading x: %w", db.ErrUnavailable), "part", "x")
	}
	q.Failed(log, "reading failed", errors.New("bad JSON"))
	q.Failed(log, "reading failed", errors.New("bad JSON"))
	q.Worked(log, "read again")
	q.Worked(log, "read again")
	q.Failed(log, "reading failed", db.ErrUnavailable)
	out := buf.String()
	if n := strings.Count(out, "reading failed"); n != 4 {
		t.Errorf("said %d times:\n%s", n, out)
	}
	if n := strings.Count(out, "read again"); n != 1 {
		t.Errorf("the recovery said %d times:\n%s", n, out)
	}
}
