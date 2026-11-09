package apps

// --- Helm repositories (Phase 12, wave 5, HR-01 to HR-12, ADR-123 to
// ADR-125) ---
//
// Charts come from Helm repositories and OCI registries as well as from
// git (ADR-123). The repositories the console knows are a list that
// SuperAdmins keep in Settings › Applications: a name, the address, and
// how to sign in. The list is a key of the settings ConfigMap; each
// password or token is a key of the Secret <release>-helm-repositories,
// and nowhere else: not in a record, a card, a log, an audit record, a
// backup or a browser's response (X-01).
//
// A chart is fetched as an archive: a repository's index.yaml says where
// each version is and its digest, a registry's manifest names the chart's
// layer and its digest. The archive is checked against the digest and
// its size, and unpacked in memory, where it reads as a git checkout
// does: Compose, Render, Inspect and Watson's deploys work on it as they
// are. Its digest stands for the commit, so a card deploys the archive it
// showed or nothing (ADR-124).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"golang.org/x/mod/semver"
	"helm.sh/helm/v4/pkg/chart/loader/archive"
	"sigs.k8s.io/yaml"

	"github.com/ucesys/221b/server/internal/kv"
)

// RepositoriesKey is the list's key in the settings ConfigMap, and
// DefaultRepositoriesBytes the most it holds.
const (
	RepositoriesKey          = "helm-repositories.json"
	DefaultRepositoriesBytes = 16 << 10
	// MaxRepositories is how many the list holds.
	MaxRepositories = 50
)

// How a repository is signed in to (HR-02).
const (
	AuthNone  = "none"
	AuthBasic = "basic"
	AuthToken = "token"
)

// What a fetch from a repository holds at most.
const (
	maxIndex    = 32 << 20
	maxArchive  = 16 << 20
	maxManifest = 1 << 20
	maxSmall    = 256 << 10
	// indexFor is how long a repository's index is kept before it is read
	// again.
	indexFor = 2 * time.Minute
)

// repoName is a repository's name: what a helm line names it by
// (repo/chart).
var repoName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrNoRepository is a chart of a repository the console does not know.
var ErrNoRepository = errors.New("no such chart repository")

// Repository is a Helm repository or an OCI registry the console knows,
// as Settings › Applications lists it (HR-02).
type Repository struct {
	Name string `json:"name"`
	// Kind is helm (a chart repository over HTTPS, with its index.yaml)
	// or oci (an OCI registry).
	Kind string `json:"kind"`
	// URL is https://charts.example.com/stable, or
	// oci://registry.example.com[/path]: an OCI registry's entry covers
	// the charts under its path.
	URL string `json:"url"`
	// Auth is none, basic (a user name and a password) or token (a bearer
	// token, or a registry's access token).
	Auth     string `json:"auth"`
	Username string `json:"username,omitempty"`
	// HasSecret says a password or token is kept; it never leaves the
	// server.
	HasSecret bool `json:"hasSecret"`
	// The last test (HR-03): when, whether it answered, and what it said.
	TestedAt time.Time `json:"testedAt,omitzero"`
	TestOK   bool      `json:"testOk,omitempty"`
	Tested   string    `json:"tested,omitempty"`
	SetBy    string    `json:"setBy,omitempty"`
	SetAt    time.Time `json:"setAt,omitzero"`
}

