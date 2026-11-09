// Package issues is the issue service (Phase 12, docs/issues.md): people
// report problems in plain text, the service matches each report against
// the known issues and the services, has an investigator look at the rest,
// and keeps the reporter told until it is resolved.
//
// It is a component of its own, with its own records, storage and HTTP API
// (/api/issues/v1/, http.go), so that it can ship apart from 221B later.
// It knows nothing of the console's sign-in, roles or clusters: whoever
// mounts it says who is calling (Caller) and what they may do, and gives
// it a key-value backend, an audit sink and, later, an Investigator. 221B
// is its first integration (internal/api/issues.go).
package issues

import (
	"encoding/json"
	"time"

	"github.com/ucesys/221b/server/internal/cardid"
)

// Status is where a case stands (IS-04; Phase 12, wave 6, CF-01): New
// while nobody has taken it, In progress once someone has (an owner, an
// analysis, a fix being applied), Closed with exactly one outcome.
type Status string

const (
	StatusNew        Status = "new"
	StatusInProgress Status = "inProgress"
	StatusClosed     Status = "closed"
)

// Statuses lists them in their usual order.
var Statuses = []Status{StatusNew, StatusInProgress, StatusClosed}

// Valid reports whether s is a status.
func (s Status) Valid() bool {
	for _, x := range Statuses {
		if s == x {
			return true
		}
	}
	return false
}

// Open reports whether a case in this status still needs work.
func (s Status) Open() bool { return s != StatusClosed }

// Outcome is why a closed case was closed: exactly one (CF-03).
type Outcome string

const (
	// OutcomeFix is fixed: what was done, and an RCA (required at P0-P1).
	OutcomeFix Outcome = "fix"
	// OutcomeDuplicate is the same problem as another case, its original.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeNotAProblem is a wrong expectation or a misunderstanding;
	// never for a case an agent found (use an exception).
	OutcomeNotAProblem Outcome = "notAProblem"
	// OutcomeNoFixNeeded is real but one-off, transient or already gone.
	OutcomeNoFixNeeded Outcome = "noFixNeeded"
	// OutcomeKnownIssue is a problem a known issue follows.
	OutcomeKnownIssue Outcome = "knownIssue"
	// OutcomeException is intended or acceptable behaviour an agent
	// found: the agents stop reporting it.
	OutcomeException Outcome = "exception"
)

// Outcomes lists them.
var Outcomes = []Outcome{OutcomeFix, OutcomeDuplicate, OutcomeNotAProblem, OutcomeNoFixNeeded, OutcomeKnownIssue, OutcomeException}

// Valid reports whether o is an outcome.
func (o Outcome) Valid() bool {
	for _, x := range Outcomes {
		if o == x {
			return true
		}
	}
	return false
}

// Person names who did something: a signed-in person by their user name,
// or a system account filing through a token (IS-03, IS-24).
type Person struct {
	// ID is the person's user name, or "system:<account>" for a token.
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	// System marks an account that files reports through the API.
	System bool `json:"system,omitempty"`
}

// Label is how a person is shown: their name, or their ID.
func (p Person) Label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// MessageKind says who a message is from (IS-14).
type MessageKind string

const (
	FromReporter MessageKind = "reporter"
	FromOperator MessageKind = "operator"
	FromSystem   MessageKind = "system"
)

// Message is one entry of a report's conversation. A draft is a reply
// not yet sent: written by an operator or by an agent, it reaches the
// reporter only when an operator sends it (IS-14). An internal note is an
// operator's, never shown to the reporter.
type Message struct {
	ID   int         `json:"id"`
	At   time.Time   `json:"at"`
	Kind MessageKind `json:"kind"`
	From Person      `json:"from"`
	Text string      `json:"text"`
	// Draft marks a reply waiting for an operator's Send; DraftBy says
	// who wrote it ("agent" for an investigator's).
	Draft   bool   `json:"draft,omitempty"`
	DraftBy string `json:"draftBy,omitempty"`
	// Internal marks an operator's note the reporter never sees.
	Internal bool `json:"internal,omitempty"`
	// SentBy is the operator who sent a reply drafted by someone else.
	SentBy *Person `json:"sentBy,omitempty"`
}

