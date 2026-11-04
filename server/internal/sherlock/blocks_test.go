package sherlock

import (
	"errors"
	"strings"
	"testing"
)

// A shell block keeps only what Watson runs; the rest goes to a text
// block with the reason, for the person (asked October 30, 2026).
func TestFixBlocksKeepsWatsonToWhatItRuns(t *testing.T) {
	check := func(line string) error {
		if strings.Contains(line, "|") {
			return errors.New("Watson has no shell: | is not run.")
		}
		if strings.HasPrefix(line, "kubectl exec") {
			return errors.New("kubectl exec is not supported.")
		}
		return nil
	}
	in := "Look here:\n\n```sh\nkubectl get pods -n web\nkubectl logs api-0 -n web | grep OOM\n# a comment\nkubectl exec -it api-0 -- sh\n```\n\nThen:\n\n```text\nkubectl logs x | grep y\n```\n"
	got := FixBlocks(in, check)
	want := "Look here:\n\n```sh\nkubectl get pods -n web\n# a comment\n```\n\nWatson cannot run these; run them yourself:\n\n```text\n# Watson has no shell: | is not run.\nkubectl logs api-0 -n web | grep OOM\n# kubectl exec is not supported.\nkubectl exec -it api-0 -- sh\n```\n\nThen:\n\n```text\nkubectl logs x | grep y\n```\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Nothing runnable left: no shell block at all.
	only := "```bash\nkubectl logs a | grep b\n```"
	if got := FixBlocks(only, check); strings.Contains(got, "```bash") || !strings.Contains(got, "```text") {
		t.Fatalf("got:\n%s", got)
	}
	// Untouched: a clean block, a non-shell block, no check, an unclosed clean fence.
	clean := "```sh\nkubectl get nodes\n```"
	for _, in := range []string{clean, "```yaml\na: b | c\n```", "```sh\nkubectl get a"} {
		if got := FixBlocks(in, check); got != in {
			t.Fatalf("changed %q to %q", in, got)
		}
	}
	if got := FixBlocks(only, nil); got != only {
		t.Fatal("a nil check changed the text")
	}
}

func refuseThePipe(line string) error {
	if strings.Contains(line, "|") {
		return errors.New("Watson does not run pipes.")
	}
	return nil
}

// A closed block of another language ends at its own fence: the fence is
// not read as a new block, so what follows stays where it was.
func TestFixBlocksLeavesOtherBlocksAlone(t *testing.T) {
	for _, in := range []string{
		"```yaml\na: b\n```\n\n```sh\nkubectl apply -n web\n```",
		"```yaml\nkind: A\n```\n\n```yaml\ndata:\n  check.sh: |\n    kubectl get pods -A | grep -v Running\n```",
		"```text\nkubectl logs a | grep b\n```\n\nand then\n\n```text\nkubectl logs c | grep d\n```",
	} {
		if got := FixBlocks(in, refuseThePipe); got != in {
			t.Errorf("changed:\n%s\n--- to ---\n%s", in, got)
		}
	}
}

// A command is what Watson reads: lines joined by a backslash are judged
// joined, and go or stay together.
func TestFixBlocksReadsJoinedLinesAsOneCommand(t *testing.T) {
	fine := "```sh\nkubectl scale deployment/api -n web \\\n  --replicas=3\n```"
	if got := FixBlocks(fine, refuseThePipe); got != fine {
		t.Fatalf("a command Watson runs was torn:\n%s", got)
	}
	got := FixBlocks("```sh\nkubectl logs api-0 -n web \\\n  | grep OOM\n```", refuseThePipe)
	if strings.Contains(got, "```sh") || !strings.Contains(got, "```text") || !strings.Contains(got, "| grep OOM") {
		t.Fatalf("a refused joined command stayed:\n%s", got)
	}
}

// The page reads tilde fences and runs an unclosed block to the end, and
// offers Send to Watson there: the check sees what the page sees.
func TestFixBlocksReadsFencesAsThePageDoes(t *testing.T) {
	for _, in := range []string{
		"~~~sh\nkubectl logs api-0 -n web | grep OOM\n~~~",
		"```sh\nkubectl logs api-0 -n web | grep OOM\n~~~",
		"Run:\n\n```sh\nkubectl logs api-0 -n web | grep OOM",
	} {
		got := FixBlocks(in, refuseThePipe)
		if !strings.Contains(got, "```text") || strings.Contains(got, "sh\nkubectl") {
			t.Errorf("not moved:\n%s\n--- to ---\n%s", in, got)
		}
	}
}

// A report that no later text replaced is the answer: Stop, a failure or
// the round limit must not leave the message empty.
func TestTheNoteStandsWhenNoTextFollows(t *testing.T) {
	if got := answerOr("", "the report"); got != "the report" {
		t.Fatalf("got %q", got)
	}
	if got := answerOr("  \n", "the report"); got != "the report" {
		t.Fatalf("got %q", got)
	}
	if got := answerOr("the answer", "the report"); got != "the answer" {
		t.Fatalf("got %q", got)
	}
}
