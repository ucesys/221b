package clusters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// LocalID is the connection of the cluster the console runs in, or was
// started against.
const LocalID = "local"

// Client is a connection's Kubernetes client as the manager sees it: a
// *kube.Cluster, or a fake in tests.
type Client interface {
	Info() kube.Info
	Counts() (nodeCount, podCount int, nodeEvents, podEvents uint64)
	RestConfig() *rest.Config
}

// Instance is one connection with its watches.
type Instance struct {
	Spec    Spec
	Cluster Client
	Store   *live.Store
	cancel  context.CancelFunc
	// startErr is why the connection could not even be prepared (a
	// kubeconfig that cannot be read); then Cluster and Store are nil.
	startErr string
	// lastTest is the latest test's result, kept for the cluster page.
	lastTest   *TestResult
	lastTested time.Time
}

// Options configure the manager.
type Options struct {
	Refresh        time.Duration
	HealthInterval time.Duration
	KubeletStats   bool
	SecretsRead    bool
	Logger         *slog.Logger
	Now            func() time.Time
}

// Manager keeps every connection running: the local one the server was
// started with, and those added in the console (P3-09).
type Manager struct {
	store Store
	opts  Options
	log   *slog.Logger
	ctx   context.Context

	mu        sync.RWMutex
	instances map[string]*Instance
	// settings are the shared settings every store follows.
	settingsRefresh time.Duration
	settingsRaw     []byte
}

// New makes a manager over a store.
func New(store Store, o Options) *Manager {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Refresh <= 0 {
		o.Refresh = 3 * time.Second
	}
	return &Manager{store: store, opts: o, log: o.Logger, instances: map[string]*Instance{}, ctx: context.Background()}
}

// Store is the connection store, for the API's tests.
func (m *Manager) Store() Store { return m.store }

// SetLocal registers the cluster the server was started with as the
// connection "local", with the watches main built; its spec can be edited
// (name, environment, description) but not its credentials.
func (m *Manager) SetLocal(ctx context.Context, spec Spec, cluster Client, store *live.Store) error {
	spec.ID, spec.Local = LocalID, true
	if spec.Mode == "" {
		spec.Mode = ModeRead
	}
	if spec.Env == "" {
		spec.Env = EnvOther
	}
	// What was saved for it (a name or description changed in the console)
	// wins. A store that cannot be read yet (the API server is still coming
	// up) is not fatal: the installation's name stands until a restart.
	if saved, _, err := m.store.Get(ctx, LocalID); err == nil {
		spec.Name, spec.Env, spec.Description, spec.Added, spec.Updated = saved.Name, saved.Env, saved.Description, saved.Added, saved.Updated
	} else if !errors.Is(err, ErrNotFound) {
		m.log.Warn("the local connection's saved name could not be read; using the installation's", "err", err)
	}
	if spec.Added.IsZero() {
		spec.Added = m.opts.Now().UTC()
	}
	m.mu.Lock()
	m.instances[LocalID] = &Instance{Spec: spec, Cluster: cluster, Store: store}
	raw, refresh := m.settingsRaw, m.settingsRefresh
	m.mu.Unlock()
	if raw != nil && store != nil {
		store.SetSettings(refresh, raw)
	}
	return nil
}

// Start loads the saved connections and starts their watches. ctx bounds
// every connection's life. A store that cannot be read yet (the API
// server is still coming up) is tried again every few seconds in the
// background, so the console starts and the connections follow.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx
	if err := m.load(ctx); err != nil {
		m.log.Warn("the saved connections could not be read; trying again", "err", err)
		go func() {
			t := time.NewTicker(10 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := m.load(ctx); err == nil {
						return
					}
				}
			}
		}()
	}
	return nil
}