// Report is one case (IS-01 to IS-15; CU-01; CF-01): the one record of a
// problem someone works on until it is closed, whether a person reported
// it (the report page, the avatar menu, Sherlock's "Report this", the
// issue API v1), opened it in the console (New Case, an alert), or an
// agent found it (a finding of a job's run). The API keeps calling it a
// report.
type Report struct {
	Number int `json:"number"`
	// Title is the case in a line, as the person who opened it or an
	// operator wrote it; a report without one shows its first line.
	Title string `json:"title,omitempty"`
	// Source is what opened it: CaseReport for a person's report, or
	// CaseFinding (agent-found), CaseManual or CaseAlert.
	Source string `json:"source,omitempty"`
	// Cluster and Object are what it is about, when anyone said: the
	// cluster's id, and any object in words ("shop/Deployment/web", "the
	// disk of db-1 at 90%"), or none.
	Cluster string `json:"cluster,omitempty"`
	Object  string `json:"object,omitempty"`
	// OpenedBy is who opened a case in the console; such a case has no
	// reporter.
	OpenedBy *Person `json:"openedBy,omitempty"`
	// --- One flow for findings and cases (Phase 12, wave 6) ---
	// Key is an agent-found case's: cluster, object and check (CF-05);
	// Job, Run, Finding and Severity name where it was found.
	Key      *Key   `json:"key,omitempty"`
	Job      string `json:"job,omitempty"`
	Run      string `json:"run,omitempty"`
	Finding  string `json:"finding,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Seen counts the runs that found it again while it was open (CF-01).
	Seen *Seen `json:"seen,omitempty"`
	// RegressionOf, SameAs and PossibleDuplicateOf link it to other
	// cases by number; Previous is how the case it repeats was closed.
	RegressionOf        int            `json:"regressionOf,omitempty"`
	SameAs              int            `json:"sameAs,omitempty"`
	PossibleDuplicateOf int            `json:"possibleDuplicateOf,omitempty"`
	Previous            *PreviousClose `json:"previous,omitempty"`
	// Exception is the accepted exception a case closed as exception made
	// ("EX-3"); RCA the id Mycroft › RCAs gave the RCA of a case fixed
	// before wave 6e ("RCA-4"). RCACause is the cause its RCA gives, for
	// finding past RCAs among the cases: the RCA is the case's (CF-44).
	Exception string `json:"exception,omitempty"`
	RCA       string `json:"rca,omitempty"`
	RCACause  string `json:"rcaCause,omitempty"`
	// VerifyPending says the next run of its job checks a fix of an
	// agent-found case; Verified is when a run did not find it again.
	VerifyPending bool       `json:"verifyPending,omitempty"`
	Verified      *time.Time `json:"verified,omitempty"`
	// SLAFrom is when its SLA clock started: at once for a case a person
	// opened, at P0-P1 only for an agent-found one (CF-02); nil is no SLA.
	SLAFrom *time.Time `json:"slaFrom,omitempty"`
	// ClosedBy is who closed it.
	ClosedBy *Person `json:"closedBy,omitempty"`
	// FixProposed says the console's analysis suggested a fix or proposed
	// changes (package casework sets it).
	FixProposed bool `json:"fixProposed,omitempty"`
	// --- end of one flow.
	// --- A proposed change names the cases it resolves (Phase 12, wave
	// 6b, CF-29 to CF-31) ---
	// AppliedBy is the change (a Watson card) whose applying fixed the
	// case, last; Verification is how the next run of the job that found
	// it saw it since: VerifyWaiting, VerifyVerified (P0-P1, waiting for
	// the RCA) or VerifyNotHeld; "" otherwise.
	AppliedBy    *AppliedChange `json:"appliedBy,omitempty"`
	Verification string         `json:"verification,omitempty"`
	// --- end of resolves.
	// --- The owner's feedback on Cases (Phase 12, wave 6c, CF-32 to
	// CF-37) ---
	// Type is what kind of problem it is (TypeSecurity, ...; "" is
	// other); Incident another system's number for it ("INC0012345").
	Type     string `json:"type,omitempty"`
	Incident string `json:"incident,omitempty"`
	// CreatedBy is who opened it: its reporter, the person who opened it
	// in the console, or the agent that found it. LastBy and LastAt are
	// who changed it last, and when.
	CreatedBy *Actor     `json:"createdBy,omitempty"`
	LastBy    *Actor     `json:"lastBy,omitempty"`
	LastAt    *time.Time `json:"lastAt,omitempty"`
	// AnalysisQueued is when an agent-found case was queued for the case
	// analysis job (CF-32); nil when it is not waiting for one.
	AnalysisQueued *time.Time `json:"analysisQueued,omitempty"`
	// --- end of the owner's feedback.
	// Text is what the reporter wrote, scrubbed of anything that looked
	// like a credential before it was kept; Scrubbed says something was.
	// After the retention, a closed report's text is dropped (IS-17) and
	// Pruned says so.
	Text     string `json:"text,omitempty"`
	Scrubbed bool   `json:"scrubbed,omitempty"`
	Pruned   bool   `json:"pruned,omitempty"`
	// Reporter is whose problem it is; FiledBy is support filing it for
	// them, when that is someone else (IS-03).
	Reporter Person    `json:"reporter"`
	FiledBy  *Person   `json:"filedBy,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Status   Status    `json:"status"`
	// StatusAt is when the report entered its status, for the timers.
	StatusAt time.Time `json:"statusAt"`
	// Outcome and Explanation say why a closed case was closed.
	Outcome     Outcome `json:"outcome,omitempty"`
	Explanation string  `json:"explanation,omitempty"`
	// Priority is 0 to 4: P0 outage, P1 blocking, P2 service degradation,
	// P3 preemptive, P4 question (IS-05, CF-02).
	Priority int `json:"priority"`
	// PriorityBy says how the priority was set: "default", "reporter"
	// (chosen on the report page or by the intake API, CF-64),
	// "severity", "alert", "analysis" (the case analysis lowered it,
	// CF-83), or an operator's name.
	PriorityBy string  `json:"priorityBy,omitempty"`
	Owner      *Person `json:"owner,omitempty"`
	// Service is the service's ID, "" while unknown, with how sure the
	// guess is and where it came from (IS-07).
	Service           string `json:"service,omitempty"`
	ServiceConfidence int    `json:"serviceConfidence,omitempty"`
	ServiceBy         string `json:"serviceBy,omitempty"`
	// KnownIssue is the known issue a case closed as knownIssue links,
	// with the match's confidence when a match made the link (IS-10);
	// Rejected are the known issues the reporter said theirs is not
	// (IS-11).
	KnownIssue      string   `json:"knownIssue,omitempty"`
	MatchConfidence int      `json:"matchConfidence,omitempty"`
	Rejected        []string `json:"rejected,omitempty"`
	// DuplicateOf is the case this one repeats (its original, the end of
	// the duplicate chain); Similar are open reports on the same service
	// that read alike (IS-12).
	DuplicateOf int   `json:"duplicateOf,omitempty"`
	Similar     []int `json:"similar,omitempty"`
	// Waiting is when an operator asked the reporter for more: the case
	// closes as no fix needed after the silence (IS-15).
	Waiting *time.Time `json:"waiting,omitempty"`
	// Watchers follow the report besides its reporter.
	Watchers []Person  `json:"watchers,omitempty"`
	Messages []Message `json:"messages,omitempty"`
	// What an investigator found (IS-16): the technical summary is for
	// operators; the plain one is for the reporter.
	Technical string   `json:"technical,omitempty"`
	Plain     string   `json:"plain,omitempty"`
	Cause     string   `json:"cause,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`
	// The service-level times (IS-06).
	Acknowledged  *time.Time `json:"acknowledged,omitempty"`
	FirstResponse *time.Time `json:"firstResponse,omitempty"`
	Resolved      *time.Time `json:"resolved,omitempty"`
	Closed        *time.Time `json:"closed,omitempty"`
	// LastReporter is when the reporter last wrote or confirmed, for the
	// timers that close a silent report.
	LastReporter time.Time `json:"lastReporter"`
	// Reopened counts the times it was reopened.
	Reopened int `json:"reopened,omitempty"`
}

