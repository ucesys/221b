package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/schedule"
)

// The audit log's actions of the jobs (JB-03, JB-20).
const (
	// ActionJob is a job changed, added or removed, with its old and new
	// values.
	ActionJob = "job.update"
	// ActionRun is a run: started by whom or what, the agent, the person
	// who set the trigger, what it found and proposed, and its cost.
	ActionRun = "job.run"
	// ActionCancel is a run a person cancelled (FX-12).
	ActionCancel = "job.cancel"
	// ActionSettings is a change to who may edit jobs and what agents on
	// their own read with.
	ActionSettings = "job.settings"
)

// AgentRequest is an agent's call on its own (JB-08): on one cluster, with
// a task, as the reader identity the settings choose, never as a person.
type AgentRequest struct {
	Agent   string
	Cluster string
	Task    string
	// Job, RunID, Label and SetBy name the run for the agent call's audit
	// record: which job, and who set its trigger.
	Job   string
	RunID string
	Label string
	SetBy string
	// Reader is the jobs' settings, for the role and group to read with.
	Reader Settings
	// ForReporters says the answer reaches reporters (Kind.ForReporters):
	// the agent is given no accepted exception (KN-32).
	ForReporters bool
}

// AgentResult is what an agent's call came to.
type AgentResult struct {
	Text    string
	Model   string
	Tokens  Tokens
	CostUSD float64
	Tools   []string
	Err     error
}

// AgentRunner runs agents on their own; cmd/221b gives the console's, over
// Sherlock's engine with the reader identity's read-only tools.
type AgentRunner interface {
	Run(ctx context.Context, req AgentRequest) AgentResult
	// Usable says why an agent cannot work on a cluster now ("" when it
	// can): it is off, has no provider, does not work there.
	Usable(agent, cluster string) string
}

// Cluster is a cluster a job may cover.
type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Alert is an alert on a cluster's Alerts tab, as the alert jobs see it.
type Alert struct {
	Cluster   string
	Key       string
	Kind      string
	Severity  string
	Name      string
	Namespace string
	Status    string
	Reason    string
	Message   string
	// Node is the node a pod alert's pod runs on, to name the alert by
	// its reason (CF-84).
	Node string
	// Fingerprint is what the alert is now, as its acknowledgement keeps
	// it (CF-79); JobAck says a job acknowledged it as it is: the alert
	// jobs leave it, and since CF-82, when a job acknowledges only what
	// its run concluded on, so does the cases' safety net (CF-78).
	Fingerprint string
	JobAck      bool
}

// Object is what the alert is about: "shop/web-7f9" or "worker-3".
func (a Alert) Object() string {
	if a.Namespace != "" {
		return a.Namespace + "/" + a.Name
	}
	return a.Name
}

// Options configure the service.
type Options struct {
	// Backend keeps the jobs and runs: the runs ConfigMap; nil is memory.
	Backend kv.Backend
	// Kinds are the kinds of jobs; nil is Default.
	Kinds *Registry
	// Agents runs the agents; nil runs none (every agent job fails).
	Agents AgentRunner
	// Proposer files the proposals; nil keeps them in memory.
	Proposer Proposer
	// Clusters lists the clusters; nil is the local one alone.
	Clusters func() []Cluster
	// Alerts lists every cluster's alerts now, for the alert jobs; nil
	// watches none.
	Alerts func(ctx context.Context) []Alert
	// CommandOK says whether Watson runs a command line a proposal holds.
	CommandOK func(line string) error
	// Acker keeps the acknowledgements of the alerts the alert runs
	// handled (CF-79); nil acknowledges none.
	Acker Acker
	// AlertWatcher is given the alerts at each look, on the leader: the
	// cases' safety net (CF-78); nil is none.
	AlertWatcher AlertWatcher
	// Router routes each run's findings into cases (package casework,
	// CF-01); nil routes none.
	Router Router
	// Rules gives the validation rules as they stand (ADR-138): the
	// catalogues the agents are told and the findings are checked
	// against; nil keeps the kinds' built-in ones.
	Rules RuleBook
	// Objects says whether a finding's object is on its cluster (CF-05);
	// nil leaves every one unverified.
	Objects ObjectCheck
	// Leads says whether this replica is the leader (ADR-70); only the
	// leader runs schedules and takes events. nil: always.
	Leads func() bool
	Audit *audit.Log
	// TimeZone is the console's, the default of a new schedule; empty is
	// UTC.
	TimeZone string
	Now      func() time.Time
	Logger   *slog.Logger
	// Every is how often the schedules and alerts are looked at; 30 s.
	Every time.Duration
	// MaxRuns and RunsBytes bound the runs kept (JB-14).
	MaxRuns   int
	RunsBytes int
	// RunTimeout bounds one run; 30 minutes.
	RunTimeout time.Duration
}

