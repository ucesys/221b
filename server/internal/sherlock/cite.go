package sherlock

import (
	"regexp"
	"strings"
)

// Ref is an object an answer may cite: a link into the console. The model
// writes it as a Markdown link to 221b://Kind/namespace/name, or
// 221b://Kind/name for a cluster-scoped object.
type Ref struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// URL is the link the model writes for the object.
func (r Ref) URL() string {
	if r.Namespace == "" {
		return "221b://" + r.Kind + "/" + r.Name
	}
	return "221b://" + r.Kind + "/" + r.Namespace + "/" + r.Name
}

func (r Ref) key() string { return strings.ToLower(r.Kind) + "/" + r.Namespace + "/" + r.Name }

// ParseRef reads a 221b:// link.
func ParseRef(u string) (Ref, bool) {
	rest, ok := strings.CutPrefix(u, "221b://")
	if !ok {
		return Ref{}, false
	}
	parts := strings.Split(rest, "/")
	for _, p := range parts {
		if p == "" {
			return Ref{}, false
		}
	}
	switch len(parts) {
	case 2:
		return Ref{Kind: parts[0], Name: parts[1]}, true
	case 3:
		return Ref{Kind: parts[0], Namespace: parts[1], Name: parts[2]}, true
	}
	return Ref{}, false
}

var linkRE = regexp.MustCompile(`\[([^\]\n]{1,300})\]\((221b://[^)\s]{1,400})\)`)

// Known is the set of objects the tools have shown the model: the only
// ones an answer may link to, since only they are known to exist and to
// be visible to the person asking.
type Known map[string]Ref

// Add records objects.
func (k Known) Add(refs ...Ref) {
	for _, r := range refs {
		if r.Kind != "" && r.Name != "" {
			k[r.key()] = r
		}
	}
}

// Has says whether an object was shown.
func (k Known) Has(r Ref) bool {
	_, ok := k[r.key()]
	return ok
}

// Cite checks the links of an answer: a link to an object the tools
// showed stays (with the kind's spelling the tools used); any other
// becomes its plain label. It returns the text and the objects cited.
func Cite(text string, known Known) (string, []Ref) {
	var cited []Ref
	seen := map[string]bool{}
	out := linkRE.ReplaceAllStringFunc(text, func(m string) string {
		sub := linkRE.FindStringSubmatch(m)
		r, ok := ParseRef(sub[2])
		if !ok {
			return sub[1]
		}
		k, ok := known[r.key()]
		if !ok {
			return sub[1]
		}
		if !seen[k.key()] {
			seen[k.key()] = true
			cited = append(cited, k)
		}
		return "[" + sub[1] + "](" + k.URL() + ")"
	})
	return out, cited
}

var askNextRE = regexp.MustCompile(`(?m)^[ \t*_]*Ask next:[ \t*_]*(.+?)[ \t]*$`)

// FollowUps takes the "Ask next:" line off the end of an answer: up to
// three questions, separated by " | ", which the console offers as chips.
func FollowUps(text string) (string, []string) {
	locs := askNextRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text, nil
	}
	last := locs[len(locs)-1]
	if strings.TrimSpace(text[last[1]:]) != "" {
		return text, nil
	}
	var out []string
	for _, q := range strings.Split(text[last[2]:last[3]], "|") {
		q = strings.TrimSpace(strings.Trim(q, "*_ "))
		if q != "" && len(q) <= 200 && len(out) < 3 {
			out = append(out, q)
		}
	}
	return strings.TrimRight(text[:last[0]], " \t\n"), out
}
