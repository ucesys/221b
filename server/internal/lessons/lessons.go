// Package lessons keeps what people decided about what the agents found
// (Phase 12, FX-01 to FX-12, ADR-108 to ADR-110): the accepted exceptions,
// each a finding a person chose to ignore, with why and until when, so the
// next runs leave it out. They live in the console's own records only
// (MY-09, ADR-118). The root-cause analyses (RCAs) a case was completed
// with were kept here too until wave 6e; an RCA is now part of its case
// only (CF-44, ADR-133), and rcas.json is left for the clean start.
package lessons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// The keys, kept in the casework ConfigMap (<release>-casework) beside
// the cases.
const (
	ExceptionsKey = "exceptions.json"
	// RCAsKey held the RCAs of builds before wave 6e (CF-44): nothing
	// reads or writes it; the clean start drops it.
	RCAsKey = "rcas.json"
)

// The most each key holds, by default.
const (
	DefaultExceptionsBytes = 128 << 10
)

// The audit log's actions.
const (
	// ActionExceptionAdd is a finding ignored: an exception accepted.
	ActionExceptionAdd = "exception.add"
	// ActionExceptionRemove is an exception removed: made inactive.
	ActionExceptionRemove = "exception.remove"
	// ActionExceptionReactivate is an inactive exception made active
	// again (build 14).
	ActionExceptionReactivate = "exception.reactivate"
)

// The wiki's pages of the records: one page per exception, and per RCA
// of builds before wave 6e, under kinds of their own, which only this
// package wrote; Mycroft may not write pages of these kinds.
const (
	WikiExceptionKind = "exception"
	WikiRCAKind       = "rca"
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("not valid")
	// ErrFull is an exception the records have no room for.
	ErrFull = errors.New("full")
	// ErrConflict is an exception an active one already covers.
	ErrConflict = errors.New("conflict")
)

// What a record keeps at most.
const (
	maxTitle     = 200
	maxDetail    = 2000
	maxReason    = 1000
	maxExcepts   = 500
	maxWikiError = 300
)

// Person is who does something, as the audit log names them.
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

func (p Person) who() audit.Who { return audit.Who{Subject: p.Subject, Email: p.Email, Name: p.Name} }

// Exception is an accepted exception: a finding a person chose to ignore.
// Later runs leave out a finding on the same cluster and resource with the
// same check while it is active: not removed, and not past its expiry.
type Exception struct {
	// ID is "EX-<n>".
	ID string `json:"id"`
	// Cluster and Resource are the finding's: "namespace/kind/name" or
	// "kind/name"; empty for none.
	Cluster  string `json:"cluster,omitempty"`
	Resource string `json:"resource,omitempty"`
	// Check is the kind of check, as Key gives it: the finding's check id,
	// or its title made plain.
	Check string `json:"check"`
	// Title and Detail are the finding's.
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Finding, Run and Job name where it came from.
	Finding string `json:"finding,omitempty"`
	Run     string `json:"run,omitempty"`
	Job     string `json:"job,omitempty"`
	// Reason is why it is accepted (required).
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	// Expires is when it stops applying; zero is never.
	Expires time.Time `json:"expires,omitzero"`
	// RemovedBy and RemovedAt say it was removed.
	RemovedBy string    `json:"removedBy,omitempty"`
	RemovedAt time.Time `json:"removedAt,omitzero"`
	// Wiki is its page in the wiki, once written; WikiError why it could
	// not be.
	Wiki      string `json:"wiki,omitempty"`
	WikiError string `json:"wikiError,omitempty"`
	// --- One flow for findings and cases (Phase 12, wave 6, CF-13) ---
	// Case is the agent-found case closed as it ("C-12"); Matches counts
	// the findings it dropped since, LastMatched the newest.
	Case        string    `json:"case,omitempty"`
	Matches     int       `json:"matches"`
	LastMatched time.Time `json:"lastMatched,omitzero"`
	// LastRun is the run whose findings it dropped last: a second routing
	// of that run counts nothing again (R2-11).
	LastRun string `json:"lastRun,omitempty"`
	// Updated is when the exception last changed (CF-55): accepted,
	// removed, made active again; a match is not a change. DuplicateOf names the exception
	// the daily housekeeping found it repeats, removing it (CF-53).
	Updated     time.Time `json:"updated,omitzero"`
	DuplicateOf string    `json:"duplicateOf,omitempty"`
}

