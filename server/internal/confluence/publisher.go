package confluence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/schedule"
	"github.com/ucesys/221b/server/internal/scrub"
)

// scrubText takes what looks like a credential out of text (X-01).
var scrubText = scrub.Text

// The publisher (ADR-97) keeps two page trees in Confluence: the known
// issues (a Status page, one page per known issue, and a resolved archive
// by month) and, when asked, Mycroft's approved wiki pages, read-only.
// The issue service and the wiki stay the sources of truth; the pages are
// views of them.
//
// Every change goes into an outbox, kept in the knowledge ConfigMap, that
// a worker on the leader writes out with retries and backoff, so the
// console carries on when Confluence is down and catches up after. Each
// page has a managed part the publisher rewrites and a Notes part people
// may edit freely; before writing, the page's version is checked, and a
// person's edit inside the managed part is not overwritten: it waits as
// a conflict for a person to keep (copied into the record) or replace. A
// nightly check compares every page with its record and repairs or
// reports what differs. Pages are never deleted: a resolved issue moves
// to the archive. A page someone writes by hand under the tree is
// imported as a draft known issue.
//
// What never reaches Confluence: reporters' names, report text,
// namespaces, hostnames, or anything an agent wrote that an operator has
// not approved (a draft known issue, an update an agent wrote).

// StateKey is the publisher's key in the knowledge ConfigMap.
const StateKey = "knowledge-publisher.json"

// ErrInvalid is a setting the publisher refuses.
var ErrInvalid = errors.New("not valid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Person is who decides a conflict.
type Person struct {
	ID, Name, Email string
}

// KnownIssues is the issue service as the publisher reads it.
type KnownIssues interface {
	// All is every known issue, drafts included.
	All(ctx context.Context) ([]issues.KnownIssue, error)
	Services(ctx context.Context) ([]issues.ServiceDef, error)
	Settings(ctx context.Context) (issues.Settings, error)
	// Import adds a draft known issue from a page someone wrote.
	Import(ctx context.Context, in issues.KnownInput) (issues.KnownIssue, error)
	// Note adds a dated update to a known issue, as a person.
	Note(ctx context.Context, id, text string, by Person) error
}

// WikiPage is an approved wiki page, as the publisher shows it.
type WikiPage struct {
	Path     string
	Title    string
	Markdown string
	Updated  time.Time
	Stale    bool
	// Sources are the lines naming its sources, with their dates.
	Sources []string
	// EditURL is where the page is edited: its git repository or bucket.
	EditURL string
}

// WikiPages are the approved wiki pages.
type WikiPages interface {
	WikiPages(ctx context.Context) ([]WikiPage, error)
}

// SecretStore keeps the write token: one key of the knowledge Secret.
type SecretStore interface {
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// tokenKey is the publisher's key in the Secret: its write token, apart
// from the read-only ones Mycroft's sources use.
const tokenKey = "confluence"

// Settings are where and how the publisher writes.
type Settings struct {
	On      bool   `json:"on"`
	BaseURL string `json:"baseURL"`
	Flavor  string `json:"flavor"`
	Email   string `json:"email,omitempty"`
	CAPEM   string `json:"caPEM,omitempty"`
	// Space is the known issues' space, and Root the title of the tree's
	// top page (default "Known issues").
	Space string `json:"space"`
	Root  string `json:"root,omitempty"`
	// Wiki publishes the approved wiki pages too, in WikiSpace under
	// WikiRoot (default "Infrastructure wiki").
	Wiki      bool   `json:"wiki,omitempty"`
	WikiSpace string `json:"wikiSpace,omitempty"`
	WikiRoot  string `json:"wikiRoot,omitempty"`
	// CheckAt is the nightly check's time, HH:MM UTC (default 03:30).
	CheckAt string    `json:"checkAt,omitempty"`
	SavedBy string    `json:"savedBy,omitempty"`
	SavedAt time.Time `json:"savedAt,omitzero"`
}

func (s Settings) root() string     { return orDefault(s.Root, "Known issues") }
func (s Settings) wikiRoot() string { return orDefault(s.WikiRoot, "Infrastructure wiki") }
func (s Settings) checkAt() string  { return orDefault(s.CheckAt, "03:30") }

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return strings.TrimSpace(s)
}

var spaceKey = regexp.MustCompile(`^~?[A-Za-z0-9_]{1,255}$`)

// Validate checks the settings as a SuperAdmin saves them.
func (s Settings) Validate() error {
	if strings.TrimSpace(s.CAPEM) != "" && (len(s.CAPEM) > 12<<10 || !strings.Contains(s.CAPEM, "-----BEGIN CERTIFICATE-----")) {
		return invalid("the private CA is a PEM certificate of at most 12 KiB")
	}
	if !s.On {
		return nil
	}
	if !strings.HasPrefix(s.BaseURL, "https://") || strings.Contains(s.BaseURL, "@") {
		return invalid("the Confluence address is an https address, such as https://example.atlassian.net")
	}
	if s.Flavor != Cloud && s.Flavor != DataCenter {
		return invalid("Confluence is cloud or datacenter")
	}
	if s.Flavor == Cloud && !strings.Contains(s.Email, "@") {
		return invalid("Confluence Cloud needs the email of the account the API token belongs to")
	}
	if !spaceKey.MatchString(s.Space) {
		return invalid("give the known issues' space key, such as STATUS")
	}
	if s.Wiki && !spaceKey.MatchString(s.WikiSpace) {
		return invalid("give the wiki's space key, such as INFRA")
	}
	if len(s.Root) > 200 || len(s.WikiRoot) > 200 {
		return invalid("a tree's top page title is at most 200 characters")
	}
	if _, err := schedule.Parse(schedule.Spec{Kind: schedule.Daily, At: s.checkAt()}); err != nil {
		return invalid("the nightly check's time is HH:MM")
	}
	return nil
}

// PageRef is a page the publisher keeps.
type PageRef struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version int    `json:"version"`
	// Hash is the managed part's text as last written.
	Hash   string    `json:"hash"`
	Parent string    `json:"parent,omitempty"`
	URL    string    `json:"url,omitempty"`
	Wrote  time.Time `json:"wrote,omitzero"`
}

// Job is one page waiting to be written.
type Job struct {
	Key   string    `json:"key"`
	Tries int       `json:"tries,omitempty"`
	Next  time.Time `json:"next,omitzero"`
	Error string    `json:"error,omitempty"`
	Added time.Time `json:"added"`
}

// Conflict is a person's edit inside a page's managed part, waiting for a
// decision.
type Conflict struct {
	// ID is the page's key: ki:<id> or wiki:<path>.
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Issue string `json:"issue,omitempty"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
	// Their is the managed part's text as the person left it.
	Their   string    `json:"their"`
	Hash    string    `json:"hash"`
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	// Owner is the known issue's owner, who is asked.
	Owner     string `json:"owner,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
}

