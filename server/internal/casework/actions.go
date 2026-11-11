package casework

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- What a person does to a case (CF-01 to CF-04, CF-12) ---

// AlertInput is the alert a case is opened from. Since CF-84 its name is
// its reason ("Pod status Unknown: node r620-worker stopped reporting"),
// and Key its key on the Alerts tab ("pod:shop/web-1"), which the case
// keeps to link the alert.
type AlertInput struct {
	Cluster  string `json:"cluster"`
	Key      string `json:"key,omitempty"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	// Object is what it fires on ("shop/Pod/web-1"), Summary what it says.
	Object  string `json:"object,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// NewCase is what a person opens a case with (New Case, an alert): a
// title, a detail, the cluster and what it is about (any words, or
// nothing), a priority; a case it is a regression of, or a known issue
// it is created closed as (CF-12).
type NewCase struct {
	FromAlert    *AlertInput `json:"fromAlert,omitempty"`
	Title        string      `json:"title,omitempty"`
	Detail       string      `json:"detail,omitempty"`
	Cluster      string      `json:"cluster,omitempty"`
	Resource     string      `json:"resource,omitempty"`
	Priority     *int        `json:"priority,omitempty"`
	RegressionOf string      `json:"regressionOf,omitempty"`
	KnownIssue   string      `json:"knownIssue,omitempty"`
	// Type is what kind of problem it is (other when none is given, an
	// outage for an alert's); Incident another system's number for it
	// (CF-36, CF-37).
	Type     string `json:"type,omitempty"`
	Incident string `json:"incident,omitempty"`
}

// Create opens a case and starts its analysis (CW-02, CW-04, CF-12). One
// from an alert takes its priority from the alert's severity: critical
// P0, warning P2. One with a known issue is created closed as it.
func (s *Service) Create(ctx context.Context, in NewCase, c Caller) (View, error) {
	info, analyse, err := s.open(ctx, in, opener{issue: c.issue(), actor: c.actor(), who: who(c.Person), from: c.From})
	if err != nil {
		return View{}, err
	}
	if !analyse {
		return s.Get(ctx, c, info.ID)
	}
	// The answer is read before the analysis starts, so it says analysing
	// even when the agent is quicker than the reply.
	s.startAnalysis(ctx, info.Number, c.actor())
	v, err := s.Get(ctx, c, info.ID)
	s.launch(info)
	return v, err
}

// opener is who opens a case: a person (Create), or the console on its
// own (the safety net for a critical alert, CF-78), with the words its
// history and the audit log say it with.
type opener struct {
	issue issues.Caller
	actor issues.Actor
	who   audit.Who
	from  string
	// opened and what replace the history's and the audit log's words
	// for what opened it.
	opened, what string
}

// open opens a case as Create does, and says whether its analysis
// should start.
func (s *Service) open(ctx context.Context, in NewCase, op opener) (caseInfo, bool, error) {
	o := issues.Opening{Source: SourceManual, Title: in.Title, Text: in.Detail, Cluster: strings.TrimSpace(in.Cluster), Object: normObject(in.Resource), Priority: in.Priority,
		KnownIssue: strings.TrimSpace(in.KnownIssue), Type: in.Type, Incident: in.Incident}
	src := Source{Kind: SourceManual}
	if a := in.FromAlert; a != nil {
		if strings.TrimSpace(a.Name) == "" {
			return caseInfo{}, false, fmt.Errorf("%w: name the alert", ErrInvalid)
		}
		o.Source = SourceAlert
		if o.Type == "" {
			o.Type = issues.TypeOutage
		}
		// The object in brackets: the alert's reason may hold a colon itself.
		o.Title = firstOf(in.Title, a.Name+bracketed(a.Object))
		o.Text, o.Cluster, o.Object = firstOf(in.Detail, a.Summary), firstOf(in.Cluster, a.Cluster), normObject(firstOf(in.Resource, a.Object))
		if o.Priority == nil {
			p := AlertPriority(a.Severity)
			o.Priority, o.PriorityBy = &p, "alert"
		}
		src = Source{Kind: SourceAlert, Alert: clip(a.Name, 200), Severity: clip(a.Severity, 40), AlertKey: clip(a.Key, 300)}
	}
	if in.RegressionOf != "" {
		n, ok := number(in.RegressionOf)
		if !ok {
			return caseInfo{}, false, fmt.Errorf("%w: a regression names its case as C-<n>", ErrInvalid)
		}
		o.RegressionOf = n
	}
	o.Title, o.Text, o.Object = clip(o.Title, MaxTitle), clip(o.Text, MaxDetail), clip(o.Object, MaxResource)
	if err := s.knownCluster(o.Cluster); err != nil {
		return caseInfo{}, false, err
	}
	r, err := s.o.Issues.Open(ctx, op.issue, o)
	if err != nil {
		return caseInfo{}, false, s.fromIssues(err)
	}
	id := caseID(r.Number)
	now := s.now()
	info := caseInfo{ID: id, Number: r.Number, Title: r.Title, Priority: r.Priority, Cluster: r.Cluster, Resource: r.Object, Detail: r.Text, Source: src}
	analyse := r.Status.Open() && r.Cluster != "" && s.o.Jobs != nil && s.o.Jobs.On(ctx, jobs.KindCase, s.event(info, false))
	opened := map[string]string{SourceManual: "Opened by hand", SourceAlert: "Opened from the alert " + src.Alert}[src.Kind]
	if op.opened != "" {
		opened = op.opened
	}
	if r.RegressionOf != 0 {
		opened += ", a regression of " + caseID(r.RegressionOf)
	}
	err = s.updateWork(ctx, r.Number, func(w *Work) error {
		*w = Work{Number: r.Number, Source: src, Updated: now}
		addEvent(w, Event{At: now, What: fmt.Sprintf("%s, P%d, %s%s", opened, r.Priority, r.CaseType(), prefixed(", incident ", r.Incident)), By: op.actor})
		switch {
		case r.KnownIssue != "":
			addEvent(w, Event{At: now, What: "Created closed as the known issue " + r.KnownIssue, By: op.actor})
		case analyse:
			addEvent(w, Event{At: now, What: "The case analysis job started on it"})
		case r.Cluster == "":
			addEvent(w, Event{At: now, What: "Not analyzed: it names no cluster"})
		default:
			addEvent(w, Event{At: now, What: "Not analyzed: the case analysis job is paused, or does not cover this cluster (AIOps › Jobs)"})
		}
		return nil
	})
	if err != nil {
		return caseInfo{}, false, err
	}
	what := map[string]string{SourceManual: "by hand", SourceAlert: "from the alert " + src.Alert}[src.Kind]
	if op.what != "" {
		what = op.what
	}
	extra := ""
	if r.KnownIssue != "" {
		extra = ", closed as the known issue " + r.KnownIssue
	}
	if r.RegressionOf != 0 {
		extra += ", a regression of " + caseID(r.RegressionOf)
	}
	s.record(audit.Event{Action: ActionCreate, Who: op.who, Cluster: r.Cluster, Target: id, From: op.from, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("case %s opened %s, P%d%s%s%s, type %s%s; analysis %s", id, what, r.Priority, prefixed(" on ", r.Cluster), objectOf(r.Object), extra, r.CaseType(), prefixed(", incident ", r.Incident), map[bool]string{true: "started", false: "not started"}[analyse])})
	return info, analyse, nil
}

// ---------- similar cases before a person creates one (CF-12) ----------

// SimilarInput is what a person is typing in New Case.
type SimilarInput struct {
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Cluster  string `json:"cluster,omitempty"`
	Resource string `json:"resource,omitempty"`
}

// SimilarCase is a case like the one being typed.
type SimilarCase struct {
	ID          string         `json:"id"`
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	Status      issues.Status  `json:"status"`
	Outcome     issues.Outcome `json:"outcome,omitempty"`
	Assignee    *issues.Person `json:"assignee,omitempty"`
	Explanation string         `json:"explanation,omitempty"`
	RCA         string         `json:"rcaId,omitempty"`
	RCACause    string         `json:"rcaCause,omitempty"`
	ClosedAt    *time.Time     `json:"closedAt,omitempty"`
	Score       int            `json:"score"`
}

// SimilarKnown is a known issue like it.
type SimilarKnown struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	Status     issues.KnownStatus `json:"status"`
	Workaround string             `json:"workaround,omitempty"`
	Expected   string             `json:"expected,omitempty"`
	Score      int                `json:"score"`
}

