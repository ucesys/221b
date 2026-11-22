package sherlock

import (
	"context"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// A kept conversation's answers keep what Auto picked for them, why and
// what it cost, after a restart: in a ConfigMap's JSON and in the
// database's message rows (AI-48, step 0052).
func TestAKeptConversationKeepsItsPicksAI48(t *testing.T) {
	k := Key{Session: "case:C-14", Cluster: "local"}
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	msgs := []*Msg{
		{ID: 1, Role: "user", Text: "Prod is down", At: at},
		{ID: 2, Role: "sherlock", Text: "The database is full.", Status: "done", At: at, Model: "claude-opus-5-5", Effort: "high", PickBy: "classifier",
			PickWhy: "the classifier said high; moved up from Claude Haiku 5.5", PickCostUSD: 0.000123, MovedUpFrom: "claude-haiku-5-5"},
	}
	check := func(t *testing.T, kp Keeper) {
		s := New(Options{Keeper: kp})
		c := s.Conversation(k)
		c.mu.Lock()
		c.msgs = msgs
		c.mu.Unlock()
		s.keep(c, k)
		v := New(Options{Keeper: kp}).Conversation(k).View()
		if len(v.Messages) != 2 {
			t.Fatalf("read back %+v", v.Messages)
		}
		m := v.Messages[1]
		if m.Effort != "high" || m.PickBy != "classifier" || m.PickWhy != msgs[1].PickWhy || m.PickCostUSD != 0.000123 || m.MovedUpFrom != "claude-haiku-5-5" {
			t.Fatalf("the pick read back: %+v", m)
		}
	}
	t.Run("memory", func(t *testing.T) { check(t, &jsonKeeper{saved: map[string][]byte{}}) })
	t.Run("database", func(t *testing.T) {
		d := dbtest.Open(t)
		id := db.NewID()
		if _, err := d.Pool().Exec(context.Background(), "INSERT INTO cases (case_id, object_name, case_number) VALUES ($1, '14', 14)", id); err != nil {
			t.Fatal(err)
		}
		check(t, pgKeeper{d: d, caseID: id})
	})
}