// Service keeps the jobs and runs them.
type Service struct {
	o        Options
	instance string

	mu sync.Mutex
	// sem holds each job's run under way on this replica; queued counts
	// those waiting for it.
	sem    map[string]chan struct{}
	queued map[string]int
	// active are the runs under way here, each with how to cancel it.
	active map[string]*activeRun
	// seen are the alerts the watcher knew at its last look, with their
	// severity.
	seen  map[string]string
	swept time.Time
	// rerouted is when the runs not routed were last tried again (W-8).
	rerouted time.Time
	// alertCases are the cases the alert runs went to, as AlertCases last
	// read them, and when (CF-77).
	alertCases   map[string][]string
	alertCasesAt time.Time

	wg     sync.WaitGroup
	base   context.Context
	cancel context.CancelFunc
}

// New builds the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Kinds == nil {
		o.Kinds = Default
	}
	if o.Proposer == nil {
		o.Proposer = &MemoryProposer{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Every <= 0 {
		o.Every = 30 * time.Second
	}
	if o.MaxRuns <= 0 {
		o.MaxRuns = DefaultMaxRuns
	}
	if o.RunsBytes <= 0 {
		o.RunsBytes = DefaultRunsBytes
	}
	if o.RunTimeout <= 0 {
		o.RunTimeout = 30 * time.Minute
	}
	if o.TimeZone == "" {
		o.TimeZone = "UTC"
	}
	tag := make([]byte, 6)
	_, _ = rand.Read(tag)
	base, cancel := context.WithCancel(context.Background())
	return &Service{o: o, instance: hex.EncodeToString(tag), sem: map[string]chan struct{}{}, queued: map[string]int{}, active: map[string]*activeRun{}, base: base, cancel: cancel}
}

// SetAgents gives the service its agents, once the console has them.
func (s *Service) SetAgents(a AgentRunner) { s.o.Agents = a }

// SetObjects gives the service the check of the findings' objects.
func (s *Service) SetObjects(f ObjectCheck) { s.o.Objects = f }

// SetProposer gives the service the cards' store.
func (s *Service) SetProposer(p Proposer) {
	if p != nil {
		s.o.Proposer = p
	}
}

// Kinds is the registry the service reads.
func (s *Service) Kinds() *Registry { return s.o.Kinds }

// Backend is the storage, for a backup.
func (s *Service) Backend() kv.Backend { return s.o.Backend }

// Wait waits for the runs under way.
func (s *Service) Wait() { s.wg.Wait() }

// Stop stops the runs under way and waits for them.
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) leads() bool { return s.o.Leads == nil || s.o.Leads() }

func (s *Service) record(e audit.Event) {
	if s.o.Audit != nil {
		s.o.Audit.Record(e)
	}
}

// ---------- the jobs ----------

// defaultJob is a kind's own job before anyone changes it: off, unless
// the kind says on.
func (s *Service) defaultJob(k Kind) Job {
	j := Job{ID: k.ID, Kind: k.ID, On: k.On, When: k.Default, Budget: k.Budget, SkipOverlap: true, CatchUp: true}
	if sp := k.Default.Schedule; sp != nil {
		cp := *sp
		if cp.TimeZone == "" {
			cp.TimeZone = s.o.TimeZone
		}
		j.When.Schedule = &cp
	}
	return j
}

// jobsOf are the jobs: each kind's own, as changed or not, then the ones
// added, whose kind is still registered.
func (s *Service) jobsOf(rec jobsRecord) []Job {
	var out []Job
	kinds := s.o.Kinds.Kinds()
	for _, k := range kinds {
		j, ok := rec.Jobs[k.ID]
		if !ok {
			j = s.defaultJob(k)
		}
		j.ID, j.Kind, j.Added = k.ID, k.ID, false
		out = append(out, j)
	}
	var added []Job
	for id, j := range rec.Jobs {
		if _, ok := s.o.Kinds.Kind(j.Kind); ok && j.Added && id == j.ID {
			added = append(added, j)
		}
	}
	slices.SortFunc(added, func(a, b Job) int { return strings.Compare(a.ID, b.ID) })
	order := func(j Job) int {
		for i, k := range kinds {
			if k.ID == j.Kind {
				return i
			}
		}
		return len(kinds)
	}
	for _, j := range added {
		at := len(out)
		for i := range out {
			if order(out[i]) > order(j) {
				at = i
				break
			}
		}
		out = slices.Insert(out, at, j)
	}
	return out
}

