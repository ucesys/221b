package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// The keys of the console's AI ConfigMap (Phase 12, ADR-75).
const (
	ProvidersKey = "ai-providers.json"
	AgentsKey    = "ai-agents.json"
	PromptsKey   = "ai-prompts.json"
	TestsKey     = "ai-tests.json"
	PilotKey     = "ai-pilot.json"
)

// BackupKeys are the keys a backup carries, as they are kept: none holds
// a credential, which stays in its provider's Secret. Export marks the
// providers that have one with clusters.Placeholder.
var BackupKeys = []string{ProvidersKey, AgentsKey, PromptsKey, TestsKey, PilotKey}

// DefaultProvider is the id of the provider Sherlock had before Phase 12:
// the settings' (or the chart's) provider, model and prices, with its key
// in the Secret it always had.
const DefaultProvider = "default"

// Provider is one place agents' models come from. Its credential is in a
// Secret of its own and never here.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	sherlock.Provider
	// Model is the model agents use on it when they name none.
	Model string `json:"model,omitempty"`
	// PriceIn and PriceOut price its models in US dollars per million
	// tokens; 0 is the listed price of a Claude model, or the highest.
	PriceIn  float64 `json:"priceIn,omitempty"`
	PriceOut float64 `json:"priceOut,omitempty"`
	// --- AI settings, round 2 (Phase 12, wave 5) ---
	// ContextWindow is the context of its default model in tokens, from
	// the endpoint's models list (vLLM's max_model_len) or set by hand;
	// 0 is not known. Sherlock answers in its compact form on a model
	// whose context its whole form does not fit (AS-07).
	ContextWindow int `json:"contextWindow,omitempty"`
	// Tested is its last connection test, kept by the console: a
	// provider whose endpoint answered without a credential may be used
	// without one (AS-04). Never taken from a request.
	Tested *ProviderTest `json:"tested,omitempty"`
}

// ProviderTest is the last connection test of a provider.
type ProviderTest struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Model string    `json:"model,omitempty"`
	// Keyless says the test ran without a credential.
	Keyless bool `json:"keyless,omitempty"`
	// Conn names the connection settings it ran with (their hash), so a
	// changed endpoint needs a new test.
	Conn  string `json:"conn,omitempty"`
	Error string `json:"error,omitempty"`
}

// ConnHash names a provider's connection settings: kind, endpoint,
// authentication, headers, query, region, project and CA.
func ConnHash(p sherlock.Provider) string {
	b, _ := json.Marshal(p)
	return hash(string(b))
}

// MaxContextWindow bounds a context window given by hand.
const MaxContextWindow = 10_000_000

// MinContextWindow is the smallest context window that can be set.
const MinContextWindow = 1024

func checkWindow(n int) error {
	if n != 0 && (n < MinContextWindow || n > MaxContextWindow) {
		return fmt.Errorf("a context window is between %d and %d tokens, or 0 when it is not known", MinContextWindow, MaxContextWindow)
	}
	return nil
}

// Providers are the providers besides the default one, kept under
// ProvidersKey.
type Providers struct {
	// DefaultName names the default provider; "" names it by its kind.
	DefaultName string     `json:"defaultName,omitempty"`
	List        []Provider `json:"list,omitempty"`
	// DefaultWindow and DefaultTested are the default provider's context
	// window and last test, whose other settings are Sherlock's (Phase 12,
	// wave 5).
	DefaultWindow int           `json:"defaultWindow,omitempty"`
	DefaultTested *ProviderTest `json:"defaultTested,omitempty"`
	// DefaultID is the provider agents that name none use, made the
	// default in Settings › Providers; "" is the default provider.
	DefaultID string `json:"defaultId,omitempty"`
	// DefaultRemoved says the default provider (Sherlock's of before
	// Phase 12) was removed, once another was made the default.
	DefaultRemoved bool `json:"defaultRemoved,omitempty"`
}

