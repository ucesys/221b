package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/schedule"
)

// --- A Wiki sync job per source (Phase 12, KN-28, ADR-141) ---
//
// Each source is read by a job of its own in AIOps › Jobs, "Wiki sync:
// <source>", Mycroft's, with the usual controls: its schedule, pause, its
// budget, Run now, who may edit it (JB-04). Adding a source makes its job
// with a default trigger by its kind; renaming it renames the job;
// removing it lets the job go (its runs are kept); switching it off
// pauses the job. A source added or changed is read once at once, as a
// run of its job; what people add or upload starts a run of theirs; a
// push to a repository of runbooks starts a run of its. Every sync is a
// run with its report, and Settings › Knowledge starts none.

// SyncJobPrefix starts the id of a source's Wiki sync job: the job, and
// its kind, are "wiki-sync-<source id>".
const SyncJobPrefix = "wiki-sync-"

// SyncJobID is a source's Wiki sync job.
func SyncJobID(source string) string { return SyncJobPrefix + source }

// RetiredJobKinds are the jobs that read every source before KN-28: a
// nightly sync of all and one of the changes every 15 minutes. Their
// settings are let go at start, once.
var RetiredJobKinds = []string{"knowledgeSync", "knowledgeChanges"}

// SyncBudget is a Wiki sync job's budget until someone changes it.
var SyncBudget = jobs.Budget{RunUSD: 5, DayUSD: 20}

// syncKindsEvery is how often the jobs' kinds are made again from the
// sources, so a replica that comes to lead lists them as they are.
const syncKindsEvery = 30 * time.Second

// nightlyHour is when a Confluence space's Wiki sync reads every page: its
// first run after it, in the schedule's time zone.
const nightlyHour = 1

// SetJobs gives the service the console's jobs (KN-28): each source gets
// its Wiki sync job, the jobs of RetiredJobKinds are let go, and from
// then on a change to a source changes its job. The kinds are made again
// every half minute until ctx ends.
func (s *Service) SetJobs(ctx context.Context, js *jobs.Service) {
	if js == nil {
		return
	}
	s.jobs.Store(js)
	s.syncKinds(ctx)
	s.migrateJobs(ctx)
	go func() {
		t := time.NewTicker(syncKindsEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s.syncKinds(ctx)
			s.migrateJobs(ctx)
		}
	}()
}

// syncKinds registers each source's job kind as the sources are now, and
// unregisters those of the sources gone. The settings are read under
// kindsMu, so the last to register saw the newest sources.
func (s *Service) syncKinds(ctx context.Context) {
	js := s.jobs.Load()
	if js == nil {
		return
	}
	s.kindsMu.Lock()
	defer s.kindsMu.Unlock()
	set, err := s.Settings(ctx)
	if err != nil {
		s.o.Logger.Warn("the sources could not be read for their Wiki sync jobs", "err", err)
		return
	}
	reg := js.Kinds()
	want := map[string]bool{}
	for _, x := range set.Sources {
		k := s.syncKind(x)
		want[k.ID] = true
		reg.Register(k)
	}
	for _, k := range reg.Kinds() {
		if strings.HasPrefix(k.ID, SyncJobPrefix) && !want[k.ID] {
			reg.Unregister(k.ID)
		}
	}
	for _, id := range RetiredJobKinds {
		reg.Unregister(id)
	}
}

// migrateJobs lets go of the jobs of RetiredJobKinds, on the leader,
// once: the per-source jobs are their kinds', there from the sources.
// It is idempotent: a job already gone is left so.
func (s *Service) migrateJobs(ctx context.Context) {
	js := s.jobs.Load()
	if js == nil || s.migrated.Load() || !s.o.Leads() {
		return
	}
	for _, id := range RetiredJobKinds {
		if _, err := js.Drop(ctx, id, jobs.Person{Subject: "221b", Name: "221b"}, "each knowledge source is read by its own Wiki sync job now (KN-28)"); err != nil {
			s.o.Logger.Warn("a knowledge job of before could not be let go; trying again", "job", id, "err", err)
			return
		}
	}
	s.migrated.Store(true)
}

// syncOnAtFirst says whether a source's job starts on. Jobs are off
// until a SuperAdmin turns them on (JB-03), as each scheduled read costs
// Mycroft's budget; the job of what people add or upload is on, as it
// runs only when someone adds something, and so is the job of the fixes,
// which runs only when the housekeeping offers one (KN-34). A new or
// changed source is read once at once either way.
func syncOnAtFirst(x Source) bool {
	return !x.Off && (x.Kind == SourceUpload || x.Kind == SourceFixes)
}

