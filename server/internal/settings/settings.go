// Package settings keeps the console's shared settings: the ones every open
// browser follows, such as the refresh rate. They are kept on the server,
// in a ConfigMap when it runs in a cluster, so that one setting governs one
// read of the cluster however many browsers are open.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Settings is what the Settings dialog changes for everyone.
type Settings struct {
	// RefreshSeconds is how often the server reads what it does not watch
	// (CPU and memory in use), rebuilds the rows that changed and sends every
	// browser the same update at the same moment.
	RefreshSeconds int `json:"refreshSeconds"`
	// TimeFormat is how every console shows dates and times, each in its
	// browser's time zone; nil is the default, 2026-05-03 1:34pm PDT.
	TimeFormat *TimeFormat `json:"timeFormat,omitempty"`
	// Fleets are each cluster's fleets (P4-01), by the cluster's
	// connection id.
	Fleets map[string][]fleets.Fleet `json:"fleets,omitempty"`
	// Acks are each cluster's acknowledged alerts, by the cluster's
	// connection id.
	Acks map[string][]acks.Ack `json:"acks,omitempty"`
	// Sherlock is how Sherlock answers (P5-05); nil is the defaults, with
	// Sherlock off everywhere.
	Sherlock *Sherlock `json:"sherlock,omitempty"`
	// Watson is how Watson approves changes (Phase 6); nil is the
	// defaults.
	Watson *Watson `json:"watson,omitempty"`
}

// TimeFormat is how dates and times read in every console.
type TimeFormat struct {
	// Date is iso (2026-05-03), us (05/03/2026), eu (03/05/2026) or long
	// (May 3, 2026).
	Date string `json:"date"`
	// Clock is 12h (1:34pm) or 24h (13:34).
	Clock string `json:"clock"`
}

// Validate reports what is wrong with the time format.
func (t TimeFormat) Validate() error {
	switch t.Date {
	case "iso", "us", "eu", "long":
	default:
		return fmt.Errorf("the date format must be iso, us, eu or long")
	}
	switch t.Clock {
	case "12h", "24h":
	default:
		return fmt.Errorf("the clock must be 12h or 24h")
	}
	return nil
}

// Watson is what an administrator chooses for Watson.
type Watson struct {
	// TwoPerson requires a second person to approve every change on a
	// cluster tagged production (P6-07).
	TwoPerson bool `json:"twoPerson"`
}

// WatsonOrDefault returns the Watson settings in force.
func (s Settings) WatsonOrDefault() Watson {
	if s.Watson == nil {
		return Watson{}
	}
	return *s.Watson
}

// Sherlock is what an administrator chooses for Sherlock. The API key is
// not here: it is kept in a Secret and never sent to a browser.
type Sherlock struct {
	// Clusters turns Sherlock on per cluster, by connection id.
	Clusters map[string]bool `json:"clusters,omitempty"`
	// Model is the Claude model it uses.
	Model string `json:"model"`
	// DailyUSD and MonthlyUSD cap what it may spend, in US dollars; 0 is
	// no cap.
	DailyUSD   float64 `json:"dailyUSD"`
	MonthlyUSD float64 `json:"monthlyUSD"`
	// ViewControl lets Sherlock change the asking person's view (P6U-14);
	// nil is on.
	ViewControl *bool `json:"viewControl,omitempty"`
	// Provider is the model's API and how to reach it (Phase 6 review); nil
	// is the Claude API at the chart's sherlock.apiURL.
	Provider *sherlock.Provider `json:"provider,omitempty"`
	// PriceIn and PriceOut are the model's prices in US dollars per million
	// tokens, for the spending caps, when it is not a listed Claude model;
	// 0 counts it at the highest listed price.
	PriceIn  float64 `json:"priceIn,omitempty"`
	PriceOut float64 `json:"priceOut,omitempty"`
}

// ChangesView says whether Sherlock may change the asking person's view.
func (s Sherlock) ChangesView() bool { return s.ViewControl == nil || *s.ViewControl }

// DefaultSherlock is a fresh installation's: off, with caps in place.
var DefaultSherlock = Sherlock{Model: "claude-opus-5-5", DailyUSD: 20, MonthlyUSD: 200}

// SherlockOrDefault returns the Sherlock settings in force.
func (s Settings) SherlockOrDefault() Sherlock {
	if s.Sherlock == nil {
		return DefaultSherlock
	}
	return *s.Sherlock
}

