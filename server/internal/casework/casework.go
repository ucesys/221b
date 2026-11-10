// Package casework is 221B's side of the cases (Phase 12, CW-01 to CW-14,
// ADR-105 to ADR-107; CU-01 to CU-14, ADR-113 to ADR-115; and since wave
// 6, one flow for findings and cases, CF-01 to CF-24).
//
// Every problem is one case, C-<n>, whether a person reported it (the
// report page, the avatar menu, Sherlock's "Report this", the issue API
// v1), opened it in the console (New Case, an alert) or an agent found it
// (each finding of a job's run becomes a case or joins one: Route). A
// case is New while nobody has taken it, In progress once someone has,
// and Closed with exactly one outcome: fix, duplicate, noAction (since
// CF-90; notAProblem and noFixNeeded read so), knownIssue or exception; a person may reopen any closed
// case, with a reason.
//
// The case itself (its number, title, priority P0 to P4, status and
// outcome, what it is about, its conversation, its known issue, its SLA)
// is the issue service's (package issues), kept in an object of its own,
// <release>-case-<n>, with a line in the issue service's index. This
// package keeps beside it, in the same object (issues.WorkKey), what 221B
// adds: the case analysis (the Troubleshooter's cause, evidence and
// suggested fix), the changes it proposed (Watson cards), the fix applied
// and its RCA (the case's only, CF-44), the history, and its one draft,
// shared by the people who work it (CF-59); and, in the ConfigMap <release>-casework, the
// settings of Settings › Cases, the check ids seen per cluster and the
// analyses under way. It routes the runs' findings, serves the console's
// Cases under /api/v1/cases/cases and runs the case analysis job.
//
// Every change is in the audit log (case.*, and the issue service's
// issue.*), and writes run on the leader (ADR-70), where the analysis,
// the routing and the retention run too.
package casework

