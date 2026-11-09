package casework

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
)

// Cards are the Watson cards, read for the live state of the changes
// that resolve a case, and linked to a case by a person (CF-28): the
// cards' store.
type Cards interface {
	Get(ctx context.Context, id string) (cards.Card, error)
	List(ctx context.Context, f cards.Filter) ([]cards.Card, error)
	Link(ctx context.Context, id, ref string, by cards.Person) (cards.Card, error)
	Unlink(ctx context.Context, id, ref string, by cards.Person) (cards.Card, error)
	// Reject rejects a waiting card whose case closed (CF-34).
	Reject(ctx context.Context, id string, by cards.Person, reason string) (cards.Card, error)
}

// Options configure the service.
type Options struct {
	// Backend keeps Settings › Cases, the checks seen and the analyses
	// under way: the casework ConfigMap; nil is memory.
	Backend kv.Backend
	// WorkBytes is the most the work on one case holds in its object,
	// DefaultWorkBytes; ChatBytes the most its Sherlock conversation
	// holds there, DefaultChatBytes (wave 6f, CF-47).
	WorkBytes int
	ChatBytes int
	// Issues keeps the cases themselves (CU-01, CF-20); nil is an issue
	// service of its own in memory, for tests.
	Issues *issues.Service
	// Jobs runs the case analysis job; nil analyses nothing.
	Jobs Runner
	// Lessons keeps the exceptions (Mycroft › Exceptions); nil is a
	// keeper of its own in memory.
	Lessons *lessons.Service
	// Fixes keeps the fixes (Mycroft › Fixes, CF-50); nil saves none.
	Fixes *fixes.Service
	// Cards reads the proposed changes' state; nil shows their ids only.
	Cards Cards
	// Clusters lists the clusters a case may be on; nil takes any id.
	Clusters func() []jobs.Cluster
	// CommandOK says whether Watson runs a command line the agent
	// suggests.
	CommandOK func(line string) error
	// Leads says whether this replica is the leader (ADR-70); only the
	// leader lets go of analyses a replica left behind and runs the
	// retention. nil: always.
	Leads  func() bool
	Audit  *audit.Log
	Now    func() time.Time
	Logger *slog.Logger
	// Every is how often the leader looks for analyses left behind and
	// whether the day's retention is due; 30 s.
	Every time.Duration
}

// Service keeps the cases' work and routes the runs' findings.
type Service struct {
	o Options

	mu sync.Mutex
	// active are the cases whose analysis runs on this replica.
	active map[int]bool
	// caught is when this replica, leading, last caught up with the
	// cards (CF-29); zero until it has, which reads them all once.
	caught time.Time
	// queueRun is the case whose analysis the queue started here and
	// that still runs (CF-32); 0 for none.
	queueRun int
	// forget drops a closed case's conversation from Sherlock's memory
	// (CF-47); nil when there is no Sherlock.
	forget func(session string) int
	// net is the leader's memory of the critical alerts, for the safety
	// net (CF-78).
	net alertNet
	// rules keeps the validation rules (ADR-138).
	rules  *Rules
	wg     sync.WaitGroup
	base   context.Context
	cancel context.CancelFunc
}

// New builds the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.WorkBytes <= 0 {
		o.WorkBytes = DefaultWorkBytes
	}
	if o.ChatBytes <= 0 {
		o.ChatBytes = DefaultChatBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Issues == nil {
		o.Issues = issues.New(issues.Options{Now: o.Now, Logger: o.Logger})
	}
	if o.Lessons == nil {
		o.Lessons = lessons.New(lessons.Options{Now: o.Now, Logger: o.Logger})
	}
	if o.Every <= 0 {
		o.Every = 30 * time.Second
	}
	base, cancel := context.WithCancel(context.Background())
	s := &Service{o: o, active: map[int]bool{}, base: base, cancel: cancel}
	s.rules = &Rules{s: s}
	// A case the issue service closes itself settles its waiting changes
	// (CF-34).
	o.Issues.OnChange(s.issueChanged)
	return s
}

// SetLessons gives the service where the exceptions go.
func (s *Service) SetLessons(l *lessons.Service) {
	if l != nil {
		s.o.Lessons = l
	}
}

// SetFixes gives the service the fixes (wave 6g, CF-50).
func (s *Service) SetFixes(f *fixes.Service) {
	if f != nil {
		s.o.Fixes = f
	}
}

