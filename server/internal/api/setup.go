package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- Getting started (Phase 12, GS-01 to GS-12, ADR-103, ADR-104) ---
//
// Getting started is a checklist of setting the console up, shown to
// SuperAdmins until every step is done or dismissed. Each step is worked
// out from the console as it is, every time it is read, never ticked by
// hand: only a dismissal is kept, under SetupKey beside the settings, with
// the first backup taken by hand, which leaves no other trace. Sherlock's
// setup skill reads the same steps (setup_status) while one is open.

// SetupKey keeps the dismissed steps and the first backup taken by hand,
// in the settings ConfigMap.
const SetupKey = "setup.json"

// Step states.
const (
	StepDone      = "done"
	StepOpen      = "open"
	StepDismissed = "dismissed"
)

// SetupStep is one step as the page and Sherlock see it.
type SetupStep struct {
	ID    string `json:"id"`
	N     int    `json:"n"`
	Title string `json:"title"`
	// State is done, open or dismissed: a dismissed step that is done
	// shows done.
	State string `json:"state"`
	// Detail says what the console has now, and what is missing.
	Detail string `json:"detail"`
	// Why says why the step matters, and comes where it does.
	Why string `json:"why"`
	// Link opens the screen where the step is done; Where names it, and
	// Action the field or button to use there.
	Link   string `json:"link"`
	Where  string `json:"where"`
	Action string `json:"action"`
	// Docs is the page of the documentation that explains the step.
	Docs        string     `json:"docs"`
	DismissedBy string     `json:"dismissedBy,omitempty"`
	DismissedAt *time.Time `json:"dismissedAt,omitempty"`
}

// SetupView is Getting started.
type SetupView struct {
	Steps     []SetupStep `json:"steps"`
	Done      int         `json:"done"`
	Dismissed int         `json:"dismissed"`
	Open      int         `json:"open"`
	// Next is the first open step's id; "" when none is.
	Next string `json:"next,omitempty"`
	// Complete says every step is done or dismissed: the checklist leaves
	// the home page and Sherlock's setup skill ends.
	Complete bool `json:"complete"`
	// Kept says the dismissals are kept in the settings ConfigMap; false,
	// in memory until a restart.
	Kept bool `json:"kept"`
}

// setupMark is who did something, and when.
type setupMark struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// setupRecord is what SetupKey holds.
type setupRecord struct {
	Dismissed map[string]setupMark `json:"dismissed,omitempty"`
	// FirstBackup is the first backup downloaded from Administration ›
	// Backup; a backup to S3 shows in its own status.
	FirstBackup *setupMark `json:"firstBackup,omitempty"`
}

// setupState is the server's: a memory store when it keeps no settings,
// and the last view for a few seconds, since Sherlock asks at every
// question.
type setupState struct {
	mu   sync.Mutex
	mem  kv.Memory
	view *SetupView
	at   time.Time
}

// setupFresh is how long a worked-out view stands.
const setupFresh = 5 * time.Second

// setupDef is a step's fixed part.
type setupDef struct {
	id, title, why, link, where, action, docs string
}