// job finds one job, with its kind.
func (s *Service) job(rec jobsRecord, id string) (Job, Kind, bool) {
	for _, j := range s.jobsOf(rec) {
		if j.ID == id {
			k, _ := s.o.Kinds.Kind(j.Kind)
			return j, k, true
		}
	}
	return Job{}, Kind{}, false
}

// clusters are the clusters there are.
func (s *Service) clusters() []Cluster {
	if s.o.Clusters == nil {
		return []Cluster{{ID: "local", Name: "local"}}
	}
	return s.o.Clusters()
}

// clusterName is a cluster's name, its id when unknown.
func (s *Service) clusterName(id string) string {
	for _, c := range s.clusters() {
		if c.ID == id && c.Name != "" {
			return c.Name
		}
	}
	return id
}

// covered are the clusters a run of the job covers: the event's, or the
// job's, every one when it names none.
func (s *Service) covered(j Job, ev *Event) []string {
	if ev != nil && ev.Cluster != "" {
		return []string{ev.Cluster}
	}
	if len(j.Clusters) > 0 {
		return append([]string(nil), j.Clusters...)
	}
	var out []string
	for _, c := range s.clusters() {
		out = append(out, c.ID)
	}
	return out
}

// day is the day spend is counted on: UTC, as the agents' ledger does.
func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// spentToday is what a job spent today.
func spentToday(rec jobsRecord, id string, now time.Time) daySpend {
	d := rec.Spend[id]
	if d.Day != day(now) {
		return daySpend{Day: day(now)}
	}
	return d
}

// round keeps cents' fractions out of the records.
func round(usd float64) float64 { return math.Round(usd*10000) / 10000 }

// ---------- what the list shows ----------

// RunBrief is a run as a list row shows it.
type RunBrief struct {
	ID       string    `json:"id"`
	Job      string    `json:"job"`
	Label    string    `json:"label"`
	Agent    string    `json:"agent,omitempty"`
	Trigger  Trigger   `json:"trigger"`
	Subject  string    `json:"subject,omitempty"`
	Repeats  int       `json:"repeats,omitempty"`
	Ref      string    `json:"ref,omitempty"`
	Clusters []string  `json:"clusters,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitzero"`
	Status   string    `json:"status"`
	Note     string    `json:"note,omitempty"`
	Findings int       `json:"findings"`
	// Open counts the cases its findings opened or reopened; Routed what
	// they all did (CF-01).
	Open      int     `json:"open"`
	Routed    Routed  `json:"routed"`
	Proposals int     `json:"proposals"`
	CostUSD   float64 `json:"costUSD"`
	Model     string  `json:"model,omitempty"`
	// NotRouted says why its findings were not routed into cases (W-8).
	NotRouted string `json:"notRouted,omitempty"`
	// Health is the worst of what it found, or failed, and Severities its
	// findings by severity (JR-01).
	Health     string `json:"health,omitempty"`
	Severities Counts `json:"severities"`
	// What the run could not be used for (I-3): a run done with a cluster
	// that failed reads "Done, with errors" (W-2).
	FailedClusters []string `json:"failedClusters,omitempty"`
	LeftOut        *LeftOut `json:"leftOut,omitempty"`
	NotRechecked   int      `json:"notRechecked,omitempty"`
}

func brief(r Run) RunBrief {
	b := RunBrief{ID: r.ID, Job: r.Job, Label: r.Label, Agent: r.Agent, Trigger: r.Trigger, Subject: r.Subject, Repeats: r.Repeats, Ref: r.Ref, Clusters: r.Clusters,
		Started: r.Started, Ended: r.Ended, Status: r.Status, Note: r.Note, Findings: len(r.Findings), Open: r.Open(), Routed: r.Routed, NotRouted: r.NotRouted, Proposals: len(r.Proposals), CostUSD: r.CostUSD, Model: r.Model,
		FailedClusters: r.FailedClusters, LeftOut: r.LeftOut, NotRechecked: r.NotRechecked}
	b.Health, b.Severities = r.Health()
	return b
}

