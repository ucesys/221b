package leader

import "sync"

// Activity says what this replica does with the clusters (ADR-70). The
// leader reads them: it lists and watches every one, polls metrics-server
// and the kubelets, checks each connection and the deployed roles, and
// serves every data request. A standby polls nothing and serves no data;
// a warm one keeps its watches open, so it takes over at once, and a cold
// one starts them when it wins the Lease.
type Activity struct {
	warm bool
	// runMu serializes the hooks' calls, so each sees the changes in order
	// and a hook registered late is not told twice.
	runMu  sync.Mutex
	mu     sync.Mutex
	active bool
	hooks  []func(active bool)
}

// NewActivity prepares the switch: warm says whether a standby keeps its
// watches open. Nothing is active until Set says so.
func NewActivity(warm bool) *Activity { return &Activity{warm: warm} }

// Warm says whether a standby keeps watching.
func (a *Activity) Warm() bool { return a.warm }

// Active says whether this replica reads the clusters now.
func (a *Activity) Active() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// Standby says whether this replica is a cold standby: not active, and
// holding no watches. It is ready to serve as soon as it knows the leader.
func (a *Activity) Standby() bool { return !a.warm && !a.Active() }

// OnChange registers f, called with each change of Active; when the
// replica is already active, f is called with true at once, so a reader
// registered after the election was won does not miss it. f must not call
// back into the Activity.
func (a *Activity) OnChange(f func(active bool)) {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	a.mu.Lock()
	a.hooks = append(a.hooks, f)
	cur := a.active
	a.mu.Unlock()
	if cur {
		f(true)
	}
}

// Set records whether this replica leads; the hooks run, in the order they
// were registered, when that changes.
func (a *Activity) Set(active bool) {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	a.mu.Lock()
	if a.active == active {
		a.mu.Unlock()
		return
	}
	a.active = active
	hooks := append([]func(bool){}, a.hooks...)
	a.mu.Unlock()
	for _, f := range hooks {
		f(active)
	}
}
