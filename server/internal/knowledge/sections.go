package knowledge

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Documents cut by section (phase-13.1-ai, part B, KN-52). A document
// longer than what one item sends Mycroft (itemBytes) is read as its
// sections, each an item of its own with its heading path ("Backups ›
// Restore › From the archive"), so all of it is excerpted, searched and,
// where Mycroft writes pages from its source, read: before, a document was
// one item, sent cut at 24 KiB and kept up to 24 chunks. A Markdown file,
// a web page or a Confluence page (whose text marks its headings with #)
// and a Word document (its Heading styles, docxText) are cut at their
// headings: the largest headings first, and a section still too large at
// the headings under it. A section with no heading under it that is still
// too large, and a document without headings, is cut at paragraphs into
// parts ("part 2"). A document of itemBytes or less stays one item.

// section is one part of a long document.
type section struct {
	// Path is its heading path, outermost first; empty for the text
	// before the first heading.
	Path []string
	// Anchor names it within the document: its path in lower case,
	// unique in the document.
	Anchor string
	Text   string
}

// sectionBytes is the most a section holds: what one item sends Mycroft.
const sectionBytes = itemBytes

var (
	atxHeading   = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	fenceLine    = regexp.MustCompile("^ {0,3}(```|~~~)")
	setextHeader = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
)

// block is a heading and the text under it up to the next heading: level
// 0 is the text before the first heading.
type block struct {
	level int
	title string
	text  string
}

// blocksOf reads a text's headings: ATX (# Title) and setext (a line of
// = or - under a line of text) outside fenced code.
func blocksOf(text string) []block {
	lines := strings.Split(text, "\n")
	out := []block{{}}
	var cur strings.Builder
	flush := func() {
		out[len(out)-1].text = cur.String()
		cur.Reset()
	}
	fenced := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if fenceLine.MatchString(l) {
			fenced = !fenced
		}
		if !fenced {
			if m := atxHeading.FindStringSubmatch(l); m != nil && strings.TrimSpace(m[2]) != "" {
				flush()
				out = append(out, block{level: len(m[1]), title: strings.TrimSpace(m[2])})
				cur.WriteString(l + "\n")
				continue
			}
			if i+1 < len(lines) && strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "- ") && setextHeader.MatchString(lines[i+1]) && len(l) <= 200 {
				level := 1
				if strings.Contains(lines[i+1], "-") {
					level = 2
				}
				flush()
				out = append(out, block{level: level, title: strings.TrimSpace(l)})
				cur.WriteString(l + "\n" + lines[i+1] + "\n")
				i++
				continue
			}
		}
		cur.WriteString(l + "\n")
	}
	flush()
	return out
}

// cutSections cuts a long document into its sections; nil when it is
// short enough to be one item.
func cutSections(text string) []section {
	if len(text) <= sectionBytes {
		return nil
	}
	blocks := blocksOf(text)
	var out []section
	group(blocks, nil, &out)
	// Anchors unique in the document.
	seen := map[string]int{}
	for i := range out {
		a := anchorOf(out[i].Path)
		seen[a]++
		if n := seen[a]; n > 1 {
			a += "-" + strconv.Itoa(n)
		}
		out[i].Anchor = a
	}
	return out
}

// group cuts blocks, the first the node's own text (its heading and what
// is under it before its first subheading), at the largest headings
// among the rest, path being the node's heading path.
func group(blocks []block, path []string, out *[]section) {
	size := 0
	for _, b := range blocks {
		size += len(b.text)
	}
	if size <= sectionBytes {
		emit(path, joinBlocks(blocks), out)
		return
	}
	head, rest := blocks[0], blocks[1:]
	if len(rest) == 0 {
		emitParts(path, head.text, out)
		return
	}
	lvl := rest[0].level
	for _, b := range rest {
		lvl = min(lvl, b.level)
	}
	// Deeper headings before the first of that level are the node's own.
	own := []block{head}
	for len(rest) > 0 && rest[0].level > lvl {
		own = append(own, rest[0])
		rest = rest[1:]
	}
	if t := joinBlocks(own); strings.TrimSpace(t) != "" {
		if len(t) <= sectionBytes {
			emit(path, t, out)
		} else {
			emitParts(path, t, out)
		}
	}
	for len(rest) > 0 {
		j := 1
		for j < len(rest) && rest[j].level > lvl {
			j++
		}
		child := rest[:j]
		rest = rest[j:]
		group(child, append(append([]string(nil), path...), child[0].title), out)
	}
}

func joinBlocks(bs []block) string {
	var b strings.Builder
	for _, x := range bs {
		b.WriteString(x.text)
	}
	return b.String()
}

