package issues

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// Caller is who a request is from and what they may do, as whoever mounts
// the service works it out (IS-20, IS-21). The service decides nothing
// about roles itself.
type Caller struct {
	Person
	// Report lets them file reports and follow their own.
	Report bool
	// Operator lets them see every report and triage it.
	Operator bool
	// Admin lets them change the settings and the API tokens.
	Admin bool
	// Scopes are a token's (IS-24); empty for a person.
	Scopes []string
	// From is the address the request came from, for the audit log.
	From string
}

// Errors the service answers with; each message says what to do.
var (
	ErrNotFound = errors.New("not found")
	ErrDenied   = errors.New("not allowed")
	// ErrRateLimited is a reporter past their reports for the hour (IS-25).
	ErrRateLimited = errors.New("too many reports")
)

// Event is one change to the records, for whatever subscribes (IS-18):
// notifications, cases, publishing. Report and Known are copies.
type Event struct {
	Kind   string      `json:"kind"`
	At     time.Time   `json:"at"`
	Report *Case       `json:"report,omitempty"`
	Known  *KnownIssue `json:"known,omitempty"`
	// By is who made the change; a system change has none.
	By *Person `json:"by,omitempty"`
	// Answered says the reporter's message answered a question a person
	// asked: the case no longer waits for them (CF-67).
	Answered bool `json:"answered,omitempty"`
}

// The kinds of Event.
const (
	EventReportCreated = "report.created"
	EventReportLinked  = "report.linked"
	EventReportStatus  = "report.status"
	EventReportMessage = "report.message"
	EventReportTriaged = "report.triaged"
	EventKnownChanged  = "known.changed"
	EventKnownUpdate   = "known.update"
)

// AuditRecord is one line for the audit log (IS-26).
type AuditRecord struct {
	Action  string
	Who     Person
	Target  string
	Detail  string
	Outcome string
	From    string
}

// The audit actions; the log's families are by the part before the dot.
const (
	ActionReport   = "issue.report"
	ActionStatus   = "issue.status"
	ActionMessage  = "issue.message"
	ActionTriage   = "issue.triage"
	ActionKnown    = "issue.known"
	ActionService  = "issue.service"
	ActionSettings = "issue.settings"
	ActionToken    = "issue.token"
	ActionImport   = "issue.import"
	// ActionClose is a case closed with its outcome, ActionReopen one
	// reopened with the reason (Phase 12, wave 6).
	ActionClose  = "issue.close"
	ActionReopen = "issue.reopen"
)

// Options configure the service.
type Options struct {
	// Backend keeps the records; nil keeps them in memory.
	Backend kv.Backend
	// Cases keeps each case in an object of its own (CF-20); nil keeps
	// them in memory.
	Cases  kv.Objects
	Limits Limits
	// Investigator looks into reports no known issue explains (IS-16); nil
	// is NoInvestigator.
	Investigator Investigator
	// Audit receives a record per report, status change, message sent and
	// change of a token or setting; nil records nothing.
	Audit func(AuditRecord)
	// HTTP sends the webhook; nil uses a client with a 10-second timeout.
	HTTP *http.Client
	// Clusters are the connected clusters, which a reporter may say a
	// report is about (CF-64); nil knows none.
	Clusters func() []Cluster
	Now      func() time.Time
	Logger   *slog.Logger
}

// Cluster is a connected cluster as the report page names it: its id and
// display name, nothing else (CF-64).
type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Service is the issue service.
type Service struct {
	kv     kv.Backend
	cases  kv.Objects
	limits Limits
	inv    Investigator
	audit  func(AuditRecord)
	http   *http.Client
	now    func() time.Time
	log    *slog.Logger
	// clusters are the connected clusters (CF-64).
	clusters func() []Cluster

	// writeMu serialises this process's changes, so they meet no conflict
	// among themselves; cache is the last state read or saved.
	writeMu sync.Mutex
	cacheMu sync.Mutex
	cache   *state

	listenMu  sync.Mutex
	listeners []func(Event)
	// investigating bounds investigations running at once.
	investigating chan struct{}
	// usedMu guards when each token was last written as used.
	usedMu sync.Mutex
	used   map[string]time.Time
}

