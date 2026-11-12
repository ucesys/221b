package ai

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// sourceRow is a row of docs/prompt-sources.md.
type sourceRow struct{ commit, license string }

// sourceRows reads the table under "## Sources".
func sourceRows(doc string) map[string]sourceRow {
	rows := map[string]sourceRow{}
	in := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(line) == "## Sources"
			continue
		}
		if !in || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 6 {
			continue
		}
		name := strings.TrimSpace(cells[1])
		if name == "Source" || strings.Trim(name, "- ") == "" {
			continue
		}
		rows[name] = sourceRow{commit: strings.TrimSpace(cells[3]), license: strings.TrimSpace(cells[4])}
	}
	return rows
}

var sourceComment = regexp.MustCompile(`<!--.*source:\s*(.*?)\s*-->`)

// checkSources is the CI check of the design's Prompts and sources, step
// 5, as hack/check-prompt-sources.sh makes it: what it finds wrong.
func checkSources(doc string, files map[string]string) []string {
	var out []string
	rows := sourceRows(doc)
	if len(rows) == 0 {
		out = append(out, "no table of sources")
	}
	for name, r := range rows {
		switch strings.ToLower(r.license) {
		case "", "-", "—", "to be recorded", "unknown":
			out = append(out, name+" names no license")
		}
		if r.commit == "" {
			out = append(out, name+" names no commit")
		}
	}
	for file, text := range files {
		for _, m := range sourceComment.FindAllStringSubmatch(text, -1) {
			r, ok := rows[m[1]]
			switch {
			case !ok:
				out = append(out, file+": "+m[1]+" has no row")
			case r.commit == "to be recorded":
				out = append(out, file+": "+m[1]+"'s commit is to be recorded")
			}
		}
	}
	return out
}

// Every source: comment in a prompt file names a row of
// docs/prompt-sources.md, and every row has a license (the design's
// Prompts and sources, step 5). No network: the files only.
func TestEveryPromptSourceIsRecorded(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/prompt-sources.md")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	err = fs.WalkDir(prompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := prompts.ReadFile(path)
		files[path] = string(b)
		return err
	})
	if err != nil || len(files) < len(Catalog) {
		t.Fatalf("the prompt files: %d, %v", len(files), err)
	}
	if bad := checkSources(string(doc), files); len(bad) > 0 {
		t.Fatalf("docs/prompt-sources.md and the prompts disagree:\n%s", strings.Join(bad, "\n"))
	}
	if len(sourceRows(string(doc))) < 7 {
		t.Fatal("the design's seven sources are not all recorded")
	}
	// The check catches what it is for.
	row := "## Sources\n\n| Source | Repository | Commit | License | What |\n| --- | --- | --- | --- | --- |\n| A | r | abc123 | MIT | x |\n| B | r | to be recorded |  | x |\n"
	bad := strings.Join(checkSources(row, map[string]string{"p.md": "<!-- source: A -->\n<!-- source: C -->\n<!-- source: B -->"}), "\n")
	for _, want := range []string{"B names no license", "C has no row", "B's commit is to be recorded"} {
		if !strings.Contains(bad, want) {
			t.Errorf("the check missed %q: %s", want, bad)
		}
	}
	if strings.Contains(bad, "p.md: A") {
		t.Errorf("a recorded source was refused: %s", bad)
	}
}

// A prompt file's comments, such as its source: comments, never reach a
// model.
func TestPromptCommentsStayOutOfThePrompt(t *testing.T) {
	if strings.Contains(SetupSkill(), "<!--") || strings.Contains(Core(Sherlock, true), "<!--") {
		t.Fatal("a comment reached the instructions")
	}
	if got := htmlComment.ReplaceAllString("a\n<!-- source: A -->\nb <!-- x\ny -->c", ""); got != "a\nb c" {
		t.Fatalf("%q", got)
	}
}
