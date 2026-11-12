package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// --- Verification rests on what a run re-checked (phase 12 review, W-1
// to W-4, ADR-145, CF-94 to CF-96) ---
//
// A fix waiting for verification is verified only on positive evidence:
// the run's scope holds the case's object, and the agent, given the key
// to re-check, says it read it again and the problem is gone. A run's
// scope comes from its kind: the nightly audit reads each cluster that
// answered as a whole, the alert diagnosis the objects of the alerts it
// diagnosed, and every other kind (the daily digest among them) nothing,
// so it never verifies. Findings past a run's cap are kept out by
// severity, and their keys never count as not seen.

// Scope is what a run read, for verifying fixes (W-1): whole clusters,
// and objects on a cluster. A run without one verifies nothing.
type Scope struct {
	Clusters []string    `json:"clusters,omitempty"`
	Objects  []ScopedObj `json:"objects,omitempty"`
}

// ScopedObj is one object a run read on a cluster, as a key holds it.
type ScopedObj struct {
	Cluster string `json:"cluster"`
	Object  string `json:"object"`
}

// Has says whether a key's cluster and object are inside the scope: its
// cluster read whole, or the object itself. A key of the cluster as a
// whole ("" object) is inside a whole cluster only.
func (s *Scope) Has(cluster, object string) bool {
	if s == nil {
		return false
	}
	if slices.Contains(s.Clusters, cluster) {
		return true
	}
	if object == "" {
		return false
	}
	for _, o := range s.Objects {
		if o.Cluster == cluster && strings.EqualFold(o.Object, object) {
			return true
		}
	}
	return false
}

// addCluster puts a cluster read whole in the scope.
func (s *Scope) addCluster(cluster string) {
	if !slices.Contains(s.Clusters, cluster) {
		s.Clusters = append(s.Clusters, cluster)
	}
}

// addObject puts an object read on a cluster in the scope; one that
// cannot be named as a key adds nothing.
func (s *Scope) addObject(cluster, object string) {
	if object == "" || s.Has(cluster, object) {
		return
	}
	s.Objects = append(s.Objects, ScopedObj{Cluster: cluster, Object: object})
}

// alertObject is the object an alert is about, as a key holds it:
// "shop/Pod/web-7f9", "Node/worker-3"; "" when it cannot be named so.
func alertObject(a Alert) string {
	if a.Kind == "" || a.Name == "" {
		return ""
	}
	text := a.Kind + "/" + a.Name
	if a.Namespace != "" {
		text = a.Namespace + "/" + text
	}
	o, err := NormalizeObject(text)
	if err != nil {
		return ""
	}
	return o.String()
}

// Recheck is what an agent says of a key it was given to re-check
// (W-1, P-2): whether the problem is still there, with what it read.
// Still is nil when the agent did not say: such an entry counts as not
// re-checked.
type Recheck struct {
	Check    string `json:"check"`
	Resource string `json:"resource"`
	Cluster  string `json:"cluster,omitempty"`
	Still    *bool  `json:"still"`
	Evidence string `json:"evidence"`
	// Key is the key it names, as the console writes keys; set by the
	// run, nil for an entry that names none.
	Key *Key `json:"key,omitempty"`
}