// New builds the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Cases == nil {
		o.Cases = &kv.MemoryObjects{}
	}
	if o.Investigator == nil {
		o.Investigator = NoInvestigator{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	s := &Service{kv: o.Backend, cases: o.Cases, limits: o.Limits.withDefaults(), inv: o.Investigator, audit: o.Audit, http: o.HTTP, now: o.Now, log: o.Logger,
		investigating: make(chan struct{}, 4), used: map[string]time.Time{}, clusters: o.Clusters}
	s.OnChange(s.notify)
	return s
}

// Clusters are the connected clusters a report may be about, by id and
// display name (CF-64); never nil.
func (s *Service) Clusters() []Cluster {
	out := []Cluster{}
	if s.clusters == nil {
		return out
	}
	for _, c := range s.clusters() {
		if c.ID == "" {
			continue
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		out = append(out, Cluster{ID: c.ID, Name: c.Name})
	}
	return out
}

// OnChange subscribes to every change (IS-18). fn runs in the goroutine
// that made the change, after it is saved; it must not block.
func (s *Service) OnChange(fn func(Event)) {
	s.listenMu.Lock()
	s.listeners = append(s.listeners, fn)
	s.listenMu.Unlock()
}

func (s *Service) emit(e Event) {
	s.listenMu.Lock()
	ls := append([]func(Event){}, s.listeners...)
	s.listenMu.Unlock()
	for _, fn := range ls {
		fn(e)
	}
}

func (s *Service) record(a AuditRecord) {
	if s.audit == nil {
		return
	}
	if a.Outcome == "" {
		a.Outcome = "ok"
	}
	s.audit(a)
}

func copyReport(r *Case) *Case {
	var out Case
	copyJSON(&out, r)
	return &out
}

func copyKnown(k *KnownIssue) *KnownIssue {
	var out KnownIssue
	copyJSON(&out, k)
	return &out
}

func ptr[T any](v T) *T { return &v }

// cleanText checks and scrubs a text a person wrote: never empty, never
// longer than MaxText, with anything that looks like a credential taken
// out (IS-01). It says whether anything was.
func cleanText(text string) (string, bool, error) {
	text = strings.TrimSpace(strings.ToValidUTF8(text, ""))
	if text == "" {
		return "", false, invalid("say what is wrong")
	}
	if utf8.RuneCountInString(text) > MaxText {
		return "", false, invalid("keep it under %d characters; attach the rest to a message", MaxText)
	}
	clean := scrub.Text(text)
	return clean, clean != text, nil
}

// Submission is a new report.
type Submission struct {
	Text string `json:"text"`
	// For is the person the report is for, when support files it for
	// them (IS-03): operators, and tokens with ScopeOnBehalf, only.
	For *Person `json:"for,omitempty"`
	// Incident is another system's number for it, and Type what kind of
	// problem it is (CF-36, CF-37): support and the API's tokens only.
	Incident string `json:"incident,omitempty"`
	Type     string `json:"type,omitempty"`
	// Priority is the reporter's, 1 to 4 (P0, an outage, is the
	// operators' call); none keeps DefaultPriority. Cluster is the id of a
	// connected cluster it is about; "" when they are not sure (CF-64).
	Priority *int   `json:"priority,omitempty"`
	Cluster  string `json:"cluster,omitempty"`
}

// checkChoice checks the priority and cluster a reporter chose (CF-64).
func (s *Service) checkChoice(in Submission) (int, string, error) {
	p := DefaultPriority
	if in.Priority != nil {
		if *in.Priority < 1 || *in.Priority > 4 {
			return 0, "", invalid("a report's priority is 1 to 4: P1 blocking, P2 service degradation, P3 preemptive, P4 question")
		}
		p = *in.Priority
	}
	cluster := strings.TrimSpace(in.Cluster)
	if cluster == "" {
		return p, "", nil
	}
	for _, c := range s.Clusters() {
		if c.ID == cluster {
			return p, cluster, nil
		}
	}
	return 0, "", invalid("there is no connected cluster %q", cluster)
}

// Receipt is what the reporter is told once a report is in.
type Receipt struct {
	Report Case `json:"report"`
	// Known is the known issue it was linked to, with its status,
	// workaround and expected fix as they stand (IS-10).
	Known *KnownIssue `json:"known,omitempty"`
	// Removed says something that looked like a password or a token was
	// taken out before the report was kept (IS-01).
	Removed bool `json:"removed"`
}

// DefaultPriority is a case's priority when nobody says: P2, a service
// degraded.
const DefaultPriority = 2

// Submit takes a report in (IS-01 to IS-03, IS-10, IS-12, IS-25):
// scrubbed, numbered, its service guessed, matched against the open known
// issues, and closed as the known issue when the match is confident;
// otherwise left for investigation.
func (s *Service) Submit(ctx context.Context, c Caller, in Submission) (Receipt, error) {
	if !c.Report && !c.Operator {
		return Receipt{}, fmt.Errorf("%w: you may not report problems here", ErrDenied)
	}
	text, removed, err := cleanText(in.Text)
	if err != nil {
		return Receipt{}, err
	}
	typ, incident := "", ""
	if in.Incident != "" || in.Type != "" {
		if !c.Operator && !c.System {
			return Receipt{}, fmt.Errorf("%w: only support and the API's tokens set a report's incident number and type", ErrDenied)
		}
		if typ, err = checkType(in.Type); err != nil {
			return Receipt{}, err
		}
		if incident, err = CleanIncident(in.Incident); err != nil {
			return Receipt{}, err
		}
	}
	priority, cluster, err := s.checkChoice(in)
	if err != nil {
		return Receipt{}, err
	}
	priorityBy := "default"
	if in.Priority != nil {
		priorityBy = "reporter"
	}
	reporter, filedBy := c.Person, (*Person)(nil)
	if in.For != nil && strings.TrimSpace(in.For.ID) != "" && in.For.ID != c.ID {
		if !c.Operator && !(c.System && contains(c.Scopes, ScopeOnBehalf)) {
			return Receipt{}, fmt.Errorf("%w: only support may report for someone else", ErrDenied)
		}
		reporter = Person{ID: strings.TrimSpace(in.For.ID), Name: strings.TrimSpace(in.For.Name), Email: strings.TrimSpace(in.For.Email)}
		filedBy = &Person{ID: c.ID, Name: c.Name, Email: c.Email, System: c.System}
	}
	var out Receipt
	err = s.update(ctx, func(st *state, ch *change) (int, error) {
		set := st.settings()
		now := s.now()
		if !c.Operator {
			sent := 0
			for _, l := range st.index.Lines {
				by := l.Reporter
				if l.FiledBy != "" {
					by = l.FiledBy
				}
				if by == c.ID && now.Sub(l.Created) < time.Hour {
					sent++
				}
			}
			if sent >= set.RatePerHour {
				return 0, fmt.Errorf("%w: you have sent %d reports in the last hour, the most allowed; add to one you sent, or try again later", ErrRateLimited, sent)
			}
		}
		st.actor = ptr(ActorOf(c.Person))
		r := Case{Text: text, Scrubbed: removed, Reporter: reporter, FiledBy: filedBy, Created: now, Updated: now,
			Status: StatusNew, StatusAt: now, Priority: priority, PriorityBy: priorityBy, LastReporter: now, Source: CaseReport, SLAFrom: ptr(now),
			Type: typ, Incident: incident, Cluster: cluster, CreatedBy: ptr(ActorOf(reporter))}
		if svc := st.matchService(text); len(svc) > 0 && svc[0].Confidence >= serviceThreshold {
			r.Service, r.ServiceConfidence, r.ServiceBy = svc[0].ID, svc[0].Confidence, "match"
		}
		var known *KnownIssue
		if cands := st.matchKnown(text, nil); len(cands) > 0 && cands[0].Confidence >= set.MatchThreshold {
			known = st.knownIssue(cands[0].ID)
			linkKnown(st, &r, known, cands[0].Confidence, systemPerson, now)
		}
		r.Similar = st.similarTo(&r, set, now)
		pr := st.add(r)
		stamp(pr, *st.actor, now)
		out = Receipt{Report: *copyReport(pr), Removed: removed}
		if known != nil {
			out.Known = copyKnown(known)
		}
		ch.event(Event{Kind: EventReportCreated, At: now, Report: copyReport(pr), By: &c.Person})
		detail := fmt.Sprintf("report #%d received from %s", pr.Number, reporter.Label())
		if filedBy != nil {
			detail += ", filed by " + filedBy.Label()
		}
		if in.Priority != nil {
			detail += fmt.Sprintf(", P%d as the reporter chose", priority)
		}
		if cluster != "" {
			detail += ", on " + cluster
		}
		if known != nil {
			detail += fmt.Sprintf(", closed as the known issue %s at %d%% confidence", known.ID, pr.MatchConfidence)
		}
		if removed {
			detail += "; something that looked like a credential was removed"
		}
		ch.audit(AuditRecord{Action: ActionReport, Who: c.Person, Target: fmt.Sprintf("report #%d", pr.Number), Detail: detail, From: c.From})
		if known == nil {
			n := pr.Number
			ch.after = append(ch.after, func() { s.investigate(n) })
		}
		return saveReports, nil
	})
	return out, err
}

// systemPerson is the issue service when it does something itself.
var systemPerson = Person{ID: "system", Name: "Issue service", System: true}

// linkKnown closes a case as a known issue (CF-06): a known issue follows
// the problem, its workaround and expected fix; it takes the known
// issue's service when it has none.
func linkKnown(st *state, r *Case, k *KnownIssue, confidence int, by Person, now time.Time) {
	r.KnownIssue, r.MatchConfidence = k.ID, confidence
	if r.Acknowledged == nil {
		r.Acknowledged = ptr(now)
	}
	if r.Service == "" && len(k.Services) > 0 && st.service(k.Services[0]) != nil {
		r.Service, r.ServiceConfidence, r.ServiceBy = k.Services[0], confidence, "known issue"
	}
	why := fmt.Sprintf("%s: %s", k.ID, k.Title)
	if confidence < 100 {
		why = fmt.Sprintf("Matched %s at %d%% confidence: %s", k.ID, confidence, k.Title)
	}
	CloseCase(r, by, OutcomeKnownIssue, why, now)
}

// similarTo lists the open reports on the same service, sent within the
// duplicate window, whose titles read like r's (IS-12).
func (st *state) similarTo(r *Case, set Settings, now time.Time) []int {
	if r.Service == "" {
		return nil
	}
	var others []Line
	var docs [][]string
	for _, o := range st.index.Lines {
		if o.Number == r.Number || !o.Open() || o.Service != r.Service || now.Sub(o.Created) > time.Duration(set.DuplicateHours)*time.Hour {
			continue
		}
		others = append(others, o)
		docs = append(docs, words(o.Title))
	}
	if len(others) == 0 {
		return nil
	}
	mine := words(titleOf(r.Text) + " " + r.Title)
	c := newCorpus(append(docs, mine))
	var out []int
	for i, o := range others {
		if c.likeness(mine, docs[i]) >= duplicateLikeness {
			out = append(out, o.Number)
		}
	}
	return out
}

// visible reports whether a caller may see a case (IS-21): operators
// every one; others their own, the ones they filed or follow, and every
// report on a service they are a contact of.
func (st *state) visible(c Caller, l *Line) bool {
	if c.Operator {
		return true
	}
	if c.ID == "" {
		return false
	}
	if l.Reporter == c.ID || l.FiledBy == c.ID || l.watchedBy(c.ID) {
		return true
	}
	if !c.System && l.Service != "" {
		if svc := st.service(l.Service); svc != nil && svc.hasContact(c.ID) {
			return true
		}
	}
	return false
}

// Filter narrows a list of cases.
type Filter struct {
	// Source keeps one kind of case; Agents those agents found and People
	// the rest (CF-17); Cluster those about a cluster; Pending those
	// waiting for the people who work the cases; Unassigned those nobody
	// owns.
	Source     string
	Agents     bool
	People     bool
	Cluster    string
	Pending    bool
	Unassigned bool
	// Mine keeps the caller's own, filed and followed reports even for
	// an operator.
	Mine     bool
	Status   Status
	Outcome  Outcome
	Open     bool
	Service  string
	Owner    string
	Priority *int
	Known    string
	// Type keeps one type of case (CF-37).
	Type string
	// Query keeps cases whose title, number, object or incident number
	// holds every word.
	Query string
}

// match says whether a line passes the filter.
func (f Filter) match(c Caller, l *Line, q []string) bool {
	if f.Mine && l.Reporter != c.ID && l.FiledBy != c.ID && !l.watchedBy(c.ID) {
		return false
	}
	if (f.Status != "" && l.Status != f.Status) || (f.Outcome != "" && l.Outcome.Canonical() != f.Outcome.Canonical()) || (f.Open && !l.Open()) || (f.Service != "" && l.Service != f.Service) ||
		(f.Priority != nil && l.Priority != *f.Priority) || (f.Known != "" && l.KnownIssue != f.Known) {
		return false
	}
	if f.Owner != "" && (l.Assignee == nil || l.Assignee.ID != f.Owner) {
		return false
	}
	if (f.Source != "" && l.Source != f.Source) || (f.Agents && !l.AgentFound) || (f.People && l.AgentFound) || (f.Cluster != "" && l.Cluster != f.Cluster) ||
		(f.Pending && !l.Open()) || (f.Unassigned && l.Assignee != nil) || (f.Type != "" && l.CaseType() != f.Type) {
		return false
	}
	if len(q) > 0 {
		hay := strings.ToLower(fmt.Sprintf("#%d C-%d %s %s %s %s", l.Number, l.Number, l.Title, l.ReporterName, l.Object, l.Cluster))
		if l.Key != nil {
			hay += " " + strings.ToLower(l.Key.Check)
		}
		if l.Incident != "" {
			hay += " " + strings.ToLower(l.Incident)
		}
		for _, w := range q {
			if !strings.Contains(hay, w) {
				return false
			}
		}
	}
	return true
}

// Lines are the index's lines a caller may see that pass a filter, newest
// first.
func (s *Service) Lines(ctx context.Context, c Caller, f Filter) ([]Line, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []Line
	q := strings.Fields(strings.ToLower(f.Query))
	for i := len(st.index.Lines) - 1; i >= 0; i-- {
		l := &st.index.Lines[i]
		if st.visible(c, l) && f.match(c, l, q) {
			out = append(out, *l)
		}
	}
	return out, nil
}

// Reports lists the cases a caller may see, newest first, as their rows
// show them: from the index, without their conversations.
func (s *Service) Reports(ctx context.Context, c Caller, f Filter) ([]Case, error) {
	lines, err := s.Lines(ctx, c, f)
	if err != nil {
		return nil, err
	}
	out := make([]Case, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.lite())
	}
	return out, nil
}

// Report is one report, whole, for a caller who may see it.
func (s *Service) Report(ctx context.Context, c Caller, n int) (Case, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return Case{}, err
	}
	l := st.line(n)
	if l == nil || !st.visible(c, l) {
		return Case{}, fmt.Errorf("%w: no report #%d", ErrNotFound, n)
	}
	r := st.report(n)
	if r == nil {
		if st.err != nil {
			return Case{}, st.err
		}
		return Case{}, fmt.Errorf("%w: no report #%d", ErrNotFound, n)
	}
	return *r, nil
}