// load starts every saved connection not running yet.
func (m *Manager) load(ctx context.Context) error {
	specs, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	for _, s := range specs {
		if s.ID == LocalID {
			continue
		}
		if _, ok := m.Get(s.ID); ok {
			continue
		}
		_, cred, err := m.store.Get(ctx, s.ID)
		if err != nil {
			m.log.Warn("connection not loaded", "cluster", s.ID, "err", err)
			continue
		}
		m.start(s, cred)
	}
	m.mu.RLock()
	n := len(m.instances)
	m.mu.RUnlock()
	m.log.Info("connections loaded", "count", n, "storage", m.store.Kind())
	return nil
}

// start builds and runs an instance for a connection, replacing a running one.
func (m *Manager) start(spec Spec, cred Credential) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.instances[spec.ID]; old != nil && old.cancel != nil {
		old.cancel()
	}
	inst := &Instance{Spec: spec}
	cfg, err := RestConfig(spec, cred)
	if err != nil {
		inst.startErr = err.Error()
		m.instances[spec.ID] = inst
		m.log.Warn("connection cannot start", "cluster", spec.ID, "err", err)
		return inst
	}
	cluster, err := kube.New(cfg, kube.Options{
		Name:           spec.Name,
		Mode:           kube.ModeURL,
		HealthInterval: m.opts.HealthInterval,
		KubeletStats:   m.opts.KubeletStats,
		SecretsRead:    m.opts.SecretsRead,
		// Real API servers speak protobuf; simulators (plain HTTP) JSON only.
		Protobuf: strings.HasPrefix(spec.Server, "https://"),
		Logger:   m.log.With("cluster", spec.ID),
	})
	if err != nil {
		inst.startErr = err.Error()
		m.instances[spec.ID] = inst
		m.log.Warn("connection cannot start", "cluster", spec.ID, "err", err)
		return inst
	}
	store := live.New(cluster, live.Options{Refresh: m.opts.Refresh, Logger: m.log.With("cluster", spec.ID, "component", "store"), Now: m.opts.Now})
	if m.settingsRaw != nil {
		store.SetSettings(m.settingsRefresh, m.settingsRaw)
	}
	cluster.OnChange(store.Notify)
	ctx, cancel := context.WithCancel(m.ctx)
	inst.Cluster, inst.Store, inst.cancel = cluster, store, cancel
	go cluster.Run(ctx)
	go store.Run(ctx)
	m.instances[spec.ID] = inst
	m.log.Info("connection started", "cluster", spec.ID, "server", spec.Server, "auth", spec.Auth)
	return inst
}

// SetSettings passes the shared settings to every store, now and later.
func (m *Manager) SetSettings(refresh time.Duration, raw []byte) {
	m.mu.Lock()
	m.settingsRefresh, m.settingsRaw = refresh, raw
	stores := make([]*live.Store, 0, len(m.instances))
	for _, inst := range m.instances {
		if inst.Store != nil {
			stores = append(stores, inst.Store)
		}
	}
	m.mu.Unlock()
	for _, s := range stores {
		s.SetSettings(refresh, raw)
	}
}

// Get finds a running connection.
func (m *Manager) Get(id string) (*Instance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	return inst, ok
}

// Summary is a connection as the cluster menu and page show it: the spec
// and the state of its watches.
type Summary struct {
	Spec
	// State is connecting, connected, disconnected, or failed when the
	// connection could not even start.
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Since   time.Time `json:"since,omitempty"`
	Version string    `json:"version,omitempty"`
	Nodes   int       `json:"nodes"`
	Pods    int       `json:"pods"`
	Synced  bool      `json:"synced"`
	// Expiring says the credential expires within a week, Expired that it
	// has (P3-07).
	Expiring bool `json:"expiring,omitempty"`
	Expired  bool `json:"expired,omitempty"`
	// LastTest is the latest connection test, when one ran.
	LastTest   *TestResult `json:"lastTest,omitempty"`
	LastTested *time.Time  `json:"lastTested,omitempty"`
}

