package credentials

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
)

// CR-04: an address with a . or .. segment, or an encoded / or \, is
// under no endpoint, and no endpoint has one: a server that resolves them
// would be asked outside the endpoint's path.
func TestDotSegmentsAndEncodedSeparatorsAreUnderNoEndpoint(t *testing.T) {
	for _, ep := range []string{"https://github.com/ucesys", "github.com/ucesys", "oci://ghcr.io/ucesys", "git@github.com:ucesys"} {
		e, err := ParseEndpoint(ep)
		if err != nil {
			t.Fatal(err)
		}
		host := "github.com"
		if strings.HasPrefix(ep, "oci") {
			host = "ghcr.io"
		}
		scheme := "https"
		if strings.HasPrefix(ep, "git@") {
			scheme = "ssh"
		}
		for _, tail := range []string{
			"/ucesys/../other/x", "/ucesys/%2e%2e/other/x", "/ucesys/%2E%2E/other", "/ucesys/.%2e/other",
			"/ucesys/./x", "/ucesys/..", "/ucesys%2F..%2Fother", "/ucesys/x%2fy", "/ucesys%5C..%5Cother", `/ucesys/x\..\y`,
		} {
			target := scheme + "://" + host + tail
			if e.Covers(target) {
				t.Errorf("%s covers %s", ep, target)
			}
		}
		if !e.Covers(scheme + "://" + host + "/ucesys/x.git") {
			t.Errorf("%s no longer covers its own", ep)
		}
	}
	for _, ep := range []string{"https://github.com/ucesys/../other", "https://github.com/ucesys/%2e%2e", "https://github.com/a%2Fb", "https://github.com/a%5cb", "git@github.com:ucesys/../x", "oci://ghcr.io/org/./x"} {
		if _, err := ParseEndpoint(ep); !errors.Is(err, ErrInvalid) {
			t.Errorf("the endpoint %s: %v", ep, err)
		}
	}
	// Signing checks it too.
	s, _ := newService(t, nil)
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{}, "tok-secret")
	if _, err := s.Resolve(ctx, c.ID, UseHTTP, "https://github.com/ucesys/%2e%2e/other"); !errors.Is(err, ErrOutside) {
		t.Fatalf("resolved outside: %v", err)
	}
	u, err := s.Resolve(ctx, c.ID, UseHTTP, "https://github.com/ucesys/x")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://github.com/ucesys/../other", "https://github.com/ucesys/%2e%2e/other", "https://github.com/ucesys%2F..%2Fother", "https://github.com/ucesys%5C..%5Cother"} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if err := u.Apply(req); !errors.Is(err, ErrOutside) || req.Header.Get("Authorization") != "" {
			t.Errorf("signed %s: %v", raw, err)
		}
		if u.Covers(raw) {
			t.Errorf("covers %s", raw)
		}
	}
}

