package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/leader"
)

// Several replicas (P7-02, ADR-70): the election of the one that leads.

// wireLeader joins the replicas' election.
func (w *wiring) wireLeader(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Several replicas (P7-02): the one holding the Lease runs Sherlock
	// and Watson, and the others forward those requests to it. Without a
	// Lease this is the only replica, and always the leader.
	// Becoming the leader is signalled here; the shared state Sherlock and
	// Watson rely on is read again before they serve (wireSherlock).
	leading := make(chan struct{}, 1)
	// A cold standby that loses the Lease cannot stand by again with its
	// watches stopped: it exits, and the pod starts afresh.
	lost := make(chan struct{})
	var lostOnce sync.Once
	onLead, onStop := roleSwitch(w.activity, func() { lostOnce.Do(func() { close(lost) }) })
	elector, err := newElector(cfg, w.restCfg, log, func() {
		onLead()
		select {
		case leading <- struct{}{}:
		default:
		}
	}, onStop)
	if err != nil {
		return err
	}
	if elector.Single() {
		w.activity.Set(true)
	}
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		if err := elector.Run(ctx); err != nil {
			log.Error("leader election stopped", "err", err)
		}
	}()
	w.leading, w.lost, w.elector, w.leaderDone = leading, lost, elector, leaderDone
	return nil
}

// roleSwitch is what the election drives (ADR-70): winning the Lease makes
// the replica active, and losing it makes it a standby. A cold standby
// cannot stand by again, since its informers cannot restart, so losing
// the Lease makes it exit instead; a warm one only stops its pollers.
func roleSwitch(act *leader.Activity, exit func()) (onLead, onStop func()) {
	return func() { act.Set(true) }, func() {
		act.Set(false)
		if !act.Warm() {
			exit()
		}
	}
}

// newElector joins the election of -leader-lease, or stands alone. The
// leader's address is the pod's IP and the port it listens on.
func newElector(cfg config.Config, restCfg *rest.Config, log *slog.Logger, onLead, onStop func()) (*leader.Elector, error) {
	if cfg.LeaderLease == "" {
		return leader.New(leader.Options{Logger: log.With("component", "leader"), OnStartedLeading: onLead, OnStoppedLeading: onStop}), nil
	}
	ns, name, _ := strings.Cut(cfg.LeaderLease, "/")
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("-listen %q: %w", cfg.Listen, err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return leader.New(leader.Options{Client: client, Namespace: ns, Name: name, Pod: cfg.PodName, Addr: net.JoinHostPort(cfg.PodIP, port), Logger: log.With("component", "leader"), OnStartedLeading: onLead, OnStoppedLeading: onStop}), nil
}
