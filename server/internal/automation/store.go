package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// The keys of Automation's ConfigMap (AU-12).
const (
	ScriptsKey   = "scripts.json"
	VariablesKey = "params.json"
	RunsKey      = "runs.json"
)

// Limits (AU-02, AU-12).
const (
	MaxText         = 32 << 10
	MaxScripts      = 200
	MaxGroup        = 40
	maxScriptsBytes = 384 << 10
	changeTries     = 12
)

// Version states (AU-05).
const (
	StateDraft      = "draft"
	StateReview     = "review"
	StateApproved   = "approved"
	StateSuperseded = "superseded"
	StateRejected   = "rejected"
	// MaxComment is the longest a reason given with a decision may be.
	MaxComment = 500
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("no such script")
	ErrExists   = errors.New("a script of that name exists")
	ErrFull     = errors.New("automation's records are full")
	ErrInvalid  = errors.New("not valid")
	// ErrOwnWork is a person approving a version they wrote or submitted.
	ErrOwnWork = errors.New("someone else must approve it")
	// ErrBusy is a run whose nodes another run has, or a change a newer
	// one overtook.
	ErrBusy = errors.New("in use")
	// ErrSameRequest is a run asked for twice: the first is the answer.
	ErrSameRequest = errors.New("this run was started already")
	// errNoChange ends an edit that leaves the record as it is.
	errNoChange = errors.New("nothing to save")
)

// busyError is a refusal for ErrBusy whose text says it all.
type busyError string

func (e busyError) Error() string        { return string(e) }
func (e busyError) Is(target error) bool { return target == ErrBusy }

// NameRE is a script's name.
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Who is a person as Automation records them: Name is shown, User tells
// people apart.
type Who struct {
	Name string `json:"name"`
	User string `json:"user,omitempty"`
}

// Same says whether two records name one person.
func (w Who) Same(o Who) bool {
	if w.User != "" || o.User != "" {
		return w.User == o.User
	}
	return w.Name == o.Name
}

