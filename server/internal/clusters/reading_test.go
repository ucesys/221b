package clusters

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/leader"
	"github.com/ucesys/221b/server/internal/live"
)

// fakeReader records what the role switch asks of a connection.
type fakeReader struct {
	mu       sync.Mutex
	watches  int
	pollers  []context.Context
	watching bool
}

func (r *fakeReader) RunWatches(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.watching {
		r.watching = true
		r.watches++
	}
}

func (r *fakeReader) RunPollers(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pollers = append(r.pollers, ctx)
}

func (r *fakeReader) state() (watches, pollers, stopped int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.pollers {
		if c.Err() != nil {
			stopped++
		}
	}
	return r.watches, len(r.pollers), stopped
}

// A warm standby watches from the start and polls only while it leads; a
// cold one does nothing until it leads, then watches and polls; losing
// the Lease stops the pollers and leaves the watches (ADR-70).
func TestAReadingFollowsTheReplicasRole(t *testing.T) {
	for _, warm := range []bool{true, false} {
		act := leader.NewActivity(warm)
		rd := &fakeReader{}
		ctx, cancel := context.WithCancel(context.Background())
		r := newReading(ctx, rd, live.New(fake.New("a"), live.Options{Refresh: time.Hour, Active: act.Active}), act)
		if w, p, _ := rd.state(); w != boolInt(warm) || p != 0 {
			t.Fatalf("warm=%v: before leading, %d watches and %d pollers", warm, w, p)
		}
		act.Set(true)
		r.set(true)
		if w, p, s := rd.state(); w != 1 || p != 1 || s != 0 {
			t.Fatalf("warm=%v: leading, %d watches, %d pollers, %d stopped", warm, w, p, s)
		}
		r.set(true) // the same again does nothing
		act.Set(false)
		r.set(false)
		if w, p, s := rd.state(); w != 1 || p != 1 || s != 1 {
			t.Fatalf("warm=%v: after losing the Lease, %d watches, %d pollers, %d stopped", warm, w, p, s)
		}
		cancel()
		r.set(true) // too late: the connection is gone
		if _, p, _ := rd.state(); p != 1 {
			t.Fatalf("warm=%v: a cancelled reading started pollers", warm)
		}
	}
}

// The manager passes the role to every connection, those started after
// the election was won included.
func TestTheManagerFansTheRoleOutToEveryConnection(t *testing.T) {
	act := leader.NewActivity(true)
	m := New(NewMemoryStore(), Options{Activity: act, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	early := &fakeReader{}
	if err := m.SetLocal(ctx, Spec{Name: "Here"}, readerClient{early}, live.New(fake.New("a"), live.Options{Active: act.Active})); err != nil {
		t.Fatal(err)
	}
	act.Set(true)
	if _, p, _ := early.state(); p != 1 {
		t.Fatalf("the local connection did not start polling on leading: %d pollers", p)
	}
	late := &fakeReader{}
	m.mu.Lock()
	m.instances["late"] = &Instance{Spec: Spec{ID: "late"}, reading: newReading(ctx, late, live.New(fake.New("b"), live.Options{Active: act.Active}), act)}
	m.mu.Unlock()
	if _, p, _ := late.state(); p != 1 {
		t.Fatalf("a connection started while leading did not poll: %d pollers", p)
	}
	act.Set(false)
	for name, rd := range map[string]*fakeReader{"early": early, "late": late} {
		if _, p, s := rd.state(); p != 1 || s != 1 {
			t.Fatalf("%s after losing the Lease: %d pollers, %d stopped", name, p, s)
		}
	}
}

// readerClient is a Client that is also a Reader, as *kube.Cluster is.
type readerClient struct{ *fakeReader }

func (readerClient) Info() kube.Info                    { return kube.Info{} }
func (readerClient) Counts() (int, int, uint64, uint64) { return 0, 0, 0, 0 }
func (readerClient) RestConfig() *rest.Config           { return nil }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
