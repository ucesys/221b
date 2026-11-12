package ai

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/sherlock"
)

// The AIOps skill (IB-10): Sherlock's while the person asks from the
// AIOps area, after its core and before the house instructions; never
// another agent's, and gone when the question comes from elsewhere.
func TestTheAIOpsSkillIsSherlocksInAIOps(t *testing.T) {
	w := setup(t, map[string]string{Sherlock: "Answer in English."})
	skill := AIOpsSkill()
	if !strings.HasPrefix(skill, "AIOps.") || strings.Contains(skill, "<!--") {
		t.Fatalf("the skill's text: %q", skill)
	}
	for _, want := range []string{"Proposed changes", "Jobs › Runs", "Exceptions", "You change nothing in AIOps yourself", "who approved it and when", "Validation rules", "Fixes", "Rebuild now"} {
		if !strings.Contains(skill, want) && !strings.Contains(strings.ToLower(skill), strings.ToLower(want)) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	q := sherlock.Question{Key: sherlock.Key{Cluster: "local"}, Cases: true}
	a, err := w.ai.Agent(Sherlock, q)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(a.Instructions, skill), strings.Index(a.Instructions, "Answer in English.")
	if i < 0 || j < i {
		t.Fatalf("the skill is not in Sherlock's core: %d %d", i, j)
	}
	if a, _ := w.ai.Agent(Sherlock, sherlock.Question{Key: q.Key}); strings.Contains(a.Instructions, skill) {
		t.Error("asked from a cluster's tabs, Sherlock has the AIOps skill")
	}
	if a, _ := w.ai.Agent(Troubleshooter, q); strings.Contains(a.Instructions, skill) {
		t.Error("another agent has the AIOps skill")
	}
}

// The console's names the AIOps skill quotes are the console's own: each
// name of aiops.labels.txt is in the skill as written and is a label in
// the browser app, so a renamed button fails here until the skill
// follows (P-8, S-2).
func TestTheAIOpsSkillNamesWhatTheConsoleShows(t *testing.T) {
	list, err := os.ReadFile("prompts/aiops.labels.txt")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(string(list), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			names = append(names, line)
		}
	}
	if len(names) < 20 {
		t.Fatalf("only %d names", len(names))
	}
	app := webSource(t)
	skill := AIOpsSkill()
	for _, name := range names {
		if !strings.Contains(skill, name) {
			t.Errorf("the skill does not say %q", name)
		}
		// A label may end in ↗, the mark of a link that opens a new tab.
		label := regexp.MustCompile("(['\"`>]\\s*)" + regexp.QuoteMeta(name) + "(\\s*(↗\\s*)?['\"`<{])")
		if !label.MatchString(app) {
			t.Errorf("%q is not a label in web/src", name)
		}
	}
	// The names the review found wrong (S-2) are gone.
	for _, old := range []string{"Open in Watson", "Refuse…", "New Case", "Analyse", "suggest an exception", "sources, Add"} {
		if strings.Contains(skill, old) {
			t.Errorf("the skill still says %q", old)
		}
	}
}

// webSource is the browser app's source without its tests and comments,
// as one text.
func webSource(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	comment := regexp.MustCompile(`(?s:/\*.*?\*/)|(?m:^[ \t]*//[^\n]*)`)
	err := filepath.WalkDir("../../../web/src", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(path, ".test.") || !(strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")) {
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.WriteString(comment.ReplaceAllString(string(text), ""))
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