// onReport changes one report the caller may see, saving it.
func (s *Service) onReport(ctx context.Context, c Caller, n int, fn func(st *state, r *Case, ch *change, now time.Time) error) (Case, error) {
	var out Case
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		st.actor = ptr(ActorOf(c.Person))
		l := st.line(n)
		if l == nil || !st.visible(c, l) {
			return 0, fmt.Errorf("%w: no report #%d", ErrNotFound, n)
		}
		r := st.report(n)
		if r == nil {
			if st.err != nil {
				return 0, st.err
			}
			return 0, fmt.Errorf("%w: no report #%d", ErrNotFound, n)
		}
		now := s.now()
		if err := st.touch(r, now, func() error { return fn(st, r, ch, now) }); err != nil {
			return 0, err
		}
		r.Updated = now
		out = *copyReport(r)
		return saveReports, nil
	})
	return out, err
}

// isReporter says whether the caller is the report's reporter, or filed it.
func isReporter(c Caller, r *Case) bool {
	return r.Reporter.ID == c.ID || (r.FiledBy != nil && r.FiledBy.ID == c.ID)
}

func addMessage(r *Case, m Message) error {
	if len(r.Messages) >= MaxMessages {
		return invalid("report #%d has %d messages, the most it keeps; send a new report and mention #%d", r.Number, MaxMessages, r.Number)
	}
	m.ID = r.nextMessageID()
	r.Messages = append(r.Messages, m)
	return nil
}

