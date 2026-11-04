package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// Parameter limits (AU-06).
const (
	MaxValue          = 1 << 10
	MaxParams         = 100
	maxVariablesBytes = 64 << 10
	SourceRun         = "run"
	SourceNode        = "node"
	SourceFleet       = "fleet"
	SourceMissing     = ""
	defaultCeiling    = PrivilegeNobody
	maxNodeOverride   = 500
	// MaxRebootMinutes is the longest a node may be given to come back
	// from a reboot.
	MaxRebootMinutes = 240
)

// FleetVariables are a fleet's parameter values, its nodes' overrides and the
// most privilege a run on it may get.
type FleetVariables struct {
	Values map[string]string            `json:"values,omitempty"`
	Nodes  map[string]map[string]string `json:"nodes,omitempty"`
	// Ceiling is none or full; empty is none, so a fleet runs nothing
	// elevated until someone raises it.
	Ceiling string `json:"ceiling,omitempty"`
	// Sensitive are parameters whose change always needs approval.
	Sensitive []string `json:"sensitive,omitempty"`
	// RebootMinutes is how long the fleet's nodes may take to come back
	// from a reboot, 0 for the console's default; NodeReboot sets it for
	// single nodes, and wins over any fleet's.
	RebootMinutes int            `json:"rebootMinutes,omitempty"`
	NodeReboot    map[string]int `json:"nodeReboot,omitempty"`
}

// Empty says whether nothing is set.
func (fp FleetVariables) Empty() bool {
	return len(fp.Values) == 0 && len(fp.Nodes) == 0 && fp.Ceiling == "" && len(fp.Sensitive) == 0 && fp.RebootMinutes == 0 && len(fp.NodeReboot) == 0
}

// Same says whether two fleets' parameters say the same.
func (fp FleetVariables) Same(o FleetVariables) bool {
	norm := func(x FleetVariables) string {
		nodes := map[string]map[string]string{}
		for n, m := range x.Nodes {
			if len(m) > 0 {
				nodes[n] = m
			}
		}
		x.Nodes = nodes
		x.Ceiling = x.CeilingOrDefault()
		x.Sensitive = slices.Sorted(slices.Values(x.Sensitive))
		b, _ := json.Marshal(x)
		return string(b)
	}
	return norm(fp) == norm(o)
}

// RebootChanged says whether the reboot timeouts differ.
func RebootChanged(a, b FleetVariables) bool {
	if a.RebootMinutes != b.RebootMinutes || len(a.NodeReboot) != len(b.NodeReboot) {
		return true
	}
	for n, m := range a.NodeReboot {
		if b.NodeReboot[n] != m {
			return true
		}
	}
	return false
}

// NodeCeiling is the ceiling a node runs under, and the fleet it comes
// from: the strictest of the fleets it is in, or All nodes' when it is in
// none.
func NodeCeiling(layers []Layer) (ceiling, from string) {
	for _, l := range layers {
		if l.Default {
			continue
		}
		if c := l.Params.CeilingOrDefault(); from == "" || (c == PrivilegeNobody && ceiling != PrivilegeNobody) {
			ceiling, from = c, l.Name
		}
	}
	if from != "" {
		return ceiling, from
	}
	for _, l := range layers {
		if l.Default {
			return l.Params.CeilingOrDefault(), l.Name
		}
	}
	return defaultCeiling, "All nodes"
}

// RebootMinutes is how long a node may take to come back from a reboot: a
// time set for the node itself wins over its fleets'; where several set
// one, the longest. 0 means none is set.
func RebootMinutes(layers []Layer, node string) int {
	own, fleet := 0, 0
	for _, l := range layers {
		own = max(own, l.Params.NodeReboot[node])
		fleet = max(fleet, l.Params.RebootMinutes)
	}
	if own > 0 {
		return own
	}
	return fleet
}

// CeilingOrDefault is the fleet's ceiling, none when unset.
func (fp FleetVariables) CeilingOrDefault() string {
	if fp.Ceiling == "" {
		return defaultCeiling
	}
	return fp.Ceiling
}

