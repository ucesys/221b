package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Parameter limits (AU-06).
const (
	MaxValue          = 1 << 10
	MaxParams         = 100
	maxVariablesBytes = 64 << 10
	SourceRun         = "run"
	SourceNode        = "node"
	SourceFleet       = "fleet"
	// SourceVariable is a run value bound to a variable (FU-04): the
	// node's own value of that variable.
	SourceVariable  = "variable"
	SourceMissing   = ""
	defaultCeiling  = PrivilegeNobody
	maxNodeOverride = 5000
	// AllNodes is the id of the fleet every node of a cluster is in. A
	// node's own values and reboot time are the node's, whatever fleets it
	// is in, and are kept under it.
	AllNodes = "_all"
	// MaxRebootMinutes is the longest a node may be given to come back
	// from a reboot.
	MaxRebootMinutes = 240
)

// FleetVariables are a fleet's parameter values, its nodes' own values and
// the most privilege a run on it may get. A node's own values and reboot
// time belong to the node, not to the fleet: kept once, under AllNodes,
// and shown with every fleet the node is in (View).
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

// ValidateOverrides checks a run's own values: a value that starts with
// var: must bind to a variable (FU-04).
func ValidateOverrides(m map[string]string) error {
	if err := checkValues("the run", m); err != nil {
		return err
	}
	return checkBindings(m)
}

type variablesRecord struct {
	// Clusters holds each cluster's fleets' parameters, by fleet id.
	Clusters map[string]map[string]FleetVariables `json:"clusters"`
}