// AddMessage adds to a report's conversation (IS-14). The reporter's
// message answers a question an operator asked; an operator's is sent to
// the reporter at once, or kept as an internal note.
func (s *Service) AddMessage(ctx context.Context, c Caller, n int, text string, internal bool) (Case, error) {
	clean, removed, err := cleanText(text)
	if err != nil {
		return Case{}, err
	}
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		m := Message{At: now, From: c.Person, Text: clean}
		switch {
		// An operator writes as one, also on a report they filed for
		// someone else (On behalf of, CS-15); only on their own problem
		// do they write as its reporter.
		case c.Operator && r.Reporter.ID != c.ID:
			m.Kind, m.Internal = FromOperator, internal
		case internal:
			return fmt.Errorf("%w: only operators write internal notes", ErrDenied)
		default:
			if !r.Status.Open() {
				return invalid("report #%d is closed; reopen it first, or send a new report", n)
			}
			m.Kind = FromReporter
		}
		if err := addMessage(r, m); err != nil {
			return err
		}
		answered := false
		if m.Kind == FromReporter {
			// The reporter's answer ends the wait: the case is in progress
			// again, as a person's question put it (CF-67).
			answered = r.Waiting != nil
			r.LastReporter, r.Waiting = now, nil
			if answered && r.Status == StatusNew {
				Take(r, now)
				statusChanged(ch, c.Person, c.From, r, now)
			}
		} else {
			markHandled(r, now, !internal)
			if !internal {
				replied(ch, c, r, now)
			}
		}
		ch.event(Event{Kind: EventReportMessage, At: now, Report: copyReport(r), By: &c.Person, Answered: answered})
		what := "a message"
		if internal {
			what = "an internal note"
		}
		detail := fmt.Sprintf("%s on report #%d from %s", what, n, c.Label())
		if removed {
			detail += "; something that looked like a credential was removed"
		}
		ch.audit(AuditRecord{Action: ActionMessage, Who: c.Person, Target: fmt.Sprintf("report #%d", n), Detail: detail, From: c.From})
		return nil
	})
}

// replied moves a New case in progress when a person replies to its
// reporter, or sends a reply an agent drafted (CF-45, CF-67): a person
// works on it. An internal note, and anything an agent does alone, leave
// its status as it is.
func replied(ch *change, c Caller, r *Case, now time.Time) {
	if c.System || r.Status != StatusNew {
		return
	}
	Take(r, now)
	statusChanged(ch, c.Person, c.From, r, now)
}

// markHandled notes a person's handling of a case for the service-level
// times (IS-06): acknowledged by any, and first responded to by a reply
// the reporter sees, or, on a case with no reporter, by anything a
// person does to it.
func markHandled(r *Case, now time.Time, reply bool) {
	if r.Acknowledged == nil {
		r.Acknowledged = ptr(now)
	}
	if (reply || r.Kind() != CaseReport) && r.FirstResponse == nil {
		r.FirstResponse = ptr(now)
	}
}

// Draft keeps a reply that waits for an operator's Send (IS-14): an
// investigator's, by "agent", or an operator's own.
func (s *Service) Draft(ctx context.Context, c Caller, n int, text, by string) (Case, error) {
	if !c.Operator {
		return Case{}, fmt.Errorf("%w: only operators draft replies", ErrDenied)
	}
	clean, _, err := cleanText(text)
	if err != nil {
		return Case{}, err
	}
	if by == "" {
		by = c.Label()
	}
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		return addMessage(r, Message{At: now, Kind: FromOperator, From: c.Person, Text: clean, Draft: true, DraftBy: by})
	})
}

