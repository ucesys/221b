// Package fixes keeps the fixes (Phase 12, wave 6g, CF-50 to CF-55): what
// fixed a problem, saved when a case closes as Fixed with Save fix on, for
// the cases like it later. A fix is the console's own record, beside the
// known issues and the accepted exceptions, and people edit it; the wiki
// stays Mycroft's. One fix stands for one problem and lists the cases it
// fixed, which go after their retention while the fix stays.
//
// A fix is active, pending or inactive. Active fixes go to the agents.
// Two fixes of the same problem that say different things are both
// pending, each naming the other, until a person accepts one; a fix that
// says what another says is a duplicate, made inactive by the daily
// housekeeping (Housekeep), which a person can undo.
package fixes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// Key is the fixes' key in the casework ConfigMap.
const Key = "fixes.json"

// DefaultBytes is the room the fixes take in it.
const DefaultBytes = 256 << 10

// The audit actions.
const (
	ActionSave       = "fix.save"
	ActionEdit       = "fix.edit"
	ActionDelete     = "fix.delete"
	ActionHousekeep  = "fix.housekeep"
	housekeeperLabel = "Housekeeping"
)

// The statuses.
const (
	Active   = "active"
	Pending  = "pending"
	Inactive = "inactive"
)

// Why a fix is pending or inactive.
const (
	ReasonDuplicate   = "duplicate"
	ReasonContradicts = "contradicts"
	ReasonPerson      = "person"
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("not valid")
	ErrFull     = errors.New("full")
)

const (
	maxTitle    = 200
	maxText     = 4000
	maxCommand  = 600
	maxCommands = 20
	maxSeen     = 200
	maxFixes    = 2000
)

