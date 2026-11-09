package casework

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- Validation rules (Phase 12, ADR-138, amends ADR-127) ---
//
// The check ids the agents name their findings by are validation rules,
// listed in Cases › Mycroft › Validation rules. The built-in ones ship in
// code (package jobs' catalogues); what a SuperAdmin adds or changes is
// kept apart, in the casework ConfigMap (RulesKey), so an upgrade still
// adds the new built-ins without undoing an edit. The list as it stands
// is the built-ins with their stored changes, plus the rules added.
//
// An id never changes: there is no rename, and a rule in use is never
// deleted but retired (its agents are no longer offered it; the cases
// and exceptions keyed by it still match). Two rules are merged by
// replacing one by the other: the old id is kept as an alias, a finding
// under it routes by the new one, and the cases and exceptions keyed by
// either match findings of both. An id an agent made up (other.<slug>)
// is promoted to a rule as it is or under a clean id, the old one an
// alias. Every change is a SuperAdmin's and is audited (rule.*).
//
// The daily housekeeping suggests rules from the other.<slug> ids found
// in three runs or more, or on two clusters or more, likely variants
// grouped by their words; a person promotes, edits or dismisses each.
// Only an exact repeat (one id found in AutoPromoteRuns runs, with no
// variant) is promoted by itself, as it is; it is reported in the run,
// and undone by retiring it.
//
// Since CF-85 the page is one table: the rules, the ids found outside the
// list and the suggestions are rows alike, each with a drawer that lists
// what its counts stand for (its cases, the runs that found it, its
// latest findings with what became of each, its exceptions). What the
// runs found is counted once per run (FoundKey names the runs counted),
// so the runs kept are counted again without counting one twice: those
// from before the counter too. An agent that names an id of its own
// describes it (CF-86); a suggestion says why it was made, from facts,
// and is an Inbox row for a SuperAdmin (CF-87).

// The keys of the casework ConfigMap the rules keep.
const (
	// RulesKey holds the rules added, the changes to the built-in ones,
	// the suggested rules and the ids dismissed.
	RulesKey = "cases-rules.json"
	// FoundKey holds what the runs found under each id: how many
	// findings, runs and clusters, and for other.<slug> their titles.
	FoundKey = "cases-rules-found.json"
	// The most each holds by default.
	DefaultRulesBytes = 64 << 10
	DefaultFoundBytes = 128 << 10
)

// The audit log's actions of the rules.
const (
	ActionRuleAdd      = "rule.add"
	ActionRuleEdit     = "rule.edit"
	ActionRuleRetire   = "rule.retire"
	ActionRuleActivate = "rule.activate"
	ActionRuleReplace  = "rule.replace"
	ActionRuleDelete   = "rule.delete"
	ActionRulePromote  = "rule.promote"
	ActionRuleAuto     = "rule.auto-promote"
	ActionRuleDismiss  = "rule.dismiss"
)

// A rule's states.
const (
	RuleActive   = "active"
	RuleRetired  = "retired"
	RuleReplaced = "replaced"
)

// What the housekeeping looks for.
const (
	// SuggestRuns and SuggestClusters make an other.<slug> id a candidate:
	// found in that many runs, or on that many clusters.
	SuggestRuns     = 3
	SuggestClusters = 2
	// AutoPromoteRuns is how many runs an exact repeat is found in before
	// it is promoted by itself.
	AutoPromoteRuns = 5
	// variantLikeness is how alike two ids' words are (or their findings'
	// titles, titleLikeness) to be taken as variants of one problem.
	variantLikeness = 0.5
	titleLikeness   = 0.6
	// maxFound is how many ids FoundKey keeps; the least recently found
	// go first. maxTitles is how many titles it keeps of each.
	maxFound  = 160
	maxTitles = 3
	// maxSays is the longest meaning; maxControl the longest standard
	// control.
	maxSays    = 200
	maxControl = 60
	// What FoundKey keeps of each id for its drawer (CF-85, CF-86): the
	// runs that found it most recently, its latest findings, and the
	// agents' latest description per job; and how many runs it remembers
	// it counted, more than the runs kept (jobs.DefaultMaxRuns).
	maxRunIDs  = 12
	maxLatest  = 5
	maxWhys    = 3
	maxWhy     = 400
	maxCounted = 1000
	// foundBudget is what FoundKey is kept within, under its limit: the
	// least recently found ids' lists go first, then the ids.
	foundBudget = DefaultFoundBytes - 8<<10
	// recountSlack: a record from before the runs counted were named
	// counted every run that ended this long before its first count, or
	// later.
	recountSlack = 2 * time.Minute
	// maxListed is how many cases, runs and findings a row lists.
	maxListed = 50
)

// rulePattern is a rule's id: lower case words of letters, digits and
// dashes, joined by dots, at least two, at most 64 characters.
var rulePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// otherPattern is an id an agent made up: other.<slug>.
var otherPattern = regexp.MustCompile(`^other\.[a-z0-9]([a-z0-9-]{0,46}[a-z0-9])?$`)

// Rule is one validation rule as it stands.
type Rule struct {
	ID   string `json:"id"`
	Says string `json:"says"`
	// Type is the type of the case a finding of it opens (CF-37).
	Type string `json:"type"`
	// Jobs are the kinds of jobs whose agents are offered it.
	Jobs []string `json:"jobs"`
	// Control is a standard's control it stands for (a CIS or Pod
	// Security one), when a person said.
	Control string `json:"control,omitempty"`
	// BuiltIn ships in code; Edited is a built-in a person changed; Auto
	// was promoted by the housekeeping.
	BuiltIn bool `json:"builtIn"`
	Edited  bool `json:"edited,omitempty"`
	Auto    bool `json:"auto,omitempty"`
	// State is active, retired or replaced (by ReplacedBy).
	State      string    `json:"state"`
	ReplacedBy string    `json:"replacedBy,omitempty"`
	Updated    time.Time `json:"updated,omitzero"`
	By         string    `json:"by,omitempty"`
}

// storedRule is what RulesKey keeps of a rule: a built-in's changes (a
// field left empty keeps the built-in's), or a rule added.
type storedRule struct {
	Says       string    `json:"says,omitempty"`
	Type       string    `json:"type,omitempty"`
	Jobs       *[]string `json:"jobs,omitempty"`
	Control    string    `json:"control,omitempty"`
	Added      bool      `json:"added,omitempty"`
	Auto       bool      `json:"auto,omitempty"`
	Retired    bool      `json:"retired,omitempty"`
	ReplacedBy string    `json:"replacedBy,omitempty"`
	Updated    time.Time `json:"updated,omitzero"`
	By         string    `json:"by,omitempty"`
}

// Suggestion is a rule the housekeeping drafted from ids found outside
// the list, for a person to promote, edit or dismiss.
type Suggestion struct {
	// ID is the id suggested.
	ID   string   `json:"id"`
	Says string   `json:"says"`
	Type string   `json:"type"`
	Jobs []string `json:"jobs"`
	// From are the other.<slug> ids it groups, the most found first: the
	// first names the suggestion.
	From     []string  `json:"from"`
	Runs     int       `json:"runs"`
	Clusters int       `json:"clusters"`
	Created  time.Time `json:"created"`
	// Why says, from facts, why the housekeeping suggests it: how often
	// it was found, where and by which jobs, and why its ids were grouped
	// (CF-86); Descriptions are the agents' own, of each id it groups.
	Why          string        `json:"why,omitempty"`
	Descriptions []Description `json:"descriptions,omitempty"`
}

// Description is an agent's description of an id it made up (CF-86):
// what the id means and why no rule on the list fitted, the latest the
// job wrote. ID names the id described, in a suggestion's list.
type Description struct {
	ID   string    `json:"id,omitempty"`
	Job  string    `json:"job"`
	Kind string    `json:"kind,omitempty"`
	Text string    `json:"text"`
	Run  string    `json:"run"`
	At   time.Time `json:"at"`
}

// FoundFinding is one finding kept under its id, for the drawers
// (CF-85): its run, where it was, and what became of it: the case it
// opened or joined, the known issue, the exception, or ignored or
// expected.
type FoundFinding struct {
	Run        string    `json:"run"`
	ID         string    `json:"id,omitempty"`
	At         time.Time `json:"at"`
	Check      string    `json:"check,omitempty"`
	Cluster    string    `json:"cluster,omitempty"`
	Object     string    `json:"object,omitempty"`
	Title      string    `json:"title,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	Routing    string    `json:"routing,omitempty"`
	Case       string    `json:"case,omitempty"`
	KnownIssue string    `json:"knownIssue,omitempty"`
	Exception  string    `json:"exception,omitempty"`
}

// RuleCase is a case keyed by a row's id, for its drawer (CF-85).
type RuleCase struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"`
	Outcome  string `json:"outcome,omitempty"`
	Priority int    `json:"priority"`
	Open     bool   `json:"open"`
}

