package issues

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// --- Cases (Phase 12, wave 5, CU-01 to CU-12, ADR-113; one flow for
// findings and cases, wave 6, CF-01 to CF-24) ---
//
// A report is a case: the one record of a problem someone works on until
// it is closed. A person reports one here (the report page, the avatar
// menu, Sherlock's "Report this", or a system through the intake); the
// console opens one by hand (New Case) or from an alert; and every
// finding of a job's run becomes one, or joins one (package casework
// routes them). Every case has one number, shown as C-<n>, a title, a
// priority P0 to P4, a status (new, inProgress, closed) and, closed,
// exactly one outcome. What 221B adds to a case (the agent's analysis and
// suggested fix, the proposed changes, the RCA's text) is package
// casework's, kept in the case's object beside the issue service's
// record.

// What opened a case.
const (
	CaseReport  = "report"
	CaseFinding = "finding"
	CaseManual  = "manual"
	CaseAlert   = "alert"
)

// The priorities (CF-02): P0 an outage, P1 blocking, P2 a service
// degraded, P3 preemptive, P4 a question.
const (
	MinPriority = 0
	MaxPriority = 4
)

// The bounds of what a case is about.
const (
	MaxTitle  = 200
	MaxObject = 300
	MaxID     = 100
)

// Kind is what opened the case.
func (r *Report) Kind() string {
	if r.Source == "" {
		return CaseReport
	}
	return r.Source
}

// AgentFound says an agent found the case: it was opened from a run's
// finding.
func (r *Report) AgentFound() bool { return r.Source == CaseFinding }

// Pending says whether the case waits for the people who work the cases
// (CF-17): New or In progress.
func (r *Report) Pending() bool { return r.Status.Open() }

// AllowedOutcomes are the outcomes an open case may close with (CF-03):
// one a person opened every one but exception (nothing to mute); one an
// agent found every one but notAProblem (an exception says that) and
// duplicate (its repeats join the open case), unless it is marked a
// possible duplicate, which a person confirms by closing it so (CF-18).
func (r *Report) AllowedOutcomes() []Outcome {
	if !r.Status.Open() {
		return []Outcome{}
	}
	// Any case may be a duplicate of an open original (CF-04): a person
	// may see two agent-found cases are one problem though their keys
	// differ.
	if r.AgentFound() {
		return []Outcome{OutcomeFix, OutcomeDuplicate, OutcomeNoFixNeeded, OutcomeKnownIssue, OutcomeException}
	}
	return []Outcome{OutcomeFix, OutcomeDuplicate, OutcomeNotAProblem, OutcomeNoFixNeeded, OutcomeKnownIssue}
}

// RCARequired says a fix needs its RCA: at P0 and P1 (CF-03).
func (r *Report) RCARequired() bool { return r.Priority <= 1 }

// MayClose says why a case may not close with an outcome, if it may not.
func (r *Report) MayClose(o Outcome) error {
	if !o.Valid() {
		return invalid("say why it is closed: fix, duplicate, notAProblem, noFixNeeded, knownIssue or exception")
	}
	if !slices.Contains(r.AllowedOutcomes(), o) {
		switch {
		case !r.Status.Open():
			return invalid("C-%d is closed already", r.Number)
		case r.AgentFound():
			return invalid("C-%d was found by an agent: accept it as an exception instead of not a problem", r.Number)
		}
		return invalid("C-%d was opened by a person: an exception is for what agents find", r.Number)
	}
	return nil
}

// titleOf is a report's title: its first line, cut to fit.
func titleOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return clip(line, 120)
}

// oneLine checks a line a person wrote, scrubbed.
func oneLine(what, s string, limit int) (string, error) {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if strings.ContainsAny(s, "\n\r") {
		s = strings.Join(strings.Fields(s), " ")
	}
	if utf8.RuneCountInString(s) > limit {
		return "", invalid("keep %s under %d characters", what, limit)
	}
	return scrub.Text(s), nil
}