// MaxProviders bounds the providers.
const MaxProviders = 12

var (
	reID    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	reModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:@_/+-]{0,127}$`)
)

// Validate reports what is wrong with a provider.
func (p Provider) Validate() error {
	if !reID.MatchString(p.ID) {
		return errors.New("a provider's id is lower-case letters, digits and dashes, starting with a letter")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 60 {
		return errors.New("give the provider a name of at most 60 characters")
	}
	if p.Model != "" && !reModel.MatchString(p.Model) {
		return fmt.Errorf("%q is not a model name", p.Model)
	}
	for _, v := range []float64{p.PriceIn, p.PriceOut} {
		if v < 0 || v > 10_000 || math.IsNaN(v) {
			return errors.New("a price must be between $0 and $10,000 per million tokens")
		}
	}
	if err := checkWindow(p.ContextWindow); err != nil {
		return err
	}
	return p.Provider.Validate()
}

// KindLabel names a kind of provider for people.
func KindLabel(kind string) string {
	switch kind {
	case sherlock.KindOpenAI:
		return "OpenAI-compatible"
	case sherlock.KindBedrock:
		return "Amazon Bedrock"
	case sherlock.KindVertex:
		return "Google Vertex AI"
	}
	return "Anthropic"
}

// AgentSettings are what an administrator chose for one agent.
type AgentSettings struct {
	// On turns the agent on or off; nil is the catalog's default. Sherlock
	// is on where its clusters say (the settings' sherlock.clusters).
	On *bool `json:"on,omitempty"`
	// Provider is the provider's id; "" is the default provider.
	Provider string `json:"provider,omitempty"`
	// Model is its model; "" is the provider's default model.
	Model string `json:"model,omitempty"`
	// DailyUSD and MonthlyUSD are its own caps; 0 is none but the overall.
	DailyUSD   float64 `json:"dailyUSD,omitempty"`
	MonthlyUSD float64 `json:"monthlyUSD,omitempty"`
	// Clusters are the clusters it works on; empty is every cluster.
	Clusters []string `json:"clusters,omitempty"`
	// Tools are the tools it may use, of its purpose's; nil is all of them.
	Tools []string `json:"tools,omitempty"`
	// --- AI settings, round 2 (Phase 12, wave 5) ---
	// ContextWindow is the context of its own model in tokens; 0 is its
	// provider's (for the provider's default model) or not known.
	ContextWindow int `json:"contextWindow,omitempty"`
	// Compact, for Sherlock, chooses its compact form: "" when its whole
	// form does not fit the model's context (CompactAuto), "always" or
	// "never" (AS-07).
	Compact string `json:"compact,omitempty"`
}

// Sherlock's forms.
const (
	CompactAuto   = ""
	CompactAlways = "always"
	CompactNever  = "never"
)

// Agents are the agents' settings, kept under AgentsKey.
type Agents struct {
	Agents map[string]AgentSettings `json:"agents,omitempty"`
	// SingleProvider says a SuperAdmin confirmed that this console has one
	// provider only, so the Script reviewer may run on the Automation
	// author's.
	SingleProvider bool `json:"singleProvider,omitempty"`
	// Migrated says Sherlock's instructions and notes from before Phase 12
	// were carried over.
	Migrated bool `json:"migrated,omitempty"`
	// BaselineTested is the hash of each agent's baseline last tested at
	// start, so a new one is tested once.
	BaselineTested map[string]string `json:"baselineTested,omitempty"`
}

// MaxCapUSD bounds an agent's cap.
const MaxCapUSD = 100_000

// Validate reports what is wrong with an agent's settings.
func (a AgentSettings) Validate(info Info) error {
	if a.Model != "" && !reModel.MatchString(a.Model) {
		return fmt.Errorf("%q is not a model name", a.Model)
	}
	for _, v := range []float64{a.DailyUSD, a.MonthlyUSD} {
		if v < 0 || v > MaxCapUSD || math.IsNaN(v) {
			return fmt.Errorf("a spending cap must be between $0 (none of its own) and $%d", MaxCapUSD)
		}
	}
	if a.DailyUSD > 0 && a.MonthlyUSD > 0 && a.DailyUSD > a.MonthlyUSD {
		return errors.New("the daily cap is above the monthly cap")
	}
	if len(a.Clusters) > 1000 {
		return errors.New("too many clusters")
	}
	if err := checkWindow(a.ContextWindow); err != nil {
		return err
	}
	switch a.Compact {
	case CompactAuto, CompactAlways, CompactNever:
	default:
		return errors.New("the compact form is chosen when needed, always or never")
	}
	if a.Compact != CompactAuto && info.ID != Sherlock {
		return errors.New("only Sherlock has a compact form")
	}
	for _, t := range a.Tools {
		if !slices.Contains(info.Tools, t) {
			return fmt.Errorf("%s is not one of the tools of the %s's purpose", t, info.Name)
		}
	}
	return nil
}

// on says whether the agent is on, with the catalog's default.
func (a AgentSettings) on(info Info) bool {
	if a.On == nil {
		return info.On
	}
	return *a.On
}

// tools are the tools in force: those chosen, never more than the purpose's.
func (a AgentSettings) tools(info Info) []string {
	if a.Tools == nil {
		return append([]string(nil), info.Tools...)
	}
	var out []string
	for _, t := range info.Tools {
		if slices.Contains(a.Tools, t) {
			out = append(out, t)
		}
	}
	return out
}

// worksOn says whether the agent works on a cluster.
func (a AgentSettings) worksOn(cluster string) bool {
	return len(a.Clusters) == 0 || slices.Contains(a.Clusters, cluster)
}

// MaxWords bounds the house instructions and each cluster's notes: they go
// with every question, and the prompt cache is not free.
const MaxWords = 2000

// AllClusters is the notes' key for every cluster.
const AllClusters = "*"

// Words counts the words of a text.
func Words(s string) int { return len(strings.Fields(s)) }

// CheckText reports a text over the word limit.
func CheckText(what, s string) error {
	if n := Words(s); n > MaxWords {
		return fmt.Errorf("%s have %d words; keep them to %d", what, n, MaxWords)
	}
	return nil
}

var reFact = regexp.MustCompile(`(?i)\b(\d{1,3}\.){3}\d{1,3}(/\d{1,2})?\b|\b[a-z0-9-]+(\.[a-z0-9-]+){2,}\b|https?://|\bport\s+\d{2,5}\b|:\d{4,5}\b|\b(runs on|is hosted on|lives on|is at|address is|ip is)\b`)