// rulesRecord is RulesKey.
type rulesRecord struct {
	Rules       map[string]*storedRule `json:"rules,omitempty"`
	Suggestions []Suggestion           `json:"suggestions,omitempty"`
	Dismissed   []string               `json:"dismissed,omitempty"`
}

// TitleCount is a finding's title and how often it came.
type TitleCount struct {
	Text string `json:"text"`
	N    int    `json:"n"`
}

// foundStat is what the runs found under one id: the counts, and for the
// drawer the runs that found it most recently and its latest findings
// (CF-85); for other.<slug> the titles, the objects' kinds and the
// agents' descriptions (CF-86) too.
type foundStat struct {
	Findings int            `json:"findings"`
	Runs     int            `json:"runs"`
	Clusters []string       `json:"clusters,omitempty"`
	Jobs     []string       `json:"jobs,omitempty"`
	Titles   []TitleCount   `json:"titles,omitempty"`
	Kinds    map[string]int `json:"kinds,omitempty"`
	First    time.Time      `json:"first"`
	Last     time.Time      `json:"last"`
	// RunIDs are the runs that found it most recently, oldest first;
	// Latest its latest findings, oldest first; Whys the latest
	// description each job wrote.
	RunIDs []string       `json:"runIds,omitempty"`
	Latest []FoundFinding `json:"latest,omitempty"`
	Whys   []Description  `json:"whys,omitempty"`
}

// foundRecord is FoundKey.
type foundRecord struct {
	IDs map[string]*foundStat `json:"ids,omitempty"`
	// Counted are the runs counted, so none is counted twice (CF-85);
	// Tracked says the runs kept were counted against them once, so
	// Counted alone says which were.
	Counted []string `json:"counted,omitempty"`
	Tracked bool     `json:"tracked,omitempty"`
}

// title is the title found most often.
func (f *foundStat) title() string {
	best := TitleCount{}
	for _, t := range f.Titles {
		if t.N > best.N {
			best = t
		}
	}
	return best.Text
}

// ---------- the list as it stands ----------

// ruleSet is the list as it stands, read once: it is jobs.Rules.
type ruleSet struct {
	list []Rule
	byID map[string]int
	// builtin are the kinds whose catalogue holds each built-in id.
	builtin map[string][]string
}

// mergeRules makes the list: the built-ins in their catalogues' order
// with their stored changes, then the rules added, by id.
func mergeRules(rec rulesRecord) *ruleSet {
	rs := &ruleSet{byID: map[string]int{}, builtin: map[string][]string{}}
	for _, k := range jobs.BuiltinRules {
		for _, c := range k.Checks {
			rs.builtin[c.ID] = append(rs.builtin[c.ID], k.Kind)
			if i, ok := rs.byID[c.ID]; ok {
				rs.list[i].Jobs = append(rs.list[i].Jobs, k.Kind)
				continue
			}
			rs.byID[c.ID] = len(rs.list)
			rs.list = append(rs.list, Rule{ID: c.ID, Says: c.Says, Type: c.Type, Jobs: []string{k.Kind}, BuiltIn: true, State: RuleActive})
		}
	}
	var added []string
	for id, st := range rec.Rules {
		if st == nil {
			continue
		}
		i, ok := rs.byID[id]
		if !ok {
			// A change to a built-in a later version no longer ships is
			// left out; a rule added stays.
			if st.Added {
				added = append(added, id)
			}
			continue
		}
		r := &rs.list[i]
		if st.Says != "" && st.Says != r.Says {
			r.Says, r.Edited = st.Says, true
		}
		if st.Type != "" && st.Type != r.Type {
			r.Type, r.Edited = st.Type, true
		}
		if st.Jobs != nil && !slices.Equal(*st.Jobs, r.Jobs) {
			r.Jobs, r.Edited = slices.Clone(*st.Jobs), true
		}
		if st.Control != "" {
			r.Control, r.Edited = st.Control, true
		}
		r.State, r.ReplacedBy = stateOf(st), st.ReplacedBy
		r.Updated, r.By = st.Updated, st.By
	}
	sort.Strings(added)
	for _, id := range added {
		st := rec.Rules[id]
		r := Rule{ID: id, Says: st.Says, Type: firstOf(st.Type, issues.TypeOther), Control: st.Control, Auto: st.Auto, State: stateOf(st), ReplacedBy: st.ReplacedBy,
			Updated: st.Updated, By: st.By}
		if st.Jobs != nil {
			r.Jobs = slices.Clone(*st.Jobs)
		}
		rs.byID[id] = len(rs.list)
		rs.list = append(rs.list, r)
	}
	for i := range rs.list {
		if rs.list[i].Jobs == nil {
			rs.list[i].Jobs = []string{}
		}
	}
	return rs
}

func stateOf(st *storedRule) string {
	switch {
	case st.ReplacedBy != "":
		return RuleReplaced
	case st.Retired:
		return RuleRetired
	}
	return RuleActive
}

// get is a rule of the list.
func (rs *ruleSet) get(id string) (Rule, bool) {
	i, ok := rs.byID[id]
	if !ok {
		return Rule{}, false
	}
	return rs.list[i], true
}

// Canon is the id a check id routes by: the rule that replaced it, at
// the end of the chain; itself otherwise.
func (rs *ruleSet) Canon(id string) string {
	cur := id
	for hops := 0; hops < 20; hops++ {
		r, ok := rs.get(cur)
		if !ok || r.ReplacedBy == "" || r.ReplacedBy == id {
			return cur
		}
		cur = r.ReplacedBy
	}
	return cur
}

// Offered are a kind's active rules: what its agent is told.
func (rs *ruleSet) Offered(k jobs.Kind) []jobs.Check {
	var out []jobs.Check
	for _, r := range rs.list {
		if r.State == RuleActive && slices.Contains(r.Jobs, k.ID) {
			out = append(out, jobs.Check{ID: r.ID, Says: r.Says, Type: r.Type})
		}
	}
	return out
}

// Known says whether a check id is one of a kind's rules: offered to it,
// one of its built-ins, or no longer active (retired or replaced), so
// what is keyed by it still matches.
func (rs *ruleSet) Known(k jobs.Kind, id string) bool {
	r, ok := rs.get(id)
	if !ok {
		return false
	}
	return slices.Contains(r.Jobs, k.ID) || slices.Contains(rs.builtin[id], k.ID) || r.State != RuleActive
}

// Type is the type of the case a finding of a check id opens: its rule's
// (the one that replaced it), other for an id not on the list.
func (rs *ruleSet) Type(id string) string {
	if r, ok := rs.get(rs.Canon(id)); ok && r.Type != "" {
		return r.Type
	}
	return issues.TypeOther
}

// Meaning is what a check id means: its rule's words, or those of the
// rule that replaced it when it has none; "" for an id not on the list.
func (rs *ruleSet) Meaning(id string) string {
	if r, ok := rs.get(id); ok && r.Says != "" {
		return r.Says
	}
	if r, ok := rs.get(rs.Canon(id)); ok {
		return r.Says
	}
	return ""
}

var _ jobs.Rules = (*ruleSet)(nil)

// ---------- the keeper ----------

// Rules keeps the validation rules: the list as it stands, the changes
// people make, what the runs found and the suggestions.
type Rules struct {
	s     *Service
	mu    sync.Mutex
	cache *ruleSet
	at    time.Time
	// recounted says this replica, as the leader, counted the runs kept
	// once (CF-85).
	recounted bool
}

// rulesFresh is how long the list read is used before it is read again.
const rulesFresh = 15 * time.Second

// Rules keeps the validation rules (ADR-138).
func (s *Service) Rules() *Rules { return s.rules }

// load reads the list as it stands, and the record it came from.
func (r *Rules) load(ctx context.Context) (*ruleSet, rulesRecord, error) {
	var rec rulesRecord
	_, err := loadJSON(ctx, r.s.o.Backend, RulesKey, &rec)
	return mergeRules(rec), rec, err
}

// set is the list as it stands, read at most every rulesFresh; the
// built-ins alone when it cannot be read.
func (r *Rules) set(ctx context.Context) *ruleSet {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.cache != nil && now.Sub(r.at) < rulesFresh {
		return r.cache
	}
	rs, _, err := r.load(ctx)
	if err != nil {
		r.s.o.Logger.Warn("the validation rules could not be read: the built-in ones are used", "err", err)
		if r.cache != nil {
			return r.cache
		}
		return rs
	}
	r.cache, r.at = rs, now
	return rs
}

