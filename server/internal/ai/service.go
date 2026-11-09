package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// SettingsStore is the console's settings, where Sherlock's settings from
// before Phase 12 stay: the clusters it is on, the overall caps, whether it
// may change the view, and the default provider, model and prices.
type SettingsStore interface {
	Current() settings.State
	UpdateSherlock(ctx context.Context, sh settings.Sherlock) (settings.State, error)
}

// Options configure the service.
type Options struct {
	// Backend keeps the keys of the AI ConfigMap; memory when nil.
	Backend  kv.Backend
	Settings SettingsStore
	// Keys holds the default provider's credential, in the Secret
	// Sherlock's key was always kept in.
	Keys *sherlock.Keys
	// KeyBackend makes the store of another provider's credential: a
	// Secret of the console's namespace; memory when nil.
	KeyBackend func(id string) sherlock.KeyBackend
	// ChartProvider is the default provider when the settings name none
	// (the chart's sherlock.provider), and the only one when ProviderLocked.
	ChartProvider  sherlock.Provider
	ProviderLocked bool
	// DefaultURL is the Claude API's address (the chart's sherlock.apiURL).
	DefaultURL string
	HTTP       *http.Client
	// Baselines are the chart's house instructions, by agent
	// (agents.<id>.instructions), read at start.
	Baselines map[string]string
	// ToolDefs are the toolbox's tool definitions, for a test's recorded
	// tools.
	ToolDefs func() []sherlock.ToolDef
	Now      func() time.Time
	Logger   *slog.Logger
}

// Service is the console's agents.
type Service struct {
	opts   Options
	engine *sherlock.Service

	mu      sync.Mutex
	snap    snapshot
	readAt  time.Time
	keys    map[string]*sherlock.Keys
	clients map[string]*sherlock.Client
	// running names the agents whose tests run now.
	running map[string]bool
	// setup is Getting started, for Sherlock's setup skill (Phase 12).
	setup SetupGuide
}

type snapshot struct {
	providers Providers
	agents    Agents
	prompts   Prompts
}

// fresh is how long a read of the configuration stands.
const fresh = 3 * time.Second

// New returns the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Keys == nil {
		o.Keys = sherlock.NewKeys(nil, "", nil)
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Service{opts: o, keys: map[string]*sherlock.Keys{}, clients: map[string]*sherlock.Client{}, running: map[string]bool{}}
}

// SetEngine gives the service the conversation engine, which runs tests;
// the engine is made with the service's Agent, so it comes second.
func (s *Service) SetEngine(e *sherlock.Service) { s.engine = e }

// Backend is the storage, for a backup.
func (s *Service) Backend() kv.Backend { return s.opts.Backend }

// Baseline is an agent's baseline house instructions from the chart.
func (s *Service) Baseline(id string) string { return strings.TrimSpace(s.opts.Baselines[id]) }

func (s *Service) sherlockSettings() settings.Sherlock {
	if s.opts.Settings == nil {
		return settings.DefaultSherlock
	}
	return s.opts.Settings.Current().SherlockOrDefault()
}

// read loads the configuration again when the last read is old; a failed
// read keeps the last.
func (s *Service) read(ctx context.Context) snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	if !s.readAt.IsZero() && now.Sub(s.readAt) < fresh && !now.Before(s.readAt) {
		return s.snap
	}
	var next snapshot
	if _, err := load(ctx, s.opts.Backend, ProvidersKey, &next.providers); err != nil {
		s.opts.Logger.Warn("reading the AI providers failed", "err", err)
		return s.snap
	}
	if _, err := load(ctx, s.opts.Backend, AgentsKey, &next.agents); err != nil {
		s.opts.Logger.Warn("reading the AI agents failed", "err", err)
		return s.snap
	}
	if _, err := load(ctx, s.opts.Backend, PromptsKey, &next.prompts); err != nil {
		s.opts.Logger.Warn("reading the AI instructions failed", "err", err)
		return s.snap
	}
	s.snap, s.readAt = next, now
	return next
}

// cached is the last read, without reading.
func (s *Service) cached() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// forget makes the next use read again: after a change here.
func (s *Service) forget() {
	s.mu.Lock()
	s.readAt = time.Time{}
	s.mu.Unlock()
}

// ---------- providers ----------

// defaultProvider is the provider Sherlock had before Phase 12.
func (s *Service) defaultProvider(snap snapshot) Provider {
	sh := s.sherlockSettings()
	p := s.opts.ChartProvider
	if sh.Provider != nil && !s.opts.ProviderLocked {
		p = *sh.Provider
	}
	if p.Kind == "" {
		// Nothing set is the Claude API, as before Phase 12.
		p.Kind = sherlock.KindAnthropic
	}
	name := snap.providers.DefaultName
	if name == "" {
		name = KindLabel(p.Normalized(s.opts.DefaultURL).Kind)
	}
	return Provider{ID: DefaultProvider, Name: name, Provider: p, Model: sh.Model, PriceIn: sh.PriceIn, PriceOut: sh.PriceOut, ContextWindow: snap.providers.DefaultWindow, Tested: snap.providers.DefaultTested}
}

