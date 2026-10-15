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
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/fleets"
)

// Settings is what the Settings dialog changes for everyone.
type Settings struct {
	// RefreshSeconds is how often the server reads what it does not watch
	// (CPU and memory in use), rebuilds the rows that changed and sends every
	// browser the same update at the same moment.
	RefreshSeconds int `json:"refreshSeconds"`
	// Fleets are each cluster's fleets (P4-01), by the cluster's
	// connection id.
	Fleets map[string][]fleets.Fleet `json:"fleets,omitempty"`
	// Acks are each cluster's acknowledged alerts, by the cluster's
	// connection id.
	Acks map[string][]acks.Ack `json:"acks,omitempty"`
	// Sherlock is how Sherlock answers (P5-05); nil is the defaults, with
	// Sherlock off everywhere.
	Sherlock *Sherlock `json:"sherlock,omitempty"`
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
}

// DefaultSherlock is a fresh installation's: off, with caps in place.
var DefaultSherlock = Sherlock{Model: "claude-opus-5-5", DailyUSD: 20, MonthlyUSD: 200}

// SherlockOrDefault returns the Sherlock settings in force.
func (s Settings) SherlockOrDefault() Sherlock {
	if s.Sherlock == nil {
		return DefaultSherlock
	}
	return *s.Sherlock
}

var modelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.:@_-]{1,79}$`)

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
	return nil
}

// Equal says whether two settings are the same.
func (s Settings) Equal(o Settings) bool { return reflect.DeepEqual(s, o) }

// ErrNotFound is what a Storage returns when nothing has been saved yet.
var ErrNotFound = errors.New("no settings saved")

// Storage keeps the settings between restarts.
type Storage interface {
	// Load returns the saved settings as JSON, or ErrNotFound.
	Load(ctx context.Context) ([]byte, error)
	// Save writes the settings as JSON.
	Save(ctx context.Context, b []byte) error
	// Kind names the storage for the browser: "configmap" or "memory".
	Kind() string
}

// Memory keeps settings until the server restarts: the laptop and
// simulator modes.
type Memory struct {
	mu sync.Mutex
	b  []byte
}

func (m *Memory) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.b == nil {
		return nil, ErrNotFound
	}
	return m.b, nil
}

func (m *Memory) Save(_ context.Context, b []byte) error {
	m.mu.Lock()
	m.b = append([]byte(nil), b...)
	m.mu.Unlock()
	return nil
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
// changes to it.
type Service struct {
	storage Storage
	log     *slog.Logger
	// onChange is told about every change, including the initial load.
	onChange func(State)

	mu      sync.Mutex
	current State
	loaded  []byte // what storage last held, to notice outside edits
	// writeMu orders updates, so a change to one part of the settings
	// does not lose a change made to another part at the same time.
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
	s := &Service{storage: storage, log: log, onChange: onChange, current: State{Settings: defaults, Storage: storage.Kind()}}
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if in.Fleets == nil {
		in.Fleets = s.Current().Fleets
	}
	if in.Acks == nil {
		in.Acks = s.Current().Acks
	}
	if in.Sherlock == nil {
		in.Sherlock = s.Current().Sherlock
	}
	return s.update(ctx, in)
}

// UpdateSherlock replaces the Sherlock settings.
func (s *Service) UpdateSherlock(ctx context.Context, sh Sherlock) (State, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := sh.Validate(); err != nil {
		return State{}, err
	}
	in := s.Current().Settings
	in.Sherlock = &sh
	return s.update(ctx, in)
}

// UpdateAcks changes one cluster's acknowledged alerts: change gets the
// list as it is and returns the new one.
func (s *Service) UpdateAcks(ctx context.Context, cluster string, change func([]acks.Ack) []acks.Ack) (State, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	in := s.Current().Settings
	list := change(append([]acks.Ack(nil), in.Acks[cluster]...))
	if err := acks.Validate(list); err != nil {
		return State{}, err
	}
	all := map[string][]acks.Ack{}
	for k, v := range in.Acks {
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
	in.Acks = all
	return s.update(ctx, in)
}

// UpdateFleets replaces one cluster's fleets.
func (s *Service) UpdateFleets(ctx context.Context, cluster string, list []fleets.Fleet) (State, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	list = fleets.Clean(list)
	if err := fleets.Validate(list); err != nil {
		return State{}, err
	}
	in := s.Current().Settings
	all := map[string][]fleets.Fleet{}
	for k, v := range in.Fleets {
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
	in.Fleets = all
	return s.update(ctx, in)
}

func (s *Service) update(ctx context.Context, in Settings) (State, error) {
	if err := in.Validate(); err != nil {
		return State{}, err
	}
	b, _ := json.Marshal(in)
	if err := s.storage.Save(ctx, b); err != nil {
		return State{}, err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.current.Settings = in
	s.current.ChangedAt = &now
	s.loaded = b
	st := s.current
	s.mu.Unlock()
	s.log.Info("settings changed", "refresh", in.Refresh(), "storage", s.storage.Kind())
	s.onChange(st)
	return st, nil
}

// Reload reads the storage again, so a ConfigMap edited by hand takes
// effect. Run calls it periodically.
func (s *Service) Reload(ctx context.Context) { s.reload(ctx, false) }

func (s *Service) reload(ctx context.Context, initial bool) {
	b, err := s.storage.Load(ctx)
	switch {
	case errors.Is(err, ErrNotFound):
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
	same := string(b) == string(s.loaded)
	s.mu.Unlock()
	if same {
		return
	}
	var in Settings
	if err := json.Unmarshal(b, &in); err != nil {
		s.log.Warn("the saved settings are not valid JSON; keeping the current ones", "err", err)
		return
	}
	if err := in.Validate(); err != nil {
		s.log.Warn("the saved settings are out of range; keeping the current ones", "err", err)
		return
	}
	s.mu.Lock()
	s.loaded = b
	changed := !in.Equal(s.current.Settings)
	s.current.Settings = in
	if !initial && changed {
		now := time.Now().UTC()
		s.current.ChangedAt = &now
	}
	st := s.current
	s.mu.Unlock()
	if initial {
		s.log.Info("settings loaded", "refresh", in.Refresh(), "storage", s.storage.Kind())
	} else if changed {
		s.log.Info("settings changed outside the console", "refresh", in.Refresh())
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