// RepositoryInput is a repository as Settings › Applications sends it.
type RepositoryInput struct {
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`
	// Secret is the password or token: a value replaces the kept one, ""
	// removes it, nil keeps it.
	Secret *string `json:"secret,omitempty"`
}

// TestResult is what a test of a repository found (HR-03).
type TestResult struct {
	OK   bool   `json:"ok"`
	Text string `json:"text"`
	// Charts counts a Helm repository's charts.
	Charts int `json:"charts,omitempty"`
}

// ChartInfo is a chart a Helm repository's index lists.
type ChartInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Latest is its newest version, AppVersion that version's app.
	Latest     string `json:"latest"`
	AppVersion string `json:"appVersion,omitempty"`
	Deprecated bool   `json:"deprecated,omitempty"`
	Versions   int    `json:"versions"`
}

// RepoSecrets keeps the passwords and tokens: one key each of the Secret
// <release>-helm-repositories, read and written with its version
// (kube.SecretKey).
type RepoSecrets interface {
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// MemorySecret keeps the passwords and tokens until the server restarts.
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

func secretKey(name string) string { return "repo-" + name }

// RepoOptions configure the repositories.
type RepoOptions struct {
	// Backend keeps the list (the settings ConfigMap); nil is memory.
	Backend kv.Backend
	// Secrets keeps the passwords and tokens; nil is memory.
	Secrets RepoSecrets
	// Client fetches from the repositories; nil is the console's, which
	// connects where a git fetch may (repohost.go). Tests give their own.
	Client *http.Client
	Now    func() time.Time
}

// Repos are the chart repositories the console knows, and the fetching
// of their charts. A nil *Repos knows none and fetches from git alone.
type Repos struct {
	o  RepoOptions
	mu sync.Mutex
	// index are the repositories' indexes, read a little while ago.
	index map[string]cachedIndex
}

type cachedIndex struct {
	at  time.Time
	idx *repoIndex
}

// NewRepos makes the repositories.
func NewRepos(o RepoOptions) *Repos {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Secrets == nil {
		o.Secrets = &MemorySecret{}
	}
	if o.Client == nil {
		o.Client = &http.Client{Transport: repoTransport, Timeout: fetchTimeout}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Repos{o: o, index: map[string]cachedIndex{}}
}

// Backend is where the list is kept, for a backup.
func (r *Repos) Backend() kv.Backend { return r.o.Backend }

type repoRecord struct {
	Repositories []Repository `json:"repositories"`
}

func (r *Repos) load(ctx context.Context) (repoRecord, string, error) {
	var rec repoRecord
	raw, version, err := r.o.Backend.LoadKey(ctx, RepositoriesKey)
	if errors.Is(err, kv.ErrNotFound) {
		return rec, version, nil
	}
	if err != nil {
		return rec, "", err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, "", fmt.Errorf("the chart repositories' record cannot be read: %w", err)
	}
	return rec, version, nil
}

// update changes the list over the version read, again when another
// replica wrote first.
func (r *Repos) update(ctx context.Context, f func(*repoRecord) error) error {
	for range 10 {
		rec, version, err := r.load(ctx)
		if err != nil {
			return err
		}
		if err := f(&rec); err != nil {
			return err
		}
		b, _ := json.Marshal(rec)
		if _, err = r.o.Backend.SaveKey(ctx, RepositoriesKey, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return errors.New("the chart repositories' record kept changing")
}

// List is the repositories, by name.
func (r *Repos) List(ctx context.Context) ([]Repository, error) {
	if r == nil {
		return []Repository{}, nil
	}
	rec, _, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	out := append([]Repository{}, rec.Repositories...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get is one repository, with its password or token ("" for none).
func (r *Repos) Get(ctx context.Context, name string) (Repository, string, error) {
	if r == nil {
		return Repository{}, "", fmt.Errorf("%w %q: this console keeps no chart repositories", ErrNoRepository, name)
	}
	rec, _, err := r.load(ctx)
	if err != nil {
		return Repository{}, "", err
	}
	i := slices.IndexFunc(rec.Repositories, func(x Repository) bool { return x.Name == name })
	if i < 0 {
		return Repository{}, "", fmt.Errorf("%w %q: Settings › Applications lists the repositories the console knows", ErrNoRepository, name)
	}
	repo := rec.Repositories[i]
	secret := ""
	if repo.HasSecret {
		b, _, found, err := r.o.Secrets.LoadKey(ctx, secretKey(name))
		if err != nil {
			return Repository{}, "", err
		}
		if found {
			secret = string(b)
		}
	}
	return repo, secret, nil
}

// CheckRepository checks a repository as typed and returns it cleaned:
// its name, its kind and address, and how it is signed in to.
func CheckRepository(name string, in RepositoryInput) (Repository, error) {
	repo := Repository{Name: strings.TrimSpace(name), Kind: strings.TrimSpace(in.Kind), Auth: strings.TrimSpace(in.Auth), Username: strings.TrimSpace(in.Username)}
	if !repoName.MatchString(repo.Name) {
		return repo, errors.New("a repository's name is lowercase letters, digits and dashes, up to 63, as a helm line names it (name/chart)")
	}
	if repo.Kind != KindHelm && repo.Kind != KindOCI {
		return repo, errors.New("a repository is a Helm repository (helm) or an OCI registry (oci)")
	}
	u, err := RepoURL(repo.Kind, in.URL)
	if err != nil {
		return repo, err
	}
	repo.URL = u
	switch repo.Auth {
	case "", AuthNone:
		repo.Auth, repo.Username = AuthNone, ""
	case AuthBasic:
		if repo.Username == "" || len(repo.Username) > 200 || strings.ContainsAny(repo.Username, ":\r\n") {
			return repo, errors.New("a user name is needed for basic sign-in, without a colon or a line break")
		}
	case AuthToken:
		if len(repo.Username) > 200 || strings.ContainsAny(repo.Username, ":\r\n") {
			return repo, errors.New("a user name has no colon or line break")
		}
	default:
		return repo, errors.New("a repository is signed in to with none, basic or token")
	}
	if in.Secret != nil && (len(*in.Secret) > 8<<10 || strings.ContainsAny(*in.Secret, "\r\n")) {
		return repo, errors.New("a password or token is one line, at most 8 KiB")
	}
	return repo, nil
}

// Save adds or changes a repository (HR-02) and says whether it added
// it. A password or token belongs to its address: one moved elsewhere
// takes a new one or none.
func (r *Repos) Save(ctx context.Context, name string, in RepositoryInput, by string) (Repository, bool, error) {
	repo, err := CheckRepository(name, in)
	if err != nil {
		return Repository{}, false, fmt.Errorf("%w: %v", ErrInvalidRepo, err)
	}
	old, oldSecret, err := r.Get(ctx, repo.Name)
	created := errors.Is(err, ErrNoRepository)
	if err != nil && !created {
		return Repository{}, false, err
	}
	secret := oldSecret
	switch {
	case repo.Auth == AuthNone:
		secret = ""
	case in.Secret != nil:
		secret = strings.TrimSpace(*in.Secret)
	case !created && old.URL != repo.URL && old.HasSecret:
		return Repository{}, false, fmt.Errorf("%w: the address changed, and the password or token kept belongs to the old one: give it again", ErrInvalidRepo)
	}
	if repo.Auth != AuthNone && secret == "" {
		return Repository{}, false, fmt.Errorf("%w: give the password or token, or sign in with none", ErrInvalidRepo)
	}
	if secret != oldSecret {
		if err := r.saveSecret(ctx, repo.Name, secret); err != nil {
			return Repository{}, false, err
		}
	}
	repo.HasSecret = secret != ""
	repo.SetBy, repo.SetAt = by, r.o.Now().UTC()
	if !created && old.URL == repo.URL && old.Auth == repo.Auth && old.Username == repo.Username && secret == oldSecret {
		repo.TestedAt, repo.TestOK, repo.Tested = old.TestedAt, old.TestOK, old.Tested
	}
	err = r.update(ctx, func(rec *repoRecord) error {
		i := slices.IndexFunc(rec.Repositories, func(x Repository) bool { return x.Name == repo.Name })
		if i < 0 {
			if len(rec.Repositories) >= MaxRepositories {
				return fmt.Errorf("%w: at most %d repositories; remove one first", ErrInvalidRepo, MaxRepositories)
			}
			rec.Repositories = append(rec.Repositories, repo)
			return nil
		}
		rec.Repositories[i] = repo
		return nil
	})
	if err != nil {
		return Repository{}, false, err
	}
	r.forget(repo.Name)
	return repo, created, nil
}

// ErrInvalidRepo is a repository or a chart that is not valid.
var ErrInvalidRepo = errors.New("not valid")

func (r *Repos) saveSecret(ctx context.Context, name, secret string) error {
	for range 5 {
		_, version, _, err := r.o.Secrets.LoadKey(ctx, secretKey(name))
		if err != nil {
			return err
		}
		if err = r.o.Secrets.SaveKeyIf(ctx, secretKey(name), []byte(secret), version); err == nil {
			return nil
		} else if !strings.Contains(err.Error(), "changed") {
			return err
		}
	}
	return errors.New("the repositories' Secret kept changing")
}

// Delete removes a repository and its password or token. The
// applications made from it keep their records; their next deploy says
// the repository is gone.
func (r *Repos) Delete(ctx context.Context, name string) error {
	old, _, err := r.Get(ctx, name)
	if err != nil {
		return err
	}
	if old.HasSecret {
		if err := r.saveSecret(ctx, name, ""); err != nil {
			return err
		}
	}
	err = r.update(ctx, func(rec *repoRecord) error {
		rec.Repositories = slices.DeleteFunc(rec.Repositories, func(x Repository) bool { return x.Name == name })
		return nil
	})
	r.forget(name)
	return err
}

// NoteTest keeps a saved repository's last test.
func (r *Repos) NoteTest(ctx context.Context, name string, t TestResult) error {
	now := r.o.Now().UTC()
	return r.update(ctx, func(rec *repoRecord) error {
		for i := range rec.Repositories {
			if rec.Repositories[i].Name == name {
				rec.Repositories[i].TestedAt, rec.Repositories[i].TestOK, rec.Repositories[i].Tested = now, t.OK, t.Text
			}
		}
		return nil
	})
}

func (r *Repos) forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.index, name)
}

// Resolve fills in where a repository chart comes from (HR-07): a Helm
// repository's chart takes its address from the repository named; an
// OCI chart takes the registry entry that covers its address, the
// longest, for its credentials, or none. A git source is as it is.
func (r *Repos) Resolve(ctx context.Context, src Source) (Source, error) {
	switch src.Kind {
	case KindHelm:
		repo, _, err := r.Get(ctx, src.Repository)
		if err != nil {
			return src, err
		}
		if repo.Kind != KindHelm {
			return src, fmt.Errorf("%w: %s is an OCI registry: name its charts as oci://%s/chart", ErrInvalidRepo, repo.Name, strings.TrimPrefix(repo.URL, "oci://"))
		}
		src.Repo = repo.URL
	case KindOCI:
		src.Repository = ""
		if r == nil {
			return src, nil
		}
		list, err := r.List(ctx)
		if err != nil {
			return src, err
		}
		best := -1
		for _, repo := range list {
			if repo.Kind == KindOCI && (src.Repo == repo.URL || strings.HasPrefix(src.Repo, repo.URL+"/")) && len(repo.URL) > best {
				best, src.Repository = len(repo.URL), repo.Name
			}
		}
	}
	return src, nil
}

// credentials are a chart's: its repository's, or none.
func (r *Repos) credentials(ctx context.Context, src Source) (Repository, string, error) {
	if src.Repository == "" {
		return Repository{Auth: AuthNone, Kind: src.Kind, URL: src.Repo}, "", nil
	}
	repo, secret, err := r.Get(ctx, src.Repository)
	if err != nil {
		return Repository{}, "", err
	}
	// A credential goes to its own address only.
	if src.Kind == KindOCI && !(src.Repo == repo.URL || strings.HasPrefix(src.Repo, repo.URL+"/")) || src.Kind == KindHelm && src.Repo != repo.URL {
		return Repository{}, "", fmt.Errorf("%w: the chart's address is not %s's", ErrInvalidRepo, repo.Name)
	}
	return repo, secret, nil
}

// Fetch fetches a source: a git repository at its ref with token, or a
// repository's chart at its version (the newest when none is named),
// with its repository's credentials.
func (r *Repos) Fetch(ctx context.Context, src Source, token string) (*Checkout, error) {
	if !src.FromRepository() {
		return Fetch(ctx, src, token)
	}
	if r == nil {
		return nil, errors.New("this console keeps no chart repositories")
	}
	src, err := r.Resolve(ctx, src)
	if err != nil {
		return nil, err
	}
	repo, secret, err := r.credentials(ctx, src)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	release, err := fetchSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if src.Kind == KindOCI {
		return r.fetchOCI(ctx, src, repo, secret)
	}
	return r.fetchHelm(ctx, src, repo, secret)
}

// FetchDigest fetches what a card showed: a git commit, or a
// repository's chart whose archive has the digest, or nothing.
func (r *Repos) FetchDigest(ctx context.Context, src Source, token, digest string) (*Checkout, error) {
	if !src.FromRepository() {
		return FetchCommit(ctx, src, token, digest)
	}
	co, err := r.Fetch(ctx, src, token)
	if err != nil {
		return nil, err
	}
	if co.Commit != digest {
		return nil, fmt.Errorf("the chart %s changed in its repository after the card was made (its archive is %s now, %s then)", src, ShortDigest(co.Commit), ShortDigest(digest))
	}
	return co, nil
}

// ShortDigest is a commit or an archive's digest, short.
func ShortDigest(d string) string {
	if rest, ok := strings.CutPrefix(d, "sha256:"); ok && len(rest) > 12 {
		return "sha256:" + rest[:12]
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Refs are what a source offers to deploy: a git repository's branches
// and tags, or a repository chart's versions as tags, newest first, the
// newest stable one its default.
func (r *Repos) Refs(ctx context.Context, src Source, token string) (Refs, error) {
	if !src.FromRepository() {
		return ListRefs(ctx, src.Repo, token)
	}
	versions, err := r.Versions(ctx, src)
	if err != nil {
		return Refs{}, err
	}
	return Refs{Default: Newest(versions), Branches: []string{}, Tags: versions}, nil
}

// Versions are a repository chart's versions, newest first.
func (r *Repos) Versions(ctx context.Context, src Source) ([]string, error) {
	if r == nil {
		return nil, errors.New("this console keeps no chart repositories")
	}
	src, err := r.Resolve(ctx, src)
	if err != nil {
		return nil, err
	}
	repo, secret, err := r.credentials(ctx, src)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var out []string
	if src.Kind == KindOCI {
		out, err = r.ociTags(ctx, src, repo, secret)
	} else {
		var idx *repoIndex
		if idx, err = r.indexOf(ctx, repo, secret); err == nil {
			entries := idx.Entries[src.Chart]
			if len(entries) == 0 {
				return nil, fmt.Errorf("%s has no chart %s", repo.Name, src.Chart)
			}
			for _, e := range entries {
				out = append(out, e.Version)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	sortTags(out)
	return out, nil
}

// Charts are what a Helm repository's index lists (HR-05).
func (r *Repos) Charts(ctx context.Context, name string) ([]ChartInfo, error) {
	repo, secret, err := r.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if repo.Kind != KindHelm {
		return nil, fmt.Errorf("%w: an OCI registry does not list its charts; type the chart's name", ErrInvalidRepo)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	idx, err := r.indexOf(ctx, repo, secret)
	if err != nil {
		return nil, err
	}
	out := []ChartInfo{}
	for name, entries := range idx.Entries {
		if len(entries) == 0 {
			continue
		}
		var versions []string
		for _, e := range entries {
			versions = append(versions, e.Version)
		}
		sortTags(versions)
		latest := Newest(versions)
		info := ChartInfo{Name: name, Latest: latest, Versions: len(entries)}
		for _, e := range entries {
			if e.Version == latest {
				info.Description, info.AppVersion, info.Deprecated = clipText(e.Description, 300), e.AppVersion, e.Deprecated
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Test asks a repository whether it answers, with its credentials (HR-03):
// a Helm repository for its index, an OCI registry at /v2/.
func (r *Repos) Test(ctx context.Context, repo Repository, secret string) TestResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if repo.Kind == KindOCI {
		host := ociHost(repo.URL)
		if _, err := r.ociGet(ctx, host, "/v2/", "", "", repo, secret, maxSmall); err != nil {
			return TestResult{Text: err.Error()}
		}
		who := "anonymously"
		if repo.Auth != AuthNone {
			who = "with the credentials given"
		}
		return TestResult{OK: true, Text: fmt.Sprintf("%s answered %s.", host, who)}
	}
	r.forget(repo.Name)
	idx, err := r.fetchIndex(ctx, repo, secret)
	if err != nil {
		return TestResult{Text: err.Error()}
	}
	n := len(idx.Entries)
	return TestResult{OK: true, Charts: n, Text: fmt.Sprintf("The repository answered: %d %s in its index.", n, map[bool]string{true: "chart", false: "charts"}[n == 1])}
}

// Newest is the newest stable version of a list sorted newest first, or
// its first.
func Newest(versions []string) string {
	for _, v := range versions {
		if sv := semverOf(v); sv != "" && semver.Prerelease(sv) == "" {
			return v
		}
	}
	if len(versions) > 0 {
		return versions[0]
	}
	return ""
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---------- Helm repositories ----------

type repoIndex struct {
	Entries map[string][]indexEntry `json:"entries"`
}

type indexEntry struct {
	Version     string   `json:"version"`
	AppVersion  string   `json:"appVersion,omitempty"`
	Description string   `json:"description,omitempty"`
	URLs        []string `json:"urls"`
	Digest      string   `json:"digest,omitempty"`
	Created     string   `json:"created,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
}