// Send sends a reply (IS-14): a draft, as it is or with text an operator
// edited, or text they typed (draft 0). Nothing else ever sends a draft.
func (s *Service) Send(ctx context.Context, c Caller, n, draft int, text string) (Case, error) {
	if !c.Operator {
		return Case{}, fmt.Errorf("%w: only operators send replies", ErrDenied)
	}
	if draft == 0 {
		return s.AddMessage(ctx, c, n, text, false)
	}
	var clean string
	if strings.TrimSpace(text) != "" {
		var err error
		if clean, _, err = cleanText(text); err != nil {
			return Case{}, err
		}
	}
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		for i := range r.Messages {
			m := &r.Messages[i]
			if m.ID != draft || !m.Draft {
				continue
			}
			if clean != "" {
				m.Text = clean
			}
			m.Draft, m.At = false, now
			if m.DraftBy == "agent" || m.From.ID != c.ID {
				m.SentBy = &Person{ID: c.ID, Name: c.Name, Email: c.Email}
			}
			markHandled(r, now, true)
			replied(ch, c, r, now)
			ch.event(Event{Kind: EventReportMessage, At: now, Report: copyReport(r), By: &c.Person})
			ch.audit(AuditRecord{Action: ActionMessage, Who: c.Person, Target: fmt.Sprintf("report #%d", n), Detail: fmt.Sprintf("a reply drafted by %s sent on report #%d", m.DraftBy, n), From: c.From})
			return nil
		}
		return fmt.Errorf("%w: report #%d has no draft %d", ErrNotFound, n, draft)
	})
}

// Discard drops a draft.
func (s *Service) Discard(ctx context.Context, c Caller, n, draft int) (Case, error) {
	if !c.Operator {
		return Case{}, fmt.Errorf("%w: only operators discard drafts", ErrDenied)
	}
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		for i, m := range r.Messages {
			if m.ID == draft && m.Draft {
				r.Messages = append(r.Messages[:i], r.Messages[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("%w: report #%d has no draft %d", ErrNotFound, n, draft)
	})
}

// follow makes a person follow a case, or stop.
func follow(r *Case, p Person, on bool) error {
	kept := r.Watchers[:0:0]
	for _, w := range r.Watchers {
		if w.ID != p.ID {
			kept = append(kept, w)
		}
	}
	if on {
		if len(kept) >= MaxWatchers {
			return invalid("C-%d has %d people following it, the most it keeps", r.Number, MaxWatchers)
		}
		kept = append(kept, Person{ID: p.ID, Name: p.Name, Email: p.Email})
	}
	r.Watchers = kept
	return nil
}

// Watch makes the caller follow a report they may see, or stop.
func (s *Service) Watch(ctx context.Context, c Caller, n int, on bool) (Case, error) {
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		return follow(r, c.Person, on)
	})
}

// Unlink is the reporter's "No, mine is something else" (IS-11): the
// case leaves the known issue, which will not be matched to it again,
// and opens again for investigation.
func (s *Service) Unlink(ctx context.Context, c Caller, n int) (Case, error) {
	r, err := s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		if !isReporter(c, r) && !c.Operator {
			return fmt.Errorf("%w: only the reporter says it is something else", ErrDenied)
		}
		if r.KnownIssue == "" {
			return invalid("report #%d is not linked to a known issue", n)
		}
		was := r.KnownIssue
		r.Rejected = append(r.Rejected, was)
		if r.ServiceBy == "known issue" {
			r.Service, r.ServiceConfidence, r.ServiceBy = "", 0, ""
		}
		_ = addMessage(r, Message{At: now, Kind: FromSystem, Text: fmt.Sprintf("%s said this is not %s; it goes to investigation.", c.Label(), was)})
		r.KnownIssue, r.MatchConfidence = "", 0
		if !r.Status.Open() {
			ReopenCase(r, now)
		}
		ch.event(Event{Kind: EventReportLinked, At: now, Report: copyReport(r), By: &c.Person})
		ch.audit(AuditRecord{Action: ActionTriage, Who: c.Person, Target: fmt.Sprintf("report #%d", n), Detail: fmt.Sprintf("report #%d unlinked from %s by %s", n, was, c.Label()), From: c.From})
		ch.after = append(ch.after, func() { s.investigate(n) })
		return nil
	})
	return r, err
}

// Confirm is the reporter's answer to a fix (IS-15): "It works" is noted,
// "Still broken" reopens the case.
func (s *Service) Confirm(ctx context.Context, c Caller, n int, works bool) (Case, error) {
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		if !isReporter(c, r) && !c.Operator {
			return fmt.Errorf("%w: only the reporter confirms a fix", ErrDenied)
		}
		if !works {
			return s.reopen(st, r, ch, c, "", now)
		}
		if r.Status != StatusClosed || r.Outcome != OutcomeFix {
			return invalid("report #%d is not closed as fixed", n)
		}
		r.LastReporter = now
		_ = addMessage(r, Message{At: now, Kind: FromSystem, Text: c.Label() + " confirmed it works."})
		return nil
	})
}

// Reopen is the reporter's "Still broken" on a recently closed report
// (IS-15), or an operator's reopening with a reason.
func (s *Service) Reopen(ctx context.Context, c Caller, n int, reason string) (Case, error) {
	return s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		if !isReporter(c, r) && !c.Operator {
			return fmt.Errorf("%w: only the reporter reopens a report", ErrDenied)
		}
		return s.reopen(st, r, ch, c, reason, now)
	})
}

func (s *Service) reopen(st *state, r *Case, ch *change, c Caller, reason string, now time.Time) error {
	set := st.settings()
	if r.Status.Open() {
		return invalid("C-%d is still open", r.Number)
	}
	if r.Pruned || (r.Closed != nil && now.Sub(*r.Closed) > time.Duration(set.ReopenDays)*24*time.Hour && !c.Operator) {
		return invalid("report #%d was closed more than %d days ago; send a new report and mention #%d", r.Number, set.ReopenDays, r.Number)
	}
	r.LastReporter = now
	ReopenCase(r, now)
	text := c.Label() + " says it is still broken. Reopened."
	if reason = clip(reason, MaxShort); reason != "" {
		text = c.Label() + " reopened it: " + reason
	}
	_ = addMessage(r, Message{At: now, Kind: FromSystem, Text: text})
	reopened(ch, c.Person, c.From, r, reason, now)
	return nil
}