// SetCards gives the service the cards' store.
func (s *Service) SetCards(c Cards) { s.o.Cards = c }

// Issues is the issue service the cases are kept in.
func (s *Service) Issues() *issues.Service { return s.o.Issues }

// Lessons is the keeper of the exceptions.
func (s *Service) Lessons() *lessons.Service { return s.o.Lessons }

// Backend is the storage, for the metrics.
func (s *Service) Backend() kv.Backend { return s.o.Backend }

// cases are the objects the cases are kept in.
func (s *Service) cases() kv.Objects { return s.o.Issues.Cases() }

// Wait waits for the analyses under way.
func (s *Service) Wait() { s.wg.Wait() }

// Stop stops the analyses under way and waits for them.
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) now() time.Time { return s.o.Now().UTC() }

func (s *Service) leads() bool { return s.o.Leads == nil || s.o.Leads() }

func (s *Service) record(e audit.Event) {
	if s.o.Audit != nil {
		s.o.Audit.Record(e)
	}
}

func who(p Person) audit.Who { return audit.Who{Subject: p.Subject, Email: p.Email, Name: p.Name} }

// ClusterList is the clusters a case may be on.
func (s *Service) ClusterList() []jobs.Cluster {
	if s.o.Clusters == nil {
		return []jobs.Cluster{{ID: "local", Name: "local"}}
	}
	return s.o.Clusters()
}

// Caller is who a request is from.
type Caller struct {
	Person
	// ID is how the issue service names the person: their user name.
	ID string
	// Approver works the cases: may approve proposed changes (a
	// SuperAdmin, or a role in cases.approverRoles) or triage reports
	// (issues.operatorRoles). Approve says they may approve proposed
	// changes, which accepting or removing an exception asks (CF-03).
	// Read says they may see the cases (a console-wide role, or one who
	// may edit jobs). Admin (a SuperAdmin) sets the SLA and Settings ›
	// Cases.
	Approver bool
	Approve  bool
	Read     bool
	Admin    bool
}

// issue is the caller as the issue service needs them: the cases are the
// people who work them.
func (c Caller) issue() issues.Caller {
	id := firstOf(c.ID, c.Subject, c.Email, c.Name, "anonymous")
	return issues.Caller{Person: issues.Person{ID: id, Name: c.Name, Email: c.Email}, Report: true, Operator: true, Admin: c.Admin, From: c.From}
}

// key is whose a draft is.
func (c Caller) key() string { return c.issue().ID }

// actor is the caller as a case's history and its last change name them
// (CF-35).
func (c Caller) actor() issues.Actor {
	return issues.Actor{Name: firstOf(c.Name, c.Label()), Kind: issues.ActorPerson}
}

// card is the caller as a card names them.
func (c Caller) card() cards.Person {
	return cards.Person{Key: c.key(), Name: firstOf(c.Name, c.Label())}
}

// system is the console when it changes a case itself: the case
// analysis, the routing of findings, the retention.
var system = issues.Caller{Person: issues.Person{ID: "system:cases", Name: "Cases", System: true}, Operator: true}

// ---------- reading ----------

// Filter narrows the cases listed.
type Filter struct {
	// Status is new, inProgress or closed; Open keeps those not closed,
	// Pending those waiting for the people who work the cases.
	Status  string
	Outcome string
	Open    bool
	Pending bool
	Cluster string
	// Priority is 0 to 4; nil is any.
	Priority *int
	// Source keeps one kind: report, finding, manual, alert; Agents those
	// an agent found, People the others (CF-17).
	Source string
	Agents bool
	People bool
	// Owner keeps those a person (their issue-service id) owns;
	// Unassigned those nobody owns.
	Owner      string
	Unassigned bool
	// Type keeps one type of case (CF-37).
	Type string
	// Q is words in the title, object, check, incident number or id.
	Q string
}