func (m *Manager) summarize(inst *Instance) Summary {
	s := Summary{Spec: inst.Spec}
	now := m.opts.Now()
	if inst.Spec.Cred.Expires != nil {
		s.Expired = inst.Spec.Cred.Expires.Before(now)
		s.Expiring = !s.Expired && inst.Spec.Cred.Expires.Before(now.Add(7*24*time.Hour))
	}
	if inst.Cluster == nil {
		s.State, s.Message = "failed", inst.startErr
	} else {
		info := inst.Cluster.Info()
		s.State, s.Message, s.Since, s.Version, s.Synced = info.State, info.Message, info.Since, info.Version, info.Synced
		// Synced once the rows are in the store: the cluster says so a
		// moment before, and a snapshot read then would be empty.
		if inst.Store != nil {
			s.Synced = s.Synced && inst.Store.Synced()
		}
		s.Nodes, s.Pods, _, _ = inst.Cluster.Counts()
	}
	if inst.lastTest != nil {
		s.LastTest = inst.lastTest
		t := inst.lastTested
		s.LastTested = &t
	}
	return s
}

// List summarizes every connection: the local one first, then by name.
func (m *Manager) List() []Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Summary, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, m.summarize(inst))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Summary describes one connection.
func (m *Manager) Summary(id string) (Summary, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	if !ok {
		return Summary{}, false
	}
	return m.summarize(inst), true
}

// Draft is what the editor sends: a spec with the credential fields it
// may carry. Credential fields are empty when the saved ones are kept.
type Draft struct {
	Spec
	Credential
}

// nameTaken says whether another connection has the name.
func (m *Manager) nameTaken(name, except string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, inst := range m.instances {
		if id != except && strings.EqualFold(inst.Spec.Name, name) {
			return true
		}
	}
	return false
}

// prepare validates a draft and inspects its credential, returning the
// spec to save and the credential (nil when the saved one is kept).
func (m *Manager) prepare(d Draft, orig *Spec, origCred Credential) (Spec, *Credential, error) {
	spec := d.Spec
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Server = strings.TrimSpace(spec.Server)
	now := m.opts.Now().UTC()
	if orig != nil {
		spec.ID, spec.Local, spec.Added = orig.ID, orig.Local, orig.Added
		if spec.Local {
			// The local cluster keeps its own connection; only how it is shown changes.
			spec.Server, spec.CA, spec.CAInfo, spec.Auth, spec.Cred = orig.Server, orig.CA, orig.CAInfo, orig.Auth, orig.Cred
		}
	} else {
		spec.ID = Slug(spec.Name)
		spec.Local = false
		spec.Added = now
	}
	spec.Updated = now
	if err := ValidateSpec(spec); err != nil {
		return Spec{}, nil, err
	}
	except := ""
	if orig != nil {
		except = orig.ID
	}
	if m.nameTaken(spec.Name, except) {
		return Spec{}, nil, invalid("name", fmt.Sprintf("Another cluster is already called “%s”.", spec.Name))
	}
	if orig == nil {
		if reserved[spec.ID] {
			return Spec{}, nil, invalid("name", "That name is reserved; choose another.")
		}
		if _, ok := m.Get(spec.ID); ok {
			return Spec{}, nil, invalid("name", fmt.Sprintf("Another cluster already has the identifier “%s”; choose a name that differs in more than punctuation.", spec.ID))
		}
	}
	if spec.Local {
		return spec, nil, nil
	}

	// The credential: new when any of its fields is given or the method
	// changed; otherwise the saved one, which must exist.
	given := !d.Credential.Empty()
	methodChanged := orig == nil || orig.Auth != spec.Auth
	var cred *Credential
	switch {
	case given || methodChanged:
		if !given {
			return Spec{}, nil, invalid("auth", "Give the credentials for the new sign-in method.")
		}
		c := d.Credential
		check, server, err := CheckCredential(spec.Auth, c, spec.Server, now)
		if err != nil {
			return Spec{}, nil, err
		}
		spec.Server = server
		if _, err := ServerURL(spec.Server); err != nil {
			return Spec{}, nil, invalid("server", err.Error())
		}
		spec.Cred = check.Info
		// The CA travels with the credential in the Secret.
		c.CAPEM = strings.TrimSpace(d.Credential.CAPEM)
		if c.CAPEM == "" && orig != nil && orig.CA == CAPEM && spec.CA == CAPEM {
			c.CAPEM = origCred.CAPEM
		}
		if c.CAPEM == "" && spec.Auth == AuthKubeconfig && spec.CA == CAPEM {
			c.CAPEM = kubeconfigCA(c.Kubeconfig, c.Context)
		}
		cred = &c
	default:
		c := origCred
		cred = &c
		spec.Cred = orig.Cred
	}
	// The CA certificate, when pasted anew or lifted from a kubeconfig.
	if cred != nil && spec.CA == CAPEM {
		if strings.TrimSpace(cred.CAPEM) == "" {
			return Spec{}, nil, invalid("caPem", "Paste the cluster’s CA certificate (PEM), or choose the system trust store.")
		}
		desc, err := CADescription([]byte(cred.CAPEM))
		if err != nil {
			return Spec{}, nil, invalid("caPem", "The CA certificate is not a certificate in PEM form.")
		}
		spec.CAInfo = desc
	} else if spec.CA != CAPEM {
		spec.CAInfo = ""
		if cred != nil {
			cred.CAPEM = ""
		}
	}
	return spec, cred, nil
}