// setupDefs are the steps, in the order they are best done.
var setupDefs = []setupDef{
	{"signin", "Sign-in: local users, or OIDC or LDAP",
		"Everyone signs in first: roles, approvals and the audit log all name a person, and nothing that needs one (SSH, Automation, Secret values) runs without sign-in.",
		"#/admin/users", "Administration › Users", "local users are added with Add user; OIDC and LDAP are set in the chart (auth.mode)", "docs/getting-started.md#1-sign-in"},
	{"clusters", "Clusters: connect the clusters to manage",
		"Every later step works on the clusters the console knows, so they come before what reads them.",
		"#/home", "Home › Clusters", "Add a cluster, then paste its token or kubeconfig in the dialog", "docs/getting-started.md#2-clusters"},
	{"provider", "AI provider and default model",
		"The agents need a model before they can answer; the provider's key is typed on the screen into a Kubernetes Secret, never into a chat.",
		"#/admin/settings/providers", "Administration › Settings › Providers", "the default provider's credential field and its default model", "docs/getting-started.md#3-ai-provider-and-default-model"},
	{"agents", "Agents: turn them on, set budgets",
		"With a provider working, turning agents on and giving each a budget decides who answers and what it may cost.",
		"#/admin/settings/agents", "Administration › Settings › Agents", "each agent's switch, model and daily and monthly budget, and the overall limits", "docs/getting-started.md#4-agents"},
	{"knowledge", "Knowledge: connect Mycroft's sources",
		"Mycroft reads your Confluence spaces, runbooks and documents into a wiki the agents cite, and proposes the hosts and fleets they list, so it comes before fleets and hosts.",
		"#/admin/settings/knowledge", "Administration › Settings › Knowledge", "where the wiki is kept, then Add source; a source's token goes in its own field", "docs/getting-started.md#5-knowledge"},
	{"jobs", "Jobs and notifications, in Cases",
		"Jobs run agents on their own, read only (the nightly audit, an alert's diagnosis), and notifications tell people what waits for them in Cases.",
		"#/aiops/jobs", "AIOps › Jobs, and Administration › Settings › Watson for the notification channels", "a job's switch and budget; a channel's webhook address is typed in its own field", "docs/getting-started.md#6-jobs-and-notifications"},
	{"fleets", "Fleets",
		"Fleets group nodes by the labels and taints they share; Automation runs on fleets, and the views filter by them.",
		"#/nodes", "Kubernetes › Nodes, the fleet list", "New fleet, from the labels and taints the nodes share", "docs/getting-started.md#7-fleets"},
	{"hosts", "SSH and hosts outside Kubernetes",
		"SSH lets Automation and the node tools reach nodes, and the machines outside Kubernetes join their cluster's Nodes tab.",
		"#/nodes", "Kubernetes › Nodes", "Pair for SSH on a node, and Add host for a machine outside Kubernetes; its password or key is typed in the pairing dialog", "docs/getting-started.md#8-ssh-and-hosts-outside-kubernetes"},
	{"backup", "Backups: a first one, then a schedule to S3",
		"Once there is something to keep, take a first backup, then schedule them to S3 so they keep coming.",
		"#/admin/backup", "Administration › Backup", "Download a backup; then Backups to S3, with its access key typed in its own field", "docs/getting-started.md#9-backups"},
}

// setupStepIDs are the steps' ids, for checking a dismissal.
func setupStepIDs() []string {
	out := make([]string, len(setupDefs))
	for i, d := range setupDefs {
		out[i] = d.id
	}
	return out
}

func (s *Server) setupKV() kv.Backend {
	if s.opts.KV != nil {
		return s.opts.KV
	}
	return &s.setup.mem
}

func (s *Server) loadSetup(ctx context.Context) (setupRecord, string, error) {
	var rec setupRecord
	b, version, err := s.setupKV().LoadKey(ctx, SetupKey)
	if errors.Is(err, kv.ErrNotFound) {
		return rec, version, nil
	}
	if err != nil {
		return rec, version, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return setupRecord{}, version, fmt.Errorf("Getting started's record cannot be read: %w", err)
	}
	return rec, version, nil
}

// changeSetup edits the record over the version it read, again on a
// conflict with another replica's write.
func (s *Server) changeSetup(ctx context.Context, edit func(*setupRecord) bool) error {
	for try := 0; ; try++ {
		rec, version, err := s.loadSetup(ctx)
		if err != nil {
			return err
		}
		if !edit(&rec) {
			return nil
		}
		b, _ := json.Marshal(rec)
		_, err = s.setupKV().SaveKey(ctx, SetupKey, b, version)
		if errors.Is(err, kv.ErrConflict) && try < 4 {
			continue
		}
		s.forgetSetup()
		return err
	}
}

func (s *Server) forgetSetup() {
	s.setup.mu.Lock()
	s.setup.view = nil
	s.setup.mu.Unlock()
}

// Setup is Getting started as the console is now, worked out at most a few
// seconds ago.
func (s *Server) Setup(ctx context.Context) SetupView {
	s.setup.mu.Lock()
	if v := s.setup.view; v != nil && s.opts.Now().Sub(s.setup.at) < setupFresh {
		s.setup.mu.Unlock()
		return *v
	}
	s.setup.mu.Unlock()
	v := s.workOutSetup(ctx)
	s.setup.mu.Lock()
	s.setup.view, s.setup.at = &v, s.opts.Now()
	s.setup.mu.Unlock()
	return v
}