// FactWarnings are the lines of a text that look like facts about the
// infrastructure (addresses, host names, ports, where something runs):
// they belong in Knowledge, which Mycroft keeps current, rather than in
// instructions that go stale. A warning, never a refusal.
func FactWarnings(s string) []string {
	var out []string
	for i, line := range strings.Split(s, "\n") {
		if reFact.MatchString(line) {
			l := strings.TrimSpace(line)
			if len(l) > 80 {
				l = l[:80] + "…"
			}
			out = append(out, fmt.Sprintf("Line %d looks like a fact about your infrastructure (%s): facts belong in Knowledge, where they are kept current.", i+1, l))
			if len(out) == 10 {
				break
			}
		}
	}
	return out
}

// hash names a baseline's text.
func hash(s string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return hex.EncodeToString(sum[:8])
}

// States of an agent's instructions against the chart's baseline.
const (
	// InSync: the baseline from the chart is in force.
	InSync = "in-sync"
	// Edited: an edit made in the console is in force, and the chart has
	// not changed since.
	Edited = "edited"
	// BaselineChanged: an edit made in the console is in force, and an
	// upgrade brought a new baseline since; nothing replaces either until
	// a SuperAdmin chooses.
	BaselineChanged = "baseline-changed"
)

// Stages of a draft on its way to live.
const (
	StageDraft = "draft"
	StageTest  = "test"
	StagePilot = "pilot"
)