// Stale says an active exception matched nothing for days: never since
// it was accepted that long ago, or not since that long (CF-13).
func (e Exception) Stale(now time.Time, days int) bool {
	if !e.Active(now) || days <= 0 {
		return false
	}
	since := e.LastMatched
	if since.IsZero() {
		since = e.At
	}
	return now.Sub(since) > time.Duration(days)*24*time.Hour
}

// Active says whether the exception applies at a time.
func (e Exception) Active(now time.Time) bool {
	return e.RemovedAt.IsZero() && (e.Expires.IsZero() || now.Before(e.Expires))
}

// State is "active", "expired" or "removed".
func (e Exception) State(now time.Time) string {
	switch {
	case !e.RemovedAt.IsZero():
		return "removed"
	case !e.Expires.IsZero() && !now.Before(e.Expires):
		return "expired"
	}
	return "active"
}

// Covers says whether the exception covers a finding: the same cluster,
// resource and check (as Key gives it).
func (e Exception) Covers(cluster, resource, check string) bool {
	return e.Cluster == cluster && strings.EqualFold(e.Resource, resource) && e.Check == check
}

// Wiki writes the records' pages: package knowledge's WriteRecord, which
// puts a page straight into the wiki's approved pages, a person having
// decided it already. It answers knowledge.ErrNoStore while no wiki is
// kept, which is no error here: the console's own records hold them.
type Wiki interface {
	WriteRecord(ctx context.Context, kind, name, title, body string) (path string, err error)
}

// NoWiki says whether an error of the Wiki means that no wiki is kept.
type NoWiki func(error) bool

// Limits are the keys' limits, as the ConfigMap's layout sets them.
type Limits struct {
	ExceptionsBytes int
}

// Options configure the service.
type Options struct {
	// Backend keeps the records: the casework ConfigMap; nil is memory.
	Backend kv.Backend
	Limits  Limits
	// Wiki writes the pages; nil writes none. NoWiki tells its answer
	// that no wiki is kept from a failure.
	Wiki   Wiki
	NoWiki NoWiki
	Audit  *audit.Log
	Now    func() time.Time
	Logger *slog.Logger
	// StaleDays is after how many days without a match an exception is
	// stale (Settings › Cases); nil never.
	StaleDays func() int
	// Clusters lists the console's clusters: the names the page shows and
	// the clusters a new exception may be on; nil takes any id, named by
	// itself. Seen gives the validation rules (check ids) seen on each
	// cluster, which the New exception form offers; nil offers none.
	Clusters func() []jobs.Cluster
	Seen     func(ctx context.Context) map[string][]string
}

// Service keeps the exceptions.
type Service struct {
	o  Options
	mu sync.Mutex
	// cache is the exceptions as last read, for Active, which every run
	// asks: read again when older than a few seconds.
	cache   []Exception
	cacheAt time.Time
}

// New builds the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Limits.ExceptionsBytes <= 0 {
		o.Limits.ExceptionsBytes = DefaultExceptionsBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Service{o: o}
}

// SetWiki gives the service the wiki, once the console has it.
func (s *Service) SetWiki(w Wiki, no NoWiki) {
	s.mu.Lock()
	s.o.Wiki, s.o.NoWiki = w, no
	s.mu.Unlock()
}

// Backend is the storage, for a backup.
func (s *Service) Backend() kv.Backend { return s.o.Backend }

// SetStaleDays says where the days an exception goes stale after come
// from.
func (s *Service) SetStaleDays(f func() int) { s.o.StaleDays = f }

// SetClusters says where the clusters and the validation rules seen on
// each come from (package casework), once the console has them.
func (s *Service) SetClusters(list func() []jobs.Cluster, seen func(ctx context.Context) map[string][]string) {
	s.mu.Lock()
	s.o.Clusters, s.o.Seen = list, seen
	s.mu.Unlock()
}

func (s *Service) record(e audit.Event) {
	if s.o.Audit != nil {
		s.o.Audit.Record(e)
	}
}

// ---------- the records ----------

type exceptionsRecord struct {
	Next  int         `json:"next"`
	Items []Exception `json:"items"`
}

func load[T any](ctx context.Context, b kv.Backend, key string, v *T) (string, error) {
	raw, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return version, nil
	}
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return "", fmt.Errorf("the record %s cannot be read: %w", key, err)
	}
	return version, nil
}

var errUnchanged = errors.New("unchanged")

