package cards

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cardid"
)

// --- The phase 12 review: a change keyed on what it is about, and an
// apply that ends after a new leader failed its card (W-10, W-11) ---

// gateExec applies only once released.
type gateExec struct {
	*fakeExec
	release chan struct{}
}

func (g gateExec) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	<-g.release
	return g.fakeExec.Apply(ctx, c, i, by)
}

// A new leader's sweep fails a card the old leader is still applying; the
// apply that ends after it keeps Failed and adds what was done, so the
// card's state never flips twice (W-11, ADR-149, CF-100).
func TestALateApplyKeepsTheSweepsFailedCF100(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	gate := gateExec{fakeExec: w.exec, release: make(chan struct{})}
	w.st.Register(KindKubectl, gate)
	id, err := w.st.Propose(ctx, restart("dev-1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Approve(ctx, id, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	w.clock.add(6 * time.Minute)
	leader := w.open()
	leader.Sweep(ctx)
	if c, _ := leader.Get(ctx, id); c.State != Failed {
		t.Fatalf("the sweep: %+v", c.State)
	}
	close(gate.release)
	c := waitState(t, w.st, id, Failed)
	if len(c.Results) != 1 || !c.Results[0].OK || !strings.Contains(c.Reason, "the console stopped") {
		t.Fatalf("the card %+v", c)
	}
	if last := c.History[len(c.History)-1].What; last != "the actions it was running finished after it was marked failed: applied; it stays failed, and the results are what was done" {
		t.Errorf("history %q", last)
	}
	if log := w.audit.String(); !strings.Contains(log, "the actions finished applied after the card was marked failed, which it stays") {
		t.Errorf("audit %s", log)
	}
}

// A card filed under a key of before (the job and the title) is replaced
// by the same change under its new key: the same cluster, cases and
// commands (W-10, CF-101); another change is filed beside it.
func TestACardKeyedBeforeIsReplacedByTheSameChangeCF101(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	old := restart("dev-1", "job:nightly-audit:dev-1:restart shop/web")
	old.Resolves = []string{"C-4"}
	id, err := w.st.Propose(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	step := []string{cardid.Step(KindKubectl, old.Actions[0].Args)}
	other := restart("dev-1", cardid.Key("dev-1", []string{"C-5"}, step))
	other.Resolves = []string{"C-5"}
	if _, err := w.st.Propose(ctx, other); err != nil {
		t.Fatal(err)
	}
	if c, _ := w.st.Get(ctx, id); c.State != Queued {
		t.Fatalf("replaced by another change: %+v", c.State)
	}
	same := restart("dev-1", cardid.Key("dev-1", []string{"C-4"}, step))
	same.Title, same.Resolves = "Roll web again", []string{"C-4"}
	newer, err := w.st.Propose(ctx, same)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := w.st.Get(ctx, id); c.State != Replaced || c.ReplacedBy != newer {
		t.Fatalf("the card of before: %+v", c)
	}
}
