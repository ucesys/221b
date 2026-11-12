// Package jobs runs the console's agents on their own (Phase 12, JB-01 to
// JB-24, ADR-90 to ADR-93): jobs that start an agent on a schedule or when
// something happens (an alert fires, a report arrives, a source changes),
// with nobody at the screen. What they make is read-only: a run with its
// report and findings, kept in the runs ConfigMap, and proposals that wait
// for a person in AIOps › Inbox. Nothing a job does changes a cluster.
//
// A job is of a kind. The kinds are registered, the console's own here and
// in package investigate, and other packages' at start-up, each with the
// function that does one run; the service keeps the jobs' settings, runs
// the schedules on the leader (ADR-70), takes the events, keeps the
// budgets and records every run.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/schedule"
)

// The events a job can start on.
const (
	// EventAlert is a new alert on a cluster's Alerts tab.
	EventAlert = "alert"
	// EventReport is a problem report the issue service received.
	EventReport = "report"
	// EventSource is a knowledge source that changed (Mycroft).
	EventSource = "source"
	// --- Case work (Phase 12) ---
	// EventCase is a case opened, or a person asking for its analysis
	// again (package casework).
	EventCase = "case"
)

// The least severities an alert job may take: warning and above, or
// errors only.
const (
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// What started a run.
const (
	TriggerSchedule = "schedule"
	TriggerEvent    = "event"
	TriggerManual   = "manual"
)

// A run's status.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusFailed  = "failed"
	// StatusSkipped is a time or an event let go because the job's last
	// run was still going.
	StatusSkipped = "skipped"
	// StatusMissed is a scheduled time the console was down for, past the
	// grace.
	StatusMissed = "missed"
	// StatusBudget is a run stopped, or not started, because the job's
	// budget for the run or the day is spent.
	StatusBudget = "budget"
	// StatusInterrupted is a run its replica stopped running: it lost the
	// lead or went down.
	StatusInterrupted = "interrupted"
	// StatusCancelled is a run a person cancelled while it ran (FX-12).
	StatusCancelled = "cancelled"
)

// --- One flow for findings and cases (Phase 12, wave 6, CF-05) ---

// What became of a finding of a run: each becomes a case or joins one
// (package casework routes them, CF-01).
const (
	// RoutedOpened opened a new case; RoutedSeen joined an open one.
	RoutedOpened = "opened"
	RoutedSeen   = "seen"
	// RoutedKnown was recorded on the known issue its case was closed as.
	RoutedKnown = "known"
	// RoutedExcepted was dropped: an accepted exception covers it.
	RoutedExcepted = "excepted"
	// RoutedReopened reopened its case: the fix did not hold.
	RoutedReopened = "reopened"
	// RoutedRegression opened a new case, a regression of a fixed one.
	RoutedRegression = "regression"
	// RoutedCapped opened nothing: the run opened as many cases as it may.
	RoutedCapped = "capped"
	// RoutedInvalid was not routed: its check or object is not valid.
	RoutedInvalid = "invalid"
	// RoutedIgnored opened no case and joined none: its priority is below
	// the job's lowest (CF-33).
	RoutedIgnored = "ignored"
	// RoutedExpected opened no case and joined none: the agent found the
	// system working as intended, whatever the severity, and said why
	// (CF-82).
	RoutedExpected = "expected"
)

// The severities of a finding.
const (
	FindingInfo     = "info"
	FindingWarning  = "warning"
	FindingCritical = "critical"
)

// Grace is how late a scheduled run missed while the console was down may
// still be made: within it, once; past it, the job waits for its next time
// (as the backups to S3 do).
const Grace = time.Hour

// DedupWindow is how long a repeat of the same event (the same alert on
// the same object) updates the run it started instead of starting another.
const DedupWindow = 24 * time.Hour

// When says what starts a job: a schedule, an event, or both ("every day
// at 01:00, and on change").
type When struct {
	Schedule *schedule.Spec `json:"schedule,omitempty"`
	// Event is EventAlert, EventReport or EventSource; empty for none.
	Event string `json:"event,omitempty"`
	// Severity is an alert job's least severity: SeverityWarning or
	// SeverityError.
	Severity string `json:"severity,omitempty"`
}