// Saved is what a backup keeps: the settings and the pages, not the
// outbox or the token.
type Saved struct {
	Settings Settings           `json:"settings"`
	Pages    map[string]PageRef `json:"pages,omitempty"`
	Imported map[string]string  `json:"imported,omitempty"`
	Token    string             `json:"token,omitempty"`
}

type state struct {
	Settings  Settings           `json:"settings"`
	Pages     map[string]PageRef `json:"pages,omitempty"`
	Outbox    []Job              `json:"outbox,omitempty"`
	Conflicts []Conflict         `json:"conflicts,omitempty"`
	// Imported are the pages imported as draft known issues, by page id.
	Imported    map[string]string `json:"imported,omitempty"`
	LastWrite   time.Time         `json:"lastWrite,omitzero"`
	LastError   string            `json:"lastError,omitempty"`
	LastCheck   time.Time         `json:"lastCheck,omitzero"`
	CheckReport []string          `json:"checkReport,omitempty"`
}

func (st *state) init() {
	if st.Pages == nil {
		st.Pages = map[string]PageRef{}
	}
	if st.Imported == nil {
		st.Imported = map[string]string{}
	}
}

// PublisherOptions configure the publisher.
type PublisherOptions struct {
	// Backend keeps the state (the knowledge ConfigMap); Limit is its
	// key's most bytes.
	Backend kv.Backend
	Limit   int
	Secrets SecretStore
	Issues  KnownIssues
	// Hidden are names never published: the clusters' nodes, the hosts
	// and their addresses.
	Hidden func(ctx context.Context) []string
	// ConsoleURL is the console's address, for the links back.
	ConsoleURL string
	HTTP       *http.Client
	Now        func() time.Time
	Logger     *slog.Logger
	// Sleep is the client's wait between retries (tests).
	Sleep func(ctx context.Context, d time.Duration) error
	// Every is how often the outbox is looked at (default 30s).
	Every time.Duration
}

// Publisher writes the page trees.
type Publisher struct {
	o      PublisherOptions
	mu     sync.Mutex
	kick   chan struct{}
	memMu  sync.Mutex
	mem    []string
	wikiMu sync.Mutex
	wiki   WikiPages
}

// NewPublisher makes the publisher.
func NewPublisher(o PublisherOptions) *Publisher {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
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
	return &Publisher{o: o, kick: make(chan struct{}, 1)}
}

// SetWiki gives the publisher the wiki's approved pages.
func (p *Publisher) SetWiki(w WikiPages) {
	p.wikiMu.Lock()
	p.wiki = w
	p.wikiMu.Unlock()
}

func (p *Publisher) wikiPages(ctx context.Context) ([]WikiPage, error) {
	p.wikiMu.Lock()
	w := p.wiki
	p.wikiMu.Unlock()
	if w == nil {
		return nil, nil
	}
	return w.WikiPages(ctx)
}

// OnIssue is the issue service's change (issues.Service.OnChange): the
// known issue's page and the Status page go into the outbox. It does not
// block.
func (p *Publisher) OnIssue(e issues.Event) {
	if e.Known == nil || (e.Kind != issues.EventKnownChanged && e.Kind != issues.EventKnownUpdate) {
		return
	}
	p.enqueue("ki:"+e.Known.ID, "status")
}

// EnqueueWiki puts approved wiki pages into the outbox.
func (p *Publisher) EnqueueWiki(paths ...string) {
	keys := make([]string, len(paths))
	for i, x := range paths {
		keys[i] = "wiki:" + x
	}
	p.enqueue(keys...)
}

func (p *Publisher) enqueue(keys ...string) {
	p.memMu.Lock()
	for _, k := range keys {
		if !slices.Contains(p.mem, k) {
			p.mem = append(p.mem, k)
		}
	}
	p.memMu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *Publisher) takeMem() []string {
	p.memMu.Lock()
	defer p.memMu.Unlock()
	out := p.mem
	p.mem = nil
	return out
}

// maxOutbox bounds the outbox; maxConflicts the conflicts kept.
const (
	maxOutbox    = 2000
	maxConflicts = 200
)

func (p *Publisher) load(ctx context.Context) (state, string, error) {
	var st state
	raw, version, err := p.o.Backend.LoadKey(ctx, StateKey)
	if errors.Is(err, kv.ErrNotFound) {
		st.init()
		return st, version, nil
	}
	if err != nil {
		return st, "", err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			return st, "", fmt.Errorf("%s cannot be read: %w", StateKey, err)
		}
	}
	st.init()
	return st, version, nil
}

// edit changes the state where it was read, trying again when another
// writer came first.
func (p *Publisher) edit(ctx context.Context, fn func(*state) error) (state, error) {
	for try := 0; try < 10; try++ {
		st, version, err := p.load(ctx)
		if err != nil {
			return st, err
		}
		if err := fn(&st); err != nil {
			return st, err
		}
		if len(st.Outbox) > maxOutbox {
			st.Outbox = st.Outbox[len(st.Outbox)-maxOutbox:]
		}
		if len(st.Conflicts) > maxConflicts {
			st.Conflicts = st.Conflicts[len(st.Conflicts)-maxConflicts:]
		}
		raw, _ := json.Marshal(st)
		for p.o.Limit > 0 && len(raw) > p.o.Limit && len(st.CheckReport) > 0 {
			st.CheckReport = st.CheckReport[:len(st.CheckReport)/2]
			raw, _ = json.Marshal(st)
		}
		if p.o.Limit > 0 && len(raw) > p.o.Limit {
			return st, fmt.Errorf("%s is full (%d KiB at most)", StateKey, p.o.Limit>>10)
		}
		if _, err := p.o.Backend.SaveKey(ctx, StateKey, raw, version); errors.Is(err, kv.ErrConflict) {
			continue
		} else if err != nil {
			return st, err
		}
		return st, nil
	}
	return state{}, fmt.Errorf("%s: %w; try again", StateKey, kv.ErrConflict)
}

func (p *Publisher) token(ctx context.Context) (string, error) {
	if p.o.Secrets == nil {
		return "", nil
	}
	b, _, found, err := p.o.Secrets.LoadKey(ctx, tokenKey)
	if err != nil || !found || len(b) == 0 {
		return "", err
	}
	var c struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", err
	}
	return c.Token, nil
}

