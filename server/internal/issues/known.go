package issues

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/scrub"
)

// KnownInput is a known issue as an operator writes it. Nil fields stay as
// they are when it is changed.
type KnownInput struct {
	Title      *string      `json:"title,omitempty"`
	Symptoms   []string     `json:"symptoms,omitempty"`
	Services   []string     `json:"services,omitempty"`
	Status     *KnownStatus `json:"status,omitempty"`
	Workaround *string      `json:"workaround,omitempty"`
	Expected   *string      `json:"expected,omitempty"`
	Owner      *Person      `json:"owner,omitempty"`
	Kind       *string      `json:"kind,omitempty"`
	Window     *Window      `json:"window,omitempty"`
	Source     *string      `json:"source,omitempty"`
	Link       *string      `json:"link,omitempty"`
	// Inactive false makes a known issue the housekeeping made inactive
	// active again (CF-53).
	Inactive *bool `json:"inactive,omitempty"`
}

// short checks one line an operator wrote.
func short(what, s string, limit int) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		return "", invalid("keep %s under %d characters", what, limit)
	}
	return scrub.Text(s), nil
}

// apply puts an input on a known issue, checking every field.
func (in KnownInput) apply(st *state, k *KnownIssue) error {
	var err error
	if in.Title != nil {
		if k.Title, err = short("the title", *in.Title, MaxShort); err != nil {
			return err
		}
	}
	if k.Title == "" {
		return invalid("give the known issue a title")
	}
	if in.Inactive != nil {
		k.Inactive = *in.Inactive
		if !k.Inactive {
			k.DuplicateOf = ""
		}
	}
	if in.Symptoms != nil {
		if len(in.Symptoms) > MaxSymptoms {
			return invalid("at most %d symptoms", MaxSymptoms)
		}
		k.Symptoms = nil
		for _, s := range in.Symptoms {
			v, err := short("a symptom", s, MaxShort)
			if err != nil {
				return err
			}
			if v != "" {
				k.Symptoms = append(k.Symptoms, v)
			}
		}
	}
	if in.Services != nil {
		for _, id := range in.Services {
			if st.service(id) == nil {
				return fmt.Errorf("%w: no service %q", ErrNotFound, id)
			}
		}
		k.Services = append([]string(nil), in.Services...)
	}
	if in.Status != nil {
		if !in.Status.Valid() {
			return invalid("%q is not a known issue's status", *in.Status)
		}
		k.Status = *in.Status
	}
	if in.Workaround != nil {
		if k.Workaround, err = short("the workaround", *in.Workaround, MaxText); err != nil {
			return err
		}
	}
	if in.Expected != nil {
		if k.Expected, err = short("the expected fix", *in.Expected, 120); err != nil {
			return err
		}
	}
	if in.Owner != nil {
		if in.Owner.ID == "" {
			k.Owner = nil
		} else {
			k.Owner = &Person{ID: in.Owner.ID, Name: in.Owner.Name, Email: in.Owner.Email}
		}
	}
	if in.Kind != nil {
		if *in.Kind != KindNormal && *in.Kind != KindPlanned {
			return invalid("a known issue is normal or planned")
		}
		k.Kind = *in.Kind
	}
	if in.Window != nil {
		if in.Window.End.Before(in.Window.Start) {
			return invalid("a window ends after it starts")
		}
		k.Window = &Window{Start: in.Window.Start, End: in.Window.End}
	}
	if k.Kind == KindPlanned && k.Window == nil {
		return invalid("planned maintenance needs its window")
	}
	if in.Source != nil {
		if !contains([]string{SourceOperator, SourceAlert, SourcePlanned, SourceAgent, SourceConfluence}, *in.Source) {
			return invalid("a known issue comes from operator, alert, plannedChange, agent or confluence")
		}
		k.Source = *in.Source
	}
	if in.Link != nil {
		if v := strings.TrimSpace(*in.Link); v != "" && !webURL(v) {
			return invalid("the full page must be an http or https address")
		} else {
			k.Link = v
		}
	}
	return nil
}

