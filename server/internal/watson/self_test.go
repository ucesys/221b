package watson

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/sim"
)

// The simulator's release web/shop has replicas: 3, image.tag, ingress.host,
// db.host and db.password.
const shopValues = " --set replicas=3,image.tag=3.2.1,ingress.host=shop.example.test,db.host=db.internal,db.password=" + sim.PlantedHelmPassword

// A deploy card lists, by key, the values the release has now that the
// deploy would remove or change, and warns when it removes some.
func TestADeployCardListsTheValuesItRemovesAndChanges(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{})
	repo := gitChart(t)
	it := card(t, svc, cl, manager(alice()), "helm upgrade shop --git "+repo+" --path charts/web --ref master -n web --set replicas=5,db.host=db.internal")
	impact := strings.Join(it.Plan.Impact, "\n")
	if !strings.Contains(impact, "Removes 3 values the release has now (helm get values): db.password, image and ingress.") || !strings.Contains(impact, "Changes 1 value the release has now: replicas.") {
		t.Fatalf("%s", impact)
	}
	if strings.Contains(impact, sim.PlantedHelmPassword) || strings.Contains(impact, "shop.example.test") {
		t.Fatalf("a card names values: %s", impact)
	}
	if it.Plan.Tone != ToneWarn || it.Plan.Alert != "" || it.Plan.refusal != "" {
		t.Fatalf("tone %q alert %q refusal %q", it.Plan.Tone, it.Plan.Alert, it.Plan.refusal)
	}

	// The same values remove nothing, and the card does not warn.
	it = card(t, svc, cl, manager(alice()), "helm upgrade shop --git "+repo+" --path charts/web --ref master -n web"+shopValues)
	impact = strings.Join(it.Plan.Impact, "\n")
	if strings.Contains(impact, "helm get values") || strings.Contains(impact, "the release has now") || it.Plan.Tone != "" {
		t.Fatalf("%q %s", it.Plan.Tone, impact)
	}
}

func TestValueKeysAreCapped(t *testing.T) {
	var keys []string
	for i := range 25 {
		keys = append(keys, "k"+string(rune('a'+i)))
	}
	lines := valueImpact(keys, nil)
	if len(lines) != 1 || !strings.Contains(lines[0], "Removes 25 values") || !strings.Contains(lines[0], "and 5 more") || strings.Contains(lines[0], "ky") {
		t.Fatal(lines)
	}
}

// A card for the console's own release says so first, as a danger; one
// that would remove its values is refused at approval, and one that
// keeps them all is not.
func TestTheConsolesOwnReleaseIsADangerAndLosingValuesIsRefused(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{Self: apps.SelfRelease{Cluster: "local", Namespace: "web", Name: "shop"}})
	repo := gitChart(t)
	ctx := context.Background()

	it := card(t, svc, cl, manager(alice()), "helm upgrade shop --git "+repo+" --path charts/web --ref master -n web --set replicas=3")
	if it.Plan.Tone != ToneDanger || it.Plan.Alert != SelfAlert || it.Plan.refusal == "" {
		t.Fatalf("tone %q alert %q refusal %q", it.Plan.Tone, it.Plan.Alert, it.Plan.refusal)
	}
	_, err := svc.Approve(ctx, cl, manager(alice()), it.ID)
	var r *Refusal
	if !errors.As(err, &r) || r.Rule != "own release" || !strings.Contains(r.Message, "removes 3 values from the release that runs this console") {
		t.Fatalf("approving: %v", err)
	}
	if got, _, _ := svc.Snapshot(it.ID, 0); got.Status != StatusPending {
		t.Fatalf("status %s", got.Status)
	}
	in, err := svc.Inspect(ctx, cl, "helm upgrade shop --git "+repo+" --path charts/web --ref master -n web --set replicas=3", nil)
	if err != nil || in.Refusal == "" || len(in.Impact) == 0 || in.Impact[0] != SelfAlert {
		t.Fatalf("a durable card: %+v %v", in, err)
	}
	if done, err := svc.RunApproved(cl, manager(alice()), in.Line, "Agent proposal", "test", &in); err == nil || done.Status != StatusRefused {
		t.Fatalf("a durable card ran: %v", err)
	}

	// Another release of the same name elsewhere is not the console's.
	if tone, alert, _ := svc.cardAlert("local", mustParse(t, "helm uninstall shop -n other"), nil); tone != "" || alert != "" {
		t.Fatalf("%q %q", tone, alert)
	}
	if tone, alert, refusal := svc.cardAlert("other-cluster", mustParse(t, "helm rollback shop 1 -n web"), nil); tone != "" || alert != "" || refusal != "" {
		t.Fatalf("%q %q", tone, alert)
	}

	// A rollback of it is a danger, and may be approved.
	it = card(t, svc, cl, manager(alice()), "helm rollback shop 1 -n web")
	if it.Plan.Tone != ToneDanger || it.Plan.Alert != SelfAlert || it.Plan.refusal != "" {
		t.Fatalf("rollback: tone %q alert %q refusal %q", it.Plan.Tone, it.Plan.Alert, it.Plan.refusal)
	}

	// An upgrade that keeps every value is a danger, and may be approved.
	it = card(t, svc, cl, manager(alice()), "helm upgrade shop --git "+repo+" --path charts/web --ref master -n web"+shopValues)
	if it.Plan.Tone != ToneDanger || it.Plan.refusal != "" {
		t.Fatalf("complete values: tone %q refusal %q %v", it.Plan.Tone, it.Plan.refusal, it.Plan.Impact)
	}
	if _, err := svc.Approve(ctx, cl, manager(alice()), it.ID); err != nil {
		t.Fatalf("approving a complete upgrade: %v", err)
	}
	wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
}

func mustParse(t *testing.T, line string) *Command {
	t.Helper()
	c, err := Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A durable card's run works out the card again, and refuses an upgrade
// that removes values from the console's own release even when its caller
// hands it an inspection without the refusal, as the Inbox did (review of
// November 24, 2026, finding 7).
func TestADurableCardFindsTheOwnReleaseRefusalItself(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{Self: apps.SelfRelease{Cluster: "local", Namespace: "web", Name: "shop"}})
	repo := gitChart(t)
	line := "helm upgrade shop --git " + repo + " --path charts/web --ref master -n web --set replicas=3"
	in, err := svc.Inspect(context.Background(), cl, line, nil)
	if err != nil || in.Refusal == "" {
		t.Fatalf("inspected %+v %v", in, err)
	}
	// What the Inbox keeps of a card: its targets and impact, no refusal.
	kept := Inspection{Line: in.Line, Targets: in.Targets, Impact: in.Impact, Fingerprint: in.Fingerprint}
	it, err := svc.RunApproved(cl, manager(alice()), in.Line, "Agent proposal", "test", &kept)
	var r *Refusal
	if !errors.As(err, &r) || r.Rule != "own release" || it == nil || it.Status != StatusRefused {
		t.Fatalf("a durable card ran: %v %+v", err, it)
	}
	if _, err := svc.RunApproved(cl, manager(alice()), in.Line, "Agent proposal", "test", nil); !errors.As(err, &r) || r.Rule != "own release" {
		t.Fatalf("without an inspection: %v", err)
	}
}