// about applies a triage's change to what a case is about (CU-03).
func (t Triage) about(r *Report, did *[]string) error {
	if t.Title != nil {
		v, err := oneLine("the title", *t.Title, MaxTitle)
		if err != nil {
			return err
		}
		if v == "" {
			return invalid("give the case a title")
		}
		if v != r.Title {
			r.Title = v
			*did = append(*did, "title changed")
		}
	}
	if t.Text != nil {
		if r.Kind() == CaseReport {
			return invalid("a report's words are its reporter's; add a note instead")
		}
		v := strings.TrimSpace(*t.Text)
		if utf8.RuneCountInString(v) > MaxText {
			return invalid("keep it under %d characters", MaxText)
		}
		if v = scrub.Text(v); v != r.Text {
			r.Text = v
			*did = append(*did, "detail changed")
		}
	}
	if t.Cluster != nil {
		v, err := oneLine("the cluster", *t.Cluster, MaxID)
		if err != nil {
			return err
		}
		if v != r.Cluster {
			r.Cluster = v
			*did = append(*did, "cluster "+orNone(v))
		}
	}
	if t.Object != nil {
		v, err := oneLine("what it is about", *t.Object, MaxObject)
		if err != nil {
			return err
		}
		if v != r.Object {
			r.Object = v
			*did = append(*did, "about "+orNone(v))
		}
	}
	return nil
}

// Expect is what an edit in place started from, field by field (CF-60):
// a field that no longer holds it is not changed, and the edit is
// refused with what the field holds now.
type Expect struct {
	Title, Text, Cluster, Object, Incident *string
}

// FieldConflict is an edit refused because the field changed since the
// person started editing it (CF-60), with what it holds now.
type FieldConflict struct {
	Field   string
	Current string
}

func (e *FieldConflict) Error() string {
	return "the " + e.Field + " changed since the edit started"
}

// check says which field no longer holds what the edit started from: the
// title as a case's view shows it (a report's first line until it has
// one).
func (e *Expect) check(r *Report) error {
	if e == nil {
		return nil
	}
	title := r.Title
	if title == "" {
		title = titleOf(r.Text)
	}
	for _, f := range []struct {
		name string
		want *string
		have string
	}{{"title", e.Title, title}, {"text", e.Text, r.Text}, {"cluster", e.Cluster, r.Cluster}, {"object", e.Object, r.Object}, {"incident", e.Incident, r.Incident}} {
		if f.want != nil && strings.TrimSpace(*f.want) != f.have {
			return &FieldConflict{Field: f.name, Current: f.have}
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Opening is a case opened in the console: by hand, from an alert, or
// from a run's finding (CF-05).
type Opening struct {
	Source  string
	Title   string
	Text    string
	Cluster string
	Object  string
	// Priority is P0 to P4; nil is P2.
	Priority   *int
	PriorityBy string
	// Key, Job, Run, Finding and Severity are an agent-found case's.
	Key      *Key
	Job      string
	Run      string
	Finding  string
	Severity string
	// RegressionOf, PossibleDuplicateOf and Previous link it to an
	// earlier case.
	RegressionOf        int
	PossibleDuplicateOf int
	Previous            *PreviousClose
	// KnownIssue opens it closed as that known issue (CF-12).
	KnownIssue string
	// Type is its type ("" is other), Incident another system's number;
	// CreatedBy who opened it when not the caller: the agent that found it
	// (CF-35 to CF-37).
	Type      string
	Incident  string
	CreatedBy *Actor
}

// Open opens a case in the console. A person's is not matched to a known
// issue nor investigated: whoever opens it knows what it is, and the
// console's case analysis looks into it; the person has looked at it, so
// it counts as acknowledged and responded to at once, and its SLA clock
// runs. An agent-found case waits for a person; its clock runs at P0-P1
// only (CF-02).
func (s *Service) Open(ctx context.Context, c Caller, in Opening) (Report, error) {
	var out Report
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		r, err := (&Tx{st: st, ch: ch, now: s.now()}).Open(c, in)
		if err != nil {
			return 0, err
		}
		out = *copyReport(r)
		parts := saveReports
		if in.KnownIssue != "" {
			parts |= saveKnown
		}
		return parts, nil
	})
	return out, err
}

var sourceWords = map[string]string{CaseFinding: "from a finding", CaseManual: "by hand", CaseAlert: "from an alert", CaseReport: "from a report"}

func aboutWords(r *Report) string {
	var w []string
	if r.Cluster != "" {
		w = append(w, "on "+r.Cluster)
	}
	if r.Object != "" {
		w = append(w, "about "+r.Object)
	}
	if len(w) == 0 {
		return ""
	}
	return ", " + strings.Join(w, ", ")
}

// caseOf checks what a case is opened with.
func caseOf(in Opening) (Report, error) {
	title, err := oneLine("the title", in.Title, MaxTitle)
	if err != nil {
		return Report{}, err
	}
	if title == "" {
		return Report{}, invalid("give the case a title")
	}
	text := strings.TrimSpace(strings.ToValidUTF8(in.Text, ""))
	if utf8.RuneCountInString(text) > MaxText {
		text = string([]rune(text)[:MaxText-1]) + "…"
	}
	clean := scrub.Text(text)
	cluster, err := oneLine("the cluster", in.Cluster, MaxID)
	if err != nil {
		return Report{}, err
	}
	object, err := oneLine("what it is about", in.Object, MaxObject)
	if err != nil {
		return Report{}, err
	}
	p := DefaultPriority
	if in.Priority != nil {
		p = *in.Priority
	}
	if p < MinPriority || p > MaxPriority {
		return Report{}, invalid("a priority is P%d to P%d", MinPriority, MaxPriority)
	}
	if in.Source == CaseFinding && p == 0 {
		return Report{}, invalid("agents never set P0")
	}
	typ, err := checkType(in.Type)
	if err != nil {
		return Report{}, err
	}
	incident, err := CleanIncident(in.Incident)
	if err != nil {
		return Report{}, err
	}
	return Report{Title: title, Text: clean, Scrubbed: clean != text, Source: in.Source, Cluster: cluster, Object: object, Priority: p, Type: typ, Incident: incident}, nil
}

// CaseView is one case as a caller sees it (as ReportView), whole, for
// the console's Cases.
func (s *Service) CaseView(ctx context.Context, c Caller, n int) (ReportView, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return ReportView{}, err
	}
	l := st.line(n)
	if l == nil || !st.visible(c, l) {
		return ReportView{}, fmt.Errorf("%w: no case C-%d", ErrNotFound, n)
	}
	r := st.report(n)
	if r == nil {
		if st.err != nil {
			return ReportView{}, st.err
		}
		return ReportView{}, fmt.Errorf("%w: no case C-%d", ErrNotFound, n)
	}
	return st.reportView(c, r, s.now()), nil
}

