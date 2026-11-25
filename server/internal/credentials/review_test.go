package credentials

// The fixes of the code review of November 25 (0.13.3): each test names
// the review's item it holds to.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// secretForms are the ways a value can be written in an address: as it
// is, escaped for a query or a path, and escaped twice.
func secretForms(v string) []string {
	return []string{v, url.QueryEscape(v), url.PathEscape(v), url.QueryEscape(url.QueryEscape(v)), url.PathEscape(url.QueryEscape(v))}
}

// credentials#1: a test that is redirected names where by its host alone,
// and its text holds no form of the secret, escaped or not; nor does the
// test kept with the credential.
func TestATestKeepsNoFormOfTheSecret(t *testing.T) {
	const tok = "fake+tok/en="
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A login redirect that carries the request, escaped once more:
		// on the same host, or at an identity provider.
		loc := "/login?next="
		if r.URL.Path == "/abs" {
			loc = "https://idp.example/login?next="
		}
		w.Header().Set("Location", loc+url.QueryEscape(r.URL.RequestURI()))
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	hc := srv.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s, _ := newService(t, hc)
	c := token(t, s, "app", srv.URL, SendAs{Method: SendQuery, Param: "access_token"}, tok)
	res, err := s.RunTest(ctx, c.ID, srv.URL+"/api/x", "ann")
	if err != nil || res.Status != http.StatusFound {
		t.Fatalf("%+v %v", res, err)
	}
	kept, _ := s.Get(ctx, c.ID)
	if kept.LastTest == nil {
		t.Fatal("no test kept")
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	for _, text := range []string{res.Text, kept.LastTest.Text} {
		for _, f := range secretForms(tok) {
			if strings.Contains(text, f) {
				t.Errorf("the test's text holds the secret as %q: %s", f, text)
			}
		}
		if strings.Contains(text, "login") || strings.Contains(text, "next=") || !strings.Contains(text, "redirects elsewhere ("+host+")") {
			t.Errorf("the redirect is named by more than its host: %s", text)
		}
	}
	// An absolute Location is named by its host too.
	res, _ = s.RunTest(ctx, c.ID, srv.URL+"/abs", "ann")
	if strings.Contains(res.Text, "next=") || !strings.Contains(res.Text, "redirects elsewhere (idp.example)") {
		t.Errorf("an absolute redirect: %s", res.Text)
	}
	// Every form of a secret is taken out of a text.
	for _, f := range secretForms(tok) {
		if got := redact("at "+f+" now", []string{tok}); strings.Contains(got, f) {
			t.Errorf("redact kept %q: %s", f, got)
		}
	}
}

// keyPEM is a test certificate's private key, as PEM.
func keyPEM(t *testing.T, c certpintest.Cert) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(c.TLS.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// credentials#2: a private key pasted with a certificate, as the
// certificate of a client certificate or as a private CA, is refused; one
// kept so already is taken out at the start, out of every backup, and out
// of a backup restored.
func TestAPrivateKeyIsNeverKeptWithItsCertificate(t *testing.T) {
	cert := certpintest.SelfSigned(t, "client.example")
	key := keyPEM(t, cert)
	combined := cert.PEM + key
	s, _ := newService(t, nil)
	hasKey := func(v any) bool {
		b, _ := json.Marshal(v)
		return strings.Contains(string(b), "PRIVATE KEY") || strings.Contains(string(b), strings.Split(key, "\n")[1])
	}
	_, err := s.Create(ctx, Credential{Name: "mtls", Kind: KindClientCert, Endpoint: "https://api.example.com", CertPEM: combined}, Secret{KeyPEM: key}, "ann")
	body := strings.Split(key, "\n")[1]
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), body) {
		t.Fatalf("a certificate with its key: %v", err)
	}
	_, err = s.Create(ctx, Credential{Name: "ca", Kind: KindToken, Endpoint: "https://api.example.com", CAPEM: combined}, Secret{Token: "tok-1"}, "ann")
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), body) {
		t.Fatalf("a private CA with its key: %v", err)
	}
	good, err := s.Create(ctx, Credential{Name: "mtls", Kind: KindClientCert, Endpoint: "https://api.example.com", CertPEM: cert.PEM, CAPEM: cert.PEM}, Secret{KeyPEM: key}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	// One kept with its key by an earlier release.
	if _, err := s.o.Repo.Update(ctx, good.ID, func(c *Credential) error { c.CertPEM, c.CAPEM = combined, combined; return nil }); err != nil {
		t.Fatal(err)
	}
	out, err := s.Export(ctx, func(v string) string { return "sealed" })
	if err != nil || len(out) != 1 || hasKey(out[0].Credential) {
		t.Fatalf("the backup holds the key: %v", err)
	}
	if ids, err := CleanCertificates(ctx, s.o.Repo); err != nil || len(ids) != 1 || ids[0] != good.ID {
		t.Fatalf("cleaned %v: %v", ids, err)
	}
	kept, _ := s.Get(ctx, good.ID)
	if hasKey(kept) || !strings.Contains(kept.CertPEM, "BEGIN CERTIFICATE") || !strings.Contains(kept.CAPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("kept: %+v", kept)
	}
	if u, err := s.Resolve(ctx, good.ID, UseHTTP, "https://api.example.com/x"); err != nil || u.Certificate() == nil {
		t.Fatalf("the cleaned certificate no longer works: %v", err)
	}
	if ids, err := CleanCertificates(ctx, s.o.Repo); err != nil || len(ids) != 0 {
		t.Fatalf("cleaned again %v: %v", ids, err)
	}
	// A backup of an earlier release, with the key beside the certificate.
	other, _ := newService(t, nil)
	e := out[0]
	e.CertPEM, e.CAPEM, e.Secret = combined, combined, ""
	if n, err := other.Import(ctx, []BackupEntry{e}, func(string) error { return nil }, func(string, string, string) (string, error) { return "", nil }, func(string, ...any) {}); err != nil || n != 1 {
		t.Fatalf("restored %d: %v", n, err)
	}
	if list, _ := other.List(ctx); len(list) != 1 || hasKey(list) {
		t.Fatalf("restored with its key: %+v", list)
	}
}

// refusingRepo refuses every change of a credential with err.
type refusingRepo struct {
	Repo
	err error
}

func (r *refusingRepo) Update(ctx context.Context, id string, change func(*Credential) error) (Credential, error) {
	if r.err != nil {
		return Credential{}, r.err
	}
	return r.Repo.Update(ctx, id, change)
}

