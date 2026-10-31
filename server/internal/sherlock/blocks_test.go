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
	// Untouched: a clean block, a non-shell block, no check, an unclosed fence.
	clean := "```sh\nkubectl get nodes\n```"
	for _, in := range []string{clean, "```yaml\na: b | c\n```", "```sh\nkubectl get a | b"} {
		if got := FixBlocks(in, check); got != in {
			t.Fatalf("changed %q to %q", in, got)
		}
	}
	if got := FixBlocks(only, nil); got != only {
		t.Fatal("a nil check changed the text")
	}
}
