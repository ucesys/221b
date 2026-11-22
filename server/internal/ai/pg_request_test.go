package ai

import (
	"context"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// A draft that waits for another SuperAdmin keeps the key of its request
// (DB-91): asked for as postAILive asks, the draft's row points at the
// request's row of approval_requests, and still does once the request is
// decided, since a decided request keeps its row.
func TestADraftThatWaitsKeepsItsRequestsKey(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	tp := twoperson.New(&kv.Keyed{Keys: map[string]kv.Backend{twoperson.Key: twoperson.NewPG(d)}, Rest: &kv.Memory{}}, nil)
	doc := NewPromptsPG(d)
	day := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	house := "Answer briefly."
	ps := Prompts{Agents: map[string]*AgentPrompt{Sherlock: {House: &house, Live: 1, Revs: 2,
		Versions: []Version{{N: 1, House: house, By: "ann", At: day.Add(-time.Hour), Why: "made live"}},
		Draft:    &Draft{Rev: 2, House: "Answer in haiku.", By: "ann", At: day}}}}
	saved(t, doc, doc, PromptsKey, ps)
	req, err := tp.Add(ctx, twoperson.Request{Kind: twoperson.KindPrompt, Agent: Sherlock, AgentName: "Sherlock", Rev: 2, By: twoperson.Who{Key: "ann", Name: "Ann"}})
	if err != nil {
		t.Fatal(err)
	}
	ps.Agents[Sherlock].Draft.Request = req.ID
	saved(t, doc, doc, PromptsKey, ps)
	linked := func() (draft, request string) {
		t.Helper()
		var p *string
		if err := d.Pool().QueryRow(ctx, `SELECT (SELECT approval_request_id::text FROM prompt_versions WHERE state = 'draft'),
			(SELECT approval_request_id::text FROM approval_requests WHERE request_number = $1 AND kind = 'prompt')`, req.ID).Scan(&p, &request); err != nil {
			t.Fatal(err)
		}
		if p != nil {
			draft = *p
		}
		return draft, request
	}
	draft, request := linked()
	if draft == "" || draft != request {
		t.Fatalf("the draft points at %q; its request is %q", draft, request)
	}
	if _, err := tp.Decide(ctx, req.ID, false, "not yet", twoperson.Who{Key: "bob", Name: "Bob"}, nil); err != nil {
		t.Fatal(err)
	}
	if d2, r2 := linked(); d2 != request || r2 != request {
		t.Fatalf("decided, the draft points at %q and the request is %q; both were %q", d2, r2, request)
	}
	// A row that names no request points at none.
	ps.Agents[Sherlock].Draft.Request = 0
	saved(t, doc, doc, PromptsKey, ps)
	if d3, _ := linked(); d3 != "" {
		t.Fatalf("a draft that names no request points at %q", d3)
	}
}
