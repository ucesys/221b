package apps

import (
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"sigs.k8s.io/yaml"
)

// Diff compares two manifests, object by object (HA-17).
type Diff struct {
	// Changed, Added and Removed name the objects, as "Kind name".
	Changed []string `json:"changed"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	// Text is the unified diff of the whole manifests, objects in the
	// same order on both sides.
	Text string `json:"text"`
	Same bool   `json:"same"`
}

// Compare diffs from against to, each a rendered manifest.
func Compare(from, to, fromLabel, toLabel string) Diff {
	a, b := objects(from), objects(to)
	d := Diff{Changed: []string{}, Added: []string{}, Removed: []string{}}
	var keys []string
	seen := map[string]bool{}
	for k := range a {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range b {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var left, right strings.Builder
	for _, k := range keys {
		x, inA := a[k]
		y, inB := b[k]
		switch {
		case inA && !inB:
			d.Removed = append(d.Removed, k)
		case inB && !inA:
			d.Added = append(d.Added, k)
		case x != y:
			d.Changed = append(d.Changed, k)
		}
		if inA {
			left.WriteString("---\n# " + k + "\n" + x + "\n")
		}
		if inB {
			right.WriteString("---\n# " + k + "\n" + y + "\n")
		}
	}
	d.Same = len(d.Changed)+len(d.Added)+len(d.Removed) == 0
	if !d.Same {
		d.Text, _ = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: difflib.SplitLines(left.String()), B: difflib.SplitLines(right.String()),
			FromFile: fromLabel, ToFile: toLabel, Context: 3,
		})
	}
	return d
}

// objects splits a manifest into its objects, keyed "Kind name" (with the
// namespace when one is set), each trimmed of trailing space.
func objects(manifest string) map[string]string {
	out := map[string]string{}
	for _, doc := range splitDocs(manifest) {
		var o struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if yaml.Unmarshal([]byte(doc), &o) != nil || o.Kind == "" || o.Metadata.Name == "" {
			continue
		}
		k := o.Kind + " " + o.Metadata.Name
		if o.Metadata.Namespace != "" {
			k = o.Kind + " " + o.Metadata.Namespace + "/" + o.Metadata.Name
		}
		// Helm writes "# Source: chart/templates/x.yaml" above each object;
		// a renamed template is not a change.
		lines := strings.Split(doc, "\n")
		var kept []string
		for _, l := range lines {
			if strings.HasPrefix(l, "# Source:") {
				continue
			}
			kept = append(kept, strings.TrimRight(l, " \t"))
		}
		out[k] = strings.TrimSpace(strings.Join(kept, "\n"))
	}
	return out
}
