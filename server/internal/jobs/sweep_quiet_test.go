package jobs

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// awayBackend fails every read and write as the database being away while
// away is set.
type awayBackend struct {
	kv.Backend
	away atomic.Bool
}

func (b *awayBackend) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	if b.away.Load() {
		return nil, "", fmt.Errorf("reading %s: %w", key, db.ErrUnavailable)
	}
	return b.Backend.LoadKey(ctx, key)
}

func (b *awayBackend) SaveKey(ctx context.Context, key string, v []byte, version string) (string, error) {
	if b.away.Load() {
		return "", fmt.Errorf("saving %s: %w", key, db.ErrUnavailable)
	}
	return b.Backend.SaveKey(ctx, key, v, version)
}

// With the database away, the sweep of the runs a replica left under way
// says so once, and once when it works again (DB-15; the thirteenth
// cluster test's N3: "the runs cannot be swept" at every sweep).
func TestTheSweepSaysAnOutageOnce(t *testing.T) {
	b := &awayBackend{Backend: testStore(t)}
	w := setupWith(t, b)
	var log bytes.Buffer
	w.s.o.Logger = slog.New(slog.NewTextHandler(&log, nil))
	ctx := context.Background()
	w.s.sweep(ctx)
	b.away.Store(true)
	for range 5 {
		w.clock.Set(w.clock.Now().Add(2 * time.Minute))
		w.s.sweep(ctx)
	}
	if n := strings.Count(log.String(), "the runs cannot be swept"); n != 1 {
		t.Fatalf("the sweep said the outage %d times in five sweeps:\n%s", n, log.String())
	}
	b.away.Store(false)
	for range 2 {
		w.clock.Set(w.clock.Now().Add(2 * time.Minute))
		w.s.sweep(ctx)
	}
	if n := strings.Count(log.String(), "the runs are swept again"); n != 1 {
		t.Fatalf("the sweep said it works again %d times:\n%s", n, log.String())
	}
}
