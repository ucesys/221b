package knowledge

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Documents are read as text: markdown and plain text as they are, Word
// (.docx: a zip of XML) by its paragraphs, HTML by its text, CSV by its
// rows (MY-05), and PDF by the text its pages draw with simple fonts. A
// PDF whose fonts map their glyphs elsewhere (most CID fonts) gives
// little or nothing, and says so.

// MaxDocument bounds an uploaded document, and maxText the text kept of
// one.
const (
	MaxDocument = 20 << 20
	maxText     = 1 << 20
)

// textKinds are the extensions read as plain text.
var textKinds = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".text": true, ".rst": true, ".adoc": true}

// Readable says whether a file's name is one of a kind the console reads.
func Readable(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return textKinds[ext] || ext == ".docx" || ext == ".pdf" || ext == ".html" || ext == ".htm" || ext == ".csv"
}

// ErrNoText is a document that holds no text the console can read.
var ErrNoText = errors.New("no text could be read from it")

// Extract reads a document's text by its name's extension.
func Extract(name string, b []byte) (string, error) {
	if len(b) > MaxDocument {
		return "", invalid("%s is over %d MiB", name, MaxDocument>>20)
	}
	var text string
	var err error
	switch ext := strings.ToLower(path.Ext(name)); {
	case textKinds[ext]:
		if !utf8.Valid(b) {
			return "", invalid("%s is not UTF-8 text", name)
		}
		text = string(b)
	case ext == ".docx":
		text, err = docxText(b)
	case ext == ".pdf":
		text, err = pdfText(b)
	case ext == ".html" || ext == ".htm":
		text = htmlText(string(b))
	case ext == ".csv":
		text, err = csvText(b)
	default:
		return "", invalid("%s is not a document the console reads: markdown, text, Word (.docx), PDF, HTML or CSV", name)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return "", fmt.Errorf("%s: %w", name, ErrNoText)
	}
	return clip(text, maxText), nil
}

// csvText reads a CSV file's rows, one per line, the cells apart by
// " | ", so a row reads as a line of a table; a file that is not quite CSV
// (quotes left open, rows of different lengths) is read as it is, as text.
func csvText(b []byte) (string, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return "", errors.New("it is not UTF-8 text")
	}
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var out strings.Builder
	for out.Len() < maxText {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return string(b), nil
		}
		for i := range row {
			row[i] = strings.Join(strings.Fields(row[i]), " ")
		}
		line := strings.TrimRight(strings.Join(row, " | "), " |")
		if line != "" {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	return out.String(), nil
}

// docxText reads a Word document's body: its paragraphs, one per line,
// tables' cells apart by tabs.
func docxText(b []byte) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", errors.New("it is not a Word document (.docx)")
	}
	var doc *zip.File
	for _, f := range z.File {
		if f.Name == "word/document.xml" {
			doc = f
		}
	}
	if doc == nil {
		return "", errors.New("it is not a Word document: word/document.xml is missing")
	}
	if doc.UncompressedSize64 > 64<<20 {
		return "", errors.New("its text is too large")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	d := xml.NewDecoder(io.LimitReader(rc, 64<<20))
	var out strings.Builder
	inText := false
	for out.Len() < maxText {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("its XML cannot be read: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				out.WriteByte('\t')
			case "br", "cr":
				out.WriteByte('\n')
			case "tc":
				if s := out.String(); s != "" && !strings.HasSuffix(s, "\n") {
					out.WriteByte('\t')
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				out.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				out.Write(t)
			}
		}
	}
	return out.String(), nil
}

// The PDF reader: every stream is inflated when it is Flate-encoded, and
// the text-showing operators (Tj, TJ, ', ") of its content are read, with
// a line between text blocks and moves to a new line.
var (
	streamStart = regexp.MustCompile(`stream\r?\n`)
	skipStream  = regexp.MustCompile(`/Subtype\s*/Image|/Type\s*/(XRef|ObjStm|XObject|Metadata)|/Length1|/FontFile`)
)

func pdfText(b []byte) (string, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(b, "\x00\t\r\n "), []byte("%PDF")) {
		return "", errors.New("it is not a PDF")
	}
	var out strings.Builder
	rest := b
	for out.Len() < maxText {
		loc := streamStart.FindIndex(rest)
		if loc == nil {
			break
		}
		end := bytes.Index(rest[loc[1]:], []byte("endstream"))
		if end < 0 {
			break
		}
		dictStart := bytes.LastIndex(rest[:loc[0]], []byte("obj"))
		if dictStart < 0 {
			dictStart = 0
		}
		dict := rest[dictStart:loc[0]]
		data := rest[loc[1] : loc[1]+end]
		rest = rest[loc[1]+end+len("endstream"):]
		if skipStream.Match(dict) {
			continue
		}
		if bytes.Contains(dict, []byte("/FlateDecode")) {
			zr, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				continue
			}
			inflated, err := io.ReadAll(io.LimitReader(zr, 32<<20))
			zr.Close()
			if err != nil && len(inflated) == 0 {
				continue
			}
			data = inflated
		} else if bytes.Contains(dict, []byte("/Filter")) {
			// Another encoding: not text the console reads.
			continue
		}
		showText(data, &out)
	}
	return out.String(), nil
}

