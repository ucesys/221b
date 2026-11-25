package clusters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/clusters/clusterstest"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube/kubetest"
)

// eachStore runs a test over every store a console keeps its connections
// in: memory, the Secrets alone, and the database with the credentials in
// the Secrets (DB-64), which is skipped when TEST_DATABASE names none.
func eachStore(t *testing.T, test func(t *testing.T, store clusters.Store)) {
	t.Run("memory", func(t *testing.T) { test(t, clusters.NewMemoryStore()) })
	t.Run("secrets", func(t *testing.T) { test(t, clusterstest.Secrets(kubetest.Cluster())) })
	t.Run("database", func(t *testing.T) {
		s, _, _ := clusterstest.PG(t, nil)
		test(t, s)
	})
}

// at is a time of whole seconds: the database keeps microseconds.
var at = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// fullSpec is a connection with every field set.
func fullSpec(id string) clusters.Spec {
	expires := at.Add(48 * time.Hour)
	return clusters.Spec{ID: id, Name: "The " + id + " cluster", Env: "staging", Description: "what " + id + " is for", Server: "https://10.21.0.10:6443", CA: "pem", CAInfo: "CN=kubernetes, until 2030-01-01",
		Auth: "token", Mode: "write", Cred: clusters.CredInfo{Who: "system:serviceaccount:kube-system:console", Context: "a-context", Cluster: "a-cluster", Issuer: "https://issuer.example.com", ClientID: "console", Expires: &expires, Set: at},
		Added: at, Updated: at.Add(time.Hour)}
}

