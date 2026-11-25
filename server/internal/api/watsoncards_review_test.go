package api

import (
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/watson"
)

// A chat's Watson command stopped before it started never ran: its card
// is told so, not that it failed (review of November 24, 2026, sweep).
func TestAWatsonCommandStoppedBeforeItRanLeavesItsCardWaiting(t *testing.T) {
	if got := watsonEndOf(watson.Item{Status: watson.StatusStopped}); got != cards.WatsonUnrun {
		t.Fatalf("stopped before it started: %s", got)
	}
	if got := watsonEndOf(watson.Item{Status: watson.StatusStopped, Started: time.Now()}); got != watson.StatusStopped {
		t.Fatalf("stopped while it ran: %s", got)
	}
}

// A standing approval's runner manages applications, as the SuperAdmin
// who set it does, so a helm install or upgrade it approves can run
// (review of November 24, 2026, pre_existing: api/cards.go:587).
func TestAStandingApprovalsRunnerManagesApplications(t *testing.T) {
	who, _ := watsonAction{}.runner(cards.Approver{Person: cards.Person{Name: "Standing"}, As: cards.Standing{By: cards.Person{Key: "root"}, RunAs: cards.RunAs{User: "root"}}})
	if !who.ManageApps {
		t.Fatalf("%+v", who)
	}
}
