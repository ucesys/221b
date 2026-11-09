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
// for Mycroft; Markdown writes the console's markdown as one for the
// publisher.

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

// Escape makes text safe inside storage-format XHTML.
func Escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

var (
	inlineCode = regexp.MustCompile("`([^`]+)`")
	bold       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
)

// inline writes one line of markdown's inline marks: code, bold and
// links to web addresses; everything else is text.
func inline(s string) string {
	s = Escape(s)
	s = inlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = bold.ReplaceAllString(s, "<strong>$1</strong>")
	s = mdLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	return s
}

// Markdown writes markdown as storage format: headings, paragraphs,
// lists, fenced code (as a code macro) and tables of pipes. It reads the
// markdown the wiki holds, not every markdown there is.
func Markdown(md string) string {
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var para []string
	list := ""
	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>")
			para = nil
		}
	}
	endList := func() {
		if list != "" {
			b.WriteString("</" + list + ">")
			list = ""
		}
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			flushPara()
			endList()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			body := strings.ReplaceAll(strings.Join(code, "\n"), "]]>", "]]]]><![CDATA[>")
			b.WriteString(`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[` + body + `]]></ac:plain-text-body></ac:structured-macro>`)
		case t == "":
			flushPara()
			endList()
		case strings.HasPrefix(t, "#"):
			flushPara()
			endList()
			n := len(t) - len(strings.TrimLeft(t, "#"))
			if n > 6 {
				n = 6
			}
			level := string(rune('0' + n))
			b.WriteString("<h" + level + ">" + inline(strings.TrimSpace(t[n:])) + "</h" + level + ">")
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			flushPara()
			if list != "ul" {
				endList()
				b.WriteString("<ul>")
				list = "ul"
			}
			b.WriteString("<li>" + inline(t[2:]) + "</li>")
		case orderedItem.MatchString(t):
			flushPara()
			if list != "ol" {
				endList()
				b.WriteString("<ol>")
				list = "ol"
			}
			b.WriteString("<li>" + inline(orderedItem.ReplaceAllString(t, "")) + "</li>")
		case strings.HasPrefix(t, "|"):
			flushPara()
			endList()
			b.WriteString("<table><tbody>")
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				row := strings.Trim(strings.TrimSpace(lines[i]), "|")
				if strings.Trim(row, "-:| ") == "" {
					continue
				}
				b.WriteString("<tr>")
				for _, c := range strings.Split(row, "|") {
					b.WriteString("<td>" + inline(strings.TrimSpace(c)) + "</td>")
				}
				b.WriteString("</tr>")
			}
			i--
			b.WriteString("</tbody></table>")
		default:
			endList()
			para = append(para, t)
		}
	}
	flushPara()
	endList()
	return b.String()
}

var orderedItem = regexp.MustCompile(`^\d+[.)]\s+`)
