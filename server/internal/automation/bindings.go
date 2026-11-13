package automation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// --- Run values bound to a variable (FU-04) ---

// BindPrefix starts a run value that binds a parameter to a variable by
// name: var:NAME gives each node its own value of NAME, worked out when the
// run starts, as secret:namespace/name/key names a Secret. Only a run's
// own values bind; a fleet's or a node's value is always text.
const BindPrefix = "var:"

// bindRE is a binding: var: and a variable's name, nothing around them.
var bindRE = regexp.MustCompile(`^var:([A-Z_][A-Z0-9_]{0,63})$`)

// escapedRE is a constant that starts like a binding, written with one
// backslash more in front: \var:x is the text var:x, \\var:x is \var:x.
var escapedRE = regexp.MustCompile(`^\\+var:`)

// Bound says which variable a run value binds to.
func Bound(v string) (string, bool) {
	m := bindRE.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// BindValue is the run value that binds to a variable.
func BindValue(name string) string { return BindPrefix + name }

// Constant is the text a run value that binds nothing stands for: one
// leading backslash of an escaped var: is dropped, anything else is as
// typed.
func Constant(v string) string {
	if escapedRE.MatchString(v) {
		return v[1:]
	}
	return v
}

// EscapeConstant is the run value for a text used as it is, which never
// binds: a text that starts like a binding gets a backslash in front.
func EscapeConstant(text string) string {
	if strings.HasPrefix(text, BindPrefix) || escapedRE.MatchString(text) {
		return `\` + text
	}
	return text
}

// checkBindings refuses a run value that starts with var: and names no
// variable, so a typo never runs as text.
func checkBindings(m map[string]string) error {
	names := make([]string, 0, len(m))
	for k, v := range m {
		if strings.HasPrefix(v, BindPrefix) {
			if _, ok := Bound(v); !ok {
				names = append(names, k)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf("%w: %s: a value that starts with var: binds to a variable, as var:NAME (capitals, digits and underscores); for the text itself, write \\var:…", ErrInvalid, strings.Join(names, ", "))
}

// BindingGaps are a run's bindings that do not reach every node: for each
// parameter bound to a variable, the nodes that have no value of it, and
// the parameters whose variable no node of the run has, which stops the
// run whether or not the parameter is required.
func BindingGaps(h Header, run map[string]string, resolved map[string]Resolved) (unset map[string][]string, none []string) {
	unset = map[string][]string{}
	for _, p := range h.Params {
		v, ok := run[p.Name]
		if !ok || p.Secret {
			continue
		}
		name, ok := Bound(v)
		if !ok {
			continue
		}
		set := 0
		for node, r := range resolved {
			if _, ok := r.Values[p.Name]; ok {
				set++
			} else if !conflicted(r, name) {
				unset[p.Name] = append(unset[p.Name], node)
			}
		}
		sort.Strings(unset[p.Name])
		if set == 0 && len(resolved) > 0 && len(unset[p.Name]) == len(resolved) {
			none = append(none, fmt.Sprintf("%s is bound to variable %s, which no node of this run has", p.Name, name))
		}
	}
	for k, v := range unset {
		if len(v) == 0 {
			delete(unset, k)
		}
	}
	sort.Strings(none)
	return unset, none
}

func conflicted(r Resolved, name string) bool {
	for _, c := range r.Conflicts {
		if c.Param == name {
			return true
		}
	}
	return false
}
