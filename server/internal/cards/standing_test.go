package cards

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func restartClass() Standing {
	return Standing{Name: "Restart crash-looping pods in development", On: true, Agent: "troubleshooter", Clusters: []string{"dev-1"}, Actions: []string{"kubectl rollout restart", "kubectl delete pod"}, Condition: ConditionAlert, PerHour: 3}
}

// A standing approval approves the narrow class it names, outside
// production, at most so many an hour; the rest wait for a person, and
// the audit log says which were approved by it.
func TestAStandingApprovalApprovesItsClassWithinItsRate(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	root := Person{Key: "root", Name: "Root"}
	if _, err := w.st.SaveStanding(ctx, Standing{Name: "prod", On: true, Clusters: []string{"prod-1"}, Actions: []string{"kubectl rollout restart"}, PerHour: 3}, root, RunAs{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("on production: %v", err)
	}
	if _, err := w.st.SaveStanding(ctx, Standing{Name: "all", On: true, Clusters: []string{"dev-1"}, Actions: []string{"kubectl"}, PerHour: 3}, root, RunAs{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the whole of kubectl: %v", err)
	}
	st, err := w.st.SaveStanding(ctx, restartClass(), root, RunAs{User: "221b-local:root", Groups: []string{"221b:superadmins"}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 4; i++ {
		id, err := w.st.Propose(ctx, restart("dev-1", ""))
		if err != nil {
			t.Fatal(err)
		}
		w.st.Wait()
		ids = append(ids, id)
	}
	for i, id := range ids[:3] {
		c := waitState(t, w.st, id, Applied)
		if c.Standing != st.ID || c.Approvals[0].Standing != st.ID || !strings.Contains(c.Approvals[0].By.Name, "standing approval") {
			t.Fatalf("card %d: %+v", i, c)
		}
	}
	fourth := waitState(t, w.st, ids[3], Queued)
	if !strings.Contains(fourth.History[len(fourth.History)-1].What, "approved 3 this hour already") {
		t.Fatalf("the fourth: %+v", fourth.History)
	}
	// An hour on, there is room again.
	w.clock.add(time.Hour)
	id, _ := w.st.Propose(ctx, restart("dev-1", ""))
	waitState(t, w.st, id, Applied)
	if log := w.audit.String(); strings.Count(log, "by a standing approval") != 4 || !strings.Contains(log, `"cards.standing"`) {
		t.Fatalf("audit: %s", log)
	}
	// What it does not cover waits: another command, another namespace,
	// production, a person's own chat.
	other := restart("dev-1", "")
	other.Actions[0].Args["command"] = "kubectl scale deployment web --replicas=0 -n shop"
	w.clock.add(time.Hour)
	if id, _ := w.st.Propose(ctx, other); waitState(t, w.st, id, Queued).Standing != "" {
		t.Fatal("covered another command")
	}
	prod := restart("prod-1", "")
	if id, _ := w.st.Propose(ctx, prod); waitState(t, w.st, id, Queued).Standing != "" {
		t.Fatal("covered production")
	}
	// A cluster made production since is never covered.
	w.mu.Lock()
	w.prod["dev-1"] = true
	w.mu.Unlock()
	if id, _ := w.st.Propose(ctx, restart("dev-1", "")); waitState(t, w.st, id, Queued).Standing != "" {
		t.Fatal("covered a cluster now production")
	}
}

// What a class covers, word by word.
func TestWhatAStandingApprovalCovers(t *testing.T) {
	st := restartClass()
	st.Namespaces = []string{"shop"}
	for line, want := range map[string]bool{
		"kubectl rollout restart deployment web -n shop":         true,
		"kubectl -n shop rollout restart deployment web":         true,
		"kubectl delete pod web-7f9 --namespace=shop":            true,
		"kubectl rollout restart deployment web -n payments":     false,
		"kubectl rollout undo deployment web -n shop":            false,
		"kubectl delete deployment web -n shop":                  false,
		"helm rollback shop 3 -n shop":                           false,
		"kubectl rollout restart deployment web":                 false,
		"kubectl rollout restart deployment web -n shop --force": true,
	} {
		a := Action{Kind: KindKubectl, Args: map[string]any{"command": line}}
		if strings.HasPrefix(line, "helm") {
			a.Kind = KindHelm
		}
		if got := st.covers(a); got != want {
			t.Errorf("%s: %v", line, got)
		}
	}
	if ok, _ := st.Matches(Card{Proposal: Proposal{Agent: "auditor", Cluster: "dev-1", Condition: Condition{Kind: ConditionAlert}}}, ClusterInfo{}); ok {
		t.Error("covered another agent")
	}
}
