package knowledge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Store is where the wiki is kept: a git repository or an S3 bucket,
// chosen per installation (ADR-95). Both hold the same files; what differs
// is how a change is staged for review and published once approved.
type Store interface {
	// Kind is StoreGit or StoreS3 (or "memory" in tests).
	Kind() string
	// Pull reads every approved file: the pages, the manifest and the
	// excerpts, with their versions.
	Pull(ctx context.Context) (map[string]File, error)
	// Write puts files straight into the approved tree: the manifest and
	// the excerpts, which are not reviewed. Pages go through Stage.
	Write(ctx context.Context, files []Change, msg string) error
	// Stage keeps a run's page changes for review: a branch in git, a
	// drafts prefix in S3.
	Stage(ctx context.Context, run string, changes []Change, msg string) error
	// Publish puts the named paths of a staged run into the approved tree,
	// each only if it did not change since it was read (its Base): those
	// that did come back as conflicts, unpublished, for a person.
	Publish(ctx context.Context, run string, changes []Change, msg string) (conflicts []string, err error)
	// Discard forgets a staged run.
	Discard(ctx context.Context, run string) error
	// Check reads the store's state: what a SuperAdmin should know of it,
	// such as a bucket without versioning.
	Check(ctx context.Context) (StoreCheck, error)
}

// Change is one file to write: its content and the version it was read
// at ("" for a new one). Body nil, in a Publish, is taken from the stage.
type Change struct {
	Path string `json:"path"`
	Body []byte `json:"-"`
	Base string `json:"base,omitempty"`
	// Delete removes the file, in a Write (the clean start, Phase 12
	// wave 6).
	Delete bool `json:"-"`
}

// StoreCheck is a store's state.
type StoreCheck struct {
	// Versioning is the bucket's ("Enabled", "Suspended", ""); empty
	// for git, which keeps every commit.
	Versioning string   `json:"versioning,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Files      int      `json:"files"`
}

// ErrConflictStore is a write the store refused because the file moved on.
var ErrConflictStore = errors.New("changed in the store since it was read")

// MemoryStore keeps a wiki in memory: for tests, and a console with no
// store yet in a test of its own.
type MemoryStore struct {
	mu     sync.Mutex
	files  map[string]File
	staged map[string]map[string][]byte
	seq    int
	// Writes counts what was written, by kind: "write", "stage",
	// "publish".
	Writes map[string]int
}

// NewMemoryStore returns an empty wiki in memory.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{files: map[string]File{}, staged: map[string]map[string][]byte{}, Writes: map[string]int{}}
}

func (m *MemoryStore) Kind() string { return "memory" }

func (m *MemoryStore) version() string { m.seq++; return fmt.Sprintf("m%d", m.seq) }

// Put writes a file as a person would, outside the console.
func (m *MemoryStore) Put(path string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = File{Path: path, Body: append([]byte(nil), body...), Version: m.version()}
}

func (m *MemoryStore) Pull(context.Context) (map[string]File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]File{}
	for p, f := range m.files {
		out[p] = File{Path: p, Body: append([]byte(nil), f.Body...), Version: f.Version}
	}
	return out, nil
}

func (m *MemoryStore) Write(_ context.Context, files []Change, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Writes["write"]++
	for _, c := range files {
		if c.Delete {
			delete(m.files, c.Path)
			continue
		}
		m.files[c.Path] = File{Path: c.Path, Body: append([]byte(nil), c.Body...), Version: m.version()}
	}
	return nil
}

func (m *MemoryStore) Stage(_ context.Context, run string, changes []Change, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Writes["stage"]++
	st := map[string][]byte{}
	for _, c := range changes {
		st[c.Path] = append([]byte(nil), c.Body...)
	}
	m.staged[run] = st
	return nil
}

func (m *MemoryStore) Publish(_ context.Context, run string, changes []Change, _ string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Writes["publish"]++
	st, ok := m.staged[run]
	if !ok {
		return nil, fmt.Errorf("%w: nothing is staged for %s", ErrNotFound, run)
	}
	var conflicts []string
	for _, c := range changes {
		if f, ok := m.files[c.Path]; (ok && f.Version != c.Base) || (!ok && c.Base != "") {
			conflicts = append(conflicts, c.Path)
			continue
		}
		body, ok := st[c.Path]
		if !ok {
			return nil, fmt.Errorf("%w: %s is not staged", ErrNotFound, c.Path)
		}
		m.files[c.Path] = File{Path: c.Path, Body: body, Version: m.version()}
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func (m *MemoryStore) Discard(_ context.Context, run string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.staged, run)
	return nil
}

func (m *MemoryStore) Check(context.Context) (StoreCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return StoreCheck{Files: len(m.files)}, nil
}

// wanted says whether a file belongs in the local copy: the pages, the
// manifest and the excerpts, and nothing else the repository or bucket
// holds.
func wanted(p string) bool {
	return p == ManifestPath || (strings.HasPrefix(p, PagesDir) && strings.HasSuffix(p, ".md")) || (strings.HasPrefix(p, ExcerptsDir) && strings.HasSuffix(p, ".json"))
}
