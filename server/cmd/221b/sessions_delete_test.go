package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube"
)

// The owner, November 20, 2026 (answer 1): at a start with a database,
// once the sessions' copy is recorded, this release's session Secrets
// that hold no data are deleted, and only those: one holding a session a
// console from before the move started since stays; another release's
// session Secret and a Secret with no labels are never touched, empty or
// not; and nothing is deleted when the copy is not recorded, or the
// database cannot say whether it is.
func TestEmptySessionSecretsAreDeletedOnceTheCopyIsRecorded(t *testing.T) {
	ctx := t.Context()
	d := dbtest.Open(t)
	cs := fake.NewClientset()
	const ns, release = "221b", "k8s-console-221b"
	secretsOf := func() *kube.SessionStore {
		return kube.NewSessionStoreFor(cs, ns, release, map[string]string{"app.kubernetes.io/instance": release})
	}
	create := func(name string, labels map[string]string) {
		t.Helper()
		if _, err := cs.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// An empty Secret an earlier start emptied, another release's empty
	// session Secret, and an empty Secret with no labels.
	create(release+"-session-"+"0000000000000000000000000000000000000000000000000000000000000001", map[string]string{"app.kubernetes.io/component": "session", "app.kubernetes.io/instance": release})
	create("other-221b-session-0000000000000000000000000000000000000000000000000000000000000002", map[string]string{"app.kubernetes.io/component": "session", "app.kubernetes.io/instance": "other-221b"})
	create(release+"-session-0000000000000000000000000000000000000000000000000000000000000003", nil)
	// A person signed in on the console from before the move.
	store := secretsOf()
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go store.Run(wctx)
	if !store.Synced(wctx) {
		t.Fatal("the session cache did not sync")
	}
	old := auth.NewSessions(8*time.Hour, 7*24*time.Hour, nil)
	old.Share(store, slog.New(slog.DiscardHandler))
	old.Start(auth.Session{User: auth.User{Username: "ann"}, Method: "local"})
	names := func() []string {
		t.Helper()
		list, err := cs.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, sec := range list.Items {
			out = append(out, sec.Name)
		}
		slices.Sort(out)
		return out
	}
	all := names()
	if len(all) != 4 {
		t.Fatalf("before: %v", all)
	}
	start := func(d *db.DB, secrets sessionSecrets) *wiring {
		t.Helper()
		w := &wiring{database: d, log: slog.New(slog.DiscardHandler), authCfg: auth.Config{SessionIdle: 8 * time.Hour}}
		w.sessionsInDatabase(ctx, secrets)
		return w
	}
	// The copy fails: nothing is deleted.
	start(d, &failingSecrets{sessionSecrets: secretsOf(), list: errors.New("the API server is away")})
	if got := names(); !slices.Equal(got, all) {
		t.Fatalf("a copy that failed deleted: %v", got)
	}
	// The database cannot say: nothing is deleted.
	gone := dbtest.Open(t)
	gone.Close()
	start(gone, secretsOf())
	if got := names(); !slices.Equal(got, all) {
		t.Fatalf("a database that does not answer deleted: %v", got)
	}
	// The copy: ann's Secret is emptied and deleted with the one emptied
	// before; the others stay.
	start(d, secretsOf())
	want := []string{release + "-session-0000000000000000000000000000000000000000000000000000000000000003", "other-221b-session-0000000000000000000000000000000000000000000000000000000000000002"}
	if got := names(); !slices.Equal(got, want) {
		t.Fatalf("after the copy: %v, want %v", got, want)
	}
	// A session the console from before starts now is left whole.
	old.Start(auth.Session{User: auth.User{Username: "late"}, Method: "local"})
	start(d, secretsOf())
	if got := names(); len(got) != 3 {
		t.Fatalf("after a later start: %v", got)
	}
	if held, err := secretsOf().All(ctx); err != nil || len(held) != 1 {
		t.Fatalf("the late session's Secret: %d held, %v", len(held), err)
	}
}
