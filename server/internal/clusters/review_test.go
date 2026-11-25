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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/clusters/clusterstest"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kube/kubetest"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/sim"
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

// Finding 4: after a rollback to a console from before the move and an
// upgrade again, a connection the older console made again under the same
// id lost its record from the Secret at the next start, unread, and ran
// with the database's old server and the older console's new token.
func TestAConnectionMadeAgainAfterARollbackIsNotPairedWithTheOldServer(t *testing.T) {
	store, d, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	spec, a := fullSpec("staging"), fullCred("a")
	spec.Server = "https://a.example:6443"
	must(t, store.Save(ctx, spec, &a))
	// Rolled back: the older console finds no staging, and it is added
	// again there, for another server, its record in its Secret.
	must(t, cs.CoreV1().Secrets(clusterstest.Namespace).Delete(ctx, clusterstest.SecretName("staging"), metav1.DeleteOptions{}))
	again, b := spec, fullCred("b")
	again.Server = "https://b.example:6443"
	older := clusterstest.Secrets(cs)
	must(t, older.Save(ctx, again, &b))
	// Upgraded again: the copy is not made twice; the records are dropped.
	if _, err := clusters.DropSpecs(ctx, d, older, nil); err != nil {
		t.Fatal(err)
	}
	got, cred, err := store.Get(ctx, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if got.Server == spec.Server && cred.Token == b.Token {
		t.Fatal("the old server is paired with the token of the connection made again")
	}
	if sec := held(t, cs)[clusterstest.SecretName("staging")]; sec["spec.json"] == "" {
		t.Error("the record of the connection made again was dropped unread")
	}
	m, mctx := started(t, store)
	if s, _ := m.Summary("staging"); s.State != "failed" || !strings.Contains(s.Message, "rollback") {
		t.Errorf("the connection: %s %q", s.State, s.Message)
	}
	// Edited with its credential, it is whole, and its record goes.
	edit := again
	edit.Name, edit.CA = "staging", "system"
	if _, err := m.Update(mctx, "staging", clusters.Draft{Spec: edit, Credential: clusters.Credential{Token: saToken("staging")}}); err != nil {
		t.Fatal(err)
	}
	if got, cred, _ := store.Get(ctx, "staging"); got.Server != again.Server || cred.Token != saToken("staging") || got.Unsettled != "" {
		t.Fatalf("after the edit: %s %q", got.Server, got.Unsettled)
	}
	if sec := held(t, cs)[clusterstest.SecretName("staging")]; sec["spec.json"] != "" {
		t.Error("the edit left the record in the Secret")
	}
}

// Finding 3: a console that started while its Secrets API failed (a 403
// from a webhook, KMS errors) showed the local cluster under the
// installation's name and environment "other", and an edit then saved
// those over the saved row: off production, with no two-person request.
func TestALocalEditNeverSavesWhatWasNotRead(t *testing.T) {
	store, _, cs := clusterstest.PG(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := clusters.Spec{ID: clusters.LocalID, Name: "eu-prod", Env: "production", Description: "saved", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster", Mode: "read", Local: true, Added: at, Updated: at}
	must(t, store.Save(ctx, saved, nil))
	install := clusters.Spec{Name: "here", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster"}
	edit := clusters.Draft{Spec: clusters.Spec{Name: "eu-prod", Env: "other", Description: "edited", Mode: "read"}}
	env := func(what string) {
		t.Helper()
		if got, _, err := store.Get(ctx, clusters.LocalID); err != nil || got.Env != "production" {
			t.Fatalf("%s: the local cluster's saved environment is %q (%v)", what, got.Env, err)
		}
	}

	// The Secrets API refuses every read; the database answers.
	refuse := error(apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("denied by a webhook")))
	failing(cs, &refuse, "get")
	failing(cs, &refuse, "list")
	m := clusters.New(store, quiet)
	must(t, m.SetLocal(ctx, install, nil, nil))
	_ = m.Start(ctx)
	s, _ := m.Summary(clusters.LocalID)
	if s.Name != "eu-prod" || s.Env != "production" {
		t.Errorf("the local cluster while the Secrets fail: %s, %s", s.Name, s.Env)
	}
	// The editor sends the environment the page shows.
	edit.Env = s.Env
	if _, err := m.Update(ctx, clusters.LocalID, edit); err != nil {
		t.Fatalf("a local edit while the Secrets fail: %v", err)
	}
	env("an edit while the Secrets fail")

	// The database is away at the start: nothing saved was read, and an
	// edit is refused rather than saving the installation's environment.
	away := fmt.Errorf("reading the connection: %w", db.ErrUnavailable)
	f := &flaky{Store: store}
	f.set(away, away, nil)
	m2 := clusters.New(f, quiet)
	must(t, m2.SetLocal(ctx, install, nil, nil))
	f.set(away, nil, nil)
	if _, err := m2.Update(ctx, clusters.LocalID, clusters.Draft{Spec: clusters.Spec{Name: "here", Env: "other", Description: "x", Mode: "read"}}); err == nil {
		t.Error("a local edit before its saved spec was read went through")
	}
	env("an edit before the saved spec was read")
	// Read at a sync, even one whose list fails (the Secrets), it is edited.
	f.set(nil, nil, nil)
	_ = m2.Sync(ctx)
	if s, _ := m2.Summary(clusters.LocalID); s.Env != "production" || s.Name != "eu-prod" {
		t.Fatalf("after a sync whose list failed: %s %s", s.Name, s.Env)
	}
	if _, err := m2.Update(ctx, clusters.LocalID, clusters.Draft{Spec: clusters.Spec{Name: "eu-prod", Env: "production", Description: "edited", Mode: "read"}}); err != nil {
		t.Fatalf("a local edit once read: %v", err)
	}
	refuse = nil
	if got, _, _ := store.Get(ctx, clusters.LocalID); got.Description != "edited" || got.Env != "production" {
		t.Fatalf("the edit once read: %+v", got)
	}
}

// Finding 7: a connection whose read failed once with an error the sync
// did not take for "could not be asked" (a statement timeout, a lock not
// granted, a Secret's 403 between the list and the get) was marked as
// read and missing from the leader until a restart.
func TestAConnectionThatCouldNotBeReadIsReadAgain(t *testing.T) {
	_, srv := simServer(t)
	store := clusters.NewMemoryStore()
	f := &flaky{Store: store}
	m, ctx := started(t, f)
	spec := clusters.Spec{ID: "prod", Name: "prod", Env: "production", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}
	tok := clusters.Credential{Token: saToken("prod")}
	for name, err := range map[string]error{
		"a statement timeout":  fmt.Errorf("reading the connection prod: %w", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}),
		"a lock not granted":   fmt.Errorf("reading the connection prod: %w", &pgconn.PgError{Code: "55P03", Message: "could not obtain lock"}),
		"a Secret's 403":       fmt.Errorf("reading the connection Secret: %w", apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "b221-cluster-prod", errors.New("webhook"))),
		"an internal error":    fmt.Errorf("reading the connection prod: %w", &pgconn.PgError{Code: "XX000", Message: "internal"}),
		"too many connections": fmt.Errorf("reading the connection prod: %w", &pgconn.PgError{Code: "53300", Message: "too many connections"}),
	} {
		spec.Description = name
		must(t, store.Save(ctx, spec, &tok))
		f.set(nil, err, nil)
		_ = m.Sync(ctx)
		f.set(nil, nil, nil)
		_ = m.Sync(ctx)
		if s, ok := m.Summary("prod"); !ok || s.Description != name {
			t.Errorf("after %s: the connection is not read again (%v %q)", name, ok, s.Description)
		}
	}
}

// Finding 8: edits of the local cluster were never held to the version
// they read (DB-92): its summary carried no version, so a stale editor
// saved over a newer save rather than being told.
func TestALocalEditIsHeldToTheVersionItRead(t *testing.T) {
	// The stores that keep versions: the Secrets alone keep none (0
	// holds to nothing).
	for name, store := range map[string]func() clusters.Store{
		"memory":   func() clusters.Store { return clusters.NewMemoryStore() },
		"database": func() clusters.Store { s, _, _ := clusterstest.PG(t, nil); return s },
	} {
		t.Run(name, func(t *testing.T) { testALocalEditIsHeld(t, store()) })
	}
}

func testALocalEditIsHeld(t *testing.T, store clusters.Store) {
	{
		ctx := context.Background()
		must(t, store.Save(ctx, clusters.Spec{ID: clusters.LocalID, Name: "here", Env: "staging", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster", Mode: "read", Local: true, Added: at}, nil))
		m := clusters.New(store, quiet)
		must(t, m.SetLocal(ctx, clusters.Spec{Name: "install", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster"}, nil, nil))
		_ = m.Start(ctx)
		defer m.Stop()
		opened, _ := m.Summary(clusters.LocalID)
		if opened.EditVersion == 0 {
			t.Fatal("the local cluster's summary carries no version")
		}
		// Two tabs open the editor; the first renames it.
		first := clusters.Draft{Spec: clusters.Spec{Name: "Lakehouse", Env: "staging", Mode: "read"}, EditVersion: opened.EditVersion}
		sum, err := m.Update(ctx, clusters.LocalID, first)
		must(t, err)
		if sum.EditVersion == opened.EditVersion || sum.EditVersion == 0 {
			t.Errorf("the version after the save: %d, was %d", sum.EditVersion, opened.EditVersion)
		}
		// The second saves a description from its stale form.
		stale := clusters.Draft{Spec: clusters.Spec{Name: "here", Env: "staging", Description: "stale", Mode: "read"}, EditVersion: opened.EditVersion}
		if err := m.Held(ctx, clusters.LocalID, stale.EditVersion); !errors.Is(err, clusters.ErrConflict) {
			t.Errorf("Held of a stale local edit: %v", err)
		}
		if _, err := m.Update(ctx, clusters.LocalID, stale); !errors.Is(err, clusters.ErrConflict) {
			t.Fatalf("a stale local edit: %v", err)
		}
		if got, _, _ := store.Get(ctx, clusters.LocalID); got.Name != "Lakehouse" {
			t.Fatalf("the stale edit saved over the newer one: %q", got.Name)
		}
		// An edit of the version it read goes through.
		now, _ := m.Summary(clusters.LocalID)
		_, err = m.Update(ctx, clusters.LocalID, clusters.Draft{Spec: clusters.Spec{Name: "Lakehouse", Env: "staging", Description: "fresh", Mode: "read"}, EditVersion: now.EditVersion})
		must(t, err)
	}
}

// heldSim is a simulator that holds the test's question of who it signs
// in as until released, and says when it is asked.
func heldSim(t *testing.T) (srv *httptest.Server, asked chan struct{}, release func()) {
	t.Helper()
	sm := sim.New(sim.Options{Nodes: 3, Pods: 20, Seed: 7})
	asked = make(chan struct{}, 1)
	held := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "selfsubjectreviews") {
			select {
			case asked <- struct{}{}:
			default:
			}
			<-held
		}
		sm.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	var once sync.Once
	release = func() { once.Do(func() { close(held) }) }
	t.Cleanup(release)
	return srv, asked, release
}

// Finding 11: a test still running when its connection was removed and
// made again (or re-pointed) was kept as the new connection's last test,
// timed after the change, where one made again starts untested (DB-64).
func TestATestRunningAcrossARecreationIsNotTheNewConnections(t *testing.T) {
	eachStore(t, func(t *testing.T, store clusters.Store) {
		slow, asked, release := heldSim(t)
		_, other := simServer(t)
		m, ctx := started(t, store)
		draft := func(server string) clusters.Draft {
			return clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "staging", Server: server, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: saToken("staging")}}
		}
		_, err := m.Add(ctx, draft(slow.URL))
		must(t, err)
		done := make(chan error, 1)
		go func() {
			_, err := m.TestSaved(ctx, "staging", "ann")
			done <- err
		}()
		select {
		case <-asked:
		case <-time.After(10 * time.Second):
			t.Fatal("the test never asked who it signs in as")
		}
		// Meanwhile staging is removed and made again, for another server.
		must(t, m.Remove(ctx, "staging"))
		_, err = m.Add(ctx, draft(other.URL))
		must(t, err)
		release()
		must(t, <-done)
		if s, _ := m.Summary("staging"); s.LastTest != nil {
			t.Errorf("the connection made again shows the old one's test: %s", s.LastTest.Summary)
		}
		if spec, _, err := store.Get(ctx, "staging"); err != nil || spec.Tested != nil {
			t.Errorf("the store keeps the old connection's test for the new one: %v", err)
		}
		// A test of the connection as it is now is kept.
		_, err = m.TestSaved(ctx, "staging", "ann")
		must(t, err)
		if s, _ := m.Summary("staging"); s.LastTest == nil {
			t.Error("a test of the connection as it is was not kept")
		}
	})
}

// Finding 12: once a connection's Secret was made again by hand without
// its labels, its revision no longer followed it (the Secrets are listed
// by label), so later rotations never reached the running connection, and
// the start said the connection had no Secret.
func TestASecretMadeAgainByHandWithoutItsLabelsIsFollowed(t *testing.T) {
	store, _, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	spec, cred := fullSpec("prod"), fullCred("first")
	must(t, store.Save(ctx, spec, &cred))
	secrets := cs.CoreV1().Secrets(clusterstest.Namespace)
	byHand := func(tok string) {
		t.Helper()
		_ = secrets.Delete(ctx, clusterstest.SecretName("prod"), metav1.DeleteOptions{})
		_, err := secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: clusterstest.SecretName("prod"), Namespace: clusterstest.Namespace}, Data: map[string][]byte{"token": []byte(tok)}}, metav1.CreateOptions{})
		must(t, err)
	}
	byHand("second-token")
	first, err := store.Revisions(ctx)
	must(t, err)
	// Rotated again, the way it was replaced.
	byHand("third-token")
	second, err := store.Revisions(ctx)
	must(t, err)
	if first["prod"] == second["prod"] {
		t.Errorf("a rotation in a Secret without its labels left the revision at %q", first["prod"])
	}
	if _, cred, _ := store.Get(ctx, "prod"); cred.Token != "third-token" {
		t.Fatal("the Secret made by hand is not read")
	}
	if noSecret, _, err := store.Unmatched(ctx); err != nil || len(noSecret) != 0 {
		t.Errorf("a connection whose Secret is there, without its labels, is said to have none: %v %v", noSecret, err)
	}
}

// Finding 13: every rename, description, mode or environment edit wrote
// the Secret again from the credential read at its start: a key this
// console does not know went, and a kept row with no Secret was given an
// empty one, which silenced the start's warning.
func TestAnEditWithoutACredentialLeavesTheSecretAlone(t *testing.T) {
	_, srv := simServer(t)
	store, _, cs := clusterstest.PG(t, nil)
	m, ctx := started(t, store)
	spec := clusters.Spec{Name: "prod", Env: "production", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}
	_, err := m.Add(ctx, clusters.Draft{Spec: spec, Credential: clusters.Credential{Token: saToken("prod")}})
	must(t, err)
	secrets := cs.CoreV1().Secrets(clusterstest.Namespace)
	sec, err := secrets.Get(ctx, clusterstest.SecretName("prod"), metav1.GetOptions{})
	must(t, err)
	sec.Data["added-by-hand"] = []byte("kept")
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	must(t, err)
	before, _ := secrets.Get(ctx, clusterstest.SecretName("prod"), metav1.GetOptions{})
	renamed := spec
	renamed.Description, renamed.Mode = "renamed", "write"
	_, err = m.Update(ctx, "prod", clusters.Draft{Spec: renamed})
	must(t, err)
	after, _ := secrets.Get(ctx, clusterstest.SecretName("prod"), metav1.GetOptions{})
	if string(after.Data["added-by-hand"]) != "kept" || after.ResourceVersion != before.ResourceVersion {
		t.Errorf("an edit without a credential wrote the Secret: keys %v", keysOf(held(t, cs)[clusterstest.SecretName("prod")]))
	}
	// A kept row whose Secret is gone is not given an empty one.
	must(t, secrets.Delete(ctx, clusterstest.SecretName("prod"), metav1.DeleteOptions{}))
	renamed.Description = "again"
	_, _ = m.Update(ctx, "prod", clusters.Draft{Spec: renamed})
	if _, err := secrets.Get(ctx, clusterstest.SecretName("prod"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("an edit without a credential made a Secret: %v", err)
	}
}

// Finding 14: a Kubernetes failure while saving or removing a connection
// was reported as a database outage: the Secret's write ran inside the
// transaction, whose error a "connection reset" turned into
// db.ErrUnavailable.
func TestASecretsFailureIsNotADatabaseOutage(t *testing.T) {
	store, d, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	spec, cred := fullSpec("staging"), fullCred("first")
	must(t, store.Save(ctx, spec, &cred))
	reset := error(errors.New("Put \"https://10.43.0.1:443/api/v1/namespaces/console/secrets/b221-cluster-staging\": read tcp 10.42.0.5:40000->10.43.0.1:443: read: connection reset by peer"))
	for _, verb := range []string{"create", "update", "delete", "list"} {
		failing(cs, &reset, verb)
	}
	next := fullCred("second")
	check := func(what string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, db.ErrUnavailable) || !strings.Contains(err.Error(), "connection reset") {
			t.Errorf("%s: %v", what, err)
		}
	}
	check("a save", store.Save(ctx, spec, &next))
	check("a removal", store.Delete(ctx, "staging"))
	_, err := clusters.CopyFromSecrets(ctx, d, clusterstest.Secrets(cs), nil)
	check("the copy", err)
	if h := d.Health(); !h.Available {
		t.Errorf("the database is taken for away: %+v", h)
	}
}

// Finding 15: on a read-and-change connection whose account could not
// make Watson's changes, the permissions step said only that, and hid
// the warning that nobody would see anything until it may impersonate.
func TestThePermissionsStepSaysEverythingItFound(t *testing.T) {
	_, srv := simServer(t)
	admin(t, srv, "/sim/deny", `{"verbs":["impersonate","patch"]}`)
	spec := clusters.Spec{Name: "prod", Env: "production", Server: srv.URL, CA: "system", Auth: "token", Mode: "write"}
	r := clusters.Test(context.Background(), spec, clusters.Credential{Token: saToken("prod")}, clusters.TestOptions{SecretsRead: true, Timeout: 5 * time.Second})
	p := r.Steps[len(r.Steps)-1]
	if p.Key != "Permissions" || p.Tone != "warn" || !strings.Contains(p.Text, "cannot impersonate") || !strings.Contains(p.Text, "Watson") {
		t.Fatalf("the permissions step: %+v", p)
	}
}

// Cut list: getSpec and testLocal read an instance's spec without the
// lock Update writes it under (a data race, which -race reports).
func TestReadingTheLocalSpecWhileItIsEditedIsNoRace(t *testing.T) {
	ctx := context.Background()
	m := clusters.New(clusters.NewMemoryStore(), quiet)
	must(t, m.SetLocal(ctx, clusters.Spec{Name: "here", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster"}, nil, nil))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_, _ = m.Update(ctx, clusters.LocalID, clusters.Draft{Spec: clusters.Spec{Name: "here", Env: "other", Description: fmt.Sprint(i), Mode: "read"}})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = m.Held(ctx, clusters.LocalID, 1)
			_, _ = m.TestSaved(ctx, clusters.LocalID, "ann")
		}
	}()
	wg.Wait()
}

// whoSim is a simulator whose answer to "who am I" is given: a user name,
// or "" for a refusal (403).
func whoSim(t *testing.T, who string) *httptest.Server {
	t.Helper()
	sm := sim.New(sim.Options{Nodes: 3, Pods: 20, Seed: 7})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "selfsubjectreviews") {
			sm.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if who == "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1","status":{"userInfo":{"username":"` + who + `"}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Cut list: the sign-in step said "Signed in as <user>" for a kubeconfig
// that signs in as nobody: the API server's anonymous user, or, when it
// would not say, the name of the kubeconfig's user entry.
func TestTheSignInStepDoesNotNameWhoNobodyIs(t *testing.T) {
	signIn := func(srv *httptest.Server) clusters.Step {
		t.Helper()
		kc := `apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "` + srv.URL + `"}}]
users: [{name: admin, user: {}}]
contexts: [{name: c, context: {cluster: c, user: admin}}]
current-context: c
`
		check, server, err := clusters.CheckCredential("kubeconfig", clusters.Credential{Kubeconfig: kc}, "", time.Now())
		must(t, err)
		spec := clusters.Spec{Name: "c", Env: "development", Server: server, CA: "system", Auth: "kubeconfig", Mode: "read", Cred: check.Info}
		r := clusters.Test(context.Background(), spec, clusters.Credential{Kubeconfig: kc}, clusters.TestOptions{Timeout: 5 * time.Second})
		for _, st := range r.Steps {
			if st.Key == "Sign-in" {
				return st
			}
		}
		t.Fatalf("no sign-in step: %s", steps(r))
		return clusters.Step{}
	}
	if st := signIn(whoSim(t, "system:anonymous")); st.Tone != "warn" || !strings.Contains(st.Text, "anonymous") || strings.Contains(st.Text, "Signed in as") {
		t.Errorf("an anonymous sign-in: %+v", st)
	}
	if st := signIn(whoSim(t, "")); strings.Contains(st.Text, "admin") {
		t.Errorf("a sign-in the server would not name: %+v", st)
	}
}

// Cut list: a connection started again (re-pointed, given another
// credential, changed elsewhere) had a new store with no hosts outside
// Kubernetes until the next read of them, up to 30 seconds on.
func TestARestartedConnectionKeepsItsHosts(t *testing.T) {
	_, srv := simServer(t)
	m, ctx := started(t, clusters.NewMemoryStore())
	spec := clusters.Spec{Name: "edge", Env: "development", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}
	_, err := m.Add(ctx, clusters.Draft{Spec: spec, Credential: clusters.Credential{Token: saToken("edge")}})
	must(t, err)
	m.SetHosts("edge", []nodes.Row{{Name: "edge-host-1"}})
	has := func(what string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			st, _, _ := m.Lookup("edge")
			if st != nil && strings.Contains(string(st.Snapshot([]string{"nodes"}, nil)), "edge-host-1") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the host outside Kubernetes is not shown", what)
			}
		}
	}
	has("at first")
	// A new credential starts the connection again.
	_, err = m.Update(ctx, "edge", clusters.Draft{Spec: spec, Credential: clusters.Credential{Token: saToken("again")}})
	must(t, err)
	has("after a restart")
}

// Cut list: a connection's last test was read in two statements (its
// row's columns, then its steps) outside one snapshot, so a read made
// while another replica kept a test could show one test's summary over
// the other's steps.
func TestALastTestIsReadWhole(t *testing.T) {
	store, _, _ := clusterstest.PG(t, nil)
	ctx := context.Background()
	spec, cred := fullSpec("staging"), fullCred("x")
	must(t, store.Save(ctx, spec, &cred))
	test := func(name string, n int) clusters.LastTest {
		r := clusters.TestResult{Tone: "ok", Summary: name}
		for i := 0; i < n; i++ {
			r.Steps = append(r.Steps, clusters.Step{Key: "Address", Tone: "ok", Text: name})
		}
		return clusters.LastTest{Result: r, At: at, By: "ann"}
	}
	a, b := test("a", 5), test("b", 2)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			next := a
			if i%2 == 1 {
				next = b
			}
			_ = store.SaveTest(ctx, "staging", next)
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for reads := 0; time.Now().Before(deadline); reads++ {
		got, _, err := store.Get(ctx, "staging")
		if err != nil || got.Tested == nil {
			continue
		}
		r := got.Tested.Result
		want := map[string]int{"a": 5, "b": 2}[r.Summary]
		if len(r.Steps) != want {
			t.Fatalf("read %d: the summary of test %q over %d steps", reads, r.Summary, len(r.Steps))
		}
		for _, st := range r.Steps {
			if st.Text != r.Summary {
				t.Fatalf("read %d: the summary of test %q over a step of test %q", reads, r.Summary, st.Text)
			}
		}
	}
}

// stuck is a store whose reads of one connection never answer until
// their context ends, as a database holding a lock does.
type stuck struct {
	clusters.Store
	id string
}

func (s *stuck) Get(ctx context.Context, id string) (clusters.Spec, clusters.Credential, error) {
	if id == s.id {
		<-ctx.Done()
		return clusters.Spec{}, clusters.Credential{}, ctx.Err()
	}
	return s.Store.Get(ctx, id)
}

// Cut list: a sync held the lock every edit takes across its reads of
// the store, with no deadline of its own, so an edit could wait as long
// as a read that did not answer.
func TestASyncThatCannotReadDoesNotHoldEditsUp(t *testing.T) {
	inner := clusters.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := quiet
	opts.ReadTimeout = 300 * time.Millisecond
	m := clusters.New(&stuck{Store: inner, id: "slow"}, opts)
	must(t, m.Start(ctx))
	defer m.Stop()
	must(t, inner.Save(ctx, clusters.Spec{ID: "slow", Name: "slow", Env: "development", Server: "https://10.9.0.1:6443", CA: "system", Auth: "token", Mode: "read"}, &clusters.Credential{Token: saToken("slow")}))
	go func() { _ = m.Sync(ctx) }()
	time.Sleep(100 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "quick", Env: "development", Server: "https://10.9.0.2:6443", CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: saToken("quick")}})
		done <- err
	}()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an edit waited for a sync whose read never answered")
	}
}