// indexOf is a repository's index, read again after indexFor.
func (r *Repos) indexOf(ctx context.Context, repo Repository, secret string) (*repoIndex, error) {
	r.mu.Lock()
	c, ok := r.index[repo.Name]
	r.mu.Unlock()
	if ok && repo.Name != "" && r.o.Now().Sub(c.at) < indexFor {
		return c.idx, nil
	}
	idx, err := r.fetchIndex(ctx, repo, secret)
	if err != nil {
		return nil, err
	}
	if repo.Name != "" {
		r.mu.Lock()
		r.index[repo.Name] = cachedIndex{at: r.o.Now(), idx: idx}
		r.mu.Unlock()
	}
	return idx, nil
}

func (r *Repos) fetchIndex(ctx context.Context, repo Repository, secret string) (*repoIndex, error) {
	b, err := r.helmGet(ctx, repo, secret, repo.URL+"/index.yaml", maxIndex)
	if err != nil {
		return nil, fmt.Errorf("reading %s's index.yaml: %w", repo.URL, err)
	}
	var idx repoIndex
	if err := yaml.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("%s's index.yaml cannot be read: %w", repo.URL, err)
	}
	if idx.Entries == nil {
		return nil, fmt.Errorf("%s's index.yaml lists no charts: is it a Helm repository?", repo.URL)
	}
	return &idx, nil
}

