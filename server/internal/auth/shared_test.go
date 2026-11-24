package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/roles"
)

// Two replicas share sessions through a store every replica sees (P7-02):
// what one starts the other finds, what one ends the other loses. The
// same tests run over the two stores there are: the Secrets, and the
// database's table (DB-66), which needs TEST_DATABASE.

// sharedKit is two replicas sharing one store, and what a test asks of
// the store behind them.
type sharedKit struct {
	a, b *Sessions
	// kept is how many records the store keeps.
	kept func() int
	// changes is how many times a kept record was written again.
	changes func() int
	// record is a record as the store keeps it now, nil for none.
	record func(key string) []byte
	// newer makes a kept record one a newer console wrote.
	newer func(key string)
	// another is the store as one more replica has it.
	another func() SharedStore
	// The Secrets' client, or the database: one of them, by the store.
	secrets *fake.Clientset
	db      *db.DB
}

// countedStore counts the writes that changed a record already kept.
type countedStore struct {
	SharedStore
	changes *atomic.Int32
}

func (c countedStore) Put(ctx context.Context, key string, b []byte, replace func([]byte) bool) (bool, error) {
	was := false
	wrote, err := c.SharedStore.Put(ctx, key, b, func(current []byte) bool {
		was = current != nil
		return replace == nil || replace(current)
	})
	if wrote && was {
		c.changes.Add(1)
	}
	return wrote, err
}

func eachSharedStore(t *testing.T, now func() time.Time, test func(t *testing.T, k sharedKit)) {
	t.Helper()
	t.Run("Secrets", func(t *testing.T) { test(t, secretsKit(t, now)) })
	t.Run("database", func(t *testing.T) { test(t, databaseKit(t, now)) })
}

