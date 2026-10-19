package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/roles"
)

// Two replicas share sessions through Secrets (P7-02): what one starts the
// other finds, what one ends the other loses.

func sharedPair(t *testing.T, now func() time.Time) (*Sessions, *Sessions, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mk := func() *Sessions {
		store := kube.NewSessionStoreFor(cs, "221b", "k8s-console-221b", map[string]string{"app.kubernetes.io/instance": "k8s-console"})
		go store.Run(ctx)
		if !store.Synced(ctx) {
			t.Fatal("the session cache did not sync")
		}
		s := NewSessions(8*time.Hour, 7*24*time.Hour, now)
		s.Share(store, nil)
		return s
	}
	return mk(), mk(), cs
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionsAreSharedBetweenReplicas(t *testing.T) {
	a, b, cs := sharedPair(t, nil)
	started := a.Start(Session{User: User{Username: "ann", KubeName: "local:ann", KubeGroups: []string{"221b:superadmins"}}, Method: "local", Admin: true, Roles: []roles.ID{roles.SuperAdmin}})

	got, ok := b.Get(started.ID)
	if !ok {
		t.Fatal("a session started on one replica is unknown on the other")
	}
	if got.User.Username != "ann" || !got.Admin || got.User.KubeName != "local:ann" || len(got.User.KubeGroups) != 1 || got.Method != "local" {
		t.Fatalf("the other replica reads %+v", got)
	}

	// The Secret is named after a hash of the ID and holds no trace of it.
	secrets, err := cs.CoreV1().Secrets("221b").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 {
		t.Fatalf("Secrets %v %v", secrets, err)
	}
	sec := secrets.Items[0]
	if sec.Name != "k8s-console-221b-session-"+keyOf(started.ID) || strings.Contains(sec.Name, started.ID) {
		t.Fatalf("Secret named %s", sec.Name)
	}
	if strings.Contains(string(sec.Data["session.json"]), started.ID) {
		t.Fatal("the stored session holds its ID")
	}
	if sec.Labels["app.kubernetes.io/component"] != "session" || sec.Labels["app.kubernetes.io/instance"] != "k8s-console" {
		t.Fatalf("labels %v", sec.Labels)
	}
	var rec map[string]any
	_ = json.Unmarshal(sec.Data["session.json"], &rec)
	if rec["version"] != float64(1) {
		t.Fatalf("stored %v", rec)
	}

	// Ended on one replica (a sign-out, a reset): gone on both, also for an
	// open stream's check.
	a.Delete(started.ID)
	eventually(t, "the other replica still has an ended session", func() bool {
		_, ok := b.peek(started.ID)
		return !ok
	})
	if _, ok := b.Get(started.ID); ok {
		t.Fatal("an ended session still signs in")
	}
}

func TestEndingAPersonsSessionsReachesEveryReplica(t *testing.T) {
	a, b, _ := sharedPair(t, nil)
	s1 := a.Start(Session{User: User{Username: "bob"}, Method: "local"})
	s2 := b.Start(Session{User: User{Username: "bob"}, Method: "local"})
	other := b.Start(Session{User: User{Username: "cy"}, Method: "local"})
	eventually(t, "replica A does not list replica B's sessions", func() bool { return len(a.Live()) == 3 })
	// As endSessions does after a password reset on replica A.
	if n := a.DeleteWhere(func(x Session) bool { return x.User.Username == "bob" }); n != 2 {
		t.Fatalf("ended %d sessions", n)
	}
	eventually(t, "bob still has a session on replica B", func() bool {
		return !b.Alive(s1.ID) && !b.Alive(s2.ID)
	})
	if !b.Alive(other.ID) {
		t.Fatal("someone else's session ended too")
	}
}

func TestLastActivityIsWrittenBackSeldom(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	a, b, cs := sharedPair(t, now)
	var updates atomic.Int32
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates.Add(1)
		return false, nil, nil
	})
	sess := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
	for range 10 {
		clock.Add(int64(20 * time.Second))
		if _, ok := a.Get(sess.ID); !ok {
			t.Fatal("the session ended")
		}
	}
	if n := updates.Load(); n != 0 {
		t.Fatalf("%d writes in under 5 minutes of activity", n)
	}
	clock.Add(int64(2 * time.Minute))
	if _, ok := a.Get(sess.ID); !ok {
		t.Fatal("the session ended")
	}
	if n := updates.Load(); n != 1 {
		t.Fatalf("%d writes after 5 minutes", n)
	}
	// The other replica counts the stored activity: 7 hours after it, the
	// session is still alive there; 9 hours after, it is not.
	clock.Add(int64(7 * time.Hour))
	eventually(t, "the other replica lost a live session", func() bool { return b.Alive(sess.ID) })
	clock.Add(int64(2 * time.Hour))
	if b.Alive(sess.ID) {
		t.Fatal("an idle session is alive")
	}
}

