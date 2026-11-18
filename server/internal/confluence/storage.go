package confluence

import (
	"encoding/xml"
	"errors"
	"html"
	"io"
	"regexp"
	"strings"
)

// Confluence keeps a page's body in its storage format: XHTML with its own
// elements (ac:structured-macro, ri:page, …). Text reads one as plain text
// for Mycroft.

// blockElements end a line of text.
var blockElements = map[string]bool{"p": true, "div": true, "br": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"li": true, "tr": true, "pre": true, "blockquote": true, "table": true, "ul": true, "ol": true, "hr": true}

// Text is a storage-format body as plain text: one line per paragraph,
// heading, list item and table row, cells apart by " | ", headings marked
// with #, and every macro's parameters left out. Markup it cannot read is
// skipped, never an error.
func Text(storage string) string {
	d := xml.NewDecoder(strings.NewReader("<root>" + storage + "</root>"))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	var b strings.Builder
	skip := 0
	cell := false
	for {
		tok, err := d.Token()
		if err != nil {
			if !errors.Is(err, io.EOF) && b.Len() == 0 {
				return strings.TrimSpace(stripTags(storage))
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			switch {
			case name == "parameter" || name == "placeholder":
				skip++
			case blockElements[name]:
				newline(&b)
				if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
					b.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
				}
				if name == "li" {
					b.WriteString("- ")
				}
				cell = false
			case name == "td" || name == "th":
				if cell {
					b.WriteString(" | ")
				}
				cell = true
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if name == "parameter" || name == "placeholder" {
				skip--
			} else if blockElements[name] {
				newline(&b)
			}
		case xml.CharData:
			if skip > 0 {
				continue
			}
			s := strings.Join(strings.Fields(string(t)), " ")
			if s == "" {
				continue
			}
			if last := b.String(); last != "" && !strings.HasSuffix(last, "\n") && !strings.HasSuffix(last, " ") && !strings.HasSuffix(last, "- ") {
				b.WriteByte(' ')
			}
			b.WriteString(s)
		}
	}
	lines := strings.Split(b.String(), "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimRight(l, " "); strings.TrimSpace(l) != "" && l != "-" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func newline(b *strings.Builder) {
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteByte('\n')
	}
}

var tags = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string { return html.UnescapeString(tags.ReplaceAllString(s, " ")) }
