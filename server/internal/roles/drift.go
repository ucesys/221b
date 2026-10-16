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

// Grants expands rules to one entry for each group, resource, name and verb.
// A wildcard stays a wildcard, so a rule with "*" never matches one that
// lists what it covers: a cluster that widens a role is always reported.
func Grants(rules []Rule) map[string]bool {
	out := map[string]bool{}
	for _, r := range rules {
		names := r.ResourceNames
		if len(names) == 0 {
			names = []string{""}
		}
		if len(r.NonResourceURLs) > 0 {
			for _, u := range r.NonResourceURLs {
				for _, v := range r.Verbs {
					out[fmt.Sprintf("%s on %s", v, u)] = true
				}
			}
		}
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, n := range names {
					for _, v := range r.Verbs {
						out[grant(v, g, res, n)] = true
					}
				}
			}
		}
	}
	return out
}

func grant(verb, group, resource, name string) string {
	what := resource
	if group != "" {
		what += "." + group
	}
	if name != "" {
		what += " " + name
	}
	return verb + " " + what
}

// Compare says how a deployed role differs from the shipped one.
func Compare(shipped, deployed []Rule) Difference {
	s, d := Grants(shipped), Grants(deployed)
	var out Difference
	for g := range d {
		if !s[g] {
			out.Extra = append(out.Extra, g)
		}
	}
	for g := range s {
		if !d[g] {
			out.Missing = append(out.Missing, g)
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
