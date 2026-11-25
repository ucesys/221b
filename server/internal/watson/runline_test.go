package watson

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/kube"
)

// An Automation dry run of a line that ends in a comment changes nothing:
// the dry run is the parsed command's, not text after the line, which the
// comment would swallow (review of November 24, 2026, finding 1).
func TestAnAutomationDryRunOfALineWithACommentChangesNothing(t *testing.T) {
	svc, cl, s, _, _ := setup(t, Options{})
	var out bytes.Buffer
	code, err := svc.RunLine(context.Background(), cl, kube.Person{}, "kubectl cordon worker-003 # patching", true, &out)
	if err != nil || code != 0 {
		t.Fatalf("dry run: %d %v %q", code, err, out.String())
	}
	if !strings.Contains(out.String(), "(server dry run)") {
		t.Fatalf("not a dry run: %q", out.String())
	}
	if s.Node("worker-003").Spec.Unschedulable {
		t.Fatal("a dry run cordoned the node")
	}
	// A dry run on a read-only connection still runs: it changes nothing.
	ro := cl
	ro.ReadOnly = true
	out.Reset()
	if code, err := svc.RunLine(context.Background(), ro, kube.Person{}, "kubectl cordon worker-003 # patching", true, &out); err != nil || code != 0 {
		t.Fatalf("read-only dry run: %d %v", code, err)
	}
	// The real run of the same line is refused there.
	if _, err := svc.RunLine(context.Background(), ro, kube.Person{}, "kubectl cordon worker-003 # patching", false, &out); err == nil {
		t.Fatal("a change ran on a read-only connection")
	}
	if s.Node("worker-003").Spec.Unschedulable {
		t.Fatal("cordoned")
	}
	// A line that asks for no dry run (--dry-run=none) is made one.
	out.Reset()
	if code, err := svc.RunLine(context.Background(), cl, kube.Person{}, "kubectl cordon worker-003 --dry-run=none # x", true, &out); err != nil || code != 0 || s.Node("worker-003").Spec.Unschedulable {
		t.Fatalf("--dry-run=none: %d %v %q", code, err, out.String())
	}
}
