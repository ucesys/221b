package clusters_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/clusters"
)

// Two replicas over one store: what one adds, changes or removes reaches
// the other at its next sync (P7-02). A change of name, environment or
// mode keeps the running watches; a new credential restarts them.
func TestReplicasSyncConnections(t *testing.T) {
	_, other := simServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := clusters.NewMemoryStore()
	opts := clusters.Options{Refresh: time.Second, HealthInterval: time.Second, SecretsRead: true}
	a, b := clusters.New(store, opts), clusters.New(store, opts)
	local := clusters.Spec{Name: "here", Env: "other", Server: other.URL, CA: "system", Auth: "incluster", Mode: "read"}
	for _, m := range []*clusters.Manager{a, b} {
		if err := m.SetLocal(ctx, local, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sa := token(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console", "exp": time.Now().Add(24 * time.Hour).Unix()})
	draft := clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "staging", Server: other.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: sa}}
	if _, err := a.Add(ctx, draft); err != nil {
		t.Fatal(err)
	}
	ownBefore, _ := a.Get("staging")
	if _, ok := b.Get("staging"); ok {
		t.Fatal("b has it before a sync")
	}
	for _, m := range []*clusters.Manager{a, b} {
		if err := m.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if own, _ := a.Get("staging"); own != ownBefore {
		t.Error("a's own connection restarted at its sync")
	}
	first, ok := b.Get("staging")
	if !ok {
		t.Fatal("b did not start the connection a added")
	}

	// Production and write mode, set through a: b's spec follows, so the
	// two-person and read-only rules Watson takes from it hold there too.
	if _, err := a.Update(ctx, "staging", clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "production", Server: other.URL, CA: "system", Auth: "token", Mode: "write"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := b.Summary("staging"); s.Env != "production" || s.Mode != "write" {
		t.Fatalf("b after the change: %+v", s.Spec)
	}
	if same, _ := b.Get("staging"); same != first {
		t.Error("a change of environment restarted the watches")
	}

	// A new token restarts them.
	sa2 := token(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console-2", "exp": time.Now().Add(48 * time.Hour).Unix()})
	if _, err := a.Update(ctx, "staging", clusters.Draft{Spec: clusters.Spec{Name: "staging", Env: "production", Server: other.URL, CA: "system", Auth: "token", Mode: "write"}, Credential: clusters.Credential{Token: sa2}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := b.Get("staging"); again == first {
		t.Error("a new credential did not restart the watches")
	}
	if s, _ := b.Summary("staging"); !strings.Contains(s.Cred.Who, "cluster-console-2") {
		t.Errorf("b signs in as %q", s.Cred.Who)
	}

	// The local cluster's name and environment, changed through a.
	if _, err := a.Update(ctx, clusters.LocalID, clusters.Draft{Spec: clusters.Spec{Name: "Lakehouse", Env: "production"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := b.Summary(clusters.LocalID); s.Name != "Lakehouse" || s.Env != "production" || s.Mode != "read" {
		t.Fatalf("b's local cluster: %+v", s.Spec)
	}

	// Removed through a: b stops it.
	if err := a.Remove(ctx, "staging"); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get("staging"); ok {
		t.Error("b still runs a connection removed through a")
	}
	if _, ok := b.Get(clusters.LocalID); !ok {
		t.Error("b lost its local cluster")
	}
}

// The Secret store reports a revision per connection, which a save
// changes; specs keep fields a newer console wrote, an older format loads,
// and a newer one is neither read nor saved over (P7-03).
func TestSecretStoreRevisionsAndFormats(t *testing.T) {
	_, srv := simServer(t)
	cfg := &rest.Config{Host: srv.URL}
	store, err := clusters.NewSecretStore(cfg, "221b", "k8s-console-221b", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	spec := clusters.Spec{ID: "staging", Name: "staging", Env: "staging", Server: "https://10.21.0.10:6443", CA: "system", Auth: "token", Mode: "read", Added: time.Now().UTC()}
	if err := store.Save(ctx, spec, &clusters.Credential{Token: "t"}); err != nil {
		t.Fatal(err)
	}
	revs, err := store.Revisions(ctx)
	if err != nil || revs["staging"] == "" || len(revs) != 1 {
		t.Fatalf("revisions %v %v", revs, err)
	}
	spec.Description = "changed"
	if err := store.Save(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Revisions(ctx)
	if after["staging"] == revs["staging"] {
		t.Fatalf("a save kept the revision %q", after["staging"])
	}

	// The simulator speaks JSON only.
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	secrets := cs.CoreV1().Secrets("221b")
	put := func(name, specJSON string) {
		t.Helper()
		sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "221b", Labels: map[string]string{clusters.LabelComponent: clusters.ComponentValue, clusters.LabelCluster: strings.TrimPrefix(name, "k8s-console-221b-cluster-")}}, Data: map[string][]byte{"token": []byte("t")}}
			sec.Data["spec.json"] = []byte(specJSON)
			if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			return
		}
		sec.Data["spec.json"] = []byte(specJSON)
		if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// A field a newer console added survives a save here.
	put("k8s-console-221b-cluster-staging", `{"version":1,"spec":{"id":"staging","name":"staging","env":"staging","server":"https://10.21.0.10:6443","ca":"system","auth":"token","mode":"read","proxy":{"url":"http://proxy:3128"}}}`)
	got, _, err := store.Get(ctx, "staging")
	if err != nil || got.Name != "staging" {
		t.Fatalf("get %+v %v", got, err)
	}
	got.Description = "kept"
	if err := store.Save(ctx, got, nil); err != nil {
		t.Fatal(err)
	}
	sec, _ := secrets.Get(ctx, "k8s-console-221b-cluster-staging", metav1.GetOptions{})
	var st struct {
		Version int                        `json:"version"`
		Spec    map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(sec.Data["spec.json"], &st); err != nil || st.Version != 1 || string(st.Spec["proxy"]) != `{"url":"http://proxy:3128"}` || string(st.Spec["description"]) != `"kept"` {
		t.Fatalf("saved %s %v", sec.Data["spec.json"], err)
	}

	// A spec from before the format was numbered loads.
	put("k8s-console-221b-cluster-old", `{"spec":{"id":"old","name":"old","env":"development","server":"https://10.0.0.1:6443","ca":"system","auth":"token","mode":"read"}}`)
	if old, _, err := store.Get(ctx, "old"); err != nil || old.Name != "old" {
		t.Fatalf("format 0: %+v %v", old, err)
	}

	// A newer format is neither read nor saved over.
	newer := `{"version":9,"spec":{"id":"next","name":"next"}}`
	put("k8s-console-221b-cluster-next", newer)
	if _, _, err := store.Get(ctx, "next"); err == nil || !strings.Contains(err.Error(), "newer console") {
		t.Fatalf("a newer format was read: %v", err)
	}
	if err := store.Save(ctx, clusters.Spec{ID: "next", Name: "next", Env: "other", Server: "https://10.0.0.2:6443", CA: "system", Auth: "token", Mode: "read"}, nil); err == nil || !strings.Contains(err.Error(), "newer console") {
		t.Fatalf("saved over a newer format: %v", err)
	}
	sec, _ = secrets.Get(ctx, "k8s-console-221b-cluster-next", metav1.GetOptions{})
	if string(sec.Data["spec.json"]) != newer {
		t.Fatalf("the newer Secret changed: %s", sec.Data["spec.json"])
	}
	if list, _ := store.List(ctx); len(list) != 2 {
		t.Fatalf("list skips only the newer one: %+v", list)
	}
}