// update changes a key over the version read, again when another replica
// wrote it first; fit gives the bytes to save, or an error when they do
// not fit.
func update[T any](ctx context.Context, b kv.Backend, key string, f func(*T) error, fit func(*T) ([]byte, error)) error {
	for try := 0; try < 10; try++ {
		var v T
		version, err := load(ctx, b, key, &v)
		if err != nil {
			return err
		}
		if err := f(&v); errors.Is(err, errUnchanged) {
			return nil
		} else if err != nil {
			return err
		}
		out, err := fit(&v)
		if err != nil {
			return err
		}
		if _, err = b.SaveKey(ctx, key, out, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("the record %s kept changing", key)
}

// fitExceptions keeps the exceptions under their limits: the oldest
// removed or expired ones go first; active ones are never let go, so a
// new one is refused once only active ones are left.
func (s *Service) fitExceptions(r *exceptionsRecord) ([]byte, error) {
	now := s.o.Now()
	drop := func() bool {
		for i := len(r.Items) - 1; i >= 0; i-- {
			if !r.Items[i].Active(now) {
				r.Items = append(r.Items[:i:i], r.Items[i+1:]...)
				return true
			}
		}
		return false
	}
	for len(r.Items) > maxExcepts && drop() {
	}
	for {
		b, _ := json.Marshal(r)
		if len(b) <= s.o.Limits.ExceptionsBytes && len(r.Items) <= maxExcepts {
			return b, nil
		}
		if !drop() {
			return nil, fmt.Errorf("%w: the accepted exceptions fill their record; remove some in AIOps › Mycroft › Exceptions first", ErrFull)
		}
	}
}

func (s *Service) loadExceptions(ctx context.Context) (exceptionsRecord, error) {
	var rec exceptionsRecord
	_, err := load(ctx, s.o.Backend, ExceptionsKey, &rec)
	return rec, err
}

func (s *Service) updateExceptions(ctx context.Context, f func(*exceptionsRecord) error) error {
	err := update(ctx, s.o.Backend, ExceptionsKey, f, s.fitExceptions)
	s.mu.Lock()
	s.cacheAt = time.Time{}
	s.mu.Unlock()
	return err
}

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

// ---------- the kind of check ----------

var (
	checkChars = regexp.MustCompile(`[^a-z0-9._:/-]+`)
	digits     = regexp.MustCompile(`[0-9]+`)
	nonWord    = regexp.MustCompile(`[^\p{L}#]+`)
)

// Key is the kind of check a finding is, for matching it with an
// exception: its check id ("rbac.cluster-admin-binding"), or, when the
// agent gave none, its title made plain: lower case, numbers as #, and
// only words, so "Volume 91% full" and "Volume 93% full" meet.
func Key(check, title string) string {
	c := strings.Trim(checkChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(check)), "-"), "-")
	if c != "" {
		if len(c) > 100 {
			c = c[:100]
		}
		return c
	}
	t := strings.ToLower(title)
	t = digits.ReplaceAllString(t, "#")
	t = strings.TrimSpace(nonWord.ReplaceAllString(t, " "))
	if utf8.RuneCountInString(t) > 120 {
		t = string([]rune(t)[:120])
	}
	return "title:" + t
}

// ---------- exceptions ----------

// NewException is what Ignore records: the finding, why, and until when.
type NewException struct {
	Cluster  string
	Resource string
	// Check is the finding's check id, or empty; Title its title, which
	// stands for the check when it has none.
	Check   string
	Title   string
	Detail  string
	Finding string
	Run     string
	Job     string
	Reason  string
	// Expires is when it stops applying; zero is never.
	Expires time.Time
	// Case is the case closed as it.
	Case string
	// Unique refuses it while an active exception covers the same
	// cluster, object and check (ErrConflict): New exception asks it.
	Unique bool
}