// credentials#3: an edit that is refused leaves the credential's secret
// as it was: a name another has, a list that cannot be written, a
// credential removed meanwhile.
func TestARefusedEditKeepsTheSecret(t *testing.T) {
	s, _ := newService(t, nil)
	gh := token(t, s, "GitHub", "https://github.com/org", SendAs{}, "fake-tok-1")
	token(t, s, "gitlab", "https://gitlab.com/org", SendAs{}, "fake-tok-9")
	secretOf := func() string {
		t.Helper()
		sec, err := s.secret(ctx, gh.ID)
		if err != nil {
			t.Fatal(err)
		}
		return sec.Token + sec.Password
	}
	if _, err := s.Update(ctx, gh.ID, Credential{Name: "GitLab", Kind: KindToken, Endpoint: "https://github.com/org"}, &Secret{Token: "fake-tok-2"}, "bob"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a name another has: %v", err)
	}
	if got := secretOf(); got != "fake-tok-1" {
		t.Fatalf("a refused rename changed the secret to %q", got)
	}
	repo := &refusingRepo{Repo: s.o.Repo, err: errors.New("the database is away")}
	s.o.Repo = repo
	for _, edit := range []struct {
		c   Credential
		sec *Secret
	}{
		{Credential{Name: "GitHub", Kind: KindToken, Endpoint: "https://github.com/org"}, &Secret{Token: "fake-tok-2"}},
		{Credential{Name: "GitHub", Kind: KindUserPass, Endpoint: "https://github.com/org", Username: "u"}, &Secret{Password: "fake-pw-2"}},
		{Credential{Name: "GitHub", Kind: KindNone, Endpoint: "https://github.com/org"}, nil},
	} {
		if _, err := s.Update(ctx, gh.ID, edit.c, edit.sec, "bob"); err == nil {
			t.Fatalf("%s: saved", edit.c.Kind)
		}
		if got := secretOf(); got != "fake-tok-1" {
			t.Fatalf("a refused change to %s left the secret %q", edit.c.Kind, got)
		}
	}
	// Removed while it was edited: no secret is written for it.
	repo.err = fmt.Errorf("%w %q", ErrNotFound, gh.ID)
	if _, err := s.Update(ctx, gh.ID, Credential{Name: "GitHub", Kind: KindToken, Endpoint: "https://github.com/org"}, &Secret{Token: "fake-tok-2"}, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed meanwhile: %v", err)
	}
	if got := secretOf(); got != "fake-tok-1" {
		t.Fatalf("the secret of a credential removed meanwhile was written: %q", got)
	}
}