func TestANewerSessionFormatIsReadAndNotWritten(t *testing.T) {
	a, b, cs := sharedPair(t, nil)
	sess := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
	name := "k8s-console-221b-session-" + keyOf(sess.ID)
	sec, _ := cs.CoreV1().Secrets("221b").Get(context.Background(), name, metav1.GetOptions{})
	var rec map[string]any
	_ = json.Unmarshal(sec.Data["session.json"], &rec)
	rec["version"] = 2
	rec["device"] = "laptop"
	sec.Data["session.json"], _ = json.Marshal(rec)
	if _, err := cs.CoreV1().Secrets("221b").Update(context.Background(), sec, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the newer record is not read", func() bool {
		_, ok := b.peek(sess.ID)
		return ok
	})
	b.Update(sess.ID, func(x *Session) { x.Ref = "changed" })
	after, _ := cs.CoreV1().Secrets("221b").Get(context.Background(), name, metav1.GetOptions{})
	if !strings.Contains(string(after.Data["session.json"]), `"version":2`) || strings.Contains(string(after.Data["session.json"]), "changed") {
		t.Fatalf("a newer session was overwritten: %s", after.Data["session.json"])
	}
}

func TestSessionFieldsThisVersionDoesNotKnowAreKept(t *testing.T) {
	sess := Session{User: User{Username: "ann"}, Method: "local"}
	b, _ := encodeSession(sess, map[string]json.RawMessage{"device": json.RawMessage(`"laptop"`)})
	back, extra, version, err := decodeSession(b)
	if err != nil || version != 1 || string(extra["device"]) != `"laptop"` || back.User.Username != "ann" {
		t.Fatalf("%+v %v %d %v", back, extra, version, err)
	}
	// A record from before versions reads as the same session.
	old, _, v0, err := decodeSession([]byte(`{"user":{"username":"bob"},"method":"ldap"}`))
	if err != nil || v0 != 0 || old.User.Username != "bob" || old.Method != "ldap" {
		t.Fatalf("%+v %d %v", old, v0, err)
	}
}

// Accounts are shared through one Secret, written only over the version
// read (P7-02), in a versioned format that keeps what it does not know
// (P7-03).

func secretAccounts(cs *fake.Clientset) Backend {
	return SecretBackend(kube.NewSecretKeyFor(cs, "221b", "k8s-console-221b-users", map[string]string{"app.kubernetes.io/component": "users"}), "users.json")
}

func newAccountsOn(b Backend) *Accounts {
	return NewAccounts(b, nil, []LocalGroup{{Name: "ops", Role: roles.Viewer, Namespaces: []string{"web"}}}, false, nil)
}