// fullCred is a credential with every field set, each to tag and its name.
func fullCred(tag string) clusters.Credential {
	return clusters.Credential{Token: tag + "-token", Kubeconfig: tag + "-kubeconfig", Context: tag + "-context", CertPEM: tag + "-cert", KeyPEM: tag + "-key", CAPEM: tag + "-ca",
		OIDCIssuer: tag + "-issuer", OIDCClientID: tag + "-client", OIDCClientSecret: tag + "-clientsecret", OIDCRefreshToken: tag + "-refresh", OIDCScopes: tag + "-scopes"}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// What every store does (P3-07, P3-08, P7-02, DB-64): a connection is
// read as it was saved, field for field, with its credential; a save
// without a credential keeps the saved one, and one with a credential
// replaces it whole; every save changes the revision; the local cluster
// is kept with no credential; and a removed connection is gone, with its
// credential, and never has a revision again that it had before.
func TestStoresKeepConnections(t *testing.T) { eachStore(t, testStoresKeepConnections) }

func testStoresKeepConnections(t *testing.T, store clusters.Store) {
	ctx := context.Background()
	if list, err := store.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("an empty store lists %v: %v", list, err)
	}
	if revs, err := store.Revisions(ctx); err != nil || len(revs) != 0 {
		t.Fatalf("an empty store has revisions %v: %v", revs, err)
	}
	if _, _, err := store.Get(ctx, "staging"); !errors.Is(err, clusters.ErrNotFound) {
		t.Fatalf("a connection that is not there: %v", err)
	}
	spec, cred := fullSpec("staging"), fullCred("first")
	must(t, store.Save(ctx, spec, &cred))
	got, gotCred, err := store.Get(ctx, "staging")
	if err != nil || asJSON(got) != asJSON(spec) || gotCred != cred {
		t.Fatalf("read back: %v\n%s\n%s\n%+v", err, asJSON(got), asJSON(spec), gotCred)
	}
	seen := map[string]bool{}
	revision := func() string {
		t.Helper()
		revs, err := store.Revisions(ctx)
		if err != nil || revs["staging"] == "" {
			t.Fatalf("revisions %v: %v", revs, err)
		}
		if seen[revs["staging"]] {
			t.Fatalf("the revision %q is one the connection had before", revs["staging"])
		}
		seen[revs["staging"]] = true
		return revs["staging"]
	}
	revision()

	// Without a credential the saved one stays; every field may be emptied.
	spec.Description, spec.CAInfo, spec.Cred, spec.Updated = "", "", clusters.CredInfo{}, at.Add(2*time.Hour)
	must(t, store.Save(ctx, spec, nil))
	got, gotCred, err = store.Get(ctx, "staging")
	if err != nil || asJSON(got) != asJSON(spec) || gotCred != cred {
		t.Fatalf("after a save without a credential: %v\n%s\n%+v", err, asJSON(got), gotCred)
	}
	revision()

	// A credential given replaces the saved one whole.
	next := clusters.Credential{Token: "second-token"}
	must(t, store.Save(ctx, spec, &next))
	if _, gotCred, err = store.Get(ctx, "staging"); err != nil || gotCred != next {
		t.Fatalf("after a new credential: %v %+v", err, gotCred)
	}
	revision()

	// The local cluster's name is kept with no credential, and listed first.
	local := clusters.Spec{ID: clusters.LocalID, Name: "Zebra", Env: "production", Description: "the console's own", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster", Mode: "read", Local: true, Added: at, Updated: at}
	must(t, store.Save(ctx, local, nil))
	if got, gotCred, err := store.Get(ctx, clusters.LocalID); err != nil || asJSON(got) != asJSON(local) || gotCred != (clusters.Credential{}) {
		t.Fatalf("the local cluster: %v %s %+v", err, asJSON(got), gotCred)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != clusters.LocalID || list[1].ID != "staging" {
		t.Fatalf("the list: %v %+v", err, list)
	}
	if revs, err := store.Revisions(ctx); err != nil || len(revs) != 2 || revs[clusters.LocalID] == "" {
		t.Fatalf("revisions with the local cluster: %v %v", revs, err)
	}

	// Removed: gone, with its credential, and not there to remove twice.
	must(t, store.Delete(ctx, "staging"))
	if _, _, err := store.Get(ctx, "staging"); !errors.Is(err, clusters.ErrNotFound) {
		t.Fatalf("after its removal: %v", err)
	}
	if err := store.Delete(ctx, "staging"); !errors.Is(err, clusters.ErrNotFound) {
		t.Fatalf("removed twice: %v", err)
	}
	if revs, err := store.Revisions(ctx); err != nil || len(revs) != 1 {
		t.Fatalf("revisions after the removal: %v %v", revs, err)
	}
	if list, err := store.List(ctx); err != nil || len(list) != 1 {
		t.Fatalf("the list after the removal: %v %+v", err, list)
	}
	// Made again under the same id: a new connection, with none of the
	// old one's fields, and a revision it never had.
	again := clusters.Spec{ID: "staging", Name: "Staging again", Env: "development", Server: "https://10.21.0.11:6443", CA: "system", Auth: "token", Mode: "read", Added: at.Add(24 * time.Hour)}
	must(t, store.Save(ctx, again, &next))
	if got, _, err := store.Get(ctx, "staging"); err != nil || asJSON(got) != asJSON(again) {
		t.Fatalf("made again: %v %s", err, asJSON(got))
	}
	revision()
}

// flaky is a store that can be told to fail, as a database or an API
// server that is away does, and counts what it is asked.
type flaky struct {
	clusters.Store
	mu                     sync.Mutex
	revisions, get, remove error
	gets                   map[string]int
}

func (f *flaky) set(revisions, get, remove error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revisions, f.get, f.remove = revisions, get, remove
}

func (f *flaky) asked(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets[id]
}

func (f *flaky) Revisions(ctx context.Context) (map[string]string, error) {
	f.mu.Lock()
	err := f.revisions
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.Store.Revisions(ctx)
}

func (f *flaky) Get(ctx context.Context, id string) (clusters.Spec, clusters.Credential, error) {
	f.mu.Lock()
	if f.gets == nil {
		f.gets = map[string]int{}
	}
	f.gets[id]++
	err := f.get
	f.mu.Unlock()
	if err != nil {
		return clusters.Spec{}, clusters.Credential{}, err
	}
	return f.Store.Get(ctx, id)
}

func (f *flaky) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	err := f.remove
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.Store.Delete(ctx, id)
}

func saToken(name string) string {
	return token(map[string]any{"sub": "system:serviceaccount:kube-system:" + name, "exp": time.Now().Add(24 * time.Hour).Unix()})
}

var quiet = clusters.Options{Refresh: time.Second, HealthInterval: time.Second, SecretsRead: true, Logger: slog.New(slog.DiscardHandler)}

// A store that cannot be asked changes nothing in a console that runs
// (the design's risk 10, DB-64): while the list cannot be read, and while
// one connection of it cannot, every connection keeps running as it was,
// the sync says so, and the connection is read again at the next sync,
// where it used to be marked as seen and left until someone changed it. A
// connection that was read and cannot be used is still said once and not
// read again. And a removal the store refuses leaves the connection
// running and listed, to be removed again.
func TestSyncKeepsWhatRunsWhileTheStoreCannotBeAsked(t *testing.T) {
	eachStore(t, testSyncKeepsWhatRunsWhileTheStoreCannotBeAsked)
}

func testSyncKeepsWhatRunsWhileTheStoreCannotBeAsked(t *testing.T, store clusters.Store) {
	_, srv := simServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	away := fmt.Errorf("reading the connections: %w", db.ErrUnavailable)
	f := &flaky{Store: store}
	a, b := clusters.New(f, quiet), clusters.New(store, quiet)
	local := clusters.Spec{Name: "here", Env: "other", Server: srv.URL, CA: "system", Auth: "incluster", Mode: "read"}
	for _, m := range []*clusters.Manager{a, b} {
		must(t, m.SetLocal(ctx, local, nil, nil))
		must(t, m.Start(ctx))
	}
	draft := func(name, env string) clusters.Draft {
		return clusters.Draft{Spec: clusters.Spec{Name: name, Env: env, Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}}
	}
	first := draft("staging", "staging")
	first.Credential = clusters.Credential{Token: saToken("console")}
	_, err := a.Add(ctx, first)
	must(t, err)
	must(t, a.Sync(ctx))
	must(t, b.Sync(ctx))
	running, _ := a.Get("staging")
	same := func(when string) {
		t.Helper()
		if now, ok := a.Get("staging"); !ok || now != running {
			t.Fatalf("%s: the running connection is gone or was started again", when)
		}
		if n := len(a.List()); n != 2 {
			t.Fatalf("%s: %d connections are listed", when, n)
		}
		if s, _ := a.Summary("staging"); s.Env != "staging" {
			t.Fatalf("%s: the connection is %+v", when, s.Spec)
		}
	}

	// The whole list cannot be read.
	f.set(away, nil, nil)
	if err := a.Sync(ctx); !errors.Is(err, db.ErrUnavailable) {
		t.Fatalf("a sync over a store that is away: %v", err)
	}
	same("the list cannot be read")

	// Another replica changes the connection and adds one, and then the
	// connections cannot be read one by one.
	_, err = b.Update(ctx, "staging", draft("staging", "production"))
	must(t, err)
	second := draft("second", "development")
	second.Credential = clusters.Credential{Token: saToken("second")}
	_, err = b.Add(ctx, second)
	must(t, err)
	f.set(nil, away, nil)
	for i := 0; i < 2; i++ {
		before := f.asked("staging")
		if err := a.Sync(ctx); !errors.Is(err, db.ErrUnavailable) || !strings.Contains(err.Error(), "staging") {
			t.Fatalf("a sync whose connections cannot be read: %v", err)
		}
		if f.asked("staging") != before+1 {
			t.Fatalf("sync %d did not ask for the connection again", i)
		}
		same("a connection cannot be read")
		if _, ok := a.Get("second"); ok {
			t.Fatal("a connection that could not be read was started")
		}
	}
	// The store answers again: what the other replica did is here.
	f.set(nil, nil, nil)
	must(t, a.Sync(ctx))
	if s, _ := a.Summary("staging"); s.Env != "production" {
		t.Fatalf("the change made meanwhile was never read: %+v", s.Spec)
	}
	if now, _ := a.Get("staging"); now != running {
		t.Error("a change of environment restarted the watches")
	}
	if _, ok := a.Get("second"); !ok {
		t.Fatal("the connection added meanwhile was never started")
	}

	// A connection the store answered with something this console cannot
	// use is said once: not read again until it changes.
	_, err = b.Update(ctx, "staging", draft("staging", "development"))
	must(t, err)
	f.set(nil, fmt.Errorf("reading the connection Secret: %w: the connection was saved by a newer console (format 9)", clusters.ErrUnreadable), nil)
	must(t, a.Sync(ctx))
	asked := f.asked("staging")
	must(t, a.Sync(ctx))
	if f.asked("staging") != asked {
		t.Error("a connection that cannot be used is read at every sync")
	}
	if now, ok := a.Get("staging"); !ok || now != running {
		t.Fatal("a connection that cannot be used was stopped")
	}

	// A removal the store refuses leaves it running, and can be asked again.
	f.set(nil, nil, away)
	if err := a.Remove(ctx, "staging"); !errors.Is(err, db.ErrUnavailable) {
		t.Fatalf("a removal over a store that is away: %v", err)
	}
	if now, ok := a.Get("staging"); !ok || now != running {
		t.Fatal("a connection that could not be removed is not running any more")
	}
	f.set(nil, nil, nil)
	must(t, a.Remove(ctx, "staging"))
	if _, ok := a.Get("staging"); ok {
		t.Fatal("still running after its removal")
	}
	if _, _, err := store.Get(ctx, "staging"); !errors.Is(err, clusters.ErrNotFound) {
		t.Fatalf("still saved after its removal: %v", err)
	}
}

// What "could not be asked" is: the database away, a time that ran out,
// an API server that did not answer or could not. Everything a store
// answered, a refusal included, is not.
func TestNotAsked(t *testing.T) {
	secrets := schema.GroupResource{Resource: "secrets"}
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"none":                 {nil, false},
		"the database away":    {fmt.Errorf("reading the connection: %w: %w", db.ErrUnavailable, errors.New("connection refused")), true},
		"a time that ran out":  {fmt.Errorf("reading: %w", context.DeadlineExceeded), true},
		"the API unavailable":  {fmt.Errorf("reading the Secret: %w", apierrors.NewServiceUnavailable("etcd is away")), true},
		"the API timing out":   {apierrors.NewTimeoutError("slow", 1), true},
		"the API overloaded":   {apierrors.NewTooManyRequests("later", 1), true},
		"the API failing":      {apierrors.NewInternalError(errors.New("boom")), true},
		"nothing listening":    {fmt.Errorf("reading the Secret: %w", &url.Error{Op: "Get", URL: "https://10.0.0.1:6443", Err: errors.New("connection refused")}), true},
		"no such Secret":       {apierrors.NewNotFound(secrets, "x"), false},
		"not allowed":          {apierrors.NewForbidden(secrets, "x", errors.New("no")), false},
		"written meanwhile":    {apierrors.NewConflict(secrets, "x", errors.New("modified")), false},
		"no such connection":   {clusters.ErrNotFound, false},
		"a format not known":   {errors.New("the connection was saved by a newer console (format 9)"), false},
		"a database's refusal": {errors.New("ERROR: column \"nope\" does not exist (SQLSTATE 42703)"), false},
	} {
		if got := clusters.NotAsked(c.err); got != c.want {
			t.Errorf("%s: could not be asked %v, want %v", name, got, c.want)
		}
	}
}