// workOutSetup checks every step against the console.
func (s *Server) workOutSetup(ctx context.Context) SetupView {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rec, _, err := s.loadSetup(ctx)
	if err != nil {
		s.opts.Logger.Warn("reading Getting started's record failed", "err", err)
	}
	_, inMemory := s.setupKV().(*kv.Memory)
	v := SetupView{Kept: !inMemory}
	check := map[string]func(context.Context, setupRecord) (bool, string){
		"signin": s.setupSignIn, "clusters": s.setupClusters, "provider": s.setupProvider, "agents": s.setupAgents,
		"knowledge": s.setupKnowledge, "jobs": s.setupJobs, "fleets": s.setupFleets, "hosts": s.setupHosts, "backup": s.setupBackup,
	}
	for i, d := range setupDefs {
		done, detail := check[d.id](ctx, rec)
		st := SetupStep{ID: d.id, N: i + 1, Title: d.title, State: StepOpen, Detail: detail, Why: d.why, Link: d.link, Where: d.where, Action: d.action, Docs: d.docs}
		mark, dismissed := rec.Dismissed[d.id]
		switch {
		case done:
			st.State = StepDone
			v.Done++
		case dismissed:
			st.State = StepDismissed
			v.Dismissed++
		default:
			v.Open++
			if v.Next == "" {
				v.Next = d.id
			}
		}
		if dismissed {
			at := mark.At
			st.DismissedBy, st.DismissedAt = mark.By, &at
		}
		v.Steps = append(v.Steps, st)
	}
	v.Complete = v.Open == 0
	return v
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *Server) setupSignIn(_ context.Context, _ setupRecord) (bool, string) {
	m, ok := s.opts.Auth.(interface{ Mode() auth.Mode })
	if s.opts.Auth == nil || !ok || m.Mode() == auth.ModeNone {
		return false, "Sign-in is off: everyone who reaches the console holds the anonymous role, and SSH, Automation and Secret values stay off. Set auth.mode in the chart to local, oidc or ldap."
	}
	switch m.Mode() {
	case auth.ModeOIDC:
		return true, "People sign in through OIDC."
	case auth.ModeLDAP:
		return true, "People sign in through LDAP."
	}
	acc := s.accounts()
	if acc == nil {
		return false, "Local sign-in has no accounts store."
	}
	list := acc.List()
	added := 0
	for _, a := range list {
		if !a.Managed {
			added++
		}
	}
	if added > 0 && !acc.Persistent() {
		return false, plural(len(list), "local user", "local users") + fmt.Sprintf(", %d of them added in the console and kept in memory: they are lost when it restarts. Name a Secret to keep them in (-users-secret, or -connections-secrets).", added)
	}
	return len(list) > 0, plural(len(list), "local user", "local users") + "."
}

func (s *Server) setupClusters(_ context.Context, _ setupRecord) (bool, string) {
	if s.opts.Clusters == nil {
		return s.opts.Store != nil, "The console manages the cluster it was started against."
	}
	list := s.opts.Clusters.List()
	up := 0
	for _, c := range list {
		if c.State == kube.StateConnected {
			up++
		}
	}
	detail := fmt.Sprintf("%s, %d connected.", plural(len(list), "cluster", "clusters"), up)
	if len(list) == 1 {
		detail += " Only the console's own so far: add the others from the home page."
	}
	return up > 0, detail
}

func (s *Server) setupProvider(ctx context.Context, _ setupRecord) (bool, string) {
	if s.opts.AI == nil {
		return false, "The AI agents are not offered by this console."
	}
	v := s.opts.AI.View(ctx)
	for _, p := range v.Providers {
		if !p.Default {
			continue
		}
		model := v.DefaultModel
		if p.NeedsKey && !p.Key.Set && !p.KeylessOK {
			return false, fmt.Sprintf("The default provider, %s, has no credential yet: type it into its field in Settings › Providers.", p.Name)
		}
		if model == "" {
			return false, fmt.Sprintf("The default provider, %s, has no default model: pick one in Settings › Providers.", p.Name)
		}
		if p.KeylessOK {
			return true, fmt.Sprintf("The default provider, %s, answered its test without a credential; its default model is %s.", p.Name, model)
		}
		return true, fmt.Sprintf("The default provider, %s, has its credential; its default model is %s.", p.Name, model)
	}
	return false, "No provider is set up."
}

