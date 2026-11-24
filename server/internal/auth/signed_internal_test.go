package auth

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kube"
)

// The session store away (DB-65): what a replica holds stands, and
// nothing is taken to have ended because the store could not say.

var errAwayDB = fmt.Errorf("reading the sessions: %w", db.ErrUnavailable)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func outageSessions(c *clock, store SharedStore, key *SessionKey) *Sessions {
	s := NewSessions(8*time.Hour, 7*24*time.Hour, c.now)
	s.Share(store, nil)
	s.Outage(key, 0)
	return s
}

func held(s *Sessions) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

func TestASweepWithAStoreThatFailsForgetsNothing(t *testing.T) {
	for name, fail := range map[string]error{
		"the database away":    errAwayDB,
		"a refusal":            errors.New("secrets is forbidden"),
		"no answer in time":    context.DeadlineExceeded,
		"the store left alone": ErrStoreAway,
	} {
		t.Run(name, func(t *testing.T) {
			c := newClock()
			store := NewOutageStore()
			s := outageSessions(c, store, nil)
			ann := s.Start(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
			bob := s.Start(Session{User: User{Subject: "local:bob", Username: "bob"}, Method: "local"})
			if store.Len() != 2 || held(s) != 2 {
				t.Fatalf("stored %d, held %d", store.Len(), held(s))
			}
			store.Fail(fail)
			for range 3 {
				s.Sweep()
				c.add(time.Minute)
			}
			if held(s) != 2 {
				t.Fatalf("a sweep that could not list the store forgot: %d held", held(s))
			}
			if live := s.Live(); len(live) != 2 {
				t.Fatalf("live while the store fails: %d", len(live))
			}
			if s.Count() != 2 {
				t.Fatalf("counted %d", s.Count())
			}
			for _, id := range []string{ann.ID, bob.ID} {
				if _, ok := s.Get(id); !ok {
					t.Fatal("a session this replica holds did not stand")
				}
			}
			// Long past the idle limit, still not listed: nothing is dropped,
			// here or in the store.
			c.add(9 * time.Hour)
			s.Sweep()
			if held(s) != 2 {
				t.Fatalf("an idle sweep without the list forgot: %d held", held(s))
			}
			store.Fail(nil)
			if store.Len() != 2 {
				t.Fatalf("the store lost a record: %d", store.Len())
			}
			// With the list back the idle ones go, as they always did.
			c.add(awayFor + time.Second)
			s.Sweep()
			if held(s) != 0 || store.Len() != 0 {
				t.Fatalf("after the store came back: held %d, stored %d", held(s), store.Len())
			}
		})
	}
}

// The Secrets' store lists from a cache: one that was never filled is not
// "no sessions".
func TestASweepBeforeTheSecretsAreListedForgetsNothing(t *testing.T) {
	cs := fake.NewClientset()
	store := kube.NewSessionStoreFor(cs, "221b", "k8s-console-221b", nil)
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("a cache that was never filled listed no sessions without an error")
	}
	s := NewSessions(8*time.Hour, 7*24*time.Hour, nil)
	s.Share(store, nil)
	sess := s.Start(Session{User: User{Username: "ann"}, Method: "local"})
	s.Sweep()
	if held(s) != 1 || len(s.Live()) != 1 {
		t.Fatalf("held %d, live %d", held(s), len(s.Live()))
	}
	if _, ok := s.Get(sess.ID); !ok {
		t.Fatal("the session is gone")
	}
}

func TestASessionThatCannotBeAskedAboutIsAlive(t *testing.T) {
	c := newClock()
	store := NewOutageStore()
	a, b := outageSessions(c, store, nil), outageSessions(c, store, nil)
	sess := a.Start(Session{User: User{Username: "ann"}, Method: "local"})
	other := randomID()
	if !b.Alive(sess.ID) || b.Alive(other) {
		t.Fatal("with the store answering: a stored session is alive, an unknown one is not")
	}
	fresh := outageSessions(c, store, nil)
	for name, fail := range map[string]error{"away": errAwayDB, "a refusal": errors.New("forbidden")} {
		store.Fail(fail)
		// A replica that never saw the session cannot say it ended.
		if !fresh.Alive(sess.ID) || !fresh.Alive(other) {
			t.Errorf("%s: a session the store could not be asked about was taken for ended", name)
		}
		// Not a session: nothing to keep alive, and nothing asked.
		if fresh.Alive("not-a-session") || fresh.Alive("") {
			t.Errorf("%s: a made-up value is alive", name)
		}
		// One this replica holds stands, and still ends by the clock.
		if !b.Alive(sess.ID) {
			t.Errorf("%s: a session this replica holds is not alive", name)
		}
		store.Fail(nil)
		c.add(awayFor + time.Second)
	}
	store.Fail(errAwayDB)
	c.add(9 * time.Hour)
	if b.Alive(sess.ID) {
		t.Error("a session this replica holds, idle past its limit, is alive")
	}
}