func (p *Publisher) saveToken(ctx context.Context, token string) error {
	if p.o.Secrets == nil {
		return errors.New("there is nowhere to keep the token")
	}
	for try := 0; ; try++ {
		_, version, _, err := p.o.Secrets.LoadKey(ctx, tokenKey)
		if err != nil {
			return err
		}
		raw := []byte{}
		if token != "" {
			raw, _ = json.Marshal(map[string]string{"token": token})
		}
		if err := p.o.Secrets.SaveKeyIf(ctx, tokenKey, raw, version); err == nil || try == 4 {
			return err
		}
	}
}

func (p *Publisher) client(set Settings, token string) (*Client, error) {
	if token == "" {
		return nil, invalid("no write token is saved for Confluence")
	}
	hc := p.o.HTTP
	if set.CAPEM != "" {
		hc = nil
	}
	return New(Config{BaseURL: set.BaseURL, Flavor: set.Flavor, Email: set.Email, Token: token, CAPEM: set.CAPEM, HTTP: hc, Sleep: p.o.Sleep})
}

// View is Settings › Issues › Confluence.
type View struct {
	Settings    Settings  `json:"settings"`
	Token       bool      `json:"token"`
	Outbox      int       `json:"outbox"`
	Failing     int       `json:"failing"`
	LastError   string    `json:"lastError,omitempty"`
	LastWrite   time.Time `json:"lastWrite,omitzero"`
	LastCheck   time.Time `json:"lastCheck,omitzero"`
	NextCheck   time.Time `json:"nextCheck,omitzero"`
	CheckReport []string  `json:"checkReport,omitempty"`
	Conflicts   int       `json:"conflicts"`
	Pages       int       `json:"pages"`
	WikiPages   int       `json:"wikiPages"`
}

// View reads the publisher's state for its settings.
func (p *Publisher) View(ctx context.Context) (View, error) {
	st, _, err := p.load(ctx)
	if err != nil {
		return View{}, err
	}
	tok, err := p.token(ctx)
	if err != nil {
		return View{}, err
	}
	v := View{Settings: st.Settings, Token: tok != "", Outbox: len(st.Outbox), LastError: st.LastError, LastWrite: st.LastWrite, LastCheck: st.LastCheck, CheckReport: st.CheckReport, Conflicts: len(st.Conflicts)}
	for _, j := range st.Outbox {
		if j.Tries > 0 {
			v.Failing++
		}
	}
	for k := range st.Pages {
		if strings.HasPrefix(k, "wiki:") {
			v.WikiPages++
		} else if strings.HasPrefix(k, "ki:") {
			v.Pages++
		}
	}
	if st.Settings.On {
		v.NextCheck = p.nextCheck(st)
	}
	return v, nil
}

func (p *Publisher) nextCheck(st state) time.Time {
	sc, err := schedule.Parse(schedule.Spec{Kind: schedule.Daily, At: st.Settings.checkAt()})
	if err != nil {
		return time.Time{}
	}
	last := st.LastCheck
	if last.IsZero() {
		last = p.o.Now().Add(-time.Minute)
	}
	return sc.Next(last)
}

// Save saves the settings, and the write token when given ("" removes
// it). A changed space starts its tree afresh.
func (p *Publisher) Save(ctx context.Context, set Settings, token *string, by string) (string, error) {
	set.BaseURL = strings.TrimRight(strings.TrimSpace(set.BaseURL), "/")
	set.Space, set.WikiSpace = strings.TrimSpace(set.Space), strings.TrimSpace(set.WikiSpace)
	if err := set.Validate(); err != nil {
		return "", err
	}
	if token != nil && *token != "" && len(*token) > 4096 {
		return "", invalid("the token is at most 4,096 characters")
	}
	set.SavedBy, set.SavedAt = by, p.o.Now().UTC()
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.edit(ctx, func(st *state) error {
		old := st.Settings
		if old.Space != set.Space || old.BaseURL != set.BaseURL || old.root() != set.root() {
			for k := range st.Pages {
				if !strings.HasPrefix(k, "wiki") {
					delete(st.Pages, k)
				}
			}
		}
		if old.WikiSpace != set.WikiSpace || old.BaseURL != set.BaseURL || old.wikiRoot() != set.wikiRoot() {
			for k := range st.Pages {
				if strings.HasPrefix(k, "wiki") {
					delete(st.Pages, k)
				}
			}
		}
		st.Settings = set
		// The next check writes every page as the new settings have it.
		st.LastCheck = time.Time{}
		return nil
	})
	if err != nil {
		return "", err
	}
	detail := fmt.Sprintf("publishing to Confluence %s: %s, space %s", map[bool]string{true: "on", false: "off"}[set.On], set.BaseURL, set.Space)
	if set.Wiki {
		detail += ", the wiki in space " + set.WikiSpace
	}
	if token != nil {
		if err := p.saveToken(ctx, strings.TrimSpace(*token)); err != nil {
			return detail, err
		}
		detail += map[bool]string{true: "; write token removed", false: "; write token replaced"}[*token == ""]
	}
	p.enqueue()
	return detail, nil
}

// Test reaches the spaces with settings as typed, and names them.
func (p *Publisher) Test(ctx context.Context, set Settings, token *string) ([]string, error) {
	set.On = true
	if err := set.Validate(); err != nil {
		return nil, err
	}
	tok := ""
	if token != nil {
		tok = *token
	} else {
		var err error
		if tok, err = p.token(ctx); err != nil {
			return nil, err
		}
	}
	cl, err := p.client(set, tok)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, key := range []string{set.Space, set.WikiSpace} {
		if key == "" || (key == set.WikiSpace && !set.Wiki) {
			continue
		}
		sp, err := cl.Space(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("space %s: %w", key, err)
		}
		out = append(out, sp.Key+": "+sp.Name)
	}
	return out, nil
}

// Conflicts are the edits waiting for a decision.
func (p *Publisher) Conflicts(ctx context.Context) ([]Conflict, error) {
	st, _, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	return st.Conflicts, nil
}