func (s *Server) setupAgents(ctx context.Context, _ setupRecord) (bool, string) {
	if s.opts.AI == nil {
		return false, "The AI agents are not offered by this console."
	}
	v := s.opts.AI.View(ctx)
	on, sherlockOn, clustersOn := 0, false, 0
	for _, a := range v.Agents {
		if a.Enabled {
			on++
		}
		if a.ID == "sherlock" {
			sherlockOn, clustersOn = a.Enabled, len(a.Clusters)
		}
	}
	limits := "no overall limit"
	if v.DailyUSD > 0 || v.MonthlyUSD > 0 {
		limits = fmt.Sprintf("overall limits $%.2f a day and $%.2f a month", v.DailyUSD, v.MonthlyUSD)
	}
	detail := fmt.Sprintf("%d of %d agents on; %s.", on, len(v.Agents), limits)
	if sherlockOn {
		detail = fmt.Sprintf("Sherlock is on for %s; ", plural(clustersOn, "cluster", "clusters")) + detail
	} else {
		detail = "Sherlock is off on every cluster, so no agent answers anyone; " + detail
	}
	return sherlockOn && (v.DailyUSD > 0 || v.MonthlyUSD > 0), detail
}

func (s *Server) setupKnowledge(ctx context.Context, _ setupRecord) (bool, string) {
	k := s.opts.Knowledge
	if k == nil {
		return false, "Mycroft's knowledge is not offered by this console."
	}
	set, err := k.Settings(ctx)
	if err != nil {
		return false, "The knowledge settings could not be read: " + err.Error()
	}
	sources, docs := 0, 0
	for _, x := range set.Sources {
		switch {
		case x.Off:
		case x.Kind == knowledge.SourceConfluence || x.Kind == knowledge.SourceGit:
			sources++
		case x.Kind == knowledge.SourceUpload:
			docs += len(k.Documents(x.ID))
		}
	}
	var missing []string
	if set.Store.Kind == "" {
		missing = append(missing, "where the wiki is kept is not chosen")
	}
	if sources+docs == 0 {
		missing = append(missing, "no Confluence space, repository or document is connected besides the console itself")
	}
	if len(missing) > 0 {
		return false, strings.ToUpper(missing[0][:1]) + strings.Join(missing, "; ")[1:] + "."
	}
	return true, fmt.Sprintf("The wiki is kept in %s; %s and %s connected.", set.Store.Kind, plural(sources, "source", "sources"), plural(docs, "document", "documents"))
}

func (s *Server) setupJobs(ctx context.Context, _ setupRecord) (bool, string) {
	jobsOn := -1
	if s.opts.Jobs != nil {
		if list, err := s.opts.Jobs.List(ctx); err == nil {
			jobsOn = 0
			for _, j := range list {
				if j.On && j.External == nil {
					jobsOn++
				}
			}
		}
	}
	channels := -1
	if s.opts.Cards != nil {
		if nv, err := s.opts.Cards.Notifications(ctx); err == nil {
			channels = 0
			for _, c := range nv.Channels {
				if c.On && c.URLSet {
					channels++
				}
			}
		}
	}
	var parts []string
	switch jobsOn {
	case -1:
		parts = append(parts, "the jobs are not offered here")
	case 0:
		parts = append(parts, "no agent job is on")
	default:
		parts = append(parts, plural(jobsOn, "job", "jobs")+" on")
	}
	switch channels {
	case -1:
		parts = append(parts, "notifications are not offered here")
	case 0:
		parts = append(parts, "no notification channel is on")
	default:
		parts = append(parts, plural(channels, "notification channel", "notification channels")+" on")
	}
	detail := strings.Join(parts, "; ")
	return jobsOn > 0 && channels > 0, strings.ToUpper(detail[:1]) + detail[1:] + "."
}

// setupClusterIDs are the clusters the console knows.
func (s *Server) setupClusterIDs() []string {
	if s.opts.Clusters == nil {
		return []string{clusters.LocalID}
	}
	var out []string
	for _, c := range s.opts.Clusters.List() {
		out = append(out, c.ID)
	}
	return out
}