func TestAccountsAreSharedBetweenReplicas(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	a, b := newAccountsOn(secretAccounts(cs)), newAccountsOn(secretAccounts(cs))
	for _, x := range []*Accounts{a, b} {
		if err := x.Load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Setup(ctx, NewAccount{Username: "admin", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	// The other replica still thinks it is the first run: its own first
	// admin is refused on the accounts as saved.
	if !b.FirstRun() {
		t.Fatal("the second replica read the accounts already")
	}
	if _, err := b.Setup(ctx, NewAccount{Username: "mallory", Password: "correct horse battery"}); !errors.Is(err, ErrFirstRunClosed) {
		t.Fatalf("a second first admin: %v", err)
	}

	// Created on one replica, both see it; a change from each keeps the
	// other's.
	if _, err := a.Create(ctx, NewAccount{Username: "ann", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Create(ctx, NewAccount{Username: "bob", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go a.Run(runCtx, 20*time.Millisecond, nil)
	eventually(t, "replica A does not see bob", func() bool { _, ok := a.Find("bob"); return ok })
	if _, ok := a.Find("ann"); !ok {
		t.Fatal("ann was lost")
	}

	// Disabled on B: A refuses her within its reload interval.
	if _, _, err := b.Update(ctx, "ann", Change{Disabled: ptrTo(true)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "replica A still lets a disabled user sign in", func() bool {
		u, _ := a.Find("ann")
		return u.Disabled
	})
}

func TestAConflictingWriteIsMadeAgain(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	a := newAccountsOn(secretAccounts(cs))
	if err := a.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create(ctx, NewAccount{Username: "ann", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	// The first update meets a write by another replica.
	var once atomic.Bool
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if once.CompareAndSwap(false, true) {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "k8s-console-221b-users", errors.New("changed"))
		}
		return false, nil, nil
	})
	if _, err := a.Create(ctx, NewAccount{Username: "bob", Password: "correct horse battery"}); err != nil {
		t.Fatalf("the change was not made again: %v", err)
	}
	b := newAccountsOn(secretAccounts(cs))
	if err := b.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Find("ann"); !ok {
		t.Fatal("ann was lost")
	}
	if _, ok := b.Find("bob"); !ok {
		t.Fatal("bob was not saved")
	}
}

func TestSavedAccountsKeepWhatTheyDoNotKnow(t *testing.T) {
	ctx := context.Background()
	mem := &MemoryBackend{}
	// Saved by a console from before versions, with a field from a newer
	// one on a user and at the top.
	_ = mem.Save(ctx, []byte(`{"users":[{"username":"ann","passwordHash":"x","created":"2026-01-01T00:00:00Z","mfa":{"totp":true}}],"policy":"strict"}`))
	a := newAccountsOn(mem)
	if err := a.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create(ctx, NewAccount{Username: "bob", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	b, _ := mem.Load(ctx)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != float64(1) || doc["policy"] != "strict" {
		t.Fatalf("saved %s", b)
	}
	users := doc["users"].([]any)
	ann := users[0].(map[string]any)
	if ann["username"] != "ann" || ann["mfa"] == nil || len(users) != 2 {
		t.Fatalf("saved %s", b)
	}
}

func TestNewerAccountsAreReadButNotWritten(t *testing.T) {
	ctx := context.Background()
	mem := &MemoryBackend{}
	_ = mem.Save(ctx, []byte(`{"version":2,"users":[{"username":"ann","passwordHash":"x","created":"2026-01-01T00:00:00Z"}]}`))
	a := newAccountsOn(mem)
	if err := a.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Find("ann"); !ok {
		t.Fatal("a newer format is not read")
	}
	_, err := a.Create(ctx, NewAccount{Username: "bob", Password: "correct horse battery"})
	if !errors.Is(err, ErrNewerAccounts) || !strings.Contains(err.Error(), "newer 221B") {
		t.Fatalf("a write over a newer format: %v", err)
	}
	b, _ := mem.Load(ctx)
	if strings.Contains(string(b), "bob") {
		t.Fatal("the newer accounts were overwritten")
	}
}

func ptrTo[T any](v T) *T { return &v }
