package knowledge

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/s3/s3test"
)

// onePageSite is a web site of one page.
var onePageSite = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte("<html><head><title>Runbook</title></head><body><p>Restart the pod when it hangs.</p></body></html>"))
})

// CR-15 to CR-19: a web site with a self-signed certificate, without a
// credential: Test shows it, Trust pins it with the source as seen again,
// reads accept it, a renewal fails visibly, Forget refuses it again.
func TestTrustingASourcesCertificate(t *testing.T) {
	f := newFixture(t)
	f.s.o.CrawlHTTP = &http.Client{Transport: &http.Transport{}}
	cert := certpintest.SelfSigned(t)
	srv := certpintest.NewSwitchable(t, cert, onePageSite)
	fp := certpin.Fingerprint(cert.Leaf.Raw)
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "site", Name: "Intranet", Kind: SourceWeb, URL: srv.URL + "/", Pages: 5}, nil, true); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.TestSource(ctx, admin, "site")
	if err == nil || got.Certificate == nil || !got.Certificate.Trustable || got.Certificate.SHA256 != fp || !strings.Contains(err.Error(), "trust the certificate shown") {
		t.Fatalf("an untrusted certificate: %+v %v", got.Certificate, err)
	}
	w := serve(t, f.s, &admin, "POST", "sources/site/test", "", nil)
	var res struct {
		OK          bool          `json:"ok"`
		Certificate *certpin.Cert `json:"certificate"`
	}
	if json.Unmarshal(w.Body.Bytes(), &res); res.OK || res.Certificate == nil || res.Certificate.SHA256 != fp {
		t.Fatalf("the API shows it: %s", w.Body)
	}

	// Who and what may be trusted.
	if w := serve(t, f.s, &bob, "POST", "sources/trust", `{"saved":"site","sha256":"`+fp+`"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("an Admin: %d %s", w.Code, w.Body)
	}
	typed := Source{Name: "New", Kind: SourceWeb, URL: srv.URL + "/", Pages: 5}
	if _, err := f.s.TrustSource(ctx, admin, TrustInput{SHA256: fp, Source: &typed}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "save the source first") {
		t.Fatalf("an unsaved source without a credential: %v", err)
	}
	other := certpintest.SelfSigned(t)
	if _, err := f.s.TrustSource(ctx, admin, TrustInput{SHA256: certpin.Fingerprint(other.Leaf.Raw), Saved: "site"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a fingerprint not seen: %v", err)
	}
	if w := serve(t, f.s, &admin, "POST", "sources/trust", `{"saved":"site","sha256":"`+fp+`"}`, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"with":"source"`) {
		t.Fatalf("trust: %d %s", w.Code, w.Body)
	}
	if !slices.Contains(f.actions(), ActionTrust) {
		t.Fatalf("audited: %v", f.actions())
	}
	if got, err = f.s.TestSource(ctx, admin, "site"); err != nil || got.Items != 1 || got.Certificate == nil || !got.Certificate.Pinned {
		t.Fatalf("pinned: %+v %v", got, err)
	}
	v, _ := f.s.GetView(ctx, admin)
	if v.Pins["site"].Pin.Fingerprint() != fp || v.Pins["site"].With != "source" {
		t.Fatalf("the view: %+v", v.Pins)
	}
	// An edit at the same address keeps it.
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "site", Name: "Intranet docs", Kind: SourceWeb, URL: srv.URL + "/", Pages: 5}, nil, false); err != nil {
		t.Fatal(err)
	}
	if set, _ := f.s.Settings(ctx); set.mustSource(t, "site").PinnedCert.Fingerprint() != fp {
		t.Fatal("an edit dropped the pin")
	}

	srv.Use(other)
	if _, err := f.s.TestSource(ctx, admin, "site"); err == nil || !strings.Contains(err.Error(), "the certificate changed (was "+fp+", now "+certpin.Fingerprint(other.Leaf.Raw)+")") {
		t.Fatalf("a renewal: %v", err)
	}
	srv.Use(cert)
	if w := serve(t, f.s, &admin, "DELETE", "sources/site/trust", "", nil); w.Code != http.StatusOK {
		t.Fatalf("forget: %d %s", w.Code, w.Body)
	}
	if !slices.Contains(f.actions(), ActionForget) {
		t.Fatalf("audited: %v", f.actions())
	}
	if _, err := f.s.TestSource(ctx, admin, "site"); err == nil {
		t.Fatal("forgotten, the certificate was accepted")
	}
	if err := f.s.ForgetSource(ctx, admin, "site"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nothing to forget: %v", err)
	}
}

func (s Settings) mustSource(t *testing.T, id string) Source {
	t.Helper()
	x, ok := s.source(id)
	if !ok {
		t.Fatalf("no source %s", id)
	}
	return x
}

