package twoperson

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// The store behaves the same over the database as over a ConfigMap: a
// request waits, another person decides it, a number is never given
// twice; and every field comes back.
func TestRequestsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s := New(NewPG(d), func() time.Time { return now })
	ann, bob := Who{Key: "local|ann", Name: "Ann"}, Who{Key: "local|bob", Name: "Bob"}
	env, err := s.Add(ctx, Request{Kind: KindEnvironment, Cluster: "c1", ClusterName: "prod", From: "staging", To: "production", By: ann})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := s.Add(ctx, Request{Kind: KindPrompt, Agent: "troubleshooter", AgentName: "Troubleshooter", Rev: 4, Untested: "the test API is down", By: ann})
	if err != nil {
		t.Fatal(err)
	}
	super, err := s.Add(ctx, Request{Kind: KindSuperAdmin, User: "cy", Super: true, Groups: []string{"ops", "dba"}, By: bob})
	if err != nil {
		t.Fatal(err)
	}
	if env.ID == prompt.ID || prompt.ID == super.ID {
		t.Fatalf("numbers %d %d %d", env.ID, prompt.ID, super.ID)
	}
	if _, err := s.Decide(ctx, env.ID, true, "", ann, nil); !errors.Is(err, ErrOwn) {
		t.Fatalf("the requester approved her own request: %v", err)
	}
	now = now.Add(time.Minute)
	decided, err := s.Decide(ctx, env.ID, true, "go ahead", bob, nil)
	if err != nil || decided.State != Approved {
		t.Fatalf("deciding: %+v %v", decided, err)
	}
	if _, err := s.Withdraw(ctx, func(r Request) bool { return r.Agent == "troubleshooter" }, ann, "Withdrawn: draft 5 was saved."); err != nil {
		t.Fatal(err)
	}
	// Another replica's store reads the same, every field included.
	list, err := New(NewPG(d), nil).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(list, mine) || len(list) != 3 {
		t.Fatalf("two stores disagree, or %d requests: %v", len(list), err)
	}
	by := map[int]Request{}
	for _, r := range list {
		by[r.ID] = r
	}
	if r := by[env.ID]; r.ClusterName != "prod" || r.From != "staging" || r.To != "production" || r.By != ann || r.DecidedBy == nil || *r.DecidedBy != bob || r.Comment != "go ahead" || !r.DecidedAt.Equal(now) || r.Withdrawn {
		t.Errorf("the environment request came back as %+v", r)
	}
	if r := by[prompt.ID]; r.State != Rejected || !r.Withdrawn || r.Rev != 4 || r.Untested == "" || r.AgentName != "Troubleshooter" {
		t.Errorf("the prompt request came back as %+v", r)
	}
	if r := by[super.ID]; r.State != Waiting || !r.Super || !reflect.DeepEqual(r.Groups, []string{"ops", "dba"}) || r.DecidedBy != nil {
		t.Errorf("the SuperAdmin request came back as %+v", r)
	}
	next, err := s.Add(ctx, Request{Kind: KindEnvironment, Cluster: "c2", From: "dev", To: "staging", By: bob})
	if err != nil || next.ID <= super.ID {
		t.Fatalf("the next number is %d after %d: %v", next.ID, super.ID, err)
	}
}

// What the ConfigMap held is copied once, numbers and decisions included.
func TestRequestsAreCopiedOnce(t *testing.T) {
	ctx := context.Background()
	cm := &kv.Memory{}
	// PostgreSQL keeps a time to the microsecond.
	old := New(cm, func() time.Time { return time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC) })
	ann, bob := Who{Key: "a", Name: "Ann"}, Who{Key: "b", Name: "Bob"}
	r1, _ := old.Add(ctx, Request{Kind: KindEnvironment, Cluster: "c1", From: "dev", To: "production", By: ann})
	if _, err := old.Decide(ctx, r1.ID, false, "not now", bob, nil); err != nil {
		t.Fatal(err)
	}
	r2, _ := old.Add(ctx, Request{Kind: KindSuperAdmin, User: "cy", Super: true, By: ann})
	pg := NewPG(dbtest.Open(t))
	if copied, err := pg.Import(ctx, cm, "test"); err != nil || !copied {
		t.Fatalf("first copy: %v %v", copied, err)
	}
	if copied, err := pg.Import(ctx, cm, "test"); err != nil || copied {
		t.Fatalf("second copy: %v %v", copied, err)
	}
	want, _ := old.List(ctx)
	got, err := New(pg, nil).List(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v (%v)", got, want, err)
	}
	next, err := New(pg, nil).Add(ctx, Request{Kind: KindEnvironment, Cluster: "c9", From: "dev", To: "staging", By: bob})
	if err != nil || next.ID <= r2.ID {
		t.Fatalf("after the copy the next number is %d: %v", next.ID, err)
	}
}
