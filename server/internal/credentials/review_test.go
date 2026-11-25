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
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/certpin/certpintest"
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