// The local cluster never needs the store (DB-64): registered while the
// store cannot be read, it stands under the installation's name, the
// start does not fail, and the name saved for it is taken at the first
// sync the store answers.
func TestTheLocalClusterNeverNeedsTheStore(t *testing.T) {
	eachStore(t, func(t *testing.T, store clusters.Store) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		must(t, store.Save(ctx, clusters.Spec{ID: clusters.LocalID, Name: "Lakehouse", Env: "production", Description: "saved", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster", Mode: "read", Local: true, Added: at, Updated: at}, nil))
		away := fmt.Errorf("reading the connection: %w", db.ErrUnavailable)
		f := &flaky{Store: store}
		f.set(away, away, nil)
		m := clusters.New(f, quiet)
		must(t, m.SetLocal(ctx, clusters.Spec{Name: "here", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster"}, nil, nil))
		must(t, m.Start(ctx))
		if s, ok := m.Summary(clusters.LocalID); !ok || s.Name != "here" || s.Env != "other" || !s.Local {
			t.Fatalf("the local cluster over a store that is away: %+v", s.Spec)
		}
		f.set(nil, nil, nil)
		must(t, m.Sync(ctx))
		if s, _ := m.Summary(clusters.LocalID); s.Name != "Lakehouse" || s.Env != "production" || s.Description != "saved" || !s.Added.Equal(at) {
			t.Fatalf("the local cluster once the store answers: %+v", s.Spec)
		}
	})
}

// A backup's connections over every store (ADR-137): the credential goes
// out sealed, and comes back as it was; one that comes back without its
// secret is listed, and says what to do.
func TestABackupSealsAndRestoresAConnection(t *testing.T) {
	eachStore(t, func(t *testing.T, store clusters.Store) {
		_, srv := simServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := clusters.New(store, quiet)
		must(t, m.Start(ctx))
		sa := saToken("console")
		sum, err := m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: sa}})
		must(t, err)
		sealed, err := m.Sealed(ctx, "staging", func(clear string) string { return "sealed:" + clear })
		if err != nil || sealed.Token != "sealed:"+sa {
			t.Fatalf("sealed: %v", err)
		}
		must(t, m.Remove(ctx, "staging"))
		back, err := m.Restore(ctx, sum.Spec, clusters.Credential{Token: strings.TrimPrefix(sealed.Token, "sealed:")})
		if err != nil || back.State == "failed" || back.Cred.Who != sum.Cred.Who {
			t.Fatalf("restored: %v %+v", err, back)
		}
		if spec, cred, err := store.Get(ctx, "staging"); err != nil || cred.Token != sa || spec.Name != "staging" || spec.Server != srv.URL {
			t.Fatalf("the restored connection as it is kept: %v %+v", err, spec)
		}
		if _, err := m.Restore(ctx, sum.Spec, clusters.Credential{Token: sa}); err == nil {
			t.Error("a connection that is here was restored over")
		}
		must(t, m.Remove(ctx, "staging"))
		back, err = m.Restore(ctx, sum.Spec, clusters.Credential{Token: clusters.Placeholder})
		if err != nil || back.State != "failed" || !strings.Contains(back.Message, "paste it again") {
			t.Fatalf("restored without its secret: %v %+v", err, back)
		}
	})
}

// --- The database's own (DB-64) ---

// row is what the database keeps of a cluster, as text.
func row(t *testing.T, d *db.DB, id string) (kept bool, version int, fields string, found bool) {
	t.Helper()
	err := d.Pool().QueryRow(context.Background(), `SELECT is_kept, version, concat_ws('|', name, environment, description, server_url, ca_mode, ca_info, auth_method, mode, is_local::text,
		cred_who, cred_context, cred_cluster, cred_issuer, cred_client_id, cred_expires_at::text, cred_set_at::text, updated_at::text) FROM clusters WHERE cluster_id = $1`, id).Scan(&kept, &version, &fields)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		t.Fatal(err)
	}
	return kept, version, fields, err == nil
}

// held is the keys of every Secret of the test's cluster, with what each holds.
func held(t *testing.T, cs *fake.Clientset) map[string]map[string]string {
	t.Helper()
	l, err := cs.CoreV1().Secrets(clusterstest.Namespace).List(context.Background(), metav1.ListOptions{})
	must(t, err)
	out := map[string]map[string]string{}
	for _, sec := range l.Items {
		out[sec.Name] = map[string]string{}
		for k, v := range sec.Data {
			out[sec.Name][k] = string(v)
		}
	}
	return out
}

func keysOf(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// A connection's row holds every field of it and its Secret holds the
// credential and nothing else: no "spec.json", the labels a connection's
// Secret always had.
func TestAConnectionIsARowAndItsSecretHoldsTheCredentialAlone(t *testing.T) {
	store, d, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	spec, cred := fullSpec("staging"), fullCred("first")
	must(t, store.Save(ctx, spec, &cred))
	kept, version, fields, found := row(t, d, "staging")
	want := "The staging cluster|staging|what staging is for|https://10.21.0.10:6443|pem|CN=kubernetes, until 2030-01-01|token|write|false|system:serviceaccount:kube-system:console|a-context|a-cluster|https://issuer.example.com|console|"
	if !found || !kept || version != 1 || !strings.HasPrefix(fields, want) {
		t.Fatalf("the row: found %v kept %v version %d\n%s", found, kept, version, fields)
	}
	sec, err := cs.CoreV1().Secrets(clusterstest.Namespace).Get(ctx, clusterstest.SecretName("staging"), metav1.GetOptions{})
	must(t, err)
	if got := slices.Sorted(maps.Keys(sec.Data)); !slices.Equal(got, []string{"ca.crt", "context", "kubeconfig", "oidc-client-id", "oidc-client-secret", "oidc-issuer", "oidc-refresh-token", "oidc-scopes", "tls.crt", "tls.key", "token"}) {
		t.Fatalf("the Secret's keys: %v", got)
	}
	if sec.Labels[clusters.LabelComponent] != clusters.ComponentValue || sec.Labels[clusters.LabelCluster] != "staging" || sec.Labels["app.kubernetes.io/instance"] != clusterstest.Prefix {
		t.Fatalf("the Secret's labels: %v", sec.Labels)
	}
	// The local cluster has a row and no Secret at all.
	must(t, store.Save(ctx, clusters.Spec{ID: clusters.LocalID, Name: "Here", Env: "other", Local: true, Added: at}, nil))
	if _, err := cs.CoreV1().Secrets(clusterstest.Namespace).Get(ctx, clusterstest.SecretName(clusters.LocalID), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the local cluster was given a Secret: %v", err)
	}
	if noSecret, noRow, err := store.Unmatched(ctx); err != nil || len(noSecret) != 0 || len(noRow) != 0 {
		t.Fatalf("the two halves do not match: %v %v %v", noSecret, noRow, err)
	}
	// A Secret a console from before the move wrote, saved over with a
	// credential: the record in it goes, since the database keeps the
	// connection; one a newer console wrote is not saved over.
	old := clusterstest.Secrets(cs)
	must(t, old.Save(ctx, fullSpec("staging"), nil))
	must(t, store.Save(ctx, spec, &cred))
	if keys := keysOf(held(t, cs)[clusterstest.SecretName("staging")]); slices.Contains(keys, "spec.json") || len(keys) != 11 {
		t.Fatalf("after a save over a Secret with a record: %v", keys)
	}
	newer := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: clusterstest.SecretName("next"), Namespace: clusterstest.Namespace, Labels: map[string]string{clusters.LabelComponent: clusters.ComponentValue}},
		Data: map[string][]byte{"spec.json": []byte(`{"version":9,"spec":{"id":"next","name":"next"}}`), "token": []byte("its-token")}}
	_, err = cs.CoreV1().Secrets(clusterstest.Namespace).Create(ctx, newer, metav1.CreateOptions{})
	must(t, err)
	if err := store.Save(ctx, fullSpec("next"), &cred); err == nil || !strings.Contains(err.Error(), "newer console") {
		t.Fatalf("saved over a newer console's Secret: %v", err)
	}
	if _, _, _, found := row(t, d, "next"); found {
		t.Error("a save whose Secret was refused left its row")
	}
	if got := held(t, cs)[clusterstest.SecretName("next")]; got["token"] != "its-token" || !strings.Contains(got["spec.json"], `"version":9`) {
		t.Errorf("the newer console's Secret changed: %v", keysOf(got))
	}
}

// failing makes the test's cluster refuse a verb on Secrets while *on.
func failing(cs *fake.Clientset, on *error, verb string) {
	cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if *on != nil {
			return true, nil, *on
		}
		return false, nil, nil
	})
}