// Known lists the known issues: for an operator asking for all every
// one, drafts and inactive ones too (the console's Inactive filter); for
// anyone else the open ones and planned maintenance not yet over, as the
// report page's side panel shows them, never one the housekeeping made
// inactive as a duplicate (CF-63).
func (s *Service) Known(ctx context.Context, c Caller, all bool) ([]KnownIssue, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []KnownIssue
	for i := len(st.known.Known) - 1; i >= 0; i-- {
		k := st.known.Known[i]
		switch {
		case c.Operator && all:
		case k.Draft:
			continue
		case k.Status == KnownResolved:
			continue
		case k.Inactive:
			continue
		case k.Kind == KindPlanned && k.Window != nil && now.After(k.Window.End):
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

// KnownIssue is one known issue; a draft only for an operator.
func (s *Service) KnownIssue(ctx context.Context, c Caller, id string) (KnownIssue, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return KnownIssue{}, err
	}
	k := st.knownIssue(id)
	if k == nil || (k.Draft && !c.Operator) {
		return KnownIssue{}, fmt.Errorf("%w: no known issue %s", ErrNotFound, id)
	}
	return *k, nil
}

// LinkedReports are the numbers of the cases linked to a known issue.
func (s *Service) LinkedReports(ctx context.Context, id string) ([]int, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, l := range st.index.Lines {
		if l.KnownIssue == id {
			out = append(out, l.Number)
		}
	}
	return out, nil
}

// SaveKnown creates a known issue (id "") or changes one (IS-08). An
// operator's is accepted at once; one with the source agent is a draft
// until an operator accepts it.
func (s *Service) SaveKnown(ctx context.Context, c Caller, id string, in KnownInput) (KnownIssue, error) {
	if !c.Operator {
		return KnownIssue{}, fmt.Errorf("%w: only operators write known issues", ErrDenied)
	}
	return s.saveKnown(ctx, c.Person, c.From, id, in)
}

// Propose adds a known issue from outside a person's hands: an alert, a
// planned change, or an agent, whose proposal waits for an operator.
func (s *Service) Propose(ctx context.Context, source string, in KnownInput) (KnownIssue, error) {
	in.Source = &source
	return s.saveKnown(ctx, Person{ID: "system:" + source, Name: source, System: true}, "", "", in)
}

func (s *Service) saveKnown(ctx context.Context, by Person, from, id string, in KnownInput) (KnownIssue, error) {
	var out KnownIssue
	var resolved bool
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		now := s.now()
		var k *KnownIssue
		before := KnownStatus("")
		if id == "" {
			creator := by
			st.known.Known = append(st.known.Known, KnownIssue{ID: fmt.Sprintf("KI-%d", st.known.Next), Status: KnownInvestigating, Kind: KindNormal, Source: SourceOperator, CreatedBy: &creator, Created: now})
			st.known.Next++
			k = &st.known.Known[len(st.known.Known)-1]
		} else if k = st.knownIssue(id); k == nil {
			return 0, fmt.Errorf("%w: no known issue %s", ErrNotFound, id)
		} else {
			before = k.Status
		}
		if err := in.apply(st, k); err != nil {
			return 0, err
		}
		if id == "" && (k.Source == SourceAgent || k.Source == SourceConfluence) {
			k.Draft = true
		}
		k.Updated = now
		if k.Status == KnownResolved && before != KnownResolved {
			k.Resolved = ptr(now)
			resolved = !k.Draft
			verifyAfterResolve(k)
		} else if k.Status != KnownResolved {
			k.Resolved = nil
		}
		if !by.System {
			// A person changed it: it waits for its owner no more.
			k.Pending = false
		}
		if before != "" && before != k.Status {
			addUpdate(k, Update{At: now, By: by, Text: "Status: " + knownStatusWords[k.Status] + ".", Status: k.Status})
		}
		out = *copyKnown(k)
		ch.event(Event{Kind: EventKnownChanged, At: now, Known: copyKnown(k), By: &by})
		verb := "changed"
		if id == "" {
			verb = "created"
		}
		ch.audit(AuditRecord{Action: ActionKnown, Who: by, Target: k.ID, Detail: fmt.Sprintf("known issue %s %s: %s (%s)", k.ID, verb, k.Title, k.Status), From: from})
		return saveKnown, nil
	})
	if err == nil && resolved {
		err = s.resolveLinked(ctx, out)
	}
	return out, err
}