// CR-16: a source that names a credential pins the certificate with the
// credential, which every use of it then accepts; Forget on the source
// lets the credential's go.
func TestASourcesCredentialKeepsThePin(t *testing.T) {
	f := newFixture(t)
	f.s.o.CrawlHTTP = &http.Client{Transport: &http.Transport{}}
	creds := credentials.New(credentials.Options{HTTP: &http.Client{Transport: &http.Transport{}}, Dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext})
	f.s.o.Credentials = creds
	cert := certpintest.SelfSigned(t)
	srv := certpintest.Server(t, cert, onePageSite)
	fp := certpin.Fingerprint(cert.Leaf.Raw)
	cr, err := creds.Create(ctx, credentials.Credential{Name: "intranet", Kind: credentials.KindToken, Endpoint: srv.URL}, credentials.Secret{Token: "tok-123456"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	typed := Source{Name: "Intranet", Kind: SourceWeb, URL: srv.URL + "/", Pages: 5, Credential: cr.ID}
	got, err := f.s.TestSourceAsTyped(ctx, admin, typed, CredsChange{}, "")
	if err == nil || got.Certificate == nil || !got.Certificate.Trustable {
		t.Fatalf("untrusted: %+v %v", got.Certificate, err)
	}
	p, err := f.s.TrustSource(ctx, admin, TrustInput{SHA256: fp, Source: &typed})
	if err != nil || p.With != "credential" || p.CredentialName != "intranet" {
		t.Fatalf("trusted with the credential: %+v %v", p, err)
	}
	if c, _ := creds.Get(ctx, cr.ID); c.PinnedCert.Fingerprint() != fp {
		t.Fatalf("the credential: %+v", c.PinnedCert)
	}
	if got, err = f.s.TestSourceAsTyped(ctx, admin, typed, CredsChange{}, ""); err != nil || got.Items != 1 {
		t.Fatalf("pinned: %+v %v", got, err)
	}
	if _, err := f.s.SaveSourceWith(ctx, admin, Source{ID: "intra", Name: "Intranet", Kind: SourceWeb, URL: srv.URL + "/", Pages: 5, Credential: cr.ID}, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.s.GetView(ctx, admin); v.Pins["intra"].With != "credential" {
		t.Fatalf("the view: %+v", v.Pins)
	}
	if err := f.s.ForgetSource(ctx, admin, "intra"); err != nil {
		t.Fatal(err)
	}
	if c, _ := creds.Get(ctx, cr.ID); c.PinnedCert != nil {
		t.Fatal("the credential's pin stayed")
	}
}

// CR-16: the wiki's bucket at a self-signed address.
func TestTrustingTheStoresCertificate(t *testing.T) {
	f := newFixture(t)
	f.s.o.Store = nil
	b := s3test.NewHandler("wiki", "eu-west-1", docsKey)
	cert := certpintest.SelfSigned(t)
	srv := certpintest.Server(t, cert, b.Handler())
	fp := certpin.Fingerprint(cert.Leaf.Raw)
	id, secret := docsKeyID, docsSecret
	in := StoreInput{Store: StoreSettings{Kind: StoreS3, Endpoint: srv.URL, Region: "eu-west-1", Bucket: "wiki", PathStyle: true, Credentials: CredentialsKeys}, Keys: CredsChange{AccessKeyID: &id, SecretAccessKey: &secret}}
	body, _ := json.Marshal(in)
	w := serve(t, f.s, &admin, "POST", "store/test", string(body), nil)
	if !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), fp) {
		t.Fatalf("an untrusted store: %s", w.Body)
	}
	if _, err := f.s.TrustStore(ctx, admin, fp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unsaved store: %v", err)
	}
	if _, err := f.s.SaveStore(ctx, admin, in); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	if _, err := f.s.TrustStore(ctx, bob, fp); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin: %v", err)
	}
	if p, err := f.s.TrustStore(ctx, admin, fp); err != nil || p.With != "store" {
		t.Fatalf("trust: %+v %v", p, err)
	}
	f.s.Wait()
	if _, err := f.s.TestStore(ctx, admin, in); err != nil {
		t.Fatalf("pinned: %v", err)
	}
	// Saved again at the same address, the pin stays.
	if st, err := f.s.SaveStore(ctx, admin, in); err != nil || st.PinnedCert.Fingerprint() != fp {
		t.Fatalf("saved again: %+v %v", st.PinnedCert, err)
	}
	f.s.Wait()
	if err := f.s.ForgetStore(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TestStore(ctx, admin, in); err == nil {
		t.Fatal("forgotten, the certificate was accepted")
	}
	if !slices.Contains(f.actions(), ActionTrust) || !slices.Contains(f.actions(), ActionForget) {
		t.Fatalf("audited: %v", f.actions())
	}
}