// Object is a Kubernetes object a case is about, read from its words
// when they name one ("namespace/kind/name" or "kind/name"), so the
// console links it (CU-03).
type Object struct {
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// Late says which of the SLA's targets a case has passed (CU-06).
type Late struct {
	Response bool `json:"response,omitempty"`
	Resolve  bool `json:"resolve,omitempty"`
}

// CaseRef names another case.
type CaseRef struct {
	ID      string         `json:"id"`
	Number  int            `json:"number"`
	Title   string         `json:"title"`
	Status  issues.Status  `json:"status"`
	Outcome issues.Outcome `json:"outcome,omitempty"`
	Source  string         `json:"source,omitempty"`
}

// PreviousClose is how an earlier case of the same problem was closed.
type PreviousClose struct {
	Case        string         `json:"case"`
	Outcome     issues.Outcome `json:"outcome"`
	Explanation string         `json:"explanation"`
	By          string         `json:"by"`
	At          time.Time      `json:"at"`
}

// Brief is a case as a list row shows it.
type Brief struct {
	ID       string         `json:"id"`
	Number   int            `json:"number"`
	Title    string         `json:"title"`
	Priority int            `json:"priority"`
	Status   issues.Status  `json:"status"`
	Outcome  issues.Outcome `json:"outcome,omitempty"`
	// Pending says it waits for the people who work the cases: New or In
	// progress.
	Pending bool   `json:"pending"`
	Cluster string `json:"cluster,omitempty"`
	// ClusterName is its cluster's name (Settings › Clusters), and
	// ClusterConnected false when the console no longer has that cluster
	// (CF-60); both only for a case on a cluster.
	ClusterName      string `json:"clusterName,omitempty"`
	ClusterConnected *bool  `json:"clusterConnected,omitempty"`
	// Resource is what it is about, in any words; Object the Kubernetes
	// object those name, when they do.
	Resource string  `json:"resource,omitempty"`
	Object   *Object `json:"object,omitempty"`
	Source   Source  `json:"source"`
	// AgentFound is a case opened from a run's finding, with its key and
	// how often runs found it again.
	AgentFound bool         `json:"agentFound"`
	Key        *issues.Key  `json:"key,omitempty"`
	Seen       *issues.Seen `json:"seen,omitempty"`
	// CreatedBy is who opened it: its reporter, the person who opened it
	// in the console, or the agent that found it; LastBy and LastAt who
	// changed it last, and when (CF-35).
	CreatedBy *issues.Actor `json:"createdBy,omitempty"`
	LastBy    *issues.Actor `json:"lastBy,omitempty"`
	LastAt    *time.Time    `json:"lastAt,omitempty"`
	Created   time.Time     `json:"created"`
	Updated   time.Time     `json:"updated"`
	// Type is what kind of problem it is; Incident another system's
	// number for it (CF-36, CF-37).
	Type     string `json:"type"`
	Incident string `json:"incident,omitempty"`
	// AnalysisState is "queued" while an agent-found case waits for the
	// case analysis job (CF-32), and AnalysisQueued says the same: a
	// case's whole view keeps "analysis" for the analysis itself.
	AnalysisState  string `json:"analysis,omitempty"`
	AnalysisQueued bool   `json:"analysisQueued,omitempty"`
	// Since is when it took its status; WaitingSince, for a pending case,
	// since when it has waited.
	Since        time.Time      `json:"since"`
	WaitingSince *time.Time     `json:"waitingSince,omitempty"`
	Assignee     *issues.Person `json:"assignee,omitempty"`
	Late         *Late          `json:"late,omitempty"`
	// SLAApplies says it has an SLA clock (CF-02): "no SLA" otherwise.
	SLAApplies bool   `json:"slaApplies"`
	Service    string `json:"service,omitempty"`
	Analyzing  bool   `json:"analyzing,omitempty"`
	// FixProposed says an analysis suggested a fix or proposed changes.
	FixProposed bool `json:"fixProposed,omitempty"`
	// The links to other cases, by id ("C-12").
	RegressionOf        string `json:"regressionOf,omitempty"`
	PossibleDuplicateOf string `json:"possibleDuplicateOf,omitempty"`
	SameAs              string `json:"sameAs,omitempty"`
	DuplicateOf         string `json:"duplicateOf,omitempty"`
	// KnownIssue and Exception are the ids of what it was closed as; RCA
	// the id of a fix's RCA from before wave 6e, RCACause the cause its
	// RCA gives (CF-44); Explanation the close's explanation.
	KnownIssue  string `json:"knownIssue,omitempty"`
	Exception   string `json:"exception,omitempty"`
	RCA         string `json:"rcaId,omitempty"`
	RCACause    string `json:"rcaCause,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	// Previous is how the case it repeats was closed (CF-01).
	Previous *PreviousClose `json:"previous,omitempty"`
	ClosedAt *time.Time     `json:"closedAt,omitempty"`
	// Verified is when a later run verified the fix (CF-06).
	Verified *time.Time `json:"verified,omitempty"`
	// SuggestException says the same check was closed as no fix needed
	// three times on its cluster (CF-13).
	SuggestException bool `json:"suggestException,omitempty"`
	AskedReporter    bool `json:"askedReporter,omitempty"`
	// Verification is how the run after a change applied saw it
	// (CF-31): waiting, verified (P0-P1, add the RCA) or notHeld.
	Verification string `json:"verification,omitempty"`
}

// ParseObject reads a Kubernetes object from what a case is about:
// "namespace/kind/name" or "kind/name", without spaces; nil otherwise.
func ParseObject(text string) *Object {
	if text == "" || strings.ContainsAny(text, " \t\n") || strings.HasPrefix(text, "/") {
		return nil
	}
	parts := strings.Split(text, "/")
	if len(parts) < 2 || len(parts) > 3 || slices.Contains(parts, "") {
		return nil
	}
	if len(parts) == 2 {
		return &Object{Kind: parts[0], Name: parts[1]}
	}
	return &Object{Namespace: parts[0], Kind: parts[1], Name: parts[2]}
}

// personLabel is how a person is named in a row.
func personLabel(p *issues.Person) string {
	if p == nil {
		return ""
	}
	return firstOf(p.Name, p.ID)
}

// lateOf is how a case stands against its SLA.
func lateOf(v issues.ReportView) *Late {
	if v.SLA == nil {
		return nil
	}
	l := Late{Response: v.SLA.ResponseLate && v.FirstResponse == nil, Resolve: v.SLA.ResolveLate && v.Status.Open() && v.Resolved == nil}
	if !l.Response && !l.Resolve {
		return nil
	}
	return &l
}

// noFixCounts counts the agent-found cases closed as no fix needed by
// cluster and check, for "suggest an exception".
type noFixCounts map[string]int

func countNoFix(lines []issues.Line) noFixCounts {
	out := noFixCounts{}
	for _, l := range lines {
		if l.AgentFound && l.Key != nil && l.Outcome == issues.OutcomeNoFixNeeded {
			out[l.Key.Cluster+" "+l.Key.Check]++
		}
	}
	return out
}

// suggest says an open agent-found case's check was closed as no fix
// needed three times on its cluster (CF-13).
func (c noFixCounts) suggest(r *issues.Report) bool {
	return r.AgentFound() && r.Status.Open() && r.Key != nil && c[r.Key.Cluster+" "+r.Key.Check] >= 3
}

// shownStatus is a case's status as people see it (CF-67): an open case
// that waits for its reporter's answer is "Waiting for the reporter",
// though it is kept In progress.
func shownStatus(r issues.Report) string {
	switch {
	case r.Waiting != nil && r.Status.Open():
		return "Waiting for the reporter"
	case r.Status == issues.StatusNew:
		return "New"
	case r.Status == issues.StatusInProgress:
		return "In progress"
	}
	return "Closed"
}

func brief(v issues.ReportView, analyzing bool, nofix noFixCounts) Brief {
	r := v.Report
	b := Brief{ID: caseID(r.Number), Number: r.Number, Title: firstOf(v.Title, "Case "+caseID(r.Number)), Priority: r.Priority, Status: r.Status, Outcome: r.Outcome,
		Pending: r.Pending(), Cluster: r.Cluster, Resource: r.Object, Object: ParseObject(r.Object), Source: Source{Kind: r.Kind()},
		AgentFound: r.AgentFound(), Key: r.Key, Seen: r.Seen, Created: r.Created, Updated: r.Updated, Since: r.StatusAt, Assignee: r.Owner, Late: lateOf(v),
		SLAApplies: r.SLAFrom != nil, Service: r.Service, Analyzing: analyzing, FixProposed: r.FixProposed,
		RegressionOf: idOf(r.RegressionOf), PossibleDuplicateOf: idOf(r.PossibleDuplicateOf), SameAs: idOf(r.SameAs), DuplicateOf: idOf(r.DuplicateOf),
		KnownIssue: r.KnownIssue, Exception: r.Exception, RCA: r.RCA, RCACause: r.RCACause, Explanation: r.Explanation, ClosedAt: r.Closed, Verified: r.Verified,
		SuggestException: nofix.suggest(&r), AskedReporter: r.Waiting != nil && r.Status.Open(), Verification: r.Verification}
	if r.AgentFound() {
		b.Source = Source{Kind: SourceFinding, Finding: r.Finding, Run: r.Run, Job: r.Job, Severity: r.Severity}
		if r.Key != nil {
			b.Source.Check = r.Key.Check
		}
	}
	if r.Previous != nil {
		b.Previous = &PreviousClose{Case: caseID(r.Previous.Case), Outcome: r.Previous.Outcome, Explanation: r.Previous.Explanation, By: r.Previous.By, At: r.Previous.At}
	}
	b.Type, b.Incident, b.LastBy, b.LastAt = r.CaseType(), r.Incident, r.LastBy, r.LastAt
	if r.AnalysisQueued != nil && !analyzing {
		b.AnalysisState, b.AnalysisQueued = "queued", true
	}
	switch {
	case r.CreatedBy != nil:
		b.CreatedBy = r.CreatedBy
	case r.AgentFound():
		b.CreatedBy = &issues.Actor{Name: "an agent", Kind: issues.ActorAgent}
	case r.Kind() == SourceReport:
		b.CreatedBy = &issues.Actor{Name: firstOf(personLabel(&r.Reporter), "someone"), Kind: issues.ActorPerson}
	case r.OpenedBy != nil:
		a := issues.ActorOf(*r.OpenedBy)
		b.CreatedBy = &a
	}
	if b.Pending {
		since := r.StatusAt
		b.WaitingSince = &since
	}
	return b
}

// order is the cases' order: the pending first, P0 first, then the
// longest waiting; then the rest, the newest change first.
func order(out []Brief) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pending != b.Pending {
			return a.Pending
		}
		if a.Pending {
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			return a.Since.Before(b.Since)
		}
		return a.Updated.After(b.Updated)
	})
}

// analyzing are the cases whose analysis is under way.
func (s *Service) analyzing(ctx context.Context) map[int]bool {
	rec, err := s.loadRecord(ctx)
	out := map[int]bool{}
	if err != nil {
		return out
	}
	for k := range rec.Analyzing {
		if n, err := strconv.Atoi(k); err == nil {
			out[n] = true
		}
	}
	return out
}

// List lists the cases, in their order.
func (s *Service) List(ctx context.Context, c Caller, f Filter) ([]Brief, error) {
	views, err := s.o.Issues.CaseViews(ctx, c.issue(), issues.Filter{Status: issues.Status(f.Status), Outcome: issues.Outcome(f.Outcome), Open: f.Open, Pending: f.Pending,
		Cluster: f.Cluster, Priority: f.Priority, Source: f.Source, Agents: f.Agents, People: f.People, Owner: f.Owner, Unassigned: f.Unassigned, Type: f.Type, Query: f.Q})
	if err != nil {
		return nil, err
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return nil, err
	}
	nofix := countNoFix(lines)
	busy := s.analyzing(ctx)
	out := make([]Brief, 0, len(views))
	for _, v := range views {
		out = append(out, brief(v, busy[v.Number], nofix))
	}
	named := s.clusterNamer()
	for i := range out {
		named(&out[i])
	}
	order(out)
	return out, nil
}

// clusterNamer gives a case its cluster's name, and says whether the
// console still has that cluster (CF-60): a case keeps a cluster that is
// gone until someone picks another.
func (s *Service) clusterNamer() func(*Brief) {
	names := map[string]string{}
	for _, c := range s.ClusterList() {
		names[c.ID] = firstOf(c.Name, c.ID)
	}
	anyID := s.o.Clusters == nil
	return func(b *Brief) {
		if b.Cluster == "" {
			return
		}
		name, ok := names[b.Cluster]
		ok = ok || anyID
		b.ClusterName, b.ClusterConnected = firstOf(name, b.Cluster), &ok
	}
}

// CardRef is a change that resolves a case, with its card's state now:
// queued, approved, applied, failed, rejected, expired, replaced, or gone
// once let go of.
type CardRef struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	State   string    `json:"state"`
	Cluster string    `json:"cluster,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	// ReplacedBy is the newer card that took its place.
	ReplacedBy string `json:"replacedBy,omitempty"`
	// LinkedBy is who linked it to the case: "agent", or the person's
	// name, who may unlink it; AppliedAt when it was applied (CF-28).
	LinkedBy  string     `json:"linkedBy"`
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
}

// JobRef names a job: the one whose next run checks a fix (CF-31).
type JobRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Investigation is what the report triage job found on a reported case
// (IS-16): for operators, and for the reporter in their words.
type Investigation struct {
	Technical string   `json:"technical,omitempty"`
	Plain     string   `json:"plain,omitempty"`
	Cause     string   `json:"cause,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`
}

// View is one case, whole: the issue service's record of it and 221B's
// work on it, with what may be done to it now, its proposed changes'
// state and its draft.
type View struct {
	Brief
	Detail   string `json:"detail,omitempty"`
	Scrubbed bool   `json:"scrubbed,omitempty"`
	Pruned   bool   `json:"pruned,omitempty"`
	// Reporter and FiledBy are a reported case's; OpenedBy who opened one
	// in the console; ClosedBy who closed it.
	Reporter          *issues.Person    `json:"reporter,omitempty"`
	FiledBy           *issues.Person    `json:"filedBy,omitempty"`
	OpenedBy          *issues.Person    `json:"openedBy,omitempty"`
	ClosedBy          *issues.Person    `json:"closedBy,omitempty"`
	ServiceName       string            `json:"serviceName,omitempty"`
	ServiceBy         string            `json:"serviceBy,omitempty"`
	ServiceConfidence int               `json:"serviceConfidence,omitempty"`
	Known             *issues.KnownView `json:"known,omitempty"`
	MatchConfidence   int               `json:"matchConfidence,omitempty"`
	Rejected          []string          `json:"rejected,omitempty"`
	Similar           []int             `json:"similar,omitempty"`
	Messages          []issues.Message  `json:"messages"`
	Investigation     *Investigation    `json:"investigation,omitempty"`
	SLA               *issues.SLA       `json:"sla,omitempty"`
	Acknowledged      *time.Time        `json:"acknowledged,omitempty"`
	FirstResponse     *time.Time        `json:"firstResponse,omitempty"`
	Resolved          *time.Time        `json:"resolved,omitempty"`
	// Related are the open cases of the other kind on the same object
	// (CF-18); AllowedOutcomes what it may close with now; RCARequired
	// says a fix needs its RCA (P0-P1); MayReopen that a closed one may
	// open again.
	Related         []CaseRef        `json:"related"`
	AllowedOutcomes []issues.Outcome `json:"allowedOutcomes"`
	RCARequired     bool             `json:"rcaRequired"`
	MayReopen       bool             `json:"mayReopen"`
	Analysis        *Analysis        `json:"analysis,omitempty"`
	Runs            []string         `json:"runs,omitempty"`
	ProposalIDs     []string         `json:"proposalIds,omitempty"`
	// Proposals are the changes that resolve the case, any source, with
	// their state now (CF-28); Cards the same list, as before.
	Proposals  []CardRef `json:"proposals"`
	Cards      []CardRef `json:"cards"`
	FixApplied *Note     `json:"fixApplied,omitempty"`
	// Fix is the fix it was saved as (CF-50).
	Fix string `json:"fix,omitempty"`
	// AppliedBy is the change last applied to fix it, and VerifyJob the
	// job whose next run checks it (CF-29, CF-31).
	AppliedBy *issues.AppliedChange `json:"appliedBy,omitempty"`
	VerifyJob *JobRef               `json:"verifyJob,omitempty"`
	RCADetail *RCA                  `json:"rca,omitempty"`
	History   []Event               `json:"history"`
	// Draft is the case's one draft, unsent, shared (CF-59).
	Draft *Draft `json:"draft,omitempty"`
	// RuleMeaning is what the validation rule (the check) of an
	// agent-found case's key means, when the catalogue says (build 14).
	RuleMeaning string `json:"ruleMeaning,omitempty"`
	// Commands are the commands run from the case's drawer (CF-48): every
	// one while it is open, the changes that finished done once closed;
	// FixCommands those changes' lines, what closing as Fixed starts from
	// (CF-49).
	Commands    []Command `json:"commands"`
	FixCommands []string  `json:"fixCommands"`
}

// Open says whether the case is still worked on.
func (v View) Open() bool { return v.Status.Open() }

func (s *Service) view(ctx context.Context, c Caller, v issues.ReportView, w Work, lines []issues.Line) View {
	r := v.Report
	out := View{Brief: brief(v, s.analyzing(ctx)[r.Number], countNoFix(lines)), Detail: r.Text, Scrubbed: r.Scrubbed, Pruned: r.Pruned, FiledBy: r.FiledBy, OpenedBy: r.OpenedBy,
		ClosedBy: r.ClosedBy, ServiceName: v.ServiceName, ServiceBy: r.ServiceBy, ServiceConfidence: r.ServiceConfidence, Known: v.Known, MatchConfidence: r.MatchConfidence,
		Rejected: r.Rejected, Similar: r.Similar, Messages: r.Messages, SLA: v.SLA, Acknowledged: r.Acknowledged, FirstResponse: r.FirstResponse, Resolved: r.Resolved,
		Related: related(r, lines), AllowedOutcomes: r.AllowedOutcomes(), RCARequired: r.RCARequired(), MayReopen: !r.Status.Open(),
		Cards: []CardRef{}, History: []Event{}}
	s.clusterNamer()(&out.Brief)
	if out.Key != nil {
		out.RuleMeaning = jobs.RuleMeaning(out.Key.Check)
	}
	if r.Kind() == SourceReport {
		rep := r.Reporter
		out.Reporter = &rep
	}
	if out.Messages == nil {
		out.Messages = []issues.Message{}
	}
	if r.Technical != "" || r.Plain != "" || r.Cause != "" || len(r.Evidence) > 0 {
		out.Investigation = &Investigation{Technical: r.Technical, Plain: r.Plain, Cause: r.Cause, Evidence: r.Evidence}
	}
	if w.Source.Alert != "" {
		out.Source.Alert, out.Source.Severity, out.Source.AlertKey = w.Source.Alert, w.Source.Severity, w.Source.AlertKey
	}
	out.Analysis, out.Runs, out.ProposalIDs, out.FixApplied, out.RCADetail = w.Analysis, w.Runs, w.Proposals, w.FixApplied, w.RCA
	out.Fix = w.Fix
	if len(w.Runs) > 0 && !out.AgentFound {
		out.Source.Run = w.Runs[0]
	}
	if w.History != nil {
		out.History = w.History
	}
	// The case's one draft, for everyone who works or reads the cases
	// (CF-59); a reporter never reads this view.
	out.Draft = w.Draft
	out.Commands, out.FixCommands = w.Commands, fixLines(w.Commands)
	if out.Commands == nil {
		out.Commands = []Command{}
	}
	out.Proposals = s.proposalsOf(ctx, r.Number, w)
	out.Cards, out.AppliedBy = out.Proposals, r.AppliedBy
	if r.Verification != "" && r.Job != "" {
		out.VerifyJob = &JobRef{ID: r.Job, Label: s.jobLabel(ctx, r)}
	}
	return out
}

// related are the open cases of the other kind (agent-found or a
// person's) on the same cluster and object (CF-18).
func related(r issues.Report, lines []issues.Line) []CaseRef {
	out := []CaseRef{}
	obj := r.Object
	if r.Key != nil {
		obj = r.Key.Object
	}
	if obj == "" || r.Cluster == "" {
		return out
	}
	for _, l := range lines {
		lo := l.Object
		if l.Key != nil {
			lo = l.Key.Object
		}
		if l.Number == r.Number || !l.Open() || l.AgentFound == r.AgentFound() || l.Cluster != r.Cluster || !strings.EqualFold(lo, obj) {
			continue
		}
		out = append(out, CaseRef{ID: caseID(l.Number), Number: l.Number, Title: l.Title, Status: l.Status, Outcome: l.Outcome, Source: l.Source})
	}
	return out
}

// Get is one case, by its id ("C-12") or number.
func (s *Service) Get(ctx context.Context, c Caller, id string) (View, error) {
	n, ok := number(id)
	if !ok {
		return View{}, fmt.Errorf("%w: there is no case %q", ErrNotFound, id)
	}
	v, err := s.o.Issues.CaseView(ctx, c.issue(), n)
	if err != nil {
		return View{}, s.fromIssues(err)
	}
	w, _, err := s.loadWork(ctx, n)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return View{}, err
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, c, v, w, lines), nil
}