func TestEndingSessionsWhileTheStoreIsAwayIsDoneWhenItIsBack(t *testing.T) {
	c := newClock()
	store := NewOutageStore()
	s := outageSessions(c, store, nil)
	ann := s.Start(Session{User: User{Username: "ann"}, Method: "local"})
	bob := s.Start(Session{User: User{Username: "bob"}, Method: "local"})
	cat := s.Start(Session{User: User{Username: "cat"}, Method: "local"})
	store.Fail(errAwayDB)
	if n, _ := s.DeleteWhere(func(x Session) bool { return x.User.Username == "ann" }); n != 1 {
		t.Fatalf("ended %d sessions", n)
	}
	s.Delete(cat.ID) // a sign-out
	if _, ok := s.Get(ann.ID); ok {
		t.Fatal("a session ended while the store was away is still open here")
	}
	if _, ok := s.Get(cat.ID); ok {
		t.Fatal("a session signed out while the store was away is still open here")
	}
	if _, ok := s.Get(bob.ID); !ok {
		t.Fatal("ending one person's sessions ended another's")
	}
	// The store is back and still holds the records: this replica goes on
	// refusing them, and does not count them.
	store.Fail(nil)
	c.add(awayFor + time.Second)
	if store.Len() != 3 {
		t.Fatalf("stored %d", store.Len())
	}
	if _, ok := s.Get(ann.ID); ok {
		t.Fatal("an ended session came back with the store")
	}
	if _, ok := s.Get(cat.ID); ok {
		t.Fatal("a signed-out session came back with the store")
	}
	if n := len(s.Live()); n != 1 {
		t.Fatalf("%d live sessions", n)
	}
	// The next sweep removes them for every replica.
	s.Sweep()
	recs := store.Records()
	if _, ok := recs[keyOf(bob.ID)]; len(recs) != 1 || !ok {
		t.Fatalf("after the sweep the store holds %d records", len(recs))
	}
	other := outageSessions(c, store, nil)
	if _, ok := other.Get(ann.ID); ok {
		t.Fatal("another replica still takes the ended session")
	}
}

func TestAStoreThatIsAwayIsLeftAloneForAWhile(t *testing.T) {
	c := newClock()
	store := NewOutageStore()
	s := outageSessions(c, store, nil)
	sess := s.Start(Session{User: User{Username: "ann"}, Method: "local"})
	store.Fail(errAwayDB)
	before := store.Calls()
	for range 50 {
		if _, ok := s.Get(sess.ID); !ok {
			t.Fatal("the session did not stand")
		}
		s.Alive(randomID())
	}
	if n := store.Calls() - before; n != 1 {
		t.Fatalf("a store that is away was asked %d times", n)
	}
	c.add(awayFor + time.Second)
	for range 50 {
		s.Get(sess.ID)
	}
	if n := store.Calls() - before; n != 2 {
		t.Fatalf("after the wait it was asked %d times in all", n)
	}
	store.Fail(nil)
	c.add(awayFor + time.Second)
	for range 5 {
		s.Get(sess.ID)
	}
	if n := store.Calls() - before; n != 7 {
		t.Fatalf("a store that is back is asked each time: %d calls", n)
	}
	// A refusal is an answer: the store is asked again next time.
	store.Fail(errors.New("forbidden"))
	before = store.Calls()
	for range 5 {
		s.Get(sess.ID)
	}
	if n := store.Calls() - before; n != 5 {
		t.Fatalf("a store that refuses was asked %d times", n)
	}
}

// ---- which errors make a signed session ----

type sayingAway struct{ away bool }

func (e sayingAway) Error() string   { return "the store says" }
func (e sayingAway) StoreAway() bool { return e.away }

func TestOnlyAStoreThatIsAwayMakesASignedSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		signed bool
	}{
		{"the database is unavailable", errAwayDB, true},
		{"the store says it is away", fmt.Errorf("writing: %w", sayingAway{true}), true},
		{"the call ran out of time", fmt.Errorf("writing: %w", context.DeadlineExceeded), true},
		{"left alone", ErrStoreAway, true},
		{"the store says it is not away", sayingAway{false}, false},
		{"forbidden", errors.New("secrets is forbidden"), false},
		{"a conflict", apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "x", errors.New("changed")), false},
		{"the request was cancelled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if StoreAway(tc.err) != tc.signed {
				t.Fatalf("StoreAway(%v) = %v", tc.err, !tc.signed)
			}
			store := NewOutageStore()
			store.Fail(tc.err)
			s := outageSessions(newClock(), store, nil)
			sess, err := s.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
			if err != nil {
				t.Fatal(err)
			}
			if sess.Signed != tc.signed || signedShape(sess.ID) != tc.signed || plausibleID(sess.ID) == tc.signed {
				t.Fatalf("signed %v, ID of %d characters", sess.Signed, len(sess.ID))
			}
		})
	}
	if StoreAway(nil) {
		t.Fatal("no error is not away")
	}
}

