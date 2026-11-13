package ai

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// Sherlock hands troubleshooting to the Troubleshooter (SR-20): its
// instructions say so, and the Troubleshooter is in its roster, so
// hand_over offers it. With the Troubleshooter off it is in neither the
// roster nor the list, and Sherlock troubleshoots itself as before.
func TestSherlockHandsTroubleshootingToTheTroubleshooter(t *testing.T) {
	w := setup(t, nil)
	q := sherlock.Question{Key: sherlock.Key{Cluster: "local"}}
	roster := func(a sherlock.Agent) []string {
		var ids []string
		for _, r := range a.Roster {
			ids = append(ids, r.ID)
		}
		return ids
	}
	a, err := w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Troubleshooting goes to the Troubleshooter whenever it is in the list of agents below",
		"Hand it over with hand_over, even when a read or two might answer it",
		"Without the Troubleshooter in the list, troubleshoot yourself as above.",
		"\n- Troubleshooter (troubleshooter): ",
	} {
		if !strings.Contains(a.Instructions, want) {
			t.Errorf("Sherlock's instructions lack %q", want)
		}
	}
	if !slices.Contains(roster(a), Troubleshooter) {
		t.Fatalf("the roster %v has no Troubleshooter", roster(a))
	}
	// Its compact form hands nothing over and keeps troubleshooting itself.
	if c := CompactCore(); strings.Contains(c, "Troubleshooting goes to the Troubleshooter") {
		t.Error("the compact core delegates, though it has no hand_over")
	}

	off := false
	if err := w.ai.SaveAgent(context.Background(), Troubleshooter, AgentChange{AgentSettings: AgentSettings{On: &off}}); err != nil {
		t.Fatal(err)
	}
	a, err = w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(roster(a), Troubleshooter) || strings.Contains(a.Instructions, "\n- Troubleshooter (troubleshooter): ") {
		t.Fatalf("the Troubleshooter is off but offered: %v", roster(a))
	}
	if !strings.Contains(a.Instructions, "How you troubleshoot:") {
		t.Error("without the Troubleshooter Sherlock lost its own way of troubleshooting")
	}
}
