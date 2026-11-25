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
	"github.com/ucesys/221b/server/internal/kv"
)

// SecretStore keeps the credentials' secrets: one key each of the Secret
// <release>-credentials, read and written with its version
// (kube.SecretKey), "secret-<id>" each credential's secret part. The key
// "index" held the list without secrets before 0.13: it is read only by
// the one-time copy into the database (CopyList, DB-61).
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

// Keys names every key kept, as the Secret's own store does.
func (m *MemorySecret) Keys(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		out = append(out, k)
	}
	return out, nil
}

// DeleteKeyIf removes a key, as the Secret's own store does.
func (m *MemorySecret) DeleteKeyIf(_ context.Context, key, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return nil
	}
	if version != fmt.Sprint(m.version) {
		return errors.New("the Secret changed since it was read")
	}
	if _, ok := m.data[key]; ok {
		delete(m.data, key)
		m.version++
	}
	return nil
}

// Sizes are the bytes kept, keys counted, and those of one key.
func (m *MemorySecret) Sizes(_ context.Context, key string) (total, ofKey int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.data {
		total += len(k) + len(v)
		if k == key {
			ofKey = len(k) + len(v)
		}
	}
	return total, ofKey, nil
}

// keyDeleter is a store that removes a key, so a removed credential's
// secret leaves the Secret instead of staying as an empty key.
type keyDeleter interface {
	DeleteKeyIf(ctx context.Context, key, version string) error
}

// sizer is a store that says how big its Secret is: in all, and one key
// (its name and value).
type sizer interface {
	Sizes(ctx context.Context, key string) (total, ofKey int, err error)
}

// SecretBudget is the most the credentials' Secret is let hold: under the
// 1 MiB Kubernetes keeps in one Secret, with room for its metadata.
const SecretBudget = 900 << 10