// An s3:// endpoint is refused: a bucket's use names its store's https://
// address and the bucket, which such an endpoint never covered.
func TestABucketsEndpointIsItsStoresAddress(t *testing.T) {
	s, _ := newService(t, nil)
	if _, err := s.Create(ctx, Credential{Name: "b", Kind: KindAccessKey, Endpoint: "s3://docs"}, Secret{AccessKeyID: "AKIA1", SecretAccessKey: "secret-1"}, "ann"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "s3.amazonaws.com/BUCKET") {
		t.Fatalf("s3:// taken: %v", err)
	}
	c, err := s.Create(ctx, Credential{Name: "b", Kind: KindAccessKey, Endpoint: S3Address("", "docs")}, Secret{AccessKeyID: "AKIA1", SecretAccessKey: "secret-1"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckChoice(ctx, c.ID, UseS3, S3Address("", "docs")); err != nil {
		t.Fatal(err)
	}
}

// hookedSecret runs a hook once, at the first read of a key, as if
// another replica wrote at that moment.
type hookedSecret struct {
	*MemorySecret
	mu   sync.Mutex
	key  string
	hook func()
}

func (h *hookedSecret) LoadKey(ctx context.Context, key string) ([]byte, string, bool, error) {
	h.mu.Lock()
	f := h.hook
	if key == h.key {
		h.hook = nil
	}
	h.mu.Unlock()
	if key == h.key && f != nil {
		f()
	}
	return h.MemorySecret.LoadKey(ctx, key)
}

// CR-16, CR-19: a Trust or a Forget that races a change of the
// credential acts on the credential as it is when it is written: a pin
// never lands on an endpoint moved to another host, and a forgotten pin
// is not brought back by a change that read it before.
func TestAPinFollowsTheCredentialAsItIsNow(t *testing.T) {
	cert := certpintest.SelfSigned(t)
	srv := certpintest.NewSwitchable(t, cert, nil)
	fp := certpin.Fingerprint(cert.Leaf.Raw)

	// The endpoint moves to another host while the certificate is looked
	// at: nothing is pinned.
	var s *Service
	var id string
	moved := false
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !moved {
			moved = true
			if _, err := s.Update(ctx, id, Credential{Name: "self-signed", Kind: KindToken, Endpoint: "https://elsewhere.example"}, nil, "bob"); err != nil {
				t.Error(err)
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	s = open(t, Options{HTTP: pinService(t).o.HTTP, Dial: dial})
	c := token(t, s, "self-signed", srv.URL, SendAs{}, "tok-1")
	id = c.ID
	if _, _, err := s.Trust(ctx, c.ID, "", fp, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pinned onto the moved credential: %v", err)
	}
	if got, _ := s.Get(ctx, c.ID); got.PinnedCert != nil || got.Endpoint != "https://elsewhere.example" {
		t.Fatalf("%+v", got)
	}

	// A Forget between a change's read and its write stays forgotten.
	mem := &hookedSecret{MemorySecret: &MemorySecret{}}
	s = open(t, Options{Secrets: mem, HTTP: pinService(t).o.HTTP, Dial: pinService(t).o.Dial})
	c = token(t, s, "self-signed", srv.URL, SendAs{}, "tok-1")
	if _, _, err := s.Trust(ctx, c.ID, "", fp, "ann"); err != nil {
		t.Fatal(err)
	}
	mem.mu.Lock()
	mem.key, mem.hook = secretKey(c.ID), func() {
		if _, err := s.Forget(ctx, c.ID); err != nil {
			t.Error(err)
		}
	}
	mem.mu.Unlock()
	got, err := s.Update(ctx, c.ID, Credential{Name: "self-signed", Kind: KindToken, Endpoint: srv.URL, Description: "renamed"}, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := s.Get(ctx, c.ID); got.PinnedCert != nil || kept.PinnedCert != nil {
		t.Fatalf("the forgotten pin came back: %+v", kept.PinnedCert)
	}
	// A pin trusted meanwhile is kept by a change on the same host.
	if _, _, err := s.Trust(ctx, c.ID, "", fp, "ann"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Update(ctx, c.ID, Credential{Name: "self-signed", Kind: KindToken, Endpoint: srv.URL, Description: "again"}, nil, "bob"); got.PinnedCert == nil {
		t.Fatal("the pin was dropped on the same host")
	}
}

// CR-13: a backup's credential whose name another credential has here
// comes back under its own id, which what the backup restores with it
// names, with a name of its own.
func TestARestoredCredentialKeepsItsIDUnderAnotherName(t *testing.T) {
	s, _ := newService(t, nil)
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{}, "ghp_from_backup")
	seal := func(v string) string { return "<sealed:k:" + base64.StdEncoding.EncodeToString([]byte(v)) + ">" }
	list, err := s.Export(ctx, seal)
	if err != nil {
		t.Fatal(err)
	}
	open := func(in, _, _ string) (string, error) {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(in, "<sealed:k:"), ">"))
		return string(b), nil
	}
	check := func(string) error { return nil }
	into, _ := newService(t, nil)
	token(t, into, "github", "https://github.com/other", SendAs{}, "ghp_here")
	token(t, into, "GitHub (restored)", "https://github.com/other2", SendAs{}, "ghp_here2")
	var notes []string
	n, err := into.Import(ctx, list, check, open, func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) })
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	got, err := into.Get(ctx, c.ID)
	if err != nil || got.Name != "GitHub (restored 2)" {
		t.Fatalf("restored as %+v %v", got, err)
	}
	if u, err := into.Resolve(ctx, c.ID, UseHTTP, "https://github.com/ucesys/x"); err != nil || u.Token != "ghp_from_backup" {
		t.Fatalf("%+v %v", u, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "restored as GitHub (restored 2)") {
		t.Fatalf("%q", notes)
	}
	long := strings.Repeat("é", 50)
	if name := suffixedName(nil, long, restoredSuffix); len(name) > 100 || !utf8.ValidString(name) || !strings.HasSuffix(name, " (restored)") {
		t.Fatalf("%q", name)
	}
}

// CR-12: a credential the upgrade made from many uses has a description
// and a name within their bounds, so it can be used and changed.
func TestTheUpgradesNamesAndDescriptionsAreBounded(t *testing.T) {
	s, _ := newService(t, nil)
	f := &fakeConsumer{switched: map[string]string{}}
	for i := range 60 {
		f.found = append(f.found, Found{Key: fmt.Sprint(i), Label: fmt.Sprintf("app team-%02d/some-application-%02d", i, i), Endpoint: "https://github.com/" + strings.Repeat("ü", 40), Kind: KindToken, Secret: Secret{Token: "same-token"}})
	}
	created, err := s.Migrate(ctx, []Consumer{f}, "upgrade")
	if err != nil || len(created) != 1 {
		t.Fatalf("%+v %v", created, err)
	}
	c, _ := s.Get(ctx, created[0].ID)
	if len(c.Description) > 500 || !strings.HasSuffix(c.Description, " and 59 more.") || !strings.Contains(c.Description, "app team-00/some-application-00") {
		t.Fatalf("description %d: %q", len(c.Description), c.Description)
	}
	if len(c.Name) > 100 || !utf8.ValidString(c.Name) {
		t.Fatalf("name %q", c.Name)
	}
	if _, err := s.Resolve(ctx, c.ID, UseHTTP, "https://github.com/"+strings.Repeat("ü", 40)+"/x"); err != nil {
		t.Fatalf("not usable: %v", err)
	}
	if _, err := s.Update(ctx, c.ID, Credential{Name: c.Name, Kind: c.Kind, Endpoint: c.Endpoint, Description: c.Description}, nil, "bob"); err != nil {
		t.Fatalf("not changeable: %v", err)
	}
	if len(created[0].From) != 60 {
		t.Fatalf("from %d", len(created[0].From))
	}
}

// The credentials' Secret stays under what Kubernetes keeps in one: a
// change that would grow it past SecretBudget is refused with why, one
// that shrinks it goes, and a removed credential's key leaves it.
func TestTheCredentialsSecretStaysUnderItsBound(t *testing.T) {
	mem := &MemorySecret{}
	s := open(t, Options{Secrets: mem})
	big := strings.Repeat("x", 16<<10)
	var made []Credential
	var err error
	for i := range MaxCredentials {
		var c Credential
		c, err = s.Create(ctx, Credential{Name: fmt.Sprint("big ", i), Kind: KindToken, Endpoint: "https://example.com"}, Secret{Token: big}, "ann")
		if err != nil {
			break
		}
		made = append(made, c)
	}
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "more than their Secret can hold") || len(made) < 50 || len(made) > 56 {
		t.Fatalf("%d made: %v", len(made), err)
	}
	if total, _, _ := mem.Sizes(ctx, ""); total > SecretBudget {
		t.Fatalf("%d bytes kept", total)
	}
	// The one refused left nothing behind.
	if list, _ := s.List(ctx); len(list) != len(made) {
		t.Fatalf("%d listed", len(list))
	}
	// Removing goes, and its key leaves the Secret.
	if _, err := s.Delete(ctx, made[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, found, _ := mem.LoadKey(ctx, secretKey(made[0].ID)); found {
		t.Fatal("the removed credential's key stays")
	}
	if _, err := s.Create(ctx, Credential{Name: "fits again", Kind: KindToken, Endpoint: "https://example.com"}, Secret{Token: big}, "ann"); err != nil {
		t.Fatal(err)
	}
}

// CR-04's second exception: an OAuth2 client's secret goes to its token
// URL, on whatever host, and nowhere else: a redirect from there is not
// followed with it.
func TestTheClientSecretGoesToTheTokenURLOnly(t *testing.T) {
	var mu sync.Mutex
	var elsewhere []string
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_, sec, _ := r.BasicAuth()
		mu.Lock()
		elsewhere = append(elsewhere, sec+r.PostForm.Get("client_secret"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"stolen","token_type":"Bearer"}`))
	}))
	defer other.Close()
	auth := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/token", http.StatusTemporaryRedirect)
	}))
	defer auth.Close()
	s, _ := newService(t, auth.Client())
	c, err := s.Create(ctx, Credential{Name: "api", Kind: KindOAuth2, Endpoint: "https://api.example", TokenURL: auth.URL + "/token", ClientID: "console"}, Secret{ClientSecret: "client-secret-1"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, c.ID, UseHTTP, "https://api.example/x"); err == nil {
		t.Fatal("a token came through a redirect")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(elsewhere) > 0 {
		t.Fatalf("the redirect was followed: %q", elsewhere)
	}
	for _, bad := range []string{"http://auth.example/token", "https://auth.example/a/../token", "https://auth.example/a%2Ftoken", "https://u:p@auth.example/token"} {
		if _, err := s.Create(ctx, Credential{Name: "x", Kind: KindOAuth2, Endpoint: "https://api.example", TokenURL: bad, ClientID: "console"}, Secret{ClientSecret: "client-secret-1"}, "ann"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