// The Secrets' store: what the API server did not answer, or answered
// with "not now", is away; what it refused is not.
func TestTheSecretsStoreSaysWhenTheAPIServerIsAway(t *testing.T) {
	secrets := schema.GroupResource{Resource: "secrets"}
	for _, tc := range []struct {
		name   string
		err    error
		signed bool
	}{
		{"connection refused", errors.New("dial tcp 10.43.0.1:443: connect: connection refused"), true},
		{"no answer in time", context.DeadlineExceeded, true},
		{"service unavailable", apierrors.NewServiceUnavailable("the apiserver is shutting down"), true},
		{"etcd is away", apierrors.NewInternalError(errors.New("etcdserver: request timed out")), true},
		{"a timeout", apierrors.NewTimeoutError("timed out", 1), true},
		{"a server timeout", apierrors.NewServerTimeout(secrets, "create", 1), true},
		{"too many requests", apierrors.NewTooManyRequests("slow down", 1), true},
		{"forbidden", apierrors.NewForbidden(secrets, "x", errors.New("exceeded quota")), false},
		{"unauthorized", apierrors.NewUnauthorized("the token has expired"), false},
		{"invalid", apierrors.NewBadRequest("no"), false},
		{"too large", apierrors.NewRequestEntityTooLargeError("too large"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset()
			fail := func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err }
			cs.PrependReactor("create", "secrets", fail)
			cs.PrependReactor("get", "secrets", fail)
			cs.PrependReactor("delete", "secrets", fail)
			store := kube.NewSessionStoreFor(cs, "221b", "k8s-console-221b", nil)
			_, err := store.Put(context.Background(), "k", []byte("{}"), func([]byte) bool { return true })
			if err == nil || StoreAway(err) != tc.signed {
				t.Fatalf("Put: %v, away %v", err, StoreAway(err))
			}
			if _, _, err := store.Get(context.Background(), "k"); err == nil || StoreAway(err) != tc.signed {
				t.Fatalf("Get: %v, away %v", err, StoreAway(err))
			}
			if err := store.Delete(context.Background(), "k"); err == nil || StoreAway(err) != tc.signed {
				t.Fatalf("Delete: %v, away %v", err, StoreAway(err))
			}
			s := NewSessions(8*time.Hour, 7*24*time.Hour, nil)
			s.Share(store, nil)
			sess, err := s.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
			if err != nil || sess.Signed != tc.signed {
				t.Fatalf("signed %v, %v", sess != nil && sess.Signed, err)
			}
		})
	}
	// A Secret that is not there is an answer, and not an error.
	store := kube.NewSessionStoreFor(fake.NewClientset(), "221b", "k8s-console-221b", nil)
	if _, ok, err := store.Get(context.Background(), "k"); ok || err != nil {
		t.Fatalf("a missing record: %v %v", ok, err)
	}
}

// ---- the key ----