// credentials#4: a secret is named "without a credential", and may be
// removed, only once the list of before 0.13 is copied into the database
// and has left the Secret; and a row with only its id (what an
// application, a repository or a source names) names its secret too.
func TestNoSecretIsUnnamedBeforeTheListIsCopied(t *testing.T) {
	d := dbtest.Open(t)
	now, _ := ticking()
	mem := &MemorySecret{}
	before := New(Options{Secrets: mem, Now: now})
	a := token(t, before, "GitHub", "https://github.com/org", SendAs{}, "fake-tok-a")
	asBefore(t, before, mem)
	s := New(Options{Secrets: mem, Repo: NewPGRepo(d), Now: now})
	// The copy failed: nothing is unnamed, and nothing is removed.
	if ids, err := s.UnnamedSecrets(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("before the copy, unnamed: %v %v", ids, err)
	}
	if noRow, _, err := Unmatched(ctx, s.o.Repo, mem); err != nil || len(noRow) != 0 {
		t.Fatalf("before the copy, unmatched: %v %v", noRow, err)
	}
	if err := s.RemoveUnnamedSecret(ctx, a.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("removed before the copy: %v", err)
	}
	if _, _, found, _ := mem.LoadKey(ctx, secretKey(a.ID)); !found {
		t.Fatal("the secret of a credential not copied yet was removed")
	}
	// Copied, but the list is still in the Secret: still nothing.
	if _, err := CopyList(ctx, d, mem); err != nil {
		t.Fatal(err)
	}
	if _, err := s.o.Repo.Remove(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveUnnamedSecret(ctx, a.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("removed while the list is in the Secret: %v", err)
	}
	if _, err := RemoveList(ctx, mem); err != nil {
		t.Fatal(err)
	}
	// A row with only its id names its secret; a secret of no row is
	// unnamed, and removed.
	exec(t, d, "INSERT INTO credentials (credential_id) VALUES ($1) ON CONFLICT DO NOTHING", "cr-idonly")
	for _, id := range []string{"cr-idonly", "cr-nobody"} {
		_, v, _, _ := mem.LoadKey(ctx, secretKey(id))
		if err := mem.SaveKeyIf(ctx, secretKey(id), []byte(`{"token":"fake-x"}`), v); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.UnnamedSecrets(ctx)
	if err != nil || !slices.Contains(ids, "cr-nobody") || slices.Contains(ids, "cr-idonly") {
		t.Fatalf("unnamed: %v %v", ids, err)
	}
	if err := s.RemoveUnnamedSecret(ctx, "cr-idonly"); !errors.Is(err, ErrInUse) {
		t.Fatalf("removed the secret a row names: %v", err)
	}
	if err := s.RemoveUnnamedSecret(ctx, "cr-nobody"); err != nil {
		t.Fatal(err)
	}
}

// credentials#5: the copy of the list of before 0.13 goes past a record
// Postgres refuses: a NUL character is replaced, two names the unique
// index takes for one are told apart, and a record that still cannot be
// kept is left out and said, in the note of the upgrade the page shows.
// A NUL is refused when a credential is saved.
func TestTheCopyGoesPastARecordPostgresRefuses(t *testing.T) {
	d := dbtest.Open(t)
	now, _ := ticking()
	mem := &MemorySecret{}
	list := []Credential{
		{ID: "cr-nul", Name: "nul", Kind: KindToken, Endpoint: "https://a.example", Description: "a\x00b", LastTest: &Test{At: now(), Text: "HTTP/1.1 200 \x00OK"}},
		{ID: "cr-upper", Name: "İzmir CI", Kind: KindToken, Endpoint: "https://b.example"},
		{ID: "cr-lower", Name: "izmir ci", Kind: KindToken, Endpoint: "https://c.example"},
		{ID: "cr-bad\x00id", Name: "bad", Kind: KindToken, Endpoint: "https://d.example"},
		{ID: "cr-fine", Name: "fine", Kind: KindToken, Endpoint: "https://e.example"},
	}
	b, _ := json.Marshal(index{Credentials: list})
	if err := mem.SaveKeyIf(ctx, indexKey, b, ""); err != nil {
		t.Fatal(err)
	}
	n, err := CopyList(ctx, d, mem)
	if err != nil || n != 4 {
		m, _ := New(Options{Repo: NewPGRepo(d)}).Migration(ctx)
		t.Fatalf("copied %d: %v %q", n, err, m.Notes)
	}
	s := New(Options{Secrets: mem, Repo: NewPGRepo(d), Now: now})
	got, err := s.List(ctx)
	if err != nil || len(got) != 4 {
		t.Fatalf("the list: %d %v", len(got), err)
	}
	names := map[string]string{}
	for _, c := range got {
		names[c.ID] = c.Name
		if strings.Contains(c.Description, "\x00") || c.LastTest != nil && strings.Contains(c.LastTest.Text, "\x00") {
			t.Errorf("%s kept a NUL", c.ID)
		}
	}
	if names["cr-upper"] == "" || names["cr-lower"] == "" || !strings.Contains(names["cr-upper"]+names["cr-lower"], "(copied at upgrade") {
		t.Errorf("the two names: %v", names)
	}
	m, err := s.Migration(ctx)
	if err != nil || len(m.Notes) != 1 || !strings.Contains(m.Notes[0], "bad") || !strings.Contains(m.Notes[0], "left out") {
		t.Fatalf("the note: %+v %v", m.Notes, err)
	}
	if ok, _ := d.Imported(ctx, ListImportID); !ok {
		t.Fatal("the copy is not recorded")
	}
	if _, err := s.Create(ctx, Credential{Name: "x", Kind: KindToken, Endpoint: "https://a.example", Description: "a\x00b"}, Secret{Token: "fake-tok"}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a NUL saved: %v", err)
	}
}

// credentials#6: an endpoint typed with a percent-escape is kept escaped,
// so it reads back as the same endpoint: it covers its own addresses, not
// a sibling's, and saves again.
func TestAPercentEscapedEndpointReadsBackTheSame(t *testing.T) {
	s, _ := newService(t, nil)
	for _, tc := range []struct{ ep, under, sibling string }{
		{"https://dev.azure.com/org/My%20Project", "https://dev.azure.com/org/My%20Project/_git/repo", "https://dev.azure.com/org/My"},
		{"https://git.example.com/org/C%23", "https://git.example.com/org/C%23/repo.git", "https://git.example.com/org/C/repo.git"},
		{"https://git.example.com/org/a%3Fb", "https://git.example.com/org/a%3Fb/x", "https://git.example.com/org/a/x"},
		{"https://git.example.com/org/100%25", "https://git.example.com/org/100%25/x", "https://git.example.com/org/100/x"},
		{"https://git.example.com/org/C%23/team", "https://git.example.com/org/C%23/team/x", "https://git.example.com/org/C/team/x"},
	} {
		c := token(t, s, "t "+tc.ep, tc.ep, SendAs{}, "fake-tok")
		if c.Endpoint != tc.ep {
			t.Errorf("%s is kept as %s", tc.ep, c.Endpoint)
		}
		if !c.Covers(tc.under) || c.Covers(tc.sibling) {
			t.Errorf("%s (kept %s): covers %s %v, %s %v", tc.ep, c.Endpoint, tc.under, c.Covers(tc.under), tc.sibling, c.Covers(tc.sibling))
		}
		if m, err := s.Matching(ctx, UseGit, tc.under); err != nil || !slices.ContainsFunc(m, func(o Credential) bool { return o.ID == c.ID }) {
			t.Errorf("%s is not offered for %s: %v", tc.ep, tc.under, err)
		}
		if _, err := s.Update(ctx, c.ID, c, nil, "bob"); err != nil {
			t.Errorf("%s does not save again: %v", tc.ep, err)
		}
	}
}

// credentials#7: an address is covered whatever its length, its query
// and its fragment: only an endpoint is bounded to 500 characters and
// has neither.
func TestALongAddressIsCovered(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "wiki", "https://wiki.example.com/confluence", SendAs{}, "fake-tok")
	cql := url.QueryEscape(`siteSearch ~ "` + strings.Repeat("слово ", 20) + `"`)
	long := "https://wiki.example.com/confluence/rest/api/search?cql=" + cql
	if len(long) <= 500 {
		t.Fatalf("not long: %d", len(long))
	}
	for _, target := range []string{long, "https://wiki.example.com/confluence/display/X#anchor", "https://wiki.example.com/confluence/" + strings.Repeat("a/", 300)} {
		if !c.Covers(target) {
			t.Errorf("not covered: %.80s…", target)
		}
		if _, err := s.CheckChoice(ctx, c.ID, UseHTTP, target); err != nil {
			t.Errorf("refused %.80s…: %v", target, err)
		}
	}
	u, err := s.Resolve(ctx, c.ID, UseHTTP, long)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, long, nil)
	u.Signer()(req)
	if req.Header.Get("Authorization") == "" {
		t.Fatal("a long address went unsigned")
	}
	// Still under the endpoint only.
	for _, target := range []string{"https://wiki.example.com/other?x=/confluence/", "https://wiki.example.com/other#/confluence/x", "https://evil.example/confluence?" + cql} {
		if c.Covers(target) {
			t.Errorf("covers %.80s", target)
		}
	}
	if _, err := s.Create(ctx, Credential{Name: "long", Kind: KindToken, Endpoint: "https://wiki.example.com/" + strings.Repeat("a", 500)}, Secret{Token: "fake-tok"}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a long endpoint: %v", err)
	}
	if _, err := s.Create(ctx, Credential{Name: "frag", Kind: KindToken, Endpoint: "https://wiki.example.com/x#y"}, Secret{Token: "fake-tok"}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an endpoint with a fragment: %v", err)
	}
}

// credentials#8: a segment's ;parameters (Tomcat's ..;) do not get past
// the endpoint's path, and a segment that only has parameters is still
// covered.
func TestASegmentsParametersStayUnderTheEndpoint(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "bitbucket", "https://git.corp/scm/teama", SendAs{}, "fake-tok")
	for _, target := range []string{"https://git.corp/scm/teama/..;/teamb/repo.git", "https://git.corp/scm/teama/x/..;/..;/teamb/repo.git", "https://git.corp/scm/teama/..;jsessionid=1/teamb"} {
		if _, err := s.CheckChoice(ctx, c.ID, UseGit, target); !errors.Is(err, ErrOutside) {
			t.Errorf("accepted %s: %v", target, err)
		}
	}
	for _, target := range []string{"https://git.corp/scm/teama/repo.git;jsessionid=1", "https://git.corp/scm/teama/x;v=1/y", "https://git.corp/scm/teama/...;/x"} {
		if _, err := s.CheckChoice(ctx, c.ID, UseGit, target); err != nil {
			t.Errorf("refused %s: %v", target, err)
		}
	}
}