// Budget is what a job may spend, in US dollars: on one run, and on all its
// runs of a day (UTC). Zero is no limit of the job's own; the agent's caps
// and the overall limits still hold.
type Budget struct {
	RunUSD float64 `json:"runUSD,omitempty"`
	DayUSD float64 `json:"dayUSD,omitempty"`
}

// Job is one job's settings, as kept.
type Job struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Name is the job's own name; empty is the kind's label (JR-06).
	Name string `json:"name,omitempty"`
	// Task is the job's own instructions to its agent, in place of its
	// kind's built-in ones (Kind.Task); empty is the built-in (JR-06).
	Task string `json:"task,omitempty"`
	When When   `json:"when"`
	// Clusters are the clusters it covers; empty is every cluster.
	Clusters []string `json:"clusters,omitempty"`
	Budget   Budget   `json:"budget"`
	On       bool     `json:"on"`
	// SkipOverlap lets a time go while the last run is still going (the
	// default); CatchUp makes a time missed while the console was down
	// within the grace once it is back (the default).
	SkipOverlap bool `json:"skipOverlap"`
	CatchUp     bool `json:"catchUp"`
	// SetBy and SetAt are who last changed the job and when: who set its
	// trigger, which the runs and the audit log name (JB-03).
	SetBy string    `json:"setBy,omitempty"`
	SetAt time.Time `json:"setAt,omitzero"`
	// Since is when its schedule counts from: when it was turned on or its
	// schedule changed, so a time before is not made up.
	Since time.Time `json:"since,omitzero"`
	// Added marks a job a person added, which may be removed.
	Added bool `json:"added,omitempty"`
	// MinPriority is the lowest priority a finding of the job opens a
	// case at, 0 to 4 (CF-33): a finding below it opens none. nil is
	// DefaultMinPriority.
	MinPriority *int `json:"minPriority,omitempty"`
}

// DefaultMinPriority is a job's lowest priority when nobody set one: P3,
// so the findings of information (P4) open no case.
const DefaultMinPriority = 3

// Lowest is the lowest priority a finding of the job opens a case at.
func (j Job) Lowest() int {
	if j.MinPriority == nil {
		return DefaultMinPriority
	}
	return *j.MinPriority
}

// Covers says whether the job works on a cluster; "" (a report on no
// cluster yet) is covered by every job.
func (j Job) Covers(cluster string) bool {
	return cluster == "" || len(j.Clusters) == 0 || slices.Contains(j.Clusters, cluster)
}

// Trigger is what started a run, and who set it.
type Trigger struct {
	// Kind is TriggerSchedule, TriggerEvent or TriggerManual.
	Kind string `json:"kind"`
	// What says it in words: "Every day at 02:00, Europe/Berlin", "Alert
	// CrashLoopBackOff on shop/web-7f9 at 03:12", "Run now".
	What string `json:"what"`
	// SetBy is who set the job's trigger; By who pressed Run now.
	SetBy string `json:"setBy,omitempty"`
	By    string `json:"by,omitempty"`
	// Slot is the scheduled time a scheduled run is for.
	Slot time.Time `json:"slot,omitzero"`
	// Event is the event's kind and its key, for an event's run.
	Event string `json:"event,omitempty"`
	Key   string `json:"key,omitempty"`
	// Rebuild says a person asked to rebuild, not just run: a Wiki sync
	// gives Mycroft every item again, unchanged ones too (KN-29).
	Rebuild bool `json:"rebuild,omitempty"`
}

// Event is something that happened that a job may start on.
type Event struct {
	// Kind is EventAlert, EventReport or EventSource.
	Kind string
	// Cluster is the cluster it happened on; "" for none.
	Cluster string
	// Key names what it is about, so that a repeat updates the run it
	// started ("alert:prod/pod:shop/web-7f9"); "" never repeats.
	Key string
	// Label says it in words, for the run and its proposals.
	Label string
	// Severity is an alert's: SeverityWarning or SeverityError.
	Severity string
	// Ref links the run to what it is about: "report:143".
	Ref string
	// Data is the event's own record, for the kind's Run: an alert, a
	// report.
	Data any
	// JobKind, when set, is the only kind whose jobs take it: a change to
	// one knowledge source starts that source's Wiki sync job alone
	// (KN-28).
	JobKind string
}

