package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// The keys of the runs ConfigMap (<release>-runs): the jobs with their
// settings and schedule state, and the runs with their findings.
const (
	JobsKey = "jobs.json"
	RunsKey = "runs.json"
)

// Defaults of what the runs keep (JB-14): the newest runs, at most so many
// and so many bytes; the oldest go first, those with open findings last.
const (
	DefaultMaxRuns   = 400
	DefaultRunsBytes = 720 << 10
	DefaultJobsBytes = 64 << 10
)

// What one run keeps at most.
const (
	maxReport    = 12000
	maxFindings  = 40
	maxTitle     = 200
	maxDetail    = 2000
	maxEvidence  = 10
	maxEvidenceL = 500
	maxProposals = 10
)

// maxExpectedWhy is the most an expected finding's reason keeps (CF-82);
// maxCheckWhy the most the description of an id an agent made up keeps
// (CF-86).
const (
	maxExpectedWhy = 500
	maxCheckWhy    = 500
)

// Settings are the jobs' own settings (JB-04, JB-07).
type Settings struct {
	// Editors are the roles that may edit jobs besides a SuperAdmin, who
	// always may: admin, superViewer or viewer.
	Editors []string `json:"editors,omitempty"`
	// ReaderRole is the role agents on their own read the clusters with:
	// viewer (the default) or superViewer. ReaderGroup, for viewer, is a
	// group of that role whose namespaces they read.
	ReaderRole  string    `json:"readerRole,omitempty"`
	ReaderGroup string    `json:"readerGroup,omitempty"`
	SetBy       string    `json:"setBy,omitempty"`
	SetAt       time.Time `json:"setAt,omitzero"`
}

// The roles agents on their own may read with: read-only ones.
const (
	ReaderViewer      = "viewer"
	ReaderSuperViewer = "superViewer"
)

// Role is the reader's role, with the default filled in.
func (s Settings) Role() string {
	if s.ReaderRole == ReaderSuperViewer {
		return ReaderSuperViewer
	}
	return ReaderViewer
}

// daySpend is what a job spent on a day (UTC), and whether it stopped for
// the budget, so the stop is noted once.
type daySpend struct {
	Day     string  `json:"day"`
	USD     float64 `json:"usd"`
	Stopped string  `json:"stopped,omitempty"`
}

// jobsRecord is the jobs key: the jobs changed from their kind's
// defaults or added, each job's newest scheduled time handled, the day's
// spend and the settings.
type jobsRecord struct {
	Jobs     map[string]Job       `json:"jobs,omitempty"`
	Handled  map[string]time.Time `json:"handled,omitempty"`
	Spend    map[string]daySpend  `json:"spend,omitempty"`
	Settings Settings             `json:"settings"`
	// Next numbers the jobs added.
	Next int `json:"next,omitempty"`
}

// runsRecord is the runs key: the runs, newest first.
type runsRecord struct {
	Next int   `json:"next"`
	Runs []Run `json:"runs"`
}

func loadJSON[T any](ctx context.Context, b kv.Backend, key string, v *T) (string, error) {
	raw, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return version, nil
	}
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return "", fmt.Errorf("the jobs' record %s cannot be read: %w", key, err)
	}
	return version, nil
}

// errUnchanged, returned by an update's function, saves nothing.
var errUnchanged = errors.New("unchanged")

