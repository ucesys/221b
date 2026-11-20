package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/clusters/clusterstest"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/leader"
)

// DB-64 at a start: with a database and the connections in Secrets, the
// connections are copied into the database once and "spec.json" then
// leaves their Secrets, the credentials staying; a copy that fails leaves
// every Secret as it was and does not stop the start, and is made while
// the console runs, as soon as the Secrets can be read; a record a console
// from before the move wrote since is removed where the database keeps
// the connection, and not read, and left whole where it does not; and
// nothing is taken out when the database cannot say the copy was made. No
// Secret is ever deleted. Without a database the Secrets keep all of it.
func TestTheConnectionsLeaveTheirSecretsAtAStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := dbtest.Open(t)
	cs := clusterstest.Cluster()
	secrets := clusterstest.Secrets(cs)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	conn := func(id, name string) clusters.Spec {
		return clusters.Spec{ID: id, Name: name, Env: "staging", Server: "https://10.21.0.10:6443", CA: "system", Auth: "token", Mode: "read", Added: at, Updated: at}
	}
	held := func() map[string][]string {
		t.Helper()
		l, err := cs.CoreV1().Secrets(clusterstest.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, sec := range l.Items {
			out[sec.Name] = slices.Sorted(maps.Keys(sec.Data))
		}
		return out
	}
	names := func(s clusters.Store) map[string]string {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, c := range list {
			out[c.ID] = c.Name
		}
		return out
	}
	staging, local, late := clusterstest.SecretName("staging"), clusterstest.SecretName("local"), clusterstest.SecretName("late")

	// Without a database the Secrets are the store, and the name is claimed.
	none := &wiring{}
	if got := none.connections(ctx, secrets); got != clusters.Store(secrets) || !none.importIDs[clusters.ImportID] {
		t.Fatalf("without a database the store is %T (claimed %v)", got, none.importIDs)
	}
	lake := clusters.Spec{ID: clusters.LocalID, Name: "Lakehouse", Env: "production", Server: "https://10.0.0.1:6443", CA: "pem", Auth: "incluster", Mode: "read", Local: true, Added: at, Updated: at}
	if err := errors.Join(secrets.Save(ctx, conn("staging", "Staging"), &clusters.Credential{Token: "the-token", CAPEM: "the-ca"}), secrets.Save(ctx, lake, nil)); err != nil {
		t.Fatal(err)
	}
	before := map[string][]string{staging: {"ca.crt", "spec.json", "token"}, local: {"spec.json"}}
	if got := held(); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("the Secrets before: %v", got)
	}

	// A start that cannot list the Secrets: the console starts, nothing
	// says the copy was made, and every Secret is as it was.
	var away atomic.Bool
	away.Store(true)
	cs.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if away.Load() {
			return true, nil, errors.New("the API server is away")
		}
		return false, nil, nil
	})
	const every = 50 * time.Millisecond
	running, stop := context.WithCancel(ctx)
	failed := &wiring{database: d, moveAgainEvery: every}
	store, ok := failed.connections(running, secrets).(*clusters.PGStore)
	if !ok {
		t.Fatal("with a database and Secrets the connections are the database's")
	}
	time.Sleep(4 * every)
	if copied, err := d.Imported(ctx, clusters.ImportID); err != nil || copied {
		t.Fatalf("a copy that failed is recorded (%v): %v", copied, err)
	}
	away.Store(false)
	if got := held(); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("a copy that failed changed the Secrets: %v", got)
	}
	away.Store(true)
	if got, err := store.List(ctx); err != nil || len(got) != 0 {
		t.Fatalf("connections before the copy: %v %v", got, err)
	}
	away.Store(false)

	// The Secrets can be read again: the console that is running makes the
	// copy, without a restart, and takes the records out of the Secrets.
	want := map[string]string{"staging": "Staging", "local": "Lakehouse"}
	after := map[string][]string{staging: {"ca.crt", "token"}, local: {}}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if copied, _ := d.Imported(ctx, clusters.ImportID); copied && maps.EqualFunc(held(), after, slices.Equal) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the copy was never made while the console ran: the Secrets hold %v", held())
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if got := names(store); !maps.Equal(got, want) {
		t.Fatalf("the connections in the database: %v", got)
	}
	if _, cred, err := store.Get(ctx, "staging"); err != nil || cred.Token != "the-token" || cred.CAPEM != "the-ca" {
		t.Fatalf("the credential after the move: %v", err)
	}

	// The next start copies nothing, and finds nothing to take out.
	w := &wiring{database: d}
	store = w.connections(ctx, secrets).(*clusters.PGStore)
	if !w.importIDs[clusters.ImportID] || len(w.importClash) != 0 {
		t.Fatalf("the copy's name is not claimed: %v %v", w.importIDs, w.importClash)
	}
	if got := names(store); !maps.Equal(got, want) {
		t.Fatalf("the connections at the next start: %v", got)
	}
	if got := held(); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("the Secrets at the next start: %v", got)
	}

	// A console from before the move writes records of its own: over a
	// connection the database keeps, and a new one. The next start removes
	// the first and does not read it, and leaves the second whole.
	if err := errors.Join(secrets.Save(ctx, conn("staging", "Written by the console before"), nil), secrets.Save(ctx, conn("late", "Late"), &clusters.Credential{Token: "late-token"})); err != nil {
		t.Fatal(err)
	}
	again := &wiring{database: d}
	store = again.connections(ctx, secrets).(*clusters.PGStore)
	if got := names(store); !maps.Equal(got, want) {
		t.Fatalf("a record written since was read: %v", got)
	}
	after[late] = []string{"spec.json", "token"}
	if got := held(); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("the Secrets after the later records: %v", got)
	}
	if _, cred, err := store.Get(ctx, "staging"); err != nil || cred.Token != "the-token" || cred.CAPEM != "the-ca" {
		t.Fatalf("the credential after a console from before wrote the record again: %v", err)
	}

	// A database that cannot say whether the copy was made takes nothing out.
	if err := secrets.Save(ctx, conn("staging", "Written again"), nil); err != nil {
		t.Fatal(err)
	}
	gone := dbtest.Open(t)
	gone.Close()
	short, done := context.WithCancel(ctx)
	(&wiring{database: gone}).connections(short, secrets)
	done()
	after[staging] = []string{"ca.crt", "spec.json", "token"}
	if got := held(); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("taken out on a database that did not answer: %v", got)
	}

	// A new connection is made with no record in its Secret.
	if err := store.Save(ctx, conn("new", "New"), &clusters.Credential{Token: "new-token"}); err != nil {
		t.Fatal(err)
	}
	if got := held()[clusterstest.SecretName("new")]; !slices.Equal(got, []string{"token"}) {
		t.Fatalf("a new connection's Secret holds %v", got)
	}
}

// With a database and the connections in memory (no -connections-secrets:
// the simulator, a console run by hand) the connections and the
// applications stay in memory, and their copies' names are still claimed.
func TestConnectionsInMemoryStayInMemoryWithADatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &wiring{cfg: config.Config{ClusterName: "here"}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), restCfg: &rest.Config{Host: "http://127.0.0.1:1"}, mode: kube.ModeURL, activity: leader.NewActivity(true), database: dbtest.Open(t)}
	if err := w.wireClusters(ctx); err != nil {
		t.Fatal(err)
	}
	if kind := w.manager.Store().Kind(); kind != "memory" {
		t.Fatalf("the connections are kept in %s", kind)
	}
	if _, ok := w.appStore.(*apps.MemoryStore); !ok {
		t.Fatalf("the applications are kept in %T", w.appStore)
	}
	if !w.importIDs[clusters.ImportID] || !w.importIDs[apps.ImportID] || len(w.importClash) != 0 {
		t.Fatalf("the copies' names: %v, clashing %v", w.importIDs, w.importClash)
	}
	if inst, ok := w.manager.Get(clusters.LocalID); !ok || inst.Spec.Name != "here" {
		t.Fatalf("the local cluster: %+v", inst)
	}
}
