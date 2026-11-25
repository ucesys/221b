package roles

import (
	"fmt"
	"slices"
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
		} else if slices.Contains(names, "") {
			// "" in a list allows only the requests that name no object
			// (list, watch, create), not any object as no list does.
			names = slices.Clone(names)
			for i, n := range names {
				if n == "" {
					names[i] = NoName
				}
			}
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

// NoName stands for "" in a rule's resourceNames: the requests that name
// no object.
const NoName = "(no name)"

// resourceCovers says whether a rule's resource h matches g as the API
// server reads it: equal, "*", or "*/sub" for that subresource of any kind.
func resourceCovers(h, g string) bool {
	if h == g || h == "*" {
		return true
	}
	if sub, ok := strings.CutPrefix(h, "*/"); ok {
		_, gsub, has := strings.Cut(g, "/")
		return has && gsub == sub
	}
	return false
}

// urlCovers says whether a rule's URL h matches g: equal, "*", or a prefix
// ending in "*"; "/*" is every URL, as every URL starts with /.
func urlCovers(h, g string) bool {
	if h == g || h == "*" || h == "/*" {
		return true
	}
	return strings.HasSuffix(h, "*") && strings.HasPrefix(g, strings.TrimSuffix(h, "*"))
}

// covers says whether the grant h allows everything g does: equal, or with a
// wildcard where g has a value, or for any object where g names one.
func (h Grant) covers(g Grant) bool {
	if h.URL != "" || g.URL != "" {
		return h.URL != "" && g.URL != "" && (h.Verb == g.Verb || h.Verb == "*") && urlCovers(h.URL, g.URL)
	}
	return (h.Verb == g.Verb || h.Verb == "*") && (h.Group == g.Group || h.Group == "*") && resourceCovers(h.Resource, g.Resource) && (h.Name == "" || h.Name == g.Name)
}

// grantRule is a grant as a rule of one of each.
func grantRule(g Grant) Rule {
	if g.URL != "" {
		return Rule{NonResourceURLs: []string{g.URL}, Verbs: []string{g.Verb}}
	}
	r := Rule{APIGroups: []string{g.Group}, Resources: []string{g.Resource}, Verbs: []string{g.Verb}}
	if g.Name != "" {
		r.ResourceNames = []string{g.Name}
	}
	return r
}

func covered(set []Grant, g Grant) bool {
	for _, h := range set {
		if h.covers(g) {
			return true
		}
	}
	return false
}

// maxRuleGrants is how many grants one deployed rule is expanded to, at
// most (RB-36). A rule's grants are the product of its lists, so a few
// kilobytes of rule (100 groups, resources and names, 10 verbs) would be
// ten million grants, and the check runs for every cluster: such a rule is
// compared whole instead (ruleCovers), never expanded.
const maxRuleGrants = 4096

// ruleSize is how many grants a rule expands to, stopping past max.
func ruleSize(r Rule, max int) int {
	names := max1(len(r.ResourceNames))
	n := len(r.NonResourceURLs) * len(r.Verbs)
	p := 1
	for _, k := range []int{len(r.APIGroups), len(r.Resources), names, len(r.Verbs)} {
		if k == 0 {
			return n
		}
		if p > (max+1)/k+1 {
			return max + 1
		}
		p *= k
	}
	return n + p
}

func max1(n int) int {
	if n == 0 {
		return 1
	}
	return n
}

// ruleCovers says whether the rule h allows every grant of r, list by
// list, without expanding either: each verb, group and resource of r is in
// h's or h has "*", h names no object or every object r names, and h's URLs
// cover r's. It can miss a rule that several of h's rules cover together,
// which then reads as extra: the safe side for strict.
func ruleCovers(h, r Rule) bool {
	in := func(have []string, want []string) bool {
		if slices.Contains(have, "*") {
			return true
		}
		for _, w := range want {
			if !slices.Contains(have, w) {
				return false
			}
		}
		return true
	}
	if !in(h.Verbs, r.Verbs) {
		return false
	}
	if len(r.APIGroups) > 0 && len(r.Resources) > 0 {
		if !in(h.APIGroups, r.APIGroups) {
			return false
		}
		for _, res := range r.Resources {
			if !slices.ContainsFunc(h.Resources, func(hr string) bool { return resourceCovers(hr, res) }) {
				return false
			}
		}
		if len(h.ResourceNames) > 0 && (len(r.ResourceNames) == 0 || !in(h.ResourceNames, r.ResourceNames)) {
			return false
		}
	}
	for _, u := range r.NonResourceURLs {
		ok := false
		for _, hu := range h.NonResourceURLs {
			if urlCovers(hu, u) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// oversized names a rule too large to compare grant by grant.
func oversized(r Rule) string {
	return fmt.Sprintf("a rule of %d API groups, %d resources, %d object names and %d verbs, too large to compare grant by grant", len(r.APIGroups), len(r.Resources), len(r.ResourceNames), len(r.Verbs))
}

// Compare says how a deployed role differs from the shipped one. A grant is
// extra when nothing the shipped role has covers it, so a wildcard on the
// cluster where the definition lists verbs is always reported, and a
// read-only role next to a full definition is only missing grants. A
// deployed rule over maxRuleGrants is compared whole: extra unless one
// shipped rule covers it, and covering what it lists.
func Compare(shipped, deployed []Rule) Difference {
	s := Grants(shipped)
	var d []Grant
	var big []Rule
	for _, r := range deployed {
		if ruleSize(r, maxRuleGrants) > maxRuleGrants {
			big = append(big, r)
			continue
		}
		d = append(d, Grants([]Rule{r})...)
	}
	var out Difference
	seen := map[string]bool{}
	for _, g := range d {
		if str := g.String(); !seen[str] && !covered(s, g) {
			seen[str] = true
			out.Extra = append(out.Extra, str)
		}
	}
	for _, r := range big {
		if !slices.ContainsFunc(shipped, func(h Rule) bool { return ruleCovers(h, r) }) {
			if str := oversized(r); !seen[str] {
				seen[str] = true
				out.Extra = append(out.Extra, str)
			}
		}
	}
	for _, g := range s {
		if str := g.String(); !seen["-"+str] && !covered(d, g) && !slices.ContainsFunc(big, func(h Rule) bool { return ruleCovers(h, grantRule(g)) }) {
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