// Resolve decides a conflict: "replace" rewrites the page from its
// record; "keep" (a known issue's page only) first copies the person's
// text into the known issue's update log, then rewrites the page from it.
func (p *Publisher) Resolve(ctx context.Context, id, action string, by Person) (string, error) {
	if action != "keep" && action != "replace" {
		return "", invalid("a conflict is kept or replaced")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st, _, err := p.load(ctx)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(st.Conflicts, func(c Conflict) bool { return c.ID == id })
	if i < 0 {
		return "", fmt.Errorf("%w: no conflict %s", ErrInvalid, id)
	}
	c := st.Conflicts[i]
	if action == "keep" {
		if c.Kind != "known" || p.o.Issues == nil {
			return "", invalid("a wiki page is edited where the wiki is kept; replace the page in Confluence instead")
		}
		text := "Edited in Confluence, kept by " + by.Name + ": " + clip(c.Their, 3500)
		if err := p.o.Issues.Note(ctx, c.Issue, text, by); err != nil {
			return "", err
		}
	}
	_, err = p.edit(ctx, func(st *state) error {
		st.Conflicts = slices.DeleteFunc(st.Conflicts, func(x Conflict) bool { return x.ID == id })
		if ref, ok := st.Pages[id]; ok {
			ref.Hash, ref.Version = c.Hash, c.Version
			st.Pages[id] = ref
		}
		addJob(st, id, p.o.Now())
		return nil
	})
	if err != nil {
		return "", err
	}
	p.enqueue()
	if action == "keep" {
		return fmt.Sprintf("the Confluence edit of %s was kept in %s's update log, and the page rewritten from it", c.Title, c.Issue), nil
	}
	return fmt.Sprintf("the Confluence edit of %s was replaced by the record", c.Title), nil
}

func addJob(st *state, key string, now time.Time) {
	for i := range st.Outbox {
		if st.Outbox[i].Key == key {
			st.Outbox[i].Next = time.Time{}
			return
		}
	}
	st.Outbox = append(st.Outbox, Job{Key: key, Added: now.UTC()})
}

// Export is the backup's share, the token as seal makes it: sealed with
// the install's backup key (ADR-137), or a placeholder.
func (p *Publisher) Export(ctx context.Context, seal func(string) string) (*Saved, error) {
	st, _, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	if !st.Settings.On && len(st.Pages) == 0 && st.Settings.BaseURL == "" {
		return nil, nil
	}
	tok, err := p.token(ctx)
	if err != nil {
		return nil, err
	}
	out := &Saved{Settings: st.Settings, Pages: st.Pages, Imported: st.Imported}
	out.Token = seal(tok)
	return out, nil
}

// Import restores the settings and the pages; a token here stays, and
// one this console lacks is taken from the file when open opens it (the
// backup's, ADR-137), or listed to give again.
func (p *Publisher) Import(ctx context.Context, in Saved, open func(inFile, name, where string) (string, error)) ([]string, error) {
	if in.Token != "" && in.Token != "<secret: not in the backup>" && !(strings.HasPrefix(in.Token, "<sealed:") && strings.HasSuffix(in.Token, ">")) {
		return nil, errors.New("the file holds a token in clear where a backup holds a sealed one or a placeholder")
	}
	if err := in.Settings.Validate(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.edit(ctx, func(st *state) error {
		st.Settings, st.Pages, st.Imported = in.Settings, in.Pages, in.Imported
		st.init()
		st.LastCheck = time.Time{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var notes []string
	if tok, _ := p.token(ctx); tok == "" && in.Token != "" {
		v, err := open(in.Token, "the Confluence write token", "Settings › Issues › Confluence")
		if err == nil && v != "" {
			if err := p.saveToken(ctx, v); err != nil {
				notes = append(notes, "Confluence: the write token was not saved: "+err.Error())
			}
		}
	}
	return notes, nil
}

// Run writes the outbox on the leader, every few seconds and whenever
// something is added, and runs the nightly check. It returns when ctx
// ends.
func (p *Publisher) Run(ctx context.Context, leads func() bool) {
	t := time.NewTicker(p.o.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.kick:
		}
		if leads != nil && !leads() {
			continue
		}
		if due, err := p.checkDue(ctx); err == nil && due {
			if _, err := p.Check(ctx); err != nil {
				p.o.Logger.Warn("the Confluence pages' nightly check failed", "err", err)
			}
		}
		if _, err := p.Flush(ctx); err != nil && ctx.Err() == nil {
			p.o.Logger.Warn("writing to Confluence failed; it is tried again", "err", err)
		}
	}
}

// checkDue claims the nightly check when it is due, so one replica runs
// it.
func (p *Publisher) checkDue(ctx context.Context) (bool, error) {
	st, _, err := p.load(ctx)
	if err != nil || !st.Settings.On {
		return false, err
	}
	now := p.o.Now()
	if !st.LastCheck.IsZero() && now.Before(p.nextCheck(st)) {
		return false, nil
	}
	claimed := false
	_, err = p.edit(ctx, func(s *state) error {
		if !s.LastCheck.IsZero() && now.Before(p.nextCheck(*s)) {
			return nil
		}
		s.LastCheck, claimed = now.UTC(), true
		return nil
	})
	return claimed, err
}

// backoff is how long a failing job waits before its next try.
func backoff(tries int) time.Duration {
	d := time.Minute << min(tries, 6)
	return min(d, time.Hour)
}

// Flush writes the outbox's due pages and returns how many it wrote.
func (p *Publisher) Flush(ctx context.Context) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	mem := p.takeMem()
	st, _, err := p.load(ctx)
	if err != nil {
		p.enqueue(mem...)
		return 0, err
	}
	now := p.o.Now()
	if len(mem) > 0 {
		if st, err = p.edit(ctx, func(s *state) error {
			for _, k := range mem {
				addJob(s, k, now)
			}
			return nil
		}); err != nil {
			p.enqueue(mem...)
			return 0, err
		}
	}
	if !st.Settings.On || len(st.Outbox) == 0 {
		return 0, nil
	}
	tok, err := p.token(ctx)
	if err != nil {
		return 0, err
	}
	cl, err := p.client(st.Settings, tok)
	if err != nil {
		_, _ = p.edit(ctx, func(s *state) error { s.LastError = err.Error(); return nil })
		return 0, err
	}
	w := &writer{p: p, cl: cl, set: st.Settings, pages: map[string]PageRef{}, known: st.Pages, now: now}
	done := map[string]bool{}
	failed := map[string]string{}
	jobs := append([]Job(nil), st.Outbox...)
	// The known issues' pages before the Status page, which links them.
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Key != "status" && jobs[j].Key == "status" })
	written := 0
	for _, j := range jobs {
		if len(done)+len(failed) >= 50 || ctx.Err() != nil {
			break
		}
		if !j.Next.IsZero() && now.Before(j.Next) {
			continue
		}
		if err := w.job(ctx, j.Key); err != nil {
			if errors.Is(err, errWaits) {
				done[j.Key] = true
				continue
			}
			failed[j.Key] = clip(err.Error(), 300)
			continue
		}
		done[j.Key] = true
		written++
	}
	_, err = p.edit(ctx, func(s *state) error {
		for k, ref := range w.pages {
			s.Pages[k] = ref
		}
		for _, c := range w.conflicts {
			s.Conflicts = slices.DeleteFunc(s.Conflicts, func(x Conflict) bool { return x.ID == c.ID })
			s.Conflicts = append(s.Conflicts, c)
		}
		for k, v := range w.imported {
			s.Imported[k] = v
		}
		kept := s.Outbox[:0]
		for _, j := range s.Outbox {
			if done[j.Key] {
				continue
			}
			if msg, ok := failed[j.Key]; ok {
				j.Tries++
				j.Error = msg
				j.Next = now.Add(backoff(j.Tries)).UTC()
				s.LastError = j.Key + ": " + msg
			}
			kept = append(kept, j)
		}
		s.Outbox = kept
		if written > 0 {
			s.LastWrite = now.UTC()
		}
		if len(failed) == 0 && written > 0 {
			s.LastError = ""
		}
		return nil
	})
	return written, err
}

// errWaits is a page that waits for a person: it leaves the outbox.
var errWaits = errors.New("waits for a person")

// writer writes pages for one flush or check.
type writer struct {
	p   *Publisher
	cl  *Client
	set Settings
	// known are the pages as the state had them; pages those written now.
	known     map[string]PageRef
	pages     map[string]PageRef
	conflicts []Conflict
	imported  map[string]string
	now       time.Time
	spaces    map[string]Space
	// data read once per writer.
	all      []issues.KnownIssue
	services []issues.ServiceDef
	iset     *issues.Settings
	hidden   []string
	wiki     []WikiPage
	wikiRead bool
}

func (w *writer) ref(key string) (PageRef, bool) {
	if r, ok := w.pages[key]; ok {
		return r, true
	}
	r, ok := w.known[key]
	return r, ok
}

func (w *writer) space(ctx context.Context, key string) (Space, error) {
	if sp, ok := w.spaces[key]; ok {
		return sp, nil
	}
	sp, err := w.cl.Space(ctx, key)
	if err != nil {
		return Space{}, fmt.Errorf("space %s: %w", key, err)
	}
	if w.spaces == nil {
		w.spaces = map[string]Space{}
	}
	w.spaces[key] = sp
	return sp, nil
}

func (w *writer) issues(ctx context.Context) error {
	if w.iset != nil {
		return nil
	}
	if w.p.o.Issues == nil {
		return errors.New("the issue service is off")
	}
	var err error
	if w.all, err = w.p.o.Issues.All(ctx); err != nil {
		return err
	}
	if w.services, err = w.p.o.Issues.Services(ctx); err != nil {
		return err
	}
	set, err := w.p.o.Issues.Settings(ctx)
	if err != nil {
		return err
	}
	w.iset = &set
	if w.p.o.Hidden != nil {
		w.hidden = w.p.o.Hidden(ctx)
	}
	for _, s := range w.services {
		for _, r := range s.RunsOn {
			switch r.Kind {
			case "namespace", "workload", "host", "hostname", "cluster":
				w.hidden = append(w.hidden, r.Value)
			}
		}
	}
	return nil
}

func (w *writer) knownIssue(id string) *issues.KnownIssue {
	for i := range w.all {
		if w.all[i].ID == id {
			return &w.all[i]
		}
	}
	return nil
}

// job writes one page of the outbox.
func (w *writer) job(ctx context.Context, key string) error {
	switch {
	case key == "status":
		return w.status(ctx)
	case strings.HasPrefix(key, "ki:"):
		return w.knownPage(ctx, strings.TrimPrefix(key, "ki:"))
	case strings.HasPrefix(key, "wiki:"):
		return w.wikiPage(ctx, strings.TrimPrefix(key, "wiki:"))
	}
	return nil
}

// hashText is a managed part's text, as compared.
func hashText(storage string) string {
	t := strings.Join(strings.Fields(Text(storage)), " ")
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:16])
}