// AddException records an accepted exception and returns it (FX-03).
func (s *Service) AddException(ctx context.Context, in NewException, by Person) (Exception, error) {
	now := s.o.Now().UTC()
	reason := clip(in.Reason, maxReason)
	if reason == "" {
		return Exception{}, fmt.Errorf("%w: say why the finding is accepted", ErrInvalid)
	}
	if !in.Expires.IsZero() && !in.Expires.After(now) {
		return Exception{}, fmt.Errorf("%w: the expiry is in the past", ErrInvalid)
	}
	title := clip(in.Title, maxTitle)
	if title == "" {
		return Exception{}, fmt.Errorf("%w: the finding has no title", ErrInvalid)
	}
	e := Exception{Cluster: clip(in.Cluster, 100), Resource: clip(in.Resource, 300), Check: Key(in.Check, in.Title), Title: title, Detail: clip(in.Detail, maxDetail),
		Finding: clip(in.Finding, 60), Run: clip(in.Run, 60), Job: clip(in.Job, 100), Reason: reason, By: by.Label(), At: now, Updated: now, Case: clip(in.Case, 40)}
	if !in.Expires.IsZero() {
		e.Expires = in.Expires.UTC()
	}
	err := s.updateExceptions(ctx, func(rec *exceptionsRecord) error {
		if in.Unique {
			if o := covering(rec.Items, e, now); o != "" {
				return fmt.Errorf("%w: %s already covers this cluster, object and validation rule", ErrConflict, o)
			}
		}
		rec.Next++
		e.ID = fmt.Sprintf("EX-%d", rec.Next)
		rec.Items = append([]Exception{e}, rec.Items...)
		return nil
	})
	if err != nil {
		return Exception{}, err
	}
	until := "no expiry"
	if !e.Expires.IsZero() {
		until = "until " + e.Expires.Format("2006-01-02")
	}
	s.record(audit.Event{Action: ActionExceptionAdd, Who: by.who(), Cluster: e.Cluster, Target: e.ID, From: by.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("exception %s %s (%s) on %s, validation rule %s, %s: %s", e.ID, of(e), e.Title, orNone(e.Resource), e.Check, until, e.Reason)})
	// --- Mycroft tab (Phase 12, wave 5, MY-09) ---
	// An exception is the console's record only, listed in AIOps ›
	// Mycroft › Exceptions: no wiki page is written for it any more; the
	// agents are told the active ones in their task (FX-04).
	return e, nil
}

// of says how an exception was made: for its case, its finding, or by a
// person in Mycroft › Exceptions (New exception).
func of(e Exception) string {
	switch {
	case e.Case != "":
		return "accepted for case " + e.Case
	case e.Finding != "":
		return "accepted for finding " + e.Finding
	}
	return "created in Mycroft › Exceptions"
}

// covering names the active exception, other than e, that covers what e
// covers: the same cluster, object and check; "" for none.
func covering(items []Exception, e Exception, now time.Time) string {
	for _, o := range items {
		if o.ID != e.ID && o.Active(now) && o.Covers(e.Cluster, e.Resource, e.Check) {
			return o.ID
		}
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "no resource"
	}
	return s
}

// Exceptions lists the exceptions, newest first.
func (s *Service) Exceptions(ctx context.Context) ([]Exception, error) {
	rec, err := s.loadExceptions(ctx)
	if rec.Items == nil {
		rec.Items = []Exception{}
	}
	return rec.Items, err
}

// Exception is one exception.
func (s *Service) Exception(ctx context.Context, id string) (Exception, error) {
	rec, err := s.loadExceptions(ctx)
	if err != nil {
		return Exception{}, err
	}
	for _, e := range rec.Items {
		if e.ID == id {
			return e, nil
		}
	}
	return Exception{}, fmt.Errorf("%w: there is no exception %q", ErrNotFound, id)
}

// Active are the exceptions that apply on a cluster now: what a run
// leaves out, and what its agent is told. A failure to read them is
// logged and leaves none: a run then reports what it finds.
func (s *Service) Active(ctx context.Context, cluster string) []Exception {
	now := s.o.Now()
	cached, err := s.cached(ctx, now)
	if err != nil {
		s.o.Logger.Warn("the accepted exceptions cannot be read; runs report everything they find", "err", err)
		return nil
	}
	var out []Exception
	for _, e := range cached {
		if e.Cluster == cluster && e.Active(now) {
			out = append(out, e)
		}
	}
	return out
}

// ActiveAll are the exceptions that apply now on every cluster, for
// ask_knowledge (KN-32). A failure to read them leaves none.
func (s *Service) ActiveAll(ctx context.Context) []Exception {
	now := s.o.Now()
	cached, err := s.cached(ctx, now)
	if err != nil {
		return nil
	}
	var out []Exception
	for _, e := range cached {
		if e.Active(now) {
			out = append(out, e)
		}
	}
	return out
}

// cached are the exceptions as last read, read again when older than a
// few seconds.
func (s *Service) cached(ctx context.Context, now time.Time) ([]Exception, error) {
	s.mu.Lock()
	cached, at := s.cache, s.cacheAt
	s.mu.Unlock()
	if at.IsZero() || now.Sub(at) > 10*time.Second || now.Before(at) {
		rec, err := s.loadExceptions(ctx)
		if err != nil {
			return nil, err
		}
		cached = rec.Items
		s.mu.Lock()
		s.cache, s.cacheAt = cached, now
		s.mu.Unlock()
	}
	return cached, nil
}