// CloseCase closes a case with its outcome and explanation, with the
// times that go with it. The caller checked the outcome is allowed.
func CloseCase(r *Case, by Person, outcome Outcome, explanation string, now time.Time) {
	outcome = outcome.Canonical()
	r.Status, r.StatusAt, r.Outcome, r.Explanation, r.Closed, r.Waiting = StatusClosed, now, outcome, explanation, ptr(now), nil
	r.ClosedBy = &Person{ID: by.ID, Name: by.Name, System: by.System}
	if r.Resolved == nil {
		r.Resolved = ptr(now)
	}
	r.VerifyPending, r.Verified = outcome == OutcomeFix && r.AgentFound() && r.Job != "", nil
	r.Verification, r.AnalysisQueued, r.VerifiedBy, r.Unchecked = "", nil, nil, 0
	if outcome != OutcomeDuplicate {
		r.DuplicateOf = 0
	}
	if outcome != OutcomeKnownIssue {
		r.KnownIssue, r.MatchConfidence = "", 0
	}
	if outcome != OutcomeException {
		r.Exception = ""
	}
	if outcome != OutcomeFix {
		r.RCACause = ""
	}
	r.Updated = now
}

// ReopenCase opens a closed case again: In progress when someone owns
// it, New otherwise; the outcome goes.
func ReopenCase(r *Case, now time.Time) {
	r.Status = StatusNew
	if r.Owner != nil {
		r.Status = StatusInProgress
	}
	r.StatusAt, r.Reopened = now, r.Reopened+1
	r.Outcome, r.Explanation, r.Resolved, r.Closed, r.ClosedBy = "", "", nil, nil, nil
	r.DuplicateOf, r.Exception, r.KnownIssue, r.MatchConfidence, r.RCACause = 0, "", "", 0, ""
	r.VerifyPending, r.Verified, r.Verification, r.AnalysisQueued = false, nil, "", nil
	r.VerifiedBy, r.Unchecked = nil, 0
	r.Updated = now
}

// Take moves a new case in progress: someone works on it.
func Take(r *Case, now time.Time) {
	if r.Status == StatusNew {
		r.Status, r.StatusAt = StatusInProgress, now
	}
}

func statusChanged(ch *change, by Person, from string, r *Case, now time.Time) {
	ch.event(Event{Kind: EventReportStatus, At: now, Report: copyReport(r), By: &by})
	detail := fmt.Sprintf("C-%d is %s", r.Number, r.Status)
	if r.Outcome != "" {
		detail += ", " + string(r.Outcome)
	}
	action := ActionStatus
	if r.Status == StatusClosed {
		action = ActionClose
	}
	ch.audit(AuditRecord{Action: action, Who: by, Target: fmt.Sprintf("case C-%d", r.Number), Detail: detail, From: from})
}

// reopened records a case reopened.
func reopened(ch *change, by Person, from string, r *Case, why string, now time.Time) {
	ch.event(Event{Kind: EventReportStatus, At: now, Report: copyReport(r), By: &by})
	detail := fmt.Sprintf("C-%d reopened, now %s", r.Number, r.Status)
	if why != "" {
		detail += ": " + clip(why, MaxShort)
	}
	ch.audit(AuditRecord{Action: ActionReopen, Who: by, Target: fmt.Sprintf("case C-%d", r.Number), Detail: detail, From: from})
}

// Triage is an operator's change to a case (IS-04 to IS-07, IS-12).
// Nil fields stay as they are.
type Triage struct {
	Priority *int    `json:"priority,omitempty"`
	Owner    *Person `json:"owner,omitempty"`
	// Service "" makes it unknown.
	Service *string `json:"service,omitempty"`
	// KnownIssue closes the case as that known issue; "" unlinks it and
	// opens it again.
	KnownIssue  *string `json:"knownIssue,omitempty"`
	DuplicateOf *int    `json:"duplicateOf,omitempty"`
	Status      *Status `json:"status,omitempty"`
	Outcome     Outcome `json:"outcome,omitempty"`
	// Explanation is the close's, or the reason a case is reopened.
	Explanation string `json:"explanation,omitempty"`
	// AskInfo, with a reply sent alongside, starts the timer that closes
	// a silent case; false stops it.
	AskInfo *bool `json:"askInfo,omitempty"`
	// Title, Cluster and Object change what the case says it is about;
	// Text changes the words of a case opened in the console (a report's
	// stay the reporter's).
	Title   *string `json:"title,omitempty"`
	Text    *string `json:"text,omitempty"`
	Cluster *string `json:"cluster,omitempty"`
	Object  *string `json:"object,omitempty"`
	// Type and Incident change its type and incident number; "" clears
	// the incident (CF-36, CF-37).
	Type     *string `json:"type,omitempty"`
	Incident *string `json:"incident,omitempty"`
	// Expect is what an edit in place started from (CF-60): the console's
	// only, never the API's.
	Expect *Expect `json:"-"`
}

// SetPriority sets a case's priority and its SLA clock (CF-02): a case a
// person opened has one at every priority; an agent-found one only at
// P0-P1, from the moment it is raised there, and lowering it stops it.
func SetPriority(r *Case, p int, by string, now time.Time) {
	r.Priority, r.PriorityBy = p, by
	switch {
	case !r.AgentFound():
	case p <= 1 && r.SLAFrom == nil:
		r.SLAFrom = ptr(now)
	case p > 1:
		r.SLAFrom = nil
	}
}

