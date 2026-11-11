package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// The keys the service keeps its records under (IS-17; Phase 12, wave 6,
// CF-20). Each case is an object of its own (a ConfigMap,
// <release>-case-<n>), its record under CaseKey; the service's own
// ConfigMap holds the index, one small line per case, which lists, the
// Inbox, the agents' repeats and the similar cases read without loading
// every case; the known issues; and the rest (settings, services,
// tokens). A save writes only what changed.
const (
	IndexKey  = "issues-index.json"
	KnownKey  = "issues-known.json"
	ConfigKey = "issues-config.json"
	// CaseKey is a case object's key of the issue service's record; the
	// console keeps its own work on the case beside it (WorkKey).
	CaseKey = "case.json"
	// WorkKey is the console's key in a case object (package casework).
	WorkKey = "work.json"
)

// The most each key may hold by default; together under the 960 KiB a
// ConfigMap of the console has room for.
const (
	DefaultIndexBytes  = 640 << 10
	DefaultKnownBytes  = 192 << 10
	DefaultConfigBytes = 96 << 10
	// MaxCaseBytes bounds one case object's record, under the 1 MiB
	// Kubernetes keeps in one object with room for the console's work.
	MaxCaseBytes = 640 << 10
)

// Limits are what each key may hold, and the caps on what one record may
// carry, so that one person cannot fill the store.
type Limits struct {
	IndexBytes  int
	KnownBytes  int
	ConfigBytes int
}

// The caps on one record (IS-17).
const (
	// MaxText bounds a report's text and a message, in characters.
	MaxText = 4000
	// MaxMessages bounds a report's conversation.
	MaxMessages = 200
	// MaxWatchers bounds who follows one report or known issue.
	MaxWatchers = 100
	// MaxSymptoms bounds a known issue's symptoms, each of MaxShort
	// characters.
	MaxSymptoms = 30
	MaxShort    = 300
	// MaxUpdates bounds a known issue's log.
	MaxUpdates = 100
	// MaxServices and MaxTokens bound the configuration.
	MaxServices = 300
	MaxTokens   = 50
)

func (l Limits) withDefaults() Limits {
	if l.IndexBytes <= 0 {
		l.IndexBytes = DefaultIndexBytes
	}
	if l.KnownBytes <= 0 {
		l.KnownBytes = DefaultKnownBytes
	}
	if l.ConfigBytes <= 0 {
		l.ConfigBytes = DefaultConfigBytes
	}
	return l
}

// ErrFull is a save refused because a key would pass its limit; the
// message says what to remove.
var ErrFull = errors.New("the issue service is full")

// Line is one case in the index (CF-20): what a list row, the Inbox, the
// agents' repeats and the similar cases need, so they read no case
// object.
type Line struct {
	Number   int     `json:"number"`
	Title    string  `json:"title,omitempty"`
	Status   Status  `json:"status"`
	Outcome  Outcome `json:"outcome,omitempty"`
	Priority int     `json:"priority"`
	Source   string  `json:"source,omitempty"`
	// AgentFound is a case opened from a run's finding.
	AgentFound bool   `json:"agentFound,omitempty"`
	Cluster    string `json:"cluster,omitempty"`
	Object     string `json:"object,omitempty"`
	// Key is an agent-found case's; Job the job that found it.
	Key *Key   `json:"key,omitempty"`
	Job string `json:"job,omitempty"`
	// Reporter, FiledBy and Watchers are the people who see it on the
	// report page, by id; ReporterName and OpenedBy name who opened it.
	Reporter     string   `json:"reporter,omitempty"`
	ReporterName string   `json:"reporterName,omitempty"`
	FiledBy      string   `json:"filedBy,omitempty"`
	OpenedBy     string   `json:"openedBy,omitempty"`
	Watchers     []string `json:"watchers,omitempty"`
	Assignee     *Person  `json:"assignee,omitempty"`
	Service      string   `json:"service,omitempty"`
	KnownIssue   string   `json:"knownIssue,omitempty"`
	Exception    string   `json:"exception,omitempty"`
	RCA          string   `json:"rca,omitempty"`
	RCACause     string   `json:"rcaCause,omitempty"`
	DuplicateOf  int      `json:"duplicateOf,omitempty"`
	RegressionOf int      `json:"regressionOf,omitempty"`
	SameAs       int      `json:"sameAs,omitempty"`
	PossibleDup  int      `json:"possibleDuplicateOf,omitempty"`
	// Explanation is the close's, cut short; ClosedBy who closed it.
	Explanation   string     `json:"explanation,omitempty"`
	ClosedBy      string     `json:"closedBy,omitempty"`
	Created       time.Time  `json:"created"`
	Updated       time.Time  `json:"updated"`
	StatusAt      time.Time  `json:"statusAt"`
	ClosedAt      *time.Time `json:"closedAt,omitempty"`
	LastReporter  time.Time  `json:"lastReporter,omitzero"`
	Waiting       *time.Time `json:"waiting,omitempty"`
	Pruned        bool       `json:"pruned,omitempty"`
	Seen          *Seen      `json:"seen,omitempty"`
	VerifyPending bool       `json:"verifyPending,omitempty"`
	Verified      *time.Time `json:"verified,omitempty"`
	SLAFrom       *time.Time `json:"slaFrom,omitempty"`
	FirstResponse *time.Time `json:"firstResponse,omitempty"`
	Resolved      *time.Time `json:"resolved,omitempty"`
	FixProposed   bool       `json:"fixProposed,omitempty"`
	// Verification is how the run after a change applied saw it (CF-31).
	Verification string `json:"verification,omitempty"`
	// Type, Incident, who opened and last changed it, and when it was
	// queued for its analysis (CF-32 to CF-37).
	Type           string     `json:"type,omitempty"`
	Incident       string     `json:"incident,omitempty"`
	CreatedBy      *Actor     `json:"createdBy,omitempty"`
	LastBy         *Actor     `json:"lastBy,omitempty"`
	LastAt         *time.Time `json:"lastAt,omitempty"`
	AnalysisQueued *time.Time `json:"analysisQueued,omitempty"`
}

