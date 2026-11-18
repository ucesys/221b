package apps

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func allowLocal(t *testing.T) {
	t.Helper()
	AllowLocalRepos = true
	t.Cleanup(func() { AllowLocalRepos = false })
}

func allowHosts(t *testing.T, hosts ...string) {
	t.Helper()
	if err := SetAllowedHosts(hosts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetAllowedHosts(nil) })
}

// helmRepo is a Helm repository at u, fetched with the client given.
func helmGetWith(c *http.Client, u string) error {
	r := NewRepos(RepoOptions{Client: c})
	_, err := r.helmGet(context.Background(), Repository{Name: "x", Kind: KindHelm, URL: u, Auth: AuthNone}, "", u+"/index.yaml", maxSmall)
	return err
}

// Every failure on the way to a repository reads the same to the person:
// a refused connection, a TLS failure, an unexpected status, a name that
// does not resolve. A refused sign-in and a missing repository keep their
// own words, which say how to fix the source.
func TestAFetchThatFailsOnTheWayReadsTheSame(t *testing.T) {
	allowLocal(t)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + closed.Addr().String()
	closed.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(tlsSrv.Close)
	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(status.Close)
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(locked.Close)
	plain := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	got := map[string]error{
		"refused": helmGetWith(plain, refused),
		"tls":     helmGetWith(plain, tlsSrv.URL),
		"status":  helmGetWith(plain, status.URL),
		"dns":     helmGetWith(plain, "https://nothing-here.invalid"),
	}
	for what, err := range got {
		if !errors.Is(err, ErrNotFetched) || err.Error() != ErrNotFetched.Error() || FetchCause(err) == "" {
			t.Errorf("%s: %v", what, err)
		}
	}
	if err := helmGetWith(plain, missing.URL); err == nil || errors.Is(err, ErrNotFetched) || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing: %v", err)
	}
	if err := helmGetWith(plain, locked.URL); err == nil || errors.Is(err, ErrNotFetched) || !strings.Contains(err.Error(), "needs credentials") {
		t.Errorf("signed in: %v", err)
	}
	// go-git's failures: the same words for the network's, its own for a
	// token and a repository.
	wrap := func(err error) error { return &plumbing.UnexpectedError{Err: err} }
	gits := []error{
		wrap(&url.Error{Op: "Get", URL: "https://10.0.0.5:8443/a/info/refs", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}),
		wrap(&url.Error{Op: "Get", URL: "https://10.0.0.5:8443/a/info/refs", Err: x509.UnknownAuthorityError{}}),
		wrap(&githttp.Err{Response: &http.Response{StatusCode: http.StatusBadGateway, Request: &http.Request{URL: &url.URL{Host: "10.0.0.5"}}}}),
		&url.Error{Op: "Get", URL: "https://nothing-here.invalid/a", Err: &net.DNSError{Err: "no such host", Name: "nothing-here.invalid", IsNotFound: true}},
		context.DeadlineExceeded,
		errors.New("http redirect: changes scheme from \"https\" to \"http\""),
	}
	for _, e := range gits {
		if err := describe(e); err.Error() != ErrNotFetched.Error() {
			t.Errorf("%v: %v", e, err)
		}
	}
	if err := describe(transport.ErrAuthenticationRequired); !strings.Contains(err.Error(), "credential") {
		t.Errorf("a credential: %v", err)
	}
	if err := describe(transport.ErrRepositoryNotFound); !strings.Contains(err.Error(), "no repository at that URL") {
		t.Errorf("no repository: %v", err)
	}
}

// apps.allowedHosts: listed, a source on any other host is refused when it
// is saved and when the console connects, redirects included; *.domain
// takes any name under it, :port that port only; empty allows any.
func TestOnlyTheAllowedHostsAreFetchedFrom(t *testing.T) {
	ctx := context.Background()
	allowLocal(t)
	for _, bad := range []string{"*", "git.example.com/x", "git.example.com:99999", "user@git.example.com", "*.*.example.com"} {
		if err := SetAllowedHosts([]string{bad}); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	allowHosts(t, "git.example.com", "*.corp.example", "charts.example.org:8443")
	for addr, ok := range map[string]bool{
		"https://git.example.com/a/b":          true,
		"https://GIT.example.com./a/b":         true,
		"https://git.example.com:8443/a/b":     true,
		"https://a.corp.example/a/b":           true,
		"https://a.b.corp.example/a/b":         true,
		"oci://registry.corp.example/charts":   true,
		"https://charts.example.org:8443/x":    true,
		"https://corp.example/a/b":             false,
		"https://evilcorp.example/a/b":         false,
		"https://git.example.com.evil.net/a/b": false,
		"https://charts.example.org/x":         false,
		"https://10.0.0.5:8443/a":              false,
		"oci://registry.example.net/charts":    false,
	} {
		err := CheckSourceHost(ctx, addr)
		if ok != (err == nil) || !ok && !errors.Is(err, errHostNotAllowed) {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := CheckRepoHost(ctx, "https://10.0.0.5:8443/a"); !errors.Is(err, errHostNotAllowed) {
		t.Errorf("CheckRepoHost: %v", err)
	}
	// At the connection: git's and Helm's, and a redirect off the list.
	if _, err := ListRefs(ctx, "https://10.0.0.5:8443/a", GitAuth{}); !errors.Is(err, errHostNotAllowed) {
		t.Errorf("git: %v", err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("entries: {}\n")) }))
	t.Cleanup(other.Close)
	_, otherPort, _ := net.SplitHostPort(other.Listener.Addr().String())
	listed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+otherPort+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(listed.Close)
	base := http.DefaultTransport.(*http.Transport).Clone()
	c := &http.Client{Transport: allowedTransport{base}}
	if err := helmGetWith(c, other.URL); !errors.Is(err, errHostNotAllowed) {
		t.Errorf("helm: %v", err)
	}
	allowHosts(t, listed.Listener.Addr().String())
	if err := helmGetWith(c, listed.URL); !errors.Is(err, errHostNotAllowed) {
		t.Errorf("redirected: %v", err)
	}
	allowHosts(t, listed.Listener.Addr().String(), "localhost")
	if err := helmGetWith(c, listed.URL); err != nil {
		t.Errorf("redirected to a listed host: %v", err)
	}
	// Empty: any host the other checks allow.
	allowHosts(t)
	if err := CheckSourceHost(ctx, "https://anything.example.net/a"); err != nil || !hostAllowed("anything.example.net", "443") {
		t.Errorf("empty list: %v", err)
	}
}