// helmGet reads a Helm repository's file: with its credentials when the
// file is on the repository's own host, without them anywhere else (as
// helm does unless told to pass them on).
func (r *Repos) helmGet(ctx context.Context, repo Repository, secret, target string, limit int64) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || !(u.Scheme == "https" || AllowLocalRepos && u.Scheme == "http") {
		return nil, fmt.Errorf("%q is not an https:// address", target)
	}
	if refusedName(u.Hostname()) && !AllowLocalRepos {
		return nil, errHostRefused
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(repo.URL)
	if base != nil && strings.EqualFold(base.Host, u.Host) {
		authorize(req, repo, secret)
	}
	res, err := r.o.Client.Do(req)
	if err != nil {
		return nil, describeHTTP(err)
	}
	defer res.Body.Close()
	if err := statusError(res, repo); err != nil {
		return nil, err
	}
	return readCapped(res.Body, limit)
}

func authorize(req *http.Request, repo Repository, secret string) {
	switch {
	case secret == "":
	case repo.Auth == AuthBasic:
		req.SetBasicAuth(repo.Username, secret)
	case repo.Auth == AuthToken:
		req.Header.Set("Authorization", "Bearer "+secret)
	}
}

// fetchHelm fetches a Helm repository's chart at a version.
func (r *Repos) fetchHelm(ctx context.Context, src Source, repo Repository, secret string) (*Checkout, error) {
	idx, err := r.indexOf(ctx, repo, secret)
	if err != nil {
		return nil, err
	}
	entries := idx.Entries[src.Chart]
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s has no chart %s", repo.Name, src.Chart)
	}
	var versions []string
	for _, e := range entries {
		versions = append(versions, e.Version)
	}
	sortTags(versions)
	version := src.Ref
	if version == "" {
		version = Newest(versions)
	}
	i := slices.IndexFunc(entries, func(e indexEntry) bool { return e.Version == version })
	if i < 0 {
		return nil, fmt.Errorf("%s has no version %s of %s (it has %s)", repo.Name, version, src.Chart, strings.Join(versions[:min(len(versions), 5)], ", "))
	}
	e := entries[i]
	if len(e.URLs) == 0 {
		return nil, fmt.Errorf("%s's index gives no address for %s %s", repo.Name, src.Chart, version)
	}
	base, _ := url.Parse(repo.URL + "/")
	ref, err := url.Parse(e.URLs[0])
	if err != nil || base == nil {
		return nil, fmt.Errorf("%s's index gives %s %s an address that cannot be read", repo.Name, src.Chart, version)
	}
	b, err := r.helmGet(ctx, repo, secret, base.ResolveReference(ref).String(), maxArchive)
	if err != nil {
		return nil, fmt.Errorf("fetching %s %s: %w", src.Chart, version, err)
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	if want := strings.TrimPrefix(strings.ToLower(e.Digest), "sha256:"); want != "" && want != digest {
		return nil, fmt.Errorf("%s %s's archive does not match the digest its index gives: it was changed, or the index is out of date", src.Chart, version)
	}
	co, err := unpackChart(b, src.Chart, version)
	if err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, e.Created); err == nil {
		co.CommitDate = t.UTC()
	}
	return co, nil
}

