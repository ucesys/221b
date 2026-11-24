package api

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
)

// The review of November 23, 2026 (auth.go:354-364 with sherlock.go:99):
// a session made while the session store was away is exchanged for a
// stored one with another ID once the store is back, and what the console
// kept under the first ID (the conversation with Sherlock, Watson's cards,
// the windows a view action goes to) was the person's no more. It is now
// kept under the session's StateID, the ID of the stored session it
// becomes, from the start (DB-65).
func TestASherlockConversationStartedWhileTheStoreWasAwayGoesOnAfterTheExchange(t *testing.T) {
	store := &awayStore{recs: map[string][]byte{}}
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	var authSvc *auth.Service
	srv, admin, _, _, _, sh, _, _ := sherlockConsole(t, "ops@corp.example", nil, nil, func(o *Options) {
		hash, _ := auth.HashPassword("correct horse battery staple")
		svc, err := auth.New(auth.Config{
			Mode: auth.ModeLocal,
			Local: []auth.LocalUser{
				{Email: "ops@corp.example", Name: "Ops", PasswordHash: hash},
				{Email: "ops2@corp.example", Name: "Ops Two", PasswordHash: hash},
			},
			Admins:       []string{"ops@corp.example", "ops2@corp.example"},
			SessionIdle:  30 * time.Minute,
			SessionMax:   2 * time.Hour,
			SessionStore: store,
			SessionKey:   auth.NewSessionKey(nil, clock),
		}, quietLog(), audit.New(audit.Options{Out: &lockedBuffer{}}), clock)
		if err != nil {
			t.Fatal(err)
		}
		o.Auth, authSvc = svc, svc
	})
	enableSherlock(t, srv, admin, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`)

	store.setAway(true)
	ops2 := signIn(t, srv, "ops2@corp.example")
	signed := sessionCookie(t, ops2, srv.URL)
	if !strings.HasPrefix(signed, "s1.") {
		t.Fatalf("the sign-in while the store is away is not a signed session: %d characters", len(signed))
	}
	if m := askAndRead(t, srv, ops2, sh, "why is shop pending?"); m.Status != "done" {
		t.Fatalf("Sherlock's answer while the store is away: %+v", m)
	}

	// The store is back; Sherlock forgets the conversations of the
	// sessions that ended, and this one has not.
	store.setAway(false)
	clockMu.Lock()
	now = now.Add(11 * time.Second)
	clockMu.Unlock()
	sh.Sweep(authSvc.Sessions().Alive)

	// The next request exchanges the signed session for a stored one.
	if r := getWith(t, ops2, srv.URL+"/api/v1/me", nil); r.StatusCode != 200 {
		t.Fatalf("me after the store came back: %d", r.StatusCode)
	}
	if stored := sessionCookie(t, ops2, srv.URL); stored == signed || strings.HasPrefix(stored, "s1.") {
		t.Fatal("the signed session was not exchanged")
	}
	v := conversation(t, srv, ops2)
	if len(v.Messages) != 2 || v.Messages[0].Text != "why is shop pending?" {
		t.Fatalf("the conversation after the exchange has %d messages: %+v", len(v.Messages), v.Messages)
	}
	// It goes on: a second question is asked in the same conversation.
	askAndRead(t, srv, ops2, sh, "and now?")
	if v := conversation(t, srv, ops2); len(v.Messages) != 4 {
		t.Fatalf("after a second question the conversation has %d messages", len(v.Messages))
	}
	sh.Sweep(authSvc.Sessions().Alive)
	if v := conversation(t, srv, ops2); len(v.Messages) != 4 {
		t.Fatalf("after a sweep the conversation has %d messages", len(v.Messages))
	}
}