// View is a job as AIOps › Jobs lists it.
type View struct {
	Job
	Label     string   `json:"label"`
	Agent     string   `json:"agent,omitempty"`
	AgentName string   `json:"agentName,omitempty"`
	Does      string   `json:"does,omitempty"`
	Events    []string `json:"events,omitempty"`
	// BuiltinTask is the kind's own instructions, which Task (the job's)
	// replaces when set; Reads and Proposes say what its agent reads and
	// what a run may make (JR-05). An empty BuiltinTask is a kind whose
	// instructions cannot be changed.
	BuiltinTask string `json:"builtinTask,omitempty"`
	Reads       string `json:"reads,omitempty"`
	Proposes    string `json:"proposes,omitempty"`
	// Schedulable says a schedule may start it; Manual that Run now may.
	Schedulable bool `json:"schedulable"`
	Manual      bool `json:"manual"`
	// Rebuild says Rebuild now is offered beside Run now (KN-29).
	Rebuild bool `json:"rebuild,omitempty"`
	// WhenText is what starts it, in plain words (JB-05).
	WhenText string    `json:"whenText"`
	Next     time.Time `json:"next,omitzero"`
	// NextText is the next run in words when it has no time: "On the next
	// alert", "Paused".
	NextText string    `json:"nextText,omitempty"`
	Last     *RunBrief `json:"last,omitempty"`
	Running  bool      `json:"running"`
	// SpentToday is the day's spend, StoppedToday why the day's budget
	// stopped it.
	SpentToday   float64 `json:"spentToday"`
	StoppedToday string  `json:"stoppedToday,omitempty"`
	// Unusable says why its agent cannot work now.
	Unusable string `json:"unusable,omitempty"`
	// External is a job run elsewhere (the backups to S3), with where it
	// is set.
	External *ExternalState `json:"external,omitempty"`
	Where    string         `json:"where,omitempty"`
	Link     string         `json:"link,omitempty"`
}

// describeWhen says what starts a job in plain words.
func describeWhen(w When) string {
	var parts []string
	if w.Schedule != nil {
		if sc, err := schedule.Parse(*w.Schedule); err == nil {
			parts = append(parts, sc.Describe())
		} else {
			parts = append(parts, "A schedule that is not valid: "+err.Error())
		}
	}
	switch w.Event {
	case EventAlert:
		e := "When an alert fires, warning and above"
		if w.Severity == SeverityError {
			e = "When an alert fires, errors only"
		}
		parts = append(parts, e)
	case EventReport:
		parts = append(parts, "When a report arrives")
	case EventSource:
		parts = append(parts, "When its source changes")
	case EventCase:
		parts = append(parts, "When a case is opened")
	}
	if len(parts) == 0 {
		return "Only when run by hand"
	}
	if len(parts) == 2 {
		return parts[0] + ", and " + strings.ToLower(parts[1][:1]) + parts[1][1:]
	}
	return parts[0]
}

var nextEvent = map[string]string{EventAlert: "On the next alert", EventReport: "On the next report", EventSource: "On the next change", EventCase: "On the next case"}

// view builds a job's row.
func (s *Service) view(ctx context.Context, rec jobsRecord, runs []Run, j Job, k Kind) View {
	v := View{Job: j, Label: k.Label, Agent: k.Agent, AgentName: k.AgentName, Does: k.Does, Events: k.Events, Schedulable: k.Schedulable, Manual: k.Schedulable || k.Run != nil && slices.Contains(k.Events, EventAlert),
		BuiltinTask: k.Task, Reads: k.Reads, Proposes: k.Proposes, Rebuild: k.Rebuild && k.Run != nil}
	if j.Name != "" {
		v.Label = j.Name
	}
	// The lowest priority is always said, the default filled in (CF-33).
	lowest := j.Lowest()
	v.MinPriority = &lowest
	v.WhenText = describeWhen(j.When)
	now := s.o.Now()
	if k.External != nil {
		v.Where, v.Link, v.Manual = k.External.Where, k.External.Link, true
		if st, err := k.External.State(ctx); err == nil {
			v.External = &st
			v.On = st.On
			v.SetBy, v.SetAt = st.SetBy, st.SetAt
			if st.When != "" {
				v.WhenText = st.When
			}
			v.When = When{Schedule: st.Spec}
			v.Next, v.Running = st.Next, st.Running
		} else {
			v.Unusable = err.Error()
		}
		if !v.On {
			v.NextText = "Paused"
		}
		return v
	}
	for _, r := range runs {
		if r.Job == j.ID {
			b := brief(r)
			v.Last = &b
			break
		}
	}
	s.mu.Lock()
	v.Running = len(s.sem[j.ID]) > 0
	s.mu.Unlock()
	if !v.Running && v.Last != nil && v.Last.Status == StatusRunning {
		v.Running = true
	}
	d := spentToday(rec, j.ID, now)
	v.SpentToday, v.StoppedToday = round(d.USD), d.Stopped
	switch {
	case !j.On:
		v.NextText = "Paused"
	default:
		if j.When.Schedule != nil {
			if sc, err := schedule.Parse(*j.When.Schedule); err == nil {
				v.Next = sc.Next(now)
			}
		}
		if v.Next.IsZero() {
			v.NextText = nextEvent[j.When.Event]
		}
	}
	if k.Agent != "" {
		if s.o.Agents == nil {
			v.Unusable = "The agents are not running on this console."
		} else if cl := s.covered(j, nil); len(cl) > 0 {
			v.Unusable = s.o.Agents.Usable(k.Agent, cl[0])
		}
	}
	return v
}

