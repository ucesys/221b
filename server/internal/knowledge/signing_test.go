package knowledge

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/certpin/certpintest"
	"github.com/ucesys/221b/server/internal/credentials"
)

// asked records what a test site was sent, by path.
type asked struct {
	mu   sync.Mutex
	auth map[string][]string
	cert map[string]bool
}

func (a *asked) note(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.auth == nil {
		a.auth, a.cert = map[string][]string{}, map[string]bool{}
	}
	a.auth[r.URL.Path] = append(a.auth[r.URL.Path], r.Header.Get("Authorization")+"|"+r.URL.RawQuery+"|"+r.Header.Get("Referer"))
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		a.cert[r.URL.Path] = true
	}
}

func (a *asked) of(path string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auth[path]...)
}

func page(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte("<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"))
}

// CR-03, CR-04: a credential sent as a query parameter is in the request
// only: no page's id, address or title, no page left out, and no live
// page an agent is given holds it, even when the site writes it back
// into a redirect.
func TestAQueryCredentialIsNeverInAnAddress(t *testing.T) {
	f := newFixture(t)
	const key = "qk-secret-0123456789"
	var seen asked
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		switch {
		case r.URL.Path == "/robots.txt":
			http.NotFound(w, r)
		case !strings.HasPrefix(r.URL.Path, "/docs/"):
			page(w, "Outside", "<p>Not the docs.</p>")
		case r.URL.Query().Get("api_key") != key:
			http.Error(w, "no", http.StatusUnauthorized)
		case r.URL.Path == "/docs/old":
			// The site writes the key back into where it sends.
			http.Redirect(w, r, "/docs/new?api_key="+url.QueryEscape(key)+"&v=2", http.StatusFound)
		case r.URL.Path == "/docs/gone":
			http.Error(w, "gone", http.StatusGone)
		default:
			page(w, "Runbook "+r.URL.Path, `<p>Rotate the edge certificates each spring.</p><a href="/docs/old">old</a> <a href="/docs/b?x=1">b</a> <a href="/docs/gone">gone</a> <a href="/outside">out</a>`)
		}
	}))
	t.Cleanup(srv.Close)
	f.s.o.CrawlHTTP, f.s.o.HTTP = srv.Client(), srv.Client()
	cr, err := f.creds.Create(ctx, credentials.Credential{Name: "docs", Kind: credentials.KindToken, Endpoint: srv.URL + "/docs", SendAs: credentials.SendAs{Method: credentials.SendQuery, Param: "api_key"}}, credentials.Secret{Token: key}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveSourceWith(ctx, admin, Source{ID: "docs", Name: "Docs", Kind: SourceWeb, URL: srv.URL + "/docs/", Depth: 2, Pages: 20, Credential: cr.ID}, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	if res := syncSource(t, f, "docs"); res.Read < 3 {
		t.Fatalf("read: %+v", res)
	}
	items, err := f.s.Items(ctx, admin, ItemsQuery{Source: "docs", Limit: MaxItemsLimit})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(items)
	if strings.Contains(string(b), key) || strings.Contains(string(b), "api_key") {
		t.Fatalf("the key is in the items: %s", b)
	}
	if !strings.Contains(string(b), `"/docs/new?v=2"`) || !strings.Contains(string(b), `"/docs/b?x=1"`) || !strings.Contains(string(b), "/docs/gone") {
		t.Fatalf("the pages, by their own addresses: %s", b)
	}
	if got := seen.of("/outside"); len(got) == 0 || strings.Contains(strings.Join(got, ","), key) {
		t.Fatalf("outside the endpoint: %q", got)
	}

	// A live source given to an agent.
	if _, err := f.s.SaveSourceWith(ctx, admin, Source{ID: "status", Name: "Status", Kind: SourceLive, Credential: cr.ID, Addresses: []LiveAddress{{Kind: LivePage, URL: srv.URL + "/docs/a"}, {Kind: LivePage, URL: srv.URL + "/docs/old"}, {Kind: LivePage, URL: srv.URL + "/docs/gone"}}}, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	res, err := f.s.LookupLive(ctx, "edge certificates")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(res)
	if len(res.Pages) != 2 || strings.Contains(string(b), key) || strings.Contains(string(b), "api_key") {
		t.Fatalf("the key reached the lookup: %s", b)
	}
	if res.Pages[0].URL != srv.URL+"/docs/a" || res.Pages[1].URL != srv.URL+"/docs/new?v=2" {
		t.Fatalf("the pages' addresses: %+v", res.Pages)
	}
}

// CR-04: a redirect on the same site carries the credential only to an
// address under the endpoint; Go's own copying of the first request's
// headers sends it nowhere else.
func TestARedirectIsSignedOnlyUnderTheEndpoint(t *testing.T) {
	f := newFixture(t)
	const tok = "bearer-secret-0123456789"
	var seen asked
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/docs/out":
			http.Redirect(w, r, "/private/x", http.StatusFound)
		case "/docs/in":
			http.Redirect(w, r, "/docs/a", http.StatusMovedPermanently)
		case "/private/x":
			page(w, "Private", "<p>Somewhere else on the site.</p>")
		default:
			if r.Header.Get("Authorization") != "Bearer "+tok {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			page(w, "Docs "+r.URL.Path, `<p>The runbook.</p><a href="/docs/out">out</a> <a href="/docs/in">in</a>`)
		}
	}))
	t.Cleanup(srv.Close)
	f.s.o.CrawlHTTP, f.s.o.HTTP = srv.Client(), srv.Client()
	cr, err := f.creds.Create(ctx, credentials.Credential{Name: "docs", Kind: credentials.KindToken, Endpoint: srv.URL + "/docs"}, credentials.Secret{Token: tok}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveSourceWith(ctx, admin, Source{ID: "docs", Name: "Docs", Kind: SourceWeb, URL: srv.URL + "/docs/", Depth: 2, Pages: 20, Credential: cr.ID}, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	syncSource(t, f, "docs")
	if _, err := f.s.SaveSourceWith(ctx, admin, Source{ID: "status", Name: "Status", Kind: SourceLive, Credential: cr.ID, Addresses: []LiveAddress{{Kind: LivePage, URL: srv.URL + "/docs/out"}, {Kind: LivePage, URL: srv.URL + "/docs/in"}}}, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.LookupLive(ctx, "runbook"); err != nil {
		t.Fatal(err)
	}
	priv := seen.of("/private/x")
	if len(priv) < 2 {
		t.Fatalf("not redirected to: %q", priv)
	}
	for _, a := range priv {
		if strings.Contains(a, tok) {
			t.Fatalf("the credential went outside the endpoint: %q", priv)
		}
	}
	// Under it, the redirect is signed again.
	in := seen.of("/docs/a")
	if len(in) == 0 {
		t.Fatal("not read")
	}
	for _, a := range in {
		if !strings.HasPrefix(a, "Bearer "+tok+"|") {
			t.Fatalf("unsigned under the endpoint: %q", in)
		}
	}
}

// CR-04: a live source's client certificate goes only to the addresses
// its credential covers now, not to every address because the first is.
func TestALiveSourcesClientCertificateGoesOnlyUnderItsEndpoint(t *testing.T) {
	f := newFixture(t)
	var seen asked
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.note(r)
		page(w, "Status "+r.URL.Path, "<p>Mail is slow.</p>")
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	f.s.o.CrawlHTTP, f.s.o.HTTP = srv.Client(), srv.Client()
	client := certpintest.SelfSigned(t, "client.test")
	der, err := x509.MarshalECPrivateKey(client.TLS.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	cr, err := f.creds.Create(ctx, credentials.Credential{Name: "mtls", Kind: credentials.KindClientCert, Endpoint: srv.URL, CertPEM: client.PEM}, credentials.Secret{KeyPEM: keyPEM}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveSourceWith(ctx, admin, Source{ID: "status", Name: "Status", Kind: SourceLive, Credential: cr.ID, Addresses: []LiveAddress{{Kind: LivePage, URL: srv.URL + "/a/status"}, {Kind: LivePage, URL: srv.URL + "/b/status"}}}, CredsChange{}, true); err != nil {
		t.Fatal(err)
	}
	// The credential is narrowed to the first address's path since.
	if _, err := f.creds.Update(ctx, cr.ID, credentials.Credential{Name: "mtls", Kind: credentials.KindClientCert, Endpoint: srv.URL + "/a", CertPEM: client.PEM}, nil, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.LookupLive(ctx, "mail"); err != nil {
		t.Fatal(err)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if !seen.cert["/a/status"] || seen.cert["/b/status"] || len(seen.auth["/b/status"]) == 0 {
		t.Fatalf("the certificate went to %v (asked %v)", seen.cert, seen.auth)
	}
}

// KN-26: a test of a saved source, typed with another address, never
// sends the credential the source keeps of before 0.12.1 there.
func TestATestAsTypedSendsAKeptCredentialToItsOwnAddressOnly(t *testing.T) {
	f := newFixture(t)
	const tok = "kept-token-0123456789"
	var own, other asked
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		own.note(r)
		page(w, "Runbook", "<p>Ours.</p>")
	}))
	t.Cleanup(srv.Close)
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.note(r)
		page(w, "Elsewhere", "<p>Theirs.</p>")
	}))
	t.Cleanup(elsewhere.Close)
	f.s.o.CrawlHTTP, f.s.o.HTTP = srv.Client(), srv.Client()
	src := Source{ID: "intra", Name: "Intranet", Kind: SourceWeb, URL: srv.URL + "/ops/", Pages: 5, Auth: LiveAuthBearer}
	if _, err := f.s.SaveSource(ctx, admin, src, &[]string{tok}[0], true); err != nil {
		t.Fatal(err)
	}
	// Its own address: the kept token goes.
	if _, err := f.s.TestSourceAsTyped(ctx, admin, src, CredsChange{}, "intra"); err != nil {
		t.Fatal(err)
	}
	if got := own.of("/ops/"); len(got) == 0 || !strings.HasPrefix(got[0], "Bearer "+tok) {
		t.Fatalf("its own address: %q", got)
	}
	// Another address: it does not.
	typed := src
	typed.URL = elsewhere.URL + "/ops/"
	_, err := f.s.TestSourceAsTyped(ctx, admin, typed, CredsChange{}, "intra")
	other.mu.Lock()
	defer other.mu.Unlock()
	for p, a := range other.auth {
		if strings.Contains(strings.Join(a, ","), tok) {
			t.Fatalf("the kept token went to %s: %q", p, a)
		}
	}
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("the test: %v", err)
	}
}
