package twoperson

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

var ann, sam = Who{Key: "ann", Name: "Ann"}, Who{Key: "sam", Name: "Sam"}

// A request waits until someone else approves it; the one who asked may
// withdraw it with a reason, never approve it.
func TestARequestWaitsForSomeoneElse(t *testing.T) {
	ctx := context.Background()
	s := New(&kv.Memory{}, nil)
	r, err := s.Add(ctx, Request{Kind: KindEnvironment, Cluster: "prod", ClusterName: "Prod", From: "production", To: "staging", By: ann})
	if err != nil || r.ID != 1 || r.State != Waiting {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := s.Add(ctx, Request{Kind: KindEnvironment, Cluster: "prod", From: "production", To: "other", By: sam}); !errors.Is(err, ErrWaits) {
		t.Fatalf("a second request for the same cluster: %v", err)
	}
	applied := 0
	apply := func(Request) error { applied++; return nil }
	if _, err := s.Decide(ctx, 1, true, "", ann, apply); !errors.Is(err, ErrOwn) || applied != 0 {
		t.Fatalf("the requester approved their own: %v, applied %d", err, applied)
	}
	if _, err := s.Decide(ctx, 1, false, " ", sam, apply); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a rejection without a reason: %v", err)
	}
	if _, err := s.Decide(ctx, 1, true, "", sam, func(Request) error { return errors.New("the connection is gone") }); err == nil {
		t.Fatal("a failed apply was approved")
	}
	got, err := s.Decide(ctx, 1, true, "", sam, apply)
	if err != nil || got.State != Approved || got.DecidedBy.Key != "sam" || applied != 1 {
		t.Fatalf("%+v %v applied %d", got, err, applied)
	}
	if _, err := s.Decide(ctx, 1, false, "late", ann, apply); !errors.Is(err, ErrDecided) || !strings.HasSuffix(err.Error(), ": already approved by Sam at "+got.DecidedAt.UTC().Format("2006-01-02 15:04:05 UTC")) {
		t.Fatalf("decided twice: %v", err)
	}
	r2, _ := s.Add(ctx, Request{Kind: KindSuperAdmin, User: "bob", Super: true, Groups: []string{"admins"}, By: ann})
	if !strings.Contains(r2.What(), "bob becomes a SuperAdmin (the SuperAdmin role and groups admins)") {
		t.Fatalf("what: %s", r2.What())
	}
	if got, err := s.Decide(ctx, r2.ID, false, "changed my mind", ann, apply); err != nil || got.State != Rejected || got.Comment != "changed my mind" {
		t.Fatalf("withdrawn: %+v %v", got, err)
	}
	list, _ := s.List(ctx)
	if len(list) != 2 || list[0].ID != 2 {
		t.Fatalf("list: %+v", list)
	}
}

// Only so many decided requests stay; every waiting one does.
func TestOldDecisionsGoWaitingOnesStay(t *testing.T) {
	rec := &record{}
	rec.Requests = append(rec.Requests, Request{ID: 1, State: Waiting})
	for i := 2; i < keep+20; i++ {
		rec.Requests = append(rec.Requests, Request{ID: i, State: Approved})
	}
	prune(rec)
	if rec.Requests[0].ID != 1 || len(rec.Requests) != keep+1 {
		t.Fatalf("%d kept, first %d", len(rec.Requests), rec.Requests[0].ID)
	}
}

// The record stays within its bytes: the oldest decided requests go
// first, and a waiting one never does.
func TestTheRequestsStayWithinTheirBytes(t *testing.T) {
	rec := &record{}
	rec.Requests = append(rec.Requests, Request{ID: 1, State: Waiting, Comment: strings.Repeat("w", 400)})
	for i := 2; i < 190; i++ {
		rec.Requests = append(rec.Requests, Request{ID: i, State: Approved, Comment: strings.Repeat("r", 400)})
	}
	prune(rec)
	b, _ := json.Marshal(rec)
	if len(b) > maxBytes || rec.Requests[0].ID != 1 || rec.Requests[len(rec.Requests)-1].ID != 189 {
		t.Fatalf("%d bytes, %d kept", len(b), len(rec.Requests))
	}
}

// Two SuperAdmins approve the same request at once: the change is applied
// once, and the second is told who approved it and when (IB-09).
func TestTwoApprovalsAtOnceApplyOnce(t *testing.T) {
	ctx := context.Background()
	s := New(&kv.Memory{}, nil)
	r, _ := s.Add(ctx, Request{Kind: KindSuperAdmin, User: "bob", Super: true, By: ann})
	var applied atomic.Int32
	apply := func(Request) error {
		applied.Add(1)
		// A change that takes a moment, as a real one does.
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	bo := Who{Key: "bo", Name: "Bo"}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, who := range []Who{sam, bo} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = s.Decide(ctx, r.ID, true, "", who, apply)
		}()
	}
	close(start)
	wg.Wait()
	if applied.Load() != 1 {
		t.Fatalf("applied %d times", applied.Load())
	}
	refused := 0
	for _, err := range errs {
		if err != nil {
			if !errors.Is(err, ErrDecided) || !strings.Contains(err.Error(), "already approved by ") {
				t.Fatalf("the second: %v", err)
			}
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("refused %d: %v", refused, errs)
	}
	// Withdrawn by the one who asked reads as such.
	r2, _ := s.Add(ctx, Request{Kind: KindPrompt, Agent: "auditor", AgentName: "Auditor", Rev: 2, By: ann})
	if _, err := s.Decide(ctx, r2.ID, false, "not yet", ann, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(ctx, r2.ID, true, "", sam, apply); !errors.Is(err, ErrDecided) || !strings.Contains(err.Error(), "already withdrawn by Ann at ") {
		t.Fatalf("approving a withdrawn one: %v", err)
	}
}