// kubeconfigCA lifts the CA certificate of a kubeconfig's context.
func kubeconfigCA(kubeconfig, context string) string {
	kc, err := clientcmd.Load([]byte(kubeconfig))
	if err != nil {
		return ""
	}
	name := context
	if name == "" {
		name = kc.CurrentContext
	}
	ctx, ok := kc.Contexts[name]
	if !ok {
		return ""
	}
	cl := kc.Clusters[ctx.Cluster]
	if cl == nil {
		return ""
	}
	return string(cl.CertificateAuthorityData)
}

// Add saves and starts a new connection (P3-05).
func (m *Manager) Add(ctx context.Context, d Draft) (Summary, error) {
	spec, cred, err := m.prepare(d, nil, Credential{})
	if err != nil {
		return Summary{}, err
	}
	if err := m.store.Save(ctx, spec, cred); err != nil {
		return Summary{}, err
	}
	inst := m.start(spec, *cred)
	return m.summarize(inst), nil
}

// Update changes a connection; a credential given replaces the saved one.
func (m *Manager) Update(ctx context.Context, id string, d Draft) (Summary, error) {
	orig, origCred, err := m.getSpec(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	spec, cred, err := m.prepare(d, &orig, origCred)
	if err != nil {
		return Summary{}, err
	}
	if spec.Local {
		if err := m.store.Save(ctx, spec, nil); err != nil {
			return Summary{}, err
		}
		m.mu.Lock()
		inst := m.instances[LocalID]
		inst.Spec = spec
		m.mu.Unlock()
		return m.summarize(inst), nil
	}
	if err := m.store.Save(ctx, spec, cred); err != nil {
		return Summary{}, err
	}
	changed := orig.Server != spec.Server || orig.CA != spec.CA || orig.Auth != spec.Auth || !d.Credential.Empty() || d.Credential.CAPEM != ""
	if changed {
		return m.summarize(m.start(spec, *cred)), nil
	}
	m.mu.Lock()
	inst := m.instances[id]
	if inst != nil {
		inst.Spec = spec
	}
	m.mu.Unlock()
	if inst == nil {
		return m.summarize(m.start(spec, *cred)), nil
	}
	return m.summarize(inst), nil
}

// getSpec reads a connection from the store, or the local one from memory.
func (m *Manager) getSpec(ctx context.Context, id string) (Spec, Credential, error) {
	m.mu.RLock()
	inst, ok := m.instances[id]
	m.mu.RUnlock()
	if !ok {
		return Spec{}, Credential{}, ErrNotFound
	}
	if inst.Spec.Local {
		return inst.Spec, Credential{}, nil
	}
	spec, cred, err := m.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return inst.Spec, Credential{}, nil
	}
	return spec, cred, err
}

