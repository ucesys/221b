package credentials

// The fixes of the code review of November 25 (0.13.3): each test names
// the review's item it holds to.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
