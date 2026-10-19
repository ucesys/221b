package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"time"

	"github.com/ucesys/221b/server/internal/kube"
)

// How the accounts are kept where every replica sees them (P7-02), and in
// a format a newer or an older console can read (P7-03).

var (
	// ErrConflict is what a VersionedBackend returns when the accounts
	// changed since they were read; the change is made again on them.
	ErrConflict = errors.New("the saved accounts changed meanwhile")
	// ErrNewerAccounts refuses a change to accounts a newer console saved:
	// writing them would drop what this one does not know.
	ErrNewerAccounts = errors.New("the accounts were saved by a newer 221B; upgrade this one")
)

// accountsVersion is the format of the saved accounts. 0 is the format
// before versions; reading it as 1 needs nothing more.
const accountsVersion = 1

// VersionedBackend is a Backend whose writes are conditional, so two
// replicas changing accounts at once never undo each other: a write made
// over a version that is no longer current fails with ErrConflict.
type VersionedBackend interface {
	// LoadVersion returns the saved accounts and their version, or
	// ErrNoAccounts and the version to create them over.
	LoadVersion(ctx context.Context) ([]byte, string, error)
	// SaveIf writes the accounts if they are still at version.
	SaveIf(ctx context.Context, b []byte, version string) error
}

// LoadVersion returns what was saved and a version that changes with every
// save.
func (m *MemoryBackend) LoadVersion(ctx context.Context) ([]byte, string, error) {
	b, err := m.Load(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	return b, strconv.Itoa(m.saves), err
}

// SaveIf keeps b if nothing was saved since version.
func (m *MemoryBackend) SaveIf(_ context.Context, b []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version != strconv.Itoa(m.saves) {
		return ErrConflict
	}
	m.b = append([]byte(nil), b...)
	m.saves++
	return nil
}

// unversioned makes a plain Backend look versioned: its writes always go
// through, as before.
type unversioned struct{ Backend }

func (u unversioned) LoadVersion(ctx context.Context) ([]byte, string, error) {
	b, err := u.Load(ctx)
	return b, "", err
}

func (u unversioned) SaveIf(ctx context.Context, b []byte, _ string) error { return u.Save(ctx, b) }

func versioned(b Backend) VersionedBackend {
	if v, ok := b.(VersionedBackend); ok {
		return v
	}
	return unversioned{b}
}

// SecretBackend keeps the accounts in one key of a Secret of the console's
// namespace, written only over the version read.
func SecretBackend(sk *kube.SecretKey, key string) Backend { return secretBackend{sk: sk, key: key} }

type secretBackend struct {
	sk  *kube.SecretKey
	key string
}

func (s secretBackend) Load(ctx context.Context) ([]byte, error) {
	b, _, err := s.LoadVersion(ctx)
	return b, err
}

func (s secretBackend) LoadVersion(ctx context.Context) ([]byte, string, error) {
	b, version, found, err := s.sk.LoadKey(ctx, s.key)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, version, ErrNoAccounts
	}
	return b, version, nil
}

func (s secretBackend) Save(ctx context.Context, b []byte) error {
	_, version, err := s.LoadVersion(ctx)
	if err != nil && !errors.Is(err, ErrNoAccounts) {
		return err
	}
	return s.SaveIf(ctx, b, version)
}

func (s secretBackend) SaveIf(ctx context.Context, b []byte, version string) error {
	err := s.sk.SaveKeyIf(ctx, s.key, b, version)
	if errors.Is(err, kube.ErrConflict) {
		return ErrConflict
	}
	return err
}

// document is the saved accounts with what this version does not know of
// them, kept so that writing them back loses nothing (P7-03).
type document struct {
	format    int
	users     map[string]*Account
	extra     map[string]json.RawMessage
	userExtra map[string]map[string]json.RawMessage
}

var (
	documentFields = []string{"version", "users"}
	accountFields  = []string{"username", "name", "email", "passwordHash", "groups", "super", "disabled", "created", "lastSignIn"}
)

