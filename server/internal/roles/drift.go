package roles

import (
	"fmt"
	"sort"
	"strings"
)

// Difference is what a deployed role has against the definition shipped
// with the console (RB-36).
type Difference struct {
	// Extra are grants the cluster has and the definition does not.
	Extra []string
	// Missing are grants the definition has and the cluster does not.
	Missing []string
}

// Empty reports whether the two allow the same.
func (d Difference) Empty() bool { return len(d.Extra) == 0 && len(d.Missing) == 0 }

// MoreThanShipped reports whether the cluster allows something the shipped
// definition does not, which is what rbac.enforce: strict refuses on.
func (d Difference) MoreThanShipped() bool { return len(d.Extra) > 0 }

// Lines say the difference in words, extra grants first.
func (d Difference) Lines() []string {
	var out []string
	for _, e := range d.Extra {
		out = append(out, "allows "+e+", which the shipped role does not")
	}
	for _, m := range d.Missing {
		out = append(out, "lacks "+m+", which the shipped role has")
	}
	return out
}

// Grant is one thing a rule allows: a verb on a resource of a group, for any
// object or one by name; or a verb on a non-resource URL.
type Grant struct {
	Verb, Group, Resource, Name, URL string
}

// String is the grant in words: "delete pods", "get on /healthz".
func (g Grant) String() string {
	if g.URL != "" {
		return g.Verb + " on " + g.URL
	}
	what := g.Resource
	if g.Group != "" {
		what += "." + g.Group
	}
	if g.Name != "" {
		what += " " + g.Name
	}
	return g.Verb + " " + what
}

// Grants expands rules to one grant for each group, resource, name and verb.
func Grants(rules []Rule) []Grant {
	var out []Grant
	for _, r := range rules {
		for _, u := range r.NonResourceURLs {
			for _, v := range r.Verbs {
				out = append(out, Grant{Verb: v, URL: u})
			}
		}
		names := r.ResourceNames
		if len(names) == 0 {
			names = []string{""}
		}
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, n := range names {
					for _, v := range r.Verbs {
						out = append(out, Grant{Verb: v, Group: g, Resource: res, Name: n})
					}
				}
			}
		}
	}
	return out
}

// covers says whether the grant h allows everything g does: equal, or with a
// wildcard where g has a value, or for any object where g names one.
func (h Grant) covers(g Grant) bool {
	if h.URL != "" || g.URL != "" {
		return h.URL != "" && g.URL != "" && (h.Verb == g.Verb || h.Verb == "*") && (h.URL == g.URL || h.URL == "*" || (strings.HasSuffix(h.URL, "*") && strings.HasPrefix(g.URL, strings.TrimSuffix(h.URL, "*"))))
	}
	return (h.Verb == g.Verb || h.Verb == "*") && (h.Group == g.Group || h.Group == "*") && (h.Resource == g.Resource || h.Resource == "*") && (h.Name == "" || h.Name == g.Name)
}

func covered(set []Grant, g Grant) bool {
	for _, h := range set {
		if h.covers(g) {
			return true
		}
	}
	return false
}

// Compare says how a deployed role differs from the shipped one. A grant is
// extra when nothing the shipped role has covers it, so a wildcard on the
// cluster where the definition lists verbs is always reported, and a
// read-only role next to a full definition is only missing grants.
func Compare(shipped, deployed []Rule) Difference {
	s, d := Grants(shipped), Grants(deployed)
	var out Difference
	seen := map[string]bool{}
	for _, g := range d {
		if str := g.String(); !seen[str] && !covered(s, g) {
			seen[str] = true
			out.Extra = append(out.Extra, str)
		}
	}
	for _, g := range s {
		if str := g.String(); !seen["-"+str] && !covered(d, g) {
			seen["-"+str] = true
			out.Missing = append(out.Missing, str)
		}
	}
	sort.Strings(out.Extra)
	sort.Strings(out.Missing)
	return out
}

// Summary shortens a long list for a message: the first few and a count.
func Summary(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, "; ")
	}
	return strings.Join(items[:max], "; ") + fmt.Sprintf("; and %d more", len(items)-max)
}
