// Package fleets groups nodes into fleets (P4-01). A fleet is a set of
// labels and taints; every node that has all of them belongs to it.
// Membership is never stored: it is worked out from the nodes as they are
// now, so a node joins or leaves a fleet only by changing its own labels
// and taints. Fleets are kept with the console's settings, per cluster.
package fleets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Fleet is one fleet's definition.
type Fleet struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Color is one of six swatches, 0 to 5.
	Color int    `json:"color"`
	Desc  string `json:"desc,omitempty"`
	// Labels are "key=value"; "key=" requires the label with an empty
	// value. At most one value per key.
	Labels []string `json:"labels"`
	// Taints are "key[=value]:Effect".
	Taints []string `json:"taints"`
}

// Limits keep the settings small.
const (
	MaxFleets       = 100
	MaxRequirements = 50
	MaxName         = 80
	MaxDesc         = 400
	Colors          = 6
)

var (
	labelKeyRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)
	labelValRE = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)
	idRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	effects    = map[string]bool{"NoSchedule": true, "PreferNoSchedule": true, "NoExecute": true}
)

// SplitLabel splits "key=value" on the first "=".
func SplitLabel(s string) (key, value string) {
	key, value, _ = strings.Cut(s, "=")
	return key, value
}

// TaintID writes a taint as a fleet requires it: "key[=value]:Effect".
func TaintID(t corev1.Taint) string {
	if t.Value == "" {
		return t.Key + ":" + string(t.Effect)
	}
	return t.Key + "=" + t.Value + ":" + string(t.Effect)
}

// ParseTaint reads "key[=value]:Effect".
func ParseTaint(s string) (key, value, effect string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return "", "", "", false
	}
	effect = s[i+1:]
	key, value = SplitLabel(s[:i])
	return key, value, effect, key != "" && effects[effect]
}