// lineOf is a case's line in the index.
func lineOf(r *Case) Line {
	l := Line{Number: r.Number, Title: r.Title, Status: r.Status, Outcome: r.Outcome, Priority: r.Priority, Source: r.Kind(), AgentFound: r.AgentFound(),
		Cluster: r.Cluster, Object: r.Object, Key: r.Key, Job: r.Job, Reporter: r.Reporter.ID, ReporterName: r.Reporter.Label(), Assignee: r.Owner,
		Service: r.Service, KnownIssue: r.KnownIssue, Exception: r.Exception, RCA: r.RCA, RCACause: r.RCACause, DuplicateOf: r.DuplicateOf, RegressionOf: r.RegressionOf,
		SameAs: r.SameAs, PossibleDup: r.PossibleDuplicateOf, Explanation: clip(r.Explanation, MaxShort), Created: r.Created, Updated: r.Updated,
		StatusAt: r.StatusAt, ClosedAt: r.Closed, LastReporter: r.LastReporter, Waiting: r.Waiting, Pruned: r.Pruned, Seen: r.Seen,
		VerifyPending: r.VerifyPending, Verified: r.Verified, SLAFrom: r.SLAFrom, FirstResponse: r.FirstResponse, Resolved: r.Resolved, FixProposed: r.FixProposed,
		Verification: r.Verification, Type: r.Type, Incident: r.Incident, CreatedBy: r.CreatedBy, LastBy: r.LastBy, LastAt: r.LastAt, AnalysisQueued: r.AnalysisQueued}
	if l.Title == "" {
		l.Title = titleOf(r.Text)
	}
	if r.FiledBy != nil {
		l.FiledBy = r.FiledBy.ID
	}
	if r.OpenedBy != nil {
		l.OpenedBy = r.OpenedBy.Label()
	}
	if r.ClosedBy != nil {
		l.ClosedBy = r.ClosedBy.Label()
	}
	for _, w := range r.Watchers {
		l.Watchers = append(l.Watchers, w.ID)
	}
	return l
}

// Open says whether the case still needs work.
func (l Line) Open() bool { return l.Status.Open() }

// watchedBy reports whether a person follows the case.
func (l Line) watchedBy(id string) bool {
	for _, w := range l.Watchers {
		if w == id {
			return true
		}
	}
	return false
}

