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
	"sync"
	"time"
)

// Settings is what the Settings dialog changes for everyone.
type Settings struct {
	// RefreshSeconds is how often the server reads what it does not watch
	// (CPU and memory in use), rebuilds the rows that changed and sends every
	// browser the same update at the same moment.
	RefreshSeconds int `json:"refreshSeconds"`
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
	return nil
}

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

// Update validates, saves and applies new settings.
func (s *Service) Update(ctx context.Context, in Settings) (State, error) {
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
	changed := in != s.current.Settings
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