// Triage applies an operator's change.
func (s *Service) Triage(ctx context.Context, c Caller, n int, t Triage) (Case, error) {
	if !c.Operator {
		return Case{}, fmt.Errorf("%w: only operators triage reports", ErrDenied)
	}
	explanation := strings.TrimSpace(t.Explanation)
	if utf8.RuneCountInString(explanation) > MaxText {
		return Case{}, invalid("keep the explanation under %d characters", MaxText)
	}
	explanation = scrub.Text(explanation)
	r, err := s.onReport(ctx, c, n, func(st *state, r *Case, ch *change, now time.Time) error {
		if err := t.Expect.check(r); err != nil {
			return err
		}
		var did []string
		if err := t.about(r, &did); err != nil {
			return err
		}
		if t.Type != nil {
			v, err := checkType(*t.Type)
			if err != nil {
				return err
			}
			if v != r.CaseType() {
				r.Type = v
				did = append(did, "type "+v)
			}
		}
		if t.Incident != nil {
			v, err := CleanIncident(*t.Incident)
			if err != nil {
				return err
			}
			if v != r.Incident {
				r.Incident = v
				did = append(did, "incident "+orNone(v))
			}
		}
		if t.Priority != nil {
			if *t.Priority < MinPriority || *t.Priority > MaxPriority {
				return invalid("a priority is %d to %d", MinPriority, MaxPriority)
			}
			if r.AgentFound() && *t.Priority == 0 && r.Priority != 0 && c.System {
				return invalid("agents never set P0")
			}
			SetPriority(r, *t.Priority, c.Label(), now)
			did = append(did, fmt.Sprintf("priority P%d", r.Priority))
		}
		if t.Owner != nil {
			if t.Owner.ID == "" {
				r.Owner = nil
				did = append(did, "no owner")
			} else {
				r.Owner = &Person{ID: t.Owner.ID, Name: t.Owner.Name, Email: t.Owner.Email}
				did = append(did, "owner "+r.Owner.Label())
				if r.Status == StatusNew {
					Take(r, now)
					statusChanged(ch, c.Person, c.From, r, now)
				}
			}
		}
		if t.Service != nil {
			if *t.Service != "" && st.service(*t.Service) == nil {
				return fmt.Errorf("%w: no service %q", ErrNotFound, *t.Service)
			}
			r.Service, r.ServiceConfidence, r.ServiceBy = *t.Service, 100, c.Label()
			if *t.Service == "" {
				r.ServiceConfidence, r.ServiceBy = 0, ""
				did = append(did, "service unknown")
			} else {
				did = append(did, "service "+*t.Service)
			}
		}
		if t.AskInfo != nil {
			if *t.AskInfo {
				// Asked for more, the case waits for its reporter: shown
				// "Waiting for the reporter", kept In progress (CF-67).
				r.Waiting = ptr(now)
				did = append(did, "waiting for the reporter")
				replied(ch, c, r, now)
			} else {
				r.Waiting = nil
			}
		}
		markHandled(r, now, false)
		switch {
		case t.KnownIssue != nil && *t.KnownIssue == "":
			if r.KnownIssue != "" {
				did = append(did, "unlinked from "+r.KnownIssue)
				ReopenCase(r, now)
				reopened(ch, c.Person, c.From, r, "unlinked from its known issue", now)
			}
		case t.KnownIssue != nil:
			k := st.knownIssue(*t.KnownIssue)
			if k == nil || k.Draft {
				return fmt.Errorf("%w: no known issue %s", ErrNotFound, *t.KnownIssue)
			}
			if !r.Status.Open() {
				return invalid("C-%d is closed; reopen it first", n)
			}
			linkKnown(st, r, k, 100, c.Person, now)
			if r.Key != nil {
				k.AddKey(*r.Key, r.Job)
			}
			did = append(did, "closed as "+k.ID)
			ch.event(Event{Kind: EventReportLinked, At: now, Report: copyReport(r), By: &c.Person})
			statusChanged(ch, c.Person, c.From, r, now)
		case t.DuplicateOf != nil:
			if !r.Status.Open() {
				return invalid("C-%d is closed; reopen it first", n)
			}
			chk := st.duplicateCheck(r, *t.DuplicateOf)
			if !chk.Allowed {
				return fmt.Errorf("%w: %s", ErrDuplicate, chk.Reason)
			}
			if explanation == "" {
				explanation = fmt.Sprintf("The same problem as C-%d, where it is followed.", chk.end)
			}
			closeDuplicate(st, r, chk.end, c.Person, explanation, now)
			did = append(did, fmt.Sprintf("duplicate of C-%d", chk.end))
			statusChanged(ch, c.Person, c.From, r, now)
		case t.Status != nil && *t.Status != r.Status:
			if err := s.move(r, ch, c, *t.Status, t.Outcome, explanation, now); err != nil {
				return err
			}
		}
		if len(did) > 0 {
			ch.event(Event{Kind: EventReportTriaged, At: now, Report: copyReport(r), By: &c.Person})
			ch.audit(AuditRecord{Action: ActionTriage, Who: c.Person, Target: fmt.Sprintf("case C-%d", n), Detail: fmt.Sprintf("case C-%d: %s", n, strings.Join(did, ", ")), From: c.From})
		}
		return nil
	})
	return r, err
}

// ErrDuplicate is a duplicate whose original does not allow it (CF-04).
var ErrDuplicate = errors.New("not a duplicate")

// move checks and makes an operator's status change through the API: In
// progress, a reopening with a reason, or a close with an outcome the
// case allows (fix, noAction; the others have their own fields or the
// console's actions). An old outcome, notAProblem or noFixNeeded, is
// noAction (CF-90).
func (s *Service) move(r *Case, ch *change, c Caller, to Status, outcome Outcome, explanation string, now time.Time) error {
	outcome = outcome.Canonical()
	switch to {
	case StatusInProgress:
		if r.Status == StatusClosed {
			return invalid("reopen C-%d with a reason first (status new)", r.Number)
		}
		Take(r, now)
	case StatusNew:
		if r.Status != StatusClosed {
			return invalid("C-%d is open already", r.Number)
		}
		if explanation == "" {
			return invalid("say why C-%d is reopened", r.Number)
		}
		ReopenCase(r, now)
		_ = addMessage(r, Message{At: now, Kind: FromSystem, Internal: true, Text: c.Label() + " reopened it: " + clip(explanation, MaxShort)})
		reopened(ch, c.Person, c.From, r, explanation, now)
		return nil
	case StatusClosed:
		if r.Status == StatusClosed {
			return invalid("C-%d is closed already", r.Number)
		}
		if err := r.MayClose(outcome); err != nil {
			return err
		}
		switch outcome {
		case OutcomeDuplicate:
			return invalid("name the original with duplicateOf")
		case OutcomeKnownIssue:
			return invalid("name the known issue with knownIssue")
		case OutcomeException:
			return invalid("accept an exception in the console, where its reason and last day are kept")
		case OutcomeFix:
			if r.Priority <= 1 {
				return invalid("a fix at P%d needs its RCA: close it in the console", r.Priority)
			}
		}
		if explanation == "" {
			return invalid("explain why C-%d is closed", r.Number)
		}
		CloseCase(r, c.Person, outcome, explanation, now)
		if outcome == OutcomeFix && r.Kind() == CaseReport {
			n := r.Number
			ch.after = append(ch.after, func() { s.verify(n) })
		}
	default:
		return invalid("%q is not a status: new, inProgress or closed", to)
	}
	statusChanged(ch, c.Person, c.From, r, now)
	return nil
}