// lite is a line as a report, for the lists: what a row shows.
func (l Line) lite() Case {
	r := Case{Number: l.Number, Title: l.Title, Source: l.Source, Cluster: l.Cluster, Object: l.Object, Key: l.Key, Job: l.Job,
		Reporter: Person{ID: l.Reporter, Name: l.ReporterName}, Created: l.Created, Updated: l.Updated, Status: l.Status, StatusAt: l.StatusAt,
		Outcome: l.Outcome, Explanation: l.Explanation, Priority: l.Priority, Owner: l.Assignee, Service: l.Service, KnownIssue: l.KnownIssue,
		Exception: l.Exception, RCA: l.RCA, RCACause: l.RCACause, DuplicateOf: l.DuplicateOf, RegressionOf: l.RegressionOf, SameAs: l.SameAs, PossibleDuplicateOf: l.PossibleDup,
		Closed: l.ClosedAt, LastReporter: l.LastReporter, Waiting: l.Waiting, Pruned: l.Pruned, Seen: l.Seen, VerifyPending: l.VerifyPending,
		Verified: l.Verified, SLAFrom: l.SLAFrom, FirstResponse: l.FirstResponse, Resolved: l.Resolved, FixProposed: l.FixProposed, Verification: l.Verification,
		Type: l.Type, Incident: l.Incident, CreatedBy: l.CreatedBy, LastBy: l.LastBy, LastAt: l.LastAt, AnalysisQueued: l.AnalysisQueued}
	if l.FiledBy != "" {
		r.FiledBy = &Person{ID: l.FiledBy}
	}
	if l.OpenedBy != "" {
		r.OpenedBy = &Person{ID: l.OpenedBy, Name: l.OpenedBy}
	}
	if l.ClosedBy != "" {
		r.ClosedBy = &Person{ID: l.ClosedBy, Name: l.ClosedBy}
	}
	for _, w := range l.Watchers {
		r.Watchers = append(r.Watchers, Person{ID: w})
	}
	return r
}

// indexDoc is what IndexKey holds.
type indexDoc struct {
	// Next is the number the next case takes: numbers only grow, so a
	// number is never given twice, even after a case is let go.
	Next  int    `json:"next"`
	Lines []Line `json:"cases"`
	// Archived counts the cases let go for room once their text was
	// gone, by outcome: the counts stay when the records go (IS-17).
	Archived map[string]int `json:"archived,omitempty"`
}

// knownDoc is what KnownKey holds.
type knownDoc struct {
	Next  int          `json:"next"`
	Known []KnownIssue `json:"known"`
}

// configDoc is what ConfigKey holds.
type configDoc struct {
	Settings *Settings    `json:"settings,omitempty"`
	Services []ServiceDef `json:"services,omitempty"`
	Tokens   []Token      `json:"tokens,omitempty"`
}

// loaded is a case object read in one change: its record, the bytes it
// was read as (to tell a change), its version, and what becomes of it.
type loaded struct {
	r       *Case
	was     []byte
	version string
	created bool
	dropped bool
	// by is who changed it in this change, when not the change's actor.
	by *Actor
}

// state is the three documents as read together, at one version of what
// holds them, and the case objects read in the change under way.
type state struct {
	index   indexDoc
	known   knownDoc
	config  configDoc
	version string
	at      time.Time

	ctx   context.Context
	svc   *Service
	cases map[int]*loaded
	// err is the first case object that could not be read.
	err error
	// actor is who makes the change: each case it changes names them as
	// its last changer (CF-35); nil is the system.
	actor *Actor
}

func (st *state) settings() Settings {
	if st.config.Settings == nil {
		return DefaultSettings()
	}
	return st.config.Settings.withDefaults()
}

// line is a case's index line, or nil.
func (st *state) line(n int) *Line {
	i := sort.Search(len(st.index.Lines), func(i int) bool { return st.index.Lines[i].Number >= n })
	if i < len(st.index.Lines) && st.index.Lines[i].Number == n {
		return &st.index.Lines[i]
	}
	return nil
}

// report is a case's whole record, read from its object the first time a
// change asks for it; nil when there is none. A change may change it:
// what changed is saved.
func (st *state) report(n int) *Case {
	if l, ok := st.cases[n]; ok {
		if l.dropped {
			return nil
		}
		return l.r
	}
	if st.line(n) == nil || st.svc == nil {
		return nil
	}
	r, b, version, err := st.svc.loadCase(st.ctx, n)
	if err != nil {
		if !errors.Is(err, ErrNotFound) && st.err == nil {
			st.err = err
		}
		return nil
	}
	if st.cases == nil {
		st.cases = map[int]*loaded{}
	}
	st.cases[n] = &loaded{r: r, was: b, version: version}
	return r
}