// provider finds a provider by id.
func (s *Service) provider(snap snapshot, id string) (Provider, bool) {
	if id == "" || id == DefaultProvider {
		return s.defaultProvider(snap), true
	}
	for _, p := range snap.providers.List {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// SetCredential saves a provider's credential, of the kind its
// authentication takes, as a restore from a backup does (ADR-137).
func (s *Service) SetCredential(ctx context.Context, id, key, by string) error {
	p, ok := s.ProviderOf(ctx, id)
	if !ok {
		return fmt.Errorf("%w: there is no provider %s", ErrInvalid, id)
	}
	return s.KeysFor(id).SetFor(ctx, p.Provider.Normalized(s.opts.DefaultURL).Auth, key, by)
}

// KeysFor is a provider's credential store.
func (s *Service) KeysFor(id string) *sherlock.Keys {
	if id == "" || id == DefaultProvider {
		return s.opts.Keys
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := s.keys[id]; k != nil {
		return k
	}
	var b sherlock.KeyBackend
	if s.opts.KeyBackend != nil {
		b = s.opts.KeyBackend(id)
	}
	k := sherlock.NewKeys(b, "", nil)
	if err := k.Load(context.Background()); err != nil {
		s.opts.Logger.Warn("reading a provider's credential failed", "provider", id, "err", err)
	}
	s.keys[id] = k
	return k
}

// Reload reads every provider's credential again, so one set or removed
// through another replica takes effect here (P7-02).
func (s *Service) Reload(ctx context.Context) {
	s.forget()
	s.mu.Lock()
	keys := make([]*sherlock.Keys, 0, len(s.keys))
	for _, k := range s.keys {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	for _, k := range keys {
		if err := k.Reload(ctx); err != nil {
			s.opts.Logger.Warn("reading a provider's credential failed", "err", err)
		}
	}
}

// Run reloads the credentials every interval until ctx is done.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reload(ctx)
		}
	}
}

// Client is the client of a provider. It reads the provider's settings
// for every request, so a change takes effect at once.
func (s *Service) Client(id string) *sherlock.Client {
	if id == "" {
		id = DefaultProvider
	}
	keys := s.KeysFor(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[id]; c != nil {
		return c
	}
	c := &sherlock.Client{BaseURL: s.opts.DefaultURL, Key: keys.Key, HTTP: s.opts.HTTP, Retries: 2, Backoff: 2 * time.Second,
		Provider: func() sherlock.Provider {
			p, _ := s.provider(s.cached(), id)
			return p.Provider
		}}
	s.clients[id] = c
	return c
}

// --- AI settings, round 2 (Phase 12, wave 5) ---

// TryClient is a client of provider settings not saved yet, for a test or
// the models list before saving (AS-04, AS-05). keyOf names a provider
// whose saved credential it sends; "" sends none.
func (s *Service) TryClient(p sherlock.Provider, keyOf string) *sherlock.Client {
	key := func() string { return "" }
	if keyOf != "" {
		key = s.KeysFor(keyOf).Key
	}
	return &sherlock.Client{BaseURL: s.opts.DefaultURL, Key: key, HTTP: s.opts.HTTP, Provider: func() sherlock.Provider { return p }}
}

// SameEndpoint says whether provider settings reach the saved provider's
// endpoint, the only one its saved credential is sent to before saving.
func (s *Service) SameEndpoint(ctx context.Context, id string, p sherlock.Provider) bool {
	saved, ok := s.ProviderOf(ctx, id)
	if !ok {
		return false
	}
	a, b := saved.Provider.Normalized(s.opts.DefaultURL), p.Normalized(s.opts.DefaultURL)
	return a.Kind == b.Kind && a.Endpoint == b.Endpoint && a.Auth == b.Auth && a.AuthHeaderName == b.AuthHeaderName
}

// ListModels lists the models a saved provider's endpoint offers.
func (s *Service) ListModels(ctx context.Context, id string) ([]sherlock.ModelInfo, error) {
	if _, ok := s.ProviderOf(ctx, id); !ok {
		return nil, ErrNotFound
	}
	return s.Client(id).Models(ctx)
}

// allSecrets are every provider's credentials, never to send anywhere.
func (s *Service) allSecrets(snap snapshot) []string {
	out := sherlock.SecretsOf(s.opts.Keys.Key())
	for _, p := range snap.providers.List {
		if k := s.KeysFor(p.ID).Key(); k != "" {
			out = append(out, sherlock.SecretsOf(k)...)
		}
	}
	return out
}

// usable says why a provider cannot answer now, or "". One that needs a
// key and has none may answer once a test passed without one: an
// in-house endpoint that takes no credential (AS-04).
func (s *Service) usable(p Provider) string {
	if p.Provider.Normalized(s.opts.DefaultURL).Auth == sherlock.AuthHeader && s.KeysFor(p.ID).Key() == "" && !KeylessOK(p) {
		if p.ID == DefaultProvider {
			return "Sherlock has no API key. An administrator adds one in Settings."
		}
		return fmt.Sprintf("The provider %s has no credential. An administrator adds one in Settings › Providers.", p.Name)
	}
	return ""
}

// KeylessOK says the provider's last test, with its connection settings
// as they are, passed without a credential.
func KeylessOK(p Provider) bool {
	t := p.Tested
	return t != nil && t.OK && t.Keyless && t.Conn == ConnHash(p.Provider)
}

// RecordTest keeps a provider's connection test (AS-04): whether it
// passed, and whether it ran without a credential.
func (s *Service) RecordTest(ctx context.Context, id, model string, err error) error {
	defer s.forget()
	p, ok := s.provider(s.read(ctx), id)
	if !ok {
		return ErrNotFound
	}
	t := &ProviderTest{At: s.opts.Now().UTC(), OK: err == nil, Model: model, Keyless: s.KeysFor(id).Key() == "", Conn: ConnHash(p.Provider)}
	if err != nil {
		t.Error = clip(sherlock.Explain(err), 300)
	}
	_, err = change(ctx, s.opts.Backend, ProvidersKey, func(ps *Providers) error {
		if id == "" || id == DefaultProvider {
			ps.DefaultTested = t
			return nil
		}
		i := slices.IndexFunc(ps.List, func(x Provider) bool { return x.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		ps.List[i].Tested = t
		return nil
	})
	return err
}

// ---------- agents ----------

func (s *Service) settingsOf(snap snapshot, id string) AgentSettings {
	return snap.agents.Agents[id]
}

// on says whether an agent is on, and works on a cluster ("" is any).
func (s *Service) on(snap snapshot, info Info, cluster string) bool {
	if info.ID == Sherlock {
		return cluster == "" || s.sherlockSettings().Clusters[cluster]
	}
	a := s.settingsOf(snap, info.ID)
	return a.on(info) && (cluster == "" || a.worksOn(cluster))
}

// modelOf is the model an agent uses on its provider.
func modelOf(a AgentSettings, p Provider) string {
	if a.Model != "" {
		return a.Model
	}
	if p.Model != "" {
		return p.Model
	}
	return sherlock.DefaultModel
}

// priceOf is a model on a provider with the price its spend counts at.
func priceOf(model string, p Provider) sherlock.Model {
	return sherlock.Settings{Model: model, PriceIn: p.PriceIn, PriceOut: p.PriceOut}.Price()
}

// roster are the agents another may hand work to on a cluster: those on
// and working there, but itself and Sherlock, which routes.
func (s *Service) roster(snap snapshot, self, cluster string) []sherlock.RosterEntry {
	var out []sherlock.RosterEntry
	for _, info := range Catalog {
		if info.ID == self || info.ID == Sherlock || !s.on(snap, info, cluster) {
			continue
		}
		p, ok := s.provider(snap, s.settingsOf(snap, info.ID).Provider)
		if !ok || s.usable(p) != "" {
			continue
		}
		out = append(out, sherlock.RosterEntry{ID: info.ID, Name: info.Name, Purpose: info.Purpose})
	}
	return out
}

// Agent resolves an agent for a question or a hand-over: the engine's
// Options.Agent. The person and cluster decide whether a pilot's draft
// answers instead of the live instructions.
func (s *Service) Agent(id string, q sherlock.Question) (sherlock.Agent, error) {
	if id == "" {
		id = Sherlock
	}
	snap := s.read(context.Background())
	names := []string{q.Who.Email, q.Who.Subject, q.Who.Name}
	// Sherlock's settings test from before Phase 12 tries instructions and
	// notes before they are saved.
	var house *string
	var notes map[string]string
	if q.Instructions != "" {
		house = &q.Instructions
	}
	if q.Notes != "" {
		notes = map[string]string{AllClusters: q.Notes}
	}
	// Sherlock's setup skill (GS-07): a SuperAdmin's, while Getting
	// started has a step open.
	var setup func(context.Context) string
	if g := s.setupGuide(); id == Sherlock && q.SuperAdmin && g != nil && g.Open(context.Background()) {
		setup = g.Status
	}
	// Sherlock's Cases skill (IB-10): asked from the Cases area, it knows
	// the area's pages.
	var over *draftText
	if house != nil || notes != nil {
		over = &draftText{house: house, notes: notes}
	}
	return s.resolve(snap, id, q.Key.Cluster, names, over, setup, id == Sherlock && q.Cases)
}

// draftText is instructions to resolve an agent with instead of those in
// force: a draft's, for its test or its preview. A nil house or notes,
// and parts not given, keep those in force.
type draftText struct {
	house      *string
	notes      map[string]string
	parts      map[string]string
	partsGiven bool
}

// Preview is an agent as it would answer on a cluster with the house
// instructions and notes given, saved or not; nil keeps those in force.
func (s *Service) Preview(ctx context.Context, id, cluster string, house *string, notes map[string]string) (sherlock.Agent, error) {
	var over *draftText
	if house != nil || notes != nil {
		over = &draftText{house: house, notes: notes}
	}
	return s.resolve(s.read(ctx), id, cluster, nil, over, nil, false)
}

// PreviewDraft is an agent as its draft would answer on a cluster, or as
// it answers now without a draft.
func (s *Service) PreviewDraft(ctx context.Context, id, cluster string) (sherlock.Agent, error) {
	snap := s.read(ctx)
	var over *draftText
	if ap := snap.prompts.Agents[id]; ap != nil && ap.Draft != nil {
		over = ap.Draft.text()
	}
	return s.resolve(snap, id, cluster, nil, over, nil, false)
}

// text is the draft as instructions to resolve with.
func (d *Draft) text() *draftText {
	h := d.House
	notes := d.Notes
	if notes == nil {
		notes = map[string]string{}
	}
	return &draftText{house: &h, notes: notes, parts: d.Parts, partsGiven: true}
}

// CompactShare is the share of a model's context Sherlock's instructions
// and tools may take in its whole form: above it, it answers in its
// compact form, leaving the rest to the cluster's block, the question,
// the tools' results and the answer (AS-07).
const CompactShare = 0.5

// windowOf is the context window of an agent's model on its provider, in
// tokens, and where it was found: the agent's own (for a model of its
// own), the provider's (for its default model), or a Claude model's.
// 0 is not known.
func windowOf(set AgentSettings, p Provider, model string) (int, string) {
	switch {
	case set.Model != "" && set.ContextWindow > 0:
		return set.ContextWindow, "agent"
	case (set.Model == "" || set.Model == p.Model) && p.ContextWindow > 0:
		return p.ContextWindow, "provider"
	case p.Provider.Kind == "" || p.Provider.Kind == sherlock.KindAnthropic || strings.HasPrefix(model, "claude"):
		if _, ok := sherlock.ModelOf(model); ok {
			return sherlock.ClaudeContext, "model"
		}
	}
	return 0, ""
}

// replyBound bounds one reply on a model whose context is known: a
// quarter of it, between 1,024 and the engine's own bound; 0 leaves the
// engine's.
func replyBound(window int) int {
	if window <= 0 || window >= 4*16000 {
		return 0
	}
	return max(window/4, 1024)
}

// resolve builds the agent; over, when given, is the instructions to use
// instead (a test of a draft). setup, for Sherlock, turns its setup skill
// on with what setup_status reads; cases its Cases skill (IB-10).
func (s *Service) resolve(snap snapshot, id, cluster string, names []string, over *draftText, setup func(context.Context) string, cases bool) (sherlock.Agent, error) {
	info, ok := InfoOf(id)
	if !ok {
		return sherlock.Agent{}, fmt.Errorf("there is no agent %q", id)
	}
	if id != Sherlock && !s.on(snap, info, "") {
		return sherlock.Agent{}, fmt.Errorf("the %s is off", info.Name)
	}
	if id != Sherlock && cluster != "" && !s.settingsOf(snap, id).worksOn(cluster) {
		return sherlock.Agent{}, fmt.Errorf("the %s does not work on this cluster", info.Name)
	}
	set := s.settingsOf(snap, id)
	p, ok := s.provider(snap, set.Provider)
	if !ok {
		return sherlock.Agent{}, fmt.Errorf("the %s's provider %q is gone; pick another in Settings › Agents", info.Name, set.Provider)
	}
	if why := s.usable(p); why != "" {
		return sherlock.Agent{}, errors.New(why)
	}
	model := modelOf(set, p)
	baseline := s.Baseline(id)
	ap := snap.prompts.Agents[id]
	version := "live"
	text := ap.LiveHouse(baseline)
	var notes, parts map[string]string
	if ap != nil {
		notes, parts = ap.Notes, ap.Parts
	}
	switch {
	case over != nil:
		if over.house != nil {
			text, version = *over.house, "draft"
		}
		if over.partsGiven {
			parts, version = over.parts, "draft"
		}
	case ap != nil && ap.Draft != nil && ap.Draft.Stage == StagePilot && ap.Draft.Pilot.serves(names, cluster, s.opts.Now()):
		text, notes, parts, version = ap.Draft.House, ap.Draft.Notes, ap.Draft.Parts, fmt.Sprintf("draft %d (pilot)", ap.Draft.Rev)
	}
	if over != nil && over.notes != nil {
		notes = over.notes
	}
	view := id == Sherlock && s.sherlockSettings().ChangesView()
	roster := s.roster(snap, id, cluster)
	core, tools := Core(id, view), set.tools(info)
	if t := strings.TrimSpace(parts[PartCore]); t != "" {
		core = t
	}
	if id != Sherlock {
		setup = nil
	}
	if setup != nil {
		core += "\n\n" + SetupSkill()
		tools = append(tools, sherlock.ToolSetupStatus)
	}
	if cases && id == Sherlock {
		core += "\n\n" + CasesSkill()
	}
	window, _ := windowOf(set, p, model)
	a := sherlock.Agent{
		ID: id, Name: info.Name, Client: s.Client(p.ID), Model: model, Price: priceOf(model, p),
		Caps:         sherlock.Caps{DailyUSD: set.DailyUSD, MonthlyUSD: set.MonthlyUSD},
		Instructions: Assemble(core, roster, text),
		Notes:        notesFor(notes, cluster),
		Tools:        tools,
		Roster:       roster,
		Secrets:      s.allSecrets(snap),
		Version:      version,
		Setup:        setup,
		MaxTokens:    replyBound(window),
	}
	if id == Sherlock && s.compact(set, window, a, view) {
		a = compactForm(a, set, info, parts, text)
	}
	return a, nil
}

// toolDefs are the toolbox's tool definitions, for measuring.
func (s *Service) toolDefs() []sherlock.ToolDef {
	if s.opts.ToolDefs == nil {
		return nil
	}
	return s.opts.ToolDefs()
}

// compact says whether Sherlock answers in its compact form: chosen, or
// its whole form does not fit its share of the model's context.
func (s *Service) compact(set AgentSettings, window int, a sherlock.Agent, view bool) bool {
	switch set.Compact {
	case CompactAlways:
		return true
	case CompactNever:
		return false
	}
	return window > 0 && float64(sherlock.Measure(a, s.toolDefs(), view).Total()) > CompactShare*float64(window)
}

// compactForm is Sherlock in its compact form: the compact core (or its
// edit) with the house instructions, the essential reads of the tools it
// is given, no hand-over, no skills and no view actions (AS-07).
func compactForm(a sherlock.Agent, set AgentSettings, info Info, parts map[string]string, house string) sherlock.Agent {
	core := CompactCore()
	if t := strings.TrimSpace(parts[PartCompact]); t != "" {
		core = t
	}
	given := set.tools(info)
	var tools []string
	for _, t := range CompactTools {
		if slices.Contains(given, t) {
			tools = append(tools, t)
		}
	}
	a.Instructions = Assemble(core, nil, house)
	a.Tools, a.Roster, a.Setup, a.Compact = tools, nil, nil, true
	return a
}

// Usable says why an agent cannot answer on a cluster now, or "".
func (s *Service) Usable(id, cluster string) string {
	_, err := s.resolve(s.read(context.Background()), id, cluster, nil, nil, nil, false)
	if err != nil {
		return err.Error()
	}
	return ""
}

// ---------- the administrators' view ----------

// ProviderView is a provider as Settings › Providers shows it.
type ProviderView struct {
	Provider
	// Resolved is the provider with its kind's defaults filled in.
	Resolved sherlock.Provider `json:"resolved"`
	Default  bool              `json:"default"`
	// Locked says the chart fixes it (sherlock.lockProvider).
	Locked   bool             `json:"locked"`
	NeedsKey bool             `json:"needsKey"`
	Key      sherlock.KeyInfo `json:"key"`
	// UsedBy are the agents on it.
	UsedBy []string `json:"usedBy"`
	// KeylessOK says it needs no key: its last test passed without one,
	// with its settings as they are (Phase 12, wave 5).
	KeylessOK bool `json:"keylessOK"`
}

// AgentView is an agent as Settings › Agents shows it.
type AgentView struct {
	Info
	Settings AgentSettings `json:"settings"`
	// Enabled says whether it is on: for Sherlock, on any cluster.
	Enabled      bool   `json:"enabled"`
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	Model        string `json:"model"`
	// DefaultModel says it uses its provider's default model.
	DefaultModel bool     `json:"defaultModel"`
	InForce      []string `json:"inForce"`
	// Clusters are the clusters it works on; empty is all. Sherlock's are
	// the clusters it is on.
	Clusters []string `json:"clusters"`
	// State is where its instructions stand against the chart's baseline;
	// Stage where its draft is, "" without one.
	State     string              `json:"state"`
	Stage     string              `json:"stage,omitempty"`
	LastTest  *TestSummary        `json:"lastTest,omitempty"`
	Baseline  *TestSummary        `json:"baselineCheck,omitempty"`
	ValuesKey string              `json:"valuesKey"`
	Spend     sherlock.AgentSpend `json:"spend"`
	// Unusable says why it cannot answer now.
	Unusable string `json:"unusable,omitempty"`
	// Testing says its tests run now.
	Testing bool `json:"testing,omitempty"`
	// Footprint is what its instructions and tools take of its model's
	// context; nil when it cannot answer now (Phase 12, wave 5).
	Footprint *AgentFootprint `json:"footprint,omitempty"`
}

// AgentFootprint is what an agent's instructions and tools take of its
// model's context, in estimated tokens (AS-07): the form in force, and
// its whole form.
type AgentFootprint struct {
	sherlock.Footprint
	Whole sherlock.Footprint `json:"whole"`
	// Window is the model's context window; 0 is not known. WindowFrom
	// says where it was found: agent, provider or model.
	Window     int    `json:"window"`
	WindowFrom string `json:"windowFrom,omitempty"`
	// Compact says the compact form is in force; Mode is the choice
	// (CompactAuto, CompactAlways, CompactNever), Share the share of the
	// context the whole form may take.
	Compact bool    `json:"compact"`
	Mode    string  `json:"mode"`
	Share   float64 `json:"share"`
	// MaxTokens bounds one reply on this model; 0 is the engine's.
	MaxTokens int `json:"maxTokens,omitempty"`
}

// footprint measures an agent as it answers on the default cluster, in
// the form in force and whole.
func (s *Service) footprint(snap snapshot, info Info) *AgentFootprint {
	a, err := s.resolve(snap, info.ID, "", nil, nil, nil, false)
	if err != nil {
		return nil
	}
	view := info.ID == Sherlock && s.sherlockSettings().ChangesView()
	defs := s.toolDefs()
	set := s.settingsOf(snap, info.ID)
	f := &AgentFootprint{Footprint: sherlock.Measure(a, defs, view), Compact: a.Compact, Mode: set.Compact, Share: CompactShare, MaxTokens: a.MaxTokens}
	f.Whole = f.Footprint
	if p, ok := s.provider(snap, set.Provider); ok {
		f.Window, f.WindowFrom = windowOf(set, p, a.Model)
	}
	if a.Compact {
		whole := snap
		whole.agents.Agents = cloneAgents(snap.agents.Agents)
		set.Compact = CompactNever
		whole.agents.Agents[info.ID] = set
		if w, err := s.resolve(whole, info.ID, "", nil, nil, nil, false); err == nil {
			f.Whole = sherlock.Measure(w, defs, view)
		}
	}
	return f
}

// Overview is Settings › Agents and Providers.
type Overview struct {
	Providers []ProviderView   `json:"providers"`
	Agents    []AgentView      `json:"agents"`
	Models    []sherlock.Model `json:"models"`
	// DefaultModel is the default provider's model.
	DefaultModel string `json:"defaultModel"`
	// DailyUSD and MonthlyUSD are the overall limits, every agent's spend
	// together.
	DailyUSD       float64        `json:"dailyUSD"`
	MonthlyUSD     float64        `json:"monthlyUSD"`
	Spend          sherlock.Spend `json:"spend"`
	SingleProvider bool           `json:"singleProvider"`
	ViewControl    bool           `json:"viewControl"`
}

// View is the overview.
func (s *Service) View(ctx context.Context) Overview {
	snap := s.read(ctx)
	sh := s.sherlockSettings()
	var spend sherlock.Spend
	if s.engine != nil {
		spend = s.engine.Ledger().Now(ctx)
	}
	o := Overview{Models: sherlock.Models, DefaultModel: sh.Model, DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD, Spend: spend, SingleProvider: snap.agents.SingleProvider, ViewControl: sh.ChangesView()}
	used := map[string][]string{}
	for _, info := range Catalog {
		set := s.settingsOf(snap, info.ID)
		pid := set.Provider
		if pid == "" {
			pid = DefaultProvider
		}
		p, ok := s.provider(snap, pid)
		v := AgentView{Info: info, Settings: set, ProviderID: pid, ProviderName: p.Name, InForce: set.tools(info), Clusters: set.Clusters, ValuesKey: ValuesKey(info.ID), Spend: spend.Agent(info.ID), Testing: s.testing(info.ID)}
		if info.ID == Sherlock {
			v.Clusters = nil
			for c, on := range sh.Clusters {
				if on {
					v.Clusters = append(v.Clusters, c)
				}
			}
			sort.Strings(v.Clusters)
			v.Enabled = len(v.Clusters) > 0
		} else {
			v.Enabled = set.on(info)
		}
		if ok {
			v.Model, v.DefaultModel = modelOf(set, p), set.Model == ""
			if v.Enabled {
				used[pid] = append(used[pid], info.Name)
			}
			v.Unusable = s.usable(p)
		} else {
			v.Unusable = fmt.Sprintf("its provider %q is gone", pid)
		}
		ap := snap.prompts.Agents[info.ID]
		v.State = ap.State(s.Baseline(info.ID))
		if ap != nil {
			if ap.Draft != nil {
				v.Stage = ap.Draft.Stage
				if ap.Draft.Stage == StagePilot && ap.Draft.Pilot != nil && !s.opts.Now().Before(ap.Draft.Pilot.Until) {
					v.Stage = StageTest
				}
				v.LastTest = ap.Draft.Test.Summary()
			}
			v.Baseline = ap.BaselineCheck.Summary()
		}
		v.Footprint = s.footprint(snap, info)
		o.Agents = append(o.Agents, v)
	}
	for _, p := range append([]Provider{s.defaultProvider(snap)}, snap.providers.List...) {
		k := s.KeysFor(p.ID)
		r := p.Provider.Normalized(s.opts.DefaultURL)
		o.Providers = append(o.Providers, ProviderView{Provider: p, Resolved: r, Default: p.ID == DefaultProvider, Locked: p.ID == DefaultProvider && s.opts.ProviderLocked, NeedsKey: r.Auth == sherlock.AuthHeader, Key: k.Info(), UsedBy: used[p.ID], KeylessOK: r.Auth == sherlock.AuthHeader && !k.Info().Set && KeylessOK(p)})
	}
	return o
}

// ---------- changes ----------

// ErrInvalid is a change that is not valid; ErrNotFound one about nothing.
var (
	ErrInvalid  = errors.New("not valid")
	ErrNotFound = errors.New("not found")
	// ErrSameProvider is the Script reviewer put on the Automation
	// author's provider without the single-provider confirmation.
	ErrSameProvider = errors.New("the Script reviewer is on the same provider as the Automation author: an independent second opinion needs another provider. Pick another, or confirm this console has only one")
)

// SaveProvider adds or changes a provider. The default provider's
// settings are Sherlock's, as before Phase 12.
func (s *Service) SaveProvider(ctx context.Context, p Provider, adding bool) error {
	defer s.forget()
	if p.ID == DefaultProvider {
		if adding {
			return fmt.Errorf("%w: the default provider exists", ErrInvalid)
		}
		sh := s.sherlockSettings()
		before := s.defaultProvider(s.read(ctx))
		if s.opts.ProviderLocked && before.Provider.Kind != p.Provider.Kind || s.opts.ProviderLocked && before.Endpoint != p.Endpoint {
			return fmt.Errorf("%w: the chart fixes the default provider (sherlock.lockProvider); change it there", ErrInvalid)
		}
		if p.Model != "" {
			sh.Model = p.Model
		}
		sh.PriceIn, sh.PriceOut = p.PriceIn, p.PriceOut
		if !s.opts.ProviderLocked {
			if p.Provider.Kind == sherlock.KindAnthropic && p.Endpoint == "" && p.Auth == "" && len(p.Headers) == 0 && p.Query == "" && p.CACert == "" {
				sh.Provider = nil
			} else {
				pp := p.Provider
				sh.Provider = &pp
			}
		}
		if err := sh.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if _, err := s.opts.Settings.UpdateSherlock(ctx, sh); err != nil {
			return err
		}
		if err := checkWindow(p.ContextWindow); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		name := strings.TrimSpace(p.Name)
		_, err := change(ctx, s.opts.Backend, ProvidersKey, func(ps *Providers) error {
			if name == KindLabel(p.Provider.Normalized(s.opts.DefaultURL).Kind) {
				name = ""
			}
			ps.DefaultName = name
			ps.DefaultWindow = p.ContextWindow
			return nil
		})
		return err
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	// The last test is the console's own record, never the request's.
	p.Tested = nil
	_, err := change(ctx, s.opts.Backend, ProvidersKey, func(ps *Providers) error {
		i := slices.IndexFunc(ps.List, func(x Provider) bool { return x.ID == p.ID })
		switch {
		case adding && i >= 0:
			return fmt.Errorf("%w: a provider %q exists", ErrInvalid, p.ID)
		case !adding && i < 0:
			return ErrNotFound
		case adding && len(ps.List)+1 >= MaxProviders:
			return fmt.Errorf("%w: at most %d providers", ErrInvalid, MaxProviders)
		case adding:
			ps.List = append(ps.List, p)
		default:
			p.Tested = ps.List[i].Tested
			ps.List[i] = p
		}
		return nil
	})
	return err
}

// DeleteProvider removes a provider no agent uses, and its credential.
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	defer s.forget()
	if id == DefaultProvider {
		return fmt.Errorf("%w: the default provider stays", ErrInvalid)
	}
	snap := s.read(ctx)
	for aid, a := range snap.agents.Agents {
		if a.Provider == id {
			info, _ := InfoOf(aid)
			return fmt.Errorf("%w: the %s uses it; move it to another provider first", ErrInvalid, info.Name)
		}
	}
	if _, err := change(ctx, s.opts.Backend, ProvidersKey, func(ps *Providers) error {
		i := slices.IndexFunc(ps.List, func(x Provider) bool { return x.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		ps.List = slices.Delete(ps.List, i, i+1)
		return nil
	}); err != nil {
		return err
	}
	if err := s.KeysFor(id).Delete(ctx); err != nil && !errors.Is(err, sherlock.ErrNoKey) {
		return err
	}
	s.mu.Lock()
	delete(s.keys, id)
	delete(s.clients, id)
	s.mu.Unlock()
	return nil
}

// ProviderOf finds a provider, for its credential's kind.
func (s *Service) ProviderOf(ctx context.Context, id string) (Provider, bool) {
	return s.provider(s.read(ctx), id)
}

// AgentChange is a change to one agent's settings.
type AgentChange struct {
	AgentSettings
	// SherlockClusters, for Sherlock, are the clusters it is on.
	SherlockClusters []string `json:"sherlockClusters,omitempty"`
	// ViewControl, for Sherlock, lets it change the person's view.
	ViewControl *bool `json:"viewControl,omitempty"`
	// ConfirmSingleProvider confirms the console has one provider, so the
	// Script reviewer may share the Automation author's.
	ConfirmSingleProvider bool `json:"confirmSingleProvider,omitempty"`
}

// SaveAgent changes an agent's settings.
func (s *Service) SaveAgent(ctx context.Context, id string, in AgentChange) error {
	defer s.forget()
	info, ok := InfoOf(id)
	if !ok {
		return ErrNotFound
	}
	set := in.AgentSettings
	if err := set.Validate(info); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	snap := s.read(ctx)
	if _, ok := s.provider(snap, set.Provider); !ok {
		return fmt.Errorf("%w: there is no provider %q", ErrInvalid, set.Provider)
	}
	if set.Provider == DefaultProvider {
		set.Provider = ""
	}
	if id == Sherlock {
		// Sherlock is on where its clusters say, as before Phase 12.
		set.On = nil
		sh := s.sherlockSettings()
		if in.SherlockClusters != nil {
			on := map[string]bool{}
			for _, c := range in.SherlockClusters {
				on[c] = true
			}
			sh.Clusters = on
		}
		if in.ViewControl != nil {
			sh.ViewControl = in.ViewControl
		}
		if _, err := s.opts.Settings.UpdateSherlock(ctx, sh); err != nil {
			return err
		}
	}
	_, err := change(ctx, s.opts.Backend, AgentsKey, func(a *Agents) error {
		if a.Agents == nil {
			a.Agents = map[string]AgentSettings{}
		}
		next := cloneAgents(a.Agents)
		next[id] = set
		author, reviewer := next[AutomationAuthor].Provider, next[ScriptReviewer].Provider
		if (id == AutomationAuthor || id == ScriptReviewer) && author == reviewer && next[ScriptReviewer].on(mustInfo(ScriptReviewer)) {
			if !in.ConfirmSingleProvider && !a.SingleProvider {
				return ErrSameProvider
			}
			a.SingleProvider = true
		} else if author != reviewer {
			a.SingleProvider = false
		}
		a.Agents = next
		return nil
	})
	return err
}

func cloneAgents(m map[string]AgentSettings) map[string]AgentSettings {
	out := make(map[string]AgentSettings, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustInfo(id string) Info {
	info, _ := InfoOf(id)
	return info
}

// SetLimits changes the overall limits: what every agent spends together.
func (s *Service) SetLimits(ctx context.Context, daily, monthly float64) error {
	sh := s.sherlockSettings()
	sh.DailyUSD, sh.MonthlyUSD = daily, monthly
	if err := sh.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	_, err := s.opts.Settings.UpdateSherlock(ctx, sh)
	return err
}

func (s *Service) testing(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}