// late is a store whose list of revisions can be one taken a moment ago.
type late struct {
	clusters.Store
	mu   sync.Mutex
	revs map[string]string
}

func (l *late) Revisions(ctx context.Context) (map[string]string, error) {
	l.mu.Lock()
	revs := l.revs
	l.mu.Unlock()
	if revs != nil {
		return revs, nil
	}
	return l.Store.Revisions(ctx)
}

// Cut list: a removal left the connection marked as seen, so a sync whose
// list was taken before the connection was made again stopped it as
// removed elsewhere.
func TestAConnectionMadeAgainIsNotStoppedByAnOlderList(t *testing.T) {
	_, srv := simServer(t)
	l := &late{Store: clusters.NewMemoryStore()}
	m, ctx := started(t, l)
	draft := clusters.Draft{Spec: clusters.Spec{Name: "quick", Env: "development", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: saToken("quick")}}
	_, err := m.Add(ctx, draft)
	must(t, err)
	must(t, m.Sync(ctx))
	must(t, m.Remove(ctx, "quick"))
	// A list taken now, before it is made again.
	before, err := l.Store.Revisions(ctx)
	must(t, err)
	_, err = m.Add(ctx, draft)
	must(t, err)
	l.mu.Lock()
	l.revs = before
	l.mu.Unlock()
	must(t, m.Sync(ctx))
	if _, ok := m.Get("quick"); !ok {
		t.Fatal("the connection made again was stopped by a list taken before its save")
	}
}

