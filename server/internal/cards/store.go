package cards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// ClusterInfo is what the cards need to know of a cluster.
type ClusterInfo struct {
	Name       string
	Production bool
	// TwoPerson says a change here needs a second person: Watson's rule
	// for production clusters (P6-07).
	TwoPerson bool
}

// Approver is who approves a card: a person, or a standing approval.
type Approver struct {
	Person
	// Admin is a SuperAdmin, who alone approves the console's own records:
	// hosts, fleets and Automation parameters are theirs to change today.
	Admin bool
	// As is what an executor runs the change as: the API's Watson person,
	// or the standing approval that approves it.
	As any
}

// Executor carries out one kind of action (ADR-101).
type Executor interface {
	// Check validates an action as proposed: an error refuses the
	// proposal.
	Check(a Action) error
	// Describe reads what the action would do now, as the console: what
	// it does in words, its targets, impact and their fingerprint.
	Describe(ctx context.Context, cluster string, a Action) (Plan, error)
	// Apply carries out action i of an approved card, as by.
	Apply(ctx context.Context, c Card, i int, by Approver) (Result, error)
	// Admin says only a SuperAdmin approves it.
	Admin() bool
}

// SecretStore keeps the notification channels' addresses: a Secret, one
// key, with a version for writes that must not overwrite another's
// (kube.SecretKey, backup.MemorySecret).
type SecretStore interface {
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// Limits are what the keys may hold: their limits in the ConfigMap.
type Limits struct {
	CardsBytes, StandingBytes, NotifyBytes int
	// ChatsBytes bounds the waiting proposals' conversations (CF-61).
	ChatsBytes int
}

// Options configure the store.
type Options struct {
	Backend kv.Backend
	// Secrets keeps the channels' addresses; nil keeps them in memory.
	Secrets SecretStore
	Now     func() time.Time
	Audit   *audit.Log
	Logger  *slog.Logger
	// Cluster says what a cluster is; false for one not connected.
	Cluster func(id string) (ClusterInfo, bool)
	// Condition says whether a card's condition still holds, and why not;
	// an error is a check that could not be made.
	Condition func(ctx context.Context, cluster string, c Condition) (holds bool, why string, err error)
	// ModelOf is an agent's model, AgentName its name; nil leaves them
	// out, or as the id.
	ModelOf   func(agent string) string
	AgentName func(agent string) string
	Limits    Limits
	// HTTP sends the notifications; nil is a client with a 10-second
	// timeout.
	HTTP *http.Client
	// Leads says whether this replica leads (ADR-70): only the leader
	// expires cards. nil always does.
	Leads func() bool
}

// Store keeps the cards.
type Store struct {
	opts Options
	// mu orders this replica's writes; the versions order everyone's.
	mu sync.Mutex
	// emu guards execs and records.
	emu     sync.RWMutex
	execs   map[string]Executor
	records map[string]Record
	// applying are the cards whose actions run on this replica.
	applying sync.Map
	// sent is each channel's last delivery, on this replica.
	sent sync.Map
	wg   sync.WaitGroup
	// memSecrets stands in for the Secret without one.
	memSecrets memorySecret
	// onChange hears each card whose state changed (CF-29), set once
	// with OnChange; guarded by emu.
	onChange func(Card)
	// chatMu orders this replica's writes of the conversations (CF-61);
	// forget drops a decided proposal's from Sherlock's memory, set once
	// with SetForget, guarded by emu.
	chatMu sync.Mutex
	forget func(session string) int
}

// New makes a store.
func New(o Options) *Store {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Cluster == nil {
		o.Cluster = func(id string) (ClusterInfo, bool) { return ClusterInfo{Name: id}, id != "" }
	}
	if o.Limits.CardsBytes <= 0 {
		o.Limits.CardsBytes = DefaultCardsBytes
	}
	if o.Limits.StandingBytes <= 0 {
		o.Limits.StandingBytes = DefaultStandingBytes
	}
	if o.Limits.NotifyBytes <= 0 {
		o.Limits.NotifyBytes = DefaultNotifyBytes
	}
	if o.Limits.ChatsBytes <= 0 {
		o.Limits.ChatsBytes = DefaultChatsBytes
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	s := &Store{opts: o, execs: map[string]Executor{}, records: map[string]Record{}}
	s.execs[KindRecord] = recordExecutor{s}
	return s
}

// Register sets the executor of a kind of action. The console registers
// its own at start; a kind without one is refused on a proposal.
func (s *Store) Register(kind string, e Executor) {
	s.emu.Lock()
	defer s.emu.Unlock()
	s.execs[kind] = e
}

// SetCondition sets how a card's condition is checked: the API sets it
// once it is built, before any request.
func (s *Store) SetCondition(f func(ctx context.Context, cluster string, c Condition) (bool, string, error)) {
	s.emu.Lock()
	defer s.emu.Unlock()
	s.opts.Condition = f
}

func (s *Store) condition() func(ctx context.Context, cluster string, c Condition) (bool, string, error) {
	s.emu.RLock()
	defer s.emu.RUnlock()
	return s.opts.Condition
}

// OnChange sets what hears of a card whose state changed: proposed,
// replaced, applied, failed, rejected or expired (Phase 12, wave 6b,
// CF-29). The cases set it, so a change that resolves a case acts on it;
// it is called after the change is saved, on the replica that made it.
func (s *Store) OnChange(f func(Card)) {
	s.emu.Lock()
	defer s.emu.Unlock()
	s.onChange = f
}

// changed tells what listens of a card whose state changed. A card
// decided ends its troubleshooting first (CF-61): its conversation goes,
// its log keeps the changes that finished done.
func (s *Store) changed(c Card) {
	s.endTrouble(c)
	s.emu.RLock()
	f := s.onChange
	s.emu.RUnlock()
	if f != nil {
		f(c)
	}
}

func (s *Store) executor(kind string) Executor {
	s.emu.RLock()
	defer s.emu.RUnlock()
	return s.execs[kind]
}

// Wait waits for the actions and notifications under way, for shutdown
// and tests.
func (s *Store) Wait() { s.wg.Wait() }

func (s *Store) now() time.Time { return s.opts.Now().UTC() }

// ---------- the record ----------

type record struct {
	Next  int    `json:"next"`
	Cards []Card `json:"cards"`
}

// changeJSON edits a key's JSON over the version read, again on a
// conflict, and keeps it under max bytes with fit.
func changeJSON[T any](ctx context.Context, b kv.Backend, key string, edit func(*T) error) error {
	var err error
	for try := 0; try < 12; try++ {
		if try > 0 {
			select {
			case <-time.After(rand.N(min(50*time.Millisecond<<try, time.Second))):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var v T
		var version string
		if v, version, err = loadJSON[T](ctx, b, key); err != nil {
			return err
		}
		if err = edit(&v); err != nil {
			return err
		}
		raw, _ := json.Marshal(v)
		if _, err = b.SaveKey(ctx, key, raw, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return err
}

// loadJSON reads a key: empty when never saved, an error when not
// readable, since read as empty the next save would wipe it.
func loadJSON[T any](ctx context.Context, b kv.Backend, key string) (T, string, error) {
	var v T
	raw, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return v, version, nil
	}
	if err != nil {
		return v, "", err
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, "", fmt.Errorf("%s is not readable: %w", key, err)
	}
	return v, version, nil
}

// --- Proposed changes are PC-<n> (CF-56) ---

// replacedText starts the history line of a card a newer one replaced.
const replacedText = "replaced by the newer proposal "

// canon writes the cards' ids the one way, PC-<n>: a card kept as c<n>
// before CF-56 reads as PC-<n> from now on, and is saved so the next time
// the record changes. Doing it again changes nothing.
func (rec *record) canon() {
	for i := range rec.Cards {
		rec.Cards[i].canon()
	}
}

// canon writes a card's id, and the ids of the cards it replaced or was
// replaced by, the one way (CF-56).
func (c *Card) canon() {
	c.ID, c.ReplacedBy, c.Replaces = cardid.Canon(c.ID), cardid.Canon(c.ReplacedBy), cardid.Canon(c.Replaces)
	for j, e := range c.History {
		if id, ok := strings.CutPrefix(e.What, replacedText); ok {
			c.History[j].What = replacedText + cardid.Canon(id)
		}
	}
}

// loadCards reads the cards, their ids the one way (CF-56).
func (s *Store) loadCards(ctx context.Context) (record, error) {
	rec, _, err := loadJSON[record](ctx, s.opts.Backend, CardsKey)
	rec.canon()
	return rec, err
}

// --- end of PC-<n>.

func (s *Store) changeCards(ctx context.Context, edit func(*record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return changeJSON(ctx, s.opts.Backend, CardsKey, func(rec *record) error {
		rec.canon()
		if err := edit(rec); err != nil {
			return err
		}
		prune(rec, s.opts.Limits.CardsBytes)
		return nil
	})
}

// prune keeps every open card and the newest decided ones, as many as
// fit, with room to spare for the ConfigMap's own fields.
func prune(rec *record, limit int) {
	finished := 0
	for i := len(rec.Cards) - 1; i >= 0; i-- {
		if !rec.Cards[i].Finished() {
			continue
		}
		if finished++; finished > keepFinished {
			rec.Cards = slices.Delete(rec.Cards, i, i+1)
		}
	}
	for {
		if b, _ := json.Marshal(rec); len(b) <= limit-limit/16 {
			return
		}
		i := slices.IndexFunc(rec.Cards, Card.Finished)
		if i < 0 {
			return
		}
		rec.Cards = slices.Delete(rec.Cards, i, i+1)
	}
}

func addEvent(c *Card, at time.Time, what, by string, a Actor) {
	c.Updated = at
	c.LastBy, c.LastAt = &a, &at
	c.History = append(c.History, Event{At: at, What: what, By: by, Actor: &a})
	if n := len(c.History); n > maxHistory {
		// The first line, what proposed it, stays.
		c.History = append(c.History[:1], c.History[n-maxHistory+1:]...)
	}
}

// edit changes one card, named in either form (CF-56).
func (s *Store) edit(ctx context.Context, id string, f func(*Card) error) (Card, error) {
	var out Card
	id = cardid.Canon(id)
	err := s.changeCards(ctx, func(rec *record) error {
		for i := range rec.Cards {
			if rec.Cards[i].ID == id {
				if err := f(&rec.Cards[i]); err != nil {
					out = rec.Cards[i]
					return err
				}
				out = rec.Cards[i]
				return nil
			}
		}
		return ErrNotFound
	})
	return out, err
}

// ---------- reading ----------

// Filter narrows a listing. Mine is a person: the cards they asked for,
// set the trigger of, or decided.
type Filter struct {
	State   string
	Cluster string
	Agent   string
	Mine    *Person
}

// Mine says whether a card is a person's: they asked for it, set the
// trigger that started it, or decided it.
func (c Card) Mine(p Person) bool {
	return c.Took(p.Key) || (c.Source.SetBy != "" && (c.Source.SetBy == p.Name || c.Source.SetBy == p.Key))
}

// List returns the cards, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Card, error) {
	rec, err := s.loadCards(ctx)
	if err != nil {
		return nil, err
	}
	out := []Card{}
	for _, c := range rec.Cards {
		switch {
		case f.State != "" && c.State != f.State:
		case f.Cluster != "" && c.Cluster != f.Cluster:
		case f.Agent != "" && c.Agent != f.Agent:
		case f.Mine != nil && !c.Mine(*f.Mine):
		default:
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Get returns one card, named in either form: PC-16, or the c16 of
// before (CF-56).
func (s *Store) Get(ctx context.Context, id string) (Card, error) {
	rec, err := s.loadCards(ctx)
	if err != nil {
		return Card{}, err
	}
	id = cardid.Canon(id)
	for _, c := range rec.Cards {
		if c.ID == id {
			return c, nil
		}
	}
	return Card{}, ErrNotFound
}

// ---------- proposing ----------

// Propose stores what an agent proposes and returns its card's ID: what
// the jobs and Mycroft call (the seams' Proposer).
func (s *Store) Propose(ctx context.Context, p Proposal) (string, error) {
	c, err := s.propose(ctx, p, nil, "")
	return c.ID, err
}

// ProposeFor stores what an agent proposed in a chat with a person, who
// counts as one of the people its change needs; watsonItem is the card the
// person sent to Watson from the answer, which lives only in memory.
func (s *Store) ProposeFor(ctx context.Context, p Proposal, requester Person, watsonItem string) (Card, error) {
	return s.propose(ctx, p, &requester, watsonItem)
}

// hostKeyRE is an SSH public key, which never goes on a card.
var hostKeyRE = regexp.MustCompile(`(?:ssh-(?:rsa|dss|ed25519)|ecdsa-sha2-nistp[0-9]+|sk-ssh-ed25519@openssh\.com) AAAA[A-Za-z0-9+/]+=*`)

// clean is free text as a card keeps it: no credential, no host key, cut
// to max.
func clean(s string, max int) string {
	s = strings.TrimSpace(scrub.Text(hostKeyRE.ReplaceAllString(s, scrub.Redacted)))
	return audit.Clip(s, max)
}

// secretArg names the arguments a card never carries: a credential or a
// host key to trust (design: "Two things never go on a card").
var secretArg = regexp.MustCompile(`(?i)^(password|passwd|passphrase|sudo_?password|private_?key|secret|token|api_?key|credential|host_?key|known_?hosts|fingerprint|key_?data|cert(ificate)?_?key)$`)

// noSecrets refuses arguments that carry a credential or a host key, by
// their names or their values.
func noSecrets(v any, path string) error {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if secretArg.MatchString(k) {
				return fmt.Errorf("%w: %s%s: a card never carries a credential or a host key", ErrInvalid, path, k)
			}
			if err := noSecrets(e, path+k+"."); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := noSecrets(e, path); err != nil {
				return err
			}
		}
	case string:
		if hostKeyRE.MatchString(x) || strings.Contains(x, "-----BEGIN") {
			return fmt.Errorf("%w: %sa card never carries a key", ErrInvalid, path)
		}
		if scrub.Text(x) != x {
			return fmt.Errorf("%w: %sit looks like a credential, which a card never carries", ErrInvalid, path)
		}
	}
	return nil
}

// check validates a proposal and cleans its text.
func (s *Store) check(p *Proposal, now time.Time) error {
	p.Agent, p.Cluster = strings.TrimSpace(p.Agent), strings.TrimSpace(p.Cluster)
	p.Title = clean(p.Title, MaxTitle)
	p.Reasoning = clean(p.Reasoning, MaxReasoning)
	p.Source.Label, p.Source.SetBy = clean(p.Source.Label, MaxTitle), clean(p.Source.SetBy, 120)
	p.Source.Job = clean(p.Source.Job, 120)
	p.Source.ID = audit.Clip(strings.TrimSpace(p.Source.ID), 120)
	p.DedupKey = audit.Clip(strings.TrimSpace(p.DedupKey), 300)
	switch {
	case p.Agent == "":
		return fmt.Errorf("%w: name the agent", ErrInvalid)
	case p.Cluster == "":
		return fmt.Errorf("%w: name the cluster", ErrInvalid)
	case p.Title == "":
		return fmt.Errorf("%w: give it a title", ErrInvalid)
	case len(p.Actions) == 0:
		return fmt.Errorf("%w: a proposal does something: give its actions", ErrInvalid)
	case len(p.Actions) > MaxActions:
		return fmt.Errorf("%w: at most %d actions", ErrInvalid, MaxActions)
	case len(p.Evidence) > MaxEvidence:
		p.Evidence = p.Evidence[:MaxEvidence]
	}
	switch p.Source.Kind {
	case SourceJob, SourceAlert, SourceReport, SourceKnowledge, SourceChat, SourceCase:
	default:
		return fmt.Errorf("%w: a source is a job, alert, report, case, knowledge or chat, not %q", ErrInvalid, p.Source.Kind)
	}
	for i := range p.Evidence {
		e := &p.Evidence[i]
		e.Kind, e.Text, e.Ref = audit.Clip(strings.TrimSpace(e.Kind), 20), clean(e.Text, MaxText), clean(e.Ref, 400)
	}
	for i := range p.Actions {
		a := &p.Actions[i]
		a.Impact, a.Risk = clean(a.Impact, MaxImpact), strings.ToLower(strings.TrimSpace(a.Risk))
		switch a.Risk {
		case "", "low", "medium", "high":
		default:
			return fmt.Errorf("%w: action %d: a risk is low, medium or high", ErrInvalid, i+1)
		}
		if a.Args == nil {
			a.Args = map[string]any{}
		}
		if b, _ := json.Marshal(a.Args); len(b) > MaxArgs {
			return fmt.Errorf("%w: action %d's arguments are over %d KiB", ErrInvalid, i+1, MaxArgs>>10)
		}
		if err := noSecrets(map[string]any(a.Args), ""); err != nil {
			return fmt.Errorf("action %d: %w", i+1, err)
		}
		e := s.executor(a.Kind)
		if e == nil {
			return fmt.Errorf("%w: action %d: Watson does not run %q on this console", ErrInvalid, i+1, a.Kind)
		}
		if err := e.Check(*a); err != nil {
			return fmt.Errorf("action %d (%s): %w", i+1, a.Kind, err)
		}
	}
	if err := checkCondition(&p.Condition); err != nil {
		return err
	}
	if err := checkResolves(p); err != nil {
		return err
	}
	if p.Expires.IsZero() || p.Expires.After(now.Add(Lifetime)) {
		p.Expires = now.Add(Lifetime)
	}
	if !p.Expires.After(now) {
		return fmt.Errorf("%w: it has expired already", ErrInvalid)
	}
	p.Expires = p.Expires.UTC()
	return nil
}

// refPattern is a case's or a known issue's id, as a card names what it
// resolves.
var refPattern = regexp.MustCompile(`^(C|KI)-[1-9][0-9]{0,8}$`)

// NormRef writes the id of a case or a known issue one way ("c-12" is
// "C-12"), and says whether it is one.
func NormRef(ref string) (string, bool) {
	ref = strings.ToUpper(strings.TrimSpace(ref))
	return ref, refPattern.MatchString(ref)
}

// checkResolves validates what a proposal resolves (CF-25): the ids of
// cases and known issues, each once, at most MaxResolves, each linked by
// the agent unless the proposal says who. That each exists is the
// producer's to check: the jobs ask the cases.
func checkResolves(p *Proposal) error {
	var out []string
	by := map[string]string{}
	for _, r := range p.Resolves {
		ref, ok := NormRef(r)
		if !ok {
			return fmt.Errorf("%w: %q is not a case (C-12) or a known issue (KI-3)", ErrInvalid, audit.Clip(r, 40))
		}
		if slices.Contains(out, ref) {
			continue
		}
		out = append(out, ref)
		by[ref] = LinkedByAgent
		if who := clean(p.ResolvesBy[r], 120); who != "" {
			by[ref] = who
		}
	}
	if len(out) > MaxResolves {
		return fmt.Errorf("%w: a change resolves at most %d cases and known issues", ErrInvalid, MaxResolves)
	}
	p.Resolves, p.ResolvesBy = out, by
	if len(out) == 0 {
		p.Resolves, p.ResolvesBy = nil, nil
	}
	return nil
}

// passResolves gives a newer card what the card it replaces resolves
// (CF-28), with who linked each, as many as fit.
func passResolves(c *Card, old Card) {
	for _, ref := range old.Resolves {
		if slices.Contains(c.Resolves, ref) || len(c.Resolves) >= MaxResolves {
			continue
		}
		c.Resolves = append(c.Resolves, ref)
		if c.ResolvesBy == nil {
			c.ResolvesBy = map[string]string{}
		}
		c.ResolvesBy[ref] = firstNonEmpty(old.ResolvesBy[ref], LinkedByAgent)
	}
}

// keyedBefore says whether a waiting card filed under a key of before
// W-10 (the job or the case, and the title) is the change a new key names
// (W-10, CF-101): its key, worked out from its cluster, what the agent
// linked it to and its steps, is the new one. So a card that waits since
// before the keys changed is replaced by the same change, not joined by
// it; such a card waits a day at most.
func keyedBefore(x Card, key string) bool {
	if !strings.HasPrefix(key, cardid.KeyPrefix) || x.DedupKey == "" || strings.HasPrefix(x.DedupKey, cardid.KeyPrefix) {
		return false
	}
	if !strings.HasPrefix(x.DedupKey, "job:") && !strings.HasPrefix(x.DedupKey, "case:") && !strings.HasPrefix(x.DedupKey, "report:") {
		return false
	}
	var refs []string
	for _, r := range x.Resolves {
		if by := x.ResolvesBy[r]; by == "" || by == LinkedByAgent {
			refs = append(refs, r)
		}
	}
	steps := make([]string, len(x.Actions))
	for i, a := range x.Actions {
		steps[i] = cardid.Step(a.Kind, a.Args)
	}
	return cardid.Key(x.Cluster, refs, steps) == key
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// checkCondition validates a condition, and names an alert by its key.
func checkCondition(c *Condition) error {
	str := func(k string) string { v, _ := c.Args[k].(string); return strings.TrimSpace(v) }
	switch c.Kind {
	case "", ConditionNone:
		c.Kind, c.Args = ConditionNone, nil
	case ConditionAlert:
		key := str("key")
		if key == "" && str("kind") != "" && str("name") != "" {
			key = str("kind") + ":" + strings.TrimPrefix(str("namespace")+"/"+str("name"), "/")
		}
		if key == "" || !strings.Contains(key, ":") {
			return fmt.Errorf("%w: an alert condition names its alert: {key: \"pod:shop/web-7f9\"}", ErrInvalid)
		}
		c.Args = map[string]any{"key": key}
		if n := str("name"); n != "" {
			c.Args["name"] = n
		}
		if a := str("alert"); a != "" {
			c.Args["alert"] = a
		}
	case ConditionVersion:
		if str("resource") == "" || str("name") == "" || str("resourceVersion") == "" {
			return fmt.Errorf("%w: a resource-version condition names the resource, its name and the resourceVersion read", ErrInvalid)
		}
		c.Args = map[string]any{"resource": str("resource"), "namespace": str("namespace"), "name": str("name"), "resourceVersion": str("resourceVersion")}
	default:
		return fmt.Errorf("%w: a condition is none, alert-firing or resource-version, not %q", ErrInvalid, c.Kind)
	}
	return nil
}

// needed is how many approvals a card needs: one, and on a cluster where
// changes need a second person, two for an agent's own (an agent never
// counts as a person) and one other than the person who asked in a chat.
func needed(info ClusterInfo, requester *Person) int {
	if info.TwoPerson && requester == nil {
		return 2
	}
	return 1
}

func (s *Store) agentName(id string) string {
	if s.opts.AgentName != nil {
		if n := s.opts.AgentName(id); n != "" {
			return n
		}
	}
	return id
}

func (s *Store) propose(ctx context.Context, p Proposal, requester *Person, watsonItem string) (Card, error) {
	now := s.now()
	if err := s.check(&p, now); err != nil {
		return Card{}, err
	}
	info, ok := s.opts.Cluster(p.Cluster)
	if !ok {
		return Card{}, fmt.Errorf("%w: no cluster %q is connected", ErrInvalid, p.Cluster)
	}
	plans := make([]Plan, len(p.Actions))
	for i, a := range p.Actions {
		pl, err := s.executor(a.Kind).Describe(ctx, p.Cluster, a)
		if err != nil {
			pl.Fingerprint, pl.Unread = "", clean(err.Error(), MaxText)
		}
		if pl.Summary == "" {
			pl.Summary = a.Kind
		}
		plans[i] = pl
	}
	c := Card{Proposal: p, State: Queued, Created: now, Updated: now, Requester: requester, Production: info.Production, Needed: needed(info, requester), Plans: plans, WatsonItem: watsonItem}
	if s.opts.ModelOf != nil {
		c.Model = s.opts.ModelOf(p.Agent)
	}
	// Who proposed it: the agent, or the person who asked in a chat
	// (CF-35).
	proposer := Actor{Name: s.agentName(p.Agent), Kind: ActorAgent}
	if requester != nil {
		proposer = ActorOf(*requester)
	}
	what := "proposed by " + s.agentName(p.Agent)
	if p.Source.Label != "" {
		what += ": " + p.Source.Label
	}
	c.ProposedBy = ptrActor(proposer)
	addEvent(&c, now, what, p.Source.SetBy, proposer)
	var replaced []Card
	err := s.changeCards(ctx, func(rec *record) error {
		replaced = nil
		open := 0
		for _, x := range rec.Cards {
			if x.Open() {
				open++
			}
		}
		if open >= MaxOpen {
			return fmt.Errorf("%w: %d proposed changes wait already; decide some first", ErrFull, open)
		}
		rec.Next++
		c.ID = cardid.Of(rec.Next)
		if c.DedupKey != "" {
			for i := range rec.Cards {
				x := &rec.Cards[i]
				if x.Open() && (x.DedupKey == c.DedupKey || keyedBefore(*x, c.DedupKey)) {
					x.State, x.ReplacedBy = Replaced, c.ID
					addEvent(x, now, replacedText+c.ID, "", SystemActor)
					c.Replaces = x.ID
					passResolves(&c, *x)
					replaced = append(replaced, *x)
				}
			}
		}
		rec.Cards = append(rec.Cards, c)
		return nil
	})
	if err != nil {
		return Card{}, err
	}
	for _, x := range replaced {
		s.record(audit.ActionCardReplace, x, Person{Name: s.agentName(p.Agent)}, audit.OutcomeOK, "replaced by "+c.ID+", a newer proposed change for the same thing ("+c.DedupKey+")")
	}
	detail := fmt.Sprintf("proposed %s; actions: %s; needs %d approval(s)", strconv.Quote(c.Title), strings.Join(c.Kinds(), ", "), c.Needed)
	if len(c.Resolves) > 0 {
		detail += "; resolves " + strings.Join(c.Resolves, ", ")
	}
	s.record(audit.ActionCardPropose, c, Person{Name: s.agentName(p.Agent)}, audit.OutcomeOK, detail)
	for _, x := range replaced {
		s.changed(x)
	}
	s.changed(c)
	s.announce(c)
	return c, nil
}

// announce tells the channels of a new card, and tries the standing
// approvals on it, after the request that proposed it.
func (s *Store) announce(c Card) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.notify(ctx, c)
		s.tryStanding(ctx, c)
	}()
}

// ---------- the audit log ----------

// record writes an audit record of a card: the agent, its model, what
// started it and who set that, never a secret or any output.
func (s *Store) record(action string, c Card, by Person, outcome, detail string) {
	if s.opts.Audit == nil {
		return
	}
	d := fmt.Sprintf("proposed change %s by %s", c.ID, s.agentName(c.Agent))
	if c.Model != "" {
		d += " (" + c.Model + ")"
	}
	if c.Source.Label != "" {
		d += ", started by " + c.Source.Label
	}
	if c.Source.SetBy != "" {
		d += ", trigger set by " + c.Source.SetBy
	}
	if detail != "" {
		d += ": " + detail
	}
	s.opts.Audit.Record(audit.Event{Time: s.now(), Action: action, Who: audit.Who{Subject: by.Key, Name: by.Name}, Cluster: c.Cluster, Target: "proposal " + c.ID, Outcome: outcome, Detail: d})
}

// ---------- deciding ----------

// needsAdmin says whether only a SuperAdmin approves a card.
func (s *Store) needsAdmin(c Card) bool {
	for _, a := range c.Actions {
		if e := s.executor(a.Kind); e == nil || e.Admin() || (a.Kind == KindRecord && s.recordAdmin(a)) {
			return true
		}
	}
	return false
}

// CanApprove says whether a person may approve a card now, and why not:
// every rule but the re-check, which reads the cluster.
func (s *Store) CanApprove(c Card, by Approver) error {
	if c.State != Queued {
		return fmt.Errorf("%w: %s", ErrState, DecidedText(c))
	}
	_, err := s.may(c, by, "")
	return err
}

// may checks the rules of today for an approval (P6-07, ADR-100) and
// returns the cluster as it is now.
func (s *Store) may(c Card, by Approver, standing string) (ClusterInfo, error) {
	info, ok := s.opts.Cluster(c.Cluster)
	if !ok {
		return info, fmt.Errorf("%w: its cluster is no longer connected", ErrState)
	}
	switch {
	case standing != "" && info.Production:
		return info, fmt.Errorf("%w: a standing approval never approves anything on production", ErrForbidden)
	case s.needsAdmin(c) && !by.Admin:
		return info, fmt.Errorf("%w: only a SuperAdmin approves changes to hosts, fleets, Automation parameters and the console's other records", ErrForbidden)
	case slices.ContainsFunc(c.Approvals, func(d Decision) bool { return by.Key != "" && d.By.Key == by.Key }):
		return info, fmt.Errorf("%w: you approved it already; it waits for someone else", ErrState)
	case info.TwoPerson && by.Key == "":
		return info, fmt.Errorf("%w: changes to %s need a second person, and with sign-in off the console cannot tell people apart", ErrForbidden, info.Name)
	case info.TwoPerson && c.Requester != nil && c.Requester.Key == by.Key:
		return info, fmt.Errorf("%w: %s is a production cluster, where someone other than you approves what you asked for", ErrOwn, info.Name)
	}
	return info, nil
}

// AgentName is an agent's name, as cards and messages say it.
func (s *Store) AgentName(id string) string { return s.agentName(id) }

// DecidedText says who decided a card that no longer waits, and when, for
// the person whose approval or rejection came second: "already approved
// by Ann Lee at 2026-11-06 14:03:07 UTC" (Phase 12, IB-08).
func DecidedText(c Card) string {
	at := func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") }
	switch c.State {
	case Queued:
		return "it still waits"
	case Approved, Applied, Failed:
		if n := len(c.Approvals); n > 0 {
			last := c.Approvals[n-1]
			names := make([]string, 0, n)
			for _, d := range c.Approvals {
				names = append(names, d.By.Name)
			}
			by := strings.Join(names, " and ")
			if last.Standing != "" {
				by = "a standing approval"
			}
			text := "already approved by " + by + " at " + at(last.At)
			switch c.State {
			case Applied:
				text += "; it was applied"
			case Failed:
				text += "; it failed"
			}
			return text
		}
		return "already approved"
	case Rejected:
		if c.Rejection != nil {
			return "already rejected by " + c.Rejection.By.Name + " at " + at(c.Rejection.At)
		}
		return "already rejected"
	case Expired:
		return "it expired at " + at(c.Updated)
	case Replaced:
		if c.ReplacedBy != "" {
			return "a newer proposed change, " + c.ReplacedBy + ", replaced it at " + at(c.Updated)
		}
		return "a newer proposed change replaced it at " + at(c.Updated)
	}
	return "it is " + c.State
}

// Approve approves a card for a person (or a standing approval): every
// rule of today applies, Watson checks the card again, and once it has
// the approvals it needs its actions run, in order, as that person.
func (s *Store) Approve(ctx context.Context, id string, by Approver, comment string) (Card, error) {
	return s.approve(ctx, id, by, comment, "")
}

func (s *Store) approve(ctx context.Context, id string, by Approver, comment, standing string) (Card, error) {
	comment = strings.TrimSpace(comment)
	if len(comment) > MaxComment {
		return Card{}, fmt.Errorf("%w: a comment is at most %d characters", ErrInvalid, MaxComment)
	}
	comment = clean(comment, MaxComment)
	c, err := s.Get(ctx, id)
	if err != nil {
		return c, err
	}
	if c.State != Queued {
		return c, fmt.Errorf("%w: %s", ErrState, DecidedText(c))
	}
	if !s.now().Before(c.Expires) {
		c, _ = s.expire(ctx, id, "it waited until it expired")
		return c, fmt.Errorf("%w: it expired", ErrState)
	}
	info, err := s.may(c, by, standing)
	if err != nil {
		return c, err
	}
	need := needed(info, c.Requester)
	rc := s.recheck(ctx, c)
	c, err = s.edit(ctx, id, func(x *Card) error {
		x.Recheck = &rc
		return nil
	})
	if err != nil {
		return c, err
	}
	if !rc.OK {
		s.record(audit.ActionCardRecheck, c, by.Person, audit.OutcomeDenied, "not approved: "+rc.Why)
		return c, fmt.Errorf("%w: %s", ErrStale, rc.Why)
	}
	ready := false
	c, err = s.edit(ctx, id, func(x *Card) error {
		// Someone else decided it while it was checked again: the second
		// approval is refused with who and when, never counted (asked
		// November 6, 2026).
		if x.State != Queued {
			return fmt.Errorf("%w: %s", ErrState, DecidedText(*x))
		}
		if slices.ContainsFunc(x.Approvals, func(d Decision) bool { return by.Key != "" && d.By.Key == by.Key }) {
			return fmt.Errorf("%w: you approved it already", ErrState)
		}
		now := s.now()
		x.Approvals = append(x.Approvals, Decision{By: by.Person, At: now, Comment: comment, Standing: standing})
		x.Needed, x.Production = need, info.Production
		what := "approved"
		if standing != "" {
			x.Standing = standing
			what = "approved by a standing approval"
		}
		if len(x.Approvals) >= need {
			x.State, ready = Approved, true
		} else {
			what += fmt.Sprintf(": %d of %d", len(x.Approvals), need)
		}
		addEvent(x, now, what, by.Name, ActorOf(by.Person))
		return nil
	})
	if err != nil {
		return c, err
	}
	detail := fmt.Sprintf("approval %d of %d", len(c.Approvals), c.Needed)
	if standing != "" {
		detail += ", by a standing approval"
	}
	if c.Requester != nil {
		detail += ", asked for by " + c.Requester.Name
	}
	s.record(audit.ActionCardApprove, c, by.Person, audit.OutcomeOK, detail)
	if ready {
		s.wg.Add(1)
		s.applying.Store(c.ID, true)
		go s.apply(c, by)
	}
	return c, nil
}

// apply runs an approved card's actions in order, stopping at the first
// that fails, and records how each went.
func (s *Store) apply(c Card, by Approver) {
	defer s.wg.Done()
	defer s.applying.Delete(c.ID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	var results []Result
	state, reason := Applied, ""
	for i, a := range c.Actions {
		r, err := s.executor(a.Kind).Apply(ctx, c, i, by)
		r.Action, r.At = i, s.now()
		r.Summary, r.Detail = clean(r.Summary, MaxText), clean(r.Detail, 4*MaxText)
		if err != nil {
			r.OK = false
			if r.Summary == "" {
				r.Summary = clean(err.Error(), MaxText)
			}
		}
		results = append(results, r)
		if !r.OK {
			state, reason = Failed, fmt.Sprintf("action %d of %d failed: %s", i+1, len(c.Actions), r.Summary)
			break
		}
	}
	saveCtx, cancelSave := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSave()
	// A new leader's sweep may have failed the card while this replica
	// still ran it (W-11, ADR-149): that state stays, and what was done is
	// kept on the card with a note, so its state never flips twice.
	late := ""
	out, err := s.edit(saveCtx, c.ID, func(x *Card) error {
		late = ""
		if x.State != Approved {
			late = x.State
			x.Results = results
			addEvent(x, s.now(), fmt.Sprintf("the actions it was running finished after it was marked %s: %s; it stays %s, and the results are what was done", x.State, state, x.State), by.Name, ActorOf(by.Person))
			return nil
		}
		x.State, x.Results, x.Reason = state, results, reason
		addEvent(x, s.now(), state, by.Name, ActorOf(by.Person))
		return nil
	})
	if err != nil {
		s.opts.Logger.Warn("a proposal's result could not be saved", "proposal", c.ID, "err", err)
		out = c
	}
	var names []string
	for _, d := range out.Approvals {
		names = append(names, d.By.Name)
	}
	var done []string
	for _, r := range results {
		done = append(done, fmt.Sprintf("%s: %s", c.Actions[r.Action].Kind, r.Summary))
	}
	outcome := audit.OutcomeOK
	if state == Failed {
		outcome = audit.OutcomeFailed
	}
	detail := fmt.Sprintf("%s, approved by %s; %s", state, strings.Join(names, ", "), strings.Join(done, "; "))
	if late != "" {
		detail = fmt.Sprintf("the actions finished %s after the card was marked %s, which it stays; approved by %s; %s", state, late, strings.Join(names, ", "), strings.Join(done, "; "))
	}
	s.record(audit.ActionCardApply, out, by.Person, outcome, detail)
	if err == nil {
		s.changed(out)
	}
}

// Reject rejects a card, with the reason; the person who asked for it in
// a chat withdraws it the same way.
func (s *Store) Reject(ctx context.Context, id string, by Person, reason string) (Card, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Card{}, fmt.Errorf("%w: say why it is rejected", ErrInvalid)
	}
	if len(reason) > MaxComment {
		return Card{}, fmt.Errorf("%w: a reason is at most %d characters", ErrInvalid, MaxComment)
	}
	reason = clean(reason, MaxComment)
	c, err := s.edit(ctx, id, func(x *Card) error {
		if x.State != Queued {
			return fmt.Errorf("%w: %s", ErrState, DecidedText(*x))
		}
		now := s.now()
		x.State, x.Rejection = Rejected, &Decision{By: by, At: now, Comment: reason}
		addEvent(x, now, "rejected: "+reason, by.Name, ActorOf(by))
		return nil
	})
	if err != nil {
		return c, err
	}
	s.record(audit.ActionCardReject, c, by, audit.OutcomeOK, "rejected: "+reason)
	s.changed(c)
	return c, nil
}

// Recheck checks a card again on a person's request, and keeps the result.
func (s *Store) Recheck(ctx context.Context, id string, by Person) (Card, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return c, err
	}
	if c.State != Queued {
		return c, fmt.Errorf("%w: it is %s", ErrState, c.State)
	}
	rc := s.recheck(ctx, c)
	c, err = s.edit(ctx, id, func(x *Card) error {
		x.Recheck = &rc
		return nil
	})
	if err != nil {
		return c, err
	}
	outcome, detail := audit.OutcomeOK, "holds"
	if !rc.OK {
		outcome, detail = audit.OutcomeDenied, rc.Why
	}
	s.record(audit.ActionCardRecheck, c, by, outcome, detail)
	return c, nil
}

// recheck tests that a card's condition still holds and that its targets
// have not changed since it was proposed (design: Re-checked at
// approval).
func (s *Store) recheck(ctx context.Context, c Card) Recheck {
	rc := Recheck{At: s.now(), OK: true}
	fail := func(format string, a ...any) Recheck {
		rc.OK, rc.Why = false, fmt.Sprintf(format, a...)
		return rc
	}
	switch c.Condition.Kind {
	case ConditionAlert, ConditionVersion:
		check := s.condition()
		if check == nil {
			return fail("Watson cannot check that %s on this console.", conditionText(c.Condition))
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		holds, why, err := check(cctx, c.Cluster, c.Condition)
		cancel()
		if err != nil {
			return fail("Watson could not check that %s: %v. Try again in a moment.", conditionText(c.Condition), err)
		}
		if !holds {
			if why == "" {
				why = "it no longer holds"
			}
			return fail("The condition no longer holds: %s.", strings.TrimSuffix(why, "."))
		}
		rc.Checked = append(rc.Checked, upper(conditionText(c.Condition))+": it still does.")
	}
	for i, a := range c.Actions {
		var was Plan
		if i < len(c.Plans) {
			was = c.Plans[i]
		}
		if was.Fingerprint == "" {
			why := was.Unread
			if why == "" {
				why = "nothing was read"
			}
			return fail("Watson could not read what action %d acts on when it was proposed (%s), so it cannot tell whether that changed since. Reject this card; the agent proposes it again.", i+1, strings.TrimSuffix(why, "."))
		}
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		now, err := s.executor(a.Kind).Describe(dctx, c.Cluster, a)
		cancel()
		if err != nil {
			return fail("Watson could not read what action %d acts on now: %v. Try again in a moment.", i+1, err)
		}
		if now.Fingerprint != was.Fingerprint {
			what := strings.Join(was.Targets, ", ")
			if what == "" {
				what = "what it acts on"
			}
			return fail("Action %d (%s): %s changed since it was proposed, so the card no longer says what approving it does. Reject it; a fresh proposed change shows the state as it is.", i+1, was.Summary, what)
		}
		rc.Checked = append(rc.Checked, fmt.Sprintf("Action %d (%s): its target is unchanged since it was proposed.", i+1, was.Summary))
		if now.Warning != "" {
			rc.Warnings = append(rc.Warnings, fmt.Sprintf("Action %d (%s): %s", i+1, was.Summary, now.Warning))
		}
	}
	return rc
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// conditionText says what a condition asks, in words.
func conditionText(c Condition) string {
	str := func(k string) string { v, _ := c.Args[k].(string); return v }
	switch c.Kind {
	case ConditionAlert:
		if a := str("alert"); a != "" {
			return "the alert " + a + " on " + str("key") + " is still firing"
		}
		return "the alert on " + str("key") + " is still firing"
	case ConditionVersion:
		obj := str("resource") + " " + str("name")
		if ns := str("namespace"); ns != "" {
			obj += " in " + ns
		}
		return obj + " is still at version " + str("resourceVersion")
	}
	return "nothing more"
}

// ---------- expiry ----------

func (s *Store) expire(ctx context.Context, id, why string) (Card, error) {
	c, err := s.edit(ctx, id, func(x *Card) error {
		if x.State != Queued {
			return fmt.Errorf("%w: it is %s", ErrState, x.State)
		}
		x.State, x.Reason = Expired, why
		addEvent(x, s.now(), "expired: "+why, "", SystemActor)
		return nil
	})
	if err != nil {
		return c, err
	}
	s.record(audit.ActionCardExpire, c, Person{Name: "Watson"}, audit.OutcomeOK, "expired: "+why)
	s.changed(c)
	return c, nil
}

// Sweep expires the cards whose time is up or whose alert resolved, and
// fails a card left approved by a console that stopped while it ran its
// actions.
func (s *Store) Sweep(ctx context.Context) {
	rec, err := s.loadCards(ctx)
	if err != nil {
		s.opts.Logger.Warn("the proposals could not be read", "err", err)
		return
	}
	// The conversations of proposals decided elsewhere, or gone, go too
	// (CF-61).
	s.sweepChats(ctx, rec)
	now := s.now()
	for _, c := range rec.Cards {
		switch {
		case c.State == Queued && !now.Before(c.Expires):
			_, _ = s.expire(ctx, c.ID, "it waited until it expired, "+c.Expires.Format("2006-01-02 15:04:05 UTC"))
		case c.State == Queued && c.Condition.Kind == ConditionAlert && s.condition() != nil:
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			holds, why, err := s.condition()(cctx, c.Cluster, c.Condition)
			cancel()
			if err == nil && !holds {
				if why == "" {
					why = "it is no longer firing"
				}
				_, _ = s.expire(ctx, c.ID, "its alert resolved: "+strings.TrimSuffix(why, "."))
			}
		case c.State == Approved && now.Sub(c.Updated) > 5*time.Minute:
			if _, mine := s.applying.Load(c.ID); mine {
				continue
			}
			out, err := s.edit(ctx, c.ID, func(x *Card) error {
				if x.State != Approved {
					return ErrState
				}
				x.State, x.Reason = Failed, "the console stopped while it ran the actions; check what was done before proposing it again"
				addEvent(x, now, "failed: the console stopped", "", SystemActor)
				return nil
			})
			if err == nil {
				s.record(audit.ActionCardApply, out, Person{Name: "Watson"}, audit.OutcomeFailed, out.Reason)
				s.changed(out)
			}
		}
	}
}

// Run sweeps the cards every so often while this replica leads.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.opts.Leads == nil || s.opts.Leads() {
			s.Sweep(ctx)
		}
	}
}

// ---------- a chat's card in the Watson panel ----------

// WatsonEnded hears how the Watson card of a chat's proposal ended: run
// in the panel, the proposal is done; discarded there, it is withdrawn;
// lapsed, the proposal waits on here.
func (s *Store) WatsonEnded(ctx context.Context, item, status string, approver Person, exit int) {
	rec, err := s.loadCards(ctx)
	if err != nil {
		return
	}
	i := slices.IndexFunc(rec.Cards, func(c Card) bool { return c.WatsonItem == item && c.Open() })
	if i < 0 {
		return
	}
	id := rec.Cards[i].ID
	var action, outcome, detail string
	by := approver
	c, err := s.edit(ctx, id, func(x *Card) error {
		if !x.Open() {
			return ErrState
		}
		now := s.now()
		switch status {
		case "done", "failed", "stopped":
			x.Approvals = append(x.Approvals, Decision{By: approver, At: now, Comment: "approved in the Watson panel"})
			r := Result{Action: 0, OK: status == "done", Summary: fmt.Sprintf("run in the Watson panel: %s, exit %d", status, exit), Ref: item, At: now}
			x.Results = []Result{r}
			x.State, action, outcome = Applied, audit.ActionCardApply, audit.OutcomeOK
			if !r.OK {
				x.State, x.Reason, outcome = Failed, r.Summary, audit.OutcomeFailed
			}
			detail = x.State + " in the Watson panel"
			addEvent(x, now, detail, approver.Name, ActorOf(approver))
		case "discarded":
			if x.Requester != nil {
				by = *x.Requester
			}
			x.State, x.Rejection = Rejected, &Decision{By: by, At: now, Comment: "withdrawn in the Watson panel"}
			action, outcome, detail = audit.ActionCardReject, audit.OutcomeOK, "withdrawn in the Watson panel"
			addEvent(x, now, detail, by.Name, ActorOf(by))
		case "lapsed":
			x.WatsonItem = ""
			addEvent(x, now, "its Watson card lapsed; the proposed change waits on in the Inbox", "", SystemActor)
		default:
			return ErrState
		}
		return nil
	})
	if err == nil && action != "" {
		s.record(action, c, by, outcome, detail)
		s.changed(c)
	}
}

// ---------- what a card resolves (Phase 12, wave 6b, CF-28) ----------

// Link adds a case or a known issue to what a card resolves, linked by a
// person. ErrState when the card resolves it already; ErrInvalid past
// MaxResolves.
func (s *Store) Link(ctx context.Context, id, ref string, by Person) (Card, error) {
	ref, ok := NormRef(ref)
	if !ok {
		return Card{}, fmt.Errorf("%w: name a case (C-12) or a known issue (KI-3)", ErrInvalid)
	}
	name := firstNonEmpty(clean(by.Name, 120), clean(by.Key, 120), "someone")
	return s.edit(ctx, id, func(x *Card) error {
		if slices.Contains(x.Resolves, ref) {
			return fmt.Errorf("%w: %s resolves %s already", ErrState, x.ID, ref)
		}
		if len(x.Resolves) >= MaxResolves {
			return fmt.Errorf("%w: a change resolves at most %d cases and known issues", ErrInvalid, MaxResolves)
		}
		x.Resolves = append(x.Resolves, ref)
		if x.ResolvesBy == nil {
			x.ResolvesBy = map[string]string{}
		}
		x.ResolvesBy[ref] = name
		addEvent(x, s.now(), "linked to "+ref, name, ActorOf(Person{Key: by.Key, Name: name, Kind: by.Kind}))
		return nil
	})
}

// Unlink takes a case or a known issue a person linked off what a card
// resolves. An agent's link stays (ErrState): reject the card instead.
// ErrNotFound when the card does not resolve it.
func (s *Store) Unlink(ctx context.Context, id, ref string, by Person) (Card, error) {
	ref, _ = NormRef(ref)
	name := firstNonEmpty(clean(by.Name, 120), clean(by.Key, 120), "someone")
	return s.edit(ctx, id, func(x *Card) error {
		i := slices.Index(x.Resolves, ref)
		if i < 0 {
			return fmt.Errorf("%w: %s does not resolve %s", ErrNotFound, x.ID, ref)
		}
		if who := x.ResolvesBy[ref]; who == "" || who == LinkedByAgent {
			return fmt.Errorf("%w: the agent proposed %s for %s, so the link stays; reject the change instead", ErrState, x.ID, ref)
		}
		x.Resolves = slices.Delete(x.Resolves, i, i+1)
		delete(x.ResolvesBy, ref)
		if len(x.Resolves) == 0 {
			x.Resolves, x.ResolvesBy = nil, nil
		}
		addEvent(x, s.now(), "unlinked from "+ref, name, ActorOf(Person{Key: by.Key, Name: name, Kind: by.Kind}))
		return nil
	})
}

// ---------- backup ----------

// Backup is the store's share of a backup: every card with its history,
// the standing approvals, and the notification settings, each channel's
// address as the placeholder when one is kept.
type Backup struct {
	Next     int           `json:"next,omitempty"`
	Cards    []Card        `json:"cards,omitempty"`
	Standing []Standing    `json:"standing,omitempty"`
	Notify   *NotifyBackup `json:"notify,omitempty"`
}

// Export is the store's records for a backup; placeholder stands for each
// channel address kept.
func (s *Store) Export(ctx context.Context, seal func(string) string) (Backup, error) {
	rec, err := s.loadCards(ctx)
	if err != nil {
		return Backup{}, err
	}
	st, _, err := loadJSON[standingRecord](ctx, s.opts.Backend, StandingKey)
	if err != nil {
		return Backup{}, err
	}
	n, _, err := loadJSON[NotifySettings](ctx, s.opts.Backend, NotifyKey)
	if err != nil {
		return Backup{}, err
	}
	out := Backup{Next: rec.Next, Cards: rec.Cards, Standing: st.Standing}
	if len(n.Channels) > 0 || n.Link != "" {
		urls, _, _ := s.urls(ctx)
		nb := &NotifyBackup{Link: n.Link}
		for _, ch := range n.Channels {
			b := ChannelBackup{Channel: ch}
			b.URL = seal(urls[ch.ID])
			nb.Channels = append(nb.Channels, b)
		}
		out.Notify = nb
	}
	for i := range out.Standing {
		out.Standing[i].Applied = nil
	}
	return out, nil
}

// Import restores a backup: cards missing here are added with their
// history, standing approvals are added or replaced by ID, and the
// notification settings replaced, each channel keeping the address this
// console has for it, or taking the file's when open opens it (sealed with
// the install's backup key, ADR-137; else it is listed to give again). It
// returns how many records it restored and what is left to do by hand.
func (s *Store) Import(ctx context.Context, b Backup, open func(inFile, name, where string) (string, error)) (int, []string, error) {
	n := 0
	var notes []string
	if len(b.Cards) > 0 || b.Next > 0 {
		err := s.changeCards(ctx, func(rec *record) error {
			n = 0
			have := map[string]bool{}
			for _, c := range rec.Cards {
				have[c.ID] = true
			}
			for _, c := range b.Cards {
				// A backup made before CF-56 names its cards c<n>: each comes
				// back as PC-<n>.
				c.canon()
				if c.ID == "" || have[c.ID] {
					continue
				}
				have[c.ID] = true
				rec.Cards = append(rec.Cards, c)
				n++
			}
			rec.Next = max(rec.Next, b.Next)
			sort.SliceStable(rec.Cards, func(i, j int) bool { return rec.Cards[i].Created.Before(rec.Cards[j].Created) })
			return nil
		})
		if err != nil {
			return 0, nil, err
		}
	}
	if len(b.Standing) > 0 {
		k := 0
		err := s.changeStanding(ctx, func(rec *standingRecord) error {
			k = 0
			for _, x := range b.Standing {
				if x.ID == "" {
					continue
				}
				x.Applied = nil
				if i := slices.IndexFunc(rec.Standing, func(y Standing) bool { return y.ID == x.ID }); i >= 0 {
					rec.Standing[i] = x
				} else {
					rec.Standing = append(rec.Standing, x)
				}
				k++
			}
			return nil
		})
		if err != nil {
			return n, notes, err
		}
		n += k
	}
	if b.Notify != nil {
		urls, version, _ := s.urls(ctx)
		next := maps.Clone(urls)
		if next == nil {
			next = map[string]string{}
		}
		set := NotifySettings{Link: b.Notify.Link}
		for _, ch := range b.Notify.Channels {
			set.Channels = append(set.Channels, ch.Channel)
			if ch.URL == "" || urls[ch.ID] != "" {
				continue
			}
			addr, err := open(ch.URL, fmt.Sprintf("the address of the notification channel %q", ch.Name), "Settings › Watson › Notifications › "+ch.Name)
			if err != nil {
				return n, notes, err
			}
			if addr == "" {
				continue
			}
			if err := checkURL(ch.Kind, addr); err != nil {
				notes = append(notes, fmt.Sprintf("notifications: the backup's address of the channel %q is not taken (%v); enter it again in Settings › Watson", ch.Name, err))
				continue
			}
			next[ch.ID] = addr
		}
		if !mapsEqual(urls, next) {
			raw, _ := json.Marshal(next)
			if err := s.secrets().SaveKeyIf(ctx, urlsKey, raw, version); err != nil {
				notes = append(notes, "notifications: the channels' addresses from the backup were not saved: "+err.Error()+"; enter them again in Settings › Watson")
			}
		}
		s.mu.Lock()
		err := changeJSON(ctx, s.opts.Backend, NotifyKey, func(x *NotifySettings) error {
			*x = set
			return nil
		})
		s.mu.Unlock()
		if err != nil {
			return n, notes, err
		}
		n++
	}
	return n, notes, nil
}

func ptrActor(a Actor) *Actor { return &a }