var notesHeading = regexp.MustCompile(`(?i)<h2[^>]*>\s*Notes\s*</h2>`)

// defaultNotes is a new page's Notes part.
const defaultNotes = "<h2>Notes</h2><p></p>"

// split cuts a page's body into its managed part and its Notes part.
func split(body string) (managed, notes string) {
	loc := notesHeading.FindStringIndex(body)
	if loc == nil {
		return body, defaultNotes
	}
	return body[:loc[0]], body[loc[0]:]
}

// write puts a page's managed part in place: a new page under parent, or
// the page as it is, if no person changed its managed part since the last
// write. A person's change is a conflict, and the page waits.
func (w *writer) write(ctx context.Context, spaceKey, key, title, parent, managed string, conflict func(page Page, their string) Conflict) error {
	sp, err := w.space(ctx, spaceKey)
	if err != nil {
		return err
	}
	if parent == "" {
		parent = sp.HomepageID
	}
	want := hashText(managed)
	ref, ok := w.ref(key)
	var page Page
	if ok && ref.ID != "" {
		page, err = w.cl.Page(ctx, ref.ID)
		if NotFound(err) {
			ok = false
		} else if err != nil {
			return err
		}
	}
	if !ok {
		// A page of that title may be there already, from before the
		// state was lost: it is taken over, never written twice.
		if found, err := w.findChild(ctx, parent, title); err == nil && found != nil {
			page, ok = *found, true
			ref = PageRef{ID: found.ID, Title: title, Version: found.Version, Hash: hashText(func() string { m, _ := split(found.Body); return m }())}
		}
	}
	if !ok {
		pg, err := w.cl.Create(ctx, sp, parent, title, managed+defaultNotes)
		if err != nil {
			return err
		}
		w.pages[key] = PageRef{ID: pg.ID, Title: title, Version: pg.Version, Hash: want, Parent: parent, URL: pg.URL, Wrote: w.now.UTC()}
		return nil
	}
	cur, notes := split(page.Body)
	have := hashText(cur)
	switch {
	case have == want && page.Title == title && page.ParentID == parent:
		ref.Version, ref.Hash, ref.Parent, ref.URL = page.Version, want, parent, page.URL
		w.pages[key] = ref
		return nil
	case have != ref.Hash && page.Version != ref.Version:
		c := conflict(page, Text(cur))
		c.Hash, c.Version, c.At, c.URL = have, page.Version, w.now.UTC(), page.URL
		w.conflicts = append(w.conflicts, c)
		return errWaits
	}
	pg, err := w.cl.Update(ctx, sp, page.ID, page.Version, parent, title, managed+notes, "Kept by 221B")
	if err != nil {
		return err
	}
	w.pages[key] = PageRef{ID: pg.ID, Title: title, Version: pg.Version, Hash: want, Parent: parent, URL: firstOf(pg.URL, page.URL), Wrote: w.now.UTC()}
	return nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (w *writer) findChild(ctx context.Context, parent, title string) (*Page, error) {
	if parent == "" {
		return nil, nil
	}
	kids, err := w.cl.Children(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		if k.Title == title {
			pg, err := w.cl.Page(ctx, k.ID)
			if err != nil {
				return nil, err
			}
			return &pg, nil
		}
	}
	return nil, nil
}

// tree makes sure a tree's page exists (its top, the archive, a month)
// and returns its id. Its managed part says what it is.
func (w *writer) tree(ctx context.Context, spaceKey, key, title, parent, about string) (string, error) {
	if ref, ok := w.ref(key); ok && ref.ID != "" {
		if _, err := w.cl.Page(ctx, ref.ID); err == nil {
			return ref.ID, nil
		} else if !NotFound(err) {
			return "", err
		}
	}
	managed := w.banner("") + "<p>" + Escape(about) + "</p>"
	err := w.write(ctx, spaceKey, key, title, parent, managed, func(Page, string) Conflict { return Conflict{ID: key, Kind: "tree", Title: title} })
	if err != nil && !errors.Is(err, errWaits) {
		return "", err
	}
	ref, _ := w.ref(key)
	return ref.ID, nil
}

func (w *writer) root(ctx context.Context) (string, error) {
	return w.tree(ctx, w.set.Space, "root", w.set.root(), "", "Known issues, kept current by the issue service: the Status page, one page per known issue, and the resolved archive.")
}

// banner is the line every managed part starts with.
func (w *writer) banner(edit string) string {
	company := "the"
	if w.iset != nil && w.iset.Company != "" {
		company = Escape(w.iset.Company) + "’s"
	}
	text := "This page is kept by " + company + " issue service in 221B: everything above Notes is rewritten whenever the record changes."
	if edit != "" {
		text += " To change it, edit the record there: " + edit + "."
	}
	text += " Write anything else under Notes."
	return `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>` + text + `</p></ac:rich-text-body></ac:structured-macro>`
}

var ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// public is text as it may be published: no credential, no internal
// address, none of the names kept off Confluence.
func (w *writer) public(s string) string {
	s = scrubText(s)
	s = ipv4.ReplaceAllString(s, "[internal]")
	for _, h := range w.hidden {
		if len(h) < 3 {
			continue
		}
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(h) + `\b`)
		if err == nil {
			s = re.ReplaceAllString(s, "[internal]")
		}
	}
	return s
}