// dirty says a change made, changed or let go of a case object.
func (st *state) dirty() bool {
	for _, l := range st.cases {
		if l.created || l.dropped {
			return true
		}
		if b, err := json.Marshal(l.r); err == nil && string(b) != string(l.was) {
			return true
		}
	}
	return false
}

// actorFor is who changed a case in the change under way (CF-35).
func (st *state) actorFor(n int) Actor {
	if l, ok := st.cases[n]; ok && l.by != nil {
		return *l.by
	}
	if st.actor != nil {
		return *st.actor
	}
	return SystemActor
}

// touch names who changed a case, when fn changed it, so what a change
// returns says it before it is saved (CF-35).
func (st *state) touch(r *Case, now time.Time, fn func() error) error {
	was, _ := json.Marshal(r)
	if err := fn(); err != nil {
		return err
	}
	if b, _ := json.Marshal(r); string(b) != string(was) {
		stamp(r, st.actorFor(r.Number), now.UTC())
	}
	return nil
}

// add keeps a new case, numbered next.
func (st *state) add(r Case) *Case {
	r.Number = st.index.Next
	st.index.Next++
	if st.cases == nil {
		st.cases = map[int]*loaded{}
	}
	x := r
	st.cases[r.Number] = &loaded{r: &x, created: true}
	st.index.Lines = append(st.index.Lines, lineOf(&x))
	return &x
}

// drop lets a case go: its line now, its object once the index is saved.
func (st *state) drop(n int) {
	for i := range st.index.Lines {
		if st.index.Lines[i].Number == n {
			st.index.Lines = append(st.index.Lines[:i], st.index.Lines[i+1:]...)
			break
		}
	}
	if st.cases == nil {
		st.cases = map[int]*loaded{}
	}
	if l, ok := st.cases[n]; ok {
		l.dropped = true
	} else {
		st.cases[n] = &loaded{dropped: true}
	}
}

func (st *state) knownIssue(id string) *KnownIssue {
	for i := range st.known.Known {
		if st.known.Known[i].ID == id {
			return &st.known.Known[i]
		}
	}
	return nil
}

func (st *state) service(id string) *ServiceDef {
	for i := range st.config.Services {
		if st.config.Services[i].ID == id {
			return &st.config.Services[i]
		}
	}
	return nil
}

// clone copies the documents deeply enough for a change to be tried on
// them and thrown away: through JSON, as they are stored.
func (st *state) clone() *state {
	out := &state{version: st.version, at: st.at}
	copyJSON(&out.index, st.index)
	copyJSON(&out.known, st.known)
	copyJSON(&out.config, st.config)
	return out
}

func copyJSON(dst, src any) {
	b, _ := json.Marshal(src)
	_ = json.Unmarshal(b, dst)
}

// readFresh is how long a read may use what was read last: requests come
// in bursts from one page, and every save goes through this process.
const readFresh = 2 * time.Second

// caseName is a case object's name: its number.
func caseName(n int) string { return strconv.Itoa(n) }

// loadCase reads one case object's record.
func (s *Service) loadCase(ctx context.Context, n int) (*Case, []byte, string, error) {
	keys, version, err := s.cases.Load(ctx, caseName(n))
	if errors.Is(err, kv.ErrNotFound) {
		return nil, nil, "", fmt.Errorf("%w: no case C-%d", ErrNotFound, n)
	}
	if err != nil {
		return nil, nil, "", err
	}
	b, ok := keys[CaseKey]
	if !ok {
		return nil, nil, "", fmt.Errorf("%w: no case C-%d", ErrNotFound, n)
	}
	var r Case
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, nil, "", fmt.Errorf("the case C-%d cannot be read: %w", n, err)
	}
	return &r, b, version, nil
}

// load reads the three documents at one version: a key saved between two
// reads is read again, so a change never starts from a mix.
func (s *Service) load(ctx context.Context, fresh bool) (*state, error) {
	s.cacheMu.Lock()
	cached := s.cache
	s.cacheMu.Unlock()
	if cached != nil && !fresh && s.now().Sub(cached.at) < readFresh {
		return s.bind(ctx, cached.clone()), nil
	}
	for try := 0; ; try++ {
		st := &state{at: s.now()}
		versions := map[string]bool{}
		for _, k := range []struct {
			key string
			v   any
		}{{IndexKey, &st.index}, {KnownKey, &st.known}, {ConfigKey, &st.config}} {
			b, version, err := s.kv.LoadKey(ctx, k.key)
			if err != nil && !errors.Is(err, kv.ErrNotFound) {
				return nil, err
			}
			versions[version] = true
			st.version = version
			if err == nil {
				if err := json.Unmarshal(b, k.v); err != nil {
					return nil, fmt.Errorf("%s cannot be read: %w", k.key, err)
				}
			}
		}
		if len(versions) == 1 || try == 4 {
			if st.index.Next == 0 {
				st.index.Next = 1
			}
			if st.known.Next == 0 {
				st.known.Next = 1
			}
			sort.SliceStable(st.index.Lines, func(i, j int) bool { return st.index.Lines[i].Number < st.index.Lines[j].Number })
			s.remember(st)
			return s.bind(ctx, st.clone()), nil
		}
	}
}