// showText reads one content stream's text.
func showText(data []byte, out *strings.Builder) {
	var operands []string
	inBlock := false
	line := func() {
		if s := out.String(); s != "" && !strings.HasSuffix(s, "\n") {
			out.WriteByte('\n')
		}
	}
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '(':
			s, n := literal(data[i:])
			operands = append(operands, s)
			i += n
		case c == '<' && i+1 < len(data) && data[i+1] != '<':
			s, n := hexString(data[i:])
			operands = append(operands, s)
			i += n
		case c == '[':
			// An array of TJ: its strings, with the numbers between them
			// as spacing; a large one is a space.
			j := i + 1
			var b strings.Builder
			for j < len(data) && data[j] != ']' {
				switch data[j] {
				case '(':
					s, n := literal(data[j:])
					b.WriteString(s)
					j += n
				case '<':
					s, n := hexString(data[j:])
					b.WriteString(s)
					j += n
				default:
					k := j
					for k < len(data) && (data[k] == '-' || data[k] == '.' || (data[k] >= '0' && data[k] <= '9')) {
						k++
					}
					if k > j {
						if num := string(data[j:k]); strings.HasPrefix(num, "-") && len(num) > 3 {
							b.WriteByte(' ')
						}
						j = k
					} else {
						j++
					}
				}
			}
			operands = append(operands, b.String())
			i = j + 1
		case c == '%':
			for i < len(data) && data[i] != '\n' && data[i] != '\r' {
				i++
			}
		case isDelim(c) || c == ' ' || c == '\n' || c == '\r' || c == '\t':
			i++
		default:
			j := i
			for j < len(data) && !isDelim(data[j]) && data[j] != ' ' && data[j] != '\n' && data[j] != '\r' && data[j] != '\t' {
				j++
			}
			op := string(data[i:j])
			i = j
			switch op {
			case "BT":
				inBlock = true
			case "ET":
				inBlock = false
				line()
			case "Tj", "TJ":
				if inBlock && len(operands) > 0 {
					out.WriteString(operands[len(operands)-1])
				}
			case "'", "\"":
				line()
				if inBlock && len(operands) > 0 {
					out.WriteString(operands[len(operands)-1])
				}
			case "T*", "Td", "TD", "Tm":
				line()
			}
			if !isOperand(op) {
				operands = operands[:0]
			}
		}
		if out.Len() > maxText {
			return
		}
	}
}

func isDelim(c byte) bool {
	return c == '(' || c == ')' || c == '<' || c == '>' || c == '[' || c == ']' || c == '{' || c == '}' || c == '/' || c == '%'
}

// isOperand says whether a token is a number or a name rather than an
// operator.
func isOperand(tok string) bool {
	if tok == "" {
		return true
	}
	c := tok[0]
	return c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9') || tok == "true" || tok == "false" || tok == "null"
}

// literal reads a (string) with its escapes and balanced parentheses, and
// returns it with how many bytes it took.
func literal(data []byte) (string, int) {
	var b strings.Builder
	depth := 0
	i := 0
	for ; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '\\' && i+1 < len(data):
			i++
			switch e := data[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 'r', 'b', 'f':
			case 't':
				b.WriteByte('\t')
			case '\r', '\n':
			default:
				if e >= '0' && e <= '7' {
					v, n := 0, 0
					for n < 3 && i < len(data) && data[i] >= '0' && data[i] <= '7' {
						v = v*8 + int(data[i]-'0')
						i++
						n++
					}
					i--
					writeByte(&b, byte(v))
				} else {
					b.WriteByte(e)
				}
			}
		case c == '(':
			if depth > 0 {
				b.WriteByte(c)
			}
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return b.String(), i + 1
			}
			b.WriteByte(c)
		default:
			if depth > 0 {
				writeByte(&b, c)
			}
		}
	}
	return b.String(), i
}

// writeByte writes a byte of a simple font's text: ASCII as it is, the
// Latin-1 upper half as its character.
func writeByte(b *strings.Builder, c byte) {
	switch {
	case c >= 0x20 && c < 0x7f, c == '\n', c == '\t':
		b.WriteByte(c)
	case c >= 0xa0:
		b.WriteRune(rune(c))
	}
}

// hexString reads a <hex> string; bytes outside text are left out.
func hexString(data []byte) (string, int) {
	end := bytes.IndexByte(data, '>')
	if end < 0 {
		return "", len(data)
	}
	h := strings.Map(func(r rune) rune {
		if strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return r
		}
		return -1
	}, string(data[1:end]))
	if len(h)%2 == 1 {
		h += "0"
	}
	var b strings.Builder
	for i := 0; i+1 < len(h); i += 2 {
		var v byte
		fmt.Sscanf(h[i:i+2], "%02x", &v)
		writeByte(&b, v)
	}
	return b.String(), end + 1
}