// unpackChart reads a chart's archive into memory, as a checkout with the
// chart's files at its root, within FetchLimits.
func unpackChart(b []byte, chart, version string) (*Checkout, error) {
	if err := checkArchive(b); err != nil {
		return nil, err
	}
	files, err := archive.LoadArchiveFiles(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("the chart's archive cannot be read: %w", err)
	}
	fsys := memfs.New()
	for _, f := range files {
		name := path.Clean("/" + f.Name)
		if name == "/" {
			continue
		}
		if err := util.WriteFile(fsys, name, f.Data, 0o644); err != nil {
			return nil, fmt.Errorf("unpacking the chart: %w", err)
		}
	}
	if _, err := fsys.Stat("/Chart.yaml"); err != nil {
		return nil, errors.New("the archive holds no chart (no Chart.yaml)")
	}
	sum := sha256.Sum256(b)
	return &Checkout{Commit: "sha256:" + hex.EncodeToString(sum[:]), CommitSubject: "chart " + chart + " " + version, Version: version, FS: fsys}, nil
}

// checkArchive refuses an archive of more files, or larger ones, than a
// fetch may hold, reading its headers before anything is unpacked.
func checkArchive(b []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("the chart's archive is not gzipped: %w", err)
	}
	tr := tar.NewReader(zr)
	var files, size int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the chart's archive cannot be read: %w", err)
		}
		if files++; files > FetchLimits.Files {
			return fmt.Errorf("%w: the chart has more than %d files", ErrFetchLimit, FetchLimits.Files)
		}
		if size += h.Size; size > FetchLimits.Checkout || h.Size < 0 {
			return fmt.Errorf("%w: the chart's files come to more than %d MB", ErrFetchLimit, FetchLimits.Checkout>>20)
		}
	}
}

