package ai

import (
	"context"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// Sherlock's setup skill (Phase 12, GS-07 to GS-10, ADR-104): while
// Getting started has a step open, a SuperAdmin's Sherlock gets one more
// section of fixed instructions (prompts/setup.md) and one more tool,
// setup_status, which reads the checklist as the console works it out.
// It is not an agent of its own: it ends by itself once every step is
// done or dismissed, and leaves Sherlock's instructions as they were.

// SetupGuide is Getting started, as the API works it out.
type SetupGuide interface {
	// Open says whether a step is neither done nor dismissed.
	Open(ctx context.Context) bool
	// Status is the steps in words, for setup_status.
	Status(ctx context.Context) string
}

// SetSetup gives the service Getting started; the API makes it, so it
// comes after the service.
func (s *Service) SetSetup(g SetupGuide) {
	s.mu.Lock()
	s.setup = g
	s.mu.Unlock()
}

func (s *Service) setupGuide() SetupGuide {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setup
}

// SetupSkill is the setup skill's section of Sherlock's instructions.
func SetupSkill() string { return promptFile("setup") }

// recordedSetup is a test case's recorded setup_status, which turns the
// setup skill on for the case: nil when it recorded none.
func recordedSetup(rec []sherlock.Recorded) func(context.Context) string {
	for _, r := range rec {
		if r.Tool == sherlock.ToolSetupStatus {
			text := r.Result
			return func(context.Context) string { return text }
		}
	}
	return nil
}

// --- Inbox and Sherlock in AIOps (Phase 12) ---

// AIOpsSkill is the AIOps skill's section of Sherlock's instructions
// (IB-10, ADR-112): every page of the AIOps area and what can be done
// there, added while the person asks from AIOps.
func AIOpsSkill() string { return promptFile("aiops") }