// knownStatusWords are a known issue's status as people read it.
var knownStatusWords = map[KnownStatus]string{KnownInvestigating: "investigating", KnownIdentified: "identified", KnownFixInProgress: "fix in progress", KnownResolved: "resolved"}

// Log adds a dated entry to a known issue's log.
func (k *KnownIssue) Log(u Update) { addUpdate(k, u) }

func addUpdate(k *KnownIssue, u Update) {
	k.Updates = append(k.Updates, u)
	if len(k.Updates) > MaxUpdates {
		k.Updates = k.Updates[len(k.Updates)-MaxUpdates:]
	}
}

// verifyAfterResolve names the jobs whose next run checks a resolved
// known issue: those that found its keys (CF-06).
func verifyAfterResolve(k *KnownIssue) {
	k.VerifyJobs = nil
	for _, x := range k.Keys {
		if x.Job != "" && !contains(k.VerifyJobs, x.Job) {
			k.VerifyJobs = append(k.VerifyJobs, x.Job)
		}
	}
}

// resolveLinked tells the cases closed as a resolved known issue that it
// is (IS-15, CF-06): their reporters read it on the report page; none of
// them reopens.
func (s *Service) resolveLinked(ctx context.Context, k KnownIssue) error {
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		now := s.now()
		n := 0
		for _, l := range st.index.Lines {
			if l.KnownIssue != k.ID {
				continue
			}
			r := st.report(l.Number)
			if r == nil {
				continue
			}
			text := k.Title + " is resolved."
			if u := k.LastUpdate(); u != nil && u.Text != "" && !strings.HasPrefix(u.Text, "Status: ") {
				text += " " + u.Text
			}
			_ = addMessage(r, Message{At: now, Kind: FromSystem, Text: text + " Say so if it is still broken for you."})
			r.Updated = now
			ch.event(Event{Kind: EventReportMessage, At: now, Report: copyReport(r)})
			n++
		}
		if n == 0 {
			return 0, nil
		}
		return saveReports, nil
	})
}

// AddUpdate adds a dated entry to a known issue's log, and changes its
// status when one is given (IS-08).
func (s *Service) AddUpdate(ctx context.Context, c Caller, id, text string, status KnownStatus) (KnownIssue, error) {
	if !c.Operator {
		return KnownIssue{}, fmt.Errorf("%w: only operators update known issues", ErrDenied)
	}
	clean, _, err := cleanText(text)
	if err != nil {
		return KnownIssue{}, err
	}
	if status != "" && !status.Valid() {
		return KnownIssue{}, invalid("%q is not a known issue's status", status)
	}
	var out KnownIssue
	var resolved bool
	err = s.update(ctx, func(st *state, ch *change) (int, error) {
		k := st.knownIssue(id)
		if k == nil {
			return 0, fmt.Errorf("%w: no known issue %s", ErrNotFound, id)
		}
		now := s.now()
		u := Update{At: now, By: c.Person, Text: clean}
		if status != "" && status != k.Status {
			u.Status = status
			resolved = status == KnownResolved && !k.Draft
			k.Status = status
			if status == KnownResolved {
				k.Resolved = ptr(now)
				verifyAfterResolve(k)
			} else {
				k.Resolved = nil
			}
		}
		addUpdate(k, u)
		k.Updated, k.Pending = now, false
		out = *copyKnown(k)
		ch.event(Event{Kind: EventKnownUpdate, At: now, Known: copyKnown(k), By: &c.Person})
		ch.audit(AuditRecord{Action: ActionKnown, Who: c.Person, Target: k.ID, Detail: fmt.Sprintf("known issue %s updated (%s)", k.ID, k.Status), From: c.From})
		return saveKnown, nil
	})
	if err == nil && resolved {
		err = s.resolveLinked(ctx, out)
	}
	return out, err
}