// Key names what a finding is about (CF-05): the cluster, the object as
// namespace/Kind/name or Kind/name ("" for the cluster as a whole), and
// the check, from the job kind's catalogue or other.<slug>.
type Key struct {
	Cluster string `json:"cluster"`
	Object  string `json:"object"`
	Check   string `json:"check"`
}

// Finding is one thing a run found that a person should look at: each
// becomes a case or joins one (CF-01).
type Finding struct {
	// ID is the run's and a number: "run-12.1".
	ID       string `json:"id"`
	Severity string `json:"severity"`
	// Check is the kind of check, as the agent named it: an id of the job
	// kind's catalogue ("rbac.cluster-admin-binding") or other.<slug>.
	Check    string   `json:"check,omitempty"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Cluster  string   `json:"cluster,omitempty"`
	// Resource is what it is about, as the agent wrote it.
	Resource string `json:"resource,omitempty"`
	// Key is the finding's key, the object normalised; nil for one not
	// valid, InvalidReason saying why. Unverified says the cluster did
	// not answer whether the object is there.
	Key           *Key   `json:"key,omitempty"`
	InvalidReason string `json:"invalidReason,omitempty"`
	Unverified    bool   `json:"unverified,omitempty"`
	// Routing is what became of it, with the case, known issue or
	// exception it went to; NewCheck says its check was never seen on its
	// cluster before.
	Routing    string `json:"routing,omitempty"`
	Case       string `json:"case,omitempty"`
	KnownIssue string `json:"knownIssue,omitempty"`
	Exception  string `json:"exception,omitempty"`
	NewCheck   bool   `json:"newCheck,omitempty"`
	// Expected says the agent found the system working as intended (a
	// node put to sleep by its power manager, a planned drain), and
	// ExpectedWhy why, with its evidence (CF-82): it opens no case and
	// joins none, whatever its severity.
	Expected    bool   `json:"expected,omitempty"`
	ExpectedWhy string `json:"expectedWhy,omitempty"`
	// Confidence is how sure the agent is of the cause (PM-J1d): confirmed
	// when it read the evidence, likely when the cause fits and it could
	// not confirm it; "" on a finding of before the field, or when the
	// agent gave neither word.
	Confidence string `json:"confidence,omitempty"`
	// CheckWhy is the agent's description of a check id it made up
	// (other.<slug>): what the id means, and why no rule on the list
	// fitted (CF-86). Only kept with an other.<slug> check; one missing is
	// no reason to refuse the finding.
	CheckWhy string `json:"checkWhy,omitempty"`
}

// ProposalRef is a proposal a run made, as the Proposer filed it.
type ProposalRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Error says why it could not be filed.
	Error string `json:"error,omitempty"`
	// Resolves are the cases and known issues it resolves (CF-25), with
	// their titles as they are when the run is read (CF-39).
	Resolves       []string          `json:"resolves,omitempty"`
	ResolvesTitles map[string]string `json:"resolvesTitles,omitempty"`
}

// UnmarshalJSON reads the proposal's id as PC-<n>, whatever form it was
// kept in: a run made before CF-56 names it c<n>.
func (p *ProposalRef) UnmarshalJSON(b []byte) error {
	type plain ProposalRef
	if err := json.Unmarshal(b, (*plain)(p)); err != nil {
		return err
	}
	p.ID = cardid.Canon(p.ID)
	return nil
}

// Tokens are what a run's agent calls used.
type Tokens struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// Run is one run of a job, as kept in the runs ConfigMap.
type Run struct {
	// ID is "run-<n>".
	ID    string `json:"id"`
	Job   string `json:"job"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Agent string `json:"agent,omitempty"`
	// Trigger is what started it.
	Trigger Trigger `json:"trigger"`
	// Subject is the event's key; Repeats counts the repeats it took in
	// since, LastSeen the latest.
	Subject  string    `json:"subject,omitempty"`
	Repeats  int       `json:"repeats,omitempty"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
	// Severity is the alert's that started it, so the alert grown more
	// severe is not a repeat of it (CF-79); Fingerprint the alert as the
	// run saw it, so the cases' safety net knows what it concluded on
	// (CF-78, CF-82).
	Severity    string `json:"severity,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Ref links it to what it is about: "report:143".
	Ref      string    `json:"ref,omitempty"`
	Clusters []string  `json:"clusters,omitempty"`
	Model    string    `json:"model,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitzero"`
	Status   string    `json:"status"`
	// Note says why it was skipped, missed or stopped, or what failed.
	Note     string    `json:"note,omitempty"`
	Report   string    `json:"report,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
	// Routed counts what its findings did (CF-01, CF-33); VerifiedCases are the
	// fixes it verified, NewChecks the check ids new on their cluster.
	Routed        Routed        `json:"routed"`
	VerifiedCases []string      `json:"verifiedCases,omitempty"`
	NewChecks     []string      `json:"newChecks,omitempty"`
	Proposals     []ProposalRef `json:"proposals,omitempty"`
	// What the run could not be used for (I-3): FailedClusters did not
	// answer, so none of their fixes was verified (W-2); LeftOut are the
	// findings left out past the cap (W-3); NotRechecked counts the fixes
	// in its scope the agent did not re-check (W-1).
	FailedClusters []string `json:"failedClusters,omitempty"`
	LeftOut        *LeftOut `json:"leftOut,omitempty"`
	NotRechecked   int      `json:"notRechecked,omitempty"`
	Tokens         Tokens   `json:"tokens"`
	CostUSD        float64  `json:"costUSD"`
	// NotRouted says why its findings were not routed into cases (W-8,
	// I-3, CF-99); Reroute that the leader tries again on its next look.
	NotRouted string `json:"notRouted,omitempty"`
	Reroute   bool   `json:"reroute,omitempty"`
	// Instance is the replica that ran it, to tell a run its replica left
	// behind from one under way.
	Instance string `json:"instance,omitempty"`
	// --- A Wiki sync job per source (Phase 12, KN-28) ---
	// Facts are what the run did, as its kind says it (a sync's source,
	// the items it read, the drafts it wrote), for the run's drawer;
	// NoFindings says its kind makes no findings: it is not an audit.
	Facts      []Fact `json:"facts,omitempty"`
	NoFindings bool   `json:"noFindings,omitempty"`
}

// Fact is one thing a run did, as its drawer shows it: a label, the value
// in words, and where it leads (KN-28).
type Fact struct {
	Label string `json:"label"`
	Text  string `json:"text"`
	Href  string `json:"href,omitempty"`
	// Tone is "err" or "warn" for what went wrong; empty otherwise.
	Tone string `json:"tone,omitempty"`
}

// maxFacts and maxFactText bound what a run keeps of its facts.
const (
	maxFacts    = 20
	maxFactText = 600
)

// keepFacts clips a run's facts to what is kept.
func keepFacts(in []Fact) []Fact {
	var out []Fact
	for _, f := range in {
		if len(out) == maxFacts {
			break
		}
		if f.Label = clip(f.Label, 60); f.Label == "" {
			continue
		}
		f.Text = clip(f.Text, maxFactText)
		if f.Href != "" && !strings.HasPrefix(f.Href, "#/") {
			f.Href = ""
		}
		out = append(out, f)
	}
	return out
}

// Routed counts what a run's findings did (CF-01).
type Routed struct {
	Opened     int `json:"opened"`
	Seen       int `json:"seen"`
	Known      int `json:"known"`
	Excepted   int `json:"excepted"`
	Reopened   int `json:"reopened"`
	Regression int `json:"regression"`
	Verified   int `json:"verified"`
	Capped     int `json:"capped"`
	Invalid    int `json:"invalid"`
	Ignored    int `json:"ignored"`
	Expected   int `json:"expected"`
}

// Add counts one finding's routing.
func (r *Routed) Add(routing string) {
	switch routing {
	case RoutedOpened:
		r.Opened++
	case RoutedSeen:
		r.Seen++
	case RoutedKnown:
		r.Known++
	case RoutedExcepted:
		r.Excepted++
	case RoutedReopened:
		r.Reopened++
	case RoutedRegression:
		r.Regression++
	case RoutedCapped:
		r.Capped++
	case RoutedInvalid:
		r.Invalid++
	case RoutedIgnored:
		r.Ignored++
	case RoutedExpected:
		r.Expected++
	}
}

// Open counts the cases the run's findings opened or reopened: what
// waits for a person.
func (r Run) Open() int { return r.Routed.Opened + r.Routed.Reopened + r.Routed.Regression }

// --- Jobs and findings, round 2 (Phase 12, wave 5) ---

// A run's health (JR-01): the worst of what it found, or that it failed.
const (
	HealthCritical = "critical"
	HealthWarning  = "warning"
	HealthInfo     = "info"
	HealthNone     = "none"
	HealthFailed   = "failed"
)

// Counts are a run's findings by severity.
type Counts struct {
	Critical int `json:"critical,omitempty"`
	Warning  int `json:"warning,omitempty"`
	Info     int `json:"info,omitempty"`
}

// Health is how a run came out (JR-01): failed for one that failed or
// was interrupted, otherwise the worst severity among its findings
// (critical, warning, info), none when it found nothing; "" while it runs,
// for a time let go (skipped, missed), and for a run of a kind that makes
// no findings that did not fail. The counts are its findings by
// severity, whatever became of them since.
func (r Run) Health() (string, Counts) {
	var c Counts
	for _, f := range r.Findings {
		switch f.Severity {
		case FindingCritical:
			c.Critical++
		case FindingInfo:
			c.Info++
		default:
			c.Warning++
		}
	}
	switch r.Status {
	case StatusRunning, StatusSkipped, StatusMissed:
		return "", c
	case StatusFailed, StatusInterrupted:
		return HealthFailed, c
	}
	// A run of a kind that makes no findings found nothing to weigh
	// (KN-28): it has no health but failed.
	if r.NoFindings {
		return "", c
	}
	switch {
	case c.Critical > 0:
		return HealthCritical, c
	case c.Warning > 0:
		return HealthWarning, c
	case c.Info > 0:
		return HealthInfo, c
	}
	return HealthNone, c
}

// Outcome is what one run of a kind came to.
type Outcome struct {
	// Report is the run's report, in Markdown.
	Report    string
	Findings  []Finding
	Proposals []Proposal
	// Data is the kind's own result, for a caller that waits for the run
	// (the investigator's finding); it is not kept.
	Data any
	// Facts are what the run did, for its drawer (KN-28).
	Facts []Fact
	// Scope is what the run read, for verifying fixes; nil verifies
	// nothing. Rechecked are what the agent said of the fixes it was
	// given to re-check, and Failed the clusters that did not answer
	// (W-1, W-2).
	Scope     *Scope
	Rechecked []Recheck
	Failed    []string
}

// External is a job that runs elsewhere, on its own schedule, and is only
// shown and driven from AIOps › Jobs: the backups to S3.
type External struct {
	// Where says where it is set: "Administration › Backup", with a link.
	Where string
	Link  string
	// State is how it stands now.
	State func(ctx context.Context) (ExternalState, error)
	// RunNow starts a run, which goes on in the background; Pause and
	// resume turn its schedule off and on.
	RunNow func(ctx context.Context, by Person) error
	SetOn  func(ctx context.Context, on bool, by Person) error
}

// ExternalState is how an external job stands.
type ExternalState struct {
	// Ready says it is set up; Detail what it works on ("bucket x").
	Ready  bool   `json:"ready"`
	Detail string `json:"detail,omitempty"`
	On     bool   `json:"on"`
	// When is its schedule in words; Spec the schedule.
	When string         `json:"when,omitempty"`
	Spec *schedule.Spec `json:"spec,omitempty"`
	Next time.Time      `json:"next,omitzero"`
	// Last is its last run, in a line, and whether it worked.
	LastAt  time.Time `json:"lastAt,omitzero"`
	Last    string    `json:"last,omitempty"`
	LastOK  bool      `json:"lastOk,omitempty"`
	Running bool      `json:"running,omitempty"`
	SetBy   string    `json:"setBy,omitempty"`
	SetAt   time.Time `json:"setAt,omitzero"`
}

// Person is who does something to a job, as the audit log names them.
type Person struct {
	Subject string
	Email   string
	Name    string
	From    string
}

// Label is how the records name the person.
func (p Person) Label() string {
	for _, s := range []string{p.Email, p.Name, p.Subject} {
		if s != "" {
			return s
		}
	}
	return "someone"
}

// Kind is a kind of job: what it does, which agent does it, what starts it
// by default, and the function that does one run.
type Kind struct {
	ID    string
	Label string
	// Agent is the agent that does it, by id, with its name; empty for a
	// job no agent does (the backups).
	Agent     string
	AgentName string
	// On makes its own job on until someone turns it off (the
	// housekeeping); the others start off.
	On bool
	// Does says what it makes, for the list: "A report of RBAC risks,
	// nearly full volumes and practices not met".
	Does string
	// Task is the instructions the kind's agent is given on each run, as
	// built in: what to look at and what to report. The run puts what it
	// is about around it (the cluster, the alert, the report, the
	// accepted exceptions) and the output format after it; a job may
	// replace it with its own (Job.Task). Empty for a kind that sends no
	// task of its own, whose instructions cannot be changed (JR-05).
	Task string
	// Reads says what its agent reads, and Proposes what a run may make,
	// in words, for the job's definition (JR-05).
	Reads    string
	Proposes string
	// Default is what starts its own job until someone changes it, and
	// Budget that job's budget.
	Default When
	Budget  Budget
	// Events are the events it may start on; Schedulable says a schedule
	// may start it.
	Events      []string
	Schedulable bool
	// Checks are the kind's catalogue of check ids (CF-05): its agent
	// names one for each finding, or other.<slug>; a kind without one
	// makes no findings to route.
	Checks []Check
	// Run does one run; External is a job that runs elsewhere instead.
	Run      func(ctx context.Context, env *RunEnv) (Outcome, error)
	External *External
	// Order places it in the list.
	Order int
	// --- A Wiki sync job per source (Phase 12, KN-28) ---
	// Single says the kind's own job is its only one: it is one source's,
	// and another would read the same source again. NoFindings says its
	// runs make no findings (they are not audits).
	Single     bool
	NoFindings bool
	// Rebuild says a person may also start a run that redoes everything,
	// Rebuild now (a Wiki sync, KN-29), beside Run now.
	Rebuild bool
	// ForReporters says what its agent answers reaches reporters (the
	// report triage's summary and reply): ask_knowledge gives it no
	// accepted exception (KN-32).
	ForReporters bool
}

// AddMore says whether another job of the kind may be added beside its
// own (JR-07): not for a job run elsewhere, nor for one a report or a
// case starts, which runs the kind's first job that takes it, so another
// would never run, nor for a kind of one job (a source's Wiki sync).
func (k Kind) AddMore() bool {
	return k.External == nil && !k.Single && !slices.Contains(k.Events, EventReport) && !slices.Contains(k.Events, EventCase)
}

// Registry is the kinds of jobs. Packages register theirs at start-up;
// the service reads it whenever it lists or runs.
type Registry struct {
	mu    sync.Mutex
	kinds map[string]Kind
}

// Default is the console's registry.
var Default = NewRegistry()

// NewRegistry returns an empty registry (tests).
func NewRegistry() *Registry { return &Registry{kinds: map[string]Kind{}} }

// Register adds a kind, or replaces the one of its id.
func (r *Registry) Register(k Kind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds[k.ID] = k
}

// Register adds a kind to the console's registry.
func Register(k Kind) { Default.Register(k) }

// Unregister removes a kind: a knowledge source removed takes its Wiki
// sync job's kind with it (KN-28). Its runs are kept.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.kinds, id)
}

// Kind finds a kind.
func (r *Registry) Kind(id string) (Kind, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.kinds[id]
	return k, ok
}

// Kinds are the kinds, in their order.
func (r *Registry) Kinds() []Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Kind, 0, len(r.kinds))
	for _, k := range r.kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Errors of the service.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("not valid")
	ErrDenied   = errors.New("not allowed")
	// ErrOff is a job that is paused or not there, asked to run on an
	// event.
	ErrOff = errors.New("the job is paused")
	// ErrBudget is a job whose budget is spent.
	ErrBudget = errors.New("the job's budget is spent")
	// ErrRunning is a run asked while the job's last is still going.
	ErrRunning = errors.New("the job's last run is still going")
)