// Nit: an edit that sent only a kubeconfig's context was accepted and the
// context ignored: the credential was "empty" without its kubeconfig.
func TestAContextAloneIsNotTakenForAnEdit(t *testing.T) {
	m, ctx := started(t, clusters.NewMemoryStore())
	spec := clusters.Spec{Name: "kc", Env: "development", Server: "https://10.9.0.1:6443", CA: "system", Auth: "kubeconfig", Mode: "read"}
	_, err := m.Add(ctx, clusters.Draft{Spec: spec, Credential: clusters.Credential{Kubeconfig: kubeconfigFor(spec.Server, saToken("kc"))}})
	must(t, err)
	var v *clusters.ValidationError
	if _, err := m.Update(ctx, "kc", clusters.Draft{Spec: spec, Credential: clusters.Credential{Context: "elsewhere"}}); !errors.As(err, &v) || v.Field != "context" {
		t.Fatalf("an edit of the context alone: %v", err)
	}
	// The context it has already is no change.
	if _, err := m.Update(ctx, "kc", clusters.Draft{Spec: spec, Credential: clusters.Credential{Context: "here"}}); err != nil {
		t.Fatalf("the same context: %v", err)
	}
}

// Nit: a sync's "could not be read" error joined its connections' errors
// in map order, so its text changed from one sync to the next and the
// warning that is said once was said again and again.
func TestASyncSaysTheSameErrorTheSameWay(t *testing.T) {
	store := clusters.NewMemoryStore()
	ctx := context.Background()
	for _, id := range []string{"alpha", "beta", "gamma"} {
		must(t, store.Save(ctx, clusters.Spec{ID: id, Name: id, Env: "development", Server: "https://10.9.0.1:6443", CA: "system", Auth: "token", Mode: "read"}, &clusters.Credential{Token: saToken(id)}))
	}
	f := &flaky{Store: store}
	f.set(nil, fmt.Errorf("reading: %w", db.ErrUnavailable), nil)
	m := clusters.New(f, quiet)
	first := ""
	for i := 0; i < 30; i++ {
		err := m.Sync(ctx)
		if err == nil {
			t.Fatal("a sync whose connections cannot be read said nothing")
		}
		if i == 0 {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("sync %d said it otherwise:\n%s\nthen\n%s", i, first, err)
		}
	}
}

// Sweep: a connection restored from a backup without its secret was tested
// as if the placeholder were its token, and the placeholder sent.
func TestARestoredPlaceholderIsNotSentAsACredential(t *testing.T) {
	l := listen(t)
	m, ctx := started(t, clusters.NewMemoryStore())
	spec := clusters.Spec{ID: "restored", Name: "restored", Env: "development", Server: l.srv.URL, CA: "system", Auth: "token", Mode: "read", Added: at}
	if _, err := m.Restore(ctx, spec, clusters.Credential{Token: clusters.Placeholder}); err != nil {
		t.Fatal(err)
	}
	r, err := m.TestSaved(ctx, "restored", "ann")
	must(t, err)
	if r.Tone != "err" || !strings.Contains(r.Error, "backup") {
		t.Errorf("the test of a restored placeholder: %s %q", steps(r), r.Error)
	}
	if got := l.sent(); len(got) > 0 {
		t.Fatalf("the placeholder was sent as a credential: %d requests", len(got))
	}
}
