package automation

import (
	"slices"
	"sort"
	"strconv"
)

// The kinds of thing a variable change alters.
const (
	ItemValue      = "value"
	ItemNodeValue  = "node_value"
	ItemCeiling    = "ceiling"
	ItemSensitive  = "sensitive"
	ItemReboot     = "reboot_minutes"
	ItemNodeReboot = "node_reboot_minutes"
)

// ChangeItem is one thing a variable change alters: a fleet's value, a
// node's own value, the ceiling, whether a name is sensitive, or a reboot
// time. Old and New are nil where there is none: a value not set, a name
// not marked, no reboot time of its own.
//
// A change is what it alters, not the fleet's variables as a whole
// (decided with the owner on November 19, 2026): approving it compares
// and sets what its items name, and leaves the rest as it is then.
type ChangeItem struct {
	Kind string  `json:"kind"`
	Node string  `json:"node,omitempty"`
	Name string  `json:"name,omitempty"`
	Old  *string `json:"old,omitempty"`
	New  *string `json:"new,omitempty"`
}

func same(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func text(s string) *string { return &s }

// Items is what differs between a fleet's variables before and after, in
// a fixed order. Before and after may be the fleet's variables whole, as
// a ConfigMap keeps a change, or only what the change alters.
func Items(before, after FleetVariables) []ChangeItem {
	var out []ChangeItem
	values := func(kind, node string, a, b map[string]string) {
		names := map[string]bool{}
		for k := range a {
			names[k] = true
		}
		for k := range b {
			names[k] = true
		}
		for _, name := range slices.Sorted(mapKeys(names)) {
			it := ChangeItem{Kind: kind, Node: node, Name: name}
			if v, ok := a[name]; ok {
				it.Old = text(v)
			}
			if v, ok := b[name]; ok {
				it.New = text(v)
			}
			if !same(it.Old, it.New) {
				out = append(out, it)
			}
		}
	}
	minutes := func(m int) *string {
		if m <= 0 {
			return nil
		}
		return text(strconv.Itoa(m))
	}
	values(ItemValue, "", before.Values, after.Values)
	if a, b := before.CeilingOrDefault(), after.CeilingOrDefault(); a != b {
		out = append(out, ChangeItem{Kind: ItemCeiling, Old: text(a), New: text(b)})
	}
	marks := map[string]bool{}
	for _, n := range before.Sensitive {
		marks[n] = true
	}
	for _, n := range after.Sensitive {
		marks[n] = true
	}
	for _, name := range slices.Sorted(mapKeys(marks)) {
		was, is := slices.Contains(before.Sensitive, name), slices.Contains(after.Sensitive, name)
		if was == is {
			continue
		}
		it := ChangeItem{Kind: ItemSensitive, Name: name}
		if was {
			it.Old = text("true")
		} else {
			it.New = text("true")
		}
		out = append(out, it)
	}
	if before.RebootMinutes != after.RebootMinutes {
		out = append(out, ChangeItem{Kind: ItemReboot, Old: minutes(before.RebootMinutes), New: minutes(after.RebootMinutes)})
	}
	for _, node := range NamedNodes(before, after) {
		values(ItemNodeValue, node, before.Nodes[node], after.Nodes[node])
		if a, b := before.NodeReboot[node], after.NodeReboot[node]; a != b {
			out = append(out, ChangeItem{Kind: ItemNodeReboot, Node: node, Old: minutes(a), New: minutes(b)})
		}
	}
	return out
}

func mapKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Sides are a change's items as variables before and after: only what the
// change alters, which is what a change read from the database has.
func Sides(items []ChangeItem) (before, after FleetVariables) {
	set := func(fp *FleetVariables, it ChangeItem, v *string) {
		if v == nil {
			return
		}
		switch it.Kind {
		case ItemValue:
			if fp.Values == nil {
				fp.Values = map[string]string{}
			}
			fp.Values[it.Name] = *v
		case ItemNodeValue:
			if fp.Nodes == nil {
				fp.Nodes = map[string]map[string]string{}
			}
			if fp.Nodes[it.Node] == nil {
				fp.Nodes[it.Node] = map[string]string{}
			}
			fp.Nodes[it.Node][it.Name] = *v
		case ItemCeiling:
			fp.Ceiling = *v
		case ItemSensitive:
			fp.Sensitive = append(fp.Sensitive, it.Name)
		case ItemReboot:
			fp.RebootMinutes, _ = strconv.Atoi(*v)
		case ItemNodeReboot:
			if fp.NodeReboot == nil {
				fp.NodeReboot = map[string]int{}
			}
			fp.NodeReboot[it.Node], _ = strconv.Atoi(*v)
		}
	}
	for _, it := range items {
		set(&before, it, it.Old)
		set(&after, it, it.New)
	}
	return before, after
}

// now is what the variables have where an item points.
func (it ChangeItem) now(fp FleetVariables) *string {
	minutes := func(m int) *string {
		if m <= 0 {
			return nil
		}
		return text(strconv.Itoa(m))
	}
	switch it.Kind {
	case ItemValue:
		if v, ok := fp.Values[it.Name]; ok {
			return &v
		}
	case ItemNodeValue:
		if v, ok := fp.Nodes[it.Node][it.Name]; ok {
			return &v
		}
	case ItemCeiling:
		return text(fp.CeilingOrDefault())
	case ItemSensitive:
		if slices.Contains(fp.Sensitive, it.Name) {
			return text("true")
		}
	case ItemReboot:
		return minutes(fp.RebootMinutes)
	case ItemNodeReboot:
		return minutes(fp.NodeReboot[it.Node])
	}
	return nil
}

// what names an item for a person.
func (it ChangeItem) what() string {
	switch it.Kind {
	case ItemNodeValue:
		return it.Name + " of " + it.Node
	case ItemCeiling:
		return "the privilege ceiling"
	case ItemSensitive:
		return "the sensitive mark of " + it.Name
	case ItemReboot:
		return "the reboot timeout"
	case ItemNodeReboot:
		return "the reboot timeout of " + it.Node
	}
	return it.Name
}

// weigh compares the variables as they are now with a change's items:
// done says every item is as the change leaves it, and moved names the
// items that are neither as the change found them nor as it leaves them.
func weigh(cur FleetVariables, items []ChangeItem) (done bool, moved []string) {
	done = true
	for _, it := range items {
		at := it.now(cur)
		if same(at, it.New) {
			continue
		}
		done = false
		if !same(at, it.Old) {
			moved = append(moved, it.what())
		}
	}
	sort.Strings(moved)
	return done, moved
}

// applyItems sets what a change's items name, and leaves the rest of the
// variables as they are.
func applyItems(cur FleetVariables, items []ChangeItem) FleetVariables {
	out := cur
	out.Values = map[string]string{}
	for k, v := range cur.Values {
		out.Values[k] = v
	}
	out.Nodes = map[string]map[string]string{}
	for n, m := range cur.Nodes {
		out.Nodes[n] = map[string]string{}
		for k, v := range m {
			out.Nodes[n][k] = v
		}
	}
	out.NodeReboot = map[string]int{}
	for n, m := range cur.NodeReboot {
		out.NodeReboot[n] = m
	}
	out.Sensitive = append([]string(nil), cur.Sensitive...)
	number := func(v *string) int {
		if v == nil {
			return 0
		}
		n, _ := strconv.Atoi(*v)
		return n
	}
	for _, it := range items {
		switch it.Kind {
		case ItemValue:
			if it.New == nil {
				delete(out.Values, it.Name)
			} else {
				out.Values[it.Name] = *it.New
			}
		case ItemNodeValue:
			if it.New == nil {
				delete(out.Nodes[it.Node], it.Name)
			} else {
				if out.Nodes[it.Node] == nil {
					out.Nodes[it.Node] = map[string]string{}
				}
				out.Nodes[it.Node][it.Name] = *it.New
			}
		case ItemCeiling:
			if it.New != nil {
				out.Ceiling = *it.New
			}
		case ItemSensitive:
			out.Sensitive = slices.DeleteFunc(out.Sensitive, func(n string) bool { return n == it.Name })
			if it.New != nil {
				out.Sensitive = append(out.Sensitive, it.Name)
			}
		case ItemReboot:
			out.RebootMinutes = number(it.New)
		case ItemNodeReboot:
			if m := number(it.New); m > 0 {
				out.NodeReboot[it.Node] = m
			} else {
				delete(out.NodeReboot, it.Node)
			}
		}
	}
	for n, m := range out.Nodes {
		if len(m) == 0 {
			delete(out.Nodes, n)
		}
	}
	if len(out.Values) == 0 {
		out.Values = nil
	}
	if len(out.Nodes) == 0 {
		out.Nodes = nil
	}
	if len(out.NodeReboot) == 0 {
		out.NodeReboot = nil
	}
	if len(out.Sensitive) == 0 {
		out.Sensitive = nil
	}
	return out
}