// fits says whether a key may be written with n bytes (its name and
// value): a write that grows the Secret past SecretBudget is refused with
// why; one that does not grow the key always goes.
func (s *Service) fits(ctx context.Context, key string, n int) error {
	sz, ok := s.o.Secrets.(sizer)
	if !ok {
		return nil
	}
	total, cur, err := sz.Sizes(ctx, key)
	if err != nil || n <= cur {
		// The write itself says what is wrong with the store.
		return nil
	}
	if total-cur+n > SecretBudget {
		return invalid("the credentials would take over %d KiB, more than their Secret can hold: remove the credentials no longer used, or shorten long private CAs and descriptions", SecretBudget>>10)
	}
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
	// Repo keeps the list without secrets: the database's rows
	// (NewPGRepo, DB-60, DB-75), or memory (nil, NewMemoryRepo). The
	// secret parts stay in Secrets either way.
	Repo Repo
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
	if o.Repo == nil {
		o.Repo = NewMemoryRepo()
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

// conflict says a write lost to another writer and is to be tried again
// over what is kept now: the list's store says so with kv.ErrConflict,
// the Secret's with a sentence that it changed.
func conflict(err error) bool {
	return err != nil && (errors.Is(err, kv.ErrConflict) || strings.Contains(err.Error(), "changed"))
}

func (s *Service) saveSecret(ctx context.Context, id string, sec Secret) error {
	raw := []byte{}
	if !sec.empty() {
		raw, _ = json.Marshal(sec)
	}
	del, canDelete := s.o.Secrets.(keyDeleter)
	if !canDelete || len(raw) > 0 {
		if err := s.fits(ctx, secretKey(id), len(secretKey(id))+len(raw)); err != nil {
			return err
		}
	}
	for range 10 {
		_, version, _, err := s.o.Secrets.LoadKey(ctx, secretKey(id))
		if err != nil {
			return err
		}
		if len(raw) == 0 && canDelete {
			// None: the key goes.
			err = del.DeleteKeyIf(ctx, secretKey(id), version)
		} else {
			err = s.o.Secrets.SaveKeyIf(ctx, secretKey(id), raw, version)
		}
		if err == nil {
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
	out, err := s.o.Repo.List(ctx)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Credential{}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Get is one credential, without its secret.
func (s *Service) Get(ctx context.Context, id string) (Credential, error) {
	if s == nil {
		return Credential{}, fmt.Errorf("%w %q: this console keeps no credentials", ErrNotFound, id)
	}
	return s.o.Repo.Get(ctx, id)
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
	return s.o.Repo.Migration(ctx)
}

// DismissMigration hides the list of what the upgrade made.
func (s *Service) DismissMigration(ctx context.Context) error {
	return s.o.Repo.UpdateMigration(ctx, func(m *Migration) error { m.Dismissed = true; return nil })
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
	if err := s.o.Repo.Add(ctx, c, MaxCredentials); err != nil {
		return err
	}
	if err := s.saveSecret(ctx, c.ID, sec); err != nil {
		// The list does not name a credential whose secret is not kept.
		_, _ = s.o.Repo.Remove(ctx, c.ID)
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
	// The host key is written and never read back (CR-02): not given, an
	// SSH key keeps its own.
	if c.Kind == KindSSH && old.Kind == KindSSH && strings.TrimSpace(c.HostKey) == "" {
		c.HostKey = old.HostKey
	}
	if err := c.Check(next); err != nil {
		return Credential{}, err
	}
	c.ID, c.Created, c.CreatedBy, c.From, c.Set = id, old.Created, old.CreatedBy, old.From, next.parts()
	c.Changed, c.ChangedBy = s.o.Now().UTC(), by
	// The last test is kept while nothing it went by changed: everything
	// but the name, the description and the expiry (credentials-cut-2).
	resetTest := sec != nil || c.Kind != old.Kind || c.Endpoint != old.Endpoint || c.SendAs != old.SendAs || c.CAPEM != old.CAPEM ||
		c.Username != old.Username || c.Region != old.Region || c.HostKey != old.HostKey || c.AppID != old.AppID || c.InstallationID != old.InstallationID ||
		c.TokenURL != old.TokenURL || c.ClientID != old.ClientID || c.Scope != old.Scope || c.CertPEM != old.CertPEM
	// A name another credential has is refused before the secret is
	// written (credentials#3); the list's own refusal below still holds
	// for one taken meanwhile.
	if list, err := s.o.Repo.List(ctx); err != nil {
		return Credential{}, err
	} else if slices.ContainsFunc(list, func(o Credential) bool { return o.ID != id && strings.EqualFold(o.Name, c.Name) }) {
		return Credential{}, errTaken(c.Name)
	}
	wrote := next != have
	if wrote {
		if err := s.saveSecret(ctx, id, next); err != nil {
			return Credential{}, err
		}
	}
	saved, err := s.o.Repo.Update(ctx, id, func(cur *Credential) error {
		// A pinned certificate is set by Trust alone, and kept while the
		// endpoint's host is the same (CR-16), and the last test while
		// nothing it went by changed: the ones kept now, not the ones read
		// before, which a Trust, a Forget or a Test may have changed.
		c.PinnedCert, c.LastTest = nil, nil
		if sameEndpointHost(c.Endpoint, cur.Endpoint) {
			c.PinnedCert = cur.PinnedCert
		}
		if !resetTest {
			c.LastTest = cur.LastTest
		}
		*cur = c
		return nil
	})
	if err != nil && wrote {
		// The edit is refused: the secret goes back to what it was, and
		// a credential removed meanwhile keeps none (credentials#3).
		back := have
		if errors.Is(err, ErrNotFound) {
			back = Secret{}
		}
		if rerr := s.saveSecret(ctx, id, back); rerr != nil {
			s.o.Logger.Error("a refused edit's secret could not be put back: give the credential's secret again", "credential", old.Name, "err", rerr)
		}
	}
	s.forget(id)
	if err != nil {
		return Credential{}, err
	}
	return saved, nil
}

// sameEndpointHost says whether two endpoints are on one host and port,
// with or without a scheme (credentials#12: a scheme-less endpoint has no
// host for url.Parse).
func sameEndpointHost(a, b string) bool {
	ea, err := ParseEndpoint(a)
	if err != nil {
		return false
	}
	eb, err := ParseEndpoint(b)
	return err == nil && ea.Host == eb.Host
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
	if _, err := s.o.Repo.Remove(ctx, id); err != nil {
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
	_, err := s.o.Repo.Update(ctx, id, func(c *Credential) error { c.LastTest = &t; return nil })
	if errors.Is(err, ErrNotFound) {
		// Removed while it was tested: nothing to keep.
		return nil
	}
	return err
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

// CleanCertificates takes out of every credential's certificate and
// private CA what is not a certificate: a private key pasted with its
// certificate before 0.13.3, which was kept, shown and backed up in clear
// (credentials#2). It says which credentials it changed, by id; the
// secrets and everything else stay as they are. It runs at each start.
func CleanCertificates(ctx context.Context, list Repo) ([]string, error) {
	creds, err := list.List(ctx)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, c := range creds {
		_, a := certificatesOnly(c.CertPEM)
		_, b := certificatesOnly(c.CAPEM)
		if len(a)+len(b) == 0 {
			continue
		}
		_, err := list.Update(ctx, c.ID, func(cur *Credential) error {
			cur.CertPEM, _ = certificatesOnly(cur.CertPEM)
			cur.CAPEM, _ = certificatesOnly(cur.CAPEM)
			return nil
		})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return changed, err
		}
		changed = append(changed, c.ID)
	}
	return changed, nil
}

// UnnamedSecrets are the ids of the secrets the Secret keeps that no
// credential of the list names (a database restored to an earlier time,
// a write cut short, a credential an older console made after the move):
// never used, and named in the log at each start. A store that cannot
// list its keys has none.
func (s *Service) UnnamedSecrets(ctx context.Context) ([]string, error) {
	ids, _, err := Unmatched(ctx, s.o.Repo, s.o.Secrets)
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

// RemoveUnnamedSecret takes the secret kept under secret-<id> out of the
// Secret, when no credential of the list names it (DB-75): refused
// (ErrInUse) when one does, and ErrNotFound when there is no such secret.
// The secret is not read.
func (s *Service) RemoveUnnamedSecret(ctx context.Context, id string) error {
	if id == "" || strings.ContainsAny(id, "/ ") {
		return fmt.Errorf("%w: no secret %q", ErrNotFound, id)
	}
	if c, err := s.o.Repo.Get(ctx, id); err == nil {
		return fmt.Errorf("%w: the credential %s names this secret; remove the credential in Administration › Credentials instead", ErrInUse, c.Name)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	// Until the list of before 0.13 is copied, any secret may be a
	// credential's, and an application, a repository or a source may name
	// one whose record is not here (credentials#4).
	named, settled, err := namedSecrets(ctx, s.o.Repo, s.o.Secrets, nil)
	if err != nil {
		return err
	}
	if !settled {
		return fmt.Errorf("%w: the credentials' list of before 0.13 is not copied into the database yet, and may name this secret; the log of the start says why the copy failed, and the next start tries again", ErrInUse)
	}
	if named[id] {
		return fmt.Errorf("%w: an application, a Helm repository or a source names a credential of this id; restore the credential, or choose another there first", ErrInUse)
	}
	if _, _, found, err := s.o.Secrets.LoadKey(ctx, secretKey(id)); err != nil {
		return fmt.Errorf("reading the credentials' Secret: %w", err)
	} else if !found {
		return fmt.Errorf("%w: no secret %q in the credentials' Secret", ErrNotFound, id)
	}
	return s.saveSecret(ctx, id, Secret{})
}