// CaseViews are the cases a caller may see, as their rows show them,
// newest first: from the index.
func (s *Service) CaseViews(ctx context.Context, c Caller, f Filter) ([]ReportView, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []ReportView
	q := strings.Fields(strings.ToLower(f.Query))
	now := s.now()
	for i := len(st.index.Lines) - 1; i >= 0; i-- {
		l := &st.index.Lines[i]
		if st.visible(c, l) && f.match(c, l, q) {
			r := l.lite()
			out = append(out, st.reportView(c, &r, now))
		}
	}
	return out, nil
}

// ---------- duplicates (CF-04) ----------

// Offer is what fits instead of a duplicate whose original is closed.
type Offer struct {
	// Kind is reopenOriginal, regression, sameKnownIssue, notAProblem,
	// removeException or sameAs.
	Kind        string `json:"kind"`
	Original    string `json:"original"`
	KnownIssue  string `json:"knownIssue,omitempty"`
	Exception   string `json:"exception,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

// The kinds of Offer.
const (
	OfferReopenOriginal  = "reopenOriginal"
	OfferRegression      = "regression"
	OfferSameKnownIssue  = "sameKnownIssue"
	OfferNotAProblem     = "notAProblem"
	OfferRemoveException = "removeException"
	OfferSameAs          = "sameAs"
)

// DuplicateCheck says whether a case may close as a duplicate of an
// original: the end of its duplicate chain, how that stands, and what
// fits instead when it is closed.
type DuplicateCheck struct {
	Allowed  bool    `json:"allowed"`
	Original string  `json:"original"`
	Status   Status  `json:"status,omitempty"`
	Outcome  Outcome `json:"outcome,omitempty"`
	Offers   []Offer `json:"offers"`
	Reason   string  `json:"reason,omitempty"`
	end      int
}

// End is the original's number, the end of the chain.
func (d DuplicateCheck) End() int { return d.end }

// duplicateCheck follows the duplicate chain from original to its end and
// says whether r may close as its duplicate (CF-04).
func (st *state) duplicateCheck(r *Report, original int) DuplicateCheck {
	out := DuplicateCheck{Original: "C-" + strconv.Itoa(original), Offers: []Offer{}}
	seen := map[int]bool{r.Number: true}
	cur := original
	var l *Line
	for {
		if seen[cur] {
			out.Reason = "a case can never be its own original, directly or through a chain of duplicates"
			return out
		}
		seen[cur] = true
		if l = st.line(cur); l == nil {
			out.Reason = fmt.Sprintf("there is no case C-%d", cur)
			return out
		}
		if l.Status != StatusClosed || l.Outcome != OutcomeDuplicate || l.DuplicateOf == 0 {
			break
		}
		cur = l.DuplicateOf
	}
	id := "C-" + strconv.Itoa(cur)
	out.end, out.Original, out.Status, out.Outcome = cur, id, l.Status, l.Outcome
	if l.Open() {
		out.Allowed = true
		return out
	}
	switch l.Outcome {
	case OutcomeFix:
		out.Reason = id + " was closed as fixed"
		out.Offers = append(out.Offers, Offer{Kind: OfferReopenOriginal, Original: id}, Offer{Kind: OfferRegression, Original: id})
	case OutcomeKnownIssue:
		out.Reason = id + " was closed as the known issue " + l.KnownIssue
		if k := st.knownIssue(l.KnownIssue); k != nil && k.Status == KnownResolved {
			out.Reason += ", resolved since"
			out.Offers = append(out.Offers, Offer{Kind: OfferRegression, Original: id, KnownIssue: l.KnownIssue})
		} else {
			out.Offers = append(out.Offers, Offer{Kind: OfferSameKnownIssue, Original: id, KnownIssue: l.KnownIssue})
		}
	case OutcomeException:
		out.Reason = id + " was accepted as the exception " + l.Exception
		out.Offers = append(out.Offers, Offer{Kind: OfferNotAProblem, Original: id, Exception: l.Exception, Explanation: l.Explanation},
			Offer{Kind: OfferRemoveException, Original: id, Exception: l.Exception})
	case OutcomeNotAProblem:
		out.Reason = id + " was closed as not a problem"
		out.Offers = append(out.Offers, Offer{Kind: OfferNotAProblem, Original: id, Explanation: l.Explanation}, Offer{Kind: OfferReopenOriginal, Original: id})
	case OutcomeNoFixNeeded:
		out.Reason = id + " was closed as needing no fix"
		out.Offers = append(out.Offers, Offer{Kind: OfferReopenOriginal, Original: id}, Offer{Kind: OfferSameAs, Original: id})
	default:
		out.Reason = id + " is closed"
	}
	return out
}

// DuplicateCheck says whether case n may close as a duplicate of an
// original (CF-04), as the server checks it again at confirm.
func (s *Service) DuplicateCheck(ctx context.Context, n, original int) (DuplicateCheck, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return DuplicateCheck{}, err
	}
	r := st.report(n)
	if r == nil {
		if st.err != nil {
			return DuplicateCheck{}, st.err
		}
		return DuplicateCheck{}, fmt.Errorf("%w: no case C-%d", ErrNotFound, n)
	}
	return st.duplicateCheck(r, original), nil
}

// closeDuplicate closes r as a duplicate of its original: its reporter
// follows the original from now on, and the original notes it.
func closeDuplicate(st *state, r *Report, original int, by Person, explanation string, now time.Time) {
	CloseCase(r, by, OutcomeDuplicate, explanation, now)
	r.DuplicateOf = original
	o := st.report(original)
	if o == nil {
		return
	}
	if r.Kind() == CaseReport && r.Reporter.ID != "" && !o.watchedBy(r.Reporter.ID) && len(o.Watchers) < MaxWatchers {
		o.Watchers = append(o.Watchers, Person{ID: r.Reporter.ID, Name: r.Reporter.Name, Email: r.Reporter.Email})
	}
	who := ""
	if r.Kind() == CaseReport {
		who = ", reported by " + r.Reporter.Label()
	}
	_ = addMessage(o, Message{At: now, Kind: FromSystem, Internal: true, Text: fmt.Sprintf("C-%d (%s%s) was closed as a duplicate of this case.", r.Number, clip(firstTitle(r), 120), who)})
	o.Updated = now
}

func firstTitle(r *Report) string {
	if r.Title != "" {
		return r.Title
	}
	return titleOf(r.Text)
}

// ---------- a change of many cases at once ----------

// Tx is one change of the cases, the known issues and the index, saved
// together (CF-20): what package casework does to cases, and a run's
// findings routed into them, which update the index once and each case
// object at most once.
type Tx struct {
	st    *state
	ch    *change
	now   time.Time
	known bool
	index bool
}

// Batch applies fn to the records and saves what it changed, again from
// what is there when another writer came first: fn must change only what
// the Tx gives it.
func (s *Service) Batch(ctx context.Context, fn func(tx *Tx) error) error {
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		tx := &Tx{st: st, ch: ch, now: s.now()}
		if err := fn(tx); err != nil {
			return 0, err
		}
		// The case objects changed are saved with the index whatever the
		// parts say.
		parts := 0
		if tx.known {
			parts |= saveKnown
		}
		if tx.index {
			parts |= saveReports
		}
		return parts, nil
	})
}

// Apply changes one case in a Tx and returns it as saved.
func (s *Service) Apply(ctx context.Context, n int, fn func(tx *Tx, r *Report) error) (Report, error) {
	var out Report
	err := s.Batch(ctx, func(tx *Tx) error {
		r, err := tx.Case(n)
		if err != nil {
			return err
		}
		if err := tx.st.touch(r, tx.now, func() error { return fn(tx, r) }); err != nil {
			return err
		}
		r.Updated = tx.now
		out = *copyReport(r)
		return nil
	})
	return out, err
}

// Now is the change's time.
func (tx *Tx) Now() time.Time { return tx.now }

// As names who makes the change: each case it changes names them as its
// last changer (CF-35). Without it, the system.
func (tx *Tx) As(a Actor) { tx.st.actor = &a }

// By names who changed one case in the change, when not the change's
// actor: the agent whose run found it again, say.
func (tx *Tx) By(r *Report, a Actor) {
	if l, ok := tx.st.cases[r.Number]; ok && l.r == r {
		l.by = &a
	}
}

// Lines are the index, by number; a change of a case changes its line
// when saved.
func (tx *Tx) Lines() []Line { return tx.st.index.Lines }

// Line is a case's line, or nil.
func (tx *Tx) Line(n int) *Line { return tx.st.line(n) }

// Case is a case's whole record, which the Tx saves when changed.
func (tx *Tx) Case(n int) (*Report, error) {
	r := tx.st.report(n)
	if r == nil {
		if tx.st.err != nil {
			return nil, tx.st.err
		}
		return nil, fmt.Errorf("%w: no case C-%d", ErrNotFound, n)
	}
	return r, nil
}

// Known is a known issue, which the Tx saves; nil when there is none.
func (tx *Tx) Known(id string) *KnownIssue {
	k := tx.st.knownIssue(id)
	if k != nil {
		tx.known = true
	}
	return k
}

// KnownIssues are every known issue, which the Tx saves.
func (tx *Tx) KnownIssues() []*KnownIssue {
	tx.known = true
	out := make([]*KnownIssue, len(tx.st.known.Known))
	for i := range tx.st.known.Known {
		out[i] = &tx.st.known.Known[i]
	}
	return out
}

// Settings are the issue service's settings.
func (tx *Tx) Settings() Settings { return tx.st.settings() }

// Audit records a line once the change is saved.
func (tx *Tx) Audit(a AuditRecord) { tx.ch.audit(a) }

// Changed raises the event of a case changed, for the subscribers.
func (tx *Tx) Changed(r *Report, by *Person) {
	tx.ch.event(Event{Kind: EventReportTriaged, At: tx.now, Report: copyReport(r), By: by})
}

// Drop lets a case go: its line and its object.
func (tx *Tx) Drop(n int) {
	tx.index = true
	tx.st.drop(n)
}

// DuplicateCheck says whether r may close as a duplicate of original.
func (tx *Tx) DuplicateCheck(r *Report, original int) DuplicateCheck {
	return tx.st.duplicateCheck(r, original)
}

// CloseDuplicate closes r as a duplicate of its original (the end of the
// chain a DuplicateCheck allowed).
func (tx *Tx) CloseDuplicate(r *Report, original int, by Person, explanation string) {
	closeDuplicate(tx.st, r, original, by, explanation, tx.now)
}

// LinkKnown closes r as a known issue a person chose: an agent-found
// case's key is added to it, so its repeats are recorded there.
func (tx *Tx) LinkKnown(r *Report, k *KnownIssue, by Person) {
	tx.known = true
	linkKnown(tx.st, r, k, 100, by, tx.now)
	if r.Key != nil {
		k.AddKey(*r.Key, r.Job)
	}
}

// Note adds a line to a case's conversation: internal, or for its
// reporter too.
func (tx *Tx) Note(r *Report, from Person, text string, internal bool) error {
	kind := FromSystem
	if !from.System {
		kind = FromOperator
	}
	return addMessage(r, Message{At: tx.now, Kind: kind, From: from, Text: clip(text, MaxText), Internal: internal})
}

// SimilarOpen is the open case on the same cluster and object whose title
// reads most like title, or 0 (CF-18): a possible duplicate, which a
// person confirms; never merged by itself.
func (tx *Tx) SimilarOpen(title, cluster, object string, skip int) int {
	if object == "" {
		return 0
	}
	q := words(title)
	var cands []Line
	var docs [][]string
	for _, l := range tx.st.index.Lines {
		lo := l.Object
		if l.Key != nil {
			lo = l.Key.Object
		}
		if l.Number == skip || !l.Open() || l.Cluster != cluster || !strings.EqualFold(lo, object) {
			continue
		}
		cands = append(cands, l)
		docs = append(docs, words(l.Title))
	}
	if len(cands) == 0 || len(q) == 0 {
		return 0
	}
	c := newCorpus(append(docs, q))
	best, score := 0, 0.0
	for i, l := range cands {
		if x := c.likeness(q, docs[i]); x >= duplicateLikeness && x > score {
			best, score = l.Number, x
		}
	}
	return best
}

// Follow makes a person follow a case.
func (tx *Tx) Follow(r *Report, p Person) error { return follow(r, p, true) }

// Open opens a case (see Service.Open) in the Tx.
func (tx *Tx) Open(c Caller, in Opening) (*Report, error) {
	if !c.Operator {
		return nil, fmt.Errorf("%w: only the people who work the cases open them", ErrDenied)
	}
	switch in.Source {
	case CaseFinding, CaseManual, CaseAlert:
	default:
		return nil, invalid("a case opened in the console is from a finding, an alert or by hand, not %q", in.Source)
	}
	r, err := caseOf(in)
	if err != nil {
		return nil, err
	}
	now := tx.now
	r.Created, r.Updated, r.Status, r.StatusAt, r.LastReporter = now, now, StatusNew, now, now
	r.OpenedBy = &Person{ID: c.ID, Name: c.Name, Email: c.Email, System: c.System}
	r.PriorityBy = firstNonEmpty(in.PriorityBy, c.Label())
	r.Key, r.Job, r.Run, r.Finding, r.Severity = in.Key, in.Job, in.Run, in.Finding, in.Severity
	r.RegressionOf, r.PossibleDuplicateOf, r.Previous = in.RegressionOf, in.PossibleDuplicateOf, in.Previous
	if in.Source == CaseFinding {
		if r.Priority <= 1 {
			r.SLAFrom = ptr(now)
		}
		r.Seen = &Seen{Count: 1, First: now, Last: now}
	} else {
		r.Acknowledged, r.FirstResponse, r.SLAFrom = ptr(now), ptr(now), ptr(now)
	}
	if in.RegressionOf != 0 && tx.st.line(in.RegressionOf) == nil {
		return nil, invalid("there is no case C-%d to be a regression of", in.RegressionOf)
	}
	var k *KnownIssue
	if in.KnownIssue != "" {
		if k = tx.st.knownIssue(in.KnownIssue); k == nil || k.Draft {
			return nil, fmt.Errorf("%w: no known issue %s", ErrNotFound, in.KnownIssue)
		}
	}
	by := ActorOf(c.Person)
	if in.CreatedBy != nil {
		by = *in.CreatedBy
	}
	r.CreatedBy = &by
	pr := tx.st.add(r)
	tx.By(pr, by)
	stamp(pr, by, now)
	if k != nil {
		tx.LinkKnown(pr, k, c.Person)
	}
	tx.ch.event(Event{Kind: EventReportCreated, At: now, Report: copyReport(pr), By: &c.Person})
	detail := fmt.Sprintf("case C-%d opened %s by %s, P%d, %s%s", pr.Number, sourceWords[pr.Source], by.Label(), pr.Priority, pr.CaseType(), aboutWords(pr))
	if pr.Incident != "" {
		detail += ", incident " + pr.Incident
	}
	if k != nil {
		detail += ", closed as the known issue " + k.ID
	}
	tx.ch.audit(AuditRecord{Action: ActionReport, Who: c.Person, Target: fmt.Sprintf("case C-%d", pr.Number), Detail: detail, From: c.From})
	return pr, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---------- similar cases (CF-12) ----------

// Scored is a case's line with how alike it is to a text or an object,
// 0 to 100.
type Scored struct {
	Line
	Score int `json:"score"`
}

// similarScore is the least score a similar case is shown at.
const similarScore = 40

// Similar ranks the cases for what a person is about to open: those on
// the same object of the cluster first (100), then those whose titles
// read alike. skip leaves one out (the case itself).
func (s *Service) Similar(ctx context.Context, text, cluster, object string, skip int) ([]Scored, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	q := words(text)
	docs := make([][]string, len(st.index.Lines))
	for i, l := range st.index.Lines {
		docs[i] = words(l.Title)
	}
	c := newCorpus(append(docs, q))
	obj := strings.ToLower(strings.TrimSpace(object))
	var out []Scored
	for i, l := range st.index.Lines {
		if l.Number == skip {
			continue
		}
		score := 0
		if obj != "" && (cluster == "" || l.Cluster == cluster) && (strings.ToLower(l.Object) == obj || l.Key != nil && strings.ToLower(l.Key.Object) == obj) {
			score = 100
		} else if len(q) > 0 {
			score = int(c.likeness(q, docs[i]) * 100)
		}
		if score >= similarScore {
			out = append(out, Scored{Line: l, Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Number > out[j].Number
	})
	return out, nil
}

// Index is the index of every case, by number: for the console's own
// reading (the repeats, the retention).
func (s *Service) Index(ctx context.Context) ([]Line, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	return st.index.Lines, nil
}

// DropClosedAgentCases lets go of the agent-found cases closed before a
// time (CF-21): their lines and objects. It returns their numbers.
func (s *Service) DropClosedAgentCases(ctx context.Context, before time.Time, by Person) ([]int, error) {
	var gone []int
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		gone = nil
		for _, l := range st.index.Lines {
			if l.AgentFound && !l.Open() && l.ClosedAt != nil && l.ClosedAt.Before(before) {
				gone = append(gone, l.Number)
			}
		}
		if len(gone) == 0 {
			return 0, nil
		}
		for _, n := range gone {
			st.drop(n)
		}
		return saveReports, nil
	})
	return gone, err
}

// Reindex adds the lines of the case objects the index does not have
// (a restore made them): it returns how many it added.
func (s *Service) Reindex(ctx context.Context) (int, error) {
	names, err := s.cases.List(ctx)
	if err != nil {
		return 0, err
	}
	added := 0
	err = s.update(ctx, func(st *state, ch *change) (int, error) {
		added = 0
		for _, name := range names {
			n, err := strconv.Atoi(name)
			if err != nil || n <= 0 || st.line(n) != nil {
				continue
			}
			r, _, _, err := s.loadCase(ctx, n)
			if err != nil {
				continue
			}
			st.index.Lines = append(st.index.Lines, lineOf(r))
			st.index.Next = max(st.index.Next, n+1)
			added++
		}
		if added == 0 {
			return 0, nil
		}
		sort.SliceStable(st.index.Lines, func(i, j int) bool { return st.index.Lines[i].Number < st.index.Lines[j].Number })
		return saveReports, nil
	})
	return added, err
}

// Cases are the objects the cases are kept in, for a backup.
func (s *Service) Cases() kv.Objects { return s.cases }

// --- end of cases.