// SimilarException is an active exception on the same object: why agents
// are quiet about it.
type SimilarException struct {
	ID      string    `json:"id"`
	Cluster string    `json:"cluster"`
	Object  string    `json:"object"`
	Check   string    `json:"check"`
	Reason  string    `json:"reason"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

// Similar is what New Case shows beside the form while a person types.
type Similar struct {
	Open            []SimilarCase      `json:"open"`
	Known           []SimilarKnown     `json:"known"`
	Exceptions      []SimilarException `json:"exceptions"`
	RecentNoProblem []SimilarCase      `json:"recentNoProblem"`
	RecentFixed     []SimilarCase      `json:"recentFixed"`
}

// similarMax bounds each list.
const similarMax = 8

// knownScore is the least confidence a known issue is shown at.
const knownScore = 40

// SimilarTo finds what is like the case a person is about to create
// (CF-12): open cases on the same object or with a title alike, the
// known issues it matches, the exceptions on the same object, and the
// cases closed as no action needed (CF-90) or fixed within
// lookBackDays. No agent runs; creating anyway is always allowed.
func (s *Service) SimilarTo(ctx context.Context, in SimilarInput) (Similar, error) {
	out := Similar{Open: []SimilarCase{}, Known: []SimilarKnown{}, Exceptions: []SimilarException{}, RecentNoProblem: []SimilarCase{}, RecentFixed: []SimilarCase{}}
	text := strings.TrimSpace(in.Title + " " + in.Detail)
	obj := normObject(in.Resource)
	if text == "" && obj == "" {
		return out, nil
	}
	scored, err := s.o.Issues.Similar(ctx, text, in.Cluster, obj, 0)
	if err != nil {
		return out, err
	}
	back := s.now().AddDate(0, 0, -s.settings(ctx).LookBackDays)
	for _, x := range scored {
		sc := SimilarCase{ID: caseID(x.Number), Number: x.Number, Title: x.Title, Status: x.Status, Outcome: x.Outcome, Assignee: x.Assignee, Explanation: x.Explanation, RCA: x.RCA, RCACause: x.RCACause, ClosedAt: x.ClosedAt, Score: x.Score}
		recent := x.ClosedAt != nil && x.ClosedAt.After(back)
		switch {
		case x.Open() && len(out.Open) < similarMax:
			out.Open = append(out.Open, sc)
		case recent && x.Outcome == issues.OutcomeNoAction && len(out.RecentNoProblem) < similarMax:
			out.RecentNoProblem = append(out.RecentNoProblem, sc)
		case recent && x.Outcome == issues.OutcomeFix && len(out.RecentFixed) < similarMax:
			out.RecentFixed = append(out.RecentFixed, sc)
		}
	}
	if text != "" {
		m, err := s.o.Issues.MatchText(ctx, text)
		if err != nil {
			return out, err
		}
		for _, cand := range m.Known {
			if cand.Confidence < knownScore || len(out.Known) >= similarMax {
				continue
			}
			k, err := s.o.Issues.KnownIssue(ctx, system, cand.ID)
			if err != nil {
				continue
			}
			out.Known = append(out.Known, SimilarKnown{ID: k.ID, Title: k.Title, Status: k.Status, Workaround: k.Workaround, Expected: k.Expected, Score: cand.Confidence})
		}
	}
	if obj != "" {
		list, err := s.o.Lessons.Exceptions(ctx)
		if err != nil {
			return out, err
		}
		now := s.now()
		for _, e := range list {
			if e.Active(now) && strings.EqualFold(e.Resource, obj) && (in.Cluster == "" || e.Cluster == in.Cluster) && len(out.Exceptions) < similarMax {
				out.Exceptions = append(out.Exceptions, SimilarException{ID: e.ID, Cluster: e.Cluster, Object: e.Resource, Check: e.Check, Reason: e.Reason, By: e.By, At: e.At})
			}
		}
	}
	return out, nil
}

// ---------- changing a case ----------

// target reads a case for a change: its number, and how it stands now.
func (s *Service) target(ctx context.Context, c Caller, id string) (int, issues.ReportView, error) {
	n, ok := number(id)
	if !ok {
		return 0, issues.ReportView{}, fmt.Errorf("%w: there is no case %q", ErrNotFound, id)
	}
	v, err := s.o.Issues.CaseView(ctx, c.issue(), n)
	if err != nil {
		return 0, issues.ReportView{}, s.fromIssues(err)
	}
	return n, v, nil
}

// apply changes one case in the issue service, as one change by an
// actor (CF-35).
func (s *Service) apply(ctx context.Context, n int, by issues.Actor, fn func(tx *issues.Tx, r *issues.Case) error) (issues.Case, error) {
	r, err := s.o.Issues.Apply(ctx, n, func(tx *issues.Tx, r *issues.Case) error {
		tx.As(by)
		return fn(tx, r)
	})
	if err != nil {
		var d *DuplicateError
		if errors.As(err, &d) {
			return r, err
		}
		return r, s.fromIssues(err)
	}
	return r, nil
}

// Join is "Add to C-n" (CF-12): instead of opening a case like an open
// one, the caller follows it and adds what they would have written.
func (s *Service) Join(ctx context.Context, id, text string, c Caller) (View, error) {
	text = clip(text, MaxDetail)
	if text == "" {
		return View{}, fmt.Errorf("%w: say what you see, for the case", ErrInvalid)
	}
	n, _, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	p := c.issue().Person
	r, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
		if err := tx.Follow(r, p); err != nil {
			return err
		}
		return tx.Note(r, p, text, true)
	})
	if err != nil {
		return View{}, err
	}
	s.note(ctx, n, Event{At: s.now(), What: "Joined: " + clip(text, 200), By: c.actor()})
	s.record(audit.Event{Action: ActionJoin, Who: who(c.Person), Cluster: r.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("case %s joined by %s instead of a new case: follows it, a comment added", caseID(n), c.Label())})
	return s.Get(ctx, c, caseID(n))
}

// Change is what a person changes on a case.
type Change struct {
	Title    *string `json:"title,omitempty"`
	Detail   *string `json:"detail,omitempty"`
	Priority *int    `json:"priority,omitempty"`
	Cluster  *string `json:"cluster,omitempty"`
	Resource *string `json:"resource,omitempty"`
	// Assignee "" leaves it to nobody; "me" gives it to the caller.
	Assignee *issues.Person `json:"assignee,omitempty"`
	Service  *string        `json:"service,omitempty"`
	// Type is one of the types; Incident "" clears it (CF-36, CF-37).
	Type     *string `json:"type,omitempty"`
	Incident *string `json:"incident,omitempty"`
	// Base is what an edit in place started from, for each field it
	// changes (CF-60): a field whose value is no longer that is not
	// changed, and the edit is refused with its value now. Left out,
	// nothing is compared.
	Base *FieldBase `json:"base,omitempty"`
	// Force saves over a newer value (Overwrite, after a refusal); the
	// case's history says so.
	Force bool `json:"force,omitempty"`
}

// FieldBase is the values an edit in place started from (CF-60), as the
// case's view showed them.
type FieldBase struct {
	Title    *string `json:"title,omitempty"`
	Detail   *string `json:"detail,omitempty"`
	Cluster  *string `json:"cluster,omitempty"`
	Resource *string `json:"resource,omitempty"`
	Incident *string `json:"incident,omitempty"`
}

// FieldConflict is an edit refused because the field changed since the
// person started editing it (CF-60): the API answers 409 with its value
// now, and who changed the case last, and when.
type FieldConflict struct {
	Field   string     `json:"field"`
	Current string     `json:"current"`
	By      string     `json:"by,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

func (e *FieldConflict) Error() string {
	return fmt.Sprintf("the %s changed since you started editing it", e.Field)
}

// fieldNames are the issue service's names of the fields an edit in
// place compares, as the cases name them.
var fieldNames = map[string]string{"title": "title", "text": "detail", "cluster": "cluster", "object": "resource", "incident": "incident"}

// Update changes what a case says, its priority (P0 to P4, which starts
// or stops an agent-found case's SLA clock) and who works it, which takes
// a New case in progress (CW-05, CU-03, CF-02).
func (s *Service) Update(ctx context.Context, id string, ch Change, c Caller) (View, error) {
	n, before, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if ch.Cluster != nil {
		if err := s.knownCluster(*ch.Cluster); err != nil {
			return View{}, err
		}
	}
	if ch.Assignee != nil && ch.Assignee.ID == "me" {
		// "me" is the caller, as the issue service names them.
		p := c.issue().Person
		ch.Assignee = &issues.Person{ID: p.ID, Name: p.Name, Email: p.Email}
	}
	if ch.Resource != nil {
		v := normObject(*ch.Resource)
		ch.Resource = &v
	}
	t := issues.Triage{Title: ch.Title, Text: ch.Detail, Priority: ch.Priority, Cluster: ch.Cluster, Object: ch.Resource, Owner: ch.Assignee, Service: ch.Service,
		Type: ch.Type, Incident: ch.Incident}
	// Each field an edit in place changes is compared with what it started
	// from in the same write (CF-60), unless the person saves over it.
	if b := ch.Base; b != nil && !ch.Force {
		t.Expect = &issues.Expect{Title: b.Title, Text: b.Detail, Cluster: b.Cluster, Object: b.Resource, Incident: b.Incident}
	}
	after, err := s.o.Issues.Triage(ctx, c.issue(), n, t)
	var fc *issues.FieldConflict
	if errors.As(err, &fc) {
		out := &FieldConflict{Field: firstOf(fieldNames[fc.Field], fc.Field), Current: fc.Current}
		if now, err := s.Get(ctx, c, caseID(n)); err == nil {
			out.At = now.LastAt
			if now.LastBy != nil {
				out.By = now.LastBy.Name
			}
		}
		return View{}, out
	}
	if err != nil {
		return View{}, s.fromIssues(err)
	}
	var said []string
	if after.Priority != before.Priority {
		said = append(said, fmt.Sprintf("priority P%d → P%d", before.Priority, after.Priority))
		if after.AgentFound() && (after.SLAFrom == nil) != (before.SLAFrom == nil) {
			said = append(said, map[bool]string{true: "its SLA clock started", false: "its SLA clock stopped"}[after.SLAFrom != nil])
		}
	}
	if after.Title != before.Case.Title && ch.Title != nil {
		said = append(said, "title changed")
	}
	if after.Text != before.Text {
		said = append(said, "detail changed")
	}
	if after.Cluster != before.Cluster {
		said = append(said, "cluster "+firstOf(after.Cluster, "none"))
	}
	if after.Object != before.Object {
		said = append(said, "about "+firstOf(after.Object, "nothing named"))
	}
	if personLabel(after.Owner) != personLabel(before.Owner) {
		said = append(said, "assigned to "+firstOf(personLabel(after.Owner), "nobody"))
	}
	if after.Status != before.Status {
		said = append(said, "now "+string(after.Status))
	}
	if after.Service != before.Service {
		said = append(said, "service "+firstOf(after.Service, "unknown"))
	}
	// The type and the incident number have audit actions of their own
	// (CF-36, CF-37).
	var own []string
	if after.CaseType() != before.Case.CaseType() {
		what := fmt.Sprintf("type %s → %s", before.Case.CaseType(), after.CaseType())
		own = append(own, what)
		s.record(audit.Event{Action: ActionType, Who: who(c.Person), Cluster: after.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: %s", caseID(n), what)})
	}
	if after.Incident != before.Case.Incident {
		what := fmt.Sprintf("incident number %s → %s", firstOf(before.Case.Incident, "none"), firstOf(after.Incident, "none"))
		own = append(own, what)
		s.record(audit.Event{Action: ActionIncident, Who: who(c.Person), Cluster: after.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: %s", caseID(n), what)})
	}
	if len(said) > 0 || len(own) > 0 {
		what := upper(strings.Join(append(append([]string{}, said...), own...), ", "))
		if ch.Force {
			what += ", saved over a newer change"
		}
		s.note(ctx, n, Event{At: s.now(), What: what, By: c.actor()})
	}
	if len(said) > 0 {
		s.record(audit.Event{Action: ActionUpdate, Who: who(c.Person), Cluster: after.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: %s", caseID(n), strings.Join(said, ", "))})
	}
	return s.Get(ctx, c, caseID(n))
}

// Message is a reply to a reported case's reporter, an internal note, or
// a question that waits for the reporter's answer (CS-11).
type Message struct {
	Text     string `json:"text"`
	Internal bool   `json:"internal,omitempty"`
	Ask      bool   `json:"ask,omitempty"`
}

// Say adds to a case's conversation.
func (s *Service) Say(ctx context.Context, id string, m Message, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if !m.Internal && v.Kind() != SourceReport {
		return View{}, fmt.Errorf("%w: %s has no reporter to reply to; add an internal note", ErrInvalid, caseID(n))
	}
	before := shownStatus(v.Case)
	after, err := s.o.Issues.AddMessage(ctx, c.issue(), n, m.Text, m.Internal)
	if err != nil {
		return View{}, s.fromIssues(err)
	}
	if m.Ask && !m.Internal {
		ask := true
		if after, err = s.o.Issues.Triage(ctx, c.issue(), n, issues.Triage{AskInfo: &ask}); err != nil {
			return View{}, s.fromIssues(err)
		}
	}
	what := "Reply sent"
	switch {
	case m.Internal:
		what = "Internal note added"
	case m.Ask:
		what = "Question sent"
	}
	// A reply moves a New case In progress, a question to Waiting for the
	// reporter (CF-67): the history says so, as for every status change.
	if now := shownStatus(after); now != before {
		s.note(ctx, n, Event{At: s.now(), What: what + ", now " + now, By: c.actor()}, partReply)
	} else {
		s.noteSent(ctx, n, Event{At: s.now(), What: what, By: c.actor()}, partReply)
	}
	return s.Get(ctx, c, caseID(n))
}

// SendDraft sends a reply drafted by the investigator or another person,
// as it is or edited (IS-14).
func (s *Service) SendDraft(ctx context.Context, id string, msg int, text string, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	after, err := s.o.Issues.Send(ctx, c.issue(), n, msg, text)
	if err != nil {
		return View{}, s.fromIssues(err)
	}
	// Sent by a person, an agent's draft is the person's reply (CF-45,
	// CF-67): a New case goes In progress.
	if now := shownStatus(after); now != shownStatus(v.Case) {
		s.note(ctx, n, Event{At: s.now(), What: "Reply sent, now " + now, By: c.actor()})
	}
	return s.Get(ctx, c, caseID(n))
}

// DiscardDraft drops a drafted reply.
func (s *Service) DiscardDraft(ctx context.Context, id string, msg int, c Caller) (View, error) {
	n, _, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if _, err := s.o.Issues.Discard(ctx, c.issue(), n, msg); err != nil {
		return View{}, s.fromIssues(err)
	}
	return s.Get(ctx, c, caseID(n))
}

// ---------- closing a case (CF-03 to CF-04) ----------

// KnownLink closes a case as a known issue (CF-03): an existing one by
// its id, or one made from the case.
type KnownLink struct {
	ID     string    `json:"id,omitempty"`
	Create *NewKnown `json:"create,omitempty"`
}

// NewKnown is a known issue made from a case.
type NewKnown struct {
	Title      string   `json:"title"`
	Symptoms   []string `json:"symptoms,omitempty"`
	Workaround string   `json:"workaround,omitempty"`
	Services   []string `json:"services,omitempty"`
}

// Known closes a case as a known issue, making it first when asked. An
// agent-found case's key goes to the known issue: its repeats are
// recorded there (CF-06).
func (s *Service) Known(ctx context.Context, id string, in KnownLink, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if !v.Status.Open() {
		return View{}, fmt.Errorf("%w: %s is closed; reopen it first", ErrState, caseID(n))
	}
	kid := strings.TrimSpace(in.ID)
	made := false
	if in.Create != nil {
		title := firstOf(in.Create.Title, v.Case.Title)
		status := issues.KnownIdentified
		src := issues.SourceOperator
		ki := issues.KnownInput{Title: &title, Symptoms: in.Create.Symptoms, Status: &status, Source: &src, Services: in.Create.Services}
		if len(ki.Symptoms) == 0 && v.Case.Title != "" {
			ki.Symptoms = []string{v.Case.Title}
		}
		if w := strings.TrimSpace(in.Create.Workaround); w != "" {
			ki.Workaround = &w
		}
		if len(ki.Services) == 0 && v.Service != "" {
			ki.Services = []string{v.Service}
		}
		k, err := s.o.Issues.SaveKnown(ctx, c.issue(), "", ki)
		if err != nil {
			return View{}, s.fromIssues(err)
		}
		kid, made = k.ID, true
	}
	if kid == "" {
		return View{}, fmt.Errorf("%w: name the known issue, or make one", ErrInvalid)
	}
	p := c.issue().Person
	r, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
		if err := r.MayClose(issues.OutcomeKnownIssue); err != nil {
			return err
		}
		k := tx.Known(kid)
		if k == nil || k.Draft {
			return fmt.Errorf("%w: there is no known issue %s", ErrNotFound, kid)
		}
		tx.LinkKnown(r, k, p)
		return nil
	})
	if err != nil {
		return View{}, err
	}
	what := "Closed as the known issue " + kid
	if made {
		what = "Closed as the known issue " + kid + ", made from the case"
	}
	s.note(ctx, n, Event{At: s.now(), What: what, By: c.actor()}, partClose)
	s.record(audit.Event{Action: ActionKnown, Who: who(c.Person), Cluster: r.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: %s", caseID(n), strings.ToLower(what[:1])+what[1:])})
	// Its troubleshooting ends and its waiting changes are settled (CF-34, CF-47).
	s.closed(ctx, n, c.card())
	return s.Get(ctx, c, caseID(n))
}

// FixInput is a fix: what was done, and its RCA, which a P0-P1 case
// requires (CF-03). The RCA's fix is what was done (CF-44): RCA.Fix is
// left out.
type FixInput struct {
	Text string      `json:"text"`
	RCA  *Completion `json:"rca,omitempty"`
	// SaveFix saves the fix in Mycroft › Fixes (CF-50); absent is on.
	SaveFix *bool `json:"saveFix,omitempty"`
}

// saving says whether a fix is saved: on unless turned off.
func saving(b *bool) bool { return b == nil || *b }

// ErrRCARequired is a fix at P0-P1 without its RCA.
var ErrRCARequired = errors.New("an RCA is required")

// Fix closes a case as fixed (CF-03): what was done, and the RCA, which
// is the case's (CF-44). An agent-found case's fix is verified by the
// next run of its job.
func (s *Service) Fix(ctx context.Context, id string, in FixInput, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	text := clip(in.Text, MaxDetail)
	if text == "" {
		return View{}, fmt.Errorf("%w: say what was done to fix it", ErrInvalid)
	}
	if err := v.Case.MayClose(issues.OutcomeFix); err != nil {
		return View{}, s.fromIssues(err)
	}
	hasRCA := in.RCA != nil && !in.RCA.Empty()
	if v.Case.RCARequired() && !hasRCA {
		return View{}, fmt.Errorf("%w: %w at P%d: give the cause and the prevention", ErrInvalid, ErrRCARequired, v.Priority)
	}
	now := s.now()
	var rca *RCA
	if hasRCA {
		rca = &RCA{Case: caseID(n), Title: firstOf(v.Case.Title, caseID(n)), Cluster: v.Cluster, Resource: v.Object, Priority: v.Priority,
			Cause: clip(in.RCA.Cause, MaxRCAField), Fix: clip(text, MaxRCAField), Prevention: clip(in.RCA.Prevention, MaxRCAField), Text: clip(in.RCA.Text, MaxRCAField), By: c.Label(), At: now}
		if rca.Cause == "" {
			return View{}, fmt.Errorf("%w: an RCA says what the cause was", ErrInvalid)
		}
	}
	p := c.issue().Person
	r, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
		if err := r.MayClose(issues.OutcomeFix); err != nil {
			return err
		}
		// A fix a change applied that a run verified already stays
		// verified (CF-30): no run checks it again.
		verified, at, by := r.Verification == issues.VerifyVerified, r.Verified, r.VerifiedBy
		issues.CloseCase(r, p, issues.OutcomeFix, text, tx.Now())
		if verified {
			r.VerifyPending, r.Verified, r.VerifiedBy = false, at, by
		}
		if rca != nil {
			r.RCACause = clip(rca.Cause, issues.MaxShort)
		}
		return nil
	})
	if err != nil {
		return View{}, err
	}
	what := "Closed as fixed: " + text
	if rca != nil {
		what += " (with its RCA)"
	}
	err = s.updateWork(ctx, n, func(w *Work) error {
		// The change that applied the fix stays named (CF-29).
		change := ""
		if w.FixApplied != nil && r.AppliedBy != nil {
			change = w.FixApplied.Change
		}
		w.FixApplied, w.RCA, w.Updated = &Note{Text: text, By: c.Label(), At: now, Change: change}, rca, now
		// What was done and the RCA leave the draft for everyone (CF-59).
		line := what
		if drafted := clearSent(w, c.actor(), now, partFix, partRCA); drafted != "" {
			line += "; " + drafted
		}
		addEvent(w, Event{At: now, What: line, By: c.actor()})
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("a case's fix could not be noted on its work", "case", caseID(n), "err", err)
	}
	kept := "no RCA"
	if rca != nil {
		kept = "with its RCA"
	}
	verify := ""
	if r.VerifyPending {
		verify = "; the next run of " + r.Job + " verifies it"
	}
	s.record(audit.Event{Action: ActionFix, Who: who(c.Person), Cluster: r.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s closed as fixed, %s%s", caseID(n), kept, verify)})
	// Its troubleshooting ends and its waiting changes are settled (CF-34, CF-47).
	s.closed(ctx, n, c.card())
	if saving(in.SaveFix) {
		s.saveFix(ctx, []int{n}, text, in.RCA, fixPerson(c))
	}
	return s.Get(ctx, c, caseID(n))
}

// CloseInput closes a case as a duplicate or no action needed, with an
// explanation, always; a duplicate names its original. notAProblem and
// noFixNeeded, the outcomes before CF-90, read as noAction.
type CloseInput struct {
	Outcome     issues.Outcome `json:"outcome"`
	Explanation string         `json:"explanation"`
	Original    string         `json:"original,omitempty"`
}

// DuplicateError is a duplicate whose original does not allow it, with
// what fits instead (CF-04): the API answers 409 with the check.
type DuplicateError struct{ Check issues.DuplicateCheck }

func (e *DuplicateError) Error() string { return "not a duplicate: " + e.Check.Reason }

// Close closes a case with an outcome that is only words (CF-03):
// duplicate or noAction (notAProblem and noFixNeeded too, as noAction:
// CF-90). A duplicate's original is the
// end of its chain, which must be open; its reporter follows it from
// now on, and its history says so.
func (s *Service) Close(ctx context.Context, id string, in CloseInput, c Caller) (View, error) {
	explanation := clip(in.Explanation, MaxDetail)
	in.Outcome = in.Outcome.Canonical()
	switch in.Outcome {
	case issues.OutcomeDuplicate, issues.OutcomeNoAction:
	default:
		return View{}, fmt.Errorf("%w: close with duplicate or noAction here; a fix, a known issue and an exception have their own actions", ErrInvalid)
	}
	n, _, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if explanation == "" {
		return View{}, fmt.Errorf("%w: explain why %s is closed", ErrInvalid, caseID(n))
	}
	original := 0
	if in.Outcome == issues.OutcomeDuplicate {
		var ok bool
		if original, ok = number(in.Original); !ok {
			return View{}, fmt.Errorf("%w: name the case it repeats, C-<n>", ErrInvalid)
		}
	}
	p := c.issue().Person
	end, agent := 0, false
	r, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
		if err := r.MayClose(in.Outcome); err != nil {
			return err
		}
		agent = r.AgentFound()
		if in.Outcome != issues.OutcomeDuplicate {
			issues.CloseCase(r, p, in.Outcome, explanation, tx.Now())
			return nil
		}
		chk := tx.DuplicateCheck(r, original)
		if !chk.Allowed {
			return &DuplicateError{Check: chk}
		}
		end = chk.End()
		tx.CloseDuplicate(r, end, p, explanation)
		return nil
	})
	if err != nil {
		return View{}, err
	}
	what := fmt.Sprintf("Closed as %s: %s", outcomeWords(in.Outcome, ""), explanation)
	if end != 0 {
		what = fmt.Sprintf("Closed as a duplicate of %s: %s", caseID(end), explanation)
		s.note(ctx, end, Event{At: s.now(), What: fmt.Sprintf("%s (%s) was closed as a duplicate of this case; its reporter follows it here", caseID(n), clip(r.Title, 120)), By: c.actor()})
		if agent {
			_ = s.updateRecord(ctx, func(rec *record) error { rec.Duplicates++; return nil })
		}
	}
	s.note(ctx, n, Event{At: s.now(), What: what, By: c.actor()}, partClose)
	s.record(audit.Event{Action: ActionClose, Who: who(c.Person), Cluster: r.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s closed, %s%s", caseID(n), in.Outcome, prefixed(" of ", idOf(end)))})
	// Its troubleshooting ends and its waiting changes are settled (CF-34, CF-47).
	s.closed(ctx, n, c.card())
	return s.Get(ctx, c, caseID(n))
}

// DuplicateCheck says whether a case may close as a duplicate of an
// original: the UI asks when a person picks the original, and Close
// checks again (CF-04).
func (s *Service) DuplicateCheck(ctx context.Context, id, original string, c Caller) (issues.DuplicateCheck, error) {
	n, _, err := s.target(ctx, c, id)
	if err != nil {
		return issues.DuplicateCheck{}, err
	}
	o, ok := number(original)
	if !ok {
		return issues.DuplicateCheck{}, fmt.Errorf("%w: name the original, C-<n>", ErrInvalid)
	}
	chk, err := s.o.Issues.DuplicateCheck(ctx, n, o)
	if err != nil {
		return chk, s.fromIssues(err)
	}
	return chk, nil
}

// ExceptionInput accepts an agent-found case as an exception: why, and
// the last day it applies ("YYYY-MM-DD"); none is permanent.
type ExceptionInput struct {
	Reason  string `json:"reason"`
	Expires string `json:"expires,omitempty"`
}

// Exception closes an agent-found case as an accepted exception (CF-03):
// the exception covers its cluster, object and check, so agents stop
// reporting it; only those who may approve proposed changes accept one.
func (s *Service) Exception(ctx context.Context, id string, in ExceptionInput, c Caller) (View, error) {
	if !c.Approve {
		return View{}, fmt.Errorf("%w: only those who may approve proposed changes (cases.approverRoles) accept an exception", ErrDenied)
	}
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if err := v.Case.MayClose(issues.OutcomeException); err != nil {
		return View{}, s.fromIssues(err)
	}
	reason := clip(in.Reason, 1000)
	if reason == "" {
		return View{}, fmt.Errorf("%w: say why it is accepted", ErrInvalid)
	}
	var until time.Time
	if in.Expires != "" {
		d, err := time.Parse(time.DateOnly, in.Expires)
		if err != nil {
			return View{}, fmt.Errorf("%w: the last day is YYYY-MM-DD", ErrInvalid)
		}
		until = d.Add(24*time.Hour - time.Second)
	}
	k := v.Case.Key
	if k == nil {
		return View{}, fmt.Errorf("%w: %s has no key to except", ErrInvalid, caseID(n))
	}
	e, err := s.o.Lessons.AddException(ctx, lessons.NewException{Cluster: k.Cluster, Resource: k.Object, Check: k.Check, Title: v.Case.Title, Detail: v.Text,
		Finding: v.Case.Finding, Run: v.Case.Run, Job: v.Case.Job, Reason: reason, Expires: until, Case: caseID(n)}, lessons.Person(c.Person))
	if err != nil {
		if errors.Is(err, lessons.ErrInvalid) {
			return View{}, fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), lessons.ErrInvalid.Error()+": "))
		}
		return View{}, err
	}
	p := c.issue().Person
	r, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
		if err := r.MayClose(issues.OutcomeException); err != nil {
			return err
		}
		issues.CloseCase(r, p, issues.OutcomeException, reason, tx.Now())
		r.Exception = e.ID
		return nil
	})
	if err != nil {
		_, _ = s.o.Lessons.RemoveException(ctx, e.ID, lessons.Person(c.Person))
		return View{}, err
	}
	until2 := "permanent"
	if !until.IsZero() {
		until2 = "until " + in.Expires
	}
	s.note(ctx, n, Event{At: s.now(), What: fmt.Sprintf("Accepted as the exception %s, %s: %s", e.ID, until2, reason), By: c.actor()}, partClose)
	s.record(audit.Event{Action: ActionException, Who: who(c.Person), Cluster: r.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("case %s closed as the exception %s (%s), %s", caseID(n), e.ID, k, until2)})
	// Its troubleshooting ends and its waiting changes are settled (CF-34, CF-47).
	s.closed(ctx, n, c.card())
	return s.Get(ctx, c, caseID(n))
}

// Reopen opens a closed case again, with a reason (CF-02): its history
// stays, and an RCA stays in Mycroft. One closed as an exception loses
// it, which only those who may approve proposed changes do.
func (s *Service) Reopen(ctx context.Context, id, reason string, c Caller) (View, error) {
	reason = clip(reason, 500)
	if reason == "" {
		return View{}, fmt.Errorf("%w: say why it is reopened", ErrInvalid)
	}
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if v.Status.Open() {
		return View{}, fmt.Errorf("%w: %s is still open", ErrState, caseID(n))
	}
	ex := ""
	if v.Outcome == issues.OutcomeException && v.Case.Exception != "" {
		if !c.Approve {
			return View{}, fmt.Errorf("%w: %s is the exception %s; only those who may approve proposed changes remove it", ErrDenied, caseID(n), v.Case.Exception)
		}
		ex = v.Case.Exception
	}
	p := c.issue().Person
	r, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
		if r.Status.Open() {
			return fmt.Errorf("%w: %s is still open", issues.ErrInvalid, caseID(n))
		}
		issues.ReopenCase(r, tx.Now())
		return tx.Note(r, p, "Reopened: "+reason, true)
	})
	if err != nil {
		return View{}, err
	}
	if ex != "" {
		if _, err := s.o.Lessons.RemoveException(ctx, ex, lessons.Person(c.Person)); err != nil && !errors.Is(err, lessons.ErrInvalid) {
			s.o.Logger.Warn("the exception of a reopened case could not be removed", "case", caseID(n), "exception", ex, "err", err)
		}
	}
	what := "Reopened: " + reason
	if ex != "" {
		what += "; the exception " + ex + " is removed"
	}
	s.note(ctx, n, Event{At: s.now(), What: what, By: c.actor()})
	s.record(audit.Event{Action: ActionReopen, Who: who(c.Person), Cluster: r.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s reopened (was %s)%s", caseID(n), v.Outcome, prefixed("; exception removed: ", ex))})
	return s.Get(ctx, c, caseID(n))
}

// Offer applies what a duplicate check offered instead (CF-04). The
// server checks again that the original's state still offers it.
func (s *Service) Offer(ctx context.Context, id string, in issues.Offer, c Caller) (View, error) {
	n, _, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	o, ok := number(in.Original)
	if !ok {
		return View{}, fmt.Errorf("%w: name the original, C-<n>", ErrInvalid)
	}
	chk, err := s.o.Issues.DuplicateCheck(ctx, n, o)
	if err != nil {
		return View{}, s.fromIssues(err)
	}
	if in.Kind == "notAProblem" {
		// The offer's name before CF-90.
		in.Kind = issues.OfferNoAction
	}
	i := slices.IndexFunc(chk.Offers, func(x issues.Offer) bool { return x.Kind == in.Kind })
	if i < 0 {
		return View{}, &DuplicateError{Check: chk}
	}
	offer := chk.Offers[i]
	end := chk.End()
	p := c.issue().Person
	var what string
	switch offer.Kind {
	case issues.OfferRemoveException:
		if !c.Approve {
			return View{}, fmt.Errorf("%w: only those who may approve proposed changes remove an exception", ErrDenied)
		}
		if _, err := s.o.Lessons.RemoveException(ctx, offer.Exception, lessons.Person(c.Person)); err != nil && !errors.Is(err, lessons.ErrInvalid) {
			return View{}, err
		}
		what = fmt.Sprintf("The exception %s of %s removed; this case stays open", offer.Exception, caseID(end))
		if _, err := s.apply(ctx, n, c.actor(), func(tx *issues.Tx, r *issues.Case) error {
			r.SameAs = end
			return tx.Note(r, p, what, true)
		}); err != nil {
			return View{}, err
		}
	default:
		err = s.o.Issues.Batch(ctx, func(tx *issues.Tx) error {
			tx.As(c.actor())
			r, err := tx.Case(n)
			if err != nil {
				return err
			}
			if !r.Status.Open() {
				return fmt.Errorf("%w: %s is closed", issues.ErrInvalid, caseID(n))
			}
			switch offer.Kind {
			case issues.OfferReopenOriginal:
				orig, err := tx.Case(end)
				if err != nil {
					return err
				}
				was := orig.Outcome
				issues.ReopenCase(orig, tx.Now())
				why := fmt.Sprintf("Reopened: %s reported the same problem", caseID(n))
				if was == issues.OutcomeFix {
					why += "; the fix did not hold"
				}
				if err := tx.Note(orig, p, why, true); err != nil {
					return err
				}
				tx.CloseDuplicate(r, end, p, fmt.Sprintf("The same problem as %s, reopened for it.", caseID(end)))
				what = fmt.Sprintf("%s reopened, and this case closed as its duplicate", caseID(end))
			case issues.OfferRegression:
				r.RegressionOf = end
				what = "Marked a regression of " + caseID(end)
			case issues.OfferSameKnownIssue:
				k := tx.Known(offer.KnownIssue)
				if k == nil {
					return fmt.Errorf("%w: there is no known issue %s", issues.ErrNotFound, offer.KnownIssue)
				}
				tx.LinkKnown(r, k, p)
				what = "Closed as the known issue " + k.ID + ", as " + caseID(end) + " was"
			case issues.OfferNoAction:
				issues.CloseCase(r, p, issues.OutcomeNoAction, firstOf(offer.Explanation, "As "+caseID(end)+": no action needed."), tx.Now())
				what = "Closed as no action needed, as " + caseID(end) + " explains"
			case issues.OfferSameAs:
				r.SameAs = end
				what = "Linked: the same as " + caseID(end)
			}
			r.Updated = tx.Now()
			return nil
		})
		if err != nil {
			return View{}, s.fromIssues(err)
		}
	}
	s.note(ctx, n, Event{At: s.now(), What: what, By: c.actor()})
	if offer.Kind == issues.OfferReopenOriginal {
		s.note(ctx, end, Event{At: s.now(), What: fmt.Sprintf("Reopened: %s reported the same problem", caseID(n)), By: c.actor()})
	}
	s.record(audit.Event{Action: ActionOffer, Who: who(c.Person), Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: %s (%s of %s)", caseID(n), strings.ToLower(what[:1])+what[1:], offer.Kind, caseID(end))})
	// Its troubleshooting ends and its waiting changes are settled (CF-34, CF-47).
	s.closed(ctx, n, c.card())
	return s.Get(ctx, c, caseID(n))
}

// ---------- the analysis ----------

// Analyze runs the case's analysis again (CW-04): on an open case that
// names its cluster, while the job is on.
func (s *Service) Analyze(ctx context.Context, id string, c Caller) (View, error) {
	n, v, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	if !v.Status.Open() {
		return View{}, fmt.Errorf("%w: %s is closed", ErrState, caseID(n))
	}
	if v.Cluster == "" {
		return View{}, fmt.Errorf("%w: name the cluster %s is about first", ErrInvalid, caseID(n))
	}
	cur, err := s.Get(ctx, c, caseID(n))
	if err != nil {
		return View{}, err
	}
	if cur.Analyzing {
		return View{}, fmt.Errorf("%w: %s is being analyzed", ErrState, caseID(n))
	}
	info := infoOf(cur)
	if s.o.Jobs == nil || !s.o.Jobs.On(ctx, jobs.KindCase, s.event(info, false)) {
		return View{}, fmt.Errorf("%w: turn the case analysis job on in AIOps › Jobs (it must cover %s)", ErrOff, v.Cluster)
	}
	s.note(ctx, n, Event{At: s.now(), What: "Asked the case analysis job to analyze it again", By: c.actor()})
	s.record(audit.Event{Action: ActionAnalyze, Who: who(c.Person), Cluster: v.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("case %s: its analysis started again", caseID(n))})
	s.startAnalysis(ctx, n, c.actor())
	out, err := s.Get(ctx, c, caseID(n))
	s.launch(info)
	return out, err
}

// startAnalysis notes an analysis under way. The case leaves the queue
// (CF-32): a person asked, or its turn came. Its status stays as it is: a
// case is in progress only once a person works on it (CF-45).
func (s *Service) startAnalysis(ctx context.Context, n int, by issues.Actor) {
	now := s.now()
	_ = s.updateRecord(ctx, func(rec *record) error {
		if rec.Analyzing == nil {
			rec.Analyzing = map[string]time.Time{}
		}
		rec.Analyzing[fmt.Sprint(n)] = now
		return nil
	})
	if _, err := s.o.Issues.Apply(ctx, n, func(tx *issues.Tx, r *issues.Case) error {
		tx.As(by)
		r.AnalysisQueued = nil
		return nil
	}); err != nil {
		s.o.Logger.Warn("a case could not leave the analysis queue", "case", caseID(n), "err", err)
	}
}

// ---------- the SLA (CU-06, CF-02) ----------

// SLA is the response and resolution targets per priority, "0" to "4",
// for every case with an SLA clock.
func (s *Service) SLA(ctx context.Context) (map[string]issues.Target, error) {
	set, err := s.o.Issues.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return set.Targets, nil
}

// SaveSLA sets the targets; a SuperAdmin's.
func (s *Service) SaveSLA(ctx context.Context, targets map[string]issues.Target, c Caller) (map[string]issues.Target, error) {
	if !c.Admin {
		return nil, fmt.Errorf("%w: only a SuperAdmin sets the SLA", ErrDenied)
	}
	set, err := s.o.Issues.Settings(ctx)
	if err != nil {
		return nil, err
	}
	set.Targets = targets
	out, err := s.o.Issues.SaveSettings(ctx, c.issue(), set)
	if err != nil {
		return nil, s.fromIssues(err)
	}
	return out.Targets, nil
}

// ---------- Settings › Cases (CF-19) ----------

// Settings are Settings › Cases, with the counter.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	rec, err := s.loadRecord(ctx)
	if err != nil {
		return Settings{}, err
	}
	out := rec.Settings
	out.DuplicatesOfAgentCases = rec.Duplicates
	return out, nil
}

// SaveSettings changes Settings › Cases; a SuperAdmin's.
func (s *Service) SaveSettings(ctx context.Context, in Settings, c Caller) (Settings, error) {
	if !c.Admin {
		return Settings{}, fmt.Errorf("%w: only a SuperAdmin changes Settings › Cases", ErrDenied)
	}
	in.DuplicatesOfAgentCases = 0
	if err := in.Validate(); err != nil {
		return Settings{}, err
	}
	var before Settings
	err := s.updateRecord(ctx, func(rec *record) error {
		before = rec.Settings
		// Settings sent without the safety net's minutes keep them (CF-78).
		if in.CriticalAlertMinutes == nil {
			in.CriticalAlertMinutes = before.CriticalAlertMinutes
		}
		rec.Settings = in
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	s.record(audit.Event{Action: ActionSettings, Who: who(c.Person), Target: "cases settings", From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("Settings › Cases: keep closed agent-found cases %d → %d days, look back %d → %d days, exceptions stale after %d → %d days, %d → %d exceptions per agent, %d → %d new cases per run, a case for a critical alert after %s → %s",
			before.KeepAgentClosedDays, in.KeepAgentClosedDays, before.LookBackDays, in.LookBackDays, before.ExceptionStaleDays, in.ExceptionStaleDays, before.ExceptionsPerAgent, in.ExceptionsPerAgent, before.NewCasesPerRun, in.NewCasesPerRun,
			afterWords(before.AlertAfter()), afterWords(in.AlertAfter()))})
	return s.Settings(ctx)
}

// afterWords says the safety net's minutes (CF-78).
func afterWords(d time.Duration) string {
	if d == 0 {
		return "never"
	}
	return fmt.Sprintf("%d minutes", int(d/time.Minute))
}

// StaleDays are the days after which an exception without a match is
// stale, for package lessons.
func (s *Service) StaleDays() int { return s.settings(context.Background()).ExceptionStaleDays }

// KnownOpen are the known issues a case may be closed as: accepted, not
// resolved.
func (s *Service) KnownOpen(ctx context.Context, c Caller) ([]issues.KnownIssue, error) {
	list, err := s.o.Issues.Known(ctx, c.issue(), false)
	if list == nil {
		list = []issues.KnownIssue{}
	}
	return list, err
}

// bracketed is " (s)", or nothing when s is empty.
func bracketed(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}