// ---------- OCI registries ----------

// The media types of a Helm chart in an OCI registry.
const (
	ociManifest  = "application/vnd.oci.image.manifest.v1+json"
	ociChartType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	ociOldChart  = "application/tar+gzip"
)

// ociHost is an oci:// address's host.
func ociHost(u string) string {
	host, _, _ := strings.Cut(strings.TrimPrefix(u, "oci://"), "/")
	return host
}

// ociRepo is the chart's repository in the registry: the path and the
// chart.
func ociRepo(src Source) (host, repo string) {
	host, p, _ := strings.Cut(strings.TrimPrefix(src.Repo, "oci://"), "/")
	if p == "" {
		return host, src.Chart
	}
	return host, p + "/" + src.Chart
}

// ociTag is a version as a registry's tag: OCI tags have no "+", which
// helm writes as "_".
func ociTag(version string) string { return strings.ReplaceAll(version, "+", "_") }

func (r *Repos) ociTags(ctx context.Context, src Source, repo Repository, secret string) ([]string, error) {
	host, name := ociRepo(src)
	b, err := r.ociGet(ctx, host, "/v2/"+name+"/tags/list?n=1000", "application/json", "repository:"+name+":pull", repo, secret, maxManifest)
	if err != nil {
		return nil, fmt.Errorf("listing the versions of %s: %w", src.ChartRef(), err)
	}
	var list struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("the registry's list of versions cannot be read: %w", err)
	}
	var out []string
	for _, t := range list.Tags {
		v := strings.ReplaceAll(t, "_", "+")
		if chartVersion.MatchString(v) && semverOf(v) != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no versions in the registry", src.ChartRef())
	}
	return out, nil
}