// syncKind is a source's Wiki sync job's kind, with its default trigger
// by the source's kind.
func (s *Service) syncKind(x Source) jobs.Kind {
	id := x.ID
	k := jobs.Kind{ID: SyncJobID(x.ID), Label: "Wiki sync: " + x.Name, Agent: MycroftID, AgentName: "Mycroft", Order: 61, On: syncOnAtFirst(x),
		Proposes:    "Changes to the wiki's pages, which wait for review in AIOps › Inbox, and the hosts and fleets its documents list that the console lacks, as proposals there. A run makes no findings, and nothing it makes changes anything.",
		Budget:      SyncBudget,
		Schedulable: true, Single: true, NoFindings: true, Rebuild: true,
		Run: func(ctx context.Context, env *jobs.RunEnv) (jobs.Outcome, error) { return s.runSync(ctx, env, id) }}
	nightly := &schedule.Spec{Kind: schedule.Daily, At: "01:00"}
	changed := " An item unchanged since it was last read is not read again or sent to Mycroft."
	switch x.Kind {
	case SourceConfluence:
		k.Does = "Reads the space's pages edited since the last read, and every page once a night, and drafts the wiki's pages from what changed"
		k.Reads = fmt.Sprintf("The Confluence space %s at %s, with its read-only token: the pages edited since the last read; the first run after 01:00 (in the schedule's time zone) and Run now read every page.%s", x.Space, x.BaseURL, changed)
		k.Default = jobs.When{Schedule: &schedule.Spec{Kind: schedule.Cron, Cron: "*/15 * * * *"}}
	case SourceGit:
		dir := ""
		if x.Path != "" {
			dir = ", directory " + x.Path
		}
		k.Does = "Reads the repository's documents every hour and on each push, and drafts the wiki's pages from what changed"
		k.Reads = fmt.Sprintf("The repository %s, branch %s%s: its documents at the branch's head. A push, through the webhook of Settings › Knowledge, starts a run.%s", x.URL, x.Branch, dir, changed)
		k.Default = jobs.When{Schedule: &schedule.Spec{Kind: schedule.Hours, Every: 1, At: "00:00"}, Event: jobs.EventSource}
		k.Events = []string{jobs.EventSource}
	case SourceS3:
		k.Does = "Reads the bucket's documents every night, and drafts the wiki's pages from what changed"
		k.Reads = fmt.Sprintf("The bucket s3://%s/%s: its documents of the kinds Mycroft reads, at most 1000 a read.%s", x.Bucket, x.Prefix, changed)
		k.Default = jobs.When{Schedule: nightly}
	case SourceUpload:
		k.Does = "Reads what people add or upload, when they do, and drafts the wiki's pages from it"
		k.Reads = "The notes, web pages and documents people add or upload here, each read once when it is added, uploaded or replaced."
		k.Default = jobs.When{Event: jobs.EventSource}
		k.Events = []string{jobs.EventSource}
	case SourceFixes:
		k.Does = "Offers Mycroft each fix proven in cases that no runbook draws on, when the housekeeping finds one, and drafts a runbook from it"
		k.Reads = fmt.Sprintf("The active fixes of AIOps › Mycroft › Fixes seen in %d cases or more, each once and again when it changes: its problem, cause, steps, the changes that worked and prevention; never who made it or the cases' names. Pending and inactive fixes never.", ProvenCases)
		k.Default = jobs.When{Event: jobs.EventSource}
		k.Events = []string{jobs.EventSource}
	case SourceConsole:
		k.Does = "Reads what the console knows every night, and drafts the wiki's pages from what changed"
		k.Reads = "The console's own records: its clusters, nodes and hosts with their facts, fleets, Helm applications and Automation scripts; never a Secret's value, a credential or a host key." + changed
		k.Default = jobs.When{Schedule: nightly}
	}
	return k
}