// SetStatus moves a report to a status, as Triage does with only that.
func (s *Service) SetStatus(ctx context.Context, c Caller, n int, to Status, outcome Outcome, explanation string) (Case, error) {
	return s.Triage(ctx, c, n, Triage{Status: &to, Outcome: outcome, Explanation: explanation})
}

// Sweep runs the timers (IS-15, IS-17): a question unanswered closes the
// case as no action needed (CF-90), and a closed case a person opened loses its
// text past the retention.
func (s *Service) Sweep(ctx context.Context) error {
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		set := st.settings()
		now := s.now()
		day := 24 * time.Hour
		changed := false
		for i := range st.index.Lines {
			l := st.index.Lines[i]
			switch {
			case l.Open() && l.Waiting != nil && now.Sub(latest(*l.Waiting, l.LastReporter)) >= time.Duration(set.NeedInfoDays)*day:
				r := st.report(l.Number)
				if r == nil {
					continue
				}
				why := fmt.Sprintf("No answer to our question for %d days; closed. Reopen it with the answer, or send a new report.", set.NeedInfoDays)
				CloseCase(r, systemPerson, OutcomeNoAction, why, now)
				_ = addMessage(r, Message{At: now, Kind: FromSystem, Text: why})
				statusChanged(ch, systemPerson, "", r, now)
				changed = true
			case !l.Open() && !l.Pruned && !l.AgentFound && l.ClosedAt != nil && now.Sub(*l.ClosedAt) >= time.Duration(set.RetentionDays)*day:
				r := st.report(l.Number)
				if r == nil {
					continue
				}
				prune(r)
				changed = true
			}
		}
		if !changed {
			return 0, nil
		}
		return saveReports, nil
	})
}

// prune drops what a closed report says once the retention has passed,
// keeping what it was and how it ended (IS-17).
func prune(r *Case) {
	r.Text, r.Messages, r.Technical, r.Plain, r.Cause, r.Evidence, r.Watchers, r.Similar = "", nil, "", "", "", nil, nil, nil
	r.Pruned = true
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Run sweeps every few minutes while active says this process should:
// one replica runs the timers, so a report closes once.
func (s *Service) Run(ctx context.Context, every time.Duration, active func() bool) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if active != nil && !active() {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := s.Sweep(sctx); err != nil {
			s.log.Warn("the issue service's timers could not run", "err", err)
		}
		cancel()
	}
}

// Notice is one thing that changed for a person following reports.
type Notice struct {
	At     time.Time `json:"at"`
	Report int       `json:"report,omitempty"`
	Known  string    `json:"known,omitempty"`
	Text   string    `json:"text"`
}

// Updates are the changes since a time to the reports a caller reported,
// filed or follows, and to the known issues those are linked to or the
// caller follows (IS-30), newest first. The report page asks for them
// every half minute.
func (s *Service) Updates(ctx context.Context, c Caller, since time.Time) ([]Notice, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []Notice
	known := map[string]bool{}
	for i := range st.index.Lines {
		l := &st.index.Lines[i]
		if l.Reporter != c.ID && l.FiledBy != c.ID && !l.watchedBy(c.ID) {
			continue
		}
		if l.KnownIssue != "" {
			known[l.KnownIssue] = true
		}
		if !l.Updated.After(since) {
			continue
		}
		if l.StatusAt.After(since) && l.Status != StatusNew {
			out = append(out, Notice{At: l.StatusAt, Report: l.Number, Text: fmt.Sprintf("#%d is %s.", l.Number, statusWords(l.Status, l.Outcome.Canonical()))})
		}
		r := st.report(l.Number)
		if r == nil {
			continue
		}
		for _, m := range r.Messages {
			if m.At.After(since) && !m.Draft && !m.Internal && m.Kind == FromOperator {
				out = append(out, Notice{At: m.At, Report: r.Number, Text: fmt.Sprintf("A reply on #%d from %s.", r.Number, m.From.Label())})
			}
		}
	}
	for i := range st.known.Known {
		k := &st.known.Known[i]
		if (!known[k.ID] && !k.followedBy(c.ID)) || k.Draft {
			continue
		}
		for _, u := range k.Updates {
			if u.At.After(since) {
				out = append(out, Notice{At: u.At, Known: k.ID, Text: k.Title + ": " + u.Text})
			}
		}
	}
	sortNotices(out)
	return out, nil
}

// statusWords are a status as a reporter reads it.
func statusWords(s Status, o Outcome) string {
	switch s {
	case StatusNew:
		return "received"
	case StatusInProgress:
		return "being worked on"
	}
	return map[Outcome]string{OutcomeFix: "fixed", OutcomeDuplicate: "closed as a duplicate", OutcomeNoAction: "closed: no action needed",
		OutcomeKnownIssue: "a known issue", OutcomeException: "closed as an accepted exception"}[o]
}

func sortNotices(n []Notice) {
	for i := 1; i < len(n); i++ {
		for j := i; j > 0 && n[j].At.After(n[j-1].At); j-- {
			n[j], n[j-1] = n[j-1], n[j]
		}
	}
}