import (
	"context"
	"errors"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// The keys of the casework ConfigMap this package keeps (the exceptions
// beside them are package lessons').
const (
	// SettingsKey holds Settings › Cases, the counter of agent-found
	// cases closed as duplicates, the analyses under way and when the
	// retention last ran.
	SettingsKey = "cases-settings.json"
	// ChecksKey holds the check ids seen per cluster, to tell a new one.
	ChecksKey = "cases-checks.json"
	// CleanKey is the clean start's marker (CF-23).
	CleanKey = "clean-start.json"
)

// The most each key holds by default; the console passes its layout's.
const (
	DefaultSettingsBytes = 16 << 10
	DefaultChecksBytes   = 64 << 10
	DefaultCleanBytes    = 4 << 10
)

// What opened a case: the issue service's names.
const (
	SourceReport  = issues.CaseReport
	SourceFinding = issues.CaseFinding
	SourceManual  = issues.CaseManual
	SourceAlert   = issues.CaseAlert
)

// The audit log's actions of the cases (CW-12, CU-13, CF-24); the issue
// service records its own (issue.*) besides.
const (
	ActionCreate    = "case.create"
	ActionUpdate    = "case.update"
	ActionAnalyze   = "case.analyze"
	ActionDraft     = "case.draft"
	ActionReopen    = "case.reopen"
	ActionFix       = "case.fix"
	ActionClose     = "case.close"
	ActionKnown     = "case.known"
	ActionKeep      = "case.keep"
	ActionJoin      = "case.join"
	ActionException = "case.exception"
	ActionOffer     = "case.offer"
	ActionRoute     = "case.route"
	ActionSettings  = "case.settings"
	ActionRetention = "case.retention"
	ActionClean     = "cases.clean"
	// --- A proposed change names the cases it resolves (wave 6b): a
	// person links a change to a case or unlinks it (CF-28), and a change
	// applied records the fix on the cases it resolves (CF-29).
	ActionChangeLink   = "cases.change-link"
	ActionChangeUnlink = "cases.change-unlink"
	ActionFixApplied   = "case.fix-applied"
	// --- The owner's feedback on Cases (wave 6c): a case's type and
	// incident number changed (CF-36, CF-37), and one action applied to
	// many cases at once (CF-38).
	ActionType     = "case.type"
	ActionIncident = "case.incident"
	ActionBulk     = "cases.bulk"
)

// Limits of one case; longer text is cut.
const (
	MaxTitle    = 200
	MaxDetail   = 4000
	MaxResource = 300
	MaxRCAField = 4000
	MaxEvidence = 10
	MaxCommands = 5
	// maxHistory is the events one case keeps: the newest, with the first.
	maxHistory = 60
	// maxRuns is the analysis runs one case names; maxCards the proposed
	// changes.
	maxRuns  = 10
	maxCards = 20
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("not valid")
	ErrDenied   = errors.New("not allowed")
	// ErrState is a change the case's status does not allow now.
	ErrState = errors.New("the case cannot do that now")
	// ErrOff is the case analysis job paused, or no agent to run it.
	ErrOff = errors.New("the case analysis job is paused")
	// ErrFull is a case that does not fit.
	ErrFull = errors.New("too large to keep")
)

// Person is who does something to a case, as the audit log names them.
type Person = jobs.Person

// Source is what opened a case: its kind, and for a finding the finding,
// its run, job, check and severity, for an alert the alert.
type Source struct {
	// Kind is SourceReport, SourceFinding, SourceManual or SourceAlert.
	Kind     string `json:"kind"`
	Finding  string `json:"finding,omitempty"`
	Run      string `json:"run,omitempty"`
	Job      string `json:"job,omitempty"`
	Check    string `json:"check,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Alert is the alert's name, for a case opened from one: since CF-84
	// its reason; AlertKey its key on the Alerts tab ("pod:shop/web-1"),
	// to link it.
	Alert    string `json:"alert,omitempty"`
	AlertKey string `json:"alertKey,omitempty"`
}

// Fix is the agent's suggested fix: what to do, and the Watson commands
// a person runs as themselves.
type Fix struct {
	Summary  string   `json:"summary,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

// Analysis is what the case analysis job found, the last time it ran.
type Analysis struct {
	// Run is the job's run.
	Run string    `json:"run,omitempty"`
	At  time.Time `json:"at"`
	// Summary is for operators: what is wrong, where, why, what the agent
	// checked; Cause the cause in a line.
	Summary  string   `json:"summary,omitempty"`
	Cause    string   `json:"cause,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Fix      Fix      `json:"fix"`
	// Expected is the analysis's verdict that there is no problem: the
	// system works as intended, ExpectedWhy says why, with its evidence
	// (CF-82). It lowers the priority of a case the console or an agent
	// opened (CF-83).
	Expected    bool   `json:"expected,omitempty"`
	ExpectedWhy string `json:"expectedWhy,omitempty"`
	// Error says why it did not finish.
	Error string `json:"error,omitempty"`
}

// RCA is a case's root-cause analysis: what a person gave closing it as
// fixed. It is the case's only (CF-44): the agents read it when they
// analyse a case like it (CF-43).
type RCA struct {
	// ID is the id Mycroft › RCAs gave it, on a case fixed before wave 6e;
	// none since.
	ID string `json:"id,omitempty"`
	// Case, Title, Cluster and Resource name what it is about.
	Case     string `json:"case"`
	Title    string `json:"title"`
	Cluster  string `json:"cluster,omitempty"`
	Resource string `json:"resource,omitempty"`
	Priority int    `json:"priority"`
	// Cause, Fix and Prevention are the RCA; Fix is what was done, the
	// case's fix note; Text is anything else a person adds.
	Cause      string `json:"cause"`
	Fix        string `json:"fix"`
	Prevention string `json:"prevention"`
	Text       string `json:"text,omitempty"`
	// Cases are the other cases closed as fixed with it at once: one root
	// cause, one RCA, on each of them.
	Cases []string  `json:"cases,omitempty"`
	By    string    `json:"by"`
	At    time.Time `json:"at"`
}

// Event is one line of a case's history: who did what, when; By names
// them with their kind, a person, an agent or the system (CF-35).
type Event struct {
	At   time.Time    `json:"at"`
	What string       `json:"what"`
	By   issues.Actor `json:"by"`
}

// sys is the console when it changes a case on its own.
var sys = issues.SystemActor

// Note is a fix applied, as a person recorded it, or as the change that
// applied it says (CF-29): Change is that change's card.
type Note struct {
	Text   string    `json:"text"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Change string    `json:"change,omitempty"`
}

// Completion is the RCA a person gives with a fix (CF-03).
type Completion struct {
	Cause      string `json:"cause"`
	Fix        string `json:"fix"`
	Prevention string `json:"prevention"`
	Text       string `json:"text,omitempty"`
}

// Empty says it holds nothing. Fix counts for nothing: the RCA's fix is
// what was done (CF-44).
func (c Completion) Empty() bool {
	return c.Cause == "" && c.Prevention == "" && c.Text == ""
}

// Closing is a close being written, kept in a draft. An outcome before
// CF-90, notAProblem or noFixNeeded, reads as noAction.
type Closing struct {
	Outcome     issues.Outcome `json:"outcome"`
	Explanation string         `json:"explanation"`
	Original    string         `json:"original,omitempty"`
}

// The parts of a case's draft (CF-59).
const (
	partReply = "reply"
	partFix   = "fix"
	partClose = "close"
	partRCA   = "rca"
)

// Draft is what is being written on a case and has not been sent yet: a
// reply or note, a fix note, a close's explanation, an RCA. Since CF-59 a
// case has one draft, shared by everyone who works it (those who only read
// the cases read it; a reporter never sees it). Save keeps it for
// everyone; sending a part takes that part away for everyone.
type Draft struct {
	Reply string      `json:"reply,omitempty"`
	Fix   string      `json:"fix,omitempty"`
	Close *Closing    `json:"close,omitempty"`
	RCA   *Completion `json:"rca,omitempty"`
	// By is who saved it last, or sent a part of it; Updated when, set by
	// the server: the stamp a save starts from (DraftInput.Base).
	By      string    `json:"by,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	// Drafters are who wrote each part, by its name (reply, fix, close,
	// rca), in the order they first saved it.
	Drafters map[string][]string `json:"drafters,omitempty"`
	// At is when a person's own draft was kept, before CF-59.
	At time.Time `json:"at,omitzero"`
}

// Empty says whether a draft holds nothing.
func (d Draft) Empty() bool {
	return d.Reply == "" && d.Fix == "" && (d.Close == nil || d.Close.Explanation == "" && d.Close.Outcome == "") && (d.RCA == nil || d.RCA.Empty())
}

// part is what one part of a draft holds, as one text to compare; "" for
// nothing.
func (d Draft) part(name string) string {
	switch name {
	case partReply:
		return d.Reply
	case partFix:
		return d.Fix
	case partClose:
		if d.Close == nil || d.Close.Outcome == "" && d.Close.Explanation == "" {
			return ""
		}
		return string(d.Close.Outcome) + "\x00" + d.Close.Explanation + "\x00" + d.Close.Original
	case partRCA:
		if d.RCA == nil || d.RCA.Empty() {
			return ""
		}
		return d.RCA.Cause + "\x00" + d.RCA.Fix + "\x00" + d.RCA.Prevention + "\x00" + d.RCA.Text
	}
	return ""
}

// clear empties one part of a draft.
func (d *Draft) clear(name string) {
	switch name {
	case partReply:
		d.Reply = ""
	case partFix:
		d.Fix = ""
	case partClose:
		d.Close = nil
	case partRCA:
		d.RCA = nil
	}
	delete(d.Drafters, name)
}

// draftParts are the parts, in their order.
var draftParts = []string{partReply, partFix, partClose, partRCA}

// Work is what 221B keeps about one case beside the issue service's
// record, in the case's object: what opened it (an alert's name), its
// analyses, the changes it proposed, the fix applied, its RCA, its
// history and its draft.
type Work struct {
	// Number is the case's (C-<number>).
	Number int    `json:"number"`
	Source Source `json:"source"`
	// Analysis is the last analysis.
	Analysis *Analysis `json:"analysis,omitempty"`
	// Runs are the analysis runs, newest first.
	Runs []string `json:"runs,omitempty"`
	// Proposals are the Watson cards that resolve it, newest first: its
	// analyses', the runs', and those a person linked (CF-28).
	Proposals []string `json:"proposals,omitempty"`
	// LinkedBy names the person who linked each card they linked, so a
	// card let go of still says who; Handled is the state each card was
	// last acted on in (applied, failed, rejected, expired), so each acts
	// once (CF-29).
	LinkedBy   map[string]string `json:"linkedBy,omitempty"`
	Handled    map[string]string `json:"handled,omitempty"`
	FixApplied *Note             `json:"fixApplied,omitempty"`
	RCA        *RCA              `json:"rca,omitempty"`
	// Fix is the fix it was saved as, closed as Fixed with Save fix on
	// (CF-50).
	Fix     string  `json:"fix,omitempty"`
	History []Event `json:"history,omitempty"`
	// Draft is the case's one draft (CF-59).
	Draft *Draft `json:"draft,omitempty"`
	// Drafts are each person's own, by the issue service's id of them, as
	// they were kept before CF-59: read once into Draft (shareDrafts).
	Drafts map[string]Draft `json:"drafts,omitempty"`
	// Commands are the commands run from the case's drawer through Watson
	// (wave 6f, CF-48): all of them while it is open, the changes that
	// finished done once it is closed, which it keeps.
	Commands []Command `json:"commands,omitempty"`
	Updated  time.Time `json:"updated"`
}

// Runner runs the case analysis job: the jobs' service.
type Runner interface {
	RunEvent(ctx context.Context, kind string, ev jobs.Event) (jobs.Run, jobs.Outcome, error)
	On(ctx context.Context, kind string, ev jobs.Event) bool
	GetRun(ctx context.Context, id string) (jobs.Run, error)
	// Ready says whether the job would run an event now: on, taking it,
	// its day's budget not spent (CF-32).
	Ready(ctx context.Context, kind string, ev jobs.Event) error
}

// PriorityOf is an agent-found case's priority from its finding's
// severity (CF-02): critical P1, warning P3, info P4; agents never set
// P0.
func PriorityOf(severity string) int {
	switch severity {
	case jobs.FindingCritical:
		return 1
	case jobs.FindingInfo:
		return 4
	}
	return 3
}

// AlertPriority is a case's priority from its alert's severity (CF-02): a
// critical alert P0, a warning P2, anything else P3.
func AlertPriority(severity string) int {
	switch severity {
	case "critical", "error", "failed":
		return 0
	case "warning", "warn":
		return 2
	}
	return 3
}

// Settings are Settings › Cases (CF-19): SuperAdmin edits, the
// console-wide roles read.
type Settings struct {
	// KeepAgentClosedDays is how long a closed agent-found case is kept
	// before it is deleted.
	KeepAgentClosedDays int `json:"keepAgentClosedDays"`
	// LookBackDays is how far back New Case shows cases closed as fixed
	// or no action needed.
	LookBackDays int `json:"lookBackDays"`
	// ExceptionStaleDays is after how many days without a match an
	// exception is stale.
	ExceptionStaleDays int `json:"exceptionStaleDays"`
	// ExceptionsPerAgent is how many active exceptions an agent's task
	// lists; NewCasesPerRun how many new cases one run opens.
	ExceptionsPerAgent int `json:"exceptionsPerAgent"`
	NewCasesPerRun     int `json:"newCasesPerRun"`
	// CriticalAlertMinutes is after how many minutes a critical alert still
	// going that no case covers opens a case by itself (CF-78); 0 turns
	// that off, none is the default (DefaultCriticalAlertMinutes).
	CriticalAlertMinutes *int `json:"criticalAlertMinutes,omitempty"`
	// DuplicatesOfAgentCases counts the agent-found cases closed as
	// duplicates (read-only).
	DuplicatesOfAgentCases int `json:"duplicatesOfAgentCases,omitempty"`
}

// DefaultCriticalAlertMinutes is after how many minutes a critical alert
// opens a case by itself, until a SuperAdmin says otherwise (CF-78).
const DefaultCriticalAlertMinutes = 15

// DefaultSettings are a new console's.
func DefaultSettings() Settings {
	m := DefaultCriticalAlertMinutes
	return Settings{KeepAgentClosedDays: 90, LookBackDays: 90, ExceptionStaleDays: 90, ExceptionsPerAgent: 50, NewCasesPerRun: 50, CriticalAlertMinutes: &m}
}

// AlertAfter is how long a critical alert goes on before it opens a case
// by itself (CF-78); 0 is never.
func (s Settings) AlertAfter() time.Duration {
	if s.CriticalAlertMinutes == nil {
		return DefaultCriticalAlertMinutes * time.Minute
	}
	return time.Duration(*s.CriticalAlertMinutes) * time.Minute
}

func (s Settings) withDefaults() Settings {
	d := DefaultSettings()
	for _, f := range []struct {
		v *int
		d int
	}{{&s.KeepAgentClosedDays, d.KeepAgentClosedDays}, {&s.LookBackDays, d.LookBackDays}, {&s.ExceptionStaleDays, d.ExceptionStaleDays},
		{&s.ExceptionsPerAgent, d.ExceptionsPerAgent}, {&s.NewCasesPerRun, d.NewCasesPerRun}} {
		if *f.v == 0 {
			*f.v = f.d
		}
	}
	if s.CriticalAlertMinutes == nil {
		m := DefaultCriticalAlertMinutes
		s.CriticalAlertMinutes = &m
	}
	return s
}

// Validate says what is wrong with settings: days 1 to 3650, counts 1 to
// 500.
func (s Settings) Validate() error {
	for _, f := range []struct {
		name      string
		v, lo, hi int
	}{{"keepAgentClosedDays", s.KeepAgentClosedDays, 1, 3650}, {"lookBackDays", s.LookBackDays, 1, 3650}, {"exceptionStaleDays", s.ExceptionStaleDays, 1, 3650},
		{"exceptionsPerAgent", s.ExceptionsPerAgent, 1, 500}, {"newCasesPerRun", s.NewCasesPerRun, 1, 500}} {
		if f.v < f.lo || f.v > f.hi {
			return invalidf("%s is %d to %d", f.name, f.lo, f.hi)
		}
	}
	if m := s.CriticalAlertMinutes; m != nil && (*m < 0 || *m > 1440) {
		return invalidf("criticalAlertMinutes is 0 (off) to 1440")
	}
	return nil
}