// AppliedChange is a change applied to fix a case (CF-29): the card, its
// title, who approved it and when it was applied.
type AppliedChange struct {
	Card  string    `json:"card"`
	Title string    `json:"title"`
	By    string    `json:"by,omitempty"`
	At    time.Time `json:"at"`
}

// UnmarshalJSON reads the card as PC-<n>, whatever form it was kept in:
// a case fixed before CF-56 names it c<n>.
func (a *AppliedChange) UnmarshalJSON(b []byte) error {
	type plain AppliedChange
	if err := json.Unmarshal(b, (*plain)(a)); err != nil {
		return err
	}
	a.Card = cardid.Canon(a.Card)
	return nil
}

// How the run after a change applied saw an agent-found case (CF-30).
const (
	// VerifyWaiting is a fix applied, the next run of the job not yet.
	VerifyWaiting = "waiting"
	// VerifyVerified is a P0-P1 case the next run no longer saw: it waits
	// for its RCA to close.
	VerifyVerified = "verified"
	// VerifyNotHeld is a case the next run found again.
	VerifyNotHeld = "notHeld"
)

// Key names what an agent found (CF-05): the cluster, the object as
// namespace/Kind/name or Kind/name ("" for the cluster as a whole), and
// the check.
type Key struct {
	Cluster string `json:"cluster"`
	Object  string `json:"object"`
	Check   string `json:"check"`
}