// fromIssues turns the issue service's errors into the cases'.
func (s *Service) fromIssues(err error) error {
	for _, k := range []struct{ from, to error }{{issues.ErrNotFound, ErrNotFound}, {issues.ErrInvalid, ErrInvalid}, {issues.ErrDenied, ErrDenied}, {issues.ErrFull, ErrFull}} {
		if errors.Is(err, k.from) {
			msg := strings.TrimPrefix(err.Error(), k.from.Error()+": ")
			return fmt.Errorf("%w: %s", k.to, msg)
		}
	}
	return err
}

// proposalsOf reads the changes that resolve a case, with their state
// now (CF-28): the cards the case kept, and every card that names it
// (its link is the card's), newest first. A card let go of is "gone".
func (s *Service) proposalsOf(ctx context.Context, n int, w Work) []CardRef {
	out := []CardRef{}
	id := caseID(n)
	var all []cards.Card
	read := false
	if s.o.Cards != nil {
		list, err := s.o.Cards.List(ctx, cards.Filter{})
		all, read = list, err == nil
	}
	byID := map[string]cards.Card{}
	var ids []string
	for _, c := range all {
		byID[c.ID] = c
		if slices.Contains(c.Resolves, id) {
			ids = append(ids, c.ID)
		}
	}
	for _, x := range w.Proposals {
		if !slices.Contains(ids, x) {
			ids = append(ids, x)
		}
	}
	for _, x := range ids {
		ref := CardRef{ID: x, State: "unknown", LinkedBy: firstOf(w.LinkedBy[x], cards.LinkedByAgent)}
		if c, ok := byID[x]; ok {
			ref = CardRef{ID: x, Title: c.Title, State: c.State, Cluster: c.Cluster, Updated: c.Updated, ReplacedBy: c.ReplacedBy, LinkedBy: firstOf(c.ResolvesBy[id], ref.LinkedBy)}
			if at := c.AppliedAt(); !at.IsZero() {
				ref.AppliedAt = &at
			}
		} else if read {
			ref.State = "gone"
		}
		out = append(out, ref)
	}
	return out
}

