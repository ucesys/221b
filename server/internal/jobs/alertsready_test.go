package jobs

import (
	"context"
	"sync"
	"testing"
)

// A start must not fire a run for every standing alert (kube#13): a
// cluster not listed yet reads as having no alerts, and the alerts it
// lists later are noted, not fired. Once a look noted all of them, a new
// one fires; a cluster whose connection restarts is noted again first.
func TestStandingAlertsOfAClusterNotListedYetDoNotFire(t *testing.T) {
	w := setup(t)
	var mu sync.Mutex
	var alerts []Alert
	ready := map[string]bool{"local": true}
	w.s.o.Alerts = func(context.Context) []Alert {
		mu.Lock()
		defer mu.Unlock()
		return append([]Alert(nil), alerts...)
	}
	w.s.o.AlertsReady = func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return ready[id]
	}
	set := func(f func()) {
		mu.Lock()
		f()
		mu.Unlock()
	}
	ctx := context.Background()
	on(t, w.s, "test", Change{When: &When{Event: EventAlert, Severity: SeverityWarning}})
	look := func() {
		w.s.Tick(ctx)
		w.s.Wait()
	}
	// prod is unreachable at the start: no alerts, not ready.
	look()
	look()
	// prod lists: its standing alerts appear while it is still not
	// complete, then it is.
	set(func() {
		for _, name := range []string{"a", "b", "c"} {
			alerts = append(alerts, Alert{Cluster: "prod", Key: "pod:shop/" + name, Kind: "pod", Severity: SeverityError, Name: name, Namespace: "shop", Status: "CrashLoopBackOff"})
		}
	})
	look()
	set(func() { ready["prod"] = true })
	look()
	look()
	if n := w.runs.Load(); n != 0 {
		t.Fatalf("%d runs for alerts that stood before the cluster was listed", n)
	}
	// A new one fires.
	set(func() {
		alerts = append(alerts, Alert{Cluster: "prod", Key: "pod:shop/new", Kind: "pod", Severity: SeverityError, Name: "new", Namespace: "shop", Status: "CrashLoopBackOff"})
	})
	look()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("runs for a new alert: %d", n)
	}
	// The connection restarts: its store is empty and not complete, then
	// lists again; nothing fires.
	set(func() { alerts, ready["prod"] = nil, false })
	look()
	set(func() {
		for _, name := range []string{"d", "e"} {
			alerts = append(alerts, Alert{Cluster: "prod", Key: "pod:shop/" + name, Kind: "pod", Severity: SeverityError, Name: name, Namespace: "shop", Status: "CrashLoopBackOff"})
		}
	})
	look()
	set(func() { ready["prod"] = true })
	look()
	look()
	if n := w.runs.Load(); n != 1 {
		t.Fatalf("%d runs after the connection restarted", n)
	}
}