// UnmarshalJSON reads an entry as leniently as Fix does: a field of
// another type is empty, "still" may be written as text ("false"), so
// one odd entry never makes the block unreadable.
func (r *Recheck) UnmarshalJSON(b []byte) error {
	*r = Recheck{}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	text := func(k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	r.Check, r.Resource, r.Cluster, r.Evidence = text("check"), text("resource"), text("cluster"), text("evidence")
	var still bool
	if json.Unmarshal(m["still"], &still) == nil {
		r.Still = &still
	} else if v, err := strconv.ParseBool(strings.TrimSpace(text("still"))); err == nil {
		r.Still = &v
	}
	return nil
}

// maxRechecks is how many re-checks one run keeps.
const maxRechecks = 100

// withCluster gives the re-checks of one answer the cluster it was
// asked about, as a finding gets it (ADR-147): one that names none, the
// cluster's id or its name is that cluster's; one that names another
// cluster is left out and counted, so an answer about one cluster never
// verifies, or reopens, a fix on another the run read.
func withCluster(in []Recheck, cluster, name string) (out []Recheck, others int) {
	out = make([]Recheck, 0, len(in))
	for _, r := range in {
		if cl := strings.TrimSpace(r.Cluster); cl != "" && !strings.EqualFold(cl, cluster) && !strings.EqualFold(cl, name) {
			others++
			continue
		}
		r.Cluster = cluster
		out = append(out, r)
	}
	return out, others
}

// addRechecks takes one answer's re-checks into the run's, and says on
// the run how many named another cluster than the one asked about.
func addRechecks(out *Outcome, env *RunEnv, cluster string, in []Recheck) {
	list, others := withCluster(in, cluster, env.ClusterName(cluster))
	out.Rechecked = append(out.Rechecked, list...)
	if others > 0 {
		env.Note("%d %s named another cluster than %s, which it was asked about, and %s left out", others, plural(others, "re-check", "re-checks"), cluster, plural(others, "was", "were"))
	}
}

// keyRechecks keys a run's re-checks as the console writes keys: the
// object normalised, the check by the id its rule routes by (ADR-138).
// An entry that says nothing ("still" missing) or names no valid key is
// left out; the evidence is cut short and scrubbed.
func (s *Service) keyRechecks(ctx context.Context, in []Recheck) []Recheck {
	var out []Recheck
	var rules Rules
	for _, r := range in {
		if len(out) >= maxRechecks {
			break
		}
		if r.Still == nil {
			continue
		}
		cluster := clip(r.Cluster, 100)
		check := strings.ToLower(strings.TrimSpace(clip(r.Check, 100)))
		o, err := NormalizeObject(clip(r.Resource, 300))
		if cluster == "" || check == "" || err != nil {
			continue
		}
		if rules == nil {
			rules = s.rules(ctx)
		}
		still := *r.Still
		out = append(out, Recheck{Check: check, Resource: o.String(), Cluster: cluster, Still: &still, Evidence: clip(r.Evidence, maxEvidenceL),
			Key: &Key{Cluster: cluster, Object: o.String(), Check: rules.Canon(check)}})
	}
	return out
}

// LeftOut says how many findings a run left out past its cap, by
// severity, and the cap (W-3).
type LeftOut struct {
	Count      int    `json:"count"`
	Severities Counts `json:"severities"`
	Cap        int    `json:"cap"`
}

// leftOutKey is the key of a finding left out past the cap, when it has
// a valid one (the object is not looked up on the cluster): the router
// never takes it as not seen.
func leftOutKey(k Kind, rules Rules, f Finding) (Key, bool) {
	check := strings.ToLower(strings.TrimSpace(f.Check))
	if f.Cluster == "" || validCheck(k, rules, check) != "" {
		return Key{}, false
	}
	o, err := NormalizeObject(f.Resource)
	if err != nil {
		return Key{}, false
	}
	return Key{Cluster: f.Cluster, Object: o.String(), Check: rules.Canon(check)}, true
}

// findingRank orders findings for the cap: critical first, info last.
func findingRank(severity string) int {
	switch severity {
	case FindingCritical:
		return 0
	case FindingWarning:
		return 1
	}
	return 2
}

// normalSeverity is a finding's severity as a run keeps it: high and
// error are critical, low is info, anything else a warning.
func normalSeverity(severity string) string {
	switch severity {
	case FindingInfo, FindingWarning, FindingCritical:
		return severity
	case "high", "error":
		return FindingCritical
	case "low":
		return FindingInfo
	}
	return FindingWarning
}

// leftOutNote is what a run says of the findings it left out (W-3, I-3).
func leftOutNote(l *LeftOut) string {
	if l == nil || l.Count == 0 {
		return ""
	}
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{l.Severities.Critical, "critical"}, {l.Severities.Warning, plural(l.Severities.Warning, "warning", "warnings")}, {l.Severities.Info, "info"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.word))
		}
	}
	return fmt.Sprintf("%d %s left out past the cap of %d (%s)", l.Count, plural(l.Count, "finding was", "findings were"), maxFindings, strings.Join(parts, ", "))
}

// notRecheckedNote is what a run says of the fixes in its scope the agent
// did not re-check (W-1, I-3).
func notRecheckedNote(n int) string {
	return fmt.Sprintf("%d %s not re-checked", n, plural(n, "fix was", "fixes were"))
}

// recheckList is the part of a task that lists the job's fixes waiting
// for verification inside the scope, to re-check (W-1, P-2); "" when
// there are none.
func recheckList(keys []KeyInUse, job string, scope *Scope) string {
	var b strings.Builder
	for _, k := range keys {
		if k.Recheck && k.Job == job && scope.Has(k.Cluster, k.Object) {
			fmt.Fprintf(&b, "- %s on %s: %s (%s)\n", k.Check, orCluster(k.Object), clip(k.Title, 160), k.What)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "The fixes to re-check: each was fixed since this job found it and waits for a run to confirm it. Read each one's object again and give an entry in \"rechecked\" for every one: \"still\" true when the problem is still there, false when it is gone, and in \"evidence\" what you read. A fix you do not re-check stays unconfirmed.\n\n" +
		Quote("recheck", b.String())
}