func (s *Server) setupFleets(_ context.Context, _ setupRecord) (bool, string) {
	if s.opts.Settings == nil {
		return false, "This console keeps no settings, so no fleets."
	}
	st := s.opts.Settings.Current()
	n, in := 0, 0
	for _, list := range st.Fleets {
		if len(list) > 0 {
			n += len(list)
			in++
		}
	}
	if n == 0 {
		return false, "No fleet yet on any cluster."
	}
	return true, fmt.Sprintf("%s on %s.", plural(n, "fleet", "fleets"), plural(in, "cluster", "clusters"))
}

func (s *Server) setupHosts(ctx context.Context, _ setupRecord) (bool, string) {
	if s.opts.SSH == nil {
		return false, "SSH to nodes is off in this console (the chart's toolbox.ssh)."
	}
	pairs, hosts, paired := 0, 0, 0
	for _, id := range s.setupClusterIDs() {
		if list, err := s.opts.SSH.List(ctx, id); err == nil {
			pairs += len(list)
		}
		if s.opts.Hosts != nil {
			if list, err := s.opts.Hosts.Hosts(ctx, id); err == nil {
				hosts += len(list)
				for _, h := range list {
					if h.State == "paired" {
						paired++
					}
				}
			}
		}
	}
	detail := plural(pairs, "node", "nodes") + " paired for SSH"
	if s.opts.Hosts != nil {
		detail += fmt.Sprintf("; %s outside Kubernetes, %d paired", plural(hosts, "host", "hosts"), paired)
	} else {
		detail += "; hosts outside Kubernetes come with Automation (the chart's automation.enabled)"
	}
	return pairs+paired > 0, detail + "."
}

func (s *Server) setupBackup(ctx context.Context, rec setupRecord) (bool, string) {
	var parts []string
	done := false
	if fb := rec.FirstBackup; fb != nil {
		done = true
		// Who took it stays out: the words reach Sherlock's model.
		parts = append(parts, "A backup was first downloaded on "+fb.At.UTC().Format("2006-01-02"))
	}
	if s.opts.BackupS3 != nil {
		set, saved, _ := s.opts.BackupS3.Settings(ctx)
		st, err := s.opts.BackupS3.Status(ctx)
		switch {
		case err == nil && st.LastOK != nil:
			done = true
			parts = append(parts, "the last backup to S3 worked on "+st.LastOK.Started.UTC().Format("2006-01-02 15:04 UTC"))
		case saved && set.Enabled:
			parts = append(parts, "backups to S3 are scheduled, and none has worked yet")
		default:
			parts = append(parts, "no backups to S3 are scheduled")
		}
	}
	if !done {
		parts = append([]string{"No backup yet"}, parts...)
	}
	detail := strings.Join(parts, "; ")
	return done, strings.ToUpper(detail[:1]) + detail[1:] + "."
}

// noteFirstBackup keeps the first backup downloaded, for step 9; a
// failure is logged, never in the way of the download.
func (s *Server) noteFirstBackup(r *http.Request) {
	by := backupBy(r)
	err := s.changeSetup(r.Context(), func(rec *setupRecord) bool {
		if rec.FirstBackup != nil {
			return false
		}
		rec.FirstBackup = &setupMark{By: by, At: s.opts.Now().UTC()}
		return true
	})
	if err != nil {
		s.opts.Logger.Warn("noting the first backup for Getting started failed", "err", err)
	}
}

// SetupOpen says whether a step is open: Sherlock's setup skill is on.
func (s *Server) SetupOpen(ctx context.Context) bool { return !s.Setup(ctx).Complete }

// SetupText is Getting started in words, for Sherlock's setup_status.
func (s *Server) SetupText(ctx context.Context) string { return SetupText(s.Setup(ctx)) }