// jobLabel is the name of the job that found a case: its run's label,
// its id when the run is let go of.
func (s *Service) jobLabel(ctx context.Context, r issues.Report) string {
	if s.o.Jobs != nil && r.Run != "" {
		if run, err := s.o.Jobs.GetRun(ctx, r.Run); err == nil && run.Label != "" {
			return run.Label
		}
	}
	return r.Job
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstOf(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func objectOf(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

func prefixed(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// normObject writes what a case is about one way when it names a
// Kubernetes object (CF-05); other words stay as they are.
func normObject(text string) string {
	text = strings.TrimSpace(text)
	if o, err := jobs.NormalizeObject(text); err == nil && o.Kind != "" {
		return o.String()
	}
	return text
}

// knownCluster says why a cluster is not one a case may be on, if not.
func (s *Service) knownCluster(id string) error {
	if id != "" && s.o.Clusters != nil && !slices.ContainsFunc(s.o.Clusters(), func(x jobs.Cluster) bool { return x.ID == id }) {
		return fmt.Errorf("%w: there is no cluster %q", ErrInvalid, id)
	}
	return nil
}

// note adds a line to a case's history. Parts are what the caller just
// sent: they leave the case's draft for everyone (CF-59), and the line
// says who drafted them and who sent them.
func (s *Service) note(ctx context.Context, n int, e Event, parts ...string) {
	s.noteAs(ctx, n, e, false, parts...)
}

// noteSent is note for what the history does not list otherwise, a reply
// or a note: its line is kept only when the draft held what was sent.
func (s *Service) noteSent(ctx context.Context, n int, e Event, parts ...string) {
	s.noteAs(ctx, n, e, true, parts...)
}

func (s *Service) noteAs(ctx context.Context, n int, e Event, onlyDrafted bool, parts ...string) {
	err := s.updateWork(ctx, n, func(w *Work) error {
		now := s.now()
		line := e
		drafted := clearSent(w, e.By, now, parts...)
		if drafted != "" {
			line.What += "; " + drafted
		}
		if line.What != "" && (!onlyDrafted || drafted != "") {
			addEvent(w, line)
		}
		w.Updated = now
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("a case's history could not be kept", "case", caseID(n), "err", err)
	}
}
