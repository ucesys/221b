// Package knowledge is Mycroft's knowledge (Phase 12, ADR-94 to ADR-98):
// the sources a SuperAdmin connects (Confluence spaces, git repositories of
// runbooks, uploaded documents, and what the console itself knows), the
// wiki Mycroft writes from them in a git repository or an S3 bucket, the
// review of its changes before they are published, the search index the
// agents' ask_knowledge reads, and Mycroft's inventory and drift
// proposals.
//
// Three layers, each where it is used best: the wiki holds prose (how
// things work and why, runbooks, best practices), approved by people; the
// console's records hold the countable facts (hosts, services, owners),
// which Mycroft proposes as cards rather than writing them into pages; and
// the source index holds scrubbed excerpts of the sources, so an agent can
// cite a source the wiki does not cover yet. Sources are data, never
// instructions, and every secret in them is scrubbed before anything is
// written (X-01).
//
// Every replica keeps its own copy of the approved pages on its disk and
// its own index, and answers ask_knowledge from them, never the network.
// Only the leader (ADR-70) reads sources, writes to the wiki and publishes
// to Confluence.
package knowledge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/kv"
)

// The keys of the knowledge ConfigMap (<release>-knowledge).
const (
	// SettingsKey holds the sources and where the wiki is kept.
	SettingsKey = "knowledge-settings.json"
	// DraftsKey holds Mycroft's runs waiting for review, with their
	// diffs, and the recent decisions.
	DraftsKey = "knowledge-drafts.json"
	// StatusKey holds the last sync's outcome, per source.
	StatusKey = "knowledge-status.json"
	// PublisherKey is the Confluence publisher's settings, outbox and
	// pages.
	PublisherKey = confluence.StateKey
)

// The most each key holds, by default; together under the ConfigMap's
// budget.
const (
	DefaultSettingsBytes  = 64 << 10
	DefaultDraftsBytes    = 512 << 10
	DefaultStatusBytes    = 96 << 10
	DefaultPublisherBytes = 256 << 10
)

// Limits are the keys' limits, as the ConfigMap's layout sets them.
type Limits struct {
	SettingsBytes, DraftsBytes, StatusBytes int
}

func (l Limits) withDefaults() Limits {
	if l.SettingsBytes <= 0 {
		l.SettingsBytes = DefaultSettingsBytes
	}
	if l.DraftsBytes <= 0 {
		l.DraftsBytes = DefaultDraftsBytes
	}
	if l.StatusBytes <= 0 {
		l.StatusBytes = DefaultStatusBytes
	}
	return l
}

// Audit actions; the log's family is "knowledge".
const (
	ActionSource  = "knowledge.source"
	ActionStore   = "knowledge.store"
	ActionSync    = "knowledge.sync"
	ActionReview  = "knowledge.review"
	ActionPublish = "knowledge.publish"
	ActionHook    = "knowledge.hook"
	// ActionAdd is a note, web pages or a document added in Cases ›
	// Mycroft (Phase 12, wave 5).
	ActionAdd = "knowledge.add"
)

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("not found")
	ErrDenied   = errors.New("not allowed")
	ErrInvalid  = errors.New("not valid")
	// ErrNotLeader is work only the leader does, asked of another replica.
	ErrNotLeader = errors.New("only the console that leads does this")
	// ErrNoStore is work that needs the wiki's store before one is set.
	ErrNoStore = errors.New("choose where the wiki is kept first, in Settings › Knowledge")
	// ErrBusy is a sync asked for while one runs.
	ErrBusy = errors.New("a sync is running")
)

func invalid(format string, a ...any) error { return wrapf(ErrInvalid, format, a...) }

// Completer has an agent answer one request with no tools: package
// sherlock's CompleteAs, which scrubs the text, counts the spend against
// the agent's budget and audits the call.
type Completer interface {
	CompleteAs(ctx context.Context, agent string, who audit.Who, cluster, label, system, text string) (string, error)
}

// MycroftID is the agent that writes the wiki (package ai's Mycroft).
const MycroftID = "mycroft"