// modelRE takes the model names of the providers Sherlock speaks: Claude's,
// OpenAI's and Azure deployments', Bedrock's ids and inference profiles
// (us.anthropic.claude-…-v1:0) and Vertex's publisher/model.
var modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:@_/+-]{0,127}$`)

// MaxCapUSD bounds a cap, so a typo does not remove it in effect.
const MaxCapUSD = 100_000

// Validate reports what is wrong with the Sherlock settings.
func (s Sherlock) Validate() error {
	if !modelRE.MatchString(s.Model) {
		return fmt.Errorf("%q is not a model name", s.Model)
	}
	for _, v := range []float64{s.DailyUSD, s.MonthlyUSD} {
		if v < 0 || v > MaxCapUSD || math.IsNaN(v) {
			return fmt.Errorf("a spending cap must be between $0 (no cap) and $%d", MaxCapUSD)
		}
	}
	if s.DailyUSD > 0 && s.MonthlyUSD > 0 && s.DailyUSD > s.MonthlyUSD {
		return fmt.Errorf("the daily cap is above the monthly cap")
	}
	for _, v := range []float64{s.PriceIn, s.PriceOut} {
		if v < 0 || v > 10_000 || math.IsNaN(v) {
			return fmt.Errorf("a price must be between $0 and $10,000 per million tokens")
		}
	}
	if s.Provider != nil {
		if err := s.Provider.Validate(); err != nil {
			return err
		}
	}
	if len(s.Clusters) > 1000 {
		return fmt.Errorf("too many clusters")
	}
	return nil
}

// Refresh limits: a second keeps the server from spinning; ten minutes keeps
// a console from looking current when it is not.
const (
	MinRefresh = 1 * time.Second
	MaxRefresh = 10 * time.Minute
)

// Default is what a fresh installation uses: every change in the cluster
// shows within 5 seconds (X-07), with time to spare.
var Default = Settings{RefreshSeconds: 3}

// Refresh returns the refresh interval as a duration.
func (s Settings) Refresh() time.Duration { return time.Duration(s.RefreshSeconds) * time.Second }

// Validate reports what is wrong with the settings.
func (s Settings) Validate() error {
	if r := s.Refresh(); r < MinRefresh || r > MaxRefresh {
		return fmt.Errorf("refreshSeconds must be between %d and %d", int(MinRefresh/time.Second), int(MaxRefresh/time.Second))
	}
	for cluster, list := range s.Fleets {
		if err := fleets.Validate(list); err != nil {
			return fmt.Errorf("the fleets of %s: %w", cluster, err)
		}
	}
	for cluster, list := range s.Acks {
		if err := acks.Validate(list); err != nil {
			return fmt.Errorf("the acknowledged alerts of %s: %w", cluster, err)
		}
	}
	if s.Sherlock != nil {
		if err := s.Sherlock.Validate(); err != nil {
			return fmt.Errorf("Sherlock: %w", err)
		}
	}
	if s.TimeFormat != nil {
		if err := s.TimeFormat.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Equal says whether two settings are the same.
func (s Settings) Equal(o Settings) bool { return reflect.DeepEqual(s, o) }

// ErrNotFound is what a Storage returns when nothing has been saved yet.
var ErrNotFound = errors.New("no settings saved")

// ErrConflict is what a Storage returns when the settings changed since
// the version a save was based on: another replica saved first.
var ErrConflict = kv.ErrConflict

// ErrNewer refuses a change to settings a newer 221B saved, whose fields
// this one does not know (P7-03).
var ErrNewer = errors.New("the settings were saved by a newer 221B; upgrade this one")

// Storage keeps the settings between restarts, and shares them between
// replicas (P7-02): every value carries a version, and a save is made only
// over the version it was based on.
type Storage interface {
	// Load returns the saved settings as JSON and the storage's version,
	// or ErrNotFound (with the version, so a first save is conditional too).
	Load(ctx context.Context) ([]byte, string, error)
	// Save writes the settings when the storage is still at version ("" saves
	// over whatever it holds) and returns the new version; ErrConflict when
	// another writer came first.
	Save(ctx context.Context, b []byte, version string) (string, error)
	// Kind names the storage for the browser: "configmap" or "memory".
	Kind() string
}

// Memory keeps settings until the server restarts: the laptop and
// simulator modes.
type Memory struct {
	mu      sync.Mutex
	b       []byte
	version int
}

func (m *Memory) Load(context.Context) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := strconv.Itoa(m.version)
	if m.b == nil {
		return nil, v, ErrNotFound
	}
	return append([]byte(nil), m.b...), v, nil
}

func (m *Memory) Save(_ context.Context, b []byte, version string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version != "" && version != strconv.Itoa(m.version) {
		return "", ErrConflict
	}
	m.b = append([]byte(nil), b...)
	m.version++
	return strconv.Itoa(m.version), nil
}

func (m *Memory) Kind() string { return "memory" }

// State is the settings as the browser receives them: the values, where
// they are kept and when they were last changed.
type State struct {
	Settings
	// Storage is "configmap" when the settings survive a restart and are
	// shared by every replica, "memory" when they last until a restart.
	Storage string `json:"storage"`
	// ChangedAt is when the settings were last saved, if known.
	ChangedAt *time.Time `json:"changedAt,omitempty"`
}

// Service holds the current settings, loads them from storage and saves
// changes to it. With several replicas over one ConfigMap, each change is
// made to what the storage holds at that moment, never to this replica's
// copy, so no replica puts back what another one changed; Run picks up
// the others' changes.
type Service struct {
	storage  Storage
	log      *slog.Logger
	defaults Settings
	// onChange is told about every change, including the initial load.
	onChange func(State)

	mu      sync.Mutex
	current State
	// loaded and version are what storage last held, to notice changes
	// made elsewhere cheaply; newer says it was a newer 221B's format.
	loaded  []byte
	version string
	newer   bool
	// writeMu orders this replica's updates and reloads.
	writeMu sync.Mutex
}

// NewService loads the settings from storage, falling back to defaults.
// onChange is called with the loaded settings and after every change.
func NewService(ctx context.Context, storage Storage, defaults Settings, log *slog.Logger, onChange func(State)) *Service {
	if log == nil {
		log = slog.Default()
	}
	if onChange == nil {
		onChange = func(State) {}
	}
	if defaults.Validate() != nil {
		defaults = Default
	}
	s := &Service{storage: storage, log: log, defaults: defaults, onChange: onChange, current: State{Settings: defaults, Storage: storage.Kind()}}
	s.reload(ctx, true)
	return s
}

// Current returns the settings in force.
func (s *Service) Current() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Update validates, saves and applies new settings. What it is not given
// it keeps: the Settings dialog sends the refresh rate, not the fleets.
func (s *Service) Update(ctx context.Context, in Settings) (State, error) {
	return s.change(ctx, func(cur *Settings) error {
		next := in
		if next.Fleets == nil {
			next.Fleets = cur.Fleets
		}
		if next.Acks == nil {
			next.Acks = cur.Acks
		}
		if next.Sherlock == nil {
			next.Sherlock = cur.Sherlock
		}
		if next.Watson == nil {
			next.Watson = cur.Watson
		}
		if next.TimeFormat == nil {
			next.TimeFormat = cur.TimeFormat
		}
		*cur = next
		return nil
	})
}

// UpdateWatson replaces the Watson settings.
func (s *Service) UpdateWatson(ctx context.Context, w Watson) (State, error) {
	return s.change(ctx, func(cur *Settings) error {
		cur.Watson = &w
		return nil
	})
}

// UpdateSherlock replaces the Sherlock settings.
func (s *Service) UpdateSherlock(ctx context.Context, sh Sherlock) (State, error) {
	if err := sh.Validate(); err != nil {
		return State{}, err
	}
	return s.change(ctx, func(cur *Settings) error {
		cur.Sherlock = &sh
		return nil
	})
}

// UpdateAcks changes one cluster's acknowledged alerts: change gets the
// list as it is and returns the new one. It may be called more than once,
// when another replica saved in between.
func (s *Service) UpdateAcks(ctx context.Context, cluster string, change func([]acks.Ack) []acks.Ack) (State, error) {
	return s.change(ctx, func(cur *Settings) error {
		list := change(append([]acks.Ack(nil), cur.Acks[cluster]...))
		if err := acks.Validate(list); err != nil {
			return err
		}
		all := map[string][]acks.Ack{}
		for k, v := range cur.Acks {
			all[k] = v
		}
		if len(list) == 0 {
			delete(all, cluster)
		} else {
			all[cluster] = list
		}
		if len(all) == 0 {
			all = nil
		}
		cur.Acks = all
		return nil
	})
}

// UpdateFleets replaces one cluster's fleets.
func (s *Service) UpdateFleets(ctx context.Context, cluster string, list []fleets.Fleet) (State, error) {
	list = fleets.Clean(list)
	if err := fleets.Validate(list); err != nil {
		return State{}, err
	}
	return s.change(ctx, func(cur *Settings) error {
		all := map[string][]fleets.Fleet{}
		for k, v := range cur.Fleets {
			all[k] = v
		}
		if len(list) == 0 {
			delete(all, cluster)
		} else {
			all[cluster] = list
		}
		if len(all) == 0 {
			all = nil
		}
		cur.Fleets = all
		return nil
	})
}

// saveAttempts bounds the retries of a change that keeps meeting another
// replica's saves.
const saveAttempts = 5

// change reads what the storage holds now, applies edit to it, and saves
// the result over that version; when another replica saved first, it
// reads again and applies the edit to that. A change thus never puts
// back what this replica last read.
func (s *Service) change(ctx context.Context, edit func(*Settings) error) (State, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for attempt := 1; ; attempt++ {
		b, version, err := s.storage.Load(ctx)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return State{}, err
		}
		in, extra := s.Current().Settings, map[string]json.RawMessage(nil)
		if err == nil {
			doc, derr := decode(b)
			switch {
			case derr != nil:
				// Storage that cannot be read is replaced, starting from what
				// is in force here.
				s.log.Warn("the saved settings cannot be read; saving over them", "err", derr)
			case doc.newer:
				return State{}, ErrNewer
			case doc.settings.Validate() != nil:
				// Out of range (edited by hand): start from what is in force,
				// keeping the fields it does not know.
				extra = doc.extra
			default:
				in, extra = doc.settings, doc.extra
			}
		} else {
			in = s.defaults
		}
		if err := edit(&in); err != nil {
			return State{}, err
		}
		if err := in.Validate(); err != nil {
			return State{}, err
		}
		out := encode(in, extra)
		next, err := s.storage.Save(ctx, out, version)
		if errors.Is(err, ErrConflict) && attempt < saveAttempts {
			continue
		}
		if err != nil {
			return State{}, err
		}
		now := time.Now().UTC()
		s.mu.Lock()
		s.current.Settings = in
		s.current.ChangedAt = &now
		s.loaded, s.version, s.newer = out, next, false
		st := s.current
		s.mu.Unlock()
		s.log.Info("settings changed", "refresh", in.Refresh(), "storage", s.storage.Kind())
		s.onChange(st)
		return st, nil
	}
}

// Reload reads the storage again, so a change saved by another replica,
// or a ConfigMap edited by hand, takes effect. It is cheap when nothing
// changed, so Run can call it every few seconds.
func (s *Service) Reload(ctx context.Context) { s.reload(ctx, false) }

func (s *Service) reload(ctx context.Context, initial bool) {
	// Ordered with updates: a read that started before a change was saved
	// would otherwise put the old settings back once the change is in.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	b, version, err := s.storage.Load(ctx)
	switch {
	case errors.Is(err, ErrNotFound):
		s.mu.Lock()
		s.version = version
		s.mu.Unlock()
		if initial {
			s.log.Info("no saved settings; using defaults", "refresh", s.Current().Refresh(), "storage", s.storage.Kind())
			s.onChange(s.Current())
		}
		return
	case err != nil:
		s.log.Warn("cannot read the saved settings", "err", err)
		if initial {
			s.onChange(s.Current())
		}
		return
	}
	s.mu.Lock()
	same := (version != "" && version == s.version) || string(b) == string(s.loaded)
	s.version = version
	s.mu.Unlock()
	if same && !initial {
		return
	}
	doc, err := decode(b)
	if err != nil {
		s.log.Warn("the saved settings are not valid JSON; keeping the current ones", "err", err)
		return
	}
	if doc.newer {
		s.log.Warn("the settings were saved by a newer 221B; reading what this one knows of them and saving nothing until it is upgraded", "format", doc.version, "known", FormatVersion)
	}
	in := doc.settings
	if err := in.Validate(); err != nil {
		s.log.Warn("the saved settings are out of range; keeping the current ones", "err", err)
		return
	}
	s.mu.Lock()
	s.loaded, s.newer = b, doc.newer
	changed := !in.Equal(s.current.Settings)
	s.current.Settings = in
	if !initial && changed {
		now := time.Now().UTC()
		s.current.ChangedAt = &now
	}
	st := s.current
	s.mu.Unlock()
	if initial {
		s.log.Info("settings loaded", "refresh", in.Refresh(), "storage", s.storage.Kind(), "format", doc.version)
	} else if changed {
		s.log.Info("settings changed elsewhere", "refresh", in.Refresh())
	}
	if initial || changed {
		s.onChange(st)
	}
}

// Run re-reads the storage every interval until ctx is done.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
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