// SetupText writes the steps for a model: each with its state, what is
// missing, why it comes where it does, and where it is done.
func SetupText(v SetupView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Getting started, as the console is now: %d of %d steps done, %d dismissed, %d open. The console checks each step itself; nobody ticks one by hand, you neither.\n", v.Done, len(v.Steps), v.Dismissed, v.Open)
	for _, st := range v.Steps {
		fmt.Fprintf(&b, "\n%d. %s: %s. %s", st.N, st.Title, st.State, st.Detail)
		if st.State != StepDone {
			fmt.Fprintf(&b, "\n   Why it comes here: %s\n   Where: %s, link [%s](%s); there: %s.\n   Docs: %s", st.Why, st.Where, st.Where, st.Link, st.Action, st.Docs)
		}
	}
	if v.Next == "" {
		b.WriteString("\n\nEvery step is done or dismissed.")
		return b.String()
	}
	for _, st := range v.Steps {
		if st.ID == v.Next {
			fmt.Fprintf(&b, "\n\nNext: step %d of %d, %s. Open it with [%s](%s).", st.N, len(v.Steps), st.Title, st.Where, st.Link)
		}
	}
	return b.String()
}

// setupGuide is Getting started for Sherlock's setup skill (ai.SetupGuide).
type setupGuide struct{ s *Server }

func (g setupGuide) Open(ctx context.Context) bool     { return g.s.SetupOpen(ctx) }
func (g setupGuide) Status(ctx context.Context) string { return g.s.SetupText(ctx) }

func (s *Server) setupRoutes() {
	s.handle("GET /api/v1/setup", "setup", s.getSetup)
	s.handle("POST /api/v1/setup/dismiss", "setup-dismiss", s.postSetupDismiss)
	s.backups.Register(backup.Part{Name: "gettingStarted", Label: "Getting started", Order: 250,
		How: "replaces which steps are dismissed and when the first backup was taken; every other step is checked against the console again",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			rec, _, err := s.loadSetup(ctx)
			if err != nil {
				return nil, err
			}
			if len(rec.Dismissed) == 0 && rec.FirstBackup == nil {
				return nil, nil
			}
			return &rec, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var in setupRecord
			if err := json.Unmarshal(raw, &in); err != nil {
				return skip(env, "Getting started: %v", err)
			}
			for id := range in.Dismissed {
				if !slices.Contains(setupStepIDs(), id) {
					delete(in.Dismissed, id)
				}
			}
			if err := s.changeSetup(ctx, func(rec *setupRecord) bool { *rec = in; return true }); err != nil {
				return skip(env, "Getting started: %v", err)
			}
			return len(in.Dismissed), nil
		}})
}

// getSetup is Getting started, for a SuperAdmin: worked out afresh, so a
// step done a moment ago shows done.
func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin sees Getting started.")
		return
	}
	s.forgetSetup()
	writeValue(w, r, s.Setup(r.Context()))
}

// postSetupDismiss dismisses steps, or brings them back; each change is
// audited.
func (s *Server) postSetupDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin dismisses steps of Getting started.")
		return
	}
	var in struct {
		Steps   []string `json:"steps"`
		Dismiss bool     `json:"dismiss"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || len(in.Steps) == 0 {
		jsonError(w, http.StatusBadRequest, "name the steps to dismiss or bring back")
		return
	}
	ids := setupStepIDs()
	for _, id := range in.Steps {
		if !slices.Contains(ids, id) {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("there is no step %q", id))
			return
		}
	}
	by := backupBy(r)
	var changed []string
	err := s.changeSetup(r.Context(), func(rec *setupRecord) bool {
		changed = changed[:0]
		for _, id := range in.Steps {
			_, was := rec.Dismissed[id]
			switch {
			case in.Dismiss && !was:
				if rec.Dismissed == nil {
					rec.Dismissed = map[string]setupMark{}
				}
				rec.Dismissed[id] = setupMark{By: by, At: s.opts.Now().UTC()}
				changed = append(changed, id)
			case !in.Dismiss && was:
				delete(rec.Dismissed, id)
				changed = append(changed, id)
			}
		}
		return len(changed) > 0
	})
	if err != nil {
		jsonError(w, http.StatusBadGateway, "the change could not be kept: "+err.Error())
		return
	}
	for _, id := range changed {
		n := slices.Index(ids, id)
		what := "dismissed"
		if !in.Dismiss {
			what = "brought back"
		}
		s.record(r, audit.ActionSetupDismiss, id, fmt.Sprintf("Getting started: step %d, %s, %s", n+1, setupDefs[n].title, what))
	}
	writeValue(w, r, s.Setup(r.Context()))
}

// --- end of Getting started.
