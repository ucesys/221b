package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// BackupFormat names the file Export writes.
const BackupFormat = "221b-issues/2"

// backupFile is the issue service's part of a backup (IS-19): the known
// issues, services and settings, and the cases' numbering. The cases are
// the objects of their own a part of their own keeps (ExportCases). The
// tokens are left out by construction: a restored console makes new ones.
type backupFile struct {
	Format    string         `json:"format"`
	Next      int            `json:"next"`
	Archived  map[string]int `json:"archived,omitempty"`
	NextKnown int            `json:"nextKnown"`
	Known     []KnownIssue   `json:"known"`
	Services  []ServiceDef   `json:"services"`
	Settings  *Settings      `json:"settings,omitempty"`
	LeftOut   []string       `json:"leftOut"`
}

// Export writes the issue service's records for a backup.
func (s *Service) Export(ctx context.Context) ([]byte, error) {
	st, err := s.load(ctx, true)
	if err != nil {
		return nil, err
	}
	f := backupFile{Format: BackupFormat, Next: st.index.Next, Archived: st.index.Archived, NextKnown: st.known.Next,
		Known: st.known.Known, Services: st.config.Services, Settings: st.config.Settings, LeftOut: []string{"API tokens: make new ones after a restore"}}
	if f.Known == nil {
		f.Known = []KnownIssue{}
	}
	if f.Services == nil {
		f.Services = []ServiceDef{}
	}
	return json.Marshal(f)
}

// ImportResult says what a restore brought back and what it kept.
type ImportResult struct {
	Known    int `json:"known"`
	Services int `json:"services"`
	// Kept counts the records that were here already and stayed as they
	// are.
	Kept     int  `json:"kept"`
	Settings bool `json:"settings"`
}

// Import adds what a backup has and this console does not (IS-19): a
// report, known issue or service already here stays as it is; the numbers
// to come stay above every number restored. The settings are replaced
// only when withSettings.
func (s *Service) Import(ctx context.Context, b []byte, withSettings bool, by Person) (ImportResult, error) {
	var f backupFile
	if err := json.Unmarshal(b, &f); err != nil {
		return ImportResult{}, invalid("the issue service's part of the backup cannot be read: %v", err)
	}
	if f.Format != BackupFormat {
		return ImportResult{}, invalid("the issue service's part is %q, not %s", f.Format, BackupFormat)
	}
	if f.Settings != nil {
		if err := f.Settings.withDefaults().Validate(); err != nil {
			return ImportResult{}, err
		}
	}
	var out ImportResult
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		out = ImportResult{}
		st.index.Next = max(st.index.Next, f.Next)
		for k, v := range f.Archived {
			if st.index.Archived == nil {
				st.index.Archived = map[string]int{}
			}
			st.index.Archived[k] += v
		}
		for _, k := range f.Known {
			if st.knownIssue(k.ID) != nil {
				out.Kept++
				continue
			}
			st.known.Known = append(st.known.Known, k)
			out.Known++
			var n int
			if _, err := fmt.Sscanf(k.ID, "KI-%d", &n); err == nil {
				st.known.Next = max(st.known.Next, n+1)
			}
		}
		st.known.Next = max(st.known.Next, f.NextKnown)
		for _, sv := range f.Services {
			if st.service(sv.ID) != nil {
				out.Kept++
				continue
			}
			st.config.Services = append(st.config.Services, sv)
			out.Services++
		}
		if withSettings && f.Settings != nil {
			set := f.Settings.withDefaults()
			st.config.Settings = &set
			out.Settings = true
		}
		ch.audit(AuditRecord{Action: ActionImport, Who: by, Target: "issue service", Detail: fmt.Sprintf("restored %d known issues, %d services; kept %d already here; settings %v", out.Known, out.Services, out.Kept, out.Settings)})
		return saveReports | saveKnown | saveConfig, nil
	})
	return out, err
}

// --- One flow for findings and cases (Phase 12, wave 6, CF-22) ---

// ExportCases is every case object, by name, with its keys: the issue
// service's record and the console's work, for a backup.
func (s *Service) ExportCases(ctx context.Context) (map[string]map[string]string, error) {
	names, err := s.cases.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, n := range names {
		keys, _, err := s.cases.Load(ctx, n)
		if err != nil {
			continue
		}
		m := map[string]string{}
		for k, v := range keys {
			m[k] = string(v)
		}
		out[n] = m
	}
	return out, nil
}

// ImportCases makes the case objects of a backup this console does not
// have, and adds them to the index; one here stays as it is. It says how
// many it made and how many were here.
func (s *Service) ImportCases(ctx context.Context, objs map[string]map[string]string, by Person) (int, int, error) {
	added, kept := 0, 0
	for name, keys := range objs {
		var r Report
		if json.Unmarshal([]byte(keys[CaseKey]), &r) != nil || caseName(r.Number) != name {
			return added, kept, invalid("the case %q in the backup cannot be read", name)
		}
	}
	for name, keys := range objs {
		if _, _, err := s.cases.Load(ctx, name); err == nil {
			kept++
			continue
		}
		version := ""
		for _, k := range sortedKeys(keys) {
			v, err := s.cases.Save(ctx, name, k, []byte(keys[k]), version)
			if err != nil {
				return added, kept, err
			}
			version = v
		}
		added++
	}
	if _, err := s.Reindex(ctx); err != nil {
		return added, kept, err
	}
	s.record(AuditRecord{Action: ActionImport, Who: by, Target: "cases", Detail: fmt.Sprintf("restored %d cases; kept %d already here", added, kept)})
	return added, kept, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