// bind lets a state read case objects.
func (s *Service) bind(ctx context.Context, st *state) *state {
	st.ctx, st.svc = ctx, s
	return st
}

func (s *Service) remember(st *state) {
	s.cacheMu.Lock()
	s.cache = st.clone()
	s.cacheMu.Unlock()
}

// change is what one change to the records does besides saving: the
// events and audit records it raises, sent once the save holds.
type change struct {
	events []Event
	audits []AuditRecord
	// after runs once the save holds, outside the lock: investigations.
	after []func()
}

func (c *change) event(e Event) { c.events = append(c.events, e) }

func (c *change) audit(a AuditRecord) { c.audits = append(c.audits, a) }

// The parts of the state a change saves. saveReports saves the case
// objects changed and the index.
const (
	saveReports = 1 << iota
	saveKnown
	saveConfig
)

// update applies fn to the records and saves the parts it names, each
// with the version it read: when another writer came first, it reads
// again and applies fn once more, so neither change is lost. fn must
// change only the state it is given.
func (s *Service) update(ctx context.Context, fn func(st *state, c *change) (int, error)) error {
	s.writeMu.Lock()
	var c *change
	var err error
	for try := 0; ; try++ {
		var st *state
		if st, err = s.load(ctx, try > 0); err != nil {
			break
		}
		c = &change{}
		var parts int
		parts, err = fn(st, c)
		if err == nil && st.err != nil {
			err = st.err
		}
		if err != nil || parts == 0 && !st.dirty() {
			break
		}
		if err = s.save(ctx, st, parts); errors.Is(err, kv.ErrConflict) && try < 5 {
			continue
		}
		if err == nil {
			st.cases = nil
			s.remember(st)
		}
		break
	}
	s.writeMu.Unlock()
	if err != nil {
		return err
	}
	for _, a := range c.audits {
		s.record(a)
	}
	for _, e := range c.events {
		s.emit(e)
	}
	for _, f := range c.after {
		f()
	}
	return nil
}

// save writes what changed: the new case objects first (a number another
// writer took is a conflict, before anything is written), then the
// service's keys, then the case objects changed, and last it deletes the
// objects of the cases let go.
func (s *Service) save(ctx context.Context, st *state, parts int) error {
	type write struct {
		n int
		b []byte
		l *loaded
	}
	var creates, changes []write
	for n, l := range st.cases {
		if l.dropped {
			continue
		}
		b, err := json.Marshal(l.r)
		if err != nil {
			return err
		}
		if !l.created && string(b) == string(l.was) {
			continue
		}
		// Who changed it, and when (CF-35).
		stamp(l.r, st.actorFor(n), s.now().UTC())
		if b, err = json.Marshal(l.r); err != nil {
			return err
		}
		if len(b) > MaxCaseBytes {
			return fmt.Errorf("%w: the case C-%d would take %d KiB, over the %d KiB one case keeps: start a new case and mention C-%d", ErrFull, n, (len(b)+1023)>>10, MaxCaseBytes>>10, n)
		}
		if ln := st.line(n); ln != nil {
			*ln = lineOf(l.r)
		}
		parts |= saveReports
		if l.created {
			creates = append(creates, write{n, b, l})
		} else {
			changes = append(changes, write{n, b, l})
		}
	}
	sort.Slice(creates, func(i, j int) bool { return creates[i].n < creates[j].n })
	sort.Slice(changes, func(i, j int) bool { return changes[i].n < changes[j].n })
	if parts&saveReports != 0 {
		// The index fits before anything is written: a case it lets go is
		// dropped with the rest.
		if err := s.fitIndex(st); err != nil {
			return err
		}
	}
	for _, w := range creates {
		if st.cases[w.n].dropped {
			continue
		}
		v, err := s.cases.Save(ctx, caseName(w.n), CaseKey, w.b, "")
		if err != nil {
			return err
		}
		w.l.version, w.l.was, w.l.created = v, w.b, false
	}
	type part struct {
		bit int
		key string
		fit func() ([]byte, error)
	}
	for _, p := range []part{{saveReports, IndexKey, func() ([]byte, error) { return json.Marshal(&st.index) }},
		{saveKnown, KnownKey, func() ([]byte, error) { return s.fitKnown(st) }},
		{saveConfig, ConfigKey, func() ([]byte, error) { return s.fitConfig(&st.config) }}} {
		if parts&p.bit == 0 {
			continue
		}
		b, err := p.fit()
		if err != nil {
			return err
		}
		next, err := s.kv.SaveKey(ctx, p.key, b, st.version)
		if err != nil {
			return err
		}
		st.version = next
	}
	for _, w := range changes {
		v, err := s.cases.Save(ctx, caseName(w.n), CaseKey, w.b, w.l.version)
		if err != nil {
			return err
		}
		w.l.version, w.l.was = v, w.b
	}
	for n, l := range st.cases {
		if l.dropped && !l.created {
			if err := s.cases.Delete(ctx, caseName(n)); err != nil {
				s.log.Warn("a case let go could not be deleted", "case", n, "err", err)
			}
		}
	}
	return nil
}