func parseDocument(b []byte) (document, error) {
	var raw struct {
		Version int               `json:"version"`
		Users   []json.RawMessage `json:"users"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return document{}, fmt.Errorf("the saved accounts are not valid JSON: %w", err)
	}
	extra, err := unknownFields(b, documentFields)
	if err != nil {
		return document{}, err
	}
	d := document{format: raw.Version, users: map[string]*Account{}, extra: extra, userExtra: map[string]map[string]json.RawMessage{}}
	for _, ub := range raw.Users {
		var u Account
		if err := json.Unmarshal(ub, &u); err != nil {
			return document{}, fmt.Errorf("a saved account is not valid JSON: %w", err)
		}
		more, err := unknownFields(ub, accountFields)
		if err != nil {
			return document{}, err
		}
		d.users[u.Username] = &u
		if more != nil {
			d.userExtra[u.Username] = more
		}
	}
	return d, nil
}

func (d document) encode() ([]byte, error) {
	names := make([]string, 0, len(d.users))
	for n := range d.users {
		names = append(names, n)
	}
	sort.Strings(names)
	users := make([]json.RawMessage, 0, len(names))
	for _, n := range names {
		b, err := mergeKnown(d.users[n], d.userExtra[n])
		if err != nil {
			return nil, err
		}
		users = append(users, b)
	}
	return mergeKnown(struct {
		Version int               `json:"version"`
		Users   []json.RawMessage `json:"users"`
	}{accountsVersion, users}, d.extra)
}

// load reads the saved accounts into memory. It is called without a.mu,
// which it takes only to put what it read in place, so reading the
// backend never holds up a sign-in.
func (a *Accounts) load(ctx context.Context) error {
	b, version, err := a.backend.LoadVersion(ctx)
	none := errors.Is(err, ErrNoAccounts)
	if err != nil && !none {
		return err
	}
	var d document
	if !none {
		a.mu.Lock()
		// A version that names nothing ("rv:" from a store without
		// resource versions) is read every time.
		same := a.loaded && len(version) > 3 && version == a.version
		a.mu.Unlock()
		if same {
			return nil
		}
		if d, err = parseDocument(b); err != nil {
			return err
		}
	} else {
		d = document{users: map[string]*Account{}, userExtra: map[string]map[string]json.RawMessage{}}
	}
	a.mu.Lock()
	a.stored, a.extra, a.userExtra = d.users, d.extra, d.userExtra
	a.newer = d.format > accountsVersion
	a.version, a.loaded = version, true
	a.mu.Unlock()
	return nil
}

// change makes a change to the accounts as they are saved now: it reads
// them, applies fn and writes them back over the version read, and starts
// again when another replica wrote in between. fn runs with a.mu held and
// returns an error to refuse the change, which then leaves everything as
// it was.
func (a *Accounts) change(ctx context.Context, fn func() error) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	for range 5 {
		if err := a.load(ctx); err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		a.mu.Lock()
		if a.newer {
			a.mu.Unlock()
			return ErrNewerAccounts
		}
		before := maps.Clone(a.stored)
		if err := fn(); err != nil {
			a.stored = before
			a.mu.Unlock()
			return err
		}
		b, err := document{users: a.stored, extra: a.extra, userExtra: a.userExtra}.encode()
		version := a.version
		a.mu.Unlock()
		if err == nil {
			err = a.backend.SaveIf(ctx, b, version)
		}
		a.mu.Lock()
		switch {
		case err == nil:
			// Read again next time: the new version is the backend's.
			a.version = ""
			a.mu.Unlock()
			return nil
		case errors.Is(err, ErrConflict):
			a.stored, a.version = before, ""
			a.mu.Unlock()
			continue
		default:
			a.stored = before
			a.mu.Unlock()
			return err
		}
	}
	return errors.New("the saved accounts kept changing; try again")
}

// Run reads the saved accounts again every interval until ctx is done, so
// a user created, changed or deleted on another replica counts here within
// that time (P7-02).
func (a *Accounts) Run(ctx context.Context, every time.Duration, onError func(error)) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := a.load(rctx)
			cancel()
			if err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