// forget drops the list read, after a change.
func (r *Rules) forget() {
	r.mu.Lock()
	r.cache = nil
	r.mu.Unlock()
}

// Book is the list as it stands for package jobs: what the agents are
// offered and the findings are checked against (jobs.SetRules).
func (r *Rules) Book(ctx context.Context) jobs.Rules { return r.set(ctx) }

// Meaning is what a check id means on the list as it stands: its rule's
// words (the rule that replaced it, when it has none); "" for an id not
// on the list. For the places that show a rule beside its id.
func (r *Rules) Meaning(id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.set(ctx).Meaning(id)
}

// update changes RulesKey and drops the list read.
func (r *Rules) update(ctx context.Context, f func(rs *ruleSet, rec *rulesRecord) error) error {
	defer r.forget()
	return update(ctx, r.s.o.Backend, RulesKey, func(rec *rulesRecord) error {
		if rec.Rules == nil {
			rec.Rules = map[string]*storedRule{}
		}
		return f(mergeRules(*rec), rec)
	})
}

// ---------- what is in use ----------

// usage is what the cases, known issues, exceptions and runs hold of
// each id, as found (not resolved through the aliases).
type usage struct {
	cases, open, exceptions map[string]int
	types                   map[string]map[string]int
	used                    map[string]bool
	found                   map[string]*foundStat
	// caseList and exceptionIDs are the cases and the active exceptions
	// behind the counts, for the drawers (CF-85).
	caseList     map[string][]RuleCase
	exceptionIDs map[string][]string
}

func (r *Rules) usage(ctx context.Context) usage {
	u := usage{cases: map[string]int{}, open: map[string]int{}, exceptions: map[string]int{}, types: map[string]map[string]int{}, used: map[string]bool{},
		caseList: map[string][]RuleCase{}, exceptionIDs: map[string][]string{}}
	s := r.s
	if lines, err := s.o.Issues.Index(ctx); err == nil {
		for _, l := range lines {
			if l.Key == nil || l.Key.Check == "" {
				continue
			}
			c := l.Key.Check
			u.used[c] = true
			u.cases[c]++
			if l.Open() {
				u.open[c]++
			}
			u.caseList[c] = append(u.caseList[c], RuleCase{ID: caseID(l.Number), Title: l.Title, Status: string(l.Status), Outcome: string(l.Outcome), Priority: l.Priority, Open: l.Open()})
			if u.types[c] == nil {
				u.types[c] = map[string]int{}
			}
			u.types[c][firstOf(l.Type, issues.TypeOther)]++
		}
	}
	if known, err := s.o.Issues.Known(ctx, system, true); err == nil {
		for _, k := range known {
			for _, x := range k.Keys {
				u.used[x.Check] = true
			}
		}
	}
	if list, err := s.o.Lessons.Exceptions(ctx); err == nil {
		now := s.now()
		for _, e := range list {
			u.used[e.Check] = true
			if e.Active(now) {
				u.exceptions[e.Check]++
				u.exceptionIDs[e.Check] = append(u.exceptionIDs[e.Check], e.ID)
			}
		}
	}
	var seen checksRecord
	if _, err := loadJSON(ctx, s.o.Backend, ChecksKey, &seen); err == nil {
		for _, list := range seen.Clusters {
			for _, c := range list {
				u.used[c] = true
			}
		}
	}
	var found foundRecord
	if _, err := loadJSON(ctx, s.o.Backend, FoundKey, &found); err == nil {
		u.found = found.IDs
	}
	if u.found == nil {
		u.found = map[string]*foundStat{}
	}
	for id := range u.found {
		u.used[id] = true
	}
	return u
}

// mostType is the type the cases of some ids have most often; other for
// none.
func (u usage) mostType(ids ...string) string {
	n := map[string]int{}
	for _, id := range ids {
		for t, c := range u.types[id] {
			n[t] += c
		}
	}
	best, most := issues.TypeOther, 0
	for _, t := range issues.CaseTypes {
		if n[t] > most {
			best, most = t, n[t]
		}
	}
	return best
}

// ---------- reading ----------

// Behind is what a row's counts stand for, listed for its drawer
// (CF-85): its cases with their status (open first, the newest first),
// the runs that found it and its latest findings with what became of
// each (the newest first), its active exceptions; for an id an agent
// made up, its findings' titles, the kinds of the objects they are on,
// and the agents' latest description per job (CF-86).
type Behind struct {
	CaseList     []RuleCase     `json:"caseList"`
	RunIDs       []string       `json:"runIds"`
	Latest       []FoundFinding `json:"latest"`
	ExceptionIDs []string       `json:"exceptionIds"`
	Titles       []TitleCount   `json:"titles,omitempty"`
	Kinds        map[string]int `json:"kinds,omitempty"`
	Descriptions []Description  `json:"descriptions,omitempty"`
}

// RuleView is a rule with what is keyed by it: its own and its aliases'.
type RuleView struct {
	Rule
	// Aliases are the ids it replaced.
	Aliases    []string   `json:"aliases,omitempty"`
	OpenCases  int        `json:"openCases"`
	Cases      int        `json:"cases"`
	Exceptions int        `json:"exceptions"`
	Findings   int        `json:"findings"`
	LastFound  *time.Time `json:"lastFound,omitempty"`
	// Runs and Clusters are how many runs found it, and on which clusters
	// (CF-85); FirstFound when it was first.
	Runs       int        `json:"runs"`
	Clusters   []string   `json:"clusters"`
	FirstFound *time.Time `json:"firstFound,omitempty"`
	// Used says a case, known issue, exception or run holds it: it is
	// never deleted then, only retired.
	Used bool `json:"used"`
	Behind
}

// FoundID is an other.<slug> id the runs found that is not on the list.
type FoundID struct {
	ID         string   `json:"id"`
	Findings   int      `json:"findings"`
	Runs       int      `json:"runs"`
	Clusters   []string `json:"clusters"`
	Jobs       []string `json:"jobs"`
	Cases      int      `json:"cases"`
	OpenCases  int      `json:"openCases"`
	Exceptions int      `json:"exceptions"`
	// Title is its findings' most frequent title; Type its cases' most
	// frequent type.
	Title string     `json:"title,omitempty"`
	Type  string     `json:"type"`
	First *time.Time `json:"first,omitempty"`
	Last  *time.Time `json:"last,omitempty"`
	// Suggestion is the suggested rule that groups it, by its id;
	// Dismissed says a person dismissed it: it is not suggested again.
	Suggestion string `json:"suggestion,omitempty"`
	Dismissed  bool   `json:"dismissed,omitempty"`
	Behind
}

// RuleJob is a kind of job with a catalogue, as the page offers it.
type RuleJob struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// RulesView is Cases › Mycroft › Validation rules: the rules, the ids
// found outside the list and the suggestions, which the page shows as
// rows of one table (CF-85).
type RulesView struct {
	Rules       []RuleView   `json:"rules"`
	Found       []FoundID    `json:"found"`
	Suggestions []Suggestion `json:"suggestions"`
	Jobs        []RuleJob    `json:"jobs"`
	Types       []string     `json:"types"`
	// ClusterNames are the clusters' names by id (Settings › Clusters):
	// the rows and findings name a cluster by its id, the page shows its
	// name, as cases do (CF-60).
	ClusterNames map[string]string `json:"clusterNames"`
	// MayChange says the person is a SuperAdmin: edits the rules.
	MayChange bool `json:"mayChange"`
}

func ruleJobs() []RuleJob {
	out := make([]RuleJob, 0, len(jobs.BuiltinRules))
	for _, k := range jobs.BuiltinRules {
		out = append(out, RuleJob{ID: k.Kind, Label: k.Label})
	}
	return out
}

