package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/appstest"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// DB-62, DB-63 at a start: with a database and the connections in
// Secrets, the applications' records are copied into the database once
// and their Secrets are then rewritten to hold the values alone; a copy
// that fails leaves every Secret as it was for the next start; a record a
// console from before the move wrote since is removed where the database
// has the application, and not read, and left whole where it has not; and
// nothing is rewritten when the database cannot say the copy was made. No
// Secret is ever deleted. (A console without a database, whose Secrets
// kept all of it, is no more: DB-69.)
func TestTheApplicationsLeaveTheirSecretsAtAStart(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Open(t)
	cs := appstest.Cluster()
	secrets := appstest.Secrets(cs)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	app := func(name, values string) apps.Record {
		return apps.Record{Cluster: "local", Namespace: "web", Name: name, Source: apps.Source{Repo: "https://github.com/ucesys/" + name, Ref: "main", RefKind: apps.RefBranch},
			Values: values, Params: []string{"image.tag=1"}, Created: at, Updated: at}
	}
	held := func() map[string][]string {
		t.Helper()
		l, err := cs.CoreV1().Secrets(appstest.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, sec := range l.Items {
			out[sec.Name] = slices.Sorted(maps.Keys(sec.Data))
		}
		return out
	}
	values := func(s apps.Store) map[string]string {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range list {
			out[r.Name] = r.Values
		}
		return out
	}
	shop, blog, late := appstest.SecretName("local", "web", "shop"), appstest.SecretName("local", "web", "blog"), appstest.SecretName("local", "web", "late")

	token := "ghp-of-before-0.12.1"
	if err := errors.Join(secrets.Save(ctx, app("shop", "db:\n  password: hunter2\n"), &token), secrets.Save(ctx, app("blog", "replicas: 2\n"), nil)); err != nil {
		t.Fatal(err)
	}
	before := map[string][]string{shop: {"spec.json", "token"}, blog: {"spec.json"}}
	if got := held(); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("the Secrets before: %v", got)
	}

	// A start that cannot list the Secrets: the console starts, nothing
	// says the copy was made, and every Secret is as it was.
	away := true
	cs.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if away {
			return true, nil, errors.New("the API server is away")
		}
		return false, nil, nil
	})
	failed := &wiring{database: d}
	if _, ok := failed.applications(ctx, secrets).(*apps.PGStore); !ok {
		t.Fatal("with a database and Secrets the applications are the database's")
	}
	away = false
	if copied, err := d.Imported(ctx, apps.ImportID); err != nil || copied {
		t.Fatalf("a copy that failed is recorded (%v): %v", copied, err)
	}
	if got := held(); !maps.EqualFunc(got, before, slices.Equal) {
		t.Fatalf("a copy that failed changed the Secrets: %v", got)
	}

	// The next start copies, and rewrites the Secrets.
	w := &wiring{database: d}
	store := w.applications(ctx, secrets)
	if !w.importIDs[apps.ImportID] || len(w.importClash) != 0 {
		t.Fatalf("the copy's name is not claimed: %v %v", w.importIDs, w.importClash)
	}
	if copied, err := d.Imported(ctx, apps.ImportID); err != nil || !copied {
		t.Fatalf("the copy is not recorded: %v", err)
	}
	want := map[string]string{"shop": "db:\n  password: hunter2\n", "blog": "replicas: 2\n"}
	if got := values(store); !maps.Equal(got, want) {
		t.Fatalf("the applications in the database: %v", got)
	}
	after := map[string][]string{shop: {"set.json", "token"}, blog: {"set.json"}}
	if got := held(); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("the Secrets after the move: %v", got)
	}
	if _, tok, err := store.Get(ctx, "local", "web", "shop"); err != nil || tok != token {
		t.Fatalf("the token after the move: %q %v", tok, err)
	}

	// A console from before the move writes records of its own: over an
	// application the database has, and a new one. The next start removes
	// the first and does not read it, and leaves the second whole.
	if err := errors.Join(secrets.Save(ctx, app("shop", "written: by the console before\n"), nil), secrets.Save(ctx, app("late", "late: true\n"), nil)); err != nil {
		t.Fatal(err)
	}
	again := &wiring{database: d}
	store = again.applications(ctx, secrets)
	if got := values(store); !maps.Equal(got, want) {
		t.Fatalf("a record written since was read: %v", got)
	}
	after[late] = []string{"spec.json"}
	if got := held(); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("the Secrets after the later records: %v", got)
	}

	// A database that cannot say whether the copy was made rewrites nothing.
	if err := secrets.Save(ctx, app("blog", "written: again\n"), nil); err != nil {
		t.Fatal(err)
	}
	gone := dbtest.Open(t)
	gone.Close()
	(&wiring{database: gone}).applications(ctx, secrets)
	after[blog] = []string{"set.json", "spec.json"}
	if got := held(); !maps.EqualFunc(got, after, slices.Equal) {
		t.Fatalf("rewritten on a database that did not answer: %v", got)
	}

	// A new application is made without a record in its Secret, and the
	// labels say whose it is.
	if err := store.Save(ctx, app("new", "new: true\n"), nil); err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets(appstest.Namespace).Get(ctx, appstest.SecretName("local", "web", "new"), metav1.GetOptions{})
	if err != nil || !slices.Equal(slices.Sorted(maps.Keys(sec.Data)), []string{"set.json"}) || sec.Labels[apps.LabelAppName] != "new" || sec.Labels[apps.LabelAppNamespace] != "web" {
		t.Fatalf("a new application's Secret: %+v %v", sec, err)
	}
}
