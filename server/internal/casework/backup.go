package casework

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
)

// A backup (CW-13, CU-14, CF-22) holds every case object whole, by its
// name: the issue service's record and the work on it (the analyses, the
// ids of the changes it proposed, the fix, the RCA, the history and the
// drafts); and Settings › Cases with the check ids seen. No record holds
// a credential: what people and agents write is scrubbed when kept.

// Exported is what a backup holds of the cases.
type Exported struct {
	Cases    map[string]map[string]string `json:"cases,omitempty"`
	Settings *Settings                    `json:"settings,omitempty"`
	Checks   map[string][]string          `json:"checks,omitempty"`
}

// Export is the cases for a backup; nil when there is none.
func (s *Service) Export(ctx context.Context) (*Exported, error) {
	objs, err := s.o.Issues.ExportCases(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := s.loadRecord(ctx)
	if err != nil {
		return nil, err
	}
	var checks checksRecord
	if _, err := loadJSON(ctx, s.o.Backend, ChecksKey, &checks); err != nil {
		return nil, err
	}
	set := rec.Settings
	if len(objs) == 0 && len(checks.Clusters) == 0 && reflect.DeepEqual(set, DefaultSettings()) {
		return nil, nil
	}
	return &Exported{Cases: objs, Settings: &set, Checks: checks.Clusters}, nil
}

// Import makes the cases of a backup this console does not have; a case
// here stays as it is. Settings › Cases are restored, and the checks seen
// added. It says how many cases it added and how many were here already.
func (s *Service) Import(ctx context.Context, raw json.RawMessage, by string) (int, int, error) {
	var in Exported
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, 0, fmt.Errorf("the cases in the backup cannot be read: %w", err)
	}
	if in.Settings != nil {
		if err := in.Settings.withDefaults().Validate(); err != nil {
			return 0, 0, err
		}
	}
	added, kept, err := s.o.Issues.ImportCases(ctx, in.Cases, issues.Person{ID: "system:restore", Name: by, System: true})
	if err != nil {
		return added, kept, err
	}
	if in.Settings != nil {
		set := in.Settings.withDefaults()
		set.DuplicatesOfAgentCases = 0
		_ = s.updateRecord(ctx, func(rec *record) error { rec.Settings = set; return nil })
	}
	if len(in.Checks) > 0 {
		_ = update(ctx, s.o.Backend, ChecksKey, func(rec *checksRecord) error {
			if rec.Clusters == nil {
				rec.Clusters = map[string][]string{}
			}
			for c, list := range in.Checks {
				for _, x := range list {
					found := false
					for _, y := range rec.Clusters[c] {
						found = found || x == y
					}
					if !found {
						rec.Clusters[c] = append(rec.Clusters[c], x)
					}
				}
			}
			return nil
		})
	}
	s.record(audit.Event{Action: ActionCreate, Who: audit.Who{Name: by}, Target: "cases", Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%d cases restored from a backup, %d were here already", added, kept)})
	return added, kept, nil
}
