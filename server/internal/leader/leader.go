// Package leader picks the one replica that runs Sherlock and Watson
// (P7-02). The replicas hold a Kubernetes Lease in the console's namespace;
// the holder's identity names its pod and the address of its HTTP server,
// so the others know where to send those requests.
package leader

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Options configure an election.
type Options struct {
	// Client reaches the Lease; nil runs alone, as the leader.
	Client kubernetes.Interface
	// Namespace and Name are the Lease's.
	Namespace, Name string
	// Pod is this replica's pod name, and Addr the host:port the other
	// replicas reach its HTTP server on (the pod IP and the listen port).
	Pod, Addr string
	// LeaseDuration, RenewDeadline and RetryPeriod tune the election; zero
	// takes 15s, 10s and 2s. A replica that stops renewing loses the lease
	// after LeaseDuration; one that shuts down gives it up at once.
	LeaseDuration, RenewDeadline, RetryPeriod time.Duration
	// OnStartedLeading and OnStoppedLeading are told when this replica
	// becomes the leader and when it stops being it.
	OnStartedLeading, OnStoppedLeading func()
	Logger                             *slog.Logger
}

// Elector says whether this replica leads and where the leader is.
type Elector struct {
	o      Options
	single bool
	leads  atomic.Bool
	mu     sync.Mutex
	holder string
}

// New prepares an election. Without a client it is a single replica that
// always leads.
func New(o Options) *Elector {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 15 * time.Second
	}
	if o.RenewDeadline <= 0 {
		o.RenewDeadline = 10 * time.Second
	}
	if o.RetryPeriod <= 0 {
		o.RetryPeriod = 2 * time.Second
	}
	e := &Elector{o: o, single: o.Client == nil}
	if e.single {
		e.leads.Store(true)
	}
	return e
}

// Single says whether there is no election: one replica that always leads.
func (e *Elector) Single() bool { return e.single }

// IsLeader says whether this replica runs Sherlock and Watson now.
func (e *Elector) IsLeader() bool { return e.leads.Load() }

// LeaderAddr is the host:port of the leader's HTTP server, or "" when no
// replica holds the lease (or in a single replica, which never forwards).
func (e *Elector) LeaderAddr() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, addr := ParseIdentity(e.holder)
	return addr
}

// Identity is what a replica writes as the Lease's holder: its pod name
// and the address of its HTTP server, "pod@10.42.0.7:8080".
func Identity(pod, addr string) string { return pod + "@" + addr }

// ParseIdentity reads an Identity back; anything else has no address.
func ParseIdentity(id string) (pod, addr string) {
	pod, addr, ok := strings.Cut(id, "@")
	if !ok {
		return id, ""
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return pod, ""
	}
	return pod, addr
}

// Run takes part in the election until ctx is done, standing again after
// losing the lease; on the way out the lease is given up, so another
// replica takes over at once rather than after LeaseDuration. A single
// replica returns at once.
func (e *Elector) Run(ctx context.Context) error {
	if e.single {
		return nil
	}
	if e.o.Pod == "" || e.o.Addr == "" {
		return fmt.Errorf("leader election needs the pod's name and address (POD_NAME and POD_IP)")
	}
	lock, err := resourcelock.New(resourcelock.LeasesResourceLock, e.o.Namespace, e.o.Name, e.o.Client.CoreV1(), e.o.Client.CoordinationV1(), resourcelock.ResourceLockConfig{Identity: Identity(e.o.Pod, e.o.Addr)})
	if err != nil {
		return err
	}
	le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   e.o.LeaseDuration,
		RenewDeadline:   e.o.RenewDeadline,
		RetryPeriod:     e.o.RetryPeriod,
		ReleaseOnCancel: true,
		Name:            e.o.Name,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(context.Context) {
				e.leads.Store(true)
				e.o.Logger.Info("leading: Sherlock and Watson run here", "lease", e.o.Name)
				if e.o.OnStartedLeading != nil {
					e.o.OnStartedLeading()
				}
			},
			OnStoppedLeading: func() {
				if !e.leads.Swap(false) {
					return
				}
				e.o.Logger.Info("no longer leading", "lease", e.o.Name)
				if e.o.OnStoppedLeading != nil {
					e.o.OnStoppedLeading()
				}
			},
			OnNewLeader: func(id string) {
				e.mu.Lock()
				e.holder = id
				e.mu.Unlock()
				pod, addr := ParseIdentity(id)
				e.o.Logger.Info("leader", "pod", pod, "addr", addr)
			},
		},
	})
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		// Run returns when the lease is lost or ctx is done.
		le.Run(ctx)
	}
	return nil
}