// Has says whether a node with these labels and taints is in the fleet.
func (f Fleet) Has(labels map[string]string, taints []corev1.Taint) bool {
	for _, l := range f.Labels {
		k, v := SplitLabel(l)
		got, ok := labels[k]
		if !ok || got != v {
			return false
		}
	}
	for _, want := range f.Taints {
		found := false
		for _, t := range taints {
			if TaintID(t) == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Validate checks a cluster's list of fleets: ids and names unique, every
// fleet with a name and at least one valid requirement.
func Validate(list []Fleet) error {
	if len(list) > MaxFleets {
		return fmt.Errorf("at most %d fleets", MaxFleets)
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, f := range list {
		name := strings.TrimSpace(f.Name)
		switch {
		case !idRE.MatchString(f.ID):
			return fmt.Errorf("fleet id %q is not valid", f.ID)
		case ids[f.ID]:
			return fmt.Errorf("two fleets have the id %q", f.ID)
		case name == "":
			return fmt.Errorf("Give the fleet a name.")
		case len(name) > MaxName:
			return fmt.Errorf("A fleet's name has at most %d characters.", MaxName)
		case names[strings.ToLower(name)]:
			return fmt.Errorf("Another fleet is already called “%s”.", name)
		case len(f.Desc) > MaxDesc:
			return fmt.Errorf("A fleet's description has at most %d characters.", MaxDesc)
		case f.Color < 0 || f.Color >= Colors:
			return fmt.Errorf("fleet color %d is not one of the %d", f.Color, Colors)
		case len(f.Labels)+len(f.Taints) == 0:
			return fmt.Errorf("Pick at least one label or taint.")
		case len(f.Labels)+len(f.Taints) > MaxRequirements:
			return fmt.Errorf("A fleet has at most %d labels and taints.", MaxRequirements)
		}
		ids[f.ID] = true
		names[strings.ToLower(name)] = true
		keys := map[string]bool{}
		for _, l := range f.Labels {
			k, v := SplitLabel(l)
			if !labelKeyRE.MatchString(k) {
				return fmt.Errorf("“%s” isn’t a valid label or taint key.", k)
			}
			if !labelValRE.MatchString(v) {
				return fmt.Errorf("“%s” isn’t a valid value: up to 63 letters, digits, dashes, dots and underscores.", v)
			}
			if keys[k] {
				return fmt.Errorf("A fleet needs one value per label: %s has two.", k)
			}
			keys[k] = true
		}
		for _, t := range f.Taints {
			k, v, _, ok := ParseTaint(t)
			if !ok {
				return fmt.Errorf("A taint is key=value:NoSchedule, PreferNoSchedule or NoExecute, not “%s”.", t)
			}
			if !labelKeyRE.MatchString(k) {
				return fmt.Errorf("“%s” isn’t a valid label or taint key.", k)
			}
			if !labelValRE.MatchString(v) {
				return fmt.Errorf("“%s” isn’t a valid value: up to 63 letters, digits, dashes, dots and underscores.", v)
			}
		}
	}
	return nil
}

// Clean trims names and descriptions and drops duplicate requirements.
func Clean(list []Fleet) []Fleet {
	out := make([]Fleet, 0, len(list))
	for _, f := range list {
		f.Name = strings.TrimSpace(f.Name)
		f.Desc = strings.TrimSpace(f.Desc)
		f.Labels = dedupe(f.Labels)
		f.Taints = dedupe(f.Taints)
		out = append(out, f)
	}
	return out
}

func dedupe(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Node is what fleets need to know of a node.
type Node struct {
	Name   string
	Labels map[string]string
	Taints []corev1.Taint
	// Allocatable CPU in millicores and memory in bytes, and what the
	// pods on it request.
	CPU, Memory       int64
	CPUReq, MemoryReq int64
	// Pods counts the pods running on it.
	Pods     int
	Cordoned bool
	// Errors and Warnings count the node's own problems.
	Errors, Warnings int
}

// Stats are a fleet's totals.
type Stats struct {
	Nodes     int   `json:"nodes"`
	CPU       int64 `json:"cpu"`
	Memory    int64 `json:"memory"`
	CPUReq    int64 `json:"cpuReq"`
	MemoryReq int64 `json:"memoryReq"`
	Pods      int   `json:"pods"`
	Cordoned  int   `json:"cordoned"`
	// Errors counts nodes with errors; Warnings nodes with warnings and no
	// errors.
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

func (s *Stats) add(n Node) {
	s.Nodes++
	s.CPU += n.CPU
	s.Memory += n.Memory
	s.CPUReq += n.CPUReq
	s.MemoryReq += n.MemoryReq
	s.Pods += n.Pods
	if n.Cordoned {
		s.Cordoned++
	}
	switch {
	case n.Errors > 0:
		s.Errors++
	case n.Warnings > 0:
		s.Warnings++
	}
}

// Row kinds.
const (
	KindFleet      = "fleet"
	KindNone       = "none"
	KindSuggestion = "suggestion"
)

// Row is one row of the fleets view: a fleet with its members and totals,
// the nodes in no fleet, or a group of alike nodes not saved as a fleet.
type Row struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	// A fleet's definition, or a suggestion's labels and taints.
	ID     string   `json:"id,omitempty"`
	Name   string   `json:"name,omitempty"`
	Color  int      `json:"color"`
	Desc   string   `json:"desc,omitempty"`
	Labels []string `json:"labels"`
	Taints []string `json:"taints"`
	// Order is a fleet's place in the list.
	Order   int      `json:"order"`
	Members []string `json:"members"`
	Stats   Stats    `json:"stats"`
}

// Build works out every fleet's members and totals, the nodes in no
// fleet, and the groups worth saving as a fleet.
func Build(defs []Fleet, nodes []Node) []Row {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	var out []Row
	inAny := map[string]bool{}
	memberSets := map[string]bool{}
	for i, f := range defs {
		r := Row{Key: KindFleet + ":" + f.ID, Kind: KindFleet, ID: f.ID, Name: f.Name, Color: f.Color, Desc: f.Desc, Labels: nonNil(f.Labels), Taints: nonNil(f.Taints), Order: i, Members: []string{}}
		for _, n := range nodes {
			if f.Has(n.Labels, n.Taints) {
				r.Members = append(r.Members, n.Name)
				r.Stats.add(n)
				inAny[n.Name] = true
			}
		}
		memberSets[strings.Join(r.Members, ",")] = true
		out = append(out, r)
	}
	none := Row{Key: KindNone, Kind: KindNone, Members: []string{}, Labels: []string{}, Taints: []string{}}
	for _, n := range nodes {
		if !inAny[n.Name] {
			none.Members = append(none.Members, n.Name)
			none.Stats.add(n)
		}
	}
	out = append(out, none)
	for _, g := range Suggest(nodes) {
		left := false
		for _, m := range g.Members {
			if !inAny[m] {
				left = true
			}
		}
		if !left || memberSets[strings.Join(g.Members, ",")] {
			continue
		}
		out = append(out, g)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Labels and taints that tell nothing about what a node is for.
func fleetLabelKey(k string) bool {
	return k != "kubernetes.io/hostname" && !strings.HasPrefix(k, "topology.") && !strings.HasPrefix(k, "failure-domain.beta.kubernetes.io/")
}

func fleetTaintKey(k string) bool {
	return !strings.HasPrefix(k, "node.kubernetes.io/") && !strings.HasPrefix(k, "node.cloudprovider.kubernetes.io/")
}

var (
	cpKeys   = []string{"node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master"}
	poolKeys = []string{
		"eks.amazonaws.com/nodegroup", "alpha.eksctl.io/nodegroup-name", "cloud.google.com/gke-nodepool",
		"kubernetes.azure.com/agentpool", "agentpool", "karpenter.sh/nodepool", "node.kubernetes.io/pool",
		"nodepool", "node-pool", "pool", "workload", "node.kubernetes.io/instance-type",
	}
)

// LabelText is "key" for an empty value, otherwise "key=value".
func LabelText(k, v string) string {
	if v == "" {
		return k
	}
	return k + "=" + v
}

// Suggest groups nodes that have the same labels and taints (leaving out
// host names, zones and the taints Kubernetes sets), names each group, and
// finds the fewest labels and taints that pick exactly its nodes. A group
// no such selection picks is left out.
func Suggest(nodes []Node) []Row {
	type group struct {
		labels  []string
		taints  []string
		members []Node
	}
	var groups []*group
	bySig := map[string]*group{}
	for _, n := range nodes {
		var ls, ts []string
		for k, v := range n.Labels {
			if fleetLabelKey(k) {
				ls = append(ls, k+"="+v)
			}
		}
		for _, t := range n.Taints {
			if fleetTaintKey(t.Key) {
				ts = append(ts, TaintID(t))
			}
		}
		sort.Strings(ls)
		sort.Strings(ts)
		sig := strings.Join(ls, "\n") + "\x00" + strings.Join(ts, "\n")
		g := bySig[sig]
		if g == nil {
			g = &group{labels: ls, taints: ts}
			bySig[sig] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, n)
	}
	// distinct: the labels a group has that not every group has.
	common := map[string]int{}
	for _, g := range groups {
		for _, l := range g.labels {
			common[l]++
		}
	}
	var out []Row
	for i, g := range groups {
		var distinct []string
		for _, l := range g.labels {
			if common[l] < len(groups) {
				distinct = append(distinct, l)
			}
		}
		labels := map[string]string{}
		for _, l := range g.labels {
			k, v := SplitLabel(l)
			labels[k] = v
		}
		cp := ""
		for _, k := range cpKeys {
			if _, ok := labels[k]; ok {
				cp = k
				break
			}
		}
		pool := ""
		for _, k := range poolKeys {
			if _, ok := labels[k]; ok {
				pool = k
				break
			}
		}
		what := ""
		if pool != "" {
			what = LabelText(pool, labels[pool])
		} else if len(distinct) > 0 {
			k, v := SplitLabel(distinct[0])
			what = LabelText(k, v)
		}
		_, worker := labels["node-role.kubernetes.io/worker"]
		title := ""
		switch {
		case cp != "":
			title = "Control plane"
		case worker:
			title = "Workers"
			if what != "" {
				title += " · " + what
			}
		case what != "":
			title = what
		default:
			title = fmt.Sprintf("Fleet %d", i+1)
		}
		want := names(g.members)
		var cands [][2][]string
		if cp != "" {
			cands = append(cands, [2][]string{{cp + "=" + labels[cp]}, nil})
		}
		if pool != "" {
			cands = append(cands, [2][]string{{pool + "=" + labels[pool]}, nil})
		}
		for _, l := range distinct {
			cands = append(cands, [2][]string{{l}, nil})
		}
		for _, t := range g.taints {
			cands = append(cands, [2][]string{nil, {t}})
		}
		cands = append(cands, [2][]string{distinct, g.taints})
		for _, c := range cands {
			if len(c[0])+len(c[1]) == 0 {
				continue
			}
			f := Fleet{Labels: c[0], Taints: c[1]}
			var got []string
			for _, n := range nodes {
				if f.Has(n.Labels, n.Taints) {
					got = append(got, n.Name)
				}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				continue
			}
			r := Row{Kind: KindSuggestion, Name: title, Labels: nonNil(append([]string(nil), c[0]...)), Taints: nonNil(append([]string(nil), c[1]...)), Members: want}
			for _, n := range g.members {
				r.Stats.add(n)
			}
			sum := sha256.Sum256([]byte(strings.Join(want, ",")))
			r.Key = KindSuggestion + ":" + hex.EncodeToString(sum[:6])
			out = append(out, r)
			break
		}
	}
	return out
}

func names(ns []Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	sort.Strings(out)
	return out
}