// nodesOwn moves what a document from before 0.13 kept of a node under one
// of its fleets to where a node's own values are kept, under AllNodes. A
// node's own value was the node's then too: it won over every fleet's. Two
// fleets that kept different values for one node were a conflict that
// stopped its runs; the first by fleet id is kept, All nodes' before any,
// and of two reboot times the longer, as a run took it.
func (rec *variablesRecord) nodesOwn() {
	for cluster, fleets := range rec.Clusters {
		ids := make([]string, 0, len(fleets))
		for id, fp := range fleets {
			if id != AllNodes && (len(fp.Nodes) > 0 || len(fp.NodeReboot) > 0) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		all := fleets[AllNodes]
		for _, id := range ids {
			fp := fleets[id]
			for node, values := range fp.Nodes {
				for name, v := range values {
					if _, set := all.Nodes[node][name]; set {
						continue
					}
					if all.Nodes == nil {
						all.Nodes = map[string]map[string]string{}
					}
					if all.Nodes[node] == nil {
						all.Nodes[node] = map[string]string{}
					}
					all.Nodes[node][name] = v
				}
			}
			for node, m := range fp.NodeReboot {
				if all.NodeReboot == nil {
					all.NodeReboot = map[string]int{}
				}
				all.NodeReboot[node] = max(all.NodeReboot[node], m)
			}
			fp.Nodes, fp.NodeReboot = nil, nil
			if fp.Empty() {
				delete(fleets, id)
			} else {
				fleets[id] = fp
			}
		}
		fleets[AllNodes] = all
		rec.Clusters[cluster] = fleets
	}
}

// View is a fleet's variables as its page shows them: the fleet's own
// and, of the nodes' own values and reboot times, those of the nodes
// given: the fleet's members. For nil it is every node's, as All nodes
// shows them.
func View(all map[string]FleetVariables, fleet string, nodes []string) FleetVariables {
	fp, kept := all[fleet], all[AllNodes]
	fp.Nodes, fp.NodeReboot = nil, nil
	take := func(node string) {
		if m := kept.Nodes[node]; len(m) > 0 {
			if fp.Nodes == nil {
				fp.Nodes = map[string]map[string]string{}
			}
			fp.Nodes[node] = maps.Clone(m)
		}
		if m := kept.NodeReboot[node]; m > 0 {
			if fp.NodeReboot == nil {
				fp.NodeReboot = map[string]int{}
			}
			fp.NodeReboot[node] = m
		}
	}
	if nodes != nil {
		for _, n := range nodes {
			take(n)
		}
		return fp
	}
	for n := range kept.Nodes {
		take(n)
	}
	for n := range kept.NodeReboot {
		take(n)
	}
	return fp
}

// NamedNodes are the nodes a fleet's variables say something of. Never
// nil, which View and SetVariablesFor take for every node.
func NamedNodes(fps ...FleetVariables) []string {
	seen := map[string]bool{}
	for _, fp := range fps {
		for n := range fp.Nodes {
			seen[n] = true
		}
		for n := range fp.NodeReboot {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// put writes a fleet's variables as a caller saw and changed them: the
// fleet's own, and the own values and reboot times of the nodes given,
// every node's for nil. A node of those that fp says nothing of has none
// from then on.
func (rec *variablesRecord) put(cluster, fleet string, fp FleetVariables, nodes []string) {
	if rec.Clusters == nil {
		rec.Clusters = map[string]map[string]FleetVariables{}
	}
	if rec.Clusters[cluster] == nil {
		rec.Clusters[cluster] = map[string]FleetVariables{}
	}
	fleets := rec.Clusters[cluster]
	all := fleets[AllNodes]
	if nodes == nil {
		all.Nodes, all.NodeReboot = nil, nil
		nodes = NamedNodes(fp)
	}
	for _, n := range nodes {
		delete(all.Nodes, n)
		delete(all.NodeReboot, n)
		if m := fp.Nodes[n]; len(m) > 0 {
			if all.Nodes == nil {
				all.Nodes = map[string]map[string]string{}
			}
			all.Nodes[n] = maps.Clone(m)
		}
		if m := fp.NodeReboot[n]; m > 0 {
			if all.NodeReboot == nil {
				all.NodeReboot = map[string]int{}
			}
			all.NodeReboot[n] = m
		}
	}
	if len(all.Nodes) == 0 {
		all.Nodes = nil
	}
	if len(all.NodeReboot) == 0 {
		all.NodeReboot = nil
	}
	own := fp
	own.Nodes, own.NodeReboot = nil, nil
	if fleet == AllNodes {
		own.Nodes, own.NodeReboot = all.Nodes, all.NodeReboot
		all = own
	} else if own.Empty() {
		delete(fleets, fleet)
	} else {
		fleets[fleet] = own
	}
	if all.Empty() {
		delete(fleets, AllNodes)
	} else {
		fleets[AllNodes] = all
	}
}

// Variables returns a cluster's variables as they are kept, by fleet id:
// each fleet's own, and under AllNodes also every node's own values and
// reboot time. View gives a fleet's as its page shows them.
func (st *Store) Variables(ctx context.Context, cluster string) (map[string]FleetVariables, error) {
	var rec variablesRecord
	if _, err := st.load(ctx, VariablesKey, &rec); err != nil {
		return nil, err
	}
	rec.nodesOwn()
	out := rec.Clusters[cluster]
	if out == nil {
		out = map[string]FleetVariables{}
	}
	return out, nil
}

type byKey struct{}

// SetBy says who a call sets variables for, so that the rows it writes
// name them (variables.updated_by, DB-87): the person who saves them, or
// whose change is approved. A call that says no one writes no name.
func SetBy(ctx context.Context, who Who) context.Context {
	return context.WithValue(ctx, byKey{}, who)
}

// byOf is who SetBy said, as a row names them: the name shown, or the
// user when there is no name.
func byOf(ctx context.Context) string {
	who, _ := ctx.Value(byKey{}).(Who)
	if who.Name != "" {
		return who.Name
	}
	return who.User
}

// SetVariables sets a fleet's own variables to fp's, and the own values
// and reboot times of the nodes fp names; a node it does not name keeps
// its own. All nodes' are replaced whole, every node's own with them.
func (st *Store) SetVariables(ctx context.Context, cluster, fleet string, fp FleetVariables) error {
	if fleet == AllNodes {
		return st.SetVariablesFor(ctx, cluster, fleet, fp, nil)
	}
	return st.SetVariablesFor(ctx, cluster, fleet, fp, NamedNodes(fp))
}

// SetVariablesFor sets a fleet's variables as someone who saw them with
// the own values of the nodes given, and changed them, leaves them: the
// fleet's own are fp's, and so are those nodes' own values and reboot
// times, none for a node fp no longer names. For nil the nodes are all
// the cluster's.
func (st *Store) SetVariablesFor(ctx context.Context, cluster, fleet string, fp FleetVariables, nodes []string) error {
	if err := ValidateVariables(fp); err != nil {
		return err
	}
	if nodes != nil {
		for _, n := range NamedNodes(fp) {
			if !slices.Contains(nodes, n) {
				return fmt.Errorf("%w: node %s is not one of this fleet's", ErrInvalid, n)
			}
		}
	}
	return change(ctx, st, VariablesKey, maxVariablesBytes, func(rec *variablesRecord) error {
		rec.nodesOwn()
		rec.put(cluster, fleet, fp, nodes)
		return nil
	})
}

// SetVariablesFrom saves a fleet's variables as someone who read them as
// before and changed them to next: what differs between the two (Items)
// is set over the variables as they are now, and nothing else, as an
// approved change is. A value, mark, ceiling or reboot time the save would
// set that changed since it was read refuses it (ErrBusy), so a save from
// a read gone stale (the 45 s Sherlock may take to weigh it) never undoes
// what someone else saved or approved in between.
func (st *Store) SetVariablesFrom(ctx context.Context, cluster, fleet string, before, next FleetVariables) error {
	if err := ValidateVariables(next); err != nil {
		return err
	}
	items := Items(before, next)
	nodes := NamedNodes(before, next)
	return change(ctx, st, VariablesKey, maxVariablesBytes, func(rec *variablesRecord) error {
		rec.nodesOwn()
		cur := View(rec.Clusters[cluster], fleet, nodes)
		done, moved := weigh(cur, items)
		switch {
		case done:
			return errNoChange
		case len(moved) > 0:
			return busyError(fmt.Sprintf("These variables changed since they were read (%s differ): read them again, and save from what they are now.", strings.Join(moved, ", ")))
		}
		rec.put(cluster, fleet, applyItems(cur, items), nodes)
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
	// Bound are the parameters the run binds to a variable, and to which
	// (FU-04).
	Bound map[string]string `json:"bound,omitempty"`
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
// its fleets', then All nodes'. Two fleets that set it are a conflict. A
// node's own value is kept once and shown with each of its fleets, so the
// layers that have it agree.
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
// conflict is an error for the run, not a value. A run value that binds
// to a variable (var:NAME, FU-04) is the node's own value of that
// variable, inherited as any other; a node without one has no value, as
// if the run gave none. A secret parameter's value is a Secret's
// reference, which never binds.
func ResolveLayers(h Header, layers []Layer, node string, run map[string]string) Resolved {
	r := Resolved{Values: map[string]string{}, Sources: map[string]string{}}
	for _, p := range h.Params {
		from := p.Name
		if v, ok := run[p.Name]; ok {
			name, bound := Bound(v)
			if !bound || p.Secret {
				if !p.Secret {
					v = Constant(v)
				}
				// An empty string is a value the run may give, but none
				// for a required parameter (FU-07).
				if p.Required && v == "" {
					r.Missing = append(r.Missing, p.Name+" (an empty string)")
					continue
				}
				r.Values[p.Name], r.Sources[p.Name] = v, SourceRun
				continue
			}
			if r.Bound == nil {
				r.Bound = map[string]string{}
			}
			r.Bound[p.Name], from = name, name
		}
		v, source, conflict := inherited(layers, node, from)
		if r.Bound[p.Name] != "" && source != SourceMissing {
			source = SourceVariable
		}
		switch {
		case conflict != nil:
			r.Conflicts = append(r.Conflicts, Conflict{Node: node, Param: from, Fleets: conflict})
		case source != SourceMissing:
			r.Values[p.Name], r.Sources[p.Name] = v, source
		case p.Required && r.Bound[p.Name] != "":
			r.Missing = append(r.Missing, fmt.Sprintf("%s (bound to variable %s, not set here)", p.Name, from))
		case p.Required:
			r.Missing = append(r.Missing, p.Name)
		}
	}
	sort.Strings(r.Missing)
	return r
}

// Inherited is what a node gets for a variable from its fleets, and
// where from (SourceNode, SourceFleet, or SourceMissing); conflict names
// the fleets that both set it.
func Inherited(layers []Layer, node, name string) (value, source string, conflict []string) {
	return inherited(layers, node, name)
}

// FleetsOf are the fleets of a run's nodes, each once, in the order they
// are met, with their values for the parameters h declares; and each
// node's fleets by id. A run keeps both (RunFleet).
func FleetsOf(h Header, nodes map[string][]Layer, order []string) ([]RunFleet, map[string][]string) {
	var fleets []RunFleet
	seen := map[string]bool{}
	of := map[string][]string{}
	for _, node := range order {
		for _, l := range nodes[node] {
			of[node] = append(of[node], l.ID)
			if seen[l.ID] {
				continue
			}
			seen[l.ID] = true
			f := RunFleet{ID: l.ID, Name: l.Name}
			for _, p := range h.Params {
				if v, ok := l.Params.Values[p.Name]; ok {
					if f.Values == nil {
						f.Values = map[string]string{}
					}
					f.Values[p.Name] = v
				}
			}
			fleets = append(fleets, f)
		}
	}
	return fleets, of
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
	rec.nodesOwn()
	if rec.Clusters == nil {
		rec.Clusters = map[string]map[string]FleetVariables{}
	}
	return rec.Clusters, nil
}