// The two halves are written so that a failure of either leaves a
// connection whole (DB-64): a Secret that cannot be written leaves no row
// and the row of before; a Secret that cannot be deleted leaves the
// connection as it was; a removal whose Secret is gone already goes
// through.
func TestAWriteThatFailsHalfWayLeavesTheConnectionWhole(t *testing.T) {
	store, d, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	var refuse error
	for _, verb := range []string{"create", "update", "delete"} {
		failing(cs, &refuse, verb)
	}
	spec, cred := fullSpec("staging"), fullCred("first")
	refuse = apierrors.NewServiceUnavailable("the API server is away")
	if err := store.Save(ctx, spec, &cred); err == nil || !clusters.NotAsked(err) {
		t.Fatalf("a save whose Secret cannot be written: %v", err)
	}
	if _, _, _, found := row(t, d, "staging"); found {
		t.Fatal("a row was left with no Secret")
	}
	refuse = nil
	must(t, store.Save(ctx, spec, &cred))
	before, _ := store.Revisions(ctx)

	refuse = apierrors.NewServiceUnavailable("the API server is away")
	changed := spec
	changed.Server = "https://10.99.0.1:6443"
	next := fullCred("second")
	if err := store.Save(ctx, changed, &next); err == nil {
		t.Fatal("a save whose Secret cannot be written went through")
	}
	if got, gotCred, err := store.Get(ctx, "staging"); err != nil || got.Server != spec.Server || gotCred != cred {
		t.Fatalf("the connection after a save that failed: %v %+v", err, got)
	}
	if err := store.Delete(ctx, "staging"); err == nil {
		t.Fatal("a removal whose Secret cannot be deleted went through")
	}
	if got, gotCred, err := store.Get(ctx, "staging"); err != nil || asJSON(got) != asJSON(spec) || gotCred != cred {
		t.Fatalf("the connection after a removal that failed: %v", err)
	}
	if after, _ := store.Revisions(ctx); after["staging"] != before["staging"] {
		t.Errorf("writes that failed changed the revision: %q, was %q", after["staging"], before["staging"])
	}
	refuse = nil

	// The Secret went and the removal never answered: asked again, it is made.
	must(t, cs.CoreV1().Secrets(clusterstest.Namespace).Delete(ctx, clusterstest.SecretName("staging"), metav1.DeleteOptions{}))
	must(t, store.Delete(ctx, "staging"))
	if kept, _, _, found := row(t, d, "staging"); !found || kept {
		t.Fatalf("the row after the removal: found %v kept %v", found, kept)
	}
}

// A row whose Secret is not there is a connection with an empty
// credential, not an error (DB-64): it is listed, it says why it cannot
// sign in, the log says so once, and editing it with a credential makes
// it whole again.
func TestAConnectionWithoutItsSecretSaysWhyAndIsRepairedByEditing(t *testing.T) {
	_, srv := simServer(t)
	var logs lockedBuffer
	store, _, cs := clusterstest.PG(t, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := clusters.New(store, quiet)
	must(t, m.Start(ctx))
	spec := clusters.Spec{Name: "staging", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}
	_, err := m.Add(ctx, clusters.Draft{Spec: spec, Credential: clusters.Credential{Token: saToken("console")}})
	must(t, err)
	must(t, m.Sync(ctx))
	must(t, cs.CoreV1().Secrets(clusterstest.Namespace).Delete(ctx, clusterstest.SecretName("staging"), metav1.DeleteOptions{}))

	got, cred, err := store.Get(ctx, "staging")
	if err != nil || got.Name != "staging" || cred != (clusters.Credential{}) {
		t.Fatalf("a row without its Secret: %v %+v", err, got)
	}
	if noSecret, noRow, err := store.Unmatched(ctx); err != nil || !slices.Equal(noSecret, []string{"staging"}) || len(noRow) != 0 {
		t.Fatalf("the halves that do not match: %v %v %v", noSecret, noRow, err)
	}
	// This replica notices (the Secret's part of the revision is gone),
	// and a replica that starts now lists it too.
	must(t, m.Sync(ctx))
	fresh := clusters.New(clusters.NewPGStore(dbOf(store), clusterstest.Secrets(cs), nil), quiet)
	must(t, fresh.Start(ctx))
	for name, mgr := range map[string]*clusters.Manager{"the running replica": m, "a replica that starts": fresh} {
		s, ok := mgr.Summary("staging")
		if !ok || s.State != "failed" || !strings.Contains(s.Message, "has no token") {
			t.Fatalf("%s: %+v", name, s)
		}
	}
	for i := 0; i < 3; i++ {
		_, _, _ = store.Get(ctx, "staging")
	}
	if n := strings.Count(logs.String(), "has no Secret"); n != 1 {
		t.Errorf("a connection without its Secret is said %d times:\n%s", n, logs.String())
	}
	// Editing it without a credential still works (a new name), and with
	// one it signs in again.
	renamed := spec
	renamed.Name = "staging-2"
	if _, err := m.Update(ctx, "staging", clusters.Draft{Spec: renamed}); err != nil {
		t.Fatalf("renaming a connection that has no Secret: %v", err)
	}
	sum, err := m.Update(ctx, "staging", clusters.Draft{Spec: renamed, Credential: clusters.Credential{Token: saToken("again")}})
	if err != nil || sum.State == "failed" {
		t.Fatalf("giving it a credential again: %v %+v", err, sum)
	}
	if _, cred, err := store.Get(ctx, "staging"); err != nil || cred.Token == "" {
		t.Fatalf("the credential after the repair: %v", err)
	}
	must(t, fresh.Sync(ctx))
	if s, _ := fresh.Summary("staging"); s.State == "failed" || s.Name != "staging-2" {
		t.Fatalf("the other replica after the repair: %+v", s)
	}
}

// A Secret with no kept row is not a connection (DB-64): not listed, not
// started, never deleted, whatever it holds; the start's comparison names
// it. A row with only its id is not a connection either.
func TestASecretWithoutARowIsNotAConnectionAndIsNeverDeleted(t *testing.T) {
	store, d, cs := clusterstest.PG(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := clusterstest.Secrets(cs)
	cred := fullCred("orphan")
	must(t, old.Save(ctx, fullSpec("orphan"), &cred))
	bare := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: clusterstest.SecretName("bare"), Namespace: clusterstest.Namespace, Labels: map[string]string{clusters.LabelComponent: clusters.ComponentValue, clusters.LabelCluster: "bare"}},
		Data: map[string][]byte{"token": []byte("a-token")}}
	_, err := cs.CoreV1().Secrets(clusterstest.Namespace).Create(ctx, bare, metav1.CreateOptions{})
	must(t, err)
	_, err = d.Pool().Exec(ctx, "INSERT INTO clusters (cluster_id) VALUES ('bare'), ('pointed-at')")
	must(t, err)
	before := held(t, cs)

	if list, err := store.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("listed: %+v %v", list, err)
	}
	if revs, err := store.Revisions(ctx); err != nil || len(revs) != 0 {
		t.Fatalf("revisions: %v %v", revs, err)
	}
	for _, id := range []string{"orphan", "bare", "pointed-at"} {
		if _, _, err := store.Get(ctx, id); !errors.Is(err, clusters.ErrNotFound) {
			t.Errorf("%s is read as a connection: %v", id, err)
		}
		if err := store.Delete(ctx, id); !errors.Is(err, clusters.ErrNotFound) {
			t.Errorf("%s is removed as a connection: %v", id, err)
		}
	}
	m := clusters.New(store, quiet)
	must(t, m.Start(ctx))
	must(t, m.Sync(ctx))
	if list := m.List(); len(list) != 0 {
		t.Fatalf("started: %+v", list)
	}
	if noSecret, noRow, err := store.Unmatched(ctx); err != nil || len(noSecret) != 0 || !slices.Equal(noRow, []string{clusterstest.SecretName("bare"), clusterstest.SecretName("orphan")}) {
		t.Fatalf("the halves that do not match: %v %v %v", noSecret, noRow, err)
	}
	if after := held(t, cs); asJSON(after) != asJSON(before) {
		t.Fatalf("a Secret without a row was changed or deleted:\n%v\n%v", after, before)
	}
}