// Objects are the objects the exceptions on a cluster name, active or
// not, once each: what the New exception form suggests first. They are
// read as Active reads them, at most every few seconds.
func (s *Service) Objects(ctx context.Context, cluster string) ([]string, error) {
	cached, err := s.cached(ctx, s.o.Now())
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range cached {
		if e.Cluster == cluster && e.Resource != "" && !seen[e.Resource] {
			seen[e.Resource] = true
			out = append(out, e.Resource)
		}
	}
	return out, nil
}

// Match counts what exceptions dropped (CF-13): each id once per finding
// it covered, at a time, for a run; an exception that counted the run
// already is left alone, so a run routed again counts nothing twice
// (R2-11).
func (s *Service) Match(ctx context.Context, run string, hits map[string]int, at time.Time) error {
	if len(hits) == 0 {
		return nil
	}
	return s.updateExceptions(ctx, func(rec *exceptionsRecord) error {
		n := 0
		for i := range rec.Items {
			if c := hits[rec.Items[i].ID]; c > 0 && (run == "" || rec.Items[i].LastRun != run) {
				rec.Items[i].Matches += c
				rec.Items[i].LastMatched, rec.Items[i].LastRun = at.UTC(), run
				n++
			}
		}
		if n == 0 {
			return errUnchanged
		}
		return nil
	})
}

// RemoveException removes an exception (FX-05): it stops applying, and
// stays listed as removed, with who removed it.
func (s *Service) RemoveException(ctx context.Context, id string, by Person) (Exception, error) {
	now := s.o.Now().UTC()
	var out Exception
	err := s.updateExceptions(ctx, func(rec *exceptionsRecord) error {
		for i := range rec.Items {
			e := &rec.Items[i]
			if e.ID != id {
				continue
			}
			if !e.RemovedAt.IsZero() {
				return fmt.Errorf("%w: %s was removed by %s already", ErrInvalid, id, e.RemovedBy)
			}
			e.RemovedBy, e.RemovedAt, e.Updated = by.Label(), now, now
			out = *e
			return nil
		}
		return fmt.Errorf("%w: there is no exception %q", ErrNotFound, id)
	})
	if err != nil {
		return Exception{}, err
	}
	s.record(audit.Event{Action: ActionExceptionRemove, Who: by.who(), Cluster: out.Cluster, Target: out.ID, From: by.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("exception %s removed (%s on %s, validation rule %s); findings like it are reported again", out.ID, out.Title, orNone(out.Resource), out.Check)})
	// One written to the wiki before exceptions stopped being pages there
	// (MY-09) keeps its page, which says it was removed.
	if out.Wiki != "" {
		_, _, _ = s.writePage(ctx, WikiExceptionKind, strings.ToLower(out.ID), "Exception "+out.ID+": "+out.Title, exceptionPage(out, now))
	}
	return out, nil
}

// ReactivateException makes an inactive (removed) exception active again
// (build 14): who removed it, when, and what it duplicated are cleared,
// and it applies from now. It is refused while another active exception
// covers the same cluster, object and check (ErrConflict), and for one
// whose last day has passed.
func (s *Service) ReactivateException(ctx context.Context, id string, by Person) (Exception, error) {
	now := s.o.Now().UTC()
	var out Exception
	var was string
	err := s.updateExceptions(ctx, func(rec *exceptionsRecord) error {
		for i := range rec.Items {
			e := &rec.Items[i]
			if e.ID != id {
				continue
			}
			if e.RemovedAt.IsZero() {
				return fmt.Errorf("%w: %s is not inactive", ErrInvalid, id)
			}
			if !e.Expires.IsZero() && !now.Before(e.Expires) {
				return fmt.Errorf("%w: the last day of %s has passed; create a new exception instead", ErrInvalid, id)
			}
			if o := covering(rec.Items, *e, now); o != "" {
				return fmt.Errorf("%w: %s already covers this cluster, object and validation rule", ErrConflict, o)
			}
			was = "turned off by " + e.RemovedBy
			if e.DuplicateOf != "" {
				was = "a duplicate of " + e.DuplicateOf
			}
			e.RemovedBy, e.RemovedAt, e.DuplicateOf, e.Updated = "", time.Time{}, "", now
			out = *e
			return nil
		}
		return fmt.Errorf("%w: there is no exception %q", ErrNotFound, id)
	})
	if err != nil {
		return Exception{}, err
	}
	s.record(audit.Event{Action: ActionExceptionReactivate, Who: by.who(), Cluster: out.Cluster, Target: out.ID, From: by.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("exception %s made active again (%s on %s, validation rule %s), was %s; findings like it are left out again", out.ID, out.Title, orNone(out.Resource), out.Check, was)})
	return out, nil
}

