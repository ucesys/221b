package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// A backup (JB-21) holds the jobs and their settings: what each job does,
// when, on which clusters, its budget and state, who may edit jobs and
// what agents on their own read with. The runs and their findings are
// left out: they are a history that can be large, of a console that may
// be gone, and the next runs make them again.

// Exported is what a backup holds of the jobs.
type Exported struct {
	Jobs     map[string]Job `json:"jobs,omitempty"`
	Settings Settings       `json:"settings"`
	Next     int            `json:"next,omitempty"`
}

// Export is the jobs for a backup; nil when nothing was ever changed.
func (s *Service) Export(ctx context.Context) (*Exported, error) {
	rec, err := s.loadJobs(ctx)
	if err != nil {
		return nil, err
	}
	if len(rec.Jobs) == 0 && rec.Settings.SetBy == "" {
		return nil, nil
	}
	return &Exported{Jobs: rec.Jobs, Settings: rec.Settings, Next: rec.Next}, nil
}

// Import replaces the jobs and their settings with a backup's, and says
// how many jobs it restored. A job's schedule counts from the restore, so
// no time before it is made up; a job of a kind this console does not
// have is kept, and shows once the kind is back.
func (s *Service) Import(ctx context.Context, raw json.RawMessage, by string) (int, error) {
	var in Exported
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, fmt.Errorf("the jobs in the backup cannot be read: %w", err)
	}
	now := s.o.Now().UTC()
	for id, j := range in.Jobs {
		if j.ID != id || j.Kind == "" {
			return 0, fmt.Errorf("the job %q in the backup is not valid", id)
		}
		if k, ok := s.o.Kinds.Kind(j.Kind); ok {
			if err := s.check(j, k); err != nil {
				return 0, fmt.Errorf("the job %q in the backup: %w", id, err)
			}
		}
		if j.On {
			j.Since = now
		}
		in.Jobs[id] = j
	}
	if _, err := (Settings{}).validRole(in.Settings); err != nil {
		return 0, err
	}
	err := s.updateJobs(ctx, func(rec *jobsRecord) error {
		rec.Jobs, rec.Settings, rec.Next = in.Jobs, in.Settings, max(rec.Next, in.Next)
		rec.Handled = map[string]time.Time{}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.record(audit.Event{Action: ActionJob, Who: audit.Who{Name: by}, Target: "jobs", Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%d jobs and the jobs' settings restored from a backup; their schedules count from now", len(in.Jobs))})
	return len(in.Jobs), nil
}

// validRole checks a backup's reader role: never one that may change
// anything.
func (Settings) validRole(in Settings) (string, error) {
	switch in.ReaderRole {
	case "", ReaderViewer, ReaderSuperViewer:
		return in.Role(), nil
	}
	return "", fmt.Errorf("the backup's jobs read as %q; agents on their own read as a Viewer or a SuperViewer only", in.ReaderRole)
}