// Removing a connection (P3-08, DB-64) deletes its Secret and leaves its
// row with only its id, every column emptied, because other tables point
// at the row: the cluster's applications and acknowledged alerts are
// still there, and its hosts and fleets still name it.
func TestRemovingAConnectionKeepsItsRowAndWhatPointsAtIt(t *testing.T) {
	_, srv := simServer(t)
	store, d, cs := clusterstest.PG(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := clusters.New(store, quiet)
	must(t, m.Start(ctx))
	_, err := m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "staging", Description: "to be removed", Server: srv.URL, CA: "system", Auth: "token", Mode: "write"}, Credential: clusters.Credential{Token: saToken("console")}})
	must(t, err)
	for _, sql := range []string{
		"INSERT INTO alert_acks (alert_ack_id, cluster_id, alert_key) VALUES ($1, 'staging', 'KubePodCrashLooping')",
		"INSERT INTO applications (application_id, cluster_id, namespace, name) VALUES ($1, 'staging', 'web', 'shop')",
		"INSERT INTO fleets (fleet_id, cluster_id, fleet_key, name) VALUES ($1, 'staging', 'workers', 'Workers')",
		"INSERT INTO hosts (host_id, cluster_id, name) VALUES ($1, 'staging', 'node-1')",
	} {
		_, err := d.Pool().Exec(ctx, sql, db.NewID())
		must(t, err)
	}
	pointing := func() (n int) {
		t.Helper()
		must(t, d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM alert_acks WHERE cluster_id = 'staging') + (SELECT count(*) FROM applications WHERE cluster_id = 'staging')
			+ (SELECT count(*) FROM fleets WHERE cluster_id = 'staging') + (SELECT count(*) FROM hosts WHERE cluster_id = 'staging')`).Scan(&n))
		return n
	}
	_, version, _, _ := row(t, d, "staging")
	must(t, m.Remove(ctx, "staging"))
	kept, after, fields, found := row(t, d, "staging")
	if !found || kept || after <= version || fields != "||||||||false|||||" {
		t.Fatalf("the row of a removed connection: found %v kept %v version %d (was %d) %q", found, kept, after, version, fields)
	}
	if _, err := cs.CoreV1().Secrets(clusterstest.Namespace).Get(ctx, clusterstest.SecretName("staging"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Secret of a removed connection: %v", err)
	}
	if n := pointing(); n != 4 {
		t.Fatalf("%d of the four rows that point at the cluster are left", n)
	}
	if _, ok := m.Get("staging"); ok {
		t.Error("still running")
	}
	if list, _ := store.List(ctx); len(list) != 0 {
		t.Errorf("still listed: %+v", list)
	}
	// Connected again under the same name: the same row, kept again, and
	// what pointed at it still does.
	_, err = m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "production", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: saToken("console")}})
	must(t, err)
	if kept, again, fields, _ := row(t, d, "staging"); !kept || again <= after || !strings.HasPrefix(fields, "staging|production||") {
		t.Fatalf("connected again: kept %v version %d %q", kept, again, fields)
	}
	if n := pointing(); n != 4 {
		t.Fatalf("%d of the four rows are left after it was connected again", n)
	}
}

// The revision changes when only the Secret changed (DB-64): a token
// replaced by hand in the Secret restarts the connection on every replica
// at its next sync, with no row written.
func TestACredentialReplacedByHandInTheSecretIsNoticed(t *testing.T) {
	_, srv := simServer(t)
	store, d, cs := clusterstest.PG(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := clusters.New(store, quiet)
	must(t, m.Start(ctx))
	_, err := m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: saToken("console")}})
	must(t, err)
	must(t, m.Sync(ctx))
	running, _ := m.Get("staging")
	before, err := store.Revisions(ctx)
	must(t, err)
	must(t, m.Sync(ctx))
	if now, _ := m.Get("staging"); now != running {
		t.Fatal("a sync with nothing changed restarted the connection")
	}

	secrets := cs.CoreV1().Secrets(clusterstest.Namespace)
	sec, err := secrets.Get(ctx, clusterstest.SecretName("staging"), metav1.GetOptions{})
	must(t, err)
	byHand := saToken("rotated")
	sec.Data["token"] = []byte(byHand)
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	must(t, err)

	after, err := store.Revisions(ctx)
	if err != nil || after["staging"] == before["staging"] {
		t.Fatalf("the revision is %q after the Secret changed, was %q: %v", after["staging"], before["staging"], err)
	}
	_, version, _, _ := row(t, d, "staging")
	if version != 1 {
		t.Fatalf("the row was written: version %d", version)
	}
	must(t, m.Sync(ctx))
	if now, ok := m.Get("staging"); !ok || now == running {
		t.Fatal("the connection was not restarted with the new token")
	}
	if cfg, ok := m.RestConfigOf("staging"); !ok || cfg.BearerToken != byHand {
		t.Fatal("the connection does not sign in with the token of the Secret")
	}
}

// dbOf is the database a test's store is over, found again by making a
// second store over it: PG returns it, this is for the tests that were
// given the store alone.
func dbOf(s *clusters.PGStore) *db.DB { return clusters.DatabaseOf(s) }

// While the database is away, and while the API server that holds the
// Secrets is, every running connection keeps running with the credential
// it has, a change or a removal is refused with why, and nothing is
// marked as read that was not: when both answer again, what another
// replica changed meanwhile is here (DB-64, the design's risk 10, over
// the store itself).
func TestTheDatabaseOrTheSecretsAwayLeaveEveryConnectionRunning(t *testing.T) {
	_, srv := simServer(t)
	store, d, cs := clusterstest.PG(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := clusters.New(store, quiet)
	b := clusters.New(clusters.NewPGStore(d, clusterstest.Secrets(cs), nil), quiet)
	for _, m := range []*clusters.Manager{a, b} {
		must(t, m.SetLocal(ctx, clusters.Spec{Name: "here", Server: srv.URL, CA: "system", Auth: "incluster"}, nil, nil))
		must(t, m.Start(ctx))
	}
	draft := func(env string) clusters.Draft {
		return clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: env, Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}}
	}
	first := draft("staging")
	first.Credential = clusters.Credential{Token: saToken("console")}
	_, err := a.Add(ctx, first)
	must(t, err)
	must(t, a.Sync(ctx))
	must(t, b.Sync(ctx))
	running, _ := a.Get("staging")

	// The API server is away for reads of one Secret, after another
	// replica changed the connection.
	_, err = b.Update(ctx, "staging", draft("production"))
	must(t, err)
	var refuse error
	failing(cs, &refuse, "get")
	refuse = apierrors.NewServiceUnavailable("the API server is away")
	for i := 0; i < 2; i++ {
		if err := a.Sync(ctx); err == nil || !clusters.NotAsked(err) {
			t.Fatalf("a sync whose Secret cannot be read: %v", err)
		}
		if now, ok := a.Get("staging"); !ok || now != running {
			t.Fatal("the connection was stopped or restarted while its Secret could not be read")
		}
		if s, _ := a.Summary("staging"); s.Env != "staging" {
			t.Fatalf("a connection that could not be read was applied: %+v", s.Spec)
		}
	}
	// And for the list.
	var refuseList error
	failing(cs, &refuseList, "list")
	refuseList = apierrors.NewServiceUnavailable("the API server is away")
	if err := a.Sync(ctx); err == nil {
		t.Fatal("a sync whose Secrets cannot be listed went through")
	}
	if now, ok := a.Get("staging"); !ok || now != running || len(a.List()) != 2 {
		t.Fatal("the connections changed while the Secrets could not be listed")
	}
	refuse, refuseList = nil, nil
	must(t, a.Sync(ctx))
	if s, _ := a.Summary("staging"); s.Env != "production" {
		t.Fatalf("the change made meanwhile was never read: %+v", s.Spec)
	}
	if now, _ := a.Get("staging"); now != running {
		t.Error("the connection was restarted for a change of environment")
	}

	// The database goes away for good.
	d.Close()
	if err := a.Sync(ctx); err == nil {
		t.Fatal("a sync over a database that is away went through")
	}
	if _, err := a.Update(ctx, "staging", draft("development")); err == nil {
		t.Error("a change was saved with the database away")
	}
	if err := a.Remove(ctx, "staging"); err == nil {
		t.Error("a connection was removed with the database away")
	}
	if now, ok := a.Get("staging"); !ok || now != running || len(a.List()) != 2 {
		t.Fatal("the connections changed while the database was away")
	}
	if s, _ := a.Summary("staging"); s.Env != "production" {
		t.Fatalf("the connection changed while the database was away: %+v", s.Spec)
	}
	if _, _, ok := a.Lookup(clusters.LocalID); !ok {
		t.Fatal("the local cluster is gone")
	}
	if cfg, ok := a.RestConfigOf("staging"); !ok || cfg.BearerToken == "" {
		t.Fatal("the running connection lost its credential")
	}
}

// The copy at the first start with a database (DB-64): every connection a
// Secret keeps under "spec.json" becomes a kept row, whatever format it
// was stored in; a row with only its id is filled where it is, so what
// points at it still does; a connection the database already keeps stays
// as the database has it. Then "spec.json" leaves the Secrets whose
// connection has a row, the credentials staying byte for byte, and the
// local cluster's Secret is left with no keys. A Secret that cannot be
// read (a newer console's format, not a connection's record, or another
// connection's) is not copied and is left whole. No Secret is deleted,
// and the copy is made once.
func TestTheConnectionsAreCopiedFromTheirSecretsOnce(t *testing.T) {
	store, d, cs := clusterstest.PG(t, nil)
	ctx := context.Background()
	old := clusterstest.Secrets(cs)
	// As this console wrote them without a database (format 1).
	staging, cred := fullSpec("staging"), fullCred("staging")
	must(t, old.Save(ctx, staging, &cred))
	local := clusters.Spec{ID: clusters.LocalID, Name: "Lakehouse", Env: "production", Description: "saved", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster", Mode: "read", Local: true, Added: at, Updated: at}
	must(t, old.Save(ctx, local, nil))
	keptCred := fullCred("kept")
	must(t, old.Save(ctx, fullSpec("kept"), &keptCred))
	byHand := func(id string, data map[string]string) {
		t.Helper()
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: clusterstest.SecretName(id), Namespace: clusterstest.Namespace, Labels: map[string]string{clusters.LabelComponent: clusters.ComponentValue, clusters.LabelCluster: id}}, Data: map[string][]byte{}}
		for k, v := range data {
			sec.Data[k] = []byte(v)
		}
		_, err := cs.CoreV1().Secrets(clusterstest.Namespace).Create(ctx, sec, metav1.CreateOptions{})
		must(t, err)
	}
	// From before the format was numbered (format 0).
	byHand("old", map[string]string{"token": "old-token", "ca.crt": "old-ca",
		"spec.json": `{"spec":{"id":"old","name":"Old","env":"development","server":"https://10.0.0.9:6443","ca":"pem","caInfo":"CN=old","auth":"token","mode":"read","cred":{"who":"system:serviceaccount:default:old","set":"2025-03-01T10:00:00Z"},"added":"2025-03-01T10:00:00Z","updated":"2025-03-02T10:00:00Z"}}`})
	// What this console cannot read, or must not take for a connection.
	byHand("next", map[string]string{"token": "next-token", "spec.json": `{"version":9,"spec":{"id":"next","name":"Next","proxy":{"url":"http://proxy:3128"}}}`})
	byHand("junk", map[string]string{"token": "junk-token", "spec.json": "not json"})
	byHand("other", map[string]string{"token": "other-token", "spec.json": `{"version":1,"spec":{"id":"elsewhere","name":"Elsewhere","env":"other","server":"https://10.0.0.8:6443","ca":"system","auth":"token","mode":"read"}}`})
	byHand("bare", map[string]string{"token": "bare-token"})

	// What the database holds already: a row with only its id that an
	// application points at, and a connection it keeps, of the server the
	// Secret's record names (one of another server is the clusters
	// review's finding 4: TestAConnectionMadeAgainAfterARollbackIsNotPairedWithTheOldServer).
	app := db.NewID()
	for _, sql := range []string{
		"INSERT INTO clusters (cluster_id) VALUES ('old')",
		"INSERT INTO applications (application_id, cluster_id, namespace, name) VALUES ('" + app + "', 'old', 'web', 'shop')",
		"INSERT INTO clusters (cluster_id, name, environment, server_url, ca_mode, auth_method, mode, is_kept, version) VALUES ('kept', 'As the database has it', 'production', 'https://10.21.0.10:6443', 'system', 'token', 'read', true, 7)",
	} {
		_, err := d.Pool().Exec(ctx, sql)
		must(t, err)
	}
	before := held(t, cs)

	n, err := clusters.CopyFromSecrets(ctx, d, old, nil)
	if err != nil || n != 3 {
		t.Fatalf("copied %d: %v", n, err)
	}
	if after := held(t, cs); asJSON(after) != asJSON(before) {
		t.Fatal("the copy changed a Secret")
	}
	list, err := store.List(ctx)
	must(t, err)
	var ids []string
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{"local", "kept", "old", "staging"}) {
		t.Fatalf("the connections after the copy: %v", ids)
	}
	if got, gotCred, err := store.Get(ctx, "staging"); err != nil || asJSON(got) != asJSON(staging) || gotCred != cred {
		t.Fatalf("staging after the copy: %v\n%s\n%s", err, asJSON(got), asJSON(staging))
	}
	if got, _, err := store.Get(ctx, clusters.LocalID); err != nil || asJSON(got) != asJSON(local) {
		t.Fatalf("the local cluster after the copy: %v %s", err, asJSON(got))
	}
	got, gotCred, err := store.Get(ctx, "old")
	if err != nil || got.Name != "Old" || got.Env != "development" || got.Server != "https://10.0.0.9:6443" || got.CA != "pem" || got.CAInfo != "CN=old" || got.Auth != "token" || got.Mode != "read" ||
		got.Cred.Who != "system:serviceaccount:default:old" || !got.Cred.Set.Equal(time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)) || !got.Added.Equal(got.Cred.Set) || !got.Updated.Equal(got.Added.Add(24*time.Hour)) ||
		gotCred.Token != "old-token" || gotCred.CAPEM != "old-ca" {
		t.Fatalf("the connection of format 0 after the copy: %v %s", err, asJSON(got))
	}
	if got, gotCred, err := store.Get(ctx, "kept"); err != nil || got.Name != "As the database has it" || got.Env != "production" || got.CA != "system" || gotCred != keptCred {
		t.Fatalf("a connection the database kept was copied over: %v %s", err, asJSON(got))
	}
	if _, version, _, _ := row(t, d, "kept"); version != 7 {
		t.Errorf("a connection the database kept was written: version %d", version)
	}
	var apps int
	must(t, d.Pool().QueryRow(ctx, "SELECT count(*) FROM applications WHERE application_id = $1 AND cluster_id = 'old'", app).Scan(&apps))
	if apps != 1 {
		t.Fatal("the application that pointed at a row with only its id is gone")
	}
	for _, id := range []string{"next", "junk", "other", "elsewhere", "bare"} {
		if kept, _, _, found := row(t, d, id); found && kept {
			t.Errorf("%s was copied", id)
		}
	}

	// "spec.json" leaves the Secrets whose connection has a kept row.
	n, err = clusters.DropSpecs(ctx, d, old, nil)
	if err != nil || n != 4 {
		t.Fatalf("took the record out of %d Secrets: %v", n, err)
	}
	after := held(t, cs)
	if len(after) != len(before) {
		t.Fatalf("%d Secrets after, %d before: one was deleted", len(after), len(before))
	}
	for _, id := range []string{"staging", "kept", "old", clusters.LocalID} {
		name := clusterstest.SecretName(id)
		want := maps.Clone(before[name])
		delete(want, "spec.json")
		if !maps.Equal(after[name], want) {
			t.Errorf("the Secret of %s after: %v, want its credential as it was: %v", id, keysOf(after[name]), keysOf(want))
		}
	}
	if keys := keysOf(after[clusterstest.SecretName(clusters.LocalID)]); len(keys) != 0 {
		t.Errorf("the local cluster's Secret holds %v", keys)
	}
	for _, id := range []string{"next", "junk", "other", "bare"} {
		name := clusterstest.SecretName(id)
		if !maps.Equal(after[name], before[name]) {
			t.Errorf("the Secret of %s, which was not copied, was changed: %v", id, keysOf(after[name]))
		}
	}
	// Once: a second copy does nothing, and nothing more leaves a Secret.
	if n, err := clusters.CopyFromSecrets(ctx, d, old, nil); err != nil || n != 0 {
		t.Fatalf("copied again: %d %v", n, err)
	}
	if n, err := clusters.DropSpecs(ctx, d, old, nil); err != nil || n != 0 {
		t.Fatalf("took records out again: %d %v", n, err)
	}
	if noSecret, noRow, err := store.Unmatched(ctx); err != nil || len(noSecret) != 0 ||
		!slices.Equal(noRow, []string{clusterstest.SecretName("bare"), clusterstest.SecretName("junk"), clusterstest.SecretName("next"), clusterstest.SecretName("other")}) {
		t.Fatalf("the halves that do not match: %v %v %v", noSecret, noRow, err)
	}
	// The connections read the same as before the move.
	if got, gotCred, err := store.Get(ctx, "staging"); err != nil || asJSON(got) != asJSON(staging) || gotCred != cred {
		t.Fatalf("staging after its Secret lost the record: %v", err)
	}
}

// connected waits for a connection's watches to come up.
func connected(t *testing.T, m *clusters.Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		s, _ := m.Summary(id)
		if s.State == "connected" && s.Synced && s.Nodes == 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is not connected: %+v", id, s)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A restart (DB-64): connections saved through one console are started
// again, and connect, by a new one over the same database and the same
// Secrets, each with the credential it was given; the local cluster has
// the name saved for it from its first moment.
func TestTheConnectionsStartAgainAfterARestart(t *testing.T) {
	_, srv := simServer(t)
	store, d, cs := clusterstest.PG(t, nil)
	local := clusters.Spec{Name: "here", Env: "other", Server: srv.URL, CA: "system", Auth: "incluster", Mode: "read"}
	sa := saToken("console")
	kc := "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster:\n    server: " + srv.URL + "\ncontexts:\n- name: here\n  context:\n    cluster: a\n    user: u\ncurrent-context: here\nusers:\n- name: u\n  user:\n    token: " + sa + "\n"

	first, stop := context.WithCancel(context.Background())
	m := clusters.New(store, quiet)
	must(t, m.SetLocal(first, local, nil, nil))
	must(t, m.Start(first))
	_, err := m.Update(first, clusters.LocalID, clusters.Draft{Spec: clusters.Spec{Name: "Lakehouse", Env: "production"}})
	must(t, err)
	byToken, err := m.Add(first, clusters.Draft{Spec: clusters.Spec{Name: "by-token", Env: "staging", Description: "the first", Server: srv.URL, CA: "system", Auth: "token", Mode: "write"}, Credential: clusters.Credential{Token: sa}})
	must(t, err)
	byFile, err := m.Add(first, clusters.Draft{Spec: clusters.Spec{Name: "by-kubeconfig", Env: "development", CA: "system", Auth: "kubeconfig", Mode: "read"}, Credential: clusters.Credential{Kubeconfig: kc}})
	must(t, err)
	connected(t, m, byToken.ID)
	m.Stop()
	stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	again := clusters.New(clusters.NewPGStore(d, clusterstest.Secrets(cs), nil), quiet)
	must(t, again.SetLocal(ctx, local, nil, nil))
	if s, _ := again.Summary(clusters.LocalID); s.Name != "Lakehouse" || s.Env != "production" {
		t.Fatalf("the local cluster after the restart: %+v", s.Spec)
	}
	must(t, again.Start(ctx))
	if n := len(again.List()); n != 3 {
		t.Fatalf("%d connections after the restart: %+v", n, again.List())
	}
	for _, was := range []clusters.Summary{byToken, byFile} {
		connected(t, again, was.ID)
		now, _ := again.Summary(was.ID)
		// Times are kept to the microsecond.
		was.Spec.Added, was.Spec.Updated, was.Spec.Cred.Set = was.Spec.Added.Truncate(time.Microsecond), was.Spec.Updated.Truncate(time.Microsecond), was.Spec.Cred.Set.Truncate(time.Microsecond)
		if asJSON(now.Spec) != asJSON(was.Spec) {
			t.Errorf("%s after the restart:\n%s\nwas\n%s", was.ID, asJSON(now.Spec), asJSON(was.Spec))
		}
		if cfg, ok := again.RestConfigOf(was.ID); !ok || cfg.BearerToken != sa {
			t.Errorf("%s does not sign in with the credential it was given", was.ID)
		}
	}
}

// What one replica saves or removes is announced (DB-64): another
// replica's sync runs at once, long before its tick.
func TestASaveTellsTheOtherReplicasAtOnce(t *testing.T) {
	_, srv := simServer(t)
	store, d, cs := clusterstest.PG(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := clusters.New(store, quiet)
	b := clusters.New(clusters.NewPGStore(d, clusterstest.Secrets(cs), nil), quiet)
	must(t, a.Start(ctx))
	must(t, b.Start(ctx))
	listening := make(chan struct{}, 8)
	d.Subscribe(clusters.Topic, func() {
		select {
		case listening <- struct{}{}:
		default:
		}
	})
	d.Subscribe(clusters.Topic, b.SyncSoon)
	go d.Listen(ctx)
	select {
	case <-listening:
	case <-time.After(10 * time.Second):
		t.Fatal("the listener never started")
	}
	// A tick of an hour: only the notice can bring the sync.
	go b.Run(ctx, time.Hour)
	_, err := a.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: saToken("console")}})
	must(t, err)
	wait := func(what string, want bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, ok := b.Get("staging"); ok == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the other replica never heard that the connection was %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait("added", true)
	must(t, a.Remove(ctx, "staging"))
	wait("removed", false)
}

// lockedBuffer is a log the connections' goroutines write while the test
// reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// No secret of a connection is ever in the database or the log (DB-64):
// with a marker in the token, the kubeconfig, the client certificate's
// key, the OIDC client's secret and the refresh token, through the copy
// from the Secrets, a start, connections added, changed, tested, backed
// up, removed and restored, connections that cannot start, and replicas
// that sync, no row of any table and no log line holds the marker. The
// Secrets do, and the rows do hold what is not secret.
func TestNoCredentialIsEverInTheDatabaseOrTheLog(t *testing.T) {
	const marker = "MARKERc3Xp8w5secret"
	const known = "KNOWNh4Tq9v2name"
	_, srv := simServer(t)
	var logs lockedBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store, d, cs := clusterstest.PG(t, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	every := fullCred(marker)
	opts := quiet
	opts.Logger = log

	// Connections of before the move, one of each sign-in method, each
	// with every part of a credential: copied, and their records taken out.
	old := clusterstest.Secrets(cs)
	methods := []string{"token", "kubeconfig", "cert", "oidc", "none"}
	for _, auth := range methods {
		spec := fullSpec("before-" + auth)
		spec.Auth, spec.Server, spec.CA, spec.Description = auth, srv.URL, "skip", known
		must(t, old.Save(ctx, spec, &every))
	}
	if n, err := clusters.CopyFromSecrets(ctx, d, old, log); err != nil || n != len(methods) {
		t.Fatalf("copied %d: %v", n, err)
	}
	if n, err := clusters.DropSpecs(ctx, d, old, log); err != nil || n != len(methods) {
		t.Fatalf("took the record out of %d: %v", n, err)
	}
	// Two replicas start them: some cannot start, and say why.
	a, b := clusters.New(store, opts), clusters.New(clusters.NewPGStore(d, clusterstest.Secrets(cs), log), opts)
	for _, m := range []*clusters.Manager{a, b} {
		must(t, m.SetLocal(ctx, clusters.Spec{Name: "here", Server: srv.URL, CA: "system", Auth: "incluster"}, nil, nil))
		must(t, m.Start(ctx))
	}
	if n := len(a.List()); n != len(methods)+1 {
		t.Fatalf("%d connections started", n)
	}

	// The page: a token and a kubeconfig that carry the marker where the
	// console does not read it for what it shows.
	parts := strings.Split(saToken("console"), ".")
	jwt := parts[0] + "." + parts[1] + "." + marker + "signature"
	kc := "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster:\n    server: " + srv.URL + "\ncontexts:\n- name: here\n  context:\n    cluster: a\n    user: u\ncurrent-context: here\nusers:\n- name: u\n  user:\n    token: " + marker + "-in-the-kubeconfig\n"
	added, err := a.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "added", Env: "staging", Description: known, Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: jwt}})
	must(t, err)
	file, err := a.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "from-a-file", Env: "staging", CA: "system", Auth: "kubeconfig", Mode: "read"}, Credential: clusters.Credential{Kubeconfig: kc}})
	must(t, err)
	// What the editor refuses is refused in words that do not repeat it.
	for _, bad := range []clusters.Draft{
		{Spec: clusters.Spec{Name: "bad-token", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: marker + "-not-a-jwt"}},
		{Spec: clusters.Spec{Name: "bad-file", Env: "staging", CA: "system", Auth: "kubeconfig", Mode: "read"}, Credential: clusters.Credential{Kubeconfig: "users:\n- name: u\n  user:\n    token: " + marker + "\n  nonsense"}},
		{Spec: clusters.Spec{Name: "bad-cert", Env: "staging", Server: srv.URL, CA: "system", Auth: "cert", Mode: "read"}, Credential: clusters.Credential{CertPEM: marker + "-cert", KeyPEM: marker + "-key"}},
	} {
		if _, err := a.Add(ctx, bad); err == nil {
			t.Errorf("%s was accepted", bad.Name)
		} else if strings.Contains(err.Error(), marker) {
			t.Errorf("%s is refused in words that repeat the secret: %v", bad.Name, err)
		}
	}
	_, err = a.Update(ctx, added.ID, clusters.Draft{Spec: clusters.Spec{Name: "added", Env: "production", Description: known, Server: srv.URL, CA: "system", Auth: "token", Mode: "write"}, Credential: clusters.Credential{Token: parts[0] + "." + parts[1] + "." + marker + "second"}})
	must(t, err)
	_, err = a.Update(ctx, file.ID, clusters.Draft{Spec: clusters.Spec{Name: "from-a-file-renamed", Env: "staging", Server: srv.URL, CA: "system", Auth: "kubeconfig", Mode: "read"}})
	must(t, err)
	for _, id := range []string{added.ID, file.ID, "before-token", "before-cert"} {
		if _, err := a.TestSaved(ctx, id, "ann"); err != nil {
			t.Fatalf("testing %s: %v", id, err)
		}
	}
	must(t, b.Sync(ctx))

	// A backup and its restore: sealed going out, as it was coming back,
	// and once with what a backup without its key holds.
	sum, _ := a.Summary(added.ID)
	sealed, err := a.Sealed(ctx, added.ID, func(clear string) string { return "sealed:" + clear })
	must(t, err)
	must(t, a.Remove(ctx, added.ID))
	must(t, b.Sync(ctx))
	_, err = a.Restore(ctx, sum.Spec, clusters.Credential{Token: strings.TrimPrefix(sealed.Token, "sealed:")})
	must(t, err)
	// (A connection with no sign-in at all is the simulator's, and is not
	// one a backup can bring back.)
	// A restore reads the credential as an edit does: each method's is one
	// it takes, with the marker in every secret part.
	for _, auth := range methods[:4] {
		spec := fullSpec("restored-" + auth)
		spec.Name, spec.Auth, spec.Server, spec.CA = "restored-"+auth, auth, srv.URL, "system"
		c := every
		switch auth {
		case "token":
			c.Token = parts[0] + "." + parts[1] + "." + marker + "restored"
		case "kubeconfig":
			c.Kubeconfig, c.Context = kc, "here"
		case "cert":
			c.CertPEM = "-----BEGIN CERTIFICATE-----\n" + marker + "\n-----END CERTIFICATE-----"
			c.KeyPEM = "-----BEGIN PRIVATE KEY-----\n" + marker + "\n-----END PRIVATE KEY-----"
		case "oidc":
			c.OIDCIssuer, c.OIDCClientID = "https://issuer.example.com", "console"
		}
		if _, err := a.Restore(ctx, spec, c); err != nil {
			t.Fatalf("restoring a connection by %s: %v", auth, err)
		}
	}
	// A credential replaced by hand, a Secret that went, and the syncs
	// that notice both.
	secrets := cs.CoreV1().Secrets(clusterstest.Namespace)
	sec, err := secrets.Get(ctx, clusterstest.SecretName("before-token"), metav1.GetOptions{})
	must(t, err)
	sec.Data["token"] = []byte(marker + "-by-hand")
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	must(t, err)
	must(t, secrets.Delete(ctx, clusterstest.SecretName("before-none"), metav1.DeleteOptions{}))
	for _, m := range []*clusters.Manager{a, b} {
		must(t, m.Sync(ctx))
	}
	if _, _, err := store.Unmatched(ctx); err != nil {
		t.Fatal(err)
	}
	must(t, a.Remove(ctx, "before-kubeconfig"))
	must(t, b.Sync(ctx))
	a.Stop()
	b.Stop()

	if in := dbtest.Holding(t, d, marker); len(in) != 0 {
		t.Errorf("a secret of a connection is in the database: %v", in)
	}
	if in := dbtest.Holding(t, d, known); in["clusters"] == 0 {
		t.Errorf("the check is empty: what is not secret is in %v", in)
	}
	if got := logs.String(); strings.Contains(got, marker) {
		i := strings.Index(got, marker)
		t.Errorf("a secret of a connection is in the log: …%s…", got[max(0, i-300):min(len(got), i+60)])
	} else if !strings.Contains(got, "connection started") || !strings.Contains(got, "connection cannot start") {
		t.Errorf("the log the check read is not the connections':\n%s", got)
	}
	// The Secrets hold them: the marker was really there to be found.
	inSecrets := 0
	for _, data := range held(t, cs) {
		for _, v := range data {
			if strings.Contains(v, marker) {
				inSecrets++
			}
		}
	}
	if inSecrets < 6*len(methods) {
		t.Errorf("only %d values in the Secrets hold the marker", inSecrets)
	}
}

// A connection's last test is kept with the connection (DB-64): a manager
// started again over the same store shows it, every step, when it ran and
// who ran it; another replica shows it once it syncs; an edit keeps it; a
// removal takes it away, and a connection made again under the id was
// never tested. Over memory and the database (the Secrets alone are only
// the copy's source, and keep no test).
func TestALastTestIsKeptOverARestart(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testALastTestIsKept(t, clusters.NewMemoryStore()) })
	t.Run("database", func(t *testing.T) {
		s, _, _ := clusterstest.PG(t, nil)
		testALastTestIsKept(t, s)
	})
}

func testALastTestIsKept(t *testing.T, store clusters.Store) {
	_, srv := simServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := clusters.Options{Refresh: time.Second, HealthInterval: time.Second, SecretsRead: true, Now: func() time.Time { return at }}
	a := clusters.New(store, opts)
	must(t, a.Start(ctx))
	b := clusters.New(store, opts)
	must(t, b.Start(ctx))
	sa := token(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console", "exp": time.Now().Add(24 * time.Hour).Unix()})
	draft := clusters.Draft{Spec: clusters.Spec{Name: "tested", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: sa}}
	_, err := a.Add(ctx, draft)
	must(t, err)
	must(t, b.Sync(ctx))
	if s, _ := b.Summary("tested"); s.LastTest != nil || s.LastTested != nil {
		t.Fatalf("a connection never tested has a test: %+v", s.LastTest)
	}
	r, err := a.TestSaved(ctx, "tested", "ann@example.com")
	must(t, err)
	if len(r.Steps) == 0 || r.Nodes != 3 {
		t.Fatalf("the test: %+v", r)
	}
	if r.Server != srv.URL {
		t.Errorf("the test does not say where it went: %q", r.Server)
	}
	// The kept test is the connection's, without the server it went to.
	r.Server = ""
	kept := func(m *clusters.Manager, what string) {
		t.Helper()
		s, ok := m.Summary("tested")
		if !ok || s.LastTest == nil || s.LastTested == nil || !s.LastTested.Equal(at) || s.LastTestedBy != "ann@example.com" || asJSON(*s.LastTest) != asJSON(r) {
			t.Fatalf("%s: the test is not as it ran: %+v at %v by %q, want %+v", what, s.LastTest, s.LastTested, s.LastTestedBy, r)
		}
	}
	kept(a, "the replica that ran it")
	must(t, b.Sync(ctx))
	kept(b, "another replica")
	// Started again over the same store.
	c := clusters.New(store, opts)
	must(t, c.Start(ctx))
	kept(c, "after a restart")
	// An edit of the name keeps it, and so does one that restarts the
	// connection.
	_, err = c.Update(ctx, "tested", clusters.Draft{Spec: clusters.Spec{Name: "tested-renamed", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}})
	must(t, err)
	kept(c, "after a rename")
	_, err = c.Update(ctx, "tested", clusters.Draft{Spec: clusters.Spec{Name: "tested-renamed", Env: "staging", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: sa}})
	must(t, err)
	kept(c, "after a new credential")
	d := clusters.New(store, opts)
	must(t, d.Start(ctx))
	kept(d, "after an edit and a restart")
	// Removed and made again: no test.
	must(t, d.Remove(ctx, "tested"))
	_, err = d.Add(ctx, draft)
	must(t, err)
	e := clusters.New(store, opts)
	must(t, e.Start(ctx))
	if s, _ := e.Summary("tested"); s.LastTest != nil || s.LastTested != nil || s.LastTestedBy != "" {
		t.Errorf("a connection made again has the removed one's test: %+v", s)
	}
}