// update changes a key over the version read, again when another replica
// wrote what holds it first. A function that returns errUnchanged saves
// nothing.
func update[T any](ctx context.Context, b kv.Backend, key string, f func(*T) error, fit func(*T) []byte) error {
	for try := 0; try < 10; try++ {
		var v T
		version, err := loadJSON(ctx, b, key, &v)
		if err != nil {
			return err
		}
		if err := f(&v); errors.Is(err, errUnchanged) {
			return nil
		} else if err != nil {
			return err
		}
		out := fit(&v)
		if _, err = b.SaveKey(ctx, key, out, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("the jobs' record %s kept changing", key)
}

func (s *Service) loadJobs(ctx context.Context) (jobsRecord, error) {
	var rec jobsRecord
	_, err := loadJSON(ctx, s.o.Backend, JobsKey, &rec)
	return rec, err
}

func (s *Service) updateJobs(ctx context.Context, f func(*jobsRecord) error) error {
	return update(ctx, s.o.Backend, JobsKey, f, func(r *jobsRecord) []byte {
		b, _ := json.Marshal(r)
		return b
	})
}

func (s *Service) loadRuns(ctx context.Context) (runsRecord, error) {
	var rec runsRecord
	_, err := loadJSON(ctx, s.o.Backend, RunsKey, &rec)
	return rec, err
}

func (s *Service) updateRuns(ctx context.Context, f func(*runsRecord) error) error {
	return update(ctx, s.o.Backend, RunsKey, f, func(r *runsRecord) []byte { return s.fitRuns(r) })
}

// fitRuns trims the runs to their count and bytes (JB-14): the oldest
// ended run goes first (its findings live on in their cases). A run under
// way stays.
func (s *Service) fitRuns(r *runsRecord) []byte {
	drop := func() bool {
		for i := len(r.Runs) - 1; i >= 0; i-- {
			if r.Runs[i].Status == StatusRunning {
				continue
			}
			r.Runs = append(r.Runs[:i:i], r.Runs[i+1:]...)
			return true
		}
		return false
	}
	for len(r.Runs) > s.o.MaxRuns && drop() {
	}
	for {
		b, _ := json.Marshal(r)
		if len(b) <= s.o.RunsBytes || !drop() {
			return b
		}
	}
}

// clip cuts a text an agent wrote to at most n characters, with anything
// that looks like a credential taken out (X-01).
func clip(s string, n int) string {
	s = scrub.Text(strings.TrimSpace(s))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// tidy bounds and scrubs a run's findings, numbers them, and gives each
// its key, or says why it has none (CF-05). Past maxFindings the least
// severe are left out, whatever their order (W-3): it says how many, by
// severity, with the keys of those left out, which never count as not
// seen.
func (s *Service) tidy(ctx context.Context, k Kind, runID string, in []Finding) ([]Finding, []int, *LeftOut, []Key) {
	var out []Finding
	// at is where each finding the run made went among those kept, -1 for
	// one dropped: the proposals name them by position (CF-26).
	at := make([]int, len(in))
	// The validation rules, read once for the run (ADR-138).
	var rules Rules
	// Which findings are kept: every one with a title, up to the cap, the
	// most severe first; those kept stay in the order they came.
	var titled []int
	in = slices.Clone(in)
	for i := range in {
		in[i].Title = clip(in[i].Title, maxTitle)
		in[i].Severity = normalSeverity(in[i].Severity)
		if in[i].Title != "" {
			titled = append(titled, i)
		}
	}
	keep := map[int]bool{}
	var left *LeftOut
	var leftKeys []Key
	if len(titled) > maxFindings {
		order := slices.Clone(titled)
		slices.SortStableFunc(order, func(a, b int) int { return findingRank(in[a].Severity) - findingRank(in[b].Severity) })
		left = &LeftOut{Cap: maxFindings}
		for n, i := range order {
			if n < maxFindings {
				keep[i] = true
				continue
			}
			f := in[i]
			left.Count++
			switch f.Severity {
			case FindingCritical:
				left.Severities.Critical++
			case FindingInfo:
				left.Severities.Info++
			default:
				left.Severities.Warning++
			}
			if len(k.Checks) > 0 {
				if rules == nil {
					rules = s.rules(ctx)
				}
				f.Cluster = clip(f.Cluster, 100)
				f.Resource = clip(f.Resource, 300)
				f.Check = clip(f.Check, 100)
				if key, ok := leftOutKey(k, rules, f); ok {
					leftKeys = append(leftKeys, key)
				}
			}
		}
	} else {
		for _, i := range titled {
			keep[i] = true
		}
	}
	for i, f := range in {
		at[i] = -1
		if !keep[i] {
			continue
		}
		f.Detail = clip(f.Detail, maxDetail)
		var ev []string
		for _, e := range f.Evidence {
			if e = clip(e, maxEvidenceL); e != "" && len(ev) < maxEvidence {
				ev = append(ev, e)
			}
		}
		f.Evidence = ev
		f.Cluster, f.Resource = clip(f.Cluster, 100), clip(f.Resource, 300)
		f.Check = strings.ToLower(strings.TrimSpace(clip(f.Check, 100)))
		// An expected finding says why, with its evidence (CF-82): one
		// without a reason is routed as any other.
		f.ExpectedWhy = clip(f.ExpectedWhy, maxExpectedWhy)
		if f.ExpectedWhy == "" {
			f.Expected = false
		}
		if !f.Expected {
			f.ExpectedWhy = ""
		}
		// The description of an id the agent made up (CF-86): kept with an
		// other.<slug> check only.
		f.CheckWhy = clip(f.CheckWhy, maxCheckWhy)
		if !strings.HasPrefix(f.Check, "other.") {
			f.CheckWhy = ""
		}
		f.ID = fmt.Sprintf("%s.%d", runID, len(out)+1)
		f.Key, f.Routing, f.Case, f.KnownIssue, f.Exception, f.InvalidReason, f.NewCheck, f.Unverified = nil, "", "", "", "", "", false, false
		if len(k.Checks) > 0 {
			// Only a kind with a catalogue makes findings to route; another's
			// (a report's triage) stay on its run as they are.
			if rules == nil {
				rules = s.rules(ctx)
			}
			s.keyOf(ctx, k, rules, &f)
		}
		at[i] = len(out)
		out = append(out, f)
	}
	return out, at, left, leftKeys
}