func emit(path []string, text string, out *[]section) {
	if strings.TrimSpace(text) == "" {
		return
	}
	*out = append(*out, section{Path: path, Text: strings.TrimSpace(text)})
}

// emitParts cuts a text too large for one section at paragraphs (or, a
// paragraph too large, at a sentence or a word) into parts.
func emitParts(path []string, text string, out *[]section) {
	var parts []string
	var cur strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		for len(para) > sectionBytes {
			cut := strings.LastIndexAny(para[:sectionBytes], ".\n ") + 1
			if cut < sectionBytes/2 {
				cut = sectionBytes
				for cut > 0 && (para[cut]&0xC0) == 0x80 {
					cut--
				}
			}
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
			parts = append(parts, para[:cut])
			para = para[cut:]
		}
		if cur.Len()+len(para)+2 > sectionBytes && cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	for i, p := range parts {
		pp := path
		if len(parts) > 1 {
			pp = append(append([]string(nil), path...), "part "+strconv.Itoa(i+1))
		}
		emit(pp, p, out)
	}
}

// anchorOf is a heading path as an anchor: lower-case letters, digits and
// dashes, its headings apart by "/"; "start" for the text before the
// first heading.
func anchorOf(path []string) string {
	if len(path) == 0 {
		return "start"
	}
	parts := make([]string, 0, len(path))
	for _, h := range path {
		var b strings.Builder
		dash := false
		for _, r := range strings.ToLower(h) {
			if isWordRune(r) {
				b.WriteRune(r)
				dash = false
			} else if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		s := strings.Trim(b.String(), "-")
		if len(s) > 60 {
			s = strings.Trim(s[:60], "-")
		}
		if s == "" {
			s = "section"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "/")
}

// headingWords is a heading path as people read it.
func headingWords(path []string) string { return strings.Join(path, " › ") }

// sectionTitle is a section's title: its document's, then its path.
func sectionTitle(doc string, path []string) string {
	if len(path) == 0 {
		return doc
	}
	return doc + " › " + headingWords(path)
}

// sectionID is a section's item id: its document's, then its anchor.
func sectionID(doc, anchor string) string { return doc + "#" + anchor }

// docID is the id of the document an item is: a section's document, or
// the item itself.
func (st *ItemState) docID() string {
	if st.Of != "" {
		return st.Of
	}
	return st.ID
}

// sectionsOf are a document's sections in a manifest, gone ones too.
func sectionsOf(m *Manifest, source, doc string) []*ItemState {
	var out []*ItemState
	for _, st := range m.bySource(source) {
		if st.Of == doc {
			out = append(out, st)
		}
	}
	return out
}

// seeSections marks a document's sections seen with it.
func seeSections(m *Manifest, source, doc string, now time.Time) {
	for _, st := range sectionsOf(m, source, doc) {
		if !st.Gone {
			st.LastSeen = now
		}
	}
}

// readSections keeps a document's sections in the manifest as it was read
// now (secs; none when it is short enough to be one item): it returns the
// sections that changed, to be read, and the keys of those it no longer
// has, whose excerpts are emptied.
func readSections(m *Manifest, x Source, it Item, doc *ItemState, secs []section, rebuild bool, now time.Time) ([]readItem, []string) {
	var read []readItem
	var gone []string
	have := map[string]bool{}
	for _, sec := range secs {
		id := sectionID(it.ID, sec.Anchor)
		key := itemKey(x.ID, id)
		have[key] = true
		st := m.Items[key]
		if st == nil {
			st = &ItemState{Source: x.ID, ID: id, Of: it.ID}
			m.Items[key] = st
		}
		h := hashOf([]byte(sec.Text))
		st.Title, st.Heading, st.URL, st.Modified, st.LastSeen, st.Gone = clip(sectionTitle(it.Title, sec.Path), 300), headingWords(sec.Path), it.URL, it.Modified.UTC(), now, false
		st.Error, st.Skipped, st.Type, st.By = "", "", doc.Type, doc.By
		// A section that reads as it did is not read again, whatever its
		// document's version.
		st.Version = it.Version
		if st.Hash == h && !rebuild {
			continue
		}
		st.Hash, st.LastRead = h, now
		read = append(read, readItem{source: x, item: Item{ID: id, Title: st.Title, Version: it.Version, Modified: it.Modified, URL: it.URL, By: it.By, Corrects: it.Corrects, Type: it.Type, Check: it.Check}, key: key, text: sec.Text, doc: itemKey(x.ID, it.ID)})
	}
	for _, st := range sectionsOf(m, x.ID, it.ID) {
		key := itemKey(x.ID, st.ID)
		if !have[key] && !st.Gone {
			st.Gone, st.Hash, st.Version = true, "", ""
			gone = append(gone, key)
		}
	}
	doc.Sections = len(secs)
	return read, gone
}