// Accept makes an agent's proposal a known issue reports may match.
func (s *Service) Accept(ctx context.Context, c Caller, id string) (KnownIssue, error) {
	if !c.Operator {
		return KnownIssue{}, fmt.Errorf("%w: only operators accept known issues", ErrDenied)
	}
	var out KnownIssue
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		k := st.knownIssue(id)
		if k == nil {
			return 0, fmt.Errorf("%w: no known issue %s", ErrNotFound, id)
		}
		if !k.Draft {
			return 0, invalid("%s is already accepted", id)
		}
		now := s.now()
		k.Draft, k.Updated = false, now
		addUpdate(k, Update{At: now, By: c.Person, Text: "Accepted from a proposal (" + k.Source + ")."})
		out = *copyKnown(k)
		ch.event(Event{Kind: EventKnownChanged, At: now, Known: copyKnown(k), By: &c.Person})
		ch.audit(AuditRecord{Action: ActionKnown, Who: c.Person, Target: k.ID, Detail: "known issue " + k.ID + " accepted: " + k.Title, From: c.From})
		return saveKnown, nil
	})
	return out, err
}

// DeleteKnown removes a known issue and unlinks its reports.
func (s *Service) DeleteKnown(ctx context.Context, c Caller, id string) error {
	if !c.Operator {
		return fmt.Errorf("%w: only operators delete known issues", ErrDenied)
	}
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		kept := st.known.Known[:0:0]
		found := false
		for _, k := range st.known.Known {
			if k.ID == id {
				found = true
				continue
			}
			kept = append(kept, k)
		}
		if !found {
			return 0, fmt.Errorf("%w: no known issue %s", ErrNotFound, id)
		}
		st.known.Known = kept
		parts := saveKnown
		for _, l := range st.index.Lines {
			if l.KnownIssue != id {
				continue
			}
			if r := st.report(l.Number); r != nil {
				r.KnownIssue, r.MatchConfidence = "", 0
				parts |= saveReports
			}
		}
		ch.audit(AuditRecord{Action: ActionKnown, Who: c.Person, Target: id, Detail: "known issue " + id + " deleted", From: c.From})
		return parts, nil
	})
}

// Follow makes the caller a watcher of a known issue, told of its
// updates on the report page (IS-30), or stops it.
func (s *Service) Follow(ctx context.Context, c Caller, id string, on bool) (KnownIssue, error) {
	if c.ID == "" || c.System {
		return KnownIssue{}, fmt.Errorf("%w: sign in to follow a known issue", ErrDenied)
	}
	var out KnownIssue
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		k := st.knownIssue(id)
		if k == nil || (k.Draft && !c.Operator) {
			return 0, fmt.Errorf("%w: no known issue %s", ErrNotFound, id)
		}
		kept := k.Followers[:0:0]
		for _, f := range k.Followers {
			if f.ID != c.ID {
				kept = append(kept, f)
			}
		}
		if on {
			if len(kept) >= MaxWatchers*10 {
				return 0, invalid("%s has as many followers as it keeps", id)
			}
			kept = append(kept, Person{ID: c.ID, Name: c.Name, Email: c.Email})
		}
		k.Followers = kept
		out = *copyKnown(k)
		return saveKnown, nil
	})
	return out, err
}

// Services lists the services.
func (s *Service) Services(ctx context.Context) ([]ServiceDef, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	return st.config.Services, nil
}