// List lists the jobs.
func (s *Service) List(ctx context.Context) ([]View, error) {
	rec, err := s.loadJobs(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return nil, err
	}
	var out []View
	for _, j := range s.jobsOf(rec) {
		k, _ := s.o.Kinds.Kind(j.Kind)
		out = append(out, s.view(ctx, rec, runs.Runs, j, k))
	}
	return out, nil
}

// Get is one job.
func (s *Service) Get(ctx context.Context, id string) (View, error) {
	rec, err := s.loadJobs(ctx)
	if err != nil {
		return View{}, err
	}
	j, k, ok := s.job(rec, id)
	if !ok {
		return View{}, fmt.Errorf("%w: there is no job %q", ErrNotFound, id)
	}
	runs, err := s.loadRuns(ctx)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, rec, runs.Runs, j, k), nil
}

// Settings are the jobs' settings.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	rec, err := s.loadJobs(ctx)
	return rec.Settings, err
}

// SaveSettings changes who may edit jobs and what agents on their own
// read with, and says what changed.
func (s *Service) SaveSettings(ctx context.Context, in Settings, by Person) (Settings, error) {
	var editors []string
	for _, e := range in.Editors {
		switch e {
		case "admin", "superViewer", "viewer":
			if !slices.Contains(editors, e) {
				editors = append(editors, e)
			}
		case "superAdmin":
		default:
			return Settings{}, fmt.Errorf("%w: %q is not a role that may edit jobs (admin, superViewer or viewer)", ErrInvalid, e)
		}
	}
	slices.Sort(editors)
	role := in.ReaderRole
	if role == "" {
		role = ReaderViewer
	}
	if role != ReaderViewer && role != ReaderSuperViewer {
		return Settings{}, fmt.Errorf("%w: agents on their own read as a Viewer or a SuperViewer, never with a role that may change anything", ErrInvalid)
	}
	group := strings.TrimSpace(in.ReaderGroup)
	if role == ReaderSuperViewer {
		group = ""
	}
	var before, saved Settings
	err := s.updateJobs(ctx, func(rec *jobsRecord) error {
		before = rec.Settings
		rec.Settings = Settings{Editors: editors, ReaderRole: role, ReaderGroup: group, SetBy: by.Label(), SetAt: s.o.Now().UTC()}
		saved = rec.Settings
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	s.record(audit.Event{Action: ActionSettings, Who: audit.Who{Subject: by.Subject, Email: by.Email, Name: by.Name}, Target: "jobs", From: by.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("who may edit jobs: SuperAdmin%s → SuperAdmin%s; agents on their own read as %s%s → %s%s",
			listOr(before.Editors), listOr(saved.Editors), before.Role(), groupOf(before.ReaderGroup), saved.Role(), groupOf(saved.ReaderGroup))})
	return saved, nil
}

func listOr(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return ", " + strings.Join(l, ", ")
}

func groupOf(g string) string {
	if g == "" {
		return ""
	}
	return " (group " + g + ")"
}

// MayEdit says whether a person holding these roles may edit jobs: a
// SuperAdmin always, others when the settings list a role they hold.
func (s Settings) MayEdit(admin bool, held []string) bool {
	if admin {
		return true
	}
	for _, h := range held {
		if slices.Contains(s.Editors, h) {
			return true
		}
	}
	return false
}