// String is the key in one line.
func (k Key) String() string { return k.Cluster + " " + orNone(k.Object) + " " + k.Check }

// Seen counts the runs that found an open case again (CF-01).
type Seen struct {
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// PreviousClose is how an earlier case of the same problem was closed:
// shown on a repeat after noFixNeeded or notAProblem, and on a regression.
type PreviousClose struct {
	Case        int       `json:"case"`
	Outcome     Outcome   `json:"outcome"`
	Explanation string    `json:"explanation"`
	By          string    `json:"by"`
	At          time.Time `json:"at"`
}

// watchedBy reports whether a person follows the report.
func (r *Report) watchedBy(id string) bool {
	for _, w := range r.Watchers {
		if w.ID == id {
			return true
		}
	}
	return false
}

// nextMessageID is the ID the report's next message takes.
func (r *Report) nextMessageID() int {
	n := 0
	for _, m := range r.Messages {
		n = max(n, m.ID)
	}
	return n + 1
}

// KnownStatus is where a known issue stands (IS-08).
type KnownStatus string

const (
	KnownInvestigating KnownStatus = "investigating"
	KnownIdentified    KnownStatus = "identified"
	KnownFixInProgress KnownStatus = "fixInProgress"
	KnownResolved      KnownStatus = "resolved"
)

// Valid reports whether s is a known issue's status.
func (s KnownStatus) Valid() bool {
	switch s {
	case KnownInvestigating, KnownIdentified, KnownFixInProgress, KnownResolved:
		return true
	}
	return false
}

// Kinds and sources of a known issue.
const (
	KindNormal  = "normal"
	KindPlanned = "planned"

	SourceOperator = "operator"
	SourceAlert    = "alert"
	SourcePlanned  = "plannedChange"
	// SourceAgent is a known issue an agent proposed: a draft until an
	// operator accepts it, matched and shown to nobody before.
	SourceAgent = "agent"
	// SourceConfluence is a page someone wrote by hand under the
	// known-issue tree in Confluence, imported as a draft the same way
	// (Phase 12, ADR-97).
	SourceConfluence = "confluence"
)

// Window is a planned maintenance's time.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Update is one dated entry of a known issue's log.
type Update struct {
	At     time.Time   `json:"at"`
	By     Person      `json:"by"`
	Text   string      `json:"text"`
	Status KnownStatus `json:"status,omitempty"`
}

// KnownIssue is a problem the operators know about (IS-08): reports that
// match it are linked to it and told its status, workaround and expected
// fix.
type KnownIssue struct {
	// ID is KI-<n>.
	ID    string `json:"id"`
	Title string `json:"title"`
	// Symptoms are what people see, in their words: what reports match.
	Symptoms   []string    `json:"symptoms,omitempty"`
	Services   []string    `json:"services,omitempty"`
	Status     KnownStatus `json:"status"`
	Workaround string      `json:"workaround,omitempty"`
	// Expected is when a fix is expected, in words ("today, 15:00").
	Expected string   `json:"expected,omitempty"`
	Owner    *Person  `json:"owner,omitempty"`
	Updates  []Update `json:"updates,omitempty"`
	Kind     string   `json:"kind"`
	Window   *Window  `json:"window,omitempty"`
	Source   string   `json:"source"`
	// Draft marks one an agent proposed, until an operator accepts it.
	Draft bool `json:"draft,omitempty"`
	// Link is the full page, on Confluence say.
	Link      string     `json:"link,omitempty"`
	Followers []Person   `json:"followers,omitempty"`
	Created   time.Time  `json:"created"`
	Updated   time.Time  `json:"updated"`
	Resolved  *time.Time `json:"resolved,omitempty"`
	// --- One flow for findings and cases (Phase 12, wave 6, CF-06) ---
	// Keys are what agents found that cases closed as this known issue
	// were about, with the job that found each: a repeat of one is
	// recorded here, StillPresent counting them, and nothing new reaches
	// the Inbox.
	Keys         []KnownFound  `json:"keys,omitempty"`
	StillPresent *StillPresent `json:"stillPresent,omitempty"`
	// VerifyJobs are the jobs whose next run checks a resolved known
	// issue: still seen reopens it, and Pending puts it in its owner's
	// Inbox until they change it.
	VerifyJobs []string `json:"verifyJobs,omitempty"`
	Pending    bool     `json:"pending,omitempty"`
	// --- Housekeeping (Phase 12, wave 6g, CF-53) ---
	// Inactive marks one the daily housekeeping found a duplicate of
	// DuplicateOf: no case is matched to it until an operator makes it
	// active again.
	Inactive    bool   `json:"inactive,omitempty"`
	DuplicateOf string `json:"duplicateOf,omitempty"`
}

// KnownFound is a key of a known issue, with the job that found it.
type KnownFound struct {
	Key
	Job string `json:"job,omitempty"`
}

// StillPresent says agents still see a known issue's problem.
type StillPresent struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// MaxKnownKeys bounds the keys of one known issue.
const MaxKnownKeys = 50

// HasKey reports whether a known issue holds a key.
func (k *KnownIssue) HasKey(x Key) bool {
	for _, y := range k.Keys {
		if y.Key == x {
			return true
		}
	}
	return false
}

// AddKey adds a key once, the newest kept.
func (k *KnownIssue) AddKey(x Key, job string) {
	if k.HasKey(x) {
		return
	}
	k.Keys = append(k.Keys, KnownFound{Key: x, Job: job})
	if len(k.Keys) > MaxKnownKeys {
		k.Keys = k.Keys[len(k.Keys)-MaxKnownKeys:]
	}
}

// Open reports whether reports may still match the known issue: accepted,
// not resolved and not made inactive as a duplicate.
func (k *KnownIssue) Open() bool { return !k.Draft && k.Status != KnownResolved && !k.Inactive }

// LastUpdate is the newest entry of the log, if any.
func (k *KnownIssue) LastUpdate() *Update {
	if len(k.Updates) == 0 {
		return nil
	}
	return &k.Updates[len(k.Updates)-1]
}

func (k *KnownIssue) followedBy(id string) bool {
	for _, f := range k.Followers {
		if f.ID == id {
			return true
		}
	}
	return false
}

// RunsOn is one thing a service runs on: a cluster, a namespace, a
// workload, a host, a fleet, a URL or a hostname (IS-09).
type RunsOn struct {
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Cluster string `json:"cluster,omitempty"`
}

// The kinds of RunsOn.
var runsOnKinds = []string{"cluster", "namespace", "workload", "host", "fleet", "url", "hostname"}

// ServiceDef is a service people use, as the issue service knows it: what
// reports are about (IS-09). Its contacts see every report on it.
type ServiceDef struct {
	// ID is a short name made from the name when it is created.
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RunsOn      []RunsOn `json:"runsOn,omitempty"`
	// Contacts are user names.
	Contacts []string `json:"contacts,omitempty"`
	// Aliases are other names and keywords people use for it.
	Aliases []string  `json:"aliases,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

func (s *ServiceDef) hasContact(id string) bool {
	for _, c := range s.Contacts {
		if c == id {
			return true
		}
	}
	return false
}