// runSync is one run of a source's Wiki sync job: a sync of the source,
// counted on the run, with its report and facts.
func (s *Service) runSync(ctx context.Context, env *jobs.RunEnv, id string) (jobs.Outcome, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return jobs.Outcome{}, err
	}
	x, ok := set.source(id)
	if !ok {
		return jobs.Outcome{}, fmt.Errorf("the source %q is gone from Settings › Knowledge", id)
	}
	if x.Off {
		return jobs.Outcome{Report: x.Name + " is switched off in Settings › Knowledge: nothing was read."}, nil
	}
	// A Confluence space's scheduled runs read the pages edited since the
	// last read, and every page on the first run of the night; every other
	// run, and every other source, reads all, which costs only the listing
	// for what is unchanged.
	full := true
	if x.Kind == SourceConfluence && env.Trigger.Kind == jobs.TriggerSchedule {
		st, _ := s.Status(ctx)
		full = nightlyDue(st.Sources[id].LastFull, env.Now(), zoneOf(env))
	}
	// The sync is the person's whose addition or upload started it, as
	// before the jobs: its drafts are theirs in AIOps › Inbox.
	who := audit.Who{Subject: "221b", Name: fmt.Sprintf("job %s (%s)", env.Job.ID, env.RunID)}
	if env.Event != nil {
		if w, ok := env.Event.Data.(audit.Who); ok && w.Subject != "" {
			who = w
		}
	}
	req := SyncRequest{Full: full, Rebuild: env.Trigger.Rebuild, Source: id, By: fmt.Sprintf("%s, %s", env.RunID, env.Trigger.What), Who: who,
		Spend: func(c Completion) error {
			return env.Spend(c.Model, jobs.Tokens{In: c.In, Out: c.Out}, c.CostUSD)
		}}
	res, err := s.syncWhenFree(ctx, req)
	if err != nil {
		return jobs.Outcome{Report: fmt.Sprintf("%s could not be read: %v", x.Name, err)}, err
	}
	out := jobs.Outcome{Report: res.Text, Facts: syncFacts(res, x)}
	if res.stop != nil {
		return out, res.stop
	}
	if res.failed {
		return out, errors.New(res.Errors[0])
	}
	return out, nil
}

