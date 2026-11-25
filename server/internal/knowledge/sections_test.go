package knowledge

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// prose is n bytes or so of paragraphs.
func prose(word string, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString("The " + word + " service keeps its state on the shared volume and restarts in order.\n\n")
	}
	return b.String()
}

// A long document is cut at its largest headings, a section still too
// large at the headings under it, and one with none at paragraphs; a short
// one is not cut (KN-52).
func TestALongDocumentIsCutBySection(t *testing.T) {
	if got := cutSections("# Small\n\nShort."); got != nil {
		t.Fatalf("a short document is one item: %+v", got)
	}
	doc := "Before any heading.\n\n# Guide\n\nIntro.\n\n## Alpha\n\n" + prose("alpha", 15<<10) +
		"## Beta\n\n" + prose("beta", 15<<10) + "```\n# not a heading\n```\n\n" +
		"## Gamma\n\n" + prose("gamma", 10<<10) + "### Gamma one\n\n" + prose("gammaone", 10<<10) + "### Gamma two\n\n" + prose("gammatwo", 10<<10) +
		"## Delta\n\n" + prose("delta", 60<<10)
	secs := cutSections(doc)
	var paths []string
	total := 0
	for _, s := range secs {
		paths = append(paths, headingWords(s.Path)+" #"+s.Anchor)
		if len(s.Text) > sectionBytes {
			t.Errorf("%s holds %d bytes", headingWords(s.Path), len(s.Text))
		}
		total += len(s.Text)
	}
	want := []string{
		" #start",
		"Guide #guide",
		"Guide › Alpha #guide/alpha",
		"Guide › Beta #guide/beta",
		"Guide › Gamma #guide/gamma",
		"Guide › Gamma › Gamma one #guide/gamma/gamma-one",
		"Guide › Gamma › Gamma two #guide/gamma/gamma-two",
		"Guide › Delta › part 1 #guide/delta/part-1",
		"Guide › Delta › part 2 #guide/delta/part-2",
		"Guide › Delta › part 3 #guide/delta/part-3",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sections:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
	if total < len(doc)-200 {
		t.Errorf("the sections hold %d of the document's %d bytes", total, len(doc))
	}
	if !strings.Contains(secs[3].Text, "# not a heading") {
		t.Errorf("a fenced line is not a heading: %q", secs[3].Text[len(secs[3].Text)-80:])
	}
	// Setext headings, and anchors unique.
	secs = cutSections("Setup\n=====\n\n" + prose("a", 20<<10) + "Setup\n-----\n\n" + prose("b", 20<<10))
	if len(secs) != 2 || secs[0].Anchor != "setup" || secs[1].Anchor != "setup/setup" {
		t.Fatalf("setext: %+v", len(secs))
	}
	secs = cutSections("## Notes\n\n" + prose("a", 20<<10) + "## Notes\n\n" + prose("b", 20<<10))
	if len(secs) != 2 || secs[0].Anchor != "notes" || secs[1].Anchor != "notes-2" {
		t.Fatalf("anchors: %q %q", secs[0].Anchor, secs[1].Anchor)
	}
	// A Word document's headings are marked as Markdown's.
	text, err := Extract("plan.docx", docxStyled(t, [][2]string{{"Title", "Plan"}, {"", "Intro."}, {"Heading2", "Restore"}, {"", "Steps."}}))
	if err != nil || !strings.Contains(text, "# Plan") || !strings.Contains(text, "## Restore") {
		t.Fatalf("docx headings: %q, %v", text, err)
	}
}

// waitSync waits for the background sync to end, and gives it.
func waitSync(t *testing.T, f *fixture, after time.Time) *SyncResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := f.s.Status(ctx)
		if st.Last != nil && !st.Running && !st.Last.Started.Before(after) {
			return st.Last
		}
		if time.Now().After(deadline) {
			t.Fatal("the sync did not end")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// All of a long document is read and searched, section by section; a
// change reads the sections that changed; removing it removes them
// (KN-52).
func TestALongDocumentIsReadAndSearchedWhole(t *testing.T) {
	f := newFixture(t)
	doc := "# Guide\n\nIntro.\n\n## Alpha\n\n" + prose("alpha", 20<<10) + "## Beta\n\n" + prose("beta", 20<<10) + "## Gamma\n\n" + prose("gamma", 20<<10) + "The zebrafish rotation runs on Sundays.\n"
	start := f.clock.now()
	if _, err := f.s.Upload(ctx, admin, "documents", "guide.md", []byte(doc)); err != nil {
		t.Fatal(err)
	}
	res := waitSync(t, f, start)
	if res.Read != 1 || res.Run == "" {
		t.Fatalf("the document was read: %+v", res)
	}
	f.s.mu.RLock()
	parent := f.s.manifest.Items[itemKey("documents", "guide.md")]
	secs := sectionsOf(f.s.manifest, "documents", "guide.md")
	f.s.mu.RUnlock()
	if parent == nil || parent.Sections != 4 || len(secs) != 4 {
		t.Fatalf("its sections: %+v, %d", parent, len(secs))
	}
	hits := f.s.Search("zebrafish rotation", "excerpt", 3)
	if len(hits) != 1 || hits[0].Title != "guide.md › Guide › Gamma" || !strings.Contains(hits[0].Text, "zebrafish") {
		t.Fatalf("the last section is searched: %+v", hits)
	}
	sent := strings.Join(f.model.calls(), "\n")
	if !strings.Contains(sent, "zebrafish") || !strings.Contains(sent, `key="documents:guide.md#guide/gamma"`) {
		t.Fatal("Mycroft did not read the last section")
	}
	if docs := f.s.Documents("documents"); len(docs) != 1 {
		t.Fatalf("the document is listed once: %+v", docs)
	}
	// One section changed: only it is read again.
	calls := len(f.model.calls())
	f.clock.add(time.Hour)
	start = f.clock.now()
	if _, err := f.s.Upload(ctx, admin, "documents", "guide.md", []byte(strings.Replace(doc, "Sundays", "Mondays", 1))); err != nil {
		t.Fatal(err)
	}
	waitSync(t, f, start)
	again := f.model.calls()[calls:]
	if len(again) != 1 || strings.Count(again[0], "<source ") != 1 || !strings.Contains(again[0], "Mondays") {
		t.Fatalf("the changed section alone was read again: %d calls", len(again))
	}
	if err := f.s.RemoveDocument(ctx, admin, "documents", "guide.md"); err != nil {
		t.Fatal(err)
	}
	if hits := f.s.Search("zebrafish rotation", "excerpt", 3); len(hits) != 0 {
		t.Fatalf("a removed document's sections: %+v", hits)
	}
}

// docxStyled is a Word document of paragraphs, each with its style.
func docxStyled(t *testing.T, paras [][2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("word/document.xml")
	var x strings.Builder
	x.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paras {
		x.WriteString(`<w:p>`)
		if p[0] != "" {
			x.WriteString(`<w:pPr><w:pStyle w:val="` + p[0] + `"/></w:pPr>`)
		}
		x.WriteString(`<w:r><w:t>` + p[1] + `</w:t></w:r></w:p>`)
	}
	x.WriteString(`</w:body></w:document>`)
	_, _ = io.WriteString(w, x.String())
	_ = z.Close()
	return b.Bytes()
}
