package clusters

import (
	"context"
	"sync"

	"github.com/ucesys/221b/server/internal/live"
)

// Activity says what this replica does with the clusters (ADR-70);
// *leader.Activity implements it. nil is a single replica, which reads.
type Activity interface {
	Active() bool
	Warm() bool
	OnChange(func(active bool))
}

// Reader is a connection whose reading the activity switches on and off:
// a *kube.Cluster. The fakes in tests need not be one, and are left alone.
type Reader interface {
	// RunWatches starts the informers, once; RunPollers starts the health
	// check and the kubelet reads until its context ends.
	RunWatches(ctx context.Context)
	RunPollers(ctx context.Context)
}

// reading runs one connection's watches and pollers as the activity says
// (ADR-70): the leader watches and polls; a warm standby only watches; a
// cold standby holds nothing until it leads. The store runs throughout,
// polling the connection only while it leads.
type reading struct {
	ctx     context.Context
	cluster Reader
	store   *live.Store
	warm    bool

	mu      sync.Mutex
	polling context.CancelFunc
}

// newReading starts what the current role needs; ctx bounds all of it.
func newReading(ctx context.Context, cl Reader, st *live.Store, act Activity) *reading {
	r := &reading{ctx: ctx, cluster: cl, store: st, warm: act == nil || act.Warm()}
	go st.Run(ctx)
	if r.warm {
		cl.RunWatches(ctx)
	}
	r.set(act == nil || act.Active())
	return r
}

// set starts the pollers (and, on a cold standby, the watches) on
// becoming active, and stops the pollers on ceasing to be; the same state
// again does nothing.
func (r *reading) set(active bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return
	}
	if !active {
		if r.polling != nil {
			r.polling()
			r.polling = nil
		}
		return
	}
	if r.polling != nil {
		return
	}
	// A cold standby's first watches; for anyone else this does nothing.
	r.cluster.RunWatches(r.ctx)
	ctx, cancel := context.WithCancel(r.ctx)
	r.polling = cancel
	r.cluster.RunPollers(ctx)
	// Usage figures without waiting for the next tick.
	r.store.RefreshNow()
}
