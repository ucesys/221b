package clusters_test

// Tests of the clusters review of November 25, 2026 (review-clusters.md):
// each names its finding.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/clusters"
)

// started is a manager over store, started, with what it needs stopped
// at the test's end.
func started(t *testing.T, store clusters.Store) (*clusters.Manager, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := clusters.New(store, quiet)
	must(t, m.Start(ctx))
	t.Cleanup(m.Stop)
	return m, ctx
}

// listener is an API server that answers nothing and keeps the
// Authorization header of every request it is sent.
type listener struct {
	mu   sync.Mutex
	auth []string
	srv  *httptest.Server
}

func listen(t *testing.T) *listener {
	l := &listener{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.auth = append(l.auth, r.Header.Get("Authorization"))
		l.mu.Unlock()
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *listener) sent() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.auth {
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

// Finding 2: an edit that changed the API server and gave no credential
// fell back to the saved one, so Test sent the saved token to whatever
// server the draft named, and a save re-pointed the connection with it.
func TestASavedCredentialIsNeverSentToAnotherServer(t *testing.T) {
	_, srv := simServer(t)
	m, ctx := started(t, clusters.NewMemoryStore())
	sa := saToken("prod")
	spec := clusters.Spec{Name: "prod", Env: "production", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}
	if _, err := m.Add(ctx, clusters.Draft{Spec: spec, Credential: clusters.Credential{Token: sa}}); err != nil {
		t.Fatal(err)
	}
	attacker := listen(t)
	moved := spec
	moved.Server, moved.CA = attacker.srv.URL, "skip"
	_, err := m.TestDraft(ctx, "prod", clusters.Draft{Spec: moved})
	var v *clusters.ValidationError
	if !errors.As(err, &v) || v.Field != "token" {
		t.Errorf("a test against another server with the saved token: %v", err)
	}
	if _, err := m.Update(ctx, "prod", clusters.Draft{Spec: moved}); !errors.As(err, &v) || v.Field != "token" {
		t.Errorf("a re-point with the saved token: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := attacker.sent(); len(got) > 0 {
		t.Fatalf("the saved token went to another server: %d requests", len(got))
	}
	// Another path on the same server keeps it; so does a new credential.
	same := spec
	same.Server = srv.URL + "/"
	if _, err := m.TestDraft(ctx, "prod", clusters.Draft{Spec: same}); err != nil {
		t.Errorf("the same server: %v", err)
	}
	if _, err := m.TestDraft(ctx, "prod", clusters.Draft{Spec: moved, Credential: clusters.Credential{Token: saToken("other")}}); err != nil {
		t.Errorf("a new credential for another server: %v", err)
	}
}