// AgentPrompt is one agent's instructions as the console keeps them.
type AgentPrompt struct {
	// House is the house instructions edited in the console and in force;
	// nil is the chart's baseline.
	House *string `json:"house,omitempty"`
	// BaseHash is the hash of the baseline House was made over, and Base
	// that baseline, to show what an upgrade changed under the edit.
	BaseHash string `json:"baseHash,omitempty"`
	Base     string `json:"base,omitempty"`
	// Notes are the per-cluster notes in force, by cluster id; AllClusters
	// is every cluster's.
	Notes map[string]string `json:"notes,omitempty"`
	// Live is the version in force; 0 is the baseline with no version.
	Live int `json:"live,omitempty"`
	// Revs counts the drafts ever saved, so a draft's revision never
	// repeats and an approval of an older one cannot apply to it.
	Revs     int       `json:"revs,omitempty"`
	Versions []Version `json:"versions,omitempty"`
	Draft    *Draft    `json:"draft,omitempty"`
	// BaselineCheck is the test run made at start with a new baseline.
	BaselineCheck *TestRun `json:"baselineCheck,omitempty"`
	// --- AI settings, round 2 (Phase 12, wave 5) ---
	// Parts are the edits in force of the agent's built-in text, by part
	// (PartCore, and Sherlock's PartCompact): each replaces that part's
	// built-in text whole; a part not here is the built-in one (AS-01).
	Parts map[string]string `json:"parts,omitempty"`
	// PartBase is the hash of the built-in text each edit was made over,
	// to show that a release changed it under the edit.
	PartBase map[string]string `json:"partBase,omitempty"`
}

// Version is the instructions as they were once live.
type Version struct {
	N     int               `json:"n"`
	House string            `json:"house"`
	Notes map[string]string `json:"notes,omitempty"`
	By    string            `json:"by"`
	At    time.Time         `json:"at"`
	// Why says how it came to be: made live, the baseline taken, restored.
	Why string `json:"why,omitempty"`
	// Parts are the edits of the built-in text it had (Phase 12, wave 5).
	Parts map[string]string `json:"parts,omitempty"`
}

// MaxVersions bounds the history each agent keeps.
const MaxVersions = 10

// Draft is instructions on their way to live: saved, tested, piloted.
type Draft struct {
	// Rev counts its saves; an approval and a test are of one.
	Rev   int               `json:"rev"`
	House string            `json:"house"`
	Notes map[string]string `json:"notes,omitempty"`
	Stage string            `json:"stage"`
	By    string            `json:"by"`
	At    time.Time         `json:"at"`
	Test  *TestRun          `json:"test,omitempty"`
	Pilot *Pilot            `json:"pilot,omitempty"`
	// Request is the approval waiting for it to go live, if any.
	Request int `json:"request,omitempty"`
	// Restored is the version it restores, which may go live untested.
	Restored int `json:"restored,omitempty"`
	// Parts are the edits of the built-in text it makes, whole: a part
	// not here is the built-in one (Phase 12, wave 5).
	Parts map[string]string `json:"parts,omitempty"`
}

// Pilot serves a draft to chosen people or clusters for a set time.
type Pilot struct {
	People   []string  `json:"people,omitempty"`
	Clusters []string  `json:"clusters,omitempty"`
	Started  time.Time `json:"started"`
	Until    time.Time `json:"until"`
	By       string    `json:"by"`
}

// MaxPilot bounds a pilot's time.
const MaxPilot = 14 * 24 * time.Hour

