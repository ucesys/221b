package clusters_test

// Tests of the clusters review of November 25, 2026 (review-clusters.md):
// each names its finding.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/clusters/clusterstest"
	"github.com/ucesys/221b/server/internal/kube/kubetest"
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

// kubeconfigFor is a kubeconfig of one context, "here", that signs in to
// server with tok; extra is more of the cluster entry (its CA, a proxy,
// a TLS server name), each line indented as the entry's fields are.
func kubeconfigFor(server, tok string, extra ...string) string {
	more := ""
	for _, e := range extra {
		more += "\n    " + e
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: here
  cluster:
    server: %s%s
users:
- name: console
  user:
    token: %s
contexts:
- name: here
  context:
    cluster: here
    user: console
current-context: here
`, server, more, tok)
}

// caData is a kubeconfig's certificate-authority-data line for a PEM.
func caData(pemText string) string {
	return "certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte(pemText))
}

// Finding 6: a kubeconfig pasted again on a connection that checks the
// server against a CA kept the old saved CA rather than the new
// kubeconfig's, so the connection then failed TLS.
func TestAKubeconfigPastedAgainBringsItsOwnCA(t *testing.T) {
	for _, tc := range []struct{ name string }{{"memory"}, {"secrets"}} {
		t.Run(tc.name, func(t *testing.T) {
			store := clusters.Store(clusters.NewMemoryStore())
			if tc.name == "secrets" {
				store = clusterstest.Secrets(kubetest.Cluster())
			}
			m, ctx := started(t, store)
			oldCA, _ := selfSigned(t, "old-ca", time.Now().Add(24*time.Hour))
			newCA, _ := selfSigned(t, "new-ca", time.Now().Add(24*time.Hour))
			spec := clusters.Spec{Name: "kc", Env: "development", Server: "https://10.9.0.1:6443", CA: "pem", Auth: "kubeconfig", Mode: "read"}
			if _, err := m.Add(ctx, clusters.Draft{Spec: spec, Credential: clusters.Credential{Kubeconfig: kubeconfigFor(spec.Server, saToken("kc"), caData(oldCA))}}); err != nil {
				t.Fatal(err)
			}
			if _, cred, _ := store.Get(ctx, "kc"); cred.CAPEM != oldCA {
				t.Fatal("the first kubeconfig's CA was not taken")
			}
			// The cluster's CA is rotated: the fresh kubeconfig is pasted, no CA.
			if _, err := m.Update(ctx, "kc", clusters.Draft{Spec: spec, Credential: clusters.Credential{Kubeconfig: kubeconfigFor(spec.Server, saToken("kc"), caData(newCA))}}); err != nil {
				t.Fatal(err)
			}
			_, cred, _ := store.Get(ctx, "kc")
			if cred.CAPEM != newCA {
				t.Fatalf("the old CA was kept over the new kubeconfig's: old %v", cred.CAPEM == oldCA)
			}
			// A kubeconfig with no CA of its own keeps the saved one.
			if _, err := m.Update(ctx, "kc", clusters.Draft{Spec: spec, Credential: clusters.Credential{Kubeconfig: kubeconfigFor(spec.Server, saToken("kc"))}}); err != nil {
				t.Fatal(err)
			}
			if _, cred, _ := store.Get(ctx, "kc"); cred.CAPEM != newCA {
				t.Fatal("a kubeconfig without a CA lost the saved one")
			}
		})
	}
}

// Finding 5: a save wrote the Secret before its row committed, and a
// read paired the committed row with the live Secret, so a save whose
// COMMIT failed after its Secret was written (a request cancelled, a
// failover) left every replica on the old server with the new token. A
// removal deleted the Secret before its commit, so one reported as failed
// could still lose the credential.
func TestAWriteWhoseCommitFailsPairsNoTokenWithAnotherServer(t *testing.T) {
	store, _, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	spec, a := fullSpec("prod"), fullCred("a")
	spec.Server = "https://a.example:6443"
	must(t, store.Save(ctx, spec, &a))
	// The request ends just after the Secret is written: COMMIT fails.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cut := false
	for _, verb := range []string{"update", "delete"} {
		cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			if cut {
				cancel()
			}
			return false, nil, nil
		})
	}
	cut = true
	moved, b := spec, fullCred("b")
	moved.Server = "https://b.example:6443"
	if err := store.Save(wctx, moved, &b); err == nil {
		t.Fatal("a save whose commit failed went through")
	}
	cut = false
	got, cred, err := store.Get(ctx, "prod")
	if err != nil || got.Server != spec.Server {
		t.Fatalf("after the failed save: %v %s", err, got.Server)
	}
	if cred.Token == b.Token {
		t.Fatal("the old server is paired with the new token")
	}
	// The connection starts as failed, saying why, and sends nothing.
	m, mctx := started(t, store)
	if s, _ := m.Summary("prod"); s.State != "failed" || !strings.Contains(s.Message, "did not finish") {
		t.Errorf("the connection after the failed save: %s %q", s.State, s.Message)
	}
	// Edited with its credential, it is whole again.
	edit := moved
	edit.Name, edit.CA = "prod", "system"
	if _, err := m.Update(mctx, "prod", clusters.Draft{Spec: edit, Credential: clusters.Credential{Token: saToken("prod")}}); err != nil {
		t.Fatal(err)
	}
	if got, cred, _ := store.Get(ctx, "prod"); got.Server != moved.Server || cred.Token != saToken("prod") {
		t.Fatalf("the edit after it: %s, token given %v", got.Server, cred.Token == saToken("prod"))
	}

	// A removal whose commit fails keeps the credential.
	_, before, _ := store.Get(ctx, "prod")
	dctx, cancelD := context.WithCancel(ctx)
	defer cancelD()
	cancel = cancelD
	cut = true
	if err := store.Delete(dctx, "prod"); err == nil {
		t.Fatal("a removal whose commit failed went through")
	}
	cut = false
	if _, cred, err := store.Get(ctx, "prod"); err != nil || cred != before {
		t.Fatalf("a removal reported as failed lost the credential: %v, kept %v", err, cred == before)
	}
}