// fetchOCI fetches an OCI registry's chart at a version: its manifest,
// then the chart's layer, checked against the digest the manifest gives.
func (r *Repos) fetchOCI(ctx context.Context, src Source, repo Repository, secret string) (*Checkout, error) {
	version := src.Ref
	if version == "" {
		tags, err := r.ociTags(ctx, src, repo, secret)
		if err != nil {
			return nil, err
		}
		sortTags(tags)
		version = Newest(tags)
	}
	host, name := ociRepo(src)
	scope := "repository:" + name + ":pull"
	b, err := r.ociGet(ctx, host, "/v2/"+name+"/manifests/"+ociTag(version), ociManifest, scope, repo, secret, maxManifest)
	if err != nil {
		return nil, fmt.Errorf("reading %s %s: %w", src.ChartRef(), version, err)
	}
	var m struct {
		MediaType string     `json:"mediaType"`
		Layers    []ociLayer `json:"layers"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("the manifest of %s %s cannot be read: %w", src.ChartRef(), version, err)
	}
	i := slices.IndexFunc(m.Layers, func(l ociLayer) bool { return l.MediaType == ociChartType || l.MediaType == ociOldChart })
	if i < 0 {
		return nil, fmt.Errorf("%s %s is not a Helm chart: its manifest has no chart layer", src.ChartRef(), version)
	}
	layer := m.Layers[i]
	want, ok := strings.CutPrefix(layer.Digest, "sha256:")
	if !ok || len(want) != 64 {
		return nil, fmt.Errorf("the chart layer of %s %s has a digest the console cannot check (%q)", src.ChartRef(), version, layer.Digest)
	}
	if layer.Size > maxArchive {
		return nil, fmt.Errorf("%w: the chart's archive is more than %d MB", ErrFetchLimit, maxArchive>>20)
	}
	blob, err := r.ociGet(ctx, host, "/v2/"+name+"/blobs/"+layer.Digest, "", scope, repo, secret, maxArchive)
	if err != nil {
		return nil, fmt.Errorf("fetching %s %s: %w", src.ChartRef(), version, err)
	}
	sum := sha256.Sum256(blob)
	if hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s %s's archive does not match the digest its manifest gives", src.ChartRef(), version)
	}
	return unpackChart(blob, src.Chart, version)
}

// ociLayer is a layer of a manifest.
type ociLayer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ociGet reads a registry's path, signing in as the registry asks
// (HR-04): with the token given, or with the credentials at the token
// service its challenge names (Bearer), or with them on the request
// (Basic); anonymously when there are none.
func (r *Repos) ociGet(ctx context.Context, host, p, accept, scope string, repo Repository, secret string, limit int64) ([]byte, error) {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	if refusedName(name) && !AllowLocalRepos {
		return nil, errHostRefused
	}
	// Registries are reached over HTTPS only: a registry's credentials
	// never travel in the clear.
	target := "https://" + host + p
	do := func(auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := r.o.Client.Do(req)
		if err != nil {
			return nil, describeHTTP(err)
		}
		return res, nil
	}
	first := ""
	if repo.Auth == AuthToken && secret != "" {
		first = "Bearer " + secret
	}
	res, err := do(first)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		challenge := res.Header.Get("WWW-Authenticate")
		res.Body.Close()
		auth, err := r.signIn(ctx, challenge, scope, repo, secret)
		if err != nil {
			return nil, err
		}
		if res, err = do(auth); err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()
	if err := statusError(res, repo); err != nil {
		return nil, err
	}
	return readCapped(res.Body, limit)
}

// signIn answers a registry's challenge: the Authorization to send again.
func (r *Repos) signIn(ctx context.Context, challenge, scope string, repo Repository, secret string) (string, error) {
	scheme, params := parseChallenge(challenge)
	user := repo.Username
	if user == "" {
		user = "token"
	}
	switch scheme {
	case "basic":
		if secret == "" {
			return "", errors.New("the registry needs a user name and password: add them to the repository in Settings › Applications")
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret)), nil
	case "bearer":
		realm := params["realm"]
		u, err := url.Parse(realm)
		if err != nil || u.Host == "" || !(u.Scheme == "https" || AllowLocalRepos && u.Scheme == "http") {
			return "", fmt.Errorf("the registry's token service %q is not an https:// address", realm)
		}
		if refusedName(u.Hostname()) && !AllowLocalRepos {
			return "", errHostRefused
		}
		q := u.Query()
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		if s := params["scope"]; s != "" {
			q.Set("scope", s)
		} else if scope != "" {
			q.Set("scope", scope)
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		if secret != "" {
			req.SetBasicAuth(user, secret)
		}
		res, err := r.o.Client.Do(req)
		if err != nil {
			return "", describeHTTP(err)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			if secret == "" {
				return "", errors.New("the registry needs credentials: add them to the repository in Settings › Applications")
			}
			return "", errors.New("the registry's token service refused the credentials")
		}
		if res.StatusCode/100 != 2 {
			return "", fmt.Errorf("the registry's token service answered %s", res.Status)
		}
		b, err := readCapped(res.Body, maxSmall)
		if err != nil {
			return "", err
		}
		var tok struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal(b, &tok) != nil || tok.Token == "" && tok.AccessToken == "" {
			return "", errors.New("the registry's token service gave no token")
		}
		if tok.Token == "" {
			tok.Token = tok.AccessToken
		}
		return "Bearer " + tok.Token, nil
	}
	if secret == "" {
		return "", errors.New("the registry needs credentials: add them to the repository in Settings › Applications")
	}
	return "", errors.New("the registry refused the credentials")
}

// parseChallenge reads a WWW-Authenticate header: its scheme, lowercase,
// and its parameters.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				break
			}
			params[k], rest = v[1:end+1], v[end+2:]
			continue
		}
		val, next, _ := strings.Cut(v, ",")
		params[k], rest = strings.TrimSpace(val), next
	}
	return strings.ToLower(scheme), params
}

// ---------- reading ----------

// repoTransport connects where a git fetch may, and no further
// (repohost.go): never to the console's own pod or node, or a cloud's
// metadata service, redirects included.
var repoTransport http.RoundTripper

func init() {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: checkConnect}).DialContext
	repoTransport = base
}

// readCapped reads a body of at most limit bytes.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, describeHTTP(err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: it sends more than %d MB", ErrFetchLimit, max(limit>>20, 1))
	}
	return b, nil
}

// statusError says what a repository's refusal means.
func statusError(res *http.Response, repo Repository) error {
	switch {
	case res.StatusCode/100 == 2:
		return nil
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		if repo.Auth == AuthNone || repo.Auth == "" {
			return errors.New("the repository needs credentials: add them to it in Settings › Applications")
		}
		return errors.New("the repository refused the credentials")
	case res.StatusCode == http.StatusNotFound:
		return errors.New("not found there (404)")
	}
	return fmt.Errorf("the repository answered %s", res.Status)
}

// describeHTTP makes a connection's errors readable.
func describeHTTP(err error) error {
	switch {
	case errors.Is(err, errHostRefused):
		return errHostRefused
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the repository did not answer in time")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("the repository could not be reached: %v", ue.Err)
	}
	return err
}