func secretsKit(t *testing.T, now func() time.Time) sharedKit {
	t.Helper()
	cs := fake.NewClientset()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Counted where the API server would see them.
	var updates atomic.Int32
	cs.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates.Add(1)
		return false, nil, nil
	})
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
	secret := func(key string) *corev1.Secret {
		sec, err := cs.CoreV1().Secrets("221b").Get(context.Background(), "k8s-console-221b-session-"+key, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return sec
	}
	return sharedKit{a: mk(), b: mk(), secrets: cs,
		kept: func() int {
			list, err := cs.CoreV1().Secrets("221b").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return len(list.Items)
		},
		changes: func() int { return int(updates.Load()) },
		record: func(key string) []byte {
			if sec := secret(key); sec != nil {
				return sec.Data["session.json"]
			}
			return nil
		},
		newer: func(key string) {
			sec := secret(key)
			var rec map[string]any
			_ = json.Unmarshal(sec.Data["session.json"], &rec)
			rec["version"] = 2
			rec["device"] = "laptop"
			sec.Data["session.json"], _ = json.Marshal(rec)
			was := updates.Load()
			if _, err := cs.CoreV1().Secrets("221b").Update(context.Background(), sec, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			// The test's own write is not the console's.
			updates.Store(was)
		},
		another: func() SharedStore { return kube.NewSessionStoreFor(cs, "221b", "k8s-console-221b", nil) },
	}
}

// databaseKit is two replicas on one database, each with its own
// connections and its own listener, as two pods have. The copies each
// holds run out by the test's clock when it has one.
func databaseKit(t *testing.T, now func() time.Time) sharedKit {
	t.Helper()
	cfg := dbtest.Config(t)
	open := func() *db.DB {
		d := dbtest.OpenConfig(t, cfg)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go d.Listen(ctx)
		return d
	}
	da := open()
	if _, err := da.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	dbb := open()
	var changes atomic.Int32
	mk := func(d *db.DB) *Sessions {
		s := NewSessions(8*time.Hour, 7*24*time.Hour, now)
		s.Share(countedStore{NewPGSessions(d, now), &changes}, nil)
		return s
	}
	another := func() SharedStore { return NewPGSessions(da, nil) }
	return sharedKit{a: mk(da), b: mk(dbb), db: da,
		kept: func() int {
			var n int
			if err := da.Pool().QueryRow(context.Background(), "SELECT count(*) FROM auth_sessions").Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		},
		changes: func() int { return int(changes.Load()) },
		record: func(key string) []byte {
			b, _, err := another().Get(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
		newer: func(key string) {
			tag, err := da.Pool().Exec(context.Background(), "UPDATE auth_sessions SET record_version = 2 WHERE auth_session_id = $1", key)
			if err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("making the row a newer console's: %v %v", tag, err)
			}
		},
		another: another,
	}
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
	eachSharedStore(t, nil, func(t *testing.T, k sharedKit) {
		a, b := k.a, k.b
		started := a.Start(Session{User: User{Username: "ann", KubeName: "local:ann", KubeGroups: []string{"221b:superadmins"}}, Method: "local", Admin: true, Roles: []roles.ID{roles.SuperAdmin}})

		got, ok := b.Get(started.ID)
		if !ok {
			t.Fatal("a session started on one replica is unknown on the other")
		}
		if got.User.Username != "ann" || !got.Admin || got.User.KubeName != "local:ann" || len(got.User.KubeGroups) != 1 || got.Method != "local" {
			t.Fatalf("the other replica reads %+v", got)
		}

		// The record is kept under a hash of the ID and holds no trace of it.
		if k.kept() != 1 {
			t.Fatalf("%d records kept after one sign-in", k.kept())
		}
		kept := k.record(keyOf(started.ID))
		if kept == nil || strings.Contains(string(kept), started.ID) {
			t.Fatalf("the stored session, under the hash of its ID: %s", kept)
		}
		var rec map[string]any
		_ = json.Unmarshal(kept, &rec)
		if rec["version"] != float64(1) {
			t.Fatalf("stored %v", rec)
		}
		if cs := k.secrets; cs != nil {
			secrets, err := cs.CoreV1().Secrets("221b").List(context.Background(), metav1.ListOptions{})
			if err != nil || len(secrets.Items) != 1 {
				t.Fatalf("Secrets %v %v", secrets, err)
			}
			sec := secrets.Items[0]
			if sec.Name != "k8s-console-221b-session-"+keyOf(started.ID) || strings.Contains(sec.Name, started.ID) {
				t.Fatalf("Secret named %s", sec.Name)
			}
			if sec.Labels["app.kubernetes.io/component"] != "session" || sec.Labels["app.kubernetes.io/instance"] != "k8s-console" {
				t.Fatalf("labels %v", sec.Labels)
			}
		}
		if k.db != nil {
			if in := dbtest.Holding(t, k.db, started.ID); len(in) != 0 {
				t.Fatalf("the session's ID is in the database: %v", in)
			}
			if in := dbtest.Holding(t, k.db, keyOf(started.ID)); in["auth_sessions"] != 1 || len(in) != 1 {
				t.Fatalf("the hash of the session's ID is in %v, not in one row of auth_sessions", in)
			}
		}

		// Ended on one replica (a sign-out, a reset): gone on both, also for an
		// open stream's check.
		a.Delete(started.ID)
		eventually(t, "the other replica still has an ended session", func() bool {
			_, ok := b.peek(started.ID)
			return !ok
		})
		// Its watch may still bring the create and then the delete: the record
		// is gone for good once the store's is, and nothing makes it again.
		eventually(t, "an ended session still signs in", func() bool {
			_, ok := b.Get(started.ID)
			return !ok
		})
		if n := k.kept(); n != 0 {
			t.Fatalf("%d records kept of an ended session", n)
		}
	})
}

// A replica may hold a session's last activity while another ends it: the
// write that follows must not make the record again, or the sign-out is
// undone on every replica.
func TestLateActivityNeverBringsBackAnEndedSession(t *testing.T) {
	eachSharedStore(t, nil, func(t *testing.T, k sharedKit) {
		a, b := k.a, k.b
		started := a.Start(Session{User: User{Username: "ann", KubeName: "local:ann"}, Method: "local"})
		held, ok := b.Get(started.ID)
		if !ok {
			t.Fatal("the session is unknown on the other replica")
		}
		if k.kept() != 1 {
			t.Fatalf("%d records after sign-in", k.kept())
		}
		a.Delete(started.ID)
		b.store(keyOf(started.ID), held, false)
		if k.kept() != 0 {
			t.Fatal("a late write made the ended session again")
		}
	})
}

func TestEndingAPersonsSessionsReachesEveryReplica(t *testing.T) {
	eachSharedStore(t, nil, func(t *testing.T, k sharedKit) {
		a, b := k.a, k.b
		s1 := a.Start(Session{User: User{Username: "bob"}, Method: "local"})
		s2 := b.Start(Session{User: User{Username: "bob"}, Method: "local"})
		other := b.Start(Session{User: User{Username: "cy"}, Method: "local"})
		eventually(t, "replica A does not list replica B's sessions", func() bool { return len(a.Live()) == 3 })
		// As endSessions does after a password reset on replica A.
		if n, _ := a.DeleteWhere(func(x Session) bool { return x.User.Username == "bob" }); n != 2 {
			t.Fatalf("ended %d sessions", n)
		}
		eventually(t, "bob still has a session on replica B", func() bool {
			return !b.Alive(s1.ID) && !b.Alive(s2.ID)
		})
		if !b.Alive(other.ID) {
			t.Fatal("someone else's session ended too")
		}
	})
}

func TestLastActivityIsWrittenBackSeldom(t *testing.T) {
	var clock atomic.Int64
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	eachSharedStore(t, now, func(t *testing.T, k sharedKit) {
		// Not a whole second: the database keeps microseconds, and what is
		// read back must not look older than what was written (DB-66).
		clock.Store(time.Date(2026, 10, 19, 9, 0, 0, 123456789, time.UTC).UnixNano())
		a, b := k.a, k.b
		sess := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
		for range 10 {
			clock.Add(int64(20 * time.Second))
			if _, ok := a.Get(sess.ID); !ok {
				t.Fatal("the session ended")
			}
		}
		if n := k.changes(); n != 0 {
			t.Fatalf("%d writes in under 5 minutes of activity", n)
		}
		clock.Add(int64(2 * time.Minute))
		if _, ok := a.Get(sess.ID); !ok {
			t.Fatal("the session ended")
		}
		if n := k.changes(); n != 1 {
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
	})
}

// The polls of an open tab count for nothing, but they are what is left to
// carry the last real activity to the other replicas; with nothing new
// they write nothing.
func TestAPollWritesBackActivityThatIsDueAndNothingElse(t *testing.T) {
	var clock atomic.Int64
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	eachSharedStore(t, now, func(t *testing.T, k sharedKit) {
		clock.Store(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC).UnixNano())
		a, b := k.a, k.b
		sess := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
		clock.Add(int64(time.Minute))
		if _, ok := a.Get(sess.ID); !ok { // too soon to be written
			t.Fatal("the session ended")
		}
		seen := now()
		clock.Add(int64(time.Minute))
		if _, ok := a.peek(sess.ID); !ok || k.changes() != 0 {
			t.Fatalf("a poll within the interval: alive %v, %d writes", ok, k.changes())
		}
		clock.Add(int64(4 * time.Minute))
		if _, ok := a.peek(sess.ID); !ok || k.changes() != 1 {
			t.Fatalf("a poll once it was due: alive %v, %d writes", ok, k.changes())
		}
		eventually(t, "the other replica did not see the activity", func() bool {
			cur, ok := b.peek(sess.ID)
			return ok && cur.LastSeen.Equal(seen)
		})
		for range 3 {
			clock.Add(int64(6 * time.Minute))
			if _, ok := a.peek(sess.ID); !ok {
				t.Fatal("the session ended")
			}
			if _, ok := b.peek(sess.ID); !ok {
				t.Fatal("the session ended")
			}
		}
		if n := k.changes(); n != 1 {
			t.Fatalf("polls with nothing new wrote %d times", n-1)
		}
	})
}

func TestANewerSessionFormatIsReadAndNotWritten(t *testing.T) {
	eachSharedStore(t, nil, func(t *testing.T, k sharedKit) {
		a, b := k.a, k.b
		sess := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
		k.newer(keyOf(sess.ID))
		eventually(t, "the newer record is not read", func() bool {
			_, ok := b.peek(sess.ID)
			return ok && strings.Contains(string(k.record(keyOf(sess.ID))), `"version":2`)
		})
		// Both replicas, the one that wrote the record first among them:
		// each reads it again before it writes.
		for _, s := range []*Sessions{b, a} {
			if !s.Update(sess.ID, func(x *Session) { x.Ref = "changed" }) {
				t.Fatal("the session is not found")
			}
			after := string(k.record(keyOf(sess.ID)))
			if !strings.Contains(after, `"version":2`) || strings.Contains(after, "changed") {
				t.Fatalf("a newer session was overwritten: %s", after)
			}
		}
	})
}

// staleGets serves a record as it was, as a replica's cache does for a
// moment after another replica writes it.
type staleGets struct {
	SharedStore
	b []byte
}

func (s staleGets) Get(context.Context, string) ([]byte, bool, error) { return s.b, true, nil }

func TestAReplicaThatHasNotSeenANewerSessionDoesNotOverwriteIt(t *testing.T) {
	eachSharedStore(t, nil, func(t *testing.T, k sharedKit) {
		sess := k.a.Start(Session{User: User{Username: "ann"}, Method: "local"})
		old := k.record(keyOf(sess.ID))
		k.newer(keyOf(sess.ID))
		// This replica still has the record as version 1 wrote it.
		c := NewSessions(8*time.Hour, 7*24*time.Hour, nil)
		c.Share(staleGets{k.another(), old}, nil)
		for range 2 {
			if !c.Update(sess.ID, func(x *Session) { x.Ref = "changed" }) {
				t.Fatal("the session is not found")
			}
			after := string(k.record(keyOf(sess.ID)))
			if !strings.Contains(after, `"version":2`) || strings.Contains(after, "changed") {
				t.Fatalf("a newer session was overwritten: %s", after)
			}
		}
	})
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
