package credentials

// The fixes of the code review of November 25 (0.13.3): each test names
// the review's item it holds to.

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

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
	// Removed while it was edited: no secret comes back for it.
	repo.err = fmt.Errorf("%w %q", ErrNotFound, gh.ID)
	if _, err := s.Update(ctx, gh.ID, Credential{Name: "GitHub", Kind: KindToken, Endpoint: "https://github.com/org"}, &Secret{Token: "fake-tok-2"}, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed meanwhile: %v", err)
	}
	if _, _, found, _ := s.o.Secrets.LoadKey(ctx, secretKey(gh.ID)); found {
		t.Fatal("the secret of a credential removed meanwhile was written")
	}
	repo.err = nil
	u, err := s.Resolve(ctx, gh.ID, UseHTTP, "https://github.com/org/x")
	_ = u
	if err == nil {
		t.Fatal("resolved a credential without its secret")
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