// Options configure the service.
type Options struct {
	// Backend keeps the records (the knowledge ConfigMap); nil keeps them
	// in memory.
	Backend kv.Backend
	Limits  Limits
	// Secrets keeps the credentials, one key each, in the Secret
	// <release>-knowledge; nil keeps them in memory.
	Secrets SecretStore
	// Dir is where the local copy of the wiki, and the source
	// repositories, are kept on the pod's disk; empty is a temporary
	// directory.
	Dir string
	// Store, when set, is the wiki's store whatever the settings say
	// (tests).
	Store Store
	// WikiConfigMap keeps the wiki in the ConfigMap <release>-wiki: the
	// temporary store, used while the settings name none (CF-57); nil
	// leaves it out, and the wiki is kept nowhere until a store is
	// chosen. WikiBytes is the most it holds (0: DefaultWikiBytes).
	WikiConfigMap kv.Backend
	WikiBytes     int
	// Model is how Mycroft is asked; nil reads sources and keeps their
	// excerpts, but writes no pages.
	Model Completer
	// Proposer receives Mycroft's inventory and drift proposals; nil drops
	// them.
	Proposer Proposer
	// Console is what the console itself knows; nil leaves that source out.
	// When it also lists the console's fleets (ConsoleFleets), the groups
	// of hosts the documents name are proposed as fleets the console lacks.
	Console Console
	// ReadNew reads a source at once, in the background, when it is added
	// (Mycroft's first import, GS-11), so the inventory it describes waits
	// in Cases › Inbox without waiting for the nightly sync.
	ReadNew bool
	// Publisher puts approved pages into Confluence; nil leaves them out.
	Publisher *confluence.Publisher
	// Leads says whether this replica leads (ADR-70); nil is always.
	Leads func() bool
	// Audit receives a record per change; nil records nothing.
	Audit func(audit.Event)
	// RefreshGit and RefreshS3 are how often the local copy is read again
	// from a git repository (default 15 minutes) or a bucket (5 minutes).
	RefreshGit, RefreshS3 time.Duration
	// HTTP reaches sources and stores (tests); nil is a client with a
	// minute's timeout per request.
	HTTP *http.Client
	// CrawlHTTP, when set, is how the web addresses people add are read
	// (tests, with httptest); nil is a client that refuses loopback,
	// link-local and multicast addresses (Phase 12, wave 5, MY-06).
	CrawlHTTP *http.Client
	// Identity is the pod's AWS identity, for a bucket without keys.
	Identity *awsv4.Identity
	// ConsoleURL is the console's address, for links back from pages.
	ConsoleURL string
	Now        func() time.Time
	Logger     *slog.Logger
}

// Service is Mycroft's knowledge.
type Service struct {
	o      Options
	kv     kv.Backend
	limits Limits

	// writeMu serialises this process's writes to the ConfigMap.
	writeMu sync.Mutex
	// syncMu lets one sync run at a time; syncing says one runs.
	syncMu sync.Mutex
	// bg counts the first reads running in the background.
	bg      sync.WaitGroup
	syncing atomic.Bool
	runSeq  atomic.Int64

	// mu guards the local copy, the index and the store.
	mu       sync.RWMutex
	files    map[string]File
	manifest *Manifest
	index    *Index
	store    Store
	storeFor string
	pulled   time.Time
	// cmStore is the ConfigMap store, when the chart made its ConfigMap.
	cmStore *ConfigMapStore

	// open makes a store from typed settings (tests).
	open func(StoreSettings, Creds) (Store, error)

	// pending are uploaded documents read but not yet given to Mycroft.
	pendingMu sync.Mutex
	pending   map[string]pendingDoc
}

// New builds the service; Start reads the local copy.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Secrets == nil {
		o.Secrets = &MemorySecret{}
	}
	if o.Proposer == nil {
		o.Proposer = NopProposer{}
	}
	if o.Leads == nil {
		o.Leads = func() bool { return true }
	}
	if o.RefreshGit <= 0 {
		o.RefreshGit = 15 * time.Minute
	}
	if o.RefreshS3 <= 0 {
		o.RefreshS3 = 5 * time.Minute
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: time.Minute}
	}
	if o.Identity == nil {
		o.Identity = &awsv4.Identity{Session: "221b-knowledge"}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Dir == "" {
		o.Dir = filepath.Join(os.TempDir(), "221b-knowledge")
	}
	s := &Service{o: o, kv: o.Backend, limits: o.Limits.withDefaults(), files: map[string]File{}, manifest: newManifest(), index: NewIndex(nil, nil, 0, o.Now()), pending: map[string]pendingDoc{}}
	if o.WikiConfigMap != nil {
		s.cmStore = NewConfigMapStore(o.WikiConfigMap, o.WikiBytes)
	}
	// Runs are numbered from the clock, so a restart does not reuse one.
	s.runSeq.Store(o.Now().Unix() % 100000)
	return s
}

// Publisher is the Confluence publisher the service was given, or nil.
func (s *Service) Publisher() *confluence.Publisher { return s.o.Publisher }

func (s *Service) audit(e audit.Event) {
	if s.o.Audit != nil {
		s.o.Audit(e)
	}
}

// leads says whether this replica writes.
func (s *Service) leads() error {
	if !s.o.Leads() {
		return ErrNotLeader
	}
	return nil
}