func TestTheSessionKeyIsMadeOnceAndReadByEveryReplica(t *testing.T) {
	cs := fake.NewClientset()
	mk := func() *SessionKey {
		return NewSessionKey(kube.NewSecretKeyFor(cs, "221b", "k8s-console-221b"+SessionKeySecretSuffix, map[string]string{"app.kubernetes.io/component": "session-key"}), nil)
	}
	keys := make([]*SessionKey, 8)
	var wg sync.WaitGroup
	for i := range keys {
		keys[i] = mk()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := keys[i].Ensure(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	sec, err := cs.CoreV1().Secrets("221b").Get(context.Background(), "k8s-console-221b-session-key", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(sec.Data["key"])
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != 32 || len(sec.Data) != 1 {
		t.Fatalf("the Secret holds %d keys, the key %d characters", len(sec.Data), len(text))
	}
	for i, k := range keys {
		if string(k.current()) != string(raw) {
			t.Fatalf("replica %d has another key than the Secret's", i)
		}
	}
	// A key that is there is never replaced, and a start reads it.
	again := mk()
	if err := again.Ensure(context.Background()); err != nil || string(again.current()) != string(raw) {
		t.Fatalf("a later start: %v", err)
	}
	after, _ := cs.CoreV1().Secrets("221b").Get(context.Background(), "k8s-console-221b-session-key", metav1.GetOptions{})
	if string(after.Data["key"]) != text {
		t.Fatal("the key was replaced")
	}
}

func TestAKeyThatCannotBeUsedIsLeftAndNotShown(t *testing.T) {
	keys := &MemoryKeys{}
	keys.Set("not-a-key-but-a-secret-all-the-same")
	k := NewSessionKey(keys, nil)
	err := k.Ensure(context.Background())
	if err == nil || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("Ensure: %v", err)
	}
	if keys.Text() != "not-a-key-but-a-secret-all-the-same" || k.current() != nil {
		t.Fatal("the key was replaced, or used")
	}
	// Without a key a sign-in while the store is away is this replica's
	// only, as it was before there were signed sessions.
	store := NewOutageStore()
	store.Fail(errAwayDB)
	s := outageSessions(newClock(), store, k)
	sess, err := s.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	if err != nil || sess.Signed || !plausibleID(sess.ID) {
		t.Fatalf("without a key: %+v %v", sess, err)
	}
	if _, ok := s.Get(sess.ID); !ok {
		t.Fatal("the session does not stand on the replica that made it")
	}
	// A key with a line end, as an editor leaves it, is read.
	good := strings.Repeat("ab", 32)
	keys.Set(good + "\n")
	k2 := NewSessionKey(keys, nil)
	if err := k2.Ensure(context.Background()); err != nil || hex.EncodeToString(k2.current()) != good {
		t.Fatalf("a key with a line end: %v", err)
	}
}

func TestWithoutSecretsTheKeyIsTheProcesssOwn(t *testing.T) {
	a, b := NewSessionKey(nil, nil), NewSessionKey(nil, nil)
	if len(a.current()) != 32 || string(a.current()) == string(b.current()) {
		t.Fatal("two processes without Secrets have one key, or none")
	}
	if a.Ensure(context.Background()) != nil || a.reread() {
		t.Fatal("a key in memory has nothing to read")
	}
}

// ---- the signed session itself ----

// signedPair is two replicas with one key, and their store, which is away.
func signedPair(t *testing.T) (*clock, *OutageStore, *MemoryKeys, *Sessions, *Sessions) {
	t.Helper()
	c := newClock()
	store := NewOutageStore()
	keys := &MemoryKeys{}
	mk := func() *Sessions {
		k := NewSessionKey(keys, c.now)
		if err := k.Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
		return outageSessions(c, store, k)
	}
	a, b := mk(), mk()
	store.Fail(errAwayDB)
	return c, store, keys, a, b
}

func TestASignedSessionCarriesWhoThePersonIsAndNothingTheyMayDo(t *testing.T) {
	c, store, keys, a, b := signedPair(t)
	sess, err := a.Open(Session{
		User:   User{Subject: "ldap:priya", Username: "priya", Email: "priya@corp.example", Name: "Priya", Groups: []string{"platform-admins", "readers"}, KubeName: "ldap:priya", KubeGroups: []string{"221b:superadmins"}},
		Method: "ldap", Ref: "uid=priya", Admin: true,
	})
	if err != nil || !sess.Signed {
		t.Fatalf("%+v %v", sess, err)
	}
	if !strings.HasPrefix(sess.ID, "s1.") || strings.Count(sess.ID, ".") != 2 || plausibleID(sess.ID) || len(sess.ID) > maxSigned {
		t.Fatalf("the cookie's value has another shape: %d characters", len(sess.ID))
	}
	if store.Len() != 0 {
		t.Fatal("a signed session was stored")
	}
	if got := sess.Expires.Sub(sess.Created); got != 30*time.Minute {
		t.Fatalf("it lasts %s", got)
	}
	calls := store.Calls()
	got, ok := b.Get(sess.ID)
	if !ok {
		t.Fatal("another replica with the key refuses it")
	}
	// The cookie is checked with the key and the clock; the store is asked
	// one thing only, whether it holds the session's ending (DB-65, after
	// the ninth cluster test's F1: until then this test asserted that the
	// store was not asked at all), and a store found away is then left
	// alone like any other.
	if n := store.Calls() - calls; n != 1 || store.Gets() == 0 {
		t.Fatalf("checking a signed session made %d calls to the store", n)
	}
	if _, ok := b.Get(sess.ID); !ok || store.Calls() != calls+1 {
		t.Fatalf("checking it again, with the store known to be away, made %d calls more", store.Calls()-calls-1)
	}
	if !got.Signed || got.User.Subject != "ldap:priya" || got.User.Username != "priya" || got.User.Email != "priya@corp.example" || got.User.Name != "Priya" || strings.Join(got.User.Groups, ",") != "platform-admins,readers" || got.Method != "ldap" || got.Ref != "uid=priya" {
		t.Fatalf("the other replica reads %+v", got)
	}
	if !got.Created.Equal(sess.Created) || !got.Expires.Equal(sess.Expires) || !got.GroupsAt.Equal(sess.Created) {
		t.Fatalf("times: created %s, expires %s, groups at %s", got.Created, got.Expires, got.GroupsAt)
	}
	// What the person may do is not in the cookie: the replica that reads
	// it has none of it until the service works it out.
	if got.Admin || len(got.Roles) != 0 || len(got.Super) != 0 || got.User.KubeName != "" || len(got.User.KubeGroups) != 0 {
		t.Fatalf("the cookie carried a grant: %+v", got)
	}
	payload, _, _ := SplitSignedForTest(sess.ID)
	p, err := openPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if p.Issued != sess.Created.Unix() || p.Expires != sess.Expires.Unix() || p.Sid == "" {
		t.Fatalf("payload %+v", p)
	}
	// The key is not in it, in any form.
	raw, _ := hex.DecodeString(keys.Text())
	for _, form := range []string{keys.Text(), string(raw), base64.RawURLEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(raw)} {
		if strings.Contains(sess.ID, form) {
			t.Fatal("the cookie holds the key")
		}
	}
	// It ends by the clock, on every replica, and is not renewed by use.
	c.add(29*time.Minute + 59*time.Second)
	if _, ok := b.Get(sess.ID); !ok {
		t.Fatal("it ended early")
	}
	if _, ok := a.Get(sess.ID); !ok {
		t.Fatal("it ended early where it was made")
	}
	c.add(time.Second)
	for name, s := range map[string]*Sessions{"a": a, "b": b, "a replica that never saw it": outageSessions(c, store, a.key)} {
		if _, ok := s.Get(sess.ID); ok {
			t.Errorf("%s takes it after it expired", name)
		}
		if s.Alive(sess.ID) {
			t.Errorf("%s says it is alive after it expired", name)
		}
	}
	a.Sweep()
	b.Sweep()
	if held(a) != 0 || held(b) != 0 {
		t.Fatalf("expired signed sessions are still held: %d %d", held(a), held(b))
	}
}

func TestAnotherKeyRefusesASignedSession(t *testing.T) {
	c, store, _, a, _ := signedPair(t)
	sess, _ := a.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	otherKeys := &MemoryKeys{}
	k := NewSessionKey(otherKeys, c.now)
	if err := k.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	other := outageSessions(c, store, k)
	if _, ok := other.Get(sess.ID); ok {
		t.Fatal("a replica with another key took it")
	}
	if other.Alive(sess.ID) {
		t.Fatal("a signed session another key signed is alive")
	}
	// And a process's own key: one made without Secrets.
	own := outageSessions(c, store, nil)
	if _, ok := own.Get(sess.ID); ok {
		t.Fatal("a process with a key of its own took it")
	}
}

func TestATamperedSignedSessionIsRefusedAndCostsNothing(t *testing.T) {
	c, store, keys, a, b := signedPair(t)
	ann, _ := a.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	root, _ := a.Open(Session{User: User{Subject: "local:root", Username: "root"}, Method: "local"})
	annPayload, annMAC, ok := SplitSignedForTest(ann.ID)
	rootPayload, rootMAC, ok2 := SplitSignedForTest(root.ID)
	if !ok || !ok2 {
		t.Fatal("the values do not split")
	}
	flip := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	// A payload this console never signed: another person, a longer life.
	forged, err := sealSigned([]byte(strings.Repeat("k", 32)), signedPayload{Sid: "x", Issued: c.now().Unix(), Expires: c.now().Unix() + 86400, Subject: "local:root", Username: "root", Method: "local"})
	if err != nil {
		t.Fatal(err)
	}
	forgedPayload, forgedMAC, _ := SplitSignedForTest(forged)
	cases := map[string]string{
		"the payload changed at its start":   "s1." + flip(annPayload, 0) + "." + annMAC,
		"the payload changed in its middle":  "s1." + flip(annPayload, len(annPayload)/2) + "." + annMAC,
		"the payload changed at its end":     "s1." + flip(annPayload, len(annPayload)-1) + "." + annMAC,
		"the MAC changed":                    "s1." + annPayload + "." + flip(annMAC, 5),
		"the MAC changed at its end":         "s1." + annPayload + "." + flip(annMAC, 42),
		"another session's MAC":              "s1." + annPayload + "." + rootMAC,
		"another session's payload":          "s1." + rootPayload + "." + annMAC,
		"a payload signed with another key":  forged,
		"a forged payload under a real MAC":  "s1." + forgedPayload + "." + annMAC,
		"a real payload under a forged MAC":  "s1." + annPayload + "." + forgedMAC,
		"no MAC":                             "s1." + annPayload,
		"an empty MAC":                       "s1." + annPayload + ".",
		"a short MAC":                        "s1." + annPayload + "." + annMAC[:42],
		"a long MAC":                         "s1." + annPayload + "." + annMAC + "A",
		"the MAC padded":                     "s1." + annPayload + "." + annMAC + "=",
		"no payload":                         "s1.." + annMAC,
		"a third part":                       ann.ID + ".x",
		"another format":                     "s2." + annPayload + "." + annMAC,
		"no format":                          annPayload + "." + annMAC,
		"the format in capitals":             "S1." + annPayload + "." + annMAC,
		"a space":                            ann.ID + " ",
		"the payload not base64":             "s1.{}." + annMAC,
		"the payload alone":                  annPayload,
		"the MAC alone, shaped like an ID":   annMAC,
		"an ID after the format":             "s1." + randomID(),
		"longer than a cookie":               "s1." + strings.Repeat("A", maxSigned) + "." + annMAC,
		"the MAC of the payload without key": "s1." + annPayload + "." + base64.RawURLEncoding.EncodeToString(macOf(nil, "s1."+annPayload)),
	}
	loads := keys.Loads()
	for name, v := range cases {
		for which, s := range map[string]*Sessions{"the replica that signed": a, "another replica": b} {
			if _, ok := s.Get(v); ok {
				t.Errorf("%s: %s took it", name, which)
			}
			if _, ok := s.peek(v); ok {
				t.Errorf("%s: %s took it without activity", name, which)
			}
			if s.Update(v, func(*Session) {}) {
				t.Errorf("%s: %s changed it", name, which)
			}
			// A value shaped like an ID is one the store would be asked
			// about, and the store is away: nothing says it ended.
			if s.Alive(v) != plausibleID(v) {
				t.Errorf("%s: %s says alive = %v", name, which, s.Alive(v))
			}
			s.Delete(v)
		}
	}
	// None of it reached the store. The key's Secret was read again at most
	// once by each replica, however many values came: a stranger's guesses
	// cost one read each half minute.
	// The store was asked twice in all: the first sign-in's write, and one
	// read on the other replica for the value shaped like an ID, each of
	// which found it away.
	if n := store.Calls(); n != 2 {
		t.Fatalf("the store was asked %d times", n)
	}
	if n := keys.Loads() - loads; n > 2 {
		t.Fatalf("the key was read again %d times", n)
	}
	// Ending the forged ones ended nothing real.
	for _, id := range []string{ann.ID, root.ID} {
		if _, ok := b.Get(id); !ok {
			t.Fatal("a real session was ended by a forged one")
		}
	}
	if n := held(b); n != 2 {
		t.Fatalf("the replica holds %d sessions", n)
	}
}

// A value of one shape never takes the other's path: a signed session is
// not looked up in the store, and an ID is not read as a signed session.
func TestTheTwoShapesOfCookieAreNeverMistaken(t *testing.T) {
	c := newClock()
	store := NewOutageStore()
	keys := &MemoryKeys{}
	k := NewSessionKey(keys, c.now)
	if err := k.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := outageSessions(c, store, k)
	stored := s.Start(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	if stored.Signed || !plausibleID(stored.ID) || signedShape(stored.ID) {
		t.Fatal("with the store answering the session is not a stored one")
	}
	rec := store.Records()[keyOf(stored.ID)]
	// A record kept under the key a signed-shaped value hashes to: the
	// store is never asked for it.
	forged := "s1." + strings.Repeat("A", 40) + "." + strings.Repeat("B", 43)
	store.Plant(keyOf(forged), rec)
	gets := store.Gets()
	if _, ok := s.Get(forged); ok {
		t.Fatal("a signed-shaped value was found in the store")
	}
	if store.Gets() != gets {
		t.Fatal("the store was asked for a signed-shaped value")
	}
	// The stored session's ID in a signed session's clothes.
	for _, v := range []string{"s1." + stored.ID, "s1." + stored.ID + "." + stored.ID, "s1.." + stored.ID} {
		if _, ok := s.Get(v); ok {
			t.Fatalf("%d characters around an ID were taken", len(v))
		}
	}
	// A signed session's parts, each shaped like an ID or not, are no IDs.
	store.Fail(errAwayDB)
	signed, _ := s.Open(Session{User: User{Subject: "local:bob", Username: "bob"}, Method: "local"})
	store.Fail(nil)
	c.add(awayFor + time.Second)
	payload, mac, _ := SplitSignedForTest(signed.ID)
	if !plausibleID(mac) {
		t.Fatal("the MAC is not shaped like an ID; the test means it to be")
	}
	store.Plant(keyOf(signed.ID), rec)
	for _, v := range []string{mac, payload} {
		if _, ok := s.Get(v); ok {
			t.Fatal("a part of a signed session was taken for an ID")
		}
	}
	// The signed session is itself, not the record planted under its hash.
	got, ok := s.Get(signed.ID)
	if !ok || got.User.Username != "bob" || !got.Signed {
		t.Fatalf("the signed session reads %+v %v", got, ok)
	}
	// In memory, without a shared store, the same holds.
	mem := NewSessions(8*time.Hour, 7*24*time.Hour, c.now)
	m := mem.Start(Session{User: User{Username: "ann"}, Method: "local"})
	if _, ok := mem.Get("s1." + m.ID); ok {
		t.Fatal("in memory an ID after the format was taken")
	}
	if _, ok := mem.Get(signed.ID); ok {
		t.Fatal("a process with its own key took another's signed session")
	}
}

func TestAKeyPutBackByHandReachesAReplicaWithinHalfAMinute(t *testing.T) {
	c, _, keys, a, b := signedPair(t)
	first, _ := a.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	if _, ok := b.Get(first.ID); !ok {
		t.Fatal("not taken")
	}
	// The Secret is replaced by hand. A signs with the new key at its next
	// sign-in, once half a minute has passed since it last read.
	old := keys.Text()
	keys.Set(strings.Repeat("0123456789abcdef", 4))
	c.add(keyReread + time.Second)
	second, _ := a.Open(Session{User: User{Subject: "local:bob", Username: "bob"}, Method: "local"})
	if !second.Signed {
		t.Fatal("not signed")
	}
	// B still has the old key: the signature does not verify, so it reads
	// the Secret again, and then it does.
	loads := keys.Loads()
	if _, ok := b.Get(second.ID); !ok {
		t.Fatal("a session signed with the key put back was refused")
	}
	if keys.Loads() != loads+1 {
		t.Fatalf("the key was read %d times", keys.Loads()-loads)
	}
	// What the old key signed is refused by a replica that has not held it.
	fresh := outageSessions(c, NewOutageStore(), b.key)
	if _, ok := fresh.Get(first.ID); ok {
		t.Fatal("a session signed with the replaced key was taken")
	}
	// Guesses do not make the Secret be read more than once in the time.
	loads = keys.Loads()
	for i := range 200 {
		fresh.Get(fmt.Sprintf("s1.%s.%s", strings.Repeat("A", 20+i%7), strings.Repeat("B", 43)))
	}
	if n := keys.Loads() - loads; n != 0 {
		t.Fatalf("guesses made the key be read %d times within half a minute", n)
	}
	c.add(keyReread + time.Second)
	for range 200 {
		fresh.Get("s1." + strings.Repeat("A", 20) + "." + strings.Repeat("B", 43))
	}
	if n := keys.Loads() - loads; n != 1 {
		t.Fatalf("after half a minute the key was read %d times", n)
	}
	// A Secret that cannot be read leaves the key as it is.
	keys.Fail(errors.New("the API server is away"))
	c.add(keyReread + time.Second)
	fresh.Get("s1." + strings.Repeat("A", 20) + "." + strings.Repeat("B", 43))
	if _, ok := fresh.Get(second.ID); !ok {
		t.Fatal("a failed read of the Secret lost the key")
	}
	if keys.Text() == old {
		t.Fatal("the key was not replaced")
	}
}

func TestASignedSessionEndedOnAReplicaIsRefusedThere(t *testing.T) {
	c, store, _, a, b := signedPair(t)
	sess, _ := a.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	other, _ := a.Open(Session{User: User{Subject: "local:ann", Username: "ann"}, Method: "local"})
	if _, ok := b.Get(sess.ID); !ok {
		t.Fatal("not taken")
	}
	calls := store.Calls()
	b.Delete(sess.ID)
	if store.Calls() != calls {
		t.Fatal("ending a signed session asked the store")
	}
	if _, ok := b.Get(sess.ID); ok {
		t.Fatal("the replica that ended it still takes it")
	}
	if b.Alive(sess.ID) {
		t.Fatal("ended, and alive")
	}
	// A sweep does not bring it back while it could still be used.
	c.add(10 * time.Minute)
	b.Sweep()
	if _, ok := b.Get(sess.ID); ok {
		t.Fatal("a sweep brought an ended signed session back")
	}
	// The person's other signed session is another session.
	if _, ok := b.Get(other.ID); !ok {
		t.Fatal("ending one signed session ended another")
	}
	// Ended where it was never seen: checked, then refused.
	fresh := outageSessions(c, store, a.key)
	fresh.Delete(other.ID)
	if _, ok := fresh.Get(other.ID); ok {
		t.Fatal("a signed session ended on a replica that had not seen it is taken there")
	}
	// What cannot be done: the replica that was not told still takes it,
	// until it expires (DB-65).
	if _, ok := a.Get(sess.ID); !ok {
		t.Fatal("the replica that was not told refuses it: the document says otherwise")
	}
	// Ending a person's sessions ends the signed ones this replica holds.
	if n, _ := a.DeleteWhere(func(x Session) bool { return x.User.Username == "ann" }); n != 2 {
		t.Fatalf("ended %d", n)
	}
	if _, ok := a.Get(sess.ID); ok {
		t.Fatal("still taken after the person's sessions were ended")
	}
	c.add(31 * time.Minute)
	a.Sweep()
	b.Sweep()
	a.mu.Lock()
	n := len(a.revoked)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d ended signed sessions are remembered past their time", n)
	}
}

func TestASessionTooLargeForACookieIsRefused(t *testing.T) {
	_, _, _, a, _ := signedPair(t)
	var groups []string
	for i := range 600 {
		groups = append(groups, "cn="+randomID()+fmt.Sprint(i))
	}
	big := Session{User: User{Subject: "ldap:priya", Username: "priya", Groups: groups}, Method: "ldap", Ref: "uid=priya"}
	sess, err := a.Open(big)
	if !errors.Is(err, ErrSessionTooLarge) || sess != nil {
		t.Fatalf("%v %v", sess, err)
	}
	if held(a) != 0 {
		t.Fatal("a refused session is held")
	}
	// Whatever fits is never longer than a cookie may be.
	for n := 0; n < 600; n += 7 {
		sess, err := a.Open(Session{User: User{Subject: "ldap:priya", Username: "priya", Groups: groups[:n]}, Method: "ldap"})
		if err != nil {
			if !errors.Is(err, ErrSessionTooLarge) {
				t.Fatal(err)
			}
			continue
		}
		if len(sess.ID) > maxSigned {
			t.Fatalf("%d groups made a value of %d characters", n, len(sess.ID))
		}
	}
	// Groups with the names directories give compress well: hundreds fit.
	var named []string
	for i := range 300 {
		named = append(named, fmt.Sprintf("cn=team-%03d-operators,ou=groups,dc=corp,dc=example", i))
	}
	if sess, err := a.Open(Session{User: User{Subject: "ldap:omar", Username: "omar", Groups: named}, Method: "ldap"}); err != nil || !sess.Signed {
		t.Fatalf("300 groups of a directory's naming: %v", err)
	}
	// A caller with no one to refuse keeps the session on this replica.
	if sess := a.Start(big); sess == nil || sess.Signed || !plausibleID(sess.ID) {
		t.Fatalf("Start: %+v", sess)
	}
}

func TestAPayloadThisConsoleSignedIsStillChecked(t *testing.T) {
	c, _, _, a, _ := signedPair(t)
	key := a.key.current()
	now := c.now().Unix()
	for name, p := range map[string]signedPayload{
		"no session":          {Issued: now, Expires: now + 60, Subject: "local:ann", Username: "ann", Method: "local"},
		"no person":           {Sid: "x", Issued: now, Expires: now + 60, Method: "local"},
		"no way of signing":   {Sid: "x", Issued: now, Expires: now + 60, Subject: "local:ann", Username: "ann"},
		"sign-in off":         {Sid: "x", Issued: now, Expires: now + 60, Subject: "anonymous", Username: "anonymous", Method: "none"},
		"expired":             {Sid: "x", Issued: now - 120, Expires: now - 60, Subject: "local:ann", Username: "ann", Method: "local"},
		"expires this second": {Sid: "x", Issued: now - 120, Expires: now, Subject: "local:ann", Username: "ann", Method: "local"},
		"no lifetime":         {Sid: "x", Issued: now, Expires: now, Subject: "local:ann", Username: "ann", Method: "local"},
	} {
		v, err := sealSigned(key, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := a.Get(v); ok {
			t.Errorf("%s: taken", name)
		}
	}
	good, _ := sealSigned(key, signedPayload{Sid: "x", Issued: now, Expires: now + 60, Subject: "local:ann", Username: "ann", Method: "local"})
	if _, ok := a.Get(good); !ok {
		t.Fatal("a good one is refused")
	}
}