// serves says whether the pilot answers for a person (any of their names)
// on a cluster at a time.
func (p *Pilot) serves(names []string, cluster string, now time.Time) bool {
	if p == nil || !now.Before(p.Until) {
		return false
	}
	if slices.Contains(p.Clusters, cluster) {
		return true
	}
	for _, n := range names {
		if n != "" && slices.ContainsFunc(p.People, func(x string) bool { return strings.EqualFold(x, n) }) {
			return true
		}
	}
	return false
}

// Prompts are every agent's instructions, kept under PromptsKey.
type Prompts struct {
	Agents map[string]*AgentPrompt `json:"agents,omitempty"`
}

// State is where an agent's live instructions stand against the baseline.
func (p *AgentPrompt) State(baseline string) string {
	if p == nil || p.House == nil {
		return InSync
	}
	if p.BaseHash == hash(baseline) {
		return Edited
	}
	return BaselineChanged
}

// LiveHouse is the house instructions in force: the edit, or the baseline.
func (p *AgentPrompt) LiveHouse(baseline string) string {
	if p == nil || p.House == nil {
		return baseline
	}
	return *p.House
}

// Settle drops an edit equal to the baseline the chart now deploys, so the
// agent is in sync again; true when it changed anything.
func (p *AgentPrompt) Settle(baseline string) bool {
	if p == nil || p.House == nil || strings.TrimSpace(*p.House) != strings.TrimSpace(baseline) {
		return false
	}
	p.House, p.BaseHash, p.Base = nil, "", ""
	return true
}

// addVersion records instructions that went live, keeping MaxVersions.
func (p *AgentPrompt) addVersion(v Version) Version {
	n := 0
	for _, x := range p.Versions {
		n = max(n, x.N)
	}
	v.N = n + 1
	p.Versions = append(p.Versions, v)
	if len(p.Versions) > MaxVersions {
		p.Versions = append([]Version(nil), p.Versions[len(p.Versions)-MaxVersions:]...)
	}
	p.Live = v.N
	return v
}

// goLive puts instructions in force over a baseline: an edit, or the
// baseline itself when they are the same.
func (p *AgentPrompt) goLive(house string, notes map[string]string, baseline, by, why string, now time.Time) Version {
	if strings.TrimSpace(house) == strings.TrimSpace(baseline) {
		p.House, p.BaseHash, p.Base = nil, "", ""
	} else {
		h := house
		p.House, p.BaseHash, p.Base = &h, hash(baseline), baseline
	}
	p.Notes = cleanNotes(notes)
	return p.addVersion(Version{House: house, Notes: p.Notes, Parts: cloneParts(p.Parts), By: by, At: now.UTC(), Why: why})
}

// setParts puts edits of the built-in text in force: an edit equal to
// its built-in text is no edit. builtIn gives a part's built-in text.
//
// An edit kept as it was keeps the built-in text it was made over.
func (p *AgentPrompt) setParts(parts map[string]string, builtIn func(string) string) {
	before, bases := p.Parts, p.PartBase
	p.Parts, p.PartBase = nil, nil
	for k, v := range parts {
		b, t := builtIn(k), strings.TrimSpace(v)
		if t == "" || t == strings.TrimSpace(b) {
			continue
		}
		if p.Parts == nil {
			p.Parts, p.PartBase = map[string]string{}, map[string]string{}
		}
		p.Parts[k], p.PartBase[k] = t, hash(b)
		if before[k] == t && bases[k] != "" {
			p.PartBase[k] = bases[k]
		}
	}
}

func cloneParts(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cleanNotes(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// notesFor are the notes for a cluster: every cluster's, then its own.
func notesFor(notes map[string]string, cluster string) string {
	var parts []string
	if n := strings.TrimSpace(notes[AllClusters]); n != "" {
		parts = append(parts, n)
	}
	if n := strings.TrimSpace(notes[cluster]); n != "" && cluster != AllClusters {
		parts = append(parts, n)
	}
	return strings.Join(parts, "\n\n")
}