// Approval is one person's approval of a version, or their decision
// against it; Comment is the reason given with a rejection.
type Approval struct {
	By      Who       `json:"by"`
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

// Version is one hashed text of a script. A draft changes as it is saved;
// submitted, it never changes again.
type Version struct {
	N        int       `json:"n"`
	Text     string    `json:"text"`
	Hash     string    `json:"hash"`
	Header   Header    `json:"header"`
	Findings []Finding `json:"findings"`
	State    string    `json:"state"`
	Author   Who       `json:"author"`
	// Editors are the others who saved this draft.
	Editors   []Who      `json:"editors,omitempty"`
	Created   time.Time  `json:"created"`
	Submitted *Approval  `json:"submitted,omitempty"`
	Approvals []Approval `json:"approvals,omitempty"`
	// Revoked are the people who took its approvals back, and when;
	// Withdrawn the approvals they took back, kept for the record.
	Revoked   []Approval `json:"revoked,omitempty"`
	Withdrawn []Approval `json:"withdrawn,omitempty"`
	// SameAs is the rejected version whose text this one has again.
	SameAs int `json:"sameAs,omitempty"`
	// Rejected is who turned it down in review, when and why.
	Rejected *Approval `json:"rejected,omitempty"`
	// Review is Sherlock's latest reading of it, which never approves.
	Review *Review `json:"review,omitempty"`
}

// Review is what Sherlock said of a version, for the person who approves.
type Review struct {
	Summary  string          `json:"summary"`
	Findings []ReviewFinding `json:"findings,omitempty"`
	At       time.Time       `json:"at"`
}

// ReviewFinding is one problem it saw.
type ReviewFinding struct {
	Level string `json:"level"`
	Line  int    `json:"line,omitempty"`
	Msg   string `json:"msg"`
}

// Involved says whether a person wrote or submitted the version, which
// keeps them from approving it.
func (v Version) Involved(w Who) bool {
	if w.Same(v.Author) || (v.Submitted != nil && w.Same(v.Submitted.By)) {
		return true
	}
	for _, e := range v.Editors {
		if w.Same(e) {
			return true
		}
	}
	return false
}

// Script is a script and its versions, oldest first.
type Script struct {
	Name     string    `json:"name"`
	Group    string    `json:"group,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Versions []Version `json:"versions"`
}

// Latest is the newest version.
func (s Script) Latest() Version {
	if len(s.Versions) == 0 {
		return Version{}
	}
	return s.Versions[len(s.Versions)-1]
}

// Approved is the newest approved version.
func (s Script) Approved() (Version, bool) {
	for i := len(s.Versions) - 1; i >= 0; i-- {
		if s.Versions[i].State == StateApproved {
			return s.Versions[i], true
		}
	}
	return Version{}, false
}

// Version returns version n.
func (s Script) Version(n int) (Version, bool) {
	for _, v := range s.Versions {
		if v.N == n {
			return v, true
		}
	}
	return Version{}, false
}

// Runnable is the newest version approved by someone who did not write or
// submit it (AU-05).
func Runnable(s Script) (Version, error) {
	v, ok := s.Approved()
	if !ok {
		return Version{}, fmt.Errorf("%s has no approved version: submit one and have it approved", s.Name)
	}
	return v, approvedBySomeoneElse(s, v)
}

func approvedBySomeoneElse(s Script, v Version) error {
	for _, a := range v.Approvals {
		if !v.Involved(a.By) {
			return nil
		}
	}
	return fmt.Errorf("%s v%d was approved only by the person who wrote it: someone else must approve it", s.Name, v.N)
}

// RunVersion is the version a run uses: on a production cluster any
// approved version, the newest when n is 0; elsewhere any version but a
// rejected one, the latest when n is 0 (asked November 1 to 3, 2026).
func RunVersion(s Script, production bool, n int) (Version, error) {
	if production {
		if n == 0 {
			return Runnable(s)
		}
		v, ok := s.Version(n)
		if !ok {
			return Version{}, fmt.Errorf("%w: %s has no v%d", ErrNotFound, s.Name, n)
		}
		if v.State != StateApproved {
			return Version{}, fmt.Errorf("%s v%d is %s: a production cluster runs approved versions only", s.Name, n, v.State)
		}
		return v, approvedBySomeoneElse(s, v)
	}
	v, ok := s.Latest(), len(s.Versions) > 0
	if n > 0 {
		v, ok = s.Version(n)
	}
	if !ok {
		return Version{}, fmt.Errorf("%w: %s has no v%d", ErrNotFound, s.Name, n)
	}
	if v.State == StateRejected {
		return Version{}, fmt.Errorf("%w: %s v%d was rejected, so it never runs: save the script again for a new version", ErrInvalid, s.Name, v.N)
	}
	return v, nil
}

// VersionMay is what a person may do with a version, as the console
// decides it, for the page to show rather than work out again.
type VersionMay struct {
	// Approve and Reject are the person's; Involved says they wrote or
	// submitted it, so someone else must approve it.
	Approve  bool `json:"approve"`
	Reject   bool `json:"reject"`
	Involved bool `json:"involved"`
	// RunProduction and RunElsewhere say whether a run may take it on a
	// production cluster and elsewhere; Dry whether it has a dry run.
	RunProduction bool `json:"runProduction"`
	RunElsewhere  bool `json:"runElsewhere"`
	Dry           bool `json:"dry"`
}

// May works out what who may do with version v of s. An approved version
// someone else approved asks for no more approvals, though more are taken.
func May(s Script, v Version, who Who) VersionMay {
	m := VersionMay{Involved: v.Involved(who)}
	already := false
	for _, a := range v.Approvals {
		already = already || a.By.Same(who)
	}
	m.Approve = v.State != StateDraft && v.State != StateRejected && !m.Involved && !already &&
		(v.State != StateApproved || approvedBySomeoneElse(s, v) != nil)
	m.Reject = v.State == StateReview && !m.Involved
	_, err := RunVersion(s, true, v.N)
	m.RunProduction = err == nil
	_, err = RunVersion(s, false, v.N)
	m.RunElsewhere = err == nil
	_, err = DryRunnable(s, v.N)
	m.Dry = err == nil
	return m
}

// DryRunnable is the version a dry run uses: version n, or the latest when
// n is 0. A script that does not declare dry-run has no dry run: it would
// not read DRY_RUN, so it would try to do its work.
func DryRunnable(s Script, n int) (Version, error) {
	v, ok := s.Latest(), true
	if n > 0 {
		v, ok = s.Version(n)
	}
	if !ok {
		return Version{}, fmt.Errorf("%w: %s has no v%d", ErrNotFound, s.Name, n)
	}
	if !v.Header.DryRun {
		return Version{}, fmt.Errorf("%w: %s v%d does not declare dry-run yes, so it has no dry run", ErrInvalid, s.Name, v.N)
	}
	return v, nil
}

// Hash is a script text's SHA-256.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Normalize gives a text one line ending.
func Normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// Store keeps scripts, parameters and runs in Automation's key-value
// storage, each change applied over the version it read (AU-12).
type Store struct {
	backend kv.Backend
	now     func() time.Time
	mu      sync.Mutex
}

// RunsKeys are the keys that may live apart from the scripts, so neither
// presses on the other's 1 MiB: the runs and the nodes' facts.
var RunsKeys = []string{RunsKey, FactsKey}

// SetRunsBackend keeps the runs and the facts in b from now on; what the
// first backend still holds of them moves there with the next save.
func (st *Store) SetRunsBackend(b kv.Backend) {
	routes := map[string]kv.Backend{}
	for _, k := range RunsKeys {
		routes[k] = b
	}
	st.backend = &kv.Routed{Default: st.backend, Routes: routes}
}

// NewStore keeps the records in b; now is the clock (nil: time.Now).
func NewStore(b kv.Backend, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{backend: b, now: now}
}

// load reads a key into v; a key never saved leaves v as it is.
func (st *Store) load(ctx context.Context, key string, v any) (string, error) {
	b, version, err := st.backend.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return version, nil
	}
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(b, v); err != nil {
		// Read as empty, the next save would wipe it.
		return "", fmt.Errorf("%s in automation's ConfigMap is not readable: %w", key, err)
	}
	return version, nil
}

// change reads a key, edits it and saves it over the version read, again
// on a conflict with another replica's save.
func change[T any](ctx context.Context, st *Store, key string, limit int, edit func(*T) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	var err error
	for try := 0; try < changeTries; try++ {
		if try > 0 {
			select {
			case <-time.After(rand.N(min(50*time.Millisecond<<try, time.Second))):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var v T
		var version string
		if version, err = st.load(ctx, key, &v); err != nil {
			return err
		}
		if err = edit(&v); errors.Is(err, errNoChange) {
			return nil
		} else if err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		if limit > 0 && len(b) > limit {
			return fmt.Errorf("%w: %s would be %d KiB, over its %d KiB", ErrFull, key, len(b)>>10, limit>>10)
		}
		if _, err = st.backend.SaveKey(ctx, key, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		if errors.Is(err, kv.ErrFull) {
			return fmt.Errorf("%w: %w", ErrFull, err)
		}
		return err
	}
	return err
}

type scriptsRecord struct {
	Scripts map[string]Script `json:"scripts"`
}

// Scripts lists the scripts by name.
func (st *Store) Scripts(ctx context.Context) ([]Script, error) {
	var rec scriptsRecord
	if _, err := st.load(ctx, ScriptsKey, &rec); err != nil {
		return nil, err
	}
	out := make([]Script, 0, len(rec.Scripts))
	for _, s := range rec.Scripts {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Script returns one script.
func (st *Store) Script(ctx context.Context, name string) (Script, bool, error) {
	var rec scriptsRecord
	if _, err := st.load(ctx, ScriptsKey, &rec); err != nil {
		return Script{}, false, err
	}
	s, ok := rec.Scripts[name]
	return s, ok, nil
}

// Save creates a script, or saves its text and group (AU-02). A changed
// text is checked and goes into the draft; with no draft, it starts the
// next version. The same text changes only the group.
func (st *Store) Save(ctx context.Context, name, group, text string, who Who, create bool) (Script, error) {
	if !NameRE.MatchString(name) {
		return Script{}, fmt.Errorf("%w: a script's name is lower-case letters, digits and dashes, at most 63", ErrInvalid)
	}
	group = strings.TrimSpace(group)
	if len(group) > MaxGroup {
		return Script{}, fmt.Errorf("%w: a group's name is at most %d characters", ErrInvalid, MaxGroup)
	}
	text = Normalize(text)
	if len(text) > MaxText {
		return Script{}, fmt.Errorf("%w: a script is at most %d KiB; this one is %d KiB", ErrInvalid, MaxText>>10, (len(text)+1023)>>10)
	}
	if strings.TrimSpace(text) == "" {
		return Script{}, fmt.Errorf("%w: the script is empty", ErrInvalid)
	}
	used, err := st.usedVersions(ctx)
	if err != nil {
		return Script{}, err
	}
	var saved Script
	err = change(ctx, st, ScriptsKey, maxScriptsBytes, func(rec *scriptsRecord) error {
		if rec.Scripts == nil {
			rec.Scripts = map[string]Script{}
		}
		now := st.now().UTC()
		s, exists := rec.Scripts[name]
		switch {
		case create && exists:
			return ErrExists
		case !create && !exists:
			return ErrNotFound
		case create && len(rec.Scripts) >= MaxScripts:
			return fmt.Errorf("%w: at most %d scripts", ErrFull, MaxScripts)
		}
		if !exists {
			s = Script{Name: name, Created: now}
		}
		s.Group = group
		hash := Hash(text)
		if latest := s.Latest(); latest.Hash == hash && latest.State == StateRejected {
			// A rejection is final for that version: the same text again is
			// a new one, which says so.
			h, fs := Check(text)
			s.Versions = append(s.Versions, Version{N: latest.N + 1, Text: text, Hash: hash, Header: h, Findings: fs, State: StateDraft, Author: who, Created: now, SameAs: latest.N})
			s.Updated = now
		} else if latest.Hash != hash {
			h, fs := Check(text)
			if last := len(s.Versions) - 1; last >= 0 && s.Versions[last].State == StateDraft {
				d := &s.Versions[last]
				d.Text, d.Hash, d.Header, d.Findings = text, hash, h, fs
				if !d.Involved(who) {
					d.Editors = append(d.Editors, who)
				}
			} else {
				s.Versions = append(s.Versions, Version{N: s.Latest().N + 1, Text: text, Hash: hash, Header: h, Findings: fs, State: StateDraft, Author: who, Created: now})
			}
			s.Updated = now
		}
		rec.Scripts[name] = s
		prune(rec, maxScriptsBytes, used)
		saved = rec.Scripts[name]
		return nil
	})
	if err == nil {
		err = st.keepGroup(ctx, group)
	}
	return saved, err
}

// usedVersions are the versions the kept runs ran, by script.
func (st *Store) usedVersions(ctx context.Context) (map[string]map[int]bool, error) {
	var rec runsRecord
	if _, err := st.load(ctx, RunsKey, &rec); err != nil {
		return nil, err
	}
	used := map[string]map[int]bool{}
	for _, r := range rec.Runs {
		if used[r.Script] == nil {
			used[r.Script] = map[int]bool{}
		}
		used[r.Script][r.Version] = true
	}
	return used, nil
}

// prune drops superseded and rejected versions, oldest first, then drafts and reviews
// older than a script's newest version, until the record fits; the newest
// version, approved ones and those a kept run ran stay, so a run's record
// can always show the text it ran.
func prune(rec *scriptsRecord, limit int, used map[string]map[int]bool) {
	size := func() int {
		b, _ := json.Marshal(rec)
		return len(b)
	}
	for _, drop := range []func(Script, int) bool{
		func(s Script, i int) bool {
			return (s.Versions[i].State == StateSuperseded || s.Versions[i].State == StateRejected) && !used[s.Name][s.Versions[i].N]
		},
		func(s Script, i int) bool {
			st := s.Versions[i].State
			return (st == StateDraft || st == StateReview) && i < len(s.Versions)-1 && !used[s.Name][s.Versions[i].N]
		},
	} {
		for size() > limit {
			name, at, oldest := "", -1, time.Time{}
			for n, s := range rec.Scripts {
				for i := range s.Versions {
					if drop(s, i) && (at < 0 || s.Versions[i].Created.Before(oldest)) {
						name, at, oldest = n, i, s.Versions[i].Created
					}
				}
			}
			if at < 0 {
				break
			}
			s := rec.Scripts[name]
			s.Versions = append(s.Versions[:at:at], s.Versions[at+1:]...)
			rec.Scripts[name] = s
		}
	}
}

// Submit puts a draft version in review (AU-05), which freezes it and
// supersedes an older version still in review; a version with errors
// cannot be submitted.
func (st *Store) Submit(ctx context.Context, name string, n int, who Who) (Script, error) {
	return st.edit(ctx, name, n, func(s *Script, v *Version) error {
		if v.State != StateDraft {
			return fmt.Errorf("%w: v%d is %s, not a draft", ErrInvalid, v.N, v.State)
		}
		if HasErrors(v.Findings) {
			return fmt.Errorf("%w: v%d has errors to fix first", ErrInvalid, v.N)
		}
		v.State = StateReview
		v.Submitted = &Approval{By: who, At: st.now().UTC()}
		for i := range s.Versions {
			if s.Versions[i].N != v.N && s.Versions[i].State == StateReview {
				s.Versions[i].State = StateSuperseded
			}
		}
		return nil
	})
}

// Approve records a person's approval of a version in review, a further
// approval of an approved one, or the approval of an older version a newer
// one replaced, which brings it back (AU-05). Whoever wrote or submitted a version
// cannot approve it. Versions approved before stay approved, so a run can
// still choose them, until someone revokes them.
func (st *Store) Approve(ctx context.Context, name string, n int, who Who) (Script, error) {
	return st.edit(ctx, name, n, func(s *Script, v *Version) error {
		if v.State == StateDraft {
			return fmt.Errorf("%w: v%d is %s: submit it for review first", ErrInvalid, v.N, v.State)
		}
		if v.State == StateRejected {
			return fmt.Errorf("%w: v%d was rejected: save the script again for a new version", ErrInvalid, v.N)
		}
		if v.Involved(who) {
			return fmt.Errorf("%w: you wrote or submitted v%d", ErrOwnWork, v.N)
		}
		for _, a := range v.Approvals {
			if a.By.Same(who) {
				return fmt.Errorf("%w: you approved v%d already", ErrInvalid, v.N)
			}
		}
		v.Approvals = append(v.Approvals, Approval{By: who, At: st.now().UTC()})
		v.State = StateApproved
		return nil
	})
}

// Reject turns down a version in review, with the reason its author reads;
// whoever wrote or submitted it cannot. It is final: a fix is saved as the
// next version.
func (st *Store) Reject(ctx context.Context, name string, n int, who Who, comment string) (Script, error) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return Script{}, fmt.Errorf("%w: say why it is rejected", ErrInvalid)
	}
	if len(comment) > MaxComment {
		return Script{}, fmt.Errorf("%w: a comment is at most %d characters", ErrInvalid, MaxComment)
	}
	return st.edit(ctx, name, n, func(_ *Script, v *Version) error {
		if v.State != StateReview {
			return fmt.Errorf("%w: v%d is %s, not in review", ErrInvalid, v.N, v.State)
		}
		if v.Involved(who) {
			return fmt.Errorf("%w: you wrote or submitted v%d", ErrOwnWork, v.N)
		}
		v.Rejected = &Approval{By: who, At: st.now().UTC(), Comment: comment}
		v.State = StateRejected
		return nil
	})
}

// SetReview keeps Sherlock's reading of a version. It is advice, so it may
// be added to a frozen version; it changes no state.
func (st *Store) SetReview(ctx context.Context, name string, n int, r Review) (Script, error) {
	r.At = st.now().UTC()
	return st.edit(ctx, name, n, func(_ *Script, v *Version) error {
		v.Review = &r
		return nil
	})
}

// Revoke takes a version's approvals back: any SuperAdmin may, for any
// approved version, their own approval or not. It goes back to review, so
// nothing runs it on production until someone approves it again.
func (st *Store) Revoke(ctx context.Context, name string, n int, who Who) (Script, error) {
	return st.edit(ctx, name, n, func(s *Script, v *Version) error {
		if v.State != StateApproved {
			return fmt.Errorf("%w: v%d is %s, not approved", ErrInvalid, v.N, v.State)
		}
		v.Withdrawn = append(v.Withdrawn, v.Approvals...)
		v.Approvals = nil
		v.Revoked = append(v.Revoked, Approval{By: who, At: st.now().UTC()})
		v.State = StateReview
		return nil
	})
}

func (st *Store) edit(ctx context.Context, name string, n int, f func(*Script, *Version) error) (Script, error) {
	var saved Script
	err := change(ctx, st, ScriptsKey, maxScriptsBytes, func(rec *scriptsRecord) error {
		s, ok := rec.Scripts[name]
		if !ok {
			return ErrNotFound
		}
		at := -1
		for i := range s.Versions {
			if s.Versions[i].N == n {
				at = i
			}
		}
		if at < 0 {
			return fmt.Errorf("%w: %s has no v%d", ErrNotFound, name, n)
		}
		v := s.Versions[at]
		if err := f(&s, &v); err != nil {
			return err
		}
		s.Versions[at] = v
		s.Updated = st.now().UTC()
		rec.Scripts[name] = s
		saved = s
		return nil
	})
	return saved, err
}

// Delete removes a script and its versions.
func (st *Store) Delete(ctx context.Context, name string) error {
	if sc, ok, err := st.Script(ctx, name); err == nil && ok {
		if err := st.keepGroup(ctx, sc.Group); err != nil {
			return err
		}
	}
	return change(ctx, st, ScriptsKey, 0, func(rec *scriptsRecord) error {
		if _, ok := rec.Scripts[name]; !ok {
			return ErrNotFound
		}
		delete(rec.Scripts, name)
		return nil
	})
}

// PendingVersion is a version waiting for someone's approval.
type PendingVersion struct {
	Script    string     `json:"script"`
	Version   int        `json:"version"`
	Author    string     `json:"author"`
	Submitted *time.Time `json:"submitted,omitempty"`
}

// ToApprove lists the versions in review that a person may approve.
func (st *Store) ToApprove(ctx context.Context, who Who) ([]PendingVersion, error) {
	list, err := st.Scripts(ctx)
	if err != nil {
		return nil, err
	}
	out := []PendingVersion{}
	for _, s := range list {
		for _, v := range s.Versions {
			if v.State != StateReview || v.Involved(who) {
				continue
			}
			p := PendingVersion{Script: s.Name, Version: v.N, Author: v.Author.Name}
			if v.Submitted != nil {
				at := v.Submitted.At
				p.Submitted = &at
			}
			out = append(out, p)
		}
	}
	return out, nil
}
