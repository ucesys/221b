package roles

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Shipped is what the chart installs, written to files/roles.json from the
// definitions in this package (RB-01, RB-29). The chart reads it and cannot
// say anything the console does not, and a test fails when the file and
// the definitions differ.
type Shipped struct {
	Nodes Role               `json:"nodes"`
	Roles map[ID]ShippedRole `json:"roles"`
}

// ShippedRole is one role in the file.
type ShippedRole struct {
	ClusterRole string `json:"clusterRole"`
	// Bound roles are bound one namespace at a time, with RoleBindings;
	// the others are bound once, with a ClusterRoleBinding.
	Bound bool `json:"bound"`
	// Group is the reserved group a role held in 221B is bound to.
	Group string `json:"group,omitempty"`
	// Templates has one entry per definition the role has.
	Templates map[Template]ShippedRules `json:"templates"`
}

// ShippedRules are a definition's rules; SecretRules are added when
// secrets.read is on.
type ShippedRules struct {
	Rules       []Rule `json:"rules"`
	SecretRules []Rule `json:"secretRules,omitempty"`
}

// Ship builds the file's content.
func Ship() Shipped {
	out := Shipped{Nodes: Nodes(), Roles: map[ID]ShippedRole{}}
	for _, id := range All {
		sr := ShippedRole{ClusterRole: id.ClusterRole(), Bound: id.Bound(), Group: SuperGroup(id), Templates: map[Template]ShippedRules{}}
		for _, t := range []Template{Full, ReadOnly} {
			if (id == Viewer || id == SuperViewer) && t == Full {
				continue
			}
			plain := Definition(id, t, Options{}).Rules
			with := Definition(id, t, Options{Secrets: true}).Rules
			var extra []Rule
			for _, r := range with {
				if !contains(plain, r) {
					extra = append(extra, r)
				}
			}
			sr.Templates[t] = ShippedRules{Rules: plain, SecretRules: extra}
		}
		out.Roles[id] = sr
	}
	return out
}

func contains(rules []Rule, r Rule) bool {
	for _, x := range rules {
		if reflect.DeepEqual(x, r) {
			return true
		}
	}
	return false
}

// JSON is the content of files/roles.json, indented, ending in a newline.
func JSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Ship()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
