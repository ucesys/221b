package jobs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/schedule"
)

// Change is a change to a job: each field given is set, the others stay.
type Change struct {
	Name *string `json:"name,omitempty"`
	// Task is the job's own instructions to its agent; "" goes back to
	// its kind's built-in ones (JR-06).
	Task *string `json:"task,omitempty"`
	When *When   `json:"when,omitempty"`
	// Clusters, empty, is every cluster.
	Clusters    *[]string `json:"clusters,omitempty"`
	Budget      *Budget   `json:"budget,omitempty"`
	On          *bool     `json:"on,omitempty"`
	SkipOverlap *bool     `json:"skipOverlap,omitempty"`
	CatchUp     *bool     `json:"catchUp,omitempty"`
	// MinPriority is the lowest priority a finding opens a case at, 0 to
	// 4 (CF-33).
	MinPriority *int `json:"minPriority,omitempty"`
}

// MaxTask bounds a job's own instructions, in characters (JR-06).
const MaxTask = 8000

// maxBudget bounds a job's budget: a typo of an extra zero or two should
// not reach the agents' overall limits unnoticed.
const maxBudget = 10000

// check checks a job against its kind.
func (s *Service) check(j Job, k Kind) error {
	w := j.When
	if w.Schedule != nil {
		if !k.Schedulable {
			return fmt.Errorf("%w: %s does not run on a schedule", ErrInvalid, k.Label)
		}
		if err := w.Schedule.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if w.Event != "" && !slices.Contains(k.Events, w.Event) {
		return fmt.Errorf("%w: %s does not start on %q", ErrInvalid, k.Label, w.Event)
	}
	if w.Event == EventAlert {
		if w.Severity == "" {
			w.Severity = SeverityWarning
		}
		if w.Severity != SeverityWarning && w.Severity != SeverityError {
			return fmt.Errorf("%w: an alert job takes warning and above, or errors only", ErrInvalid)
		}
	}
	if w.Schedule == nil && w.Event == "" && j.On {
		return fmt.Errorf("%w: say what starts the job: a schedule or an event", ErrInvalid)
	}
	for _, v := range []float64{j.Budget.RunUSD, j.Budget.DayUSD} {
		if v < 0 || v > maxBudget {
			return fmt.Errorf("%w: a budget is from $0 (none of its own) to $%d", ErrInvalid, maxBudget)
		}
	}
	if s.o.Clusters != nil {
		known := map[string]bool{}
		for _, c := range s.o.Clusters() {
			known[c.ID] = true
		}
		for _, c := range j.Clusters {
			if !known[c] {
				return fmt.Errorf("%w: there is no cluster %q", ErrInvalid, c)
			}
		}
	}
	if len(j.Name) > 80 {
		return fmt.Errorf("%w: keep the name under 80 characters", ErrInvalid)
	}
	if p := j.Lowest(); p < 0 || p > 4 {
		return fmt.Errorf("%w: the lowest priority a finding opens a case at is P0 to P4", ErrInvalid)
	}
	if j.Task != "" && k.Task == "" {
		return fmt.Errorf("%w: %s sends its agent no instructions to change", ErrInvalid, k.Label)
	}
	if n := utf8.RuneCountInString(j.Task); n > MaxTask {
		return fmt.Errorf("%w: keep the instructions under %d characters (they are %d)", ErrInvalid, MaxTask, n)
	}
	return nil
}

// Update changes a job and returns it; the audit record has its old and
// new values (JB-03). Turning a job on, or changing its schedule, counts
// its schedule from now.
func (s *Service) Update(ctx context.Context, id string, c Change, by Person) (View, error) {
	var before, after Job
	var kind Kind
	err := s.updateJobs(ctx, func(rec *jobsRecord) error {
		j, k, ok := s.job(*rec, id)
		if !ok {
			return fmt.Errorf("%w: there is no job %q", ErrNotFound, id)
		}
		kind, before = k, j
		if k.External != nil {
			if c.When != nil || c.Clusters != nil || c.Budget != nil || c.Name != nil || c.Task != nil || c.MinPriority != nil {
				return fmt.Errorf("%w: %s is set in %s; here it can only be run or paused", ErrInvalid, k.Label, k.External.Where)
			}
			return errUnchanged
		}
		// Any job takes a name of its own and instructions of its own
		// (JR-06); the kind's label and built-in instructions are what an
		// empty one, or one the same as them, stands for.
		if c.Name != nil {
			if j.Name = strings.TrimSpace(*c.Name); j.Name == k.Label && !j.Added {
				j.Name = ""
			}
		}
		if c.Task != nil {
			if j.Task = strings.TrimSpace(strings.ReplaceAll(*c.Task, "\r\n", "\n")); j.Task == strings.TrimSpace(k.Task) {
				j.Task = ""
			}
		}
		if c.When != nil {
			w := *c.When
			if w.Schedule != nil {
				sp := *w.Schedule
				sp.Kind, sp.Cron = strings.TrimSpace(sp.Kind), strings.TrimSpace(sp.Cron)
				if sp.TimeZone = strings.TrimSpace(sp.TimeZone); sp.TimeZone == "" {
					sp.TimeZone = s.o.TimeZone
				}
				w.Schedule = &sp
			}
			if w.Event == EventAlert && w.Severity == "" {
				w.Severity = SeverityWarning
			}
			if w.Event != EventAlert {
				w.Severity = ""
			}
			j.When = w
		}
		if c.Clusters != nil {
			var cl []string
			for _, x := range *c.Clusters {
				if x = strings.TrimSpace(x); x != "" && !slices.Contains(cl, x) {
					cl = append(cl, x)
				}
			}
			j.Clusters = cl
		}
		if c.Budget != nil {
			j.Budget = Budget{RunUSD: round(c.Budget.RunUSD), DayUSD: round(c.Budget.DayUSD)}
		}
		if c.SkipOverlap != nil {
			j.SkipOverlap = *c.SkipOverlap
		}
		if c.CatchUp != nil {
			j.CatchUp = *c.CatchUp
		}
		if c.On != nil {
			j.On = *c.On
		}
		if c.MinPriority != nil {
			p := *c.MinPriority
			j.MinPriority = &p
		}
		if err := s.check(j, k); err != nil {
			return err
		}
		now := s.o.Now().UTC()
		if j.On && (!before.On || fmt.Sprint(j.When.Schedule) != fmt.Sprint(before.When.Schedule)) {
			j.Since = now
		}
		j.SetBy, j.SetAt = by.Label(), now
		if rec.Jobs == nil {
			rec.Jobs = map[string]Job{}
		}
		rec.Jobs[j.ID] = j
		after = j
		return nil
	})
	if err != nil {
		return View{}, err
	}
	if kind.External != nil {
		if c.On != nil {
			if err := kind.External.SetOn(ctx, *c.On, by); err != nil {
				return View{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			s.record(audit.Event{Action: ActionJob, Who: who(by), Target: id, From: by.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s %s (set in %s)", kind.Label, map[bool]string{true: "resumed", false: "paused"}[*c.On], kind.External.Where)})
		}
		return s.Get(ctx, id)
	}
	s.record(audit.Event{Action: ActionJob, Who: who(by), Target: id, From: by.From, Outcome: audit.OutcomeOK, Detail: s.describeChange(kind, before, after)})
	return s.Get(ctx, id)
}

func who(p Person) audit.Who { return audit.Who{Subject: p.Subject, Email: p.Email, Name: p.Name} }

// describeChange is a job's change in words, old → new (JB-03).
func (s *Service) describeChange(k Kind, a, b Job) string {
	var parts []string
	add := func(what, x, y string) {
		if x != y {
			parts = append(parts, fmt.Sprintf("%s %s → %s", what, x, y))
		}
	}
	state := func(on bool) string { return map[bool]string{true: "on", false: "paused"}[on] }
	add("state", state(a.On), state(b.On))
	add("when", describeWhen(a.When), describeWhen(b.When))
	clusters := func(l []string) string {
		if len(l) == 0 {
			return "all clusters"
		}
		return strings.Join(l, ", ")
	}
	add("clusters", clusters(a.Clusters), clusters(b.Clusters))
	money := func(v float64) string {
		if v == 0 {
			return "none"
		}
		return fmt.Sprintf("$%.2f", v)
	}
	add("budget per run", money(a.Budget.RunUSD), money(b.Budget.RunUSD))
	add("budget per day", money(a.Budget.DayUSD), money(b.Budget.DayUSD))
	add("skip while running", fmt.Sprint(a.SkipOverlap), fmt.Sprint(b.SkipOverlap))
	add("catch up after a restart", fmt.Sprint(a.CatchUp), fmt.Sprint(b.CatchUp))
	add("name", a.Name, b.Name)
	add("lowest priority", fmt.Sprintf("P%d", a.Lowest()), fmt.Sprintf("P%d", b.Lowest()))
	if a.Task != b.Task {
		parts = append(parts, fmt.Sprintf("instructions %s → %s", taskWords(a.Task), taskWords(b.Task)))
	}
	agent := k.AgentName
	if agent == "" {
		agent = "no agent"
	}
	head := fmt.Sprintf("%s (%s)", k.Label, agent)
	if len(parts) == 0 {
		return head + ": no change"
	}
	return head + ": " + strings.Join(parts, "; ")
}

// taskWords says which instructions a job has, for the audit log: its
// text is the job's settings, not the record's.
func taskWords(task string) string {
	if task == "" {
		return "built-in"
	}
	sum := sha256.Sum256([]byte(task))
	return fmt.Sprintf("its own (%d characters, %x)", utf8.RuneCountInString(task), sum[:4])
}

// Add adds a job of a kind beside its own, off, and returns it.
func (s *Service) Add(ctx context.Context, kindID string, by Person) (View, error) {
	k, ok := s.o.Kinds.Kind(kindID)
	if !ok {
		return View{}, fmt.Errorf("%w: there is no kind of job %q", ErrNotFound, kindID)
	}
	if k.External != nil {
		return View{}, fmt.Errorf("%w: %s is set in %s", ErrInvalid, k.Label, k.External.Where)
	}
	if !k.AddMore() {
		return View{}, fmt.Errorf("%w: %s runs once for each %s, by its own job; another would never run", ErrInvalid, k.Label, k.Events[0])
	}
	var j Job
	err := s.updateJobs(ctx, func(rec *jobsRecord) error {
		added := 0
		for _, x := range rec.Jobs {
			if x.Added {
				added++
			}
		}
		if added >= 40 {
			return fmt.Errorf("%w: at most 40 jobs may be added; remove one first", ErrInvalid)
		}
		rec.Next++
		j = s.defaultJob(k)
		j.ID, j.Added, j.Name = fmt.Sprintf("%s-%d", k.ID, rec.Next), true, fmt.Sprintf("%s %d", k.Label, rec.Next+1)
		j.SetBy, j.SetAt = by.Label(), s.o.Now().UTC()
		if rec.Jobs == nil {
			rec.Jobs = map[string]Job{}
		}
		rec.Jobs[j.ID] = j
		return nil
	})
	if err != nil {
		return View{}, err
	}
	s.record(audit.Event{Action: ActionJob, Who: who(by), Target: j.ID, From: by.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("job %s added (%s, %s), paused", j.ID, k.Label, describeWhen(j.When))})
	return s.Get(ctx, j.ID)
}

// Remove removes a job a person added; a kind's own stays.
func (s *Service) Remove(ctx context.Context, id string, by Person) error {
	var gone Job
	err := s.updateJobs(ctx, func(rec *jobsRecord) error {
		j, ok := rec.Jobs[id]
		if !ok || !j.Added {
			if _, _, own := s.job(*rec, id); own {
				return fmt.Errorf("%w: a kind's own job cannot be removed; pause it", ErrInvalid)
			}
			return fmt.Errorf("%w: there is no job %q", ErrNotFound, id)
		}
		gone = j
		delete(rec.Jobs, id)
		delete(rec.Handled, id)
		delete(rec.Spend, id)
		return nil
	})
	if err != nil {
		return err
	}
	s.record(audit.Event{Action: ActionJob, Who: who(by), Target: id, From: by.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("job %s removed (%s, %s)", id, gone.Kind, describeWhen(gone.When))})
	return nil
}

// RunNow starts a run of a job at once, as by, and returns it; the run
// goes on in the background.
func (s *Service) RunNow(ctx context.Context, id string, by Person) (RunBrief, error) {
	rec, err := s.loadJobs(ctx)
	if err != nil {
		return RunBrief{}, err
	}
	j, k, ok := s.job(rec, id)
	if !ok {
		return RunBrief{}, fmt.Errorf("%w: there is no job %q", ErrNotFound, id)
	}
	if k.External != nil {
		if err := k.External.RunNow(ctx, by); err != nil {
			return RunBrief{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		s.record(audit.Event{Action: ActionRun, Who: who(by), Target: id, From: by.From, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s started by hand (set in %s)", k.Label, k.External.Where)})
		return RunBrief{Job: id, Label: k.Label, Status: StatusRunning, Started: s.o.Now().UTC(), Trigger: Trigger{Kind: TriggerManual, What: "Run now", By: by.Label()}}, nil
	}
	if k.Run == nil || !k.Schedulable && !slices.Contains(k.Events, EventAlert) {
		w := describeWhen(j.When)
		return RunBrief{}, fmt.Errorf("%w: %s cannot be run by hand; it runs %s", ErrInvalid, k.Label, strings.ToLower(w[:1])+w[1:])
	}
	trig := Trigger{Kind: TriggerManual, What: "Run now by " + by.Label(), SetBy: j.SetBy, By: by.Label()}
	started := make(chan Run, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_, _, _ = s.execute(s.base, j, k, trig, nil, false, started)
	}()
	select {
	case r := <-started:
		if r.Status == StatusRunning {
			return brief(r), nil
		}
		b := brief(r)
		switch r.Status {
		case StatusBudget:
			return b, fmt.Errorf("%w: %s", ErrBudget, r.Note)
		case StatusSkipped:
			return b, ErrRunning
		}
		return b, errors.New(r.Note)
	case <-ctx.Done():
		return RunBrief{}, ctx.Err()
	}
}

// Preview reads a schedule back as it is edited: in words, and its next
// three times, or what is wrong with it.
func (s *Service) Preview(sp schedule.Spec) (string, []string, error) {
	if strings.TrimSpace(sp.TimeZone) == "" {
		sp.TimeZone = s.o.TimeZone
	}
	sc, err := schedule.Parse(sp)
	if err != nil {
		return "", nil, err
	}
	var next []string
	at := s.o.Now()
	for i := 0; i < 3; i++ {
		if at = sc.Next(at); at.IsZero() {
			break
		}
		next = append(next, at.UTC().Format("2006-01-02T15:04:05Z"))
	}
	return sc.Describe(), next, nil
}