// jobLabel is a kind of job in words: "Nightly audit".
func jobLabel(kind string) string {
	for _, k := range jobs.BuiltinRules {
		if k.Kind == kind {
			return k.Label
		}
	}
	return kind
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// runNumber is a run's number: run-12 is 12.
func runNumber(id string) int {
	n := 0
	for _, c := range strings.TrimPrefix(id, "run-") {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// add adds what an id holds to a row's lists; tidy orders them and keeps
// each within maxListed.
func (b *Behind) add(u usage, id string) {
	b.CaseList = append(b.CaseList, u.caseList[id]...)
	for _, e := range u.exceptionIDs[id] {
		if !slices.Contains(b.ExceptionIDs, e) {
			b.ExceptionIDs = append(b.ExceptionIDs, e)
		}
	}
	st := u.found[id]
	if st == nil {
		return
	}
	for _, run := range st.RunIDs {
		if !slices.Contains(b.RunIDs, run) {
			b.RunIDs = append(b.RunIDs, run)
		}
	}
	b.Latest = append(b.Latest, st.Latest...)
	for _, t := range st.Titles {
		if i := slices.IndexFunc(b.Titles, func(x TitleCount) bool { return x.Text == t.Text }); i >= 0 {
			b.Titles[i].N += t.N
		} else {
			b.Titles = append(b.Titles, t)
		}
	}
	for k, n := range st.Kinds {
		if b.Kinds == nil {
			b.Kinds = map[string]int{}
		}
		b.Kinds[k] += n
	}
	b.Descriptions = append(b.Descriptions, st.Whys...)
}

func (b *Behind) tidy() {
	slices.SortStableFunc(b.CaseList, func(x, y RuleCase) int {
		if x.Open != y.Open {
			if x.Open {
				return -1
			}
			return 1
		}
		return number0(y.ID) - number0(x.ID)
	})
	slices.SortFunc(b.RunIDs, func(x, y string) int { return runNumber(y) - runNumber(x) })
	slices.SortStableFunc(b.Latest, func(x, y FoundFinding) int { return y.At.Compare(x.At) })
	slices.SortStableFunc(b.Titles, func(x, y TitleCount) int { return y.N - x.N })
	slices.SortStableFunc(b.Descriptions, func(x, y Description) int { return y.At.Compare(x.At) })
	b.CaseList = first50(b.CaseList)
	b.RunIDs = first50(b.RunIDs)
	b.Latest = first50(b.Latest)
	if b.CaseList == nil {
		b.CaseList = []RuleCase{}
	}
	if b.RunIDs == nil {
		b.RunIDs = []string{}
	}
	if b.Latest == nil {
		b.Latest = []FoundFinding{}
	}
	if b.ExceptionIDs == nil {
		b.ExceptionIDs = []string{}
	}
}

func first50[T any](list []T) []T {
	if len(list) > maxListed {
		return list[:maxListed]
	}
	return list
}

// number0 is a case's number, 0 for none.
func number0(id string) int {
	n, _ := number(id)
	return n
}

// View is the page: every rule with its counts, the ids found outside the
// list, and the suggestions, each with what its counts stand for.
func (r *Rules) View(ctx context.Context, c Caller) (RulesView, error) {
	if !c.Approver && !c.Read && !c.Admin {
		return RulesView{}, fmt.Errorf("%w: the validation rules are for the people who work the cases", ErrDenied)
	}
	rs, rec, err := r.load(ctx)
	if err != nil {
		return RulesView{}, err
	}
	u := r.usage(ctx)
	out := RulesView{Rules: []RuleView{}, Found: []FoundID{}, Suggestions: []Suggestion{}, Jobs: ruleJobs(), Types: issues.CaseTypes, ClusterNames: map[string]string{}, MayChange: c.Admin}
	for _, cl := range r.s.ClusterList() {
		out.ClusterNames[cl.ID] = firstOf(cl.Name, cl.ID)
	}
	// Each id's counts go to its own row, and to the row of the rule it
	// routes by: a merged rule shows what its aliases hold too.
	idx := map[string]int{}
	for _, rule := range rs.list {
		idx[rule.ID] = len(out.Rules)
		out.Rules = append(out.Rules, RuleView{Rule: rule, Used: u.used[rule.ID], Clusters: []string{}})
	}
	add := func(v *RuleView, id string) {
		v.OpenCases += u.open[id]
		v.Cases += u.cases[id]
		v.Exceptions += u.exceptions[id]
		if f := u.found[id]; f != nil {
			v.Findings += f.Findings
			v.Runs += f.Runs
			for _, c := range f.Clusters {
				if !slices.Contains(v.Clusters, c) {
					v.Clusters = append(v.Clusters, c)
				}
			}
			if v.LastFound == nil || f.Last.After(*v.LastFound) {
				v.LastFound = timePtr(f.Last)
			}
			if !f.First.IsZero() && (v.FirstFound == nil || f.First.Before(*v.FirstFound)) {
				v.FirstFound = timePtr(f.First)
			}
		}
		v.Behind.add(u, id)
	}
	for _, rule := range rs.list {
		add(&out.Rules[idx[rule.ID]], rule.ID)
		if canon := rs.Canon(rule.ID); canon != rule.ID {
			if i, ok := idx[canon]; ok {
				out.Rules[i].Aliases = append(out.Rules[i].Aliases, rule.ID)
				add(&out.Rules[i], rule.ID)
				// What it replaced keeps it in use.
				out.Rules[i].Used = true
			}
		}
	}
	for i := range out.Rules {
		out.Rules[i].Behind.tidy()
	}
	out.Found = r.foundOutside(rs, u, rec)
	out.Suggestions = append(out.Suggestions, rec.Suggestions...)
	return out, nil
}

// foundOutside are the other.<slug> ids found that are not on the list:
// in the runs since the rules were kept, and on the cases and exceptions
// from before; the most recently found first.
func (r *Rules) foundOutside(rs *ruleSet, u usage, rec rulesRecord) []FoundID {
	ids := map[string]bool{}
	for id := range u.found {
		ids[id] = true
	}
	for id := range u.cases {
		ids[id] = true
	}
	for id := range u.exceptions {
		ids[id] = true
	}
	out := []FoundID{}
	for id := range ids {
		if _, on := rs.get(id); on || !otherPattern.MatchString(id) {
			continue
		}
		f := FoundID{ID: id, Clusters: []string{}, Jobs: []string{}, Cases: u.cases[id], OpenCases: u.open[id], Exceptions: u.exceptions[id], Type: u.mostType(id),
			Dismissed: slices.Contains(rec.Dismissed, id)}
		if st := u.found[id]; st != nil {
			f.Findings, f.Runs, f.Title, f.First, f.Last = st.Findings, st.Runs, st.title(), timePtr(st.First), timePtr(st.Last)
			f.Clusters = append(f.Clusters, st.Clusters...)
			f.Jobs = append(f.Jobs, st.Jobs...)
		}
		for _, s := range rec.Suggestions {
			if slices.Contains(s.From, id) {
				f.Suggestion = s.ID
			}
		}
		f.Behind.add(u, id)
		f.Behind.tidy()
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Last, out[j].Last
		switch {
		case a != nil && b != nil && !a.Equal(*b):
			return a.After(*b)
		case (a == nil) != (b == nil):
			return a != nil
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Suggestions are the suggested rules, for the Inbox of a SuperAdmin, who
// decides them (CF-87): anyone else is refused, and sees none.
func (r *Rules) Suggestions(ctx context.Context, c Caller) ([]Suggestion, error) {
	if err := admin(c); err != nil {
		return nil, err
	}
	_, rec, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	return append([]Suggestion{}, rec.Suggestions...), nil
}

// ---------- changes, a SuperAdmin's ----------

// RuleInput is a rule added.
type RuleInput struct {
	ID      string   `json:"id"`
	Says    string   `json:"says"`
	Type    string   `json:"type"`
	Jobs    []string `json:"jobs"`
	Control string   `json:"control,omitempty"`
}

// RulePatch changes a rule: what is left out stays. Its id never
// changes.
type RulePatch struct {
	Says    *string   `json:"says,omitempty"`
	Type    *string   `json:"type,omitempty"`
	Jobs    *[]string `json:"jobs,omitempty"`
	Control *string   `json:"control,omitempty"`
}

// PromoteInput promotes other.<slug> ids to a rule: kept as the first
// (ID empty or one of From), or under a clean id, the others aliases of
// it. A rule already on the list takes them as its aliases.
type PromoteInput struct {
	From    []string `json:"from"`
	ID      string   `json:"id,omitempty"`
	Says    string   `json:"says"`
	Type    string   `json:"type"`
	Jobs    []string `json:"jobs"`
	Control string   `json:"control,omitempty"`
}

func admin(c Caller) error {
	if !c.Admin {
		return fmt.Errorf("%w: only a SuperAdmin changes the validation rules: they change what the agents are told", ErrDenied)
	}
	return nil
}

// fields checks a rule's words, type, jobs and control, and gives them
// tidy: the jobs in the catalogues' order.
func ruleFields(says, typ string, jobList []string, control string) (string, string, []string, string, error) {
	says = clip(says, maxSays)
	if says == "" {
		return "", "", nil, "", invalidf("say what the rule means")
	}
	if !issues.ValidType(typ) {
		return "", "", nil, "", invalidf("a rule's case type is one of %s", strings.Join(issues.CaseTypes, ", "))
	}
	var out []string
	for _, k := range jobs.BuiltinRules {
		if slices.Contains(jobList, k.Kind) {
			out = append(out, k.Kind)
		}
	}
	for _, j := range jobList {
		if !slices.ContainsFunc(jobs.BuiltinRules, func(k jobs.KindRules) bool { return k.Kind == j }) {
			return "", "", nil, "", invalidf("%q is not a job with validation rules", clip(j, 60))
		}
	}
	if len(out) == 0 {
		return "", "", nil, "", invalidf("pick at least one job to use the rule; retire it instead to offer it to none")
	}
	control = clip(control, maxControl)
	return says, typ, out, control, nil
}

func validRuleID(id string) error {
	if len(id) > 64 || !rulePattern.MatchString(id) {
		return invalidf("a rule's id is lower case words of letters, digits and dashes joined by dots, such as pod.no-limits, at most 64 characters; not %q", clip(id, 80))
	}
	return nil
}

func (r *Rules) audit(c Caller, action, target, detail string) {
	r.s.record(audit.Event{Action: action, Who: who(c.Person), Target: target, Outcome: audit.OutcomeOK, Detail: clip(detail, 1000)})
}

// Add adds a rule.
func (r *Rules) Add(ctx context.Context, in RuleInput, c Caller) (RulesView, error) {
	if err := admin(c); err != nil {
		return RulesView{}, err
	}
	id := strings.TrimSpace(in.ID)
	if err := validRuleID(id); err != nil {
		return RulesView{}, err
	}
	says, typ, jobList, control, err := ruleFields(in.Says, in.Type, in.Jobs, in.Control)
	if err != nil {
		return RulesView{}, err
	}
	now := r.s.now()
	err = r.update(ctx, func(rs *ruleSet, rec *rulesRecord) error {
		if _, ok := rs.get(id); ok {
			return invalidf("%s is on the list already", id)
		}
		rec.Rules[id] = &storedRule{Added: true, Says: says, Type: typ, Jobs: &jobList, Control: control, Updated: now, By: c.Label()}
		dropSuggested(rec, id)
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	r.audit(c, ActionRuleAdd, id, fmt.Sprintf("validation rule %s added: %s; case type %s; jobs %s", id, says, typ, strings.Join(jobList, ", ")))
	return r.View(ctx, c)
}

// Edit changes a rule's words, case type, jobs or control. A built-in's
// value set back to the shipped one drops the change.
func (r *Rules) Edit(ctx context.Context, id string, p RulePatch, c Caller) (RulesView, error) {
	if err := admin(c); err != nil {
		return RulesView{}, err
	}
	now := r.s.now()
	var detail []string
	err := r.update(ctx, func(rs *ruleSet, rec *rulesRecord) error {
		detail = nil
		cur, ok := rs.get(id)
		if !ok {
			return fmt.Errorf("%w: there is no validation rule %s", ErrNotFound, id)
		}
		says, typ, jobList, control := cur.Says, cur.Type, cur.Jobs, cur.Control
		if p.Says != nil {
			says = *p.Says
		}
		if p.Type != nil {
			typ = *p.Type
		}
		if p.Jobs != nil {
			jobList = *p.Jobs
		}
		if p.Control != nil {
			control = *p.Control
		}
		says, typ, jobList, control, err := ruleFields(says, typ, jobList, control)
		if err != nil {
			return err
		}
		if says != cur.Says {
			detail = append(detail, "meaning: "+says)
		}
		if typ != cur.Type {
			detail = append(detail, "case type: "+typ)
		}
		if !slices.Equal(jobList, cur.Jobs) {
			detail = append(detail, "jobs: "+strings.Join(jobList, ", "))
		}
		if control != cur.Control {
			detail = append(detail, "control: "+orNone(control))
		}
		if len(detail) == 0 {
			return errUnchanged
		}
		st := rec.Rules[id]
		if st == nil {
			st = &storedRule{}
			rec.Rules[id] = st
		}
		st.Says, st.Type, st.Jobs, st.Control = says, typ, &jobList, control
		if cur.BuiltIn {
			// The shipped values are kept as no change, so a later
			// version's wording of them comes through.
			b, _ := mergeRules(rulesRecord{}).get(id)
			if st.Says == b.Says {
				st.Says = ""
			}
			if st.Type == b.Type {
				st.Type = ""
			}
			if slices.Equal(jobList, b.Jobs) {
				st.Jobs = nil
			}
		}
		st.Updated, st.By = now, c.Label()
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	if len(detail) > 0 {
		r.audit(c, ActionRuleEdit, id, fmt.Sprintf("validation rule %s changed: %s", id, strings.Join(detail, "; ")))
	}
	return r.View(ctx, c)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// state changes a rule's state, by f on its stored record.
func (r *Rules) state(ctx context.Context, id string, c Caller, f func(rs *ruleSet, cur Rule, st *storedRule) error) error {
	if err := admin(c); err != nil {
		return err
	}
	now := r.s.now()
	return r.update(ctx, func(rs *ruleSet, rec *rulesRecord) error {
		cur, ok := rs.get(id)
		if !ok {
			return fmt.Errorf("%w: there is no validation rule %s", ErrNotFound, id)
		}
		st := rec.Rules[id]
		if st == nil {
			st = &storedRule{}
			rec.Rules[id] = st
		}
		if err := f(rs, cur, st); err != nil {
			return err
		}
		st.Updated, st.By = now, c.Label()
		return nil
	})
}

// Retire stops offering a rule to the agents; the cases and exceptions
// keyed by it still match.
func (r *Rules) Retire(ctx context.Context, id string, c Caller) (RulesView, error) {
	err := r.state(ctx, id, c, func(_ *ruleSet, cur Rule, st *storedRule) error {
		switch cur.State {
		case RuleRetired:
			return fmt.Errorf("%w: %s is retired already", ErrState, id)
		case RuleReplaced:
			return fmt.Errorf("%w: %s is replaced by %s; make it active first", ErrState, id, cur.ReplacedBy)
		}
		st.Retired = true
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	r.audit(c, ActionRuleRetire, id, fmt.Sprintf("validation rule %s retired: the agents are no longer offered it; what is keyed by it still matches", id))
	return r.View(ctx, c)
}

// Activate makes a retired or replaced rule active again: offered to its
// agents, and routing by itself.
func (r *Rules) Activate(ctx context.Context, id string, c Caller) (RulesView, error) {
	was := ""
	err := r.state(ctx, id, c, func(_ *ruleSet, cur Rule, st *storedRule) error {
		if cur.State == RuleActive {
			return fmt.Errorf("%w: %s is active already", ErrState, id)
		}
		was = cur.State
		if cur.ReplacedBy != "" {
			was += " by " + cur.ReplacedBy
		}
		st.Retired, st.ReplacedBy = false, ""
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	r.audit(c, ActionRuleActivate, id, fmt.Sprintf("validation rule %s made active again (it was %s)", id, was))
	return r.View(ctx, c)
}

// Replace merges a rule into another, active one: its id is kept as an
// alias, findings under it route by the other, and what is keyed by
// either matches both.
func (r *Rules) Replace(ctx context.Context, id, by string, c Caller) (RulesView, error) {
	by = strings.TrimSpace(by)
	err := r.state(ctx, id, c, func(rs *ruleSet, cur Rule, st *storedRule) error {
		if cur.State == RuleReplaced {
			return fmt.Errorf("%w: %s is replaced by %s already", ErrState, id, cur.ReplacedBy)
		}
		target, ok := rs.get(by)
		switch {
		case by == id:
			return invalidf("a rule cannot replace itself")
		case !ok:
			return fmt.Errorf("%w: there is no validation rule %s", ErrNotFound, by)
		case target.State != RuleActive:
			return invalidf("%s is %s: a rule is replaced by an active one", by, target.State)
		}
		st.ReplacedBy, st.Retired = by, false
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	r.audit(c, ActionRuleReplace, id, fmt.Sprintf("validation rule %s replaced by %s: its findings route by %s, and the cases and exceptions keyed by either match both", id, by, by))
	return r.View(ctx, c)
}

// Delete drops a rule added that nothing holds: a rule in use, or a
// built-in, is retired instead.
func (r *Rules) Delete(ctx context.Context, id string, c Caller) (RulesView, error) {
	if err := admin(c); err != nil {
		return RulesView{}, err
	}
	u := r.usage(ctx)
	err := r.update(ctx, func(rs *ruleSet, rec *rulesRecord) error {
		cur, ok := rs.get(id)
		switch {
		case !ok:
			return fmt.Errorf("%w: there is no validation rule %s", ErrNotFound, id)
		case cur.BuiltIn:
			return fmt.Errorf("%w: %s is built in: retire it instead", ErrState, id)
		case u.used[id]:
			return fmt.Errorf("%w: %s is in use (cases, exceptions or runs hold it): retire it instead", ErrState, id)
		}
		for _, x := range rs.list {
			if x.ReplacedBy == id {
				return fmt.Errorf("%w: %s replaced %s: retire it instead", ErrState, id, x.ID)
			}
		}
		delete(rec.Rules, id)
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	r.audit(c, ActionRuleDelete, id, fmt.Sprintf("validation rule %s deleted: nothing held it", id))
	return r.View(ctx, c)
}

// Promote makes other.<slug> ids a rule (see PromoteInput).
func (r *Rules) Promote(ctx context.Context, in PromoteInput, c Caller) (RulesView, error) {
	if err := admin(c); err != nil {
		return RulesView{}, err
	}
	detail, err := r.promote(ctx, in, c.Label(), false)
	if err != nil {
		return RulesView{}, err
	}
	r.audit(c, ActionRulePromote, firstOf(strings.TrimSpace(in.ID), first(in.From)), detail)
	return r.View(ctx, c)
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// promote promotes ids to a rule and says what it did.
func (r *Rules) promote(ctx context.Context, in PromoteInput, by string, auto bool) (string, error) {
	var from []string
	for _, id := range in.From {
		id = strings.TrimSpace(id)
		if !otherPattern.MatchString(id) {
			return "", invalidf("only an id an agent made up, other.<slug>, is promoted; not %q", clip(id, 80))
		}
		if !slices.Contains(from, id) {
			from = append(from, id)
		}
	}
	if len(from) == 0 || len(from) > 20 {
		return "", invalidf("promote one id to twenty")
	}
	target := strings.TrimSpace(in.ID)
	if target == "" {
		target = from[0]
	}
	if err := validRuleID(target); err != nil {
		return "", err
	}
	now := r.s.now()
	var detail string
	err := r.update(ctx, func(rs *ruleSet, rec *rulesRecord) error {
		for _, id := range from {
			if _, on := rs.get(id); on {
				return fmt.Errorf("%w: %s is on the list already", ErrState, id)
			}
		}
		existing, exists := rs.get(target)
		var aliases []string
		if exists {
			// Joined to a rule on the list: its words, type and jobs stay.
			if existing.State != RuleActive {
				return invalidf("%s is %s: promote to an active rule", target, existing.State)
			}
			for _, id := range from {
				jobList := slices.Clone(existing.Jobs)
				rec.Rules[id] = &storedRule{Added: true, Says: existing.Says, Type: existing.Type, Jobs: &jobList, ReplacedBy: target, Updated: now, By: by}
				aliases = append(aliases, id)
			}
			detail = fmt.Sprintf("%s joined to the validation rule %s as aliases: their findings route by %s", strings.Join(aliases, ", "), target, target)
		} else {
			says, typ, jobList, control, err := ruleFields(in.Says, in.Type, in.Jobs, in.Control)
			if err != nil {
				return err
			}
			rec.Rules[target] = &storedRule{Added: true, Auto: auto, Says: says, Type: typ, Jobs: &jobList, Control: control, Updated: now, By: by}
			for _, id := range from {
				if id == target {
					continue
				}
				j := slices.Clone(jobList)
				rec.Rules[id] = &storedRule{Added: true, Says: says, Type: typ, Jobs: &j, ReplacedBy: target, Updated: now, By: by}
				aliases = append(aliases, id)
			}
			detail = fmt.Sprintf("validation rule %s promoted from %s: %s; case type %s; jobs %s", target, strings.Join(from, ", "), says, typ, strings.Join(jobList, ", "))
			if len(aliases) > 0 {
				detail += fmt.Sprintf("; %s kept as aliases of it", strings.Join(aliases, ", "))
			}
		}
		for _, id := range from {
			dropSuggested(rec, id)
		}
		dropSuggested(rec, target)
		return nil
	})
	return detail, err
}

// dropSuggested drops the suggestions that hold an id.
func dropSuggested(rec *rulesRecord, id string) {
	rec.Suggestions = slices.DeleteFunc(rec.Suggestions, func(s Suggestion) bool { return s.ID == id || slices.Contains(s.From, id) })
}

// Dismiss drops a suggestion, named by its first id: its ids are not
// suggested again, nor promoted by themselves.
func (r *Rules) Dismiss(ctx context.Context, id string, c Caller) (RulesView, error) {
	if err := admin(c); err != nil {
		return RulesView{}, err
	}
	var gone Suggestion
	alone := false
	u := r.usage(ctx)
	err := r.update(ctx, func(rs *ruleSet, rec *rulesRecord) error {
		alone = false
		i := slices.IndexFunc(rec.Suggestions, func(s Suggestion) bool { return first(s.From) == id || s.ID == id })
		if i < 0 {
			// An id found outside the list and in no suggestion is
			// dismissed alone (CF-85): it is not suggested again.
			_, on := rs.get(id)
			_, found := u.found[id]
			if on || !found && u.cases[id] == 0 || !otherPattern.MatchString(id) {
				return fmt.Errorf("%w: there is no suggested rule %s", ErrNotFound, id)
			}
			if slices.Contains(rec.Dismissed, id) {
				return fmt.Errorf("%w: %s is dismissed already", ErrState, id)
			}
			alone = true
			rec.Dismissed = append(rec.Dismissed, id)
			if len(rec.Dismissed) > 500 {
				rec.Dismissed = rec.Dismissed[len(rec.Dismissed)-500:]
			}
			return nil
		}
		gone = rec.Suggestions[i]
		rec.Suggestions = slices.Delete(rec.Suggestions, i, i+1)
		for _, x := range gone.From {
			if !slices.Contains(rec.Dismissed, x) {
				rec.Dismissed = append(rec.Dismissed, x)
			}
		}
		if len(rec.Dismissed) > 500 {
			rec.Dismissed = rec.Dismissed[len(rec.Dismissed)-500:]
		}
		return nil
	})
	if err != nil {
		return RulesView{}, err
	}
	if alone {
		r.audit(c, ActionRuleDismiss, id, fmt.Sprintf("%s, found outside the validation rules, dismissed: it will not be suggested", id))
		return r.View(ctx, c)
	}
	r.audit(c, ActionRuleDismiss, id, fmt.Sprintf("suggested validation rule %s dismissed; %s will not be suggested again", gone.ID, strings.Join(gone.From, ", ")))
	return r.View(ctx, c)
}

// ---------- what the runs found ----------

// objectKind is the kind of a key's object: "shop/Pod/web" is Pod.
func objectKind(object string) string {
	parts := strings.Split(object, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

// noteFound counts a run's findings under their ids (FoundKey): the
// findings, the run, the cluster and the job, the run among those that
// found it and the finding among the latest; for other.<slug> the
// titles, the objects' kinds and the agent's description too, which the
// suggestions are drafted from. A run is counted once (CF-85).
func (r *Rules) noteFound(ctx context.Context, in jobs.RouteInput, findings []jobs.Finding, now time.Time) {
	keyed := false
	for _, f := range findings {
		keyed = keyed || f.Key != nil
	}
	if !keyed {
		return
	}
	err := update(ctx, r.s.o.Backend, FoundKey, func(rec *foundRecord) error {
		if !countRun(rec, in.Run, in.Job, in.Kind, findings, now) {
			return errUnchanged
		}
		fitFound(rec)
		return nil
	})
	if err != nil {
		r.s.o.Logger.Warn("what the run found could not be counted for the validation rules", "run", in.Run, "err", err)
	}
}

// countRun counts one run's findings under their ids, as found at a
// time; a run counted before is left alone. It says whether it counted.
func countRun(rec *foundRecord, run, job, kind string, findings []jobs.Finding, at time.Time) bool {
	if run != "" && slices.Contains(rec.Counted, run) {
		return false
	}
	if !slices.ContainsFunc(findings, func(f jobs.Finding) bool { return f.Key != nil && f.Key.Check != "" }) {
		return false
	}
	if rec.IDs == nil {
		rec.IDs = map[string]*foundStat{}
	}
	if run != "" {
		rec.Counted = append(rec.Counted, run)
		slices.SortStableFunc(rec.Counted, func(a, b string) int { return runNumber(a) - runNumber(b) })
		if len(rec.Counted) > maxCounted {
			rec.Counted = rec.Counted[len(rec.Counted)-maxCounted:]
		}
	}
	counted := map[string]bool{}
	for _, f := range findings {
		if f.Key == nil || f.Key.Check == "" {
			continue
		}
		id := f.Key.Check
		st := rec.IDs[id]
		if st == nil {
			st = &foundStat{First: at, Last: at}
			rec.IDs[id] = st
		}
		st.Findings++
		if !counted[id] {
			st.Runs++
			counted[id] = true
			if run != "" && !slices.Contains(st.RunIDs, run) {
				st.RunIDs = append(st.RunIDs, run)
				slices.SortStableFunc(st.RunIDs, func(a, b string) int { return runNumber(a) - runNumber(b) })
				if len(st.RunIDs) > maxRunIDs {
					st.RunIDs = st.RunIDs[len(st.RunIDs)-maxRunIDs:]
				}
			}
		}
		if st.First.IsZero() || at.Before(st.First) {
			st.First = at
		}
		if at.After(st.Last) {
			st.Last = at
		}
		if !slices.Contains(st.Clusters, f.Key.Cluster) {
			st.Clusters = append(st.Clusters, f.Key.Cluster)
		}
		if kind != "" && !slices.Contains(st.Jobs, kind) {
			st.Jobs = append(st.Jobs, kind)
		}
		ff := FoundFinding{Run: run, ID: f.ID, At: at, Cluster: f.Key.Cluster, Object: clip(f.Key.Object, 160), Title: clip(f.Title, 100), Severity: f.Severity,
			Routing: f.Routing, Case: f.Case, KnownIssue: f.KnownIssue, Exception: f.Exception}
		if f.Check != id {
			ff.Check = f.Check
		}
		st.Latest = append(st.Latest, ff)
		slices.SortStableFunc(st.Latest, func(a, b FoundFinding) int { return a.At.Compare(b.At) })
		if len(st.Latest) > maxLatest {
			st.Latest = st.Latest[len(st.Latest)-maxLatest:]
		}
		if !otherPattern.MatchString(id) {
			continue
		}
		if t := clip(f.Title, 100); t != "" {
			if i := slices.IndexFunc(st.Titles, func(x TitleCount) bool { return x.Text == t }); i >= 0 {
				st.Titles[i].N++
			} else if len(st.Titles) < maxTitles {
				st.Titles = append(st.Titles, TitleCount{Text: t, N: 1})
			}
		}
		if k := objectKind(f.Key.Object); k != "" && (st.Kinds[k] > 0 || len(st.Kinds) < 5) {
			if st.Kinds == nil {
				st.Kinds = map[string]int{}
			}
			st.Kinds[k]++
		}
		if why := clip(f.CheckWhy, maxWhy); why != "" {
			st.noteWhy(Description{Job: firstOf(job, kind), Kind: kind, Text: why, Run: run, At: at})
		}
	}
	return true
}

// noteWhy keeps an agent's description of an id: the latest per job, at
// most maxWhys jobs, the least recent going first (CF-86).
func (st *foundStat) noteWhy(d Description) {
	if i := slices.IndexFunc(st.Whys, func(x Description) bool { return x.Job == d.Job }); i >= 0 {
		if d.At.Before(st.Whys[i].At) {
			return
		}
		st.Whys = slices.Delete(st.Whys, i, i+1)
	}
	st.Whys = append(st.Whys, d)
	slices.SortStableFunc(st.Whys, func(a, b Description) int { return a.At.Compare(b.At) })
	if len(st.Whys) > maxWhys {
		st.Whys = st.Whys[len(st.Whys)-maxWhys:]
	}
}

// fitFound keeps FoundKey within maxFound ids and foundBudget bytes: the
// least recently found ids go first, their lists before the ids
// themselves.
func fitFound(rec *foundRecord) {
	oldest := func(f func(st *foundStat) bool) string {
		out := ""
		for id, st := range rec.IDs {
			if !f(st) {
				continue
			}
			if out == "" || st.Last.Before(rec.IDs[out].Last) || st.Last.Equal(rec.IDs[out].Last) && id < out {
				out = id
			}
		}
		return out
	}
	every := func(*foundStat) bool { return true }
	for len(rec.IDs) > maxFound {
		delete(rec.IDs, oldest(every))
	}
	for {
		b, _ := json.Marshal(rec)
		if len(b) <= foundBudget || len(rec.IDs) == 0 {
			return
		}
		if id := oldest(func(st *foundStat) bool { return len(st.Latest)+len(st.Whys)+len(st.RunIDs) > 0 }); id != "" {
			st := rec.IDs[id]
			st.Latest, st.Whys, st.RunIDs = nil, nil, nil
			continue
		}
		delete(rec.IDs, oldest(every))
	}
}

// foundRuns gives the runs kept with keyed findings, oldest first: the
// jobs' service (jobs.Service.FoundRuns).
type foundRuns interface {
	FoundRuns(ctx context.Context) ([]jobs.Run, error)
}

// runAt is when a run's findings were found: when it ended.
func runAt(run jobs.Run) time.Time {
	if !run.Ended.IsZero() {
		return run.Ended
	}
	return run.Started
}

// Recount counts what the runs kept found that FoundKey has not counted
// (CF-85), and says how many runs it counted: each run once, so it is
// done again at no cost. A record from before the runs counted were
// named counted every run that ended since its first count; the runs
// before it (the counter's first day) are counted now.
func (r *Rules) Recount(ctx context.Context) (int, error) {
	fr, ok := r.s.o.Jobs.(foundRuns)
	if !ok {
		return 0, nil
	}
	runs, err := fr.FoundRuns(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	err = update(ctx, r.s.o.Backend, FoundKey, func(rec *foundRecord) error {
		n = 0
		changed := !rec.Tracked
		if !rec.Tracked {
			var since time.Time
			for _, st := range rec.IDs {
				if !st.First.IsZero() && (since.IsZero() || st.First.Before(since)) {
					since = st.First
				}
			}
			for _, run := range runs {
				if !since.IsZero() && !runAt(run).Before(since.Add(-recountSlack)) && !slices.Contains(rec.Counted, run.ID) {
					rec.Counted = append(rec.Counted, run.ID)
				}
			}
			rec.Tracked = true
		}
		for _, run := range runs {
			if countRun(rec, run.ID, run.Job, run.Kind, run.Findings, runAt(run)) {
				n++
				changed = true
			}
		}
		if !changed {
			return errUnchanged
		}
		fitFound(rec)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		r.s.o.Logger.Info("the validation rules counted what the runs kept found", "runs", n)
	}
	return n, nil
}

// recountOnce counts the runs kept once on the leader, as it starts to
// lead (CF-85); again on the next tick when it could not.
func (r *Rules) recountOnce(ctx context.Context) {
	r.mu.Lock()
	done := r.recounted
	r.mu.Unlock()
	if done {
		return
	}
	if _, err := r.Recount(ctx); err != nil {
		r.s.o.Logger.Warn("what the runs kept found could not be counted for the validation rules", "err", err)
		return
	}
	r.mu.Lock()
	r.recounted = true
	r.mu.Unlock()
}

// ---------- the housekeeping's pass ----------

// RulesDone is what the housekeeping did to the rules: the exact repeats
// it promoted, and the rules it suggests.
type RulesDone struct {
	Promoted  []Rule
	Suggested []Suggestion
}

// slugWords are an other.<slug> id's words: other.etcd-slow is "etcd
// slow".
func slugWords(id string) string {
	return strings.ReplaceAll(strings.TrimPrefix(id, "other."), "-", " ")
}

// prefixes name a suggested id after the kind of object its findings are
// on.
var prefixes = map[string]string{
	"Pod": "pod", "Node": "node", "PersistentVolumeClaim": "pvc", "PersistentVolume": "pv", "Deployment": "workload", "StatefulSet": "workload",
	"DaemonSet": "workload", "ReplicaSet": "workload", "Job": "job", "CronJob": "job", "Service": "service", "Certificate": "cert", "Namespace": "namespace",
	"Role": "rbac", "ClusterRole": "rbac", "RoleBinding": "rbac", "ClusterRoleBinding": "rbac", "ServiceAccount": "rbac", "Ingress": "ingress",
	"NetworkPolicy": "netpol", "ConfigMap": "config", "Secret": "config",
}

// Housekeep is the housekeeping's pass over the ids found outside the
// list (ADR-138): an exact repeat found in AutoPromoteRuns runs or more,
// with no variant, is promoted as it is; the other candidates (found in
// SuggestRuns runs or on SuggestClusters clusters), with their likely
// variants, are drafted into the suggestions, which replace the last
// pass's. Dismissed ids are left alone.
func (r *Rules) Housekeep(ctx context.Context, like func(a, b string) float64) (RulesDone, error) {
	// What the runs kept found and was not counted is counted first
	// (CF-85).
	if _, err := r.Recount(ctx); err != nil {
		r.s.o.Logger.Warn("what the runs kept found could not be counted for the validation rules", "err", err)
	}
	rs, rec, err := r.load(ctx)
	if err != nil {
		return RulesDone{}, err
	}
	u := r.usage(ctx)
	var pool []string
	for id := range u.found {
		if _, on := rs.get(id); !on && otherPattern.MatchString(id) && !slices.Contains(rec.Dismissed, id) {
			pool = append(pool, id)
		}
	}
	sort.Slice(pool, func(i, j int) bool {
		a, b := u.found[pool[i]], u.found[pool[j]]
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		return pool[i] < pool[j]
	})
	alike := func(a, b string) bool {
		if like(slugWords(a), slugWords(b)) >= variantLikeness {
			return true
		}
		ta, tb := u.found[a].title(), u.found[b].title()
		return ta != "" && tb != "" && like(ta, tb) >= titleLikeness
	}
	// alikeWhy says why an id was grouped with the one a suggestion
	// starts from: their ids' words, or their findings' titles.
	alikeWhy := func(head, other string) string {
		if like(slugWords(head), slugWords(other)) >= variantLikeness {
			return "their ids are alike in words (" + slugWords(head) + "; " + slugWords(other) + ")"
		}
		return "their findings' titles are alike (“" + u.found[head].title() + "”; “" + u.found[other].title() + "”)"
	}
	grouped := map[string]bool{}
	var done RulesDone
	var auto []PromoteInput
	for _, id := range pool {
		st := u.found[id]
		if grouped[id] || st.Runs < SuggestRuns && len(st.Clusters) < SuggestClusters {
			continue
		}
		group := []string{id}
		grouped[id] = true
		for _, other := range pool {
			if !grouped[other] && alike(id, other) {
				group = append(group, other)
				grouped[other] = true
			}
		}
		says, typ, jobList := st.title(), u.mostType(group...), []string{}
		// The meaning is drafted from the agents' description of the id
		// when one wrote it (CF-86), else its findings' title.
		if m := meaningOf(st.Whys); m != "" {
			says = m
		}
		runs, findings, clusters := 0, 0, map[string]bool{}
		kinds := map[string]int{}
		var descs []Description
		for _, g := range group {
			x := u.found[g]
			runs += x.Runs
			findings += x.Findings
			for _, c := range x.Clusters {
				clusters[c] = true
			}
			for _, d := range x.Whys {
				d.ID = g
				descs = append(descs, d)
			}
			for _, j := range x.Jobs {
				if !slices.Contains(jobList, j) {
					jobList = append(jobList, j)
				}
			}
			for k, n := range x.Kinds {
				kinds[k] += n
			}
		}
		if says == "" {
			says = slugWords(id)
		}
		// Only the kinds with a catalogue are offered rules.
		jobList = slices.DeleteFunc(jobList, func(j string) bool {
			return !slices.ContainsFunc(jobs.BuiltinRules, func(k jobs.KindRules) bool { return k.Kind == j })
		})
		if len(jobList) == 0 {
			jobList = []string{jobs.KindAudit}
		}
		if len(group) == 1 && st.Runs >= AutoPromoteRuns {
			auto = append(auto, PromoteInput{From: group, Says: says, Type: typ, Jobs: jobList})
			continue
		}
		var found []string
		for _, g := range group {
			for _, j := range u.found[g].Jobs {
				if !slices.Contains(found, j) {
					found = append(found, j)
				}
			}
		}
		slices.SortStableFunc(descs, func(a, b Description) int { return b.At.Compare(a.At) })
		done.Suggested = append(done.Suggested, Suggestion{ID: suggestedID(rs, id, kinds), Says: clip(says, maxSays), Type: typ, Jobs: jobList, From: group, Runs: runs, Clusters: len(clusters),
			Why: suggestedWhy(u, group, findings, runs, len(clusters), found, alikeWhy), Descriptions: descs})
	}
	now := r.s.now()
	err = r.update(ctx, func(_ *ruleSet, rec *rulesRecord) error {
		for i := range done.Suggested {
			s := &done.Suggested[i]
			s.Created = now
			for _, was := range rec.Suggestions {
				if first(was.From) == first(s.From) {
					s.Created = was.Created
				}
			}
		}
		rec.Suggestions = done.Suggested
		return nil
	})
	if err != nil {
		return RulesDone{}, err
	}
	hk := Caller{Person: Person{Subject: "221b", Name: "Housekeeping"}, Admin: true}
	for _, in := range auto {
		detail, err := r.promote(ctx, in, "Housekeeping", true)
		if err != nil {
			r.s.o.Logger.Warn("an exact repeat could not be promoted to a validation rule", "id", in.From[0], "err", err)
			continue
		}
		r.audit(hk, ActionRuleAuto, in.From[0], detail+fmt.Sprintf("; promoted by the housekeeping as it is: found in %d runs with no variant", u.found[in.From[0]].Runs))
		if rule, ok := r.set(ctx).get(in.From[0]); ok {
			done.Promoted = append(done.Promoted, rule)
		}
	}
	return done, nil
}

// meaningOf is the meaning an agent's descriptions of an id give: the
// first sentence of the latest, at most maxSays characters (CF-86).
func meaningOf(whys []Description) string {
	var latest Description
	for _, d := range whys {
		if d.At.After(latest.At) || latest.Text == "" {
			latest = d
		}
	}
	text := strings.TrimSpace(latest.Text)
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	return clip(text, maxSays)
}

// suggestedWhy says, from facts, why a suggestion was made (CF-86): how
// often its ids were found, in how many runs, on how many clusters and by
// which jobs, and why each id was grouped with the first.
func suggestedWhy(u usage, group []string, findings, runs, clusters int, found []string, alikeWhy func(head, other string) string) string {
	labels := make([]string, 0, len(found))
	for _, j := range found {
		labels = append(labels, jobLabel(j))
	}
	why := fmt.Sprintf("Found %d %s in %d %s on %d %s by %s", findings, plural(findings, "time", "times"), runs, plural(runs, "run", "runs"), clusters, plural(clusters, "cluster", "clusters"), orNone(strings.Join(labels, ", ")))
	if len(group) == 1 {
		if st := u.found[group[0]]; st != nil && st.Runs >= SuggestRuns {
			return why + fmt.Sprintf(": %d runs or more make it a candidate.", SuggestRuns)
		}
		return why + fmt.Sprintf(": %d clusters or more make it a candidate.", SuggestClusters)
	}
	var parts []string
	for _, g := range group[1:] {
		parts = append(parts, g+" because "+alikeWhy(group[0], g))
	}
	return why + "; grouped with " + strings.Join(parts, "; ") + "."
}

// suggestedID is a clean id for a suggestion: the kind of object its
// findings are on most, then its slug (pod.etcd-slow); its own id when
// that is taken or there is no kind.
func suggestedID(rs *ruleSet, id string, kinds map[string]int) string {
	best, most := "", 0
	for k, n := range kinds {
		if n > most || n == most && k < best {
			best, most = k, n
		}
	}
	prefix := prefixes[best]
	if prefix == "" {
		return id
	}
	clean := prefix + "." + strings.TrimPrefix(id, "other.")
	if _, taken := rs.get(clean); taken || validRuleID(clean) != nil {
		return id
	}
	return clean
}

// ---------- the HTTP API ----------

// answerRules answers a change to the rules with the page as it stands.
func answerRules(w http.ResponseWriter, v RulesView, err error) {
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) rules(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.rules.View(r.Context(), c)
	answerRules(w, v, err)
}

func (h *handler) addRule(w http.ResponseWriter, r *http.Request, c Caller) {
	var in RuleInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.rules.Add(r.Context(), in, c)
	answerRules(w, v, err)
}

func (h *handler) editRule(w http.ResponseWriter, r *http.Request, c Caller) {
	var in RulePatch
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.rules.Edit(r.Context(), r.PathValue("id"), in, c)
	answerRules(w, v, err)
}

func (h *handler) deleteRule(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.rules.Delete(r.Context(), r.PathValue("id"), c)
	answerRules(w, v, err)
}

func (h *handler) retireRule(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.rules.Retire(r.Context(), r.PathValue("id"), c)
	answerRules(w, v, err)
}

func (h *handler) activateRule(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.rules.Activate(r.Context(), r.PathValue("id"), c)
	answerRules(w, v, err)
}

func (h *handler) replaceRule(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		By string `json:"by"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.rules.Replace(r.Context(), r.PathValue("id"), in.By, c)
	answerRules(w, v, err)
}

func (h *handler) promoteRule(w http.ResponseWriter, r *http.Request, c Caller) {
	var in PromoteInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.rules.Promote(r.Context(), in, c)
	answerRules(w, v, err)
}

// suggestions answers the Inbox of a SuperAdmin (CF-87).
func (h *handler) ruleSuggestions(w http.ResponseWriter, r *http.Request, c Caller) {
	list, err := h.s.rules.Suggestions(r.Context(), c)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": list})
}

func (h *handler) dismissRule(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.rules.Dismiss(r.Context(), r.PathValue("id"), c)
	answerRules(w, v, err)
}