// Allows says whether a privilege is within the ceiling.
func (fp FleetVariables) Allows(privilege string) bool {
	return fp.CeilingOrDefault() == PrivilegeElevated || privilege == PrivilegeNobody
}

func checkValues(where string, m map[string]string) error {
	if len(m) > MaxParams {
		return fmt.Errorf("%w: %s has more than %d parameters", ErrInvalid, where, MaxParams)
	}
	for k, v := range m {
		if !ParamNameRE.MatchString(k) {
			return fmt.Errorf("%w: %s is not a parameter name (capitals, digits and underscores)", ErrInvalid, k)
		}
		if Reserved[k] {
			return fmt.Errorf("%w: %s is set by the console on every node", ErrInvalid, k)
		}
		if len(v) > MaxValue {
			return fmt.Errorf("%w: %s's value in %s is over %d bytes", ErrInvalid, k, where, MaxValue)
		}
	}
	return nil
}

// ValidateVariables checks a fleet's parameters (AU-06).
func ValidateVariables(fp FleetVariables) error {
	if fp.Ceiling != "" && fp.Ceiling != PrivilegeNobody && fp.Ceiling != PrivilegeElevated {
		return fmt.Errorf("%w: the ceiling is none or full", ErrInvalid)
	}
	if err := checkValues("the fleet", fp.Values); err != nil {
		return err
	}
	if fp.RebootMinutes < 0 || fp.RebootMinutes > MaxRebootMinutes {
		return fmt.Errorf("%w: a reboot timeout is 1 to %d minutes, or unset", ErrInvalid, MaxRebootMinutes)
	}
	if len(fp.NodeReboot) > maxNodeOverride {
		return fmt.Errorf("%w: at most %d nodes may set their own reboot timeout", ErrInvalid, maxNodeOverride)
	}
	for node, m := range fp.NodeReboot {
		if m < 1 || m > MaxRebootMinutes {
			return fmt.Errorf("%w: node %s's reboot timeout is 1 to %d minutes", ErrInvalid, node, MaxRebootMinutes)
		}
	}
	if len(fp.Nodes) > maxNodeOverride {
		return fmt.Errorf("%w: at most %d nodes may override values", ErrInvalid, maxNodeOverride)
	}
	for node, m := range fp.Nodes {
		if err := checkValues("node "+node, m); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOverrides checks a run's own values.
func ValidateOverrides(m map[string]string) error { return checkValues("the run", m) }

type variablesRecord struct {
	// Clusters holds each cluster's fleets' parameters, by fleet id.
	Clusters map[string]map[string]FleetVariables `json:"clusters"`
}

// Params returns a cluster's fleets' parameters, by fleet id.
func (st *Store) Variables(ctx context.Context, cluster string) (map[string]FleetVariables, error) {
	var rec variablesRecord
	if _, err := st.load(ctx, VariablesKey, &rec); err != nil {
		return nil, err
	}
	out := rec.Clusters[cluster]
	if out == nil {
		out = map[string]FleetVariables{}
	}
	return out, nil
}

// SetVariables replaces a fleet's parameters.
func (st *Store) SetVariables(ctx context.Context, cluster, fleet string, fp FleetVariables) error {
	if err := ValidateVariables(fp); err != nil {
		return err
	}
	for node, m := range fp.Nodes {
		if len(m) == 0 {
			delete(fp.Nodes, node)
		}
	}
	return change(ctx, st, VariablesKey, maxVariablesBytes, func(rec *variablesRecord) error {
		if rec.Clusters == nil {
			rec.Clusters = map[string]map[string]FleetVariables{}
		}
		if rec.Clusters[cluster] == nil {
			rec.Clusters[cluster] = map[string]FleetVariables{}
		}
		if fp.Empty() {
			delete(rec.Clusters[cluster], fleet)
		} else {
			rec.Clusters[cluster][fleet] = fp
		}
		return nil
	})
}

// Resolved is one node's values for a run: each declared parameter's value
// and where it came from.
type Resolved struct {
	Values  map[string]string `json:"values"`
	Sources map[string]string `json:"sources"`
	Missing []string          `json:"missing,omitempty"`
	// Conflicts are parameters that more than one of the node's fleets set.
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Layer is one place a node's values come from: a fleet it belongs to.
// Default marks All nodes, which sits under every fleet and conflicts with
// none.
type Layer struct {
	ID      string
	Name    string
	Params  FleetVariables
	Default bool
}

// Conflict is a parameter a node gets from more than one fleet, so nothing
// says which applies.
type Conflict struct {
	Node   string   `json:"node"`
	Param  string   `json:"param"`
	Fleets []string `json:"fleets"`
}

// inherited is what a node gets for a parameter: its own value wins, then
// its fleets', then All nodes'. Two fleets that set it are a conflict; so
// are two different values for the node itself, kept under two fleets.
func inherited(layers []Layer, node, name string) (value, source string, conflict []string) {
	var own, fleets []Layer
	var fallback *Layer
	for i := range layers {
		l := &layers[i]
		if _, ok := l.Params.Nodes[node][name]; ok {
			own = append(own, *l)
		}
		if _, ok := l.Params.Values[name]; ok {
			if l.Default {
				fallback = l
			} else {
				fleets = append(fleets, *l)
			}
		}
	}
	names := func(ls []Layer) []string {
		out := make([]string, 0, len(ls))
		for _, l := range ls {
			out = append(out, l.Name)
		}
		sort.Strings(out)
		return out
	}
	if len(own) > 0 {
		v := own[0].Params.Nodes[node][name]
		for _, l := range own[1:] {
			if l.Params.Nodes[node][name] != v {
				return "", "", names(own)
			}
		}
		return v, SourceNode, nil
	}
	switch {
	case len(fleets) > 1:
		return "", "", names(fleets)
	case len(fleets) == 1:
		return fleets[0].Params.Values[name], SourceFleet, nil
	case fallback != nil:
		return fallback.Params.Values[name], SourceFleet, nil
	}
	return "", SourceMissing, nil
}

// ResolveLayers works out a node's values for the parameters a script
// declares: the run's own, then what the node inherits from its fleets. A
// conflict is an error for the run, not a value.
func ResolveLayers(h Header, layers []Layer, node string, run map[string]string) Resolved {
	r := Resolved{Values: map[string]string{}, Sources: map[string]string{}}
	for _, p := range h.Params {
		if v, ok := run[p.Name]; ok {
			r.Values[p.Name], r.Sources[p.Name] = v, SourceRun
			continue
		}
		v, source, conflict := inherited(layers, node, p.Name)
		switch {
		case conflict != nil:
			r.Conflicts = append(r.Conflicts, Conflict{Node: node, Param: p.Name, Fleets: conflict})
		case source != SourceMissing:
			r.Values[p.Name], r.Sources[p.Name] = v, source
		case p.Required:
			r.Missing = append(r.Missing, p.Name)
		}
	}
	sort.Strings(r.Missing)
	return r
}

// VariableConflicts lists every conflict among nodes' fleets, by node then
// parameter, whether or not a script reads the parameter yet.
func VariableConflicts(nodes map[string][]Layer) []Conflict {
	out := []Conflict{}
	for node, layers := range nodes {
		names := map[string]bool{}
		for _, l := range layers {
			for k := range l.Params.Values {
				names[k] = true
			}
			for k := range l.Params.Nodes[node] {
				names[k] = true
			}
		}
		for name := range names {
			if _, _, conflict := inherited(layers, node, name); conflict != nil {
				out = append(out, Conflict{Node: node, Param: name, Fleets: conflict})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].Param < out[j].Param
	})
	return out
}

// Resolve works out a node's values from one fleet alone.
func Resolve(h Header, fp FleetVariables, node string, run map[string]string) Resolved {
	return ResolveLayers(h, []Layer{{Params: fp}}, node, run)
}

// AllVariables returns every cluster's fleets' parameters, by cluster and
// fleet id, for a backup. Secret parameters are references, never values.
func (st *Store) AllVariables(ctx context.Context) (map[string]map[string]FleetVariables, error) {
	var rec variablesRecord
	if _, err := st.load(ctx, VariablesKey, &rec); err != nil {
		return nil, err
	}
	if rec.Clusters == nil {
		rec.Clusters = map[string]map[string]FleetVariables{}
	}
	return rec.Clusters, nil
}
