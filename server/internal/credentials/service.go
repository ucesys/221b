package credentials

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
)

// SecretStore keeps the credentials: one key each of the Secret
// <release>-credentials, read and written with its version
// (kube.SecretKey). The key "index" holds the list without secrets, and
// "secret-<id>" each credential's secret part.
type SecretStore interface {
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// MemorySecret keeps the credentials until the server restarts.
type MemorySecret struct {
	mu      sync.Mutex
	data    map[string][]byte
	version int
}

func (m *MemorySecret) LoadKey(_ context.Context, key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return nil, "", false, nil
	}
	b, ok := m.data[key]
	return append([]byte(nil), b...), fmt.Sprint(m.version), ok, nil
}

func (m *MemorySecret) SaveKeyIf(_ context.Context, key string, b []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.data == nil) != (version == "") || (m.data != nil && version != fmt.Sprint(m.version)) {
		return errors.New("the Secret changed since it was read")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte(nil), b...)
	m.version++
	return nil
}

const indexKey = "index"

func secretKey(id string) string { return "secret-" + id }

// MaxCredentials bounds the list.
const MaxCredentials = 200

// Ref is one use of a credential, as Administration › Credentials lists it
// under Used by: what uses it, and where that is set.
type Ref struct {
	Credential string `json:"-"`
	What       string `json:"what"`
	Where      string `json:"where,omitempty"`
}

// UsersFunc lists the uses of credentials one part of the console makes:
// the applications, the Helm repositories, Mycroft's sources.
type UsersFunc func(ctx context.Context) ([]Ref, error)

// Created is a credential the upgrade made (CR-12), as the page lists it
// until a SuperAdmin dismisses the list.
type Created struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	From []string `json:"from"`
}

// Migration is what the upgrade did (CR-12).
type Migration struct {
	At      time.Time `json:"at,omitzero"`
	Created []Created `json:"created,omitempty"`
	// Notes are what it left as it was, and why.
	Notes     []string `json:"notes,omitempty"`
	Dismissed bool     `json:"dismissed,omitempty"`
}

type index struct {
	Credentials []Credential `json:"credentials"`
	Migration   Migration    `json:"migration,omitzero"`
}

// Options configure the service.
type Options struct {
	// Secrets keeps the credentials; nil is memory.
	Secrets SecretStore
	// HTTP fetches OAuth2 and GitHub App tokens and runs the tests; nil is
	// one that refuses the console's own pod and node and the clouds'
	// metadata services. Tests give their own.
	HTTP *http.Client
	// Dial connects a Test's and a Trust's look at a certificate (CR-15);
	// nil refuses what HTTP's default refuses. Tests give their own.
	Dial   certpin.DialFunc
	Now    func() time.Time
	Logger *slog.Logger
}

// Service keeps the credentials and resolves them for their uses.
type Service struct {
	o     Options
	mu    sync.Mutex
	users []UsersFunc
	// minted are the OAuth2 and GitHub App tokens fetched, by credential,
	// until a little before they expire.
	minted map[string]minted
}

type minted struct {
	token   string
	expires time.Time
	// changed is the credential's Changed when it was minted: a credential
	// changed since mints again.
	changed time.Time
}