// syncWhenFree syncs once no other sync runs: one source's run waits for
// another's.
func (s *Service) syncWhenFree(ctx context.Context, req SyncRequest) (SyncResult, error) {
	for {
		res, err := s.SyncWith(ctx, req)
		if !errors.Is(err, ErrBusy) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// zoneOf is the time zone of a run's job's schedule, or the console's.
func zoneOf(env *jobs.RunEnv) *time.Location {
	tz := env.TimeZone()
	if sp := env.Job.When.Schedule; sp != nil && sp.TimeZone != "" {
		tz = sp.TimeZone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// nightlyDue says whether a read of every item is due: none since the
// last 01:00 in loc.
func nightlyDue(last, now time.Time, loc *time.Location) bool {
	n := now.In(loc)
	at := time.Date(n.Year(), n.Month(), n.Day(), nightlyHour, 0, 0, 0, loc)
	if n.Before(at) {
		at = at.AddDate(0, 0, -1)
	}
	return last.Before(at)
}

// sourceKindWords say a source's kind.
var sourceKindWords = map[string]string{SourceConfluence: "Confluence space", SourceGit: "git repository", SourceS3: "S3 bucket", SourceUpload: "documents people add", SourceConsole: "the console's own records", SourceFixes: "fixes proven in cases"}

// syncFacts are what a sync did, for its run's drawer (KN-28).
func syncFacts(r SyncResult, x Source) []jobs.Fact {
	n := func(v int, one, many string) string {
		if v == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", v, many)
	}
	read := "Every item"
	if r.Rebuild {
		read = "Every item, unchanged ones too: given to Mycroft again (Rebuild now)"
	} else if !r.Full && x.Kind == SourceConfluence {
		read = "The pages edited since the last read"
	}
	out := []jobs.Fact{
		{Label: "Source", Text: fmt.Sprintf("%s (%s)", x.Name, sourceKindWords[x.Kind]), Href: "#/admin/settings/knowledge"},
		{Label: "Read", Text: read},
		{Label: "Items read", Text: n(r.Read, "changed item", "changed items")},
		{Label: "Unchanged", Text: n(r.Unchanged, "item", "items")},
		{Label: "Gone", Text: n(r.Gone, "item", "items")},
	}
	skipped := jobs.Fact{Label: "Skipped", Text: n(r.Skipped, "item", "items")}
	if r.Skipped > 0 && r.SkippedWhy != "" {
		skipped.Text += ": " + r.SkippedWhy
	}
	out = append(out, skipped)
	drafts := jobs.Fact{Label: "Pages drafted", Text: "None"}
	if r.Pages > 0 {
		drafts = jobs.Fact{Label: "Pages drafted", Text: n(r.Pages, "page change waits", "page changes wait") + " for review in AIOps › Inbox", Href: DraftHref(r.Run)}
	}
	out = append(out, drafts)
	props := jobs.Fact{Label: "Proposals", Text: "None"}
	if r.Proposals > 0 {
		props = jobs.Fact{Label: "Proposals", Text: n(r.Proposals, "proposal waits", "proposals wait") + " in AIOps › Inbox", Href: "#/cases/inbox"}
	}
	out = append(out, props)
	if len(r.Fixes) > 0 {
		out = append(out, jobs.Fact{Label: "Fixes given", Text: strings.Join(r.Fixes, ", "), Href: "#/cases/mycroft/fixes"})
	}
	if len(r.Contradictions) > 0 {
		out = append(out, jobs.Fact{Label: "Contradicting a fix", Text: n(len(r.Contradictions), "page says", "pages say") + " otherwise than a fix; the reviewer decides", Tone: "warn", Href: DraftHref(r.Run)})
	}
	if len(r.Batches) > 0 {
		counts := map[string]int{}
		for _, b := range r.Batches {
			counts[b.Outcome]++
		}
		parts := []string{n(len(r.Batches), "request", "requests")}
		for _, o := range []struct{ key, one, many string }{
			{BatchSplit, "cut and split in two", "cut and split in two"},
			{BatchTooLarge, "item too large", "items too large"},
			{BatchFailed, "failed", "failed"},
			{BatchNotSent, "not sent", "not sent"},
		} {
			if c := counts[o.key]; c > 0 {
				parts = append(parts, n(c, o.one, o.many))
			}
		}
		f := jobs.Fact{Label: "Mycroft’s batches", Text: strings.Join(parts, ", ")}
		if len(counts) > 1 || counts[BatchOK] == 0 {
			f.Tone = "warn"
		}
		out = append(out, f)
	}
	if r.Unsent > 0 {
		out = append(out, jobs.Fact{Label: "Read again next run", Text: n(r.Unsent, "item Mycroft did not have", "items Mycroft did not have"), Tone: "warn"})
	}
	if len(r.Reported) > 0 {
		out = append(out, jobs.Fact{Label: "Reported", Text: n(len(r.Reported), "item tried", "items tried") + " to give instructions, which were not followed", Tone: "warn"})
	}
	if len(r.Errors) > 0 {
		out = append(out, jobs.Fact{Label: "Errors", Text: n(len(r.Errors), "error", "errors") + ", in the report", Tone: "err"})
	}
	return out
}

// person is a caller as the jobs name them.
func (c Caller) person() jobs.Person {
	return jobs.Person{Subject: c.ID, Email: c.Email, Name: c.Name, From: c.From}
}

// sourceSaved makes a source's job as the source now is, and reads the
// source once at once, as a run of its job (KN-28). Without the jobs, a
// source added is read in the background (GS-11).
func (s *Service) sourceSaved(ctx context.Context, c Caller, x Source, create, read bool) {
	js := s.jobs.Load()
	if js == nil {
		if create && s.o.ReadNew && !x.Off {
			s.firstRead(ctx, c, x)
		}
		return
	}
	s.syncKinds(ctx)
	if x.Off || !read {
		return
	}
	if _, _, err := s.currentStore(ctx); err != nil {
		return
	}
	what := "Source changed by " + c.label()
	if create {
		what = "Source added by " + c.label()
	}
	if _, err := js.Start(ctx, SyncJobID(x.ID), what, c.person()); err != nil && !errors.Is(err, jobs.ErrRunning) {
		s.o.Logger.Warn("a source's first read could not start; its job reads it at its next run", "source", x.ID, "err", err)
	}
}

// sourceRemoved lets a removed source's job go; its runs are kept.
func (s *Service) sourceRemoved(ctx context.Context, c Caller, id string) {
	js := s.jobs.Load()
	if js == nil {
		return
	}
	s.syncKinds(ctx)
	if _, err := js.Drop(ctx, SyncJobID(id), c.person(), "its source "+id+" was removed by "+c.label()); err != nil {
		s.o.Logger.Warn("a removed source's job could not be let go", "source", id, "err", err)
	}
}

// sourceSwitched pauses a source's job when it is switched off, and
// resumes it when it is switched on.
func (s *Service) sourceSwitched(ctx context.Context, c Caller, id string, on bool) {
	js := s.jobs.Load()
	if js == nil {
		return
	}
	s.syncKinds(ctx)
	why := "its source switched off by " + c.label()
	if on {
		why = "its source switched on by " + c.label()
	}
	if err := js.SetOn(ctx, SyncJobID(id), on, c.person(), why); err != nil {
		s.o.Logger.Warn("a source's job could not be paused or resumed", "source", id, "err", err)
	}
}

// changedNow starts a run of a source's job for what changed in it (an
// upload, an addition, a push), by whom when a person did it, and says
// whether one started: none when the job is paused or does not start on
// a change.
func (s *Service) changedNow(source, label string, by *Caller) bool {
	js := s.jobs.Load()
	if js == nil {
		return false
	}
	ev := jobs.Event{Kind: jobs.EventSource, JobKind: SyncJobID(source), Label: label}
	if by != nil {
		ev.Data = by.who()
	}
	return js.Fire(ev) > 0
}
