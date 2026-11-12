package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// While a draft is piloted, every answer of that agent is logged, the
// draft's and the live version's alike, so the two can be compared and
// rated before the draft goes live.

// PilotEntry is one answer logged during a pilot.
type PilotEntry struct {
	N        int       `json:"n"`
	Agent    string    `json:"agent"`
	Version  string    `json:"version"`
	At       time.Time `json:"at"`
	Who      string    `json:"who"`
	Cluster  string    `json:"cluster"`
	Question string    `json:"question"`
	Answer   string    `json:"answer"`
	Model    string    `json:"model"`
	CostUSD  float64   `json:"costUSD"`
	// Rating is 1 (better), -1 (worse) or 0 (not rated).
	Rating  int    `json:"rating,omitempty"`
	RatedBy string `json:"ratedBy,omitempty"`
}

// PilotLog is the pilots' answers, under PilotKey.
type PilotLog struct {
	Next    int          `json:"next"`
	Entries []PilotEntry `json:"entries,omitempty"`
}

// maxPilotEntries bounds the log.
const maxPilotEntries = 200

// OnAnswer logs an answer while its agent's draft is piloted: the engine's
// Options.OnAnswer.
func (s *Service) OnAnswer(a sherlock.Answered) {
	ap := s.cached().prompts.Agents[a.Agent]
	if ap == nil || ap.Draft == nil || ap.Draft.Stage != StagePilot || ap.Draft.Pilot == nil || !s.opts.Now().Before(ap.Draft.Pilot.Until) {
		return
	}
	who := a.Who.Email
	if who == "" {
		who = a.Who.Name
	}
	e := PilotEntry{Agent: a.Agent, Version: a.Version, At: s.opts.Now().UTC(), Who: who, Cluster: a.Cluster, Question: clip(a.Trace.Question, 300), Answer: clip(a.Msg.Text, 1200), Model: a.Msg.Model, CostUSD: a.Msg.CostUSD}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := change(ctx, s.opts.Backend, PilotKey, func(l *PilotLog) error {
		l.Next++
		e.N = l.Next
		l.Entries = append(l.Entries, e)
		if len(l.Entries) > maxPilotEntries {
			l.Entries = append([]PilotEntry(nil), l.Entries[len(l.Entries)-maxPilotEntries:]...)
		}
		return nil
	}); err != nil {
		s.opts.Logger.Warn("logging a pilot's answer failed", "err", err)
	}
}

// Pilot is an agent's logged answers, newest first.
func (s *Service) Pilot(ctx context.Context, id string) ([]PilotEntry, error) {
	var l PilotLog
	if _, err := load(ctx, s.opts.Backend, PilotKey, &l); err != nil {
		return nil, err
	}
	out := []PilotEntry{}
	for i := len(l.Entries) - 1; i >= 0; i-- {
		if l.Entries[i].Agent == id {
			out = append(out, l.Entries[i])
		}
	}
	return out, nil
}

// Rate rates a logged answer: 1, -1, or 0 to take a rating back.
func (s *Service) Rate(ctx context.Context, n, rating int, by string) error {
	if rating < -1 || rating > 1 {
		return fmt.Errorf("%w: a rating is 1, -1 or 0", ErrInvalid)
	}
	_, err := change(ctx, s.opts.Backend, PilotKey, func(l *PilotLog) error {
		i := slices.IndexFunc(l.Entries, func(e PilotEntry) bool { return e.N == n })
		if i < 0 {
			return ErrNotFound
		}
		l.Entries[i].Rating, l.Entries[i].RatedBy = rating, by
		if rating == 0 {
			l.Entries[i].RatedBy = ""
		}
		return nil
	})
	return err
}

// Export is the AI ConfigMap's keys for a backup, as kept, with each
// provider that has a saved credential carrying it as seal makes it:
// sealed with the install's backup key (ADR-137), or a placeholder. One
// the chart sets is clusters.Placeholder: it stays the chart's.
func (s *Service) Export(ctx context.Context, seal func(string) string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for _, key := range BackupKeys {
		b, _, err := s.opts.Backend.LoadKey(ctx, key)
		if err != nil {
			if errors.Is(err, kv.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if key == ProvidersKey {
			var ps struct {
				DefaultName string           `json:"defaultName,omitempty"`
				List        []map[string]any `json:"list,omitempty"`
			}
			if err := json.Unmarshal(b, &ps); err != nil {
				return nil, err
			}
			for _, p := range ps.List {
				if id, _ := p["id"].(string); id != "" && s.KeysFor(id).Info().Set {
					if k := s.KeysFor(id); k.Fixed() {
						p["credential"] = clusters.Placeholder
					} else {
						p["credential"] = seal(k.Key())
					}
				}
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(ps)
			b = bytes.TrimSpace(buf.Bytes())
		}
		out[key] = b
	}
	return out, nil
}

// Import puts back keys of a backup. A provider's credential is not
// saved here: the backup part opens it and saves it with SetCredential,
// where the provider has none (ADR-137).
func (s *Service) Import(ctx context.Context, keys map[string]json.RawMessage) error {
	defer s.forget()
	for key, b := range keys {
		if !slices.Contains(BackupKeys, key) {
			return fmt.Errorf("%w: %s is not one of the AI keys", ErrInvalid, key)
		}
		if key == ProvidersKey {
			var ps Providers
			if err := json.Unmarshal(b, &ps); err != nil {
				return err
			}
			for _, p := range ps.List {
				if err := p.Validate(); err != nil {
					return fmt.Errorf("%w: the provider %s: %v", ErrInvalid, p.ID, err)
				}
			}
			b, _ = json.Marshal(ps)
		} else if !json.Valid(b) || strings.TrimSpace(string(b)) == "" {
			return fmt.Errorf("%w: %s is not JSON", ErrInvalid, key)
		}
		_, version, err := s.opts.Backend.LoadKey(ctx, key)
		if err != nil && !errors.Is(err, kv.ErrNotFound) {
			return err
		}
		if _, err := s.opts.Backend.SaveKey(ctx, key, b, version); err != nil {
			return err
		}
	}
	return nil
}