// ---------- the wiki ----------

// writePage writes a record's page when a wiki is kept: it returns the
// page's path or why it could not be written, and false when no wiki is
// kept (nothing to note).
func (s *Service) writePage(ctx context.Context, kind, name, title, body string) (string, string, bool) {
	s.mu.Lock()
	w, no := s.o.Wiki, s.o.NoWiki
	s.mu.Unlock()
	if w == nil {
		return "", "", false
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	path, err := w.WriteRecord(wctx, kind, name, title, body)
	if err != nil {
		if no != nil && no(err) {
			return "", "", false
		}
		s.o.Logger.Warn("a page of the wiki could not be written", "page", kind+"/"+name, "err", err)
		return "", clip(err.Error(), maxWikiError), true
	}
	return path, "", true
}

func exceptionPage(e Exception, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Exception %s: %s\n\n", e.ID, e.Title)
	switch e.State(now) {
	case "removed":
		fmt.Fprintf(&b, "**Removed** by %s on %s: findings like this one are reported again.\n\n", e.RemovedBy, e.RemovedAt.Format("2006-01-02 15:04 MST"))
	case "expired":
		fmt.Fprintf(&b, "**Expired** on %s: findings like this one are reported again.\n\n", e.Expires.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "An accepted exception: the agents' runs leave out a finding of this validation rule on this resource while it applies.\n\n")
	fmt.Fprintf(&b, "- Cluster: %s\n- Resource: %s\n- Validation rule: `%s`\n", orDash(e.Cluster), orDash(e.Resource), e.Check)
	fmt.Fprintf(&b, "- Accepted by %s on %s\n", e.By, e.At.Format("2006-01-02 15:04 MST"))
	if e.Expires.IsZero() {
		b.WriteString("- Expires: never\n")
	} else {
		fmt.Fprintf(&b, "- Expires: %s\n", e.Expires.Format("2006-01-02"))
	}
	if e.Finding != "" {
		fmt.Fprintf(&b, "- From finding %s of %s (%s)\n", e.Finding, e.Run, orDash(e.Job))
	}
	fmt.Fprintf(&b, "\n## Why it is accepted\n\n%s\n", e.Reason)
	if e.Detail != "" {
		fmt.Fprintf(&b, "\n## The finding\n\n%s\n", e.Detail)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// LastChange is when an exception last changed: Updated, or for one kept
// before it was recorded, when it was removed or accepted.
func (e Exception) LastChange() time.Time {
	switch {
	case !e.Updated.IsZero():
		return e.Updated
	case !e.RemovedAt.IsZero():
		return e.RemovedAt
	}
	return e.At
}

// --- Housekeeping (Phase 12, wave 6g, CF-53) ---

// Duplicate is an exception removed as a duplicate of another.
type Duplicate struct {
	ID string
	Of string
}

// RemoveDuplicates removes the active exceptions that cover what an
// older active one covers (the same cluster, object and check), naming it.
func (s *Service) RemoveDuplicates(ctx context.Context, by Person) ([]Duplicate, error) {
	now := s.o.Now().UTC()
	var out []Duplicate
	err := s.updateExceptions(ctx, func(rec *exceptionsRecord) error {
		out = nil
		items := rec.Items
		for i := range items {
			for j := range items {
				a, b := &items[i], &items[j]
				if i == j || !a.Active(now) || !b.Active(now) || !a.Covers(b.Cluster, b.Resource, b.Check) {
					continue
				}
				if a.At.After(b.At) || (a.At.Equal(b.At) && i > j) {
					continue
				}
				b.RemovedBy, b.RemovedAt, b.Updated, b.DuplicateOf = by.Label(), now, now, a.ID
				out = append(out, Duplicate{ID: b.ID, Of: a.ID})
			}
		}
		if len(out) == 0 {
			return errUnchanged
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range out {
		s.record(audit.Event{Action: ActionExceptionRemove, Who: by.who(), Target: d.ID, Outcome: audit.OutcomeOK,
			Detail: fmt.Sprintf("exception %s removed by the housekeeping: a duplicate of %s", d.ID, d.Of)})
	}
	return out, nil
}