var statusWords = map[issues.KnownStatus]string{issues.KnownInvestigating: "Investigating", issues.KnownIdentified: "Identified", issues.KnownFixInProgress: "Fix in progress", issues.KnownResolved: "Resolved"}

func (w *writer) consoleLink(path, label string) string {
	if w.p.o.ConsoleURL == "" {
		return Escape(label) + " in 221B"
	}
	return `<a href="` + Escape(strings.TrimRight(w.p.o.ConsoleURL, "/")+path) + `">` + Escape(label) + `</a>`
}

func kiTitle(k issues.KnownIssue, w *writer) string {
	return clip(k.ID+": "+w.public(k.Title), 250)
}

// knownManaged is a known issue's page.
func (w *writer) knownManaged(k issues.KnownIssue) string {
	var b strings.Builder
	b.WriteString(w.banner(w.consoleLink("/#/aiops/mycroft/known/"+k.ID, k.ID)))
	b.WriteString("<h2>Status</h2><p>" + statusWords[k.Status])
	if k.Kind == issues.KindPlanned && k.Window != nil {
		b.WriteString(" · planned maintenance, " + k.Window.Start.UTC().Format("2006-01-02 15:04") + " to " + k.Window.End.UTC().Format("2006-01-02 15:04") + " UTC")
	}
	b.WriteString("</p>")
	if len(k.Symptoms) > 0 {
		b.WriteString("<h2>What you may see</h2><ul>")
		for _, s := range k.Symptoms {
			b.WriteString("<li>" + Escape(w.public(s)) + "</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString("<h2>Affected services</h2>")
	names := w.serviceNames(k.Services)
	if len(names) == 0 {
		b.WriteString("<p>Not tied to a service yet.</p>")
	} else {
		b.WriteString("<ul>")
		for _, n := range names {
			b.WriteString("<li>" + Escape(n) + "</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString("<h2>Workaround</h2><p>" + Escape(orDefault(w.public(k.Workaround), "None known yet.")) + "</p>")
	b.WriteString("<h2>Expected</h2><p>" + Escape(orDefault(w.public(k.Expected), "Not known yet.")) + "</p>")
	owner := "Not assigned yet."
	if k.Owner != nil {
		owner = orDefault(k.Owner.Name, k.Owner.ID)
	}
	b.WriteString("<h2>Owner</h2><p>" + Escape(owner) + "</p>")
	report := ""
	if w.iset != nil {
		report = w.iset.ReportURL
	}
	if report == "" && w.p.o.ConsoleURL != "" {
		report = strings.TrimRight(w.p.o.ConsoleURL, "/") + "/report"
	}
	b.WriteString("<h2>How to report more</h2><p>")
	if report != "" {
		b.WriteString(`If you see this too, report it on the <a href="` + Escape(report) + `">report page</a>, saying what you see; it is linked to this issue.`)
	} else {
		b.WriteString("If you see this too, report it on 221B’s report page, saying what you see.")
	}
	b.WriteString("</p><h2>Updates</h2>")
	var lines []string
	for i := len(k.Updates) - 1; i >= 0; i-- {
		u := k.Updates[i]
		text := ""
		switch {
		case !u.By.System:
			text = w.public(u.Text)
		case u.Status != "":
			// An update a system or an agent wrote: only its status.
			text = "Status: " + strings.ToLower(statusWords[u.Status]) + "."
		default:
			continue
		}
		lines = append(lines, "<li>"+u.At.UTC().Format("2006-01-02 15:04")+" UTC: "+Escape(text)+"</li>")
	}
	if len(lines) == 0 {
		b.WriteString("<p>None yet.</p>")
	} else {
		b.WriteString("<ul>" + strings.Join(lines, "") + "</ul>")
	}
	return b.String()
}

func (w *writer) serviceNames(ids []string) []string {
	var out []string
	for _, id := range ids {
		for _, s := range w.services {
			if s.ID == id {
				out = append(out, w.public(s.Name))
			}
		}
	}
	return out
}

// archiveAfter is how long a resolved issue stays beside the open ones.
const archiveAfter = 30 * 24 * time.Hour

func (w *writer) knownPage(ctx context.Context, id string) error {
	if err := w.issues(ctx); err != nil {
		return err
	}
	root, err := w.root(ctx)
	if err != nil {
		return err
	}
	k := w.knownIssue(id)
	key := "ki:" + id
	if k == nil {
		// Removed from the issue service: the page stays, archived, and
		// says so.
		ref, ok := w.ref(key)
		if !ok {
			return nil
		}
		parent, err := w.archive(ctx, w.now)
		if err != nil {
			return err
		}
		managed := w.banner("") + "<p>This known issue was removed from the issue service on " + w.now.UTC().Format("2006-01-02") + ".</p>"
		return w.write(ctx, w.set.Space, key, ref.Title, parent, managed, w.knownConflict(id, ref.Title, nil))
	}
	if k.Draft {
		// Not approved by an operator: never published.
		return nil
	}
	parent := root
	if k.Status == issues.KnownResolved && k.Resolved != nil && w.now.Sub(*k.Resolved) > archiveAfter {
		if parent, err = w.archive(ctx, *k.Resolved); err != nil {
			return err
		}
	}
	title := kiTitle(*k, w)
	return w.write(ctx, w.set.Space, key, title, parent, w.knownManaged(*k), w.knownConflict(id, title, k.Owner))
}

func (w *writer) knownConflict(id, title string, owner *issues.Person) func(Page, string) Conflict {
	return func(_ Page, their string) Conflict {
		c := Conflict{ID: "ki:" + id, Kind: "known", Issue: id, Title: title, Their: clip(their, 4000)}
		if owner != nil {
			c.Owner, c.OwnerName = owner.ID, owner.Name
		}
		return c
	}
}

// archive is the month's page of the resolved archive.
func (w *writer) archive(ctx context.Context, month time.Time) (string, error) {
	root, err := w.root(ctx)
	if err != nil {
		return "", err
	}
	arch, err := w.tree(ctx, w.set.Space, "archive", w.set.root()+": resolved archive", root, "Resolved known issues, filed by the month they were resolved in, 30 days after.")
	if err != nil {
		return "", err
	}
	m := month.UTC().Format("2006-01")
	return w.tree(ctx, w.set.Space, "archive:"+m, w.set.root()+": resolved "+m, arch, "Known issues resolved in "+m+".")
}

func (w *writer) link(title string) string {
	return `<ac:link><ri:page ri:content-title="` + Escape(title) + `"/><ac:plain-text-link-body><![CDATA[` + strings.ReplaceAll(title, "]]>", "]] >") + `]]></ac:plain-text-link-body></ac:link>`
}

// status is the Status page.
func (w *writer) status(ctx context.Context) error {
	if err := w.issues(ctx); err != nil {
		return err
	}
	root, err := w.root(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(w.banner(w.consoleLink("/#/aiops/mycroft/known", "Known issues")))
	b.WriteString("<p>Updated " + w.now.UTC().Format("2006-01-02 15:04") + " UTC.</p><h2>Open</h2>")
	groups := map[string][]issues.KnownIssue{}
	var planned, resolved []issues.KnownIssue
	for _, k := range w.all {
		switch {
		case k.Draft:
		case k.Inactive:
			// A duplicate the housekeeping made inactive: the one it
			// repeats is listed (CF-63).
		case k.Status == issues.KnownResolved:
			if k.Resolved != nil && w.now.Sub(*k.Resolved) <= 7*24*time.Hour {
				resolved = append(resolved, k)
			}
		case k.Kind == issues.KindPlanned:
			if k.Window == nil || w.now.Before(k.Window.End) {
				planned = append(planned, k)
			}
		default:
			names := w.serviceNames(k.Services)
			if len(names) == 0 {
				names = []string{"Not tied to a service"}
			}
			for _, n := range names {
				groups[n] = append(groups[n], k)
			}
		}
	}
	if len(groups) == 0 {
		b.WriteString("<p>No open known issues.</p>")
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString("<h3>" + Escape(n) + "</h3><table><tbody><tr><th>Issue</th><th>Status</th><th>Workaround</th><th>Expected</th></tr>")
		for _, k := range groups[n] {
			b.WriteString("<tr><td>" + w.link(kiTitle(k, w)) + "</td><td>" + statusWords[k.Status] + "</td><td>" + Escape(orDefault(w.public(k.Workaround), "None known yet")) + "</td><td>" + Escape(orDefault(w.public(k.Expected), "Not known yet")) + "</td></tr>")
		}
		b.WriteString("</tbody></table>")
	}
	if len(planned) > 0 {
		b.WriteString("<h2>Planned maintenance</h2><ul>")
		for _, k := range planned {
			when := ""
			if k.Window != nil {
				when = ": " + k.Window.Start.UTC().Format("2006-01-02 15:04") + " to " + k.Window.End.UTC().Format("2006-01-02 15:04") + " UTC"
			}
			b.WriteString("<li>" + w.link(kiTitle(k, w)) + Escape(when) + "</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString("<h2>Resolved in the last 7 days</h2>")
	if len(resolved) == 0 {
		b.WriteString("<p>None.</p>")
	} else {
		b.WriteString("<ul>")
		for _, k := range resolved {
			b.WriteString("<li>" + w.link(kiTitle(k, w)) + ", resolved " + k.Resolved.UTC().Format("2006-01-02") + "</li>")
		}
		b.WriteString("</ul>")
	}
	title := w.set.root() + ": status"
	return w.write(ctx, w.set.Space, "status", title, root, b.String(), func(Page, string) Conflict { return Conflict{ID: "status", Kind: "status", Title: title} })
}

func (w *writer) wikiPages(ctx context.Context) ([]WikiPage, error) {
	if w.wikiRead {
		return w.wiki, nil
	}
	list, err := w.p.wikiPages(ctx)
	if err != nil {
		return nil, err
	}
	w.wiki, w.wikiRead = list, true
	return list, nil
}

// wikiPage is an approved wiki page, read-only, with a link back to where
// it is edited.
func (w *writer) wikiPage(ctx context.Context, path string) error {
	if !w.set.Wiki {
		return nil
	}
	pages, err := w.wikiPages(ctx)
	if err != nil {
		return err
	}
	root, err := w.tree(ctx, w.set.WikiSpace, "wikiroot", w.set.wikiRoot(), "", "The infrastructure wiki Mycroft keeps in 221B, published read-only. Its pages are edited where the wiki is kept.")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(pages, func(p WikiPage) bool { return p.Path == path })
	key := "wiki:" + path
	if i < 0 {
		ref, ok := w.ref(key)
		if !ok {
			return nil
		}
		managed := w.wikiBanner("") + "<p>This page was removed from the wiki on " + w.now.UTC().Format("2006-01-02") + ".</p>"
		return w.write(ctx, w.set.WikiSpace, key, ref.Title, root, managed, w.wikiConflict(path, ref.Title))
	}
	pg := pages[i]
	var b strings.Builder
	b.WriteString(w.wikiBanner(pg.EditURL))
	if pg.Stale {
		b.WriteString(`<ac:structured-macro ac:name="warning"><ac:rich-text-body><p>Stale: none of this page’s sources was seen in a while.</p></ac:rich-text-body></ac:structured-macro>`)
	}
	b.WriteString(Markdown(scrubText(pg.Markdown)))
	if len(pg.Sources) > 0 {
		b.WriteString("<h2>Sources</h2><ul>")
		for _, s := range pg.Sources {
			b.WriteString("<li>" + Escape(s) + "</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString("<p>Updated " + pg.Updated.UTC().Format("2006-01-02") + ".</p>")
	title := clip(pg.Title, 250)
	return w.write(ctx, w.set.WikiSpace, key, title, root, b.String(), w.wikiConflict(path, title))
}

func (w *writer) wikiBanner(edit string) string {
	text := "This page is published read-only from the infrastructure wiki in 221B, approved by people; everything above Notes is rewritten when the page changes."
	if edit != "" {
		text += ` Edit it where it is kept: <a href="` + Escape(edit) + `">` + Escape(edit) + `</a>.`
	}
	return `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>` + text + `</p></ac:rich-text-body></ac:structured-macro>`
}

func (w *writer) wikiConflict(path, title string) func(Page, string) Conflict {
	return func(_ Page, their string) Conflict {
		return Conflict{ID: "wiki:" + path, Kind: "wiki", Title: title, Their: clip(their, 4000)}
	}
}

// CheckResult is what a check found and did.
type CheckResult struct {
	Report []string `json:"report"`
	Queued int      `json:"queued"`
}

// Check compares every page with its record: what is missing or differs
// is written again, a person's edit inside a managed part is reported as
// a conflict, a page with no record is archived, and a page someone wrote
// under the tree is imported as a draft known issue. It then writes what
// it queued.
func (p *Publisher) Check(ctx context.Context) (CheckResult, error) {
	st, _, err := p.load(ctx)
	if err != nil || !st.Settings.On {
		return CheckResult{}, err
	}
	tok, err := p.token(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	cl, err := p.client(st.Settings, tok)
	if err != nil {
		return CheckResult{}, err
	}
	now := p.o.Now()
	w := &writer{p: p, cl: cl, set: st.Settings, pages: map[string]PageRef{}, known: st.Pages, now: now, imported: map[string]string{}}
	var res CheckResult
	queue := map[string]bool{"status": true}
	note := func(format string, a ...any) { res.Report = append(res.Report, fmt.Sprintf(format, a...)) }
	if p.o.Issues != nil {
		if err := w.issues(ctx); err != nil {
			return res, err
		}
		for _, k := range w.all {
			if k.Draft {
				continue
			}
			key := "ki:" + k.ID
			ref, ok := st.Pages[key]
			if !ok {
				queue[key] = true
				note("%s had no page: it is written", k.ID)
				continue
			}
			page, err := cl.Page(ctx, ref.ID)
			if NotFound(err) {
				queue[key] = true
				note("%s’s page was deleted in Confluence: it is written again", k.ID)
				continue
			}
			if err != nil {
				note("%s’s page could not be read: %v", k.ID, err)
				continue
			}
			cur, _ := split(page.Body)
			want := hashText(w.knownManaged(k))
			switch have := hashText(cur); {
			case have == want:
			case have == ref.Hash || page.Version == ref.Version:
				queue[key] = true
				note("%s’s page differed from its record: it is repaired", k.ID)
			default:
				queue[key] = true
				note("%s’s page was edited in its managed part: its owner is asked to keep or replace the edit", k.ID)
			}
			if k.Status == issues.KnownResolved && k.Resolved != nil && now.Sub(*k.Resolved) > archiveAfter {
				if month, ok := st.Pages["archive:"+k.Resolved.UTC().Format("2006-01")]; !ok || page.ParentID != month.ID {
					queue[key] = true
					note("%s moves to the resolved archive", k.ID)
				}
			}
		}
		for key := range st.Pages {
			if id, ok := strings.CutPrefix(key, "ki:"); ok && w.knownIssue(id) == nil {
				queue[key] = true
			}
		}
		// Pages people wrote under the tree become draft known issues.
		if root, ok := st.Pages["root"]; ok {
			kids, err := cl.Children(ctx, root.ID)
			if err != nil {
				note("the tree's pages could not be listed: %v", err)
			}
			ours := map[string]bool{}
			for _, ref := range st.Pages {
				ours[ref.ID] = true
			}
			for _, kid := range kids {
				if ours[kid.ID] || st.Imported[kid.ID] != "" {
					continue
				}
				pg, err := cl.Page(ctx, kid.ID)
				if err != nil {
					note("the page “%s” could not be read: %v", kid.Title, err)
					continue
				}
				title := clip(strings.TrimSpace(pg.Title), 200)
				link := pg.URL
				in := issues.KnownInput{Title: &title}
				if text := strings.TrimSpace(Text(pg.Body)); text != "" {
					in.Symptoms = []string{clip(strings.Join(strings.Fields(text), " "), 200)}
				}
				if strings.HasPrefix(link, "http") {
					in.Link = &link
				}
				ki, err := p.o.Issues.Import(ctx, in)
				if err != nil {
					note("the page “%s” could not be imported: %v", kid.Title, err)
					continue
				}
				w.imported[kid.ID] = ki.ID
				note("the page “%s”, written by hand, is imported as the draft known issue %s for an operator to accept", kid.Title, ki.ID)
			}
		}
	}
	if st.Settings.Wiki {
		pages, err := w.wikiPages(ctx)
		if err != nil {
			note("the wiki's pages could not be read: %v", err)
		}
		have := map[string]bool{}
		for _, pg := range pages {
			have["wiki:"+pg.Path] = true
			if _, ok := st.Pages["wiki:"+pg.Path]; !ok {
				note("the wiki page %s had no Confluence page: it is written", pg.Path)
			}
			queue["wiki:"+pg.Path] = true
		}
		for key := range st.Pages {
			if strings.HasPrefix(key, "wiki:") && !have[key] {
				queue[key] = true
			}
		}
	}
	keys := make([]string, 0, len(queue))
	for k := range queue {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	res.Queued = len(keys)
	p.mu.Lock()
	_, err = p.edit(ctx, func(s *state) error {
		for k, v := range w.imported {
			s.Imported[k] = v
		}
		for _, k := range keys {
			addJob(s, k, now)
		}
		s.LastCheck = now.UTC()
		s.CheckReport = res.Report
		if len(s.CheckReport) == 0 {
			s.CheckReport = []string{"Every page matches its record."}
		}
		return nil
	})
	p.mu.Unlock()
	if err != nil {
		return res, err
	}
	for i := 0; i < 40; i++ {
		n, err := p.Flush(ctx)
		if err != nil || n == 0 {
			break
		}
	}
	return res, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