var slugChars = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := strings.Trim(slugChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// SaveService creates a service (id "") or replaces one (IS-09).
func (s *Service) SaveService(ctx context.Context, c Caller, id string, in ServiceDef) (ServiceDef, error) {
	if !c.Operator {
		return ServiceDef{}, fmt.Errorf("%w: only operators write services", ErrDenied)
	}
	var err error
	if in.Name, err = short("the name", in.Name, 120); err != nil {
		return ServiceDef{}, err
	}
	if in.Name == "" || slug(in.Name) == "" {
		return ServiceDef{}, invalid("give the service a name")
	}
	if in.Description, err = short("the description", in.Description, 1000); err != nil {
		return ServiceDef{}, err
	}
	if len(in.RunsOn) > 50 || len(in.Contacts) > 50 || len(in.Aliases) > 50 {
		return ServiceDef{}, invalid("at most 50 of each: places it runs on, contacts and aliases")
	}
	for i, r := range in.RunsOn {
		if !contains(runsOnKinds, r.Kind) {
			return ServiceDef{}, invalid("a service runs on a %s", strings.Join(runsOnKinds, ", "))
		}
		if in.RunsOn[i].Value, err = short("a place it runs on", r.Value, MaxShort); err != nil {
			return ServiceDef{}, err
		}
	}
	for i, a := range in.Aliases {
		if in.Aliases[i], err = short("an alias", a, 120); err != nil {
			return ServiceDef{}, err
		}
	}
	var out ServiceDef
	err = s.update(ctx, func(st *state, ch *change) (int, error) {
		now := s.now()
		in.Updated = now
		if id == "" {
			if len(st.config.Services) >= MaxServices {
				return 0, invalid("at most %d services; delete one no longer used", MaxServices)
			}
			base := slug(in.Name)
			in.ID = base
			for n := 2; st.service(in.ID) != nil; n++ {
				in.ID = fmt.Sprintf("%s-%d", base, n)
			}
			in.Created = now
			st.config.Services = append(st.config.Services, in)
		} else {
			old := st.service(id)
			if old == nil {
				return 0, fmt.Errorf("%w: no service %q", ErrNotFound, id)
			}
			in.ID, in.Created = id, old.Created
			*old = in
		}
		out = in
		ch.audit(AuditRecord{Action: ActionService, Who: c.Person, Target: "service " + in.ID, Detail: fmt.Sprintf("service %s saved: %d contacts, %d places it runs on", in.Name, len(in.Contacts), len(in.RunsOn)), From: c.From})
		return saveConfig, nil
	})
	return out, err
}

// DeleteService removes a service; its reports' service becomes unknown.
func (s *Service) DeleteService(ctx context.Context, c Caller, id string) error {
	if !c.Operator {
		return fmt.Errorf("%w: only operators delete services", ErrDenied)
	}
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		kept := st.config.Services[:0:0]
		for _, sv := range st.config.Services {
			if sv.ID != id {
				kept = append(kept, sv)
			}
		}
		if len(kept) == len(st.config.Services) {
			return 0, fmt.Errorf("%w: no service %q", ErrNotFound, id)
		}
		st.config.Services = kept
		parts := saveConfig
		for _, l := range st.index.Lines {
			if l.Service != id {
				continue
			}
			if r := st.report(l.Number); r != nil {
				r.Service, r.ServiceConfidence, r.ServiceBy = "", 0, ""
				parts |= saveReports
			}
		}
		for i := range st.known.Known {
			k := &st.known.Known[i]
			var ks []string
			for _, x := range k.Services {
				if x != id {
					ks = append(ks, x)
				}
			}
			if len(ks) != len(k.Services) {
				k.Services = ks
				parts |= saveKnown
			}
		}
		ch.audit(AuditRecord{Action: ActionService, Who: c.Person, Target: "service " + id, Detail: "service " + id + " deleted", From: c.From})
		return parts, nil
	})
}

// Match is the preview of what a text would match (IS-10): the open
// known issues and the services, with their confidence, and the
// threshold at which a report is linked by itself.
type Match struct {
	Known     []Candidate `json:"known"`
	Services  []Candidate `json:"services"`
	Threshold int         `json:"threshold"`
}

// MatchText ranks the known issues and services for a text.
func (s *Service) MatchText(ctx context.Context, text string) (Match, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return Match{}, err
	}
	return Match{Known: st.matchKnown(text, nil), Services: st.matchService(text), Threshold: st.settings().MatchThreshold}, nil
}

// Settings are the service's settings.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	st, err := s.load(ctx, false)
	if err != nil {
		return Settings{}, err
	}
	return st.settings(), nil
}