// New makes the service.
func New(o Options) *Service {
	if o.Secrets == nil {
		o.Secrets = &MemorySecret{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.HTTP == nil {
		o.HTTP = guardedClient(nil)
	}
	if o.Dial == nil {
		o.Dial = (&net.Dialer{Timeout: 15 * time.Second, Control: guardedControl}).DialContext
	}
	return &Service{o: o, minted: map[string]minted{}}
}

// Register adds a part of the console that uses credentials, for Used by
// and for refusing to delete one in use.
func (s *Service) Register(f UsersFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = append(s.users, f)
}

func (s *Service) load(ctx context.Context) (index, string, error) {
	var ix index
	b, version, found, err := s.o.Secrets.LoadKey(ctx, indexKey)
	if err != nil {
		return ix, "", fmt.Errorf("reading the credentials: %w", err)
	}
	if found && len(b) > 0 {
		if err := json.Unmarshal(b, &ix); err != nil {
			return ix, "", fmt.Errorf("the credentials' list cannot be read: %w", err)
		}
	}
	return ix, version, nil
}

// update changes the list over the version read, with the secrets to
// write in the same go (one key per write: each its own try).
func (s *Service) update(ctx context.Context, f func(*index) error) error {
	for range 10 {
		ix, version, err := s.load(ctx)
		if err != nil {
			return err
		}
		if err := f(&ix); err != nil {
			return err
		}
		b, _ := json.Marshal(ix)
		if err := s.o.Secrets.SaveKeyIf(ctx, indexKey, b, version); err != nil {
			if conflict(err) {
				continue
			}
			return fmt.Errorf("saving the credentials: %w", err)
		}
		return nil
	}
	return errors.New("the credentials kept changing: try again")
}

func conflict(err error) bool { return err != nil && strings.Contains(err.Error(), "changed") }

func (s *Service) saveSecret(ctx context.Context, id string, sec Secret) error {
	raw := []byte{}
	if !sec.empty() {
		raw, _ = json.Marshal(sec)
	}
	for range 10 {
		_, version, _, err := s.o.Secrets.LoadKey(ctx, secretKey(id))
		if err != nil {
			return err
		}
		if err = s.o.Secrets.SaveKeyIf(ctx, secretKey(id), raw, version); err == nil {
			return nil
		} else if !conflict(err) {
			return fmt.Errorf("saving the credential's secret: %w", err)
		}
	}
	return errors.New("the credentials' Secret kept changing: try again")
}

func (s *Service) secret(ctx context.Context, id string) (Secret, error) {
	var sec Secret
	b, _, found, err := s.o.Secrets.LoadKey(ctx, secretKey(id))
	if err != nil || !found || len(b) == 0 {
		return sec, err
	}
	if err := json.Unmarshal(b, &sec); err != nil {
		return Secret{}, fmt.Errorf("the secret of the credential %s cannot be read", id)
	}
	return sec, nil
}

// List is every credential, by name.
func (s *Service) List(ctx context.Context) ([]Credential, error) {
	if s == nil {
		return []Credential{}, nil
	}
	ix, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := append([]Credential{}, ix.Credentials...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Get is one credential, without its secret.
func (s *Service) Get(ctx context.Context, id string) (Credential, error) {
	if s == nil {
		return Credential{}, fmt.Errorf("%w %q: this console keeps no credentials", ErrNotFound, id)
	}
	ix, _, err := s.load(ctx)
	if err != nil {
		return Credential{}, err
	}
	for _, c := range ix.Credentials {
		if c.ID == id {
			return c, nil
		}
	}
	return Credential{}, fmt.Errorf("%w %q: Administration › Credentials lists them", ErrNotFound, id)
}

// ByName is one credential by its name, any case.
func (s *Service) ByName(ctx context.Context, name string) (Credential, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Credential{}, err
	}
	for _, c := range list {
		if strings.EqualFold(c.Name, strings.TrimSpace(name)) {
			return c, nil
		}
	}
	return Credential{}, fmt.Errorf("%w named %q: Administration › Credentials lists them", ErrNotFound, name)
}

// Migration is what the upgrade made, for the page.
func (s *Service) Migration(ctx context.Context) (Migration, error) {
	ix, _, err := s.load(ctx)
	return ix.Migration, err
}

// DismissMigration hides the list of what the upgrade made.
func (s *Service) DismissMigration(ctx context.Context) error {
	return s.update(ctx, func(ix *index) error { ix.Migration.Dismissed = true; return nil })
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "cr-" + hex.EncodeToString(b)
}

// Create adds a credential with its secret (CR-05).
func (s *Service) Create(ctx context.Context, c Credential, sec Secret, by string) (Credential, error) {
	sec = keep(c.Kind, sec.trimmed())
	if err := c.Check(sec); err != nil {
		return Credential{}, err
	}
	now := s.o.Now().UTC()
	c.ID, c.Set, c.LastTest, c.From, c.PinnedCert = newID(), sec.parts(), nil, nil, nil
	c.Created, c.CreatedBy, c.Changed, c.ChangedBy = now, by, now, by
	return c, s.add(ctx, c, sec)
}

func (s *Service) add(ctx context.Context, c Credential, sec Secret) error {
	if err := s.update(ctx, func(ix *index) error {
		if len(ix.Credentials) >= MaxCredentials {
			return invalid("at most %d credentials; remove one first", MaxCredentials)
		}
		if slices.ContainsFunc(ix.Credentials, func(o Credential) bool { return strings.EqualFold(o.Name, c.Name) }) {
			return invalid("there is a credential named %s already", c.Name)
		}
		ix.Credentials = append(ix.Credentials, c)
		return nil
	}); err != nil {
		return err
	}
	if err := s.saveSecret(ctx, c.ID, sec); err != nil {
		// The list does not name a credential whose secret is not kept.
		_ = s.update(ctx, func(ix *index) error {
			ix.Credentials = slices.DeleteFunc(ix.Credentials, func(o Credential) bool { return o.ID == c.ID })
			return nil
		})
		return err
	}
	return nil
}

// Update changes a credential (CR-05): its secret is replaced when sec is
// given, and kept otherwise; a new kind needs its secret. The new secret
// is what every use of it sends from then on.
func (s *Service) Update(ctx context.Context, id string, c Credential, sec *Secret, by string) (Credential, error) {
	old, err := s.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	have, err := s.secret(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	next := have
	if sec != nil {
		next = sec.trimmed()
	} else if c.Kind != old.Kind && c.Kind != KindNone {
		return Credential{}, invalid("a credential of another kind needs its secret: give it")
	}
	next = keep(c.Kind, next)
	if err := c.Check(next); err != nil {
		return Credential{}, err
	}
	c.ID, c.Created, c.CreatedBy, c.From, c.Set = id, old.Created, old.CreatedBy, old.From, next.parts()
	c.Changed, c.ChangedBy = s.o.Now().UTC(), by
	c.LastTest = old.LastTest
	// A pinned certificate is set by Trust alone, and kept while the
	// endpoint's host is the same (CR-16).
	c.PinnedCert = nil
	if hostOf(c.Endpoint) == hostOf(old.Endpoint) {
		c.PinnedCert = old.PinnedCert
	}
	if sec != nil || c.Kind != old.Kind || c.Endpoint != old.Endpoint || c.SendAs != old.SendAs || c.CAPEM != old.CAPEM {
		c.LastTest = nil
	}
	if next != have {
		if err := s.saveSecret(ctx, id, next); err != nil {
			return Credential{}, err
		}
	}
	err = s.update(ctx, func(ix *index) error {
		i := slices.IndexFunc(ix.Credentials, func(o Credential) bool { return o.ID == id })
		if i < 0 {
			return fmt.Errorf("%w %q", ErrNotFound, id)
		}
		if slices.ContainsFunc(ix.Credentials, func(o Credential) bool { return o.ID != id && strings.EqualFold(o.Name, c.Name) }) {
			return invalid("there is a credential named %s already", c.Name)
		}
		ix.Credentials[i] = c
		return nil
	})
	s.forget(id)
	return c, err
}

// UsedBy is each credential's uses, by id.
func (s *Service) UsedBy(ctx context.Context) (map[string][]Ref, error) {
	s.mu.Lock()
	users := append([]UsersFunc(nil), s.users...)
	s.mu.Unlock()
	out := map[string][]Ref{}
	for _, f := range users {
		refs, err := f(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if r.Credential != "" {
				out[r.Credential] = append(out[r.Credential], r)
			}
		}
	}
	return out, nil
}

// Delete removes a credential that nothing uses (CR-06).
func (s *Service) Delete(ctx context.Context, id string) (Credential, error) {
	old, err := s.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	used, err := s.UsedBy(ctx)
	if err != nil {
		return Credential{}, err
	}
	if refs := used[id]; len(refs) > 0 {
		names := make([]string, 0, len(refs))
		for _, r := range refs {
			names = append(names, r.What)
		}
		return Credential{}, fmt.Errorf("%w: %s is used by %s; choose another credential there first", ErrInUse, old.Name, strings.Join(names, ", "))
	}
	if err := s.update(ctx, func(ix *index) error {
		ix.Credentials = slices.DeleteFunc(ix.Credentials, func(o Credential) bool { return o.ID == id })
		return nil
	}); err != nil {
		return Credential{}, err
	}
	if err := s.saveSecret(ctx, id, Secret{}); err != nil {
		s.o.Logger.Warn("a removed credential's secret stays in the Secret", "credential", id, "err", err)
	}
	s.forget(id)
	return old, nil
}

// NoteTest keeps a credential's last test.
func (s *Service) NoteTest(ctx context.Context, id string, t Test) error {
	return s.update(ctx, func(ix *index) error {
		for i := range ix.Credentials {
			if ix.Credentials[i].ID == id {
				ix.Credentials[i].LastTest = &t
			}
		}
		return nil
	})
}

// Matching are the credentials that serve a use at an address: the ones a
// picker offers, the one preselected when there is one (CR-10).
func (s *Service) Matching(ctx context.Context, use, target string) ([]Credential, error) {
	if s == nil {
		return nil, nil
	}
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Credential
	for _, c := range list {
		if c.Supports(use) == nil && c.Covers(target) {
			out = append(out, c)
		}
	}
	return out, nil
}

// CheckChoice checks the credential a consumer names for a use at an
// address: "" is none; another must be there, serve the use and cover the
// address.
func (s *Service) CheckChoice(ctx context.Context, id, use, target string) (Credential, error) {
	if id == "" {
		return Credential{}, nil
	}
	if s == nil {
		return Credential{}, invalid("this console keeps no credentials")
	}
	c, err := s.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	if err := c.Supports(use); err != nil {
		return Credential{}, err
	}
	if !c.Covers(target) {
		return Credential{}, fmt.Errorf("%w: %s is for %s, not %s", ErrOutside, c.Name, c.Endpoint, target)
	}
	return c, nil
}

func (s *Service) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.minted, id)
}

// SecretValues are a credential's secret values, and its minted token,
// for scrubbing what is written about its uses: never shown.
func (s *Service) SecretValues(ctx context.Context, id string) []string {
	sec, err := s.secret(ctx, id)
	if err != nil {
		return nil
	}
	out := sec.values()
	s.mu.Lock()
	if m, ok := s.minted[id]; ok && m.token != "" {
		out = append(out, m.token)
	}
	s.mu.Unlock()
	return out
}