// fitIndex keeps the index within its limit. The cases whose text the
// retention already took are let go first, oldest first, and counted by
// outcome; when that is not enough the save is refused, saying what to
// do (IS-17).
func (s *Service) fitIndex(st *state) error {
	d := &st.index
	b, err := json.Marshal(d)
	if err != nil || len(b) <= s.limits.IndexBytes {
		return err
	}
	var pruned []Line
	for _, l := range d.Lines {
		if l.Pruned {
			pruned = append(pruned, l)
		}
	}
	sort.Slice(pruned, func(a, b int) bool { return pruned[a].Created.Before(pruned[b].Created) })
	for len(pruned) > 0 && len(b) > s.limits.IndexBytes {
		// A tenth at a time: each try encodes everything again.
		n := max(len(pruned)/10, 1)
		for _, l := range pruned[:n] {
			if d.Archived == nil {
				d.Archived = map[string]int{}
			}
			d.Archived[string(l.Outcome)]++
			st.drop(l.Number)
		}
		pruned = pruned[n:]
		if b, err = json.Marshal(d); err != nil {
			return err
		}
	}
	if len(b) > s.limits.IndexBytes {
		return fmt.Errorf("%w: the index of the cases would take %d KiB, over its %d KiB: shorten the retention in the issue settings, so closed cases' text goes sooner, or close the cases that are done", ErrFull, (len(b)+1023)>>10, s.limits.IndexBytes>>10)
	}
	return nil
}

// fitKnown encodes the known issues within their limit, dropping resolved
// ones no case links to, oldest first.
func (s *Service) fitKnown(st *state) ([]byte, error) {
	d := &st.known
	b, err := json.Marshal(d)
	if err != nil || len(b) <= s.limits.KnownBytes {
		return b, err
	}
	linked := map[string]bool{}
	for _, l := range st.index.Lines {
		if l.KnownIssue != "" {
			linked[l.KnownIssue] = true
		}
	}
	for len(b) > s.limits.KnownBytes {
		oldest := -1
		for i, k := range d.Known {
			if k.Status == KnownResolved && !linked[k.ID] && (oldest < 0 || k.Updated.Before(d.Known[oldest].Updated)) {
				oldest = i
			}
		}
		if oldest < 0 {
			return nil, fmt.Errorf("%w: the known issues would take %d KiB, over their %d KiB: resolve or delete known issues no longer needed, or shorten their logs", ErrFull, (len(b)+1023)>>10, s.limits.KnownBytes>>10)
		}
		d.Known = append(d.Known[:oldest], d.Known[oldest+1:]...)
		if b, err = json.Marshal(d); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (s *Service) fitConfig(d *configDoc) ([]byte, error) {
	b, err := json.Marshal(d)
	if err != nil || len(b) <= s.limits.ConfigBytes {
		return b, err
	}
	return nil, fmt.Errorf("%w: the services and tokens would take %d KiB, over their %d KiB: delete services no longer used, or revoke tokens no longer used", ErrFull, (len(b)+1023)>>10, s.limits.ConfigBytes>>10)
}