// SaveSettings replaces the settings (IS-29), for an administrator.
func (s *Service) SaveSettings(ctx context.Context, c Caller, set Settings) (Settings, error) {
	if !c.Admin {
		return Settings{}, fmt.Errorf("%w: only a SuperAdmin changes the issue settings", ErrDenied)
	}
	set.Company, set.StatusPage, set.ReportURL, set.Webhook.URL = strings.TrimSpace(set.Company), strings.TrimSpace(set.StatusPage), strings.TrimSpace(set.ReportURL), strings.TrimSpace(set.Webhook.URL)
	if err := set.Validate(); err != nil {
		return Settings{}, err
	}
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		before := st.settings()
		st.config.Settings = &set
		ch.audit(AuditRecord{Action: ActionSettings, Who: c.Person, Target: "issue settings", Detail: settingsChanges(before, set), From: c.From})
		return saveConfig, nil
	})
	return set, err
}

// settingsChanges names what changed, never the webhook's address, which
// may carry a token in its path.
func settingsChanges(a, b Settings) string {
	var out []string
	if a.Company != b.Company {
		out = append(out, "company name")
	}
	if a.StatusPage != b.StatusPage {
		out = append(out, "status page")
	}
	if a.ReportURL != b.ReportURL {
		out = append(out, "report page address")
	}
	if a.MatchThreshold != b.MatchThreshold {
		out = append(out, fmt.Sprintf("match threshold %d", b.MatchThreshold))
	}
	if fmt.Sprint(a.Targets) != fmt.Sprint(b.Targets) {
		out = append(out, "SLA targets")
	}
	if a.RetentionDays != b.RetentionDays {
		out = append(out, fmt.Sprintf("retention %d days", b.RetentionDays))
	}
	if a.RatePerHour != b.RatePerHour {
		out = append(out, fmt.Sprintf("rate limit %d an hour", b.RatePerHour))
	}
	if a.ReopenDays != b.ReopenDays || a.FixedDays != b.FixedDays || a.NeedInfoDays != b.NeedInfoDays || a.DuplicateHours != b.DuplicateHours || a.BumpP1At != b.BumpP1At || a.BumpP2At != b.BumpP2At {
		out = append(out, "timers")
	}
	if a.Webhook.URL != b.Webhook.URL || strings.Join(a.Webhook.Events, ",") != strings.Join(b.Webhook.Events, ",") {
		out = append(out, "webhook")
	}
	if len(out) == 0 {
		return "issue settings saved unchanged"
	}
	return "issue settings changed: " + strings.Join(out, ", ")
}

// Since is a time a page last asked, for Updates.
func Since(s string, now time.Time) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.After(now) {
		return now.Add(-time.Minute)
	}
	return t
}

// --- Housekeeping (Phase 12, wave 6g, CF-53) ---

// Duplicate is a known issue made inactive as a duplicate of another.
type Duplicate struct {
	ID string
	Of string
}

// knownAlike is how alike two known issues' titles and symptoms are for
// one to be a duplicate of the other.
const knownAlike = 0.7

// HousekeepKnown makes inactive the open known issues that repeat an
// older open one (their titles and symptoms alike), naming it; the older
// one stays. The cases linked to one keep it.
func (s *Service) HousekeepKnown(ctx context.Context, by Person) ([]Duplicate, error) {
	var out []Duplicate
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		out = nil
		now := s.now()
		list := st.known.Known
		text := func(k *KnownIssue) string { return k.Title + " " + strings.Join(k.Symptoms, " ") }
		for i := range list {
			for j := range list {
				a, b := &list[i], &list[j]
				if i == j || !a.Open() || !b.Open() || !a.Created.Before(b.Created) && !(a.Created.Equal(b.Created) && i < j) {
					continue
				}
				if fixes.Likeness(text(a), text(b)) < knownAlike {
					continue
				}
				b.Inactive, b.DuplicateOf, b.Updated = true, a.ID, now
				out = append(out, Duplicate{ID: b.ID, Of: a.ID})
				ch.audit(AuditRecord{Action: ActionKnown, Who: by, Target: b.ID, Detail: "known issue " + b.ID + " made inactive by the housekeeping: a duplicate of " + a.ID})
			}
		}
		if len(out) == 0 {
			return 0, nil
		}
		return saveKnown, nil
	})
	return out, err
}
