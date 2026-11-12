package knowledge

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// The wiki's layout, the same in git and S3: pages/<kind>/<name>.md, the
// manifest of what was read, and the scrubbed excerpts of the sources.
const (
	PagesDir     = "pages/"
	ManifestPath = "manifest.json"
	ExcerptsDir  = "excerpts/"
)

// Page kinds: one page per system, application, runbook, incident, best
// practice and decision.
var PageKinds = []string{"system", "application", "runbook", "incident", "practice", "decision"}

// criticalKinds need two people to approve: the best practices the
// Auditor enforces and the runbooks used in fixes.
var criticalKinds = map[string]bool{"practice": true, "runbook": true}

var pagePath = regexp.MustCompile(`^pages/([a-z]+)/([a-z0-9][a-z0-9._-]{0,99})\.md$`)

// ValidPagePath says whether p is a page's path, and of a known kind.
func ValidPagePath(p string) bool {
	m := pagePath.FindStringSubmatch(p)
	if m == nil {
		return false
	}
	for _, k := range PageKinds {
		if k == m[1] {
			return true
		}
	}
	return false
}

// File is one file of the wiki: its path, its content, and its version in
// the store (a git blob's hash, an object's ETag).
type File struct {
	Path    string `json:"path"`
	Body    []byte `json:"-"`
	Version string `json:"version,omitempty"`
}

// SourceRef is a source a page draws on, as the page names it.
type SourceRef struct {
	// Key is the manifest's key: "<source>:<item>".
	Key     string    `json:"key"`
	Title   string    `json:"title"`
	Version string    `json:"version,omitempty"`
	Date    time.Time `json:"date,omitzero"`
	URL     string    `json:"url,omitempty"`
}

// Page is a wiki page: its front matter and its markdown.
type Page struct {
	Path     string      `json:"path"`
	Title    string      `json:"title"`
	Kind     string      `json:"kind"`
	Critical bool        `json:"critical,omitempty"`
	Sources  []SourceRef `json:"sources,omitempty"`
	Updated  time.Time   `json:"updated,omitzero"`
	// Body is the markdown after the front matter.
	Body string `json:"body,omitempty"`
	// Stale says no source of the page was seen for the stale days.
	Stale bool `json:"stale,omitempty"`
	// Version is the store's.
	Version string `json:"version,omitempty"`
	// Retired, when set, says the page is retired (KN-24): out of the
	// index, and why.
	Retired *Retirement `json:"retired,omitempty"`
}

type frontMatter struct {
	Title    string      `json:"title"`
	Kind     string      `json:"kind"`
	Critical bool        `json:"critical,omitempty"`
	Sources  []SourceRef `json:"sources,omitempty"`
	Updated  time.Time   `json:"updated,omitzero"`
}

// Format writes a page as its file: YAML front matter between --- lines,
// then the markdown.
func (p Page) Format() []byte {
	fm, _ := yaml.Marshal(frontMatter{Title: p.Title, Kind: p.Kind, Critical: p.Critical, Sources: p.Sources, Updated: p.Updated.UTC()})
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(fm)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(p.Body))
	b.WriteString("\n")
	return b.Bytes()
}

var firstHeading = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// ParsePage reads a page's file. A page people wrote without front matter
// takes its title from its first heading, or its name.
func ParsePage(f File) Page {
	p := Page{Path: f.Path, Version: f.Version}
	text := strings.ReplaceAll(string(f.Body), "\r\n", "\n")
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if fm, body, ok := strings.Cut(rest, "\n---\n"); ok {
			var m frontMatter
			if yaml.Unmarshal([]byte(fm), &m) == nil {
				p.Title, p.Kind, p.Critical, p.Sources, p.Updated = m.Title, m.Kind, m.Critical, m.Sources, m.Updated
				text = body
			}
		}
	}
	p.Body = strings.TrimSpace(text)
	if m := pagePath.FindStringSubmatch(f.Path); m != nil {
		if p.Kind == "" {
			p.Kind = m[1]
		}
		if p.Title == "" {
			if h := firstHeading.FindStringSubmatch(p.Body); h != nil {
				p.Title = strings.TrimSpace(h[1])
			} else {
				p.Title = strings.ReplaceAll(m[2], "-", " ")
			}
		}
	}
	if p.Title == "" {
		p.Title = strings.TrimSuffix(path.Base(f.Path), ".md")
	}
	if criticalKinds[p.Kind] {
		p.Critical = true
	}
	return p
}