// Remove stops a connection and deletes its Secret (P3-08). The local
// cluster cannot be removed.
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	inst, ok := m.instances[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if inst.Spec.Local {
		m.mu.Unlock()
		return invalid("id", "The cluster the console runs in cannot be removed.")
	}
	delete(m.instances, id)
	if inst.cancel != nil {
		inst.cancel()
	}
	m.mu.Unlock()
	if err := m.store.Delete(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	m.log.Info("connection removed", "cluster", id)
	return nil
}

// TestDraft tests a draft from the editor; credential fields left empty
// fall back to the saved ones of the connection it edits.
func (m *Manager) TestDraft(ctx context.Context, id string, d Draft) (TestResult, error) {
	var orig *Spec
	origCred := Credential{}
	if id != "" {
		s, c, err := m.getSpec(ctx, id)
		if err != nil {
			return TestResult{}, err
		}
		orig, origCred = &s, c
		if s.Local {
			return m.testLocal(ctx, id)
		}
	}
	spec, cred, err := m.prepare(d, orig, origCred)
	if err != nil {
		return TestResult{}, err
	}
	return Test(ctx, spec, *cred, TestOptions{SecretsRead: m.opts.SecretsRead, Now: m.opts.Now}), nil
}

// TestSaved tests a saved connection and keeps the result for its page.
func (m *Manager) TestSaved(ctx context.Context, id string) (TestResult, error) {
	spec, cred, err := m.getSpec(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	var r TestResult
	if spec.Local {
		r, err = m.testLocal(ctx, id)
		if err != nil {
			return TestResult{}, err
		}
	} else {
		r = Test(ctx, spec, cred, TestOptions{SecretsRead: m.opts.SecretsRead, Now: m.opts.Now})
	}
	m.mu.Lock()
	if inst := m.instances[id]; inst != nil {
		inst.lastTest, inst.lastTested = &r, m.opts.Now().UTC()
	}
	m.mu.Unlock()
	return r, nil
}

// testLocal tests the console's own connection, with its own credentials.
func (m *Manager) testLocal(ctx context.Context, id string) (TestResult, error) {
	m.mu.RLock()
	inst := m.instances[id]
	m.mu.RUnlock()
	if inst == nil || inst.Cluster == nil {
		return TestResult{}, ErrNotFound
	}
	cfg := inst.Cluster.RestConfig()
	if cfg == nil {
		return TestResult{}, errors.New("the local connection has no configuration to test")
	}
	spec := inst.Spec
	spec.Server = cfg.Host
	spec.Auth = AuthNone
	spec.CA = CASkip
	if len(cfg.TLSClientConfig.CAData) > 0 || cfg.TLSClientConfig.CAFile != "" {
		spec.CA = CAPEM
	}
	return testWithConfig(ctx, spec, cfg, TestOptions{SecretsRead: m.opts.SecretsRead, Now: m.opts.Now}), nil
}

// RestConfigOf returns the configuration a running connection uses, for
// per-person calls.
func (m *Manager) RestConfigOf(id string) (*rest.Config, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	if !ok || inst.Cluster == nil {
		return nil, false
	}
	return inst.Cluster.RestConfig(), true
}

// Stop ends every connection's watches.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		if inst.cancel != nil {
			inst.cancel()
		}
	}
}

// Lookup returns a connection's store and cluster for the API; nil ones
// when the connection could not start.
func (m *Manager) Lookup(id string) (*live.Store, Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.instances[id]
	if !ok {
		return nil, nil, false
	}
	return inst.Store, inst.Cluster, true
}
