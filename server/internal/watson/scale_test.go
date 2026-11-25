package watson

import (
	"strings"
	"testing"
)

// kubectl scale takes a count of 0 or more, as kubectl does; a huge
// negative one is refused before any card, which would otherwise name a pod
// for each step down (review of November 24, 2026, finding 2).
func TestScaleTakesOnlyACountKubectlTakes(t *testing.T) {
	for _, line := range []string{
		"kubectl scale statefulset/db -n web --replicas=-2000000000",
		"kubectl scale statefulset/db -n web --replicas=-1",
		"kubectl scale statefulset/db -n web --replicas=three",
		"kubectl scale statefulset/db -n web --replicas=1.5",
		"kubectl scale statefulset/db -n web --replicas=9223372036854775807",
	} {
		if _, err := Parse(line); err == nil || !strings.Contains(err.Error(), "--replicas") {
			t.Errorf("%s: %v", line, err)
		}
	}
	for line, want := range map[string]int64{
		"kubectl scale sts/db -n web --replicas=0":    0,
		"kubectl scale sts/db -n web --replicas=12":   12,
		"kubectl scale sts/db -n web --replicas 010":  8,
		"kubectl scale sts/db -n web --replicas=0x10": 16,
	} {
		c, err := Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if n, _ := replicasOf(c); n != want {
			t.Errorf("%s: %d, want %d", line, n, want)
		}
	}
	if got := ordinalsDown("db", 3, 0, 4); got != "db-2, db-1 and db-0" {
		t.Errorf("3 to 0: %q", got)
	}
	if got := ordinalsDown("db", 2147483647, 0, 4); got != "db-2147483646, db-2147483645, db-2147483644, db-2147483643 and 2147483643 more" {
		t.Errorf("a huge count: %q", got)
	}
}

// rollout undo's --to-revision is read as kubectl reads it, base 0, on the
// card too (review of November 24, 2026, cut by the cap: impact.go:807).
func TestToRevisionIsReadAsKubectlReadsIt(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	got := card(t, svc, cl, alice(), "kubectl rollout undo statefulset/cache -n web --to-revision=010")
	if text := strings.Join(got.Plan.Impact, " "); !strings.Contains(text, "revision 8") {
		t.Fatalf("impact %v", got.Plan.Impact)
	}
	if _, err := Parse("kubectl rollout undo deploy/api -n web --to-revision=two"); err == nil {
		t.Fatal("a revision that is not a number")
	}
}