// credentials#9: a target's path is compared as it is: .git is allowed
// only where a repository's endpoint ends, for a git client's own paths,
// and never for a bucket.
func TestTheGitSuffixStaysInItsScope(t *testing.T) {
	s, _ := newService(t, nil)
	key, err := s.Create(ctx, Credential{Name: "docs", Kind: KindAccessKey, Endpoint: "https://s3.amazonaws.com/docs"}, Secret{AccessKeyID: "AKIAFAKE", SecretAccessKey: "fake-secret"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	dotgit, err := s.Create(ctx, Credential{Name: "docs.git", Kind: KindAccessKey, Endpoint: "https://s3.amazonaws.com/docs.git"}, Secret{AccessKeyID: "AKIAFAKE", SecretAccessKey: "fake-secret"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	owner := token(t, s, "acme", "https://git.example.com/acme", SendAs{}, "fake-tok")
	repo := token(t, s, "repo", "https://git.example.com/org/repo.git", SendAs{}, "fake-tok")
	mid := token(t, s, "mid", "https://git.example.com/a.git/b", SendAs{}, "fake-tok")
	for _, tc := range []struct {
		c      Credential
		target string
		ok     bool
	}{
		{key, S3Address("", "docs"), true},
		{key, S3Address("", "docs.git"), false},
		{key, S3Address("", "docs.git") + "/x", false},
		{dotgit, S3Address("", "docs.git"), true},
		{dotgit, S3Address("", "docs"), false},
		{owner, "https://git.example.com/acme/tool.git", true},
		{owner, "https://git.example.com/acme.git/tool", false},
		{owner, "https://git.example.com/acme.git/tool.git/info/refs", false},
		{repo, "https://git.example.com/org/repo.git", true},
		{repo, "https://git.example.com/org/repo", true},
		{repo, "https://git.example.com/org/repo.git/info/refs?service=git-upload-pack", true},
		{repo, "https://git.example.com/org/repo.git/git-upload-pack", true},
		{repo, "https://git.example.com/org/repo.git/other", false},
		{mid, "https://git.example.com/a.git/b", true},
		{mid, "https://git.example.com/a.git/b/c.git", true},
		{mid, "https://git.example.com/a/b", false},
	} {
		if got := tc.c.Covers(tc.target); got != tc.ok {
			t.Errorf("%s (%s) covers %s: %v", tc.c.Name, tc.c.Endpoint, tc.target, got)
		}
	}
	if dotgit.Endpoint != "https://s3.amazonaws.com/docs.git" {
		t.Errorf("a bucket's endpoint lost its name: %s", dotgit.Endpoint)
	}
	u, err := s.Resolve(ctx, key.ID, UseS3, S3Address("", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Covers(S3Address("", "docs.git")) {
		t.Error("the access key's use covers the bucket docs.git")
	}
}

// credentials#10: an endpoint's pinned certificate is trusted at the
// endpoint's host alone: an OAuth2 token URL on another host that
// presents it is refused, and the client secret is never posted there.
func TestThePinIsTrustedAtTheEndpointsHostAlone(t *testing.T) {
	cert := certpintest.SelfSigned(t, "api.internal.corp")
	var mu sync.Mutex
	posted := 0
	srv := certpintest.Server(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			mu.Lock()
			posted++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fake-minted","token_type":"bearer","expires_in":3600}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	s := pinService(t)
	pin := &certpin.Pin{SHA256: certpin.Fingerprint(cert.Leaf.Raw)}
	newOAuth := func(name, endpoint, tokenURL string) Credential {
		c, err := s.Create(ctx, Credential{Name: name, Kind: KindOAuth2, Endpoint: endpoint, TokenURL: tokenURL, ClientID: "client"}, Secret{ClientSecret: "fake-client-secret"}, "ann")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.o.Repo.Update(ctx, c.ID, func(cur *Credential) error { cur.PinnedCert = pin; return nil }); err != nil {
			t.Fatal(err)
		}
		return c
	}
	// The endpoint is pinned; the token URL is another host presenting
	// the same certificate.
	other := newOAuth("other", "https://localhost:"+port, "https://127.0.0.1:"+port+"/token")
	if _, err := s.Resolve(ctx, other.ID, UseHTTP, "https://localhost:"+port+"/x"); err == nil {
		t.Fatal("the client secret went to another host that presented the pinned certificate")
	}
	mu.Lock()
	n := posted
	mu.Unlock()
	if n != 0 {
		t.Fatalf("the token URL was posted to %d times", n)
	}
	// On the endpoint's own host, the pin holds.
	same := newOAuth("same", "https://127.0.0.1:"+port, "https://127.0.0.1:"+port+"/token")
	if u, err := s.Resolve(ctx, same.ID, UseHTTP, "https://127.0.0.1:"+port+"/x"); err != nil || u.Token != "fake-minted" {
		t.Fatalf("the endpoint's own token URL: %v", err)
	}
}

// credentials#11: one credential the upgrade cannot add does not stop the
// others: what it made is listed, what it could not make is said and left
// for the next run, and it returns no error, so the git addresses'
// migration (RP-05) follows.
func TestTheUpgradeGoesPastAnAddThatFails(t *testing.T) {
	s, _ := newService(t, nil)
	for i := range MaxCredentials - 2 {
		if err := s.o.Repo.Add(ctx, Credential{ID: fmt.Sprintf("cr-fill-%d", i), Name: fmt.Sprintf("fill %d", i), Kind: KindNone, Endpoint: "https://fill.example"}, MaxCredentials); err != nil {
			t.Fatal(err)
		}
	}
	var found []Found
	for i := range 4 {
		found = append(found, Found{Key: fmt.Sprint(i), Label: fmt.Sprintf("app ns/a%d", i), Endpoint: "https://github.com/org", Kind: KindToken, Secret: Secret{Token: fmt.Sprintf("fake-tok-%d", i)}})
	}
	apps := &fakeConsumer{switched: map[string]string{}, found: found}
	created, err := s.Migrate(ctx, []Consumer{apps}, "the upgrade")
	if err != nil || len(created) != 2 || len(apps.switched) != 2 {
		t.Fatalf("made %d, switched %d: %v", len(created), len(apps.switched), err)
	}
	m, _ := s.Migration(ctx)
	if len(m.Created) != 2 || len(m.Notes) != 2 || !strings.Contains(m.Notes[0], "keeps its own secret") || !strings.Contains(m.Notes[0], "at most") {
		t.Fatalf("the note: %+v", m)
	}
	for _, n := range m.Notes {
		if strings.Contains(n, "fake-tok") {
			t.Fatalf("a note holds the secret: %s", n)
		}
	}
	// Room again: the next run makes the others.
	for i := range 2 {
		if _, err := s.o.Repo.Remove(ctx, fmt.Sprintf("cr-fill-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if again, err := s.Migrate(ctx, []Consumer{apps}, "the upgrade"); err != nil || len(again) != 2 || len(apps.switched) != 4 {
		t.Fatalf("again: %d %v", len(again), err)
	}
}

// credentials#12: an edit keeps the pinned certificate while the
// endpoint's host stays the same, with or without a scheme.
func TestAnEditOfASchemelessEndpointKeepsThePin(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "internal", "git.internal/org", SendAs{}, "fake-tok")
	pin := &certpin.Pin{SHA256: "AA:BB"}
	for _, tc := range []struct {
		endpoint string
		kept     bool
	}{
		{"git.internal/org/team", true},
		{"https://git.internal/org", true},
		{"git.internal:8443/org", false},
		{"other.internal/org", false},
	} {
		if _, err := s.o.Repo.Update(ctx, c.ID, func(cur *Credential) error { cur.Endpoint, cur.PinnedCert = "git.internal/org", pin; return nil }); err != nil {
			t.Fatal(err)
		}
		saved, err := s.Update(ctx, c.ID, Credential{Name: "internal", Kind: KindToken, Endpoint: tc.endpoint}, nil, "bob")
		if err != nil {
			t.Fatal(err)
		}
		if (saved.PinnedCert != nil) != tc.kept {
			t.Errorf("git.internal/org to %s: pin kept %v", tc.endpoint, saved.PinnedCert != nil)
		}
	}
}

// sshServer is an SSH server with an RSA, an ECDSA and an Ed25519 host
// key, as most are (those of only when given), that lets the client key
// in; closed with the test.
func sshServer(t *testing.T, client ssh.PublicKey, only ...string) (addr string, keys map[string]ssh.PublicKey) {
	t.Helper()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), client.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("not that key")
	}}
	keys = map[string]ssh.PublicKey{}
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, dk, _ := ed25519.GenerateKey(rand.Reader)
	for _, k := range []any{rk, ek, dk} {
		sg, err := ssh.NewSignerFromKey(k)
		if err != nil {
			t.Fatal(err)
		}
		keys[sg.PublicKey().Type()] = sg.PublicKey()
		if len(only) == 0 || slices.Contains(only, sg.PublicKey().Type()) {
			cfg.AddHostKey(sg)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if sc, chans, reqs, err := ssh.NewServerConn(c, cfg); err == nil {
					go ssh.DiscardRequests(reqs)
					go func() {
						for ch := range chans {
							_ = ch.Reject(ssh.Prohibited, "no")
						}
					}()
					_ = sc.Wait()
				}
			}()
		}
	}()
	return ln.Addr().String(), keys
}

// credentials#13: the SSH test asks the server for the type of key saved,
// so a server with several host keys shows the one saved; another key
// is still a mismatch.
func TestTheSSHTestAsksForTheSavedKeysType(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	clientKey, _ := ssh.NewSignerFromKey(priv)
	addr, keys := sshServer(t, clientKey.PublicKey())
	s := pinService(t)
	for _, typ := range []string{ssh.KeyAlgoED25519, ssh.KeyAlgoRSA, ssh.KeyAlgoECDSA256} {
		c, err := s.Create(ctx, Credential{Name: "deploy " + typ, Kind: KindSSH, Endpoint: "ssh://git@" + addr + "/org", HostKey: string(ssh.MarshalAuthorizedKey(keys[typ]))}, Secret{PrivateKey: string(pem.EncodeToMemory(block))}, "ann")
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.RunTest(ctx, c.ID, "", "ann")
		if err != nil || !res.OK || !strings.Contains(res.Text, "matched") {
			t.Errorf("%s: %+v %v", typ, res, err)
		}
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherPub, _ := ssh.NewPublicKey(&other.PublicKey)
	c, err := s.Create(ctx, Credential{Name: "deploy other", Kind: KindSSH, Endpoint: "ssh://git@" + addr + "/org", HostKey: string(ssh.MarshalAuthorizedKey(otherPub))}, Secret{PrivateKey: string(pem.EncodeToMemory(block))}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := s.RunTest(ctx, c.ID, "", "ann"); res.OK || !strings.Contains(res.Text, "another host key") {
		t.Errorf("another key: %+v", res)
	}
}

// credentials#14: a GitHub App on a host with a self-signed certificate
// is offered Trust when minting its token fails for that certificate,
// and works once it is trusted.
func TestAGitHubAppOnASelfSignedHostIsOfferedTrust(t *testing.T) {
	cert := certpintest.SelfSigned(t)
	srv := certpintest.Server(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v3/app/installations/7/access_tokens" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"fake-ghs","expires_at":"2099-01-01T00:00:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	appKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rk)}))
	s := pinService(t)
	c, err := s.Create(ctx, Credential{Name: "ghes", Kind: KindGitHubApp, Endpoint: srv.URL + "/org", AppID: "1", InstallationID: "7"}, Secret{PrivateKey: appKey}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.RunTest(ctx, c.ID, "", "ann")
	if err != nil || res.OK || res.Certificate == nil || !res.Certificate.Trustable {
		t.Fatalf("no certificate to trust: %+v %v", res, err)
	}
	if _, _, err := s.Trust(ctx, c.ID, "", res.Certificate.SHA256, "ann"); err != nil {
		t.Fatal(err)
	}
	if res, err := s.RunTest(ctx, c.ID, "", "ann"); err != nil || !res.OK {
		t.Fatalf("trusted: %+v %v", res, err)
	}
}

// credentials#15: a credential of an oci:// endpoint is tested against
// the registry's API, answering its challenge as Helm does.
func TestAnOCIEndpointIsTestedAtTheRegistry(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			if r.Header.Get("Authorization") == "Bearer fake-reg" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="registry.test"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/token":
			u, p, _ := r.BasicAuth()
			if r.URL.Query().Get("service") != "registry.test" || !(u == "token" && p == "fake-tok" || u == "reader" && p == "fake-pw") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"fake-reg"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s, _ := newService(t, srv.Client())
	ep := "oci://" + strings.TrimPrefix(srv.URL, "https://") + "/org"
	tok := token(t, s, "registry token", ep, SendAs{}, "fake-tok")
	up, err := s.Create(ctx, Credential{Name: "registry user", Kind: KindUserPass, Endpoint: ep, Username: "reader"}, Secret{Password: "fake-pw"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	bad := token(t, s, "registry bad", ep, SendAs{}, "fake-wrong")
	for _, c := range []Credential{tok, up} {
		res, err := s.RunTest(ctx, c.ID, "", "ann")
		if err != nil || !res.OK || res.Status != http.StatusOK || strings.Contains(res.Text, "fake-") {
			t.Errorf("%s: %+v %v", c.Name, res, err)
		}
	}
	if res, _ := s.RunTest(ctx, bad.ID, "", "ann"); res.OK || !strings.Contains(res.Text, "token service") {
		t.Errorf("a wrong token: %+v", res)
	}
}

// credentials-cut-1: a client certificate's Test and Trust look at the
// server's certificate with the client certificate, so a server capped
// at TLS 1.2 that aborts on an empty one (Apache's SSLVerifyClient
// require) still shows its certificate, and it can be trusted.
func TestTrustIsReachableForAClientCertificate(t *testing.T) {
	server := certpintest.SelfSigned(t)
	client := certpintest.SelfSigned(t, "client.example")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 || !r.TLS.PeerCertificates[0].Equal(client.Leaf) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	// Aborts on an empty client certificate, at TLS 1.2.
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{server.TLS}, ClientAuth: tls.RequireAnyClientCert, MaxVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	s := pinService(t)
	c, err := s.Create(ctx, Credential{Name: "mtls", Kind: KindClientCert, Endpoint: srv.URL, CertPEM: client.PEM}, Secret{KeyPEM: keyPEM(t, client)}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.RunTest(ctx, c.ID, "", "ann")
	if err != nil || res.Certificate == nil || !res.Certificate.Trustable {
		t.Fatalf("no certificate shown: %+v %v", res, err)
	}
	if _, _, err := s.Trust(ctx, c.ID, "", res.Certificate.SHA256, "ann"); err != nil {
		t.Fatal(err)
	}
	if res, err := s.RunTest(ctx, c.ID, "", "ann"); err != nil || !res.OK {
		t.Fatalf("trusted: %+v %v", res, err)
	}
}

// credentials-cut-2: an edit of anything a test went by drops the last
// test; one of the description alone keeps it.
func TestAnEditOfWhatATestWentByDropsIt(t *testing.T) {
	s, _ := newService(t, nil)
	up, err := s.Create(ctx, Credential{Name: "up", Kind: KindUserPass, Endpoint: "https://a.example", Username: "reader"}, Secret{Password: "fake-pw"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	oa, err := s.Create(ctx, Credential{Name: "oa", Kind: KindOAuth2, Endpoint: "https://a.example", TokenURL: "https://a.example/token", ClientID: "one", Scope: "read"}, Secret{ClientSecret: "fake-cs"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	ak, err := s.Create(ctx, Credential{Name: "ak", Kind: KindAccessKey, Endpoint: "https://s3.amazonaws.com/docs", Region: "eu-west-1"}, Secret{AccessKeyID: "AKIAFAKE", SecretAccessKey: "fake-sk"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		c    Credential
		edit func(*Credential)
		drop bool
	}{
		{"description", up, func(c *Credential) { c.Description = "read only" }, false},
		{"username", up, func(c *Credential) { c.Username = "writer" }, true},
		{"token URL", oa, func(c *Credential) { c.TokenURL = "https://a.example/oauth/token" }, true},
		{"client id", oa, func(c *Credential) { c.ClientID = "two" }, true},
		{"scope", oa, func(c *Credential) { c.Scope = "write" }, true},
		{"region", ak, func(c *Credential) { c.Region = "us-east-1" }, true},
	} {
		if err := s.NoteTest(ctx, tc.c.ID, Test{At: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), OK: true, Text: "ok"}); err != nil {
			t.Fatal(err)
		}
		cur, _ := s.Get(ctx, tc.c.ID)
		tc.edit(&cur)
		saved, err := s.Update(ctx, tc.c.ID, cur, nil, "bob")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (saved.LastTest == nil) != tc.drop {
			t.Errorf("%s: the last test dropped %v", tc.name, saved.LastTest == nil)
		}
	}
}

// credentials-cut-3: a test's result is not kept over an edit, a Trust
// or a Forget made while it ran: it was of the credential as it was.
func TestATestIsNotKeptOverAChangeMadeWhileItRan(t *testing.T) {
	var s *Service
	var id string
	var during func()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		during()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s, _ = newService(t, srv.Client())
	c := token(t, s, "app", srv.URL, SendAs{}, "fake-tok")
	id = c.ID
	during = func() {
		cur, _ := s.Get(ctx, id)
		cur.Endpoint = srv.URL + "/api"
		if _, err := s.Update(ctx, id, cur, nil, "bob"); err != nil {
			t.Error(err)
		}
	}
	res, err := s.RunTest(ctx, id, srv.URL+"/api/x", "ann")
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if kept, _ := s.Get(ctx, id); kept.LastTest != nil {
		t.Fatalf("kept over an edit: %+v", kept.LastTest)
	}
	during = func() {
		if _, err := s.o.Repo.Update(ctx, id, func(cur *Credential) error { cur.PinnedCert = &certpin.Pin{SHA256: "AA"}; return nil }); err != nil {
			t.Error(err)
		}
	}
	if _, err := s.RunTest(ctx, id, srv.URL+"/api/x", "ann"); err != nil {
		t.Fatal(err)
	}
	if kept, _ := s.Get(ctx, id); kept.LastTest != nil {
		t.Fatalf("kept over a Trust: %+v", kept.LastTest)
	}
	// Nothing changed meanwhile: kept.
	during = func() {}
	if _, err := s.RunTest(ctx, id, srv.URL+"/api/x", "ann"); err != nil {
		t.Fatal(err)
	}
	if kept, _ := s.Get(ctx, id); kept.LastTest == nil {
		t.Fatal("a test of nothing changed is not kept")
	}
}

// credentials-cut-4: a host key pasted as several known_hosts lines (as
// ssh-keyscan prints them) keeps every key, and the test accepts any of
// them; a line with a marker (@revoked, @cert-authority) is refused, not
// read as a key to trust.
func TestEveryHostKeyPastedIsKept(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	clientKey, _ := ssh.NewSignerFromKey(priv)
	// A server without its Ed25519 key any more, whose keys were pasted
	// with it first.
	addr, keys := sshServer(t, clientKey.PublicKey(), ssh.KeyAlgoRSA)
	line := func(k ssh.PublicKey) string {
		return "[127.0.0.1]:" + addr[strings.LastIndex(addr, ":")+1:] + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
	}
	s := pinService(t)
	pasted := "# ssh-keyscan\n" + line(keys[ssh.KeyAlgoED25519]) + "\n" + line(keys[ssh.KeyAlgoRSA]) + "\n"
	c, err := s.Create(ctx, Credential{Name: "deploy", Kind: KindSSH, Endpoint: "ssh://git@" + addr + "/org", HostKey: pasted}, Secret{PrivateKey: string(pem.EncodeToMemory(block))}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.RunTest(ctx, c.ID, "", "ann"); err != nil || !res.OK {
		t.Fatalf("the second key pasted: %+v %v", res, err)
	}
	if info := c.HostKeyInfo(); info == nil || !strings.HasSuffix(info.Short, " and 1 more") {
		t.Errorf("shown as %+v", info)
	}
	for _, marker := range []string{"@revoked", "@cert-authority"} {
		bad := marker + " " + line(keys[ssh.KeyAlgoED25519])
		if _, err := s.Create(ctx, Credential{Name: "m " + marker, Kind: KindSSH, Endpoint: "ssh://git@" + addr + "/org", HostKey: bad}, Secret{PrivateKey: string(pem.EncodeToMemory(block))}, "ann"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", marker, err)
		}
	}
}

// credentials-cut-5: the address a test asks is completed under the
// endpoint for git's SSH form and for a bucket's dotted name.
func TestATypedTestAddressStaysUnderTheEndpoint(t *testing.T) {
	for _, tc := range []struct {
		c           Credential
		typed, want string
	}{
		{Credential{Kind: KindSSH, Endpoint: "git@github.com:ucesys"}, "git@github.com:ucesys/x.git", "ssh://git@github.com/ucesys/x.git"},
		{Credential{Kind: KindAccessKey, Endpoint: "https://s3.amazonaws.com"}, "my.bucket", "https://s3.amazonaws.com/my.bucket"},
		{Credential{Kind: KindAccessKey, Endpoint: "https://s3.amazonaws.com"}, "s3.amazonaws.com/my.bucket", "https://s3.amazonaws.com/my.bucket"},
		{Credential{Kind: KindToken, Endpoint: "https://github.com/org"}, "221b.git", "https://github.com/org/221b.git"},
		{Credential{Kind: KindToken, Endpoint: "github.com/org"}, "github.com/org/x", "https://github.com/org/x"},
	} {
		got := TestTarget(tc.c, tc.typed)
		if got != tc.want || !tc.c.Covers(got) {
			t.Errorf("%s under %s: %s (covered %v)", tc.typed, tc.c.Endpoint, got, tc.c.Covers(got))
		}
	}
}

// credentials-unver-1: a token sent as a query parameter is added to the
// request's query as it is: the other parameters keep their order and
// their escaping, and one of the parameter's name is replaced.
func TestAQueryTokenLeavesTheRestOfTheQueryAsItIs(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "q", "https://api.example.com", SendAs{Method: SendQuery, Param: "access_token"}, "fake+tok")
	u, err := s.Resolve(ctx, c.ID, UseHTTP, "https://api.example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x?z=1&a=%7E&cql=a+b&flag&access_token=old", nil)
	if err := u.Apply(req); err != nil {
		t.Fatal(err)
	}
	if want := "z=1&a=%7E&cql=a+b&flag&access_token=fake%2Btok"; req.URL.RawQuery != want {
		t.Fatalf("the query: %s, not %s", req.URL.RawQuery, want)
	}
}

// credentials-unver-2: a token a cookie cannot carry as it is is refused,
// not sent with bytes dropped.
func TestACookieTokenIsSentWholeOrNotAtAll(t *testing.T) {
	s, _ := newService(t, nil)
	for _, tok := range []string{"fake;tok", `fake"tok`, `fake\tok`, "fake tok"} {
		if _, err := s.Create(ctx, Credential{Name: "c " + tok, Kind: KindToken, Endpoint: "https://a.example", SendAs: SendAs{Method: SendCookie, Cookie: "session"}}, Secret{Token: tok}, "ann"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", tok, err)
		}
	}
	// A minted token is checked as it is sent.
	u := &Use{Name: "minted", Method: SendCookie, SendAs: SendAs{Method: SendCookie, Cookie: "session"}, Token: "fake;minted", ep: Endpoint{Scheme: "https", Host: "a.example"}}
	req, _ := http.NewRequest(http.MethodGet, "https://a.example/x", nil)
	if err := u.Apply(req); err == nil || req.Header.Get("Cookie") != "" {
		t.Fatalf("sent %q: %v", req.Header.Get("Cookie"), err)
	}
	c := token(t, s, "ok", "https://a.example", SendAs{Method: SendCookie, Cookie: "session"}, "fake-tok_1.2")
	ok, _ := s.Resolve(ctx, c.ID, UseHTTP, "https://a.example/x")
	req, _ = http.NewRequest(http.MethodGet, "https://a.example/x", nil)
	if err := ok.Apply(req); err != nil || req.Header.Get("Cookie") != "session=fake-tok_1.2" {
		t.Fatalf("%q: %v", req.Header.Get("Cookie"), err)
	}
}

// credentials-unver-3: a one-line secret part with a control character
// in it (a token pasted wrapped over two lines) is refused when saved:
// a header cannot carry it, and a query or Basic would send it changed.
func TestAOneLineSecretHasNoControlCharacter(t *testing.T) {
	s, _ := newService(t, nil)
	for _, tc := range []struct {
		c   Credential
		sec Secret
	}{
		{Credential{Kind: KindToken}, Secret{Token: "fake\ntok"}},
		{Credential{Kind: KindToken}, Secret{Token: "fake\x00tok\x7f"}},
		{Credential{Kind: KindUserPass, Username: "u"}, Secret{Password: "fake\rpw"}},
		{Credential{Kind: KindAccessKey}, Secret{AccessKeyID: "AKIA\tFAKE", SecretAccessKey: "fake"}},
		{Credential{Kind: KindOAuth2, TokenURL: "https://a.example/t", ClientID: "c"}, Secret{ClientSecret: "fake\x1bcs"}},
	} {
		tc.c.Name, tc.c.Endpoint = "c "+tc.c.Kind, "https://a.example"
		if _, err := s.Create(ctx, tc.c, tc.sec, "ann"); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "fake") {
			t.Errorf("%s: %v", tc.c.Kind, err)
		}
	}
}

// credentials-unver-4: a GitHub App's API is api.SUBDOMAIN.ghe.com on
// GHE.com, and keeps a GitHub Enterprise Server's port.
func TestTheGitHubAPIOfEachGitHub(t *testing.T) {
	for ep, want := range map[string]string{
		"https://github.com/org":       "https://api.github.com",
		"github.com/org":               "https://api.github.com",
		"https://www.github.com/org":   "https://api.github.com",
		"https://octocorp.ghe.com/org": "https://api.octocorp.ghe.com",
		"https://ghes.corp/org":        "https://ghes.corp/api/v3",
		"https://ghes.corp:8443/org":   "https://ghes.corp:8443/api/v3",
	} {
		if got := GitHubAPI(ep); got != want {
			t.Errorf("%s: %s, not %s", ep, got, want)
		}
	}
}

// credentials-unver-5: a password and a passphrase are kept as typed,
// spaces too: only a pasted line end is taken off.
func TestAPasswordKeepsItsSpaces(t *testing.T) {
	s, _ := newService(t, nil)
	for typed, kept := range map[string]string{" fake pw ": " fake pw ", "fake-pw\r\n": "fake-pw", "fake-pw\n": "fake-pw"} {
		c, err := s.Create(ctx, Credential{Name: fmt.Sprintf("up %q", typed), Kind: KindUserPass, Endpoint: "https://a.example", Username: "u"}, Secret{Password: typed}, "ann")
		if err != nil {
			t.Fatal(err)
		}
		if u, err := s.Resolve(ctx, c.ID, UseHTTP, "https://a.example/x"); err != nil || u.Token != kept {
			t.Errorf("%q kept as %q: %v", typed, u.Token, err)
		}
	}
	if got := (Secret{Passphrase: " fake phrase \n", Token: " fake-tok \n"}).trimmed(); got.Passphrase != " fake phrase " || got.Token != "fake-tok" {
		t.Errorf("trimmed: %+v", got)
	}
}

// credentials-unver-6: a secret that cannot be read does not fail the
// backup: its credential is backed up without it, to give again, and the
// others with theirs. A Secret that cannot be read at all still fails it.
func TestABackupGoesPastASecretThatCannotBeRead(t *testing.T) {
	mem := &MemorySecret{}
	s := open(t, Options{Secrets: mem})
	good := token(t, s, "good", "https://a.example", SendAs{}, "fake-tok-1")
	bad := token(t, s, "bad", "https://b.example", SendAs{}, "fake-tok-2")
	_, v, _, _ := mem.LoadKey(ctx, secretKey(bad.ID))
	if err := mem.SaveKeyIf(ctx, secretKey(bad.ID), []byte(`{"token":`), v); err != nil {
		t.Fatal(err)
	}
	out, err := s.Export(ctx, func(v string) string { return "sealed:" + fmt.Sprint(len(v)) })
	if err != nil || len(out) != 2 {
		t.Fatalf("%d exported: %v", len(out), err)
	}
	for _, e := range out {
		switch e.ID {
		case good.ID:
			if e.Secret == "" {
				t.Error("the readable secret is not backed up")
			}
		case bad.ID:
			if e.Secret != "" || len(e.Set) == 0 {
				t.Errorf("the unreadable one: %+v", e)
			}
		}
	}
	// A restore says it is to be given again.
	var notes []string
	other, _ := newService(t, nil)
	if n, err := other.Import(ctx, out, func(string) error { return nil }, func(string, string, string) (string, error) { return "fake-unsealed", nil }, func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) }); err != nil || n != 2 {
		t.Fatalf("restored %d: %v", n, err)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, "\n"), "bad: its secret is not in the backup") {
		t.Fatalf("notes: %q", notes)
	}
	away := open(t, Options{Secrets: failingSecret{mem}, Repo: s.o.Repo})
	if _, err := away.Export(ctx, func(v string) string { return v }); err == nil {
		t.Fatal("a Secret that cannot be read was backed up as having no secrets")
	}
}

// afterSave runs a hook once, right after the first write of a key.
type afterSave struct {
	*MemorySecret
	mu   sync.Mutex
	key  string
	hook func()
}

func (a *afterSave) SaveKeyIf(ctx context.Context, key string, b []byte, version string) error {
	err := a.MemorySecret.SaveKeyIf(ctx, key, b, version)
	a.mu.Lock()
	f := a.hook
	if key == a.key {
		a.hook = nil
	}
	a.mu.Unlock()
	if key == a.key && f != nil {
		f()
	}
	return err
}

// credentials-unver-7: two edits of one credential take turns, the secret
// with the row: the row never says one kind while the secret holds
// another's.
func TestTwoEditsOfACredentialTakeTurns(t *testing.T) {
	store := &afterSave{MemorySecret: &MemorySecret{}}
	s := open(t, Options{Secrets: store})
	c := token(t, s, "app", "https://a.example", SendAs{}, "fake-tok-1")
	var other sync.WaitGroup
	var otherErr error
	store.mu.Lock()
	store.key = secretKey(c.ID)
	store.hook = func() {
		// Another edit, made while this one's secret is written: given
		// time to finish, which it takes when nothing makes it wait.
		finished := make(chan struct{})
		other.Go(func() {
			defer close(finished)
			_, otherErr = s.Update(ctx, c.ID, Credential{Name: "app", Kind: KindToken, Endpoint: "https://a.example"}, &Secret{Token: "fake-tok-2"}, "bob")
		})
		select {
		case <-finished:
		case <-time.After(300 * time.Millisecond):
		}
	}
	store.mu.Unlock()
	if _, err := s.Update(ctx, c.ID, Credential{Name: "app", Kind: KindUserPass, Endpoint: "https://a.example", Username: "u"}, &Secret{Password: "fake-pw"}, "ann"); err != nil {
		t.Fatal(err)
	}
	other.Wait()
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	kept, _ := s.Get(ctx, c.ID)
	if _, err := s.Resolve(ctx, c.ID, UseHTTP, "https://a.example/x"); err != nil {
		t.Fatalf("the row says %s and the secret is another's: %v", kept.Kind, err)
	}
}

// credentials-unver-8: removing a secret no credential names does not
// remove one a restore brings back meanwhile under that id.
func TestRemovingAnUnnamedSecretSparesARestoredOne(t *testing.T) {
	mem := &hookedSecret{MemorySecret: &MemorySecret{}}
	s := open(t, Options{Secrets: mem})
	const id = "cr-restored"
	if err := mem.SaveKeyIf(ctx, secretKey(id), []byte(`{"token":"fake-old"}`), ""); err != nil {
		t.Fatal(err)
	}
	mem.mu.Lock()
	mem.key, mem.hook = secretKey(id), func() {
		// A restore of the credential of that id, meanwhile.
		e := BackupEntry{Credential: Credential{ID: id, Name: "restored", Kind: KindToken, Endpoint: "https://a.example", Set: []string{"token"}}, Secret: "sealed"}
		if _, err := s.Import(ctx, []BackupEntry{e}, func(string) error { return nil }, func(string, string, string) (string, error) { return `{"token":"fake-restored"}`, nil }, func(string, ...any) {}); err != nil {
			t.Error(err)
		}
	}
	mem.mu.Unlock()
	if err := s.RemoveUnnamedSecret(ctx, id); !errors.Is(err, ErrInUse) {
		t.Errorf("removed: %v", err)
	}
	if u, err := s.Resolve(ctx, id, UseHTTP, "https://a.example/x"); err != nil || u.Token != "fake-restored" {
		t.Fatalf("the restored credential lost its secret: %v", err)
	}
}