// Fix is what fixed a problem.
type Fix struct {
	// ID is "F-<n>".
	ID    string `json:"id"`
	Title string `json:"title"`
	// Key is the agents' key of the problem (lessons.Key), when a case
	// they opened had one: the same key is the same problem.
	Key string `json:"key,omitempty"`
	// Cluster and Resource are where it was first seen.
	Cluster  string `json:"cluster,omitempty"`
	Resource string `json:"resource,omitempty"`
	Symptoms string `json:"symptoms,omitempty"`
	Cause    string `json:"cause,omitempty"`
	// Steps say what to do; Commands are the changes that worked.
	Steps      string   `json:"steps,omitempty"`
	Commands   []string `json:"commands,omitempty"`
	Prevention string   `json:"prevention,omitempty"`
	Status     string   `json:"status"`
	// Reason says why it is pending or inactive; DuplicateOf the fix it
	// repeats; Contradicts the fixes of the same problem that say
	// otherwise, while pending.
	Reason      string   `json:"reason,omitempty"`
	DuplicateOf string   `json:"duplicateOf,omitempty"`
	Contradicts []string `json:"contradicts,omitempty"`
	// Accepted are the fixes a person accepted it beside: the pair is not
	// flagged again.
	Accepted []string `json:"accepted,omitempty"`
	// Cases are the cases it fixed, oldest first.
	Cases     []Seen    `json:"cases"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	CreatedBy string    `json:"createdBy"`
	UpdatedBy string    `json:"updatedBy"`
}

// Seen is a case a fix fixed.
type Seen struct {
	Case    string    `json:"case"`
	At      time.Time `json:"at"`
	Cluster string    `json:"cluster,omitempty"`
	By      string    `json:"by,omitempty"`
}

// Person is who acts.
type Person struct {
	Subject string
	Email   string
	Name    string
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

func (p Person) who() audit.Who { return audit.Who{Subject: p.Subject, Email: p.Email, Name: p.Name} }

// Housekeeper is who the daily housekeeping acts as.
var Housekeeper = Person{Subject: "221b", Name: housekeeperLabel}

// Options configure the service.
type Options struct {
	// Backend keeps the fixes: the casework ConfigMap; nil is memory.
	Backend kv.Backend
	Bytes   int
	Audit   *audit.Log
	Now     func() time.Time
	Logger  *slog.Logger
}

// Service keeps the fixes.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Bytes <= 0 {
		o.Bytes = DefaultBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Service{o: o}
}

// Backend is the storage, for a backup.
func (s *Service) Backend() kv.Backend { return s.o.Backend }

func (s *Service) record(e audit.Event) {
	if s.o.Audit != nil {
		s.o.Audit.Record(e)
	}
}

type record struct {
	Next  int   `json:"next"`
	Items []Fix `json:"items"`
}

var errUnchanged = errors.New("unchanged")

func (s *Service) load(ctx context.Context) (record, string, error) {
	var rec record
	raw, version, err := s.o.Backend.LoadKey(ctx, Key)
	if errors.Is(err, kv.ErrNotFound) {
		return rec, version, nil
	}
	if err != nil {
		return rec, "", err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, "", fmt.Errorf("the record %s cannot be read: %w", Key, err)
	}
	return rec, version, nil
}

// update changes the fixes over the version read, again when another
// replica wrote first.
func (s *Service) update(ctx context.Context, f func(*record) error) error {
	for try := 0; try < 10; try++ {
		rec, version, err := s.load(ctx)
		if err != nil {
			return err
		}
		if err := f(&rec); errors.Is(err, errUnchanged) {
			return nil
		} else if err != nil {
			return err
		}
		b, _ := json.Marshal(rec)
		if len(b) > s.o.Bytes || len(rec.Items) > maxFixes {
			return fmt.Errorf("%w: the fixes fill their record; delete the inactive ones in Cases › Mycroft › Fixes first", ErrFull)
		}
		if _, err = s.o.Backend.SaveKey(ctx, Key, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("the record %s kept changing", Key)
}

// List is every fix, the latest updated first.
func (s *Service) List(ctx context.Context) ([]Fix, error) {
	rec, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := rec.Items
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Get is one fix.
func (s *Service) Get(ctx context.Context, id string) (Fix, error) {
	rec, _, err := s.load(ctx)
	if err != nil {
		return Fix{}, err
	}
	if i := find(rec.Items, id); i >= 0 {
		return rec.Items[i], nil
	}
	return Fix{}, fmt.Errorf("%w: no fix %s", ErrNotFound, id)
}

func find(items []Fix, id string) int {
	for i := range items {
		if strings.EqualFold(items[i].ID, id) {
			return i
		}
	}
	return -1
}

// ---------- saving a case's fix ----------

// FromCase is a case closed as Fixed, to save as a fix.
type FromCase struct {
	Cases      []string
	Title      string
	Key        string
	Cluster    string
	Resource   string
	Symptoms   string
	Cause      string
	Steps      string
	Commands   []string
	Prevention string
	At         time.Time
}

// Saved says what saving a case's fix did.
type Saved struct {
	Fix Fix
	// Added is true when the case joined a fix of the same problem that
	// says the same; Contradicts the fixes the new one contradicts.
	Added       bool
	Contradicts []string
}

// SaveFromCase saves a case's fix: the case joins the active or pending
// fix of the same problem that says the same, or a new fix is made, which
// is pending beside the ones of the same problem that say otherwise.
func (s *Service) SaveFromCase(ctx context.Context, in FromCase, by Person) (Saved, error) {
	now := s.o.Now().UTC()
	at := in.At
	if at.IsZero() {
		at = now
	}
	f := Fix{Title: clip(in.Title, maxTitle), Key: clip(in.Key, 300), Cluster: clip(in.Cluster, 100), Resource: clip(in.Resource, 300),
		Symptoms: clip(in.Symptoms, maxText), Cause: clip(in.Cause, maxText), Steps: clip(in.Steps, maxText), Commands: clipAll(in.Commands), Prevention: clip(in.Prevention, maxText),
		Status: Active, Created: now, Updated: now, CreatedBy: by.Label(), UpdatedBy: by.Label()}
	if f.Title == "" {
		return Saved{}, fmt.Errorf("%w: the fix has no title", ErrInvalid)
	}
	if f.Steps == "" && len(f.Commands) == 0 {
		return Saved{}, fmt.Errorf("%w: say what was done", ErrInvalid)
	}
	var seen []Seen
	for _, c := range in.Cases {
		if c = clip(c, 40); c != "" {
			seen = append(seen, Seen{Case: c, At: at, Cluster: f.Cluster, By: by.Label()})
		}
	}
	var out Saved
	err := s.update(ctx, func(rec *record) error {
		out = Saved{}
		for i := range rec.Items {
			o := &rec.Items[i]
			if o.Status == Inactive || !sameProblem(*o, f) || !sameAdvice(*o, f) {
				continue
			}
			o.Cases = addSeen(o.Cases, seen...)
			o.Updated, o.UpdatedBy = now, by.Label()
			out = Saved{Fix: *o, Added: true}
			return nil
		}
		rec.Next++
		f.ID = "F-" + strconv.Itoa(rec.Next)
		f.Cases = addSeen(nil, seen...)
		for i := range rec.Items {
			o := &rec.Items[i]
			if o.Status == Inactive || !sameProblem(*o, f) {
				continue
			}
			contradict(o, &f, now, by.Label())
			out.Contradicts = append(out.Contradicts, o.ID)
		}
		rec.Items = append(rec.Items, f)
		out.Fix = f
		return nil
	})
	if err != nil {
		return Saved{}, err
	}
	detail := fmt.Sprintf("fix %s saved from %s: %s", out.Fix.ID, strings.Join(in.Cases, ", "), out.Fix.Title)
	switch {
	case out.Added:
		detail = fmt.Sprintf("%s added to fix %s, which says the same: %s", strings.Join(in.Cases, ", "), out.Fix.ID, out.Fix.Title)
	case len(out.Contradicts) > 0:
		detail += "; pending, it contradicts " + strings.Join(out.Contradicts, ", ")
	}
	s.record(audit.Event{Action: ActionSave, Who: by.who(), Cluster: out.Fix.Cluster, Target: out.Fix.ID, Outcome: audit.OutcomeOK, Detail: detail})
	return out, nil
}

// contradict makes two fixes of the same problem pending, each naming the
// other, unless a person accepted them side by side.
func contradict(a, b *Fix, now time.Time, by string) bool {
	if slices.Contains(a.Accepted, b.ID) || slices.Contains(b.Accepted, a.ID) {
		return false
	}
	changed := false
	for _, p := range [][2]*Fix{{a, b}, {b, a}} {
		x, y := p[0], p[1]
		if x.Status == Pending && slices.Contains(x.Contradicts, y.ID) {
			continue
		}
		x.Status, x.Reason = Pending, ReasonContradicts
		if !slices.Contains(x.Contradicts, y.ID) {
			x.Contradicts = append(x.Contradicts, y.ID)
		}
		x.Updated, x.UpdatedBy = now, by
		changed = true
	}
	return changed
}

func addSeen(list []Seen, more ...Seen) []Seen {
	for _, m := range more {
		if !slices.ContainsFunc(list, func(x Seen) bool { return x.Case == m.Case }) {
			list = append(list, m)
		}
	}
	if len(list) > maxSeen {
		list = list[len(list)-maxSeen:]
	}
	return list
}

// ---------- a person's edits ----------

// Edit is what a person changes on a fix; nil leaves a field.
type Edit struct {
	Title      *string   `json:"title,omitempty"`
	Symptoms   *string   `json:"symptoms,omitempty"`
	Cause      *string   `json:"cause,omitempty"`
	Steps      *string   `json:"steps,omitempty"`
	Commands   *[]string `json:"commands,omitempty"`
	Prevention *string   `json:"prevention,omitempty"`
	// Status "active" accepts a pending fix (its contradictions are not
	// flagged again) or makes an inactive one active; "inactive" turns it
	// off.
	Status *string `json:"status,omitempty"`
}

// Update changes a fix as a person says.
func (s *Service) Update(ctx context.Context, id string, in Edit, by Person) (Fix, error) {
	now := s.o.Now().UTC()
	var out Fix
	var what []string
	err := s.update(ctx, func(rec *record) error {
		what = nil
		i := find(rec.Items, id)
		if i < 0 {
			return fmt.Errorf("%w: no fix %s", ErrNotFound, id)
		}
		f := &rec.Items[i]
		set := func(name string, p *string, n int, field *string) error {
			if p == nil {
				return nil
			}
			v := clip(*p, n)
			if name == "title" && v == "" {
				return fmt.Errorf("%w: a fix needs a title", ErrInvalid)
			}
			if v != *field {
				*field = v
				what = append(what, name)
			}
			return nil
		}
		for _, x := range []struct {
			name  string
			p     *string
			n     int
			field *string
		}{{"title", in.Title, maxTitle, &f.Title}, {"symptoms", in.Symptoms, maxText, &f.Symptoms}, {"cause", in.Cause, maxText, &f.Cause}, {"steps", in.Steps, maxText, &f.Steps}, {"prevention", in.Prevention, maxText, &f.Prevention}} {
			if err := set(x.name, x.p, x.n, x.field); err != nil {
				return err
			}
		}
		if in.Commands != nil {
			if c := clipAll(*in.Commands); !slices.Equal(c, f.Commands) {
				f.Commands = c
				what = append(what, "commands")
			}
		}
		if in.Status != nil {
			switch *in.Status {
			case Active:
				if f.Status != Active {
					what = append(what, "made active")
					if f.Status == Pending {
						for _, o := range f.Contradicts {
							if !slices.Contains(f.Accepted, o) {
								f.Accepted = append(f.Accepted, o)
							}
							// The other one is accepted beside it too, and is
							// active again when nothing else holds it.
							if j := find(rec.Items, o); j >= 0 {
								other := &rec.Items[j]
								if !slices.Contains(other.Accepted, f.ID) {
									other.Accepted = append(other.Accepted, f.ID)
								}
								other.Contradicts = slices.DeleteFunc(other.Contradicts, func(x string) bool { return x == f.ID })
								if other.Status == Pending && other.Reason == ReasonContradicts && len(other.Contradicts) == 0 {
									other.Status, other.Reason = Active, ""
								}
								other.Updated, other.UpdatedBy = now, by.Label()
							}
						}
					}
				}
				f.Status, f.Reason, f.DuplicateOf, f.Contradicts = Active, "", "", nil
			case Inactive:
				if f.Status != Inactive {
					what = append(what, "made inactive")
				}
				f.Status, f.Reason, f.DuplicateOf, f.Contradicts = Inactive, ReasonPerson, "", nil
			default:
				return fmt.Errorf("%w: a fix is made active or inactive", ErrInvalid)
			}
		}
		if f.Steps == "" && len(f.Commands) == 0 {
			return fmt.Errorf("%w: a fix says what to do: steps or commands", ErrInvalid)
		}
		if len(what) == 0 {
			out = *f
			return errUnchanged
		}
		f.Updated, f.UpdatedBy = now, by.Label()
		out = *f
		return nil
	})
	if err != nil {
		return Fix{}, err
	}
	if len(what) == 0 {
		return out, nil
	}
	s.record(audit.Event{Action: ActionEdit, Who: by.who(), Cluster: out.Cluster, Target: out.ID, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("fix %s changed (%s): %s", out.ID, strings.Join(what, ", "), out.Title)})
	return out, nil
}

// Delete deletes a fix.
func (s *Service) Delete(ctx context.Context, id string, by Person) (Fix, error) {
	var out Fix
	err := s.update(ctx, func(rec *record) error {
		i := find(rec.Items, id)
		if i < 0 {
			return fmt.Errorf("%w: no fix %s", ErrNotFound, id)
		}
		out = rec.Items[i]
		rec.Items = append(rec.Items[:i:i], rec.Items[i+1:]...)
		for j := range rec.Items {
			o := &rec.Items[j]
			o.Contradicts = slices.DeleteFunc(o.Contradicts, func(x string) bool { return strings.EqualFold(x, out.ID) })
			if o.Status == Pending && o.Reason == ReasonContradicts && len(o.Contradicts) == 0 {
				o.Status, o.Reason = Active, ""
			}
		}
		return nil
	})
	if err != nil {
		return Fix{}, err
	}
	s.record(audit.Event{Action: ActionDelete, Who: by.who(), Cluster: out.Cluster, Target: out.ID, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("fix %s deleted: %s", out.ID, out.Title)})
	return out, nil
}

// ---------- the daily housekeeping ----------

// Done is one thing the housekeeping did.
type Done struct {
	ID string
	// What is "duplicate" (made inactive, a duplicate of Of) or
	// "contradicts" (pending, contradicting Of).
	What string
	Of   string
}

// Housekeep makes the duplicates inactive, keeping the fix that fixed the
// most cases (then the oldest) with their cases, and marks the fixes of the
// same problem that say otherwise pending.
func (s *Service) Housekeep(ctx context.Context) ([]Done, error) {
	now := s.o.Now().UTC()
	var done []Done
	err := s.update(ctx, func(rec *record) error {
		done = nil
		changed := false
		items := rec.Items
		live := func(i int) bool { return items[i].Status != Inactive }
		// Duplicates first: the one kept takes the others' cases.
		for i := range items {
			for j := range items {
				if i == j || !live(i) || !live(j) || !sameProblem(items[i], items[j]) || !sameAdvice(items[i], items[j]) {
					continue
				}
				keep, drop := i, j
				if better(items[j], items[i]) {
					keep, drop = j, i
				}
				k, d := &items[keep], &items[drop]
				k.Cases = addSeen(k.Cases, d.Cases...)
				k.Updated, k.UpdatedBy = now, housekeeperLabel
				d.Status, d.Reason, d.DuplicateOf, d.Contradicts = Inactive, ReasonDuplicate, k.ID, nil
				d.Updated, d.UpdatedBy = now, housekeeperLabel
				done = append(done, Done{ID: d.ID, What: ReasonDuplicate, Of: k.ID})
			}
		}
		for i := range items {
			for j := i + 1; j < len(items); j++ {
				if !live(i) || !live(j) || !sameProblem(items[i], items[j]) || sameAdvice(items[i], items[j]) {
					continue
				}
				if contradict(&items[i], &items[j], now, housekeeperLabel) {
					done = append(done, Done{ID: items[i].ID, What: ReasonContradicts, Of: items[j].ID})
				}
			}
		}
		// A contradiction with a fix no longer live ends.
		for i := range items {
			f := &items[i]
			if f.Status != Pending || f.Reason != ReasonContradicts {
				continue
			}
			f.Contradicts = slices.DeleteFunc(f.Contradicts, func(id string) bool {
				j := find(items, id)
				return j < 0 || items[j].Status == Inactive
			})
			if len(f.Contradicts) == 0 {
				f.Status, f.Reason, f.Updated, f.UpdatedBy = Active, "", now, housekeeperLabel
				changed = true
			}
		}
		if len(done) == 0 && !changed {
			return errUnchanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range done {
		detail := fmt.Sprintf("fix %s made inactive: a duplicate of %s", d.ID, d.Of)
		if d.What == ReasonContradicts {
			detail = fmt.Sprintf("fixes %s and %s pending: they fix the same problem differently", d.ID, d.Of)
		}
		s.record(audit.Event{Action: ActionHousekeep, Who: Housekeeper.who(), Target: d.ID, Outcome: audit.OutcomeOK, Detail: detail})
	}
	return done, nil
}

// better says a is the one to keep over b: it fixed more cases, then it
// is older.
func better(a, b Fix) bool {
	if len(a.Cases) != len(b.Cases) {
		return len(a.Cases) > len(b.Cases)
	}
	return a.Created.Before(b.Created) || (a.Created.Equal(b.Created) && num(a.ID) < num(b.ID))
}

func num(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(id), "F-"))
	return n
}

// ---------- for the agents ----------

// Hit is an active fix a text matches.
type Hit struct {
	Fix   Fix
	Score int
}

// Match gives the active fixes a text is about, the best first, at most
// limit: how much of the fix's problem the text names.
func (s *Service) Match(ctx context.Context, text string, limit int) ([]Hit, error) {
	rec, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	q := Words(text)
	if len(q) == 0 {
		return nil, nil
	}
	var out []Hit
	for _, f := range rec.Items {
		if f.Status != Active {
			continue
		}
		p := Words(problemText(f))
		if len(p) == 0 {
			continue
		}
		hit := 0
		for w := range p {
			if q[w] {
				hit++
			}
		}
		score := hit * 100 / len(p)
		if hit >= 2 && score >= matchMin {
			out = append(out, Hit{Fix: f, Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// matchMin is how much of a fix's problem a text names for it to match.
const matchMin = 50

// ---------- the same problem, the same advice ----------

// The thresholds of likeness, of the problem's words and of the advice's.
const (
	sameProblemMin = 0.6
	sameAdviceMin  = 0.5
)

// sameProblem says two fixes are of one problem: the same agents' key, or
// titles (and the kind of object) alike.
func sameProblem(a, b Fix) bool {
	if a.Key != "" && b.Key != "" {
		return strings.EqualFold(a.Key, b.Key)
	}
	return Likeness(problemText(a), problemText(b)) >= sameProblemMin
}

// sameAdvice says two fixes say the same: their changes and steps alike,
// once the names of the objects are left aside.
func sameAdvice(a, b Fix) bool {
	return Likeness(adviceText(a), adviceText(b)) >= sameAdviceMin
}

func problemText(f Fix) string { return f.Title + " " + kindOf(f.Resource) }

func adviceText(f Fix) string {
	if len(f.Commands) > 0 {
		return strings.Join(f.Commands, "\n")
	}
	return f.Steps
}

// kindOf is the kind of a resource named "ns/Kind/name" or "Kind/name".
func kindOf(resource string) string {
	parts := strings.Split(resource, "/")
	switch len(parts) {
	case 2:
		return parts[0]
	case 3:
		return parts[1]
	}
	return ""
}

// stop are the words too common to tell problems apart.
var stop = map[string]bool{"the": true, "a": true, "an": true, "of": true, "in": true, "on": true, "is": true, "are": true, "to": true, "and": true, "or": true, "for": true, "has": true, "have": true, "no": true, "not": true, "with": true, "it": true, "its": true, "be": true, "by": true, "at": true, "from": true, "this": true, "that": true}

// Words are a text's words, lower case, digits as "#", without the
// common ones.
func Words(text string) map[string]bool {
	out := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if w := b.String(); len(w) > 1 && !stop[w] {
			out[w] = true
		}
		b.Reset()
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune('#')
		case r >= 'a' && r <= 'z', r == '-', r == '.', r == '_', r == '=':
			b.WriteRune(r)
		case r > 127 && (r != '…'):
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Likeness is how alike two texts are, 0 to 1: the words they share over
// the words of either (Jaccard).
func Likeness(a, b string) float64 {
	wa, wb := Words(a), Words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	both := 0
	for w := range wa {
		if wb[w] {
			both++
		}
	}
	return float64(both) / float64(len(wa)+len(wb)-both)
}

// ---------- backups ----------

// Export is every fix, for a backup.
func (s *Service) Export(ctx context.Context) ([]byte, error) {
	rec, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rec)
}

// Import adds a backup's fixes that are not there, by id, and returns how
// many.
func (s *Service) Import(ctx context.Context, b []byte) (int, error) {
	var in record
	if err := json.Unmarshal(b, &in); err != nil {
		return 0, fmt.Errorf("%w: the fixes cannot be read: %v", ErrInvalid, err)
	}
	added := 0
	err := s.update(ctx, func(rec *record) error {
		added = 0
		for _, f := range in.Items {
			if find(rec.Items, f.ID) >= 0 {
				continue
			}
			rec.Items = append(rec.Items, f)
			added++
		}
		if in.Next > rec.Next {
			rec.Next = in.Next
		}
		if added == 0 {
			return errUnchanged
		}
		return nil
	})
	return added, err
}

// ---------- helpers ----------

// clip cuts a text to at most n characters, with anything that looks like
// a credential taken out (X-01).
func clip(text string, n int) string {
	text = scrub.Text(strings.TrimSpace(text))
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	return string(r[:n-1]) + "…"
}

func clipAll(list []string) []string {
	var out []string
	for _, c := range list {
		if c = clip(c, maxCommand); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) > maxCommands {
		out = out[:maxCommands]
	}
	return out
}
