package apps

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"sigs.k8s.io/yaml"

	"github.com/ucesys/221b/server/internal/kube"
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

// Compare diffs from against to, each a rendered manifest. values are the
// release values each side was rendered with (the record's config and the
// chart's defaults, or the values composed from the source): a credential
// they hold is masked wherever either manifest prints it (kube#5). An
// object changed when its rendered text did, masked or not (kube#4).
func Compare(from, to, fromLabel, toLabel string, values ...map[string]any) Diff {
	a, b := objects(from, values), objects(to, values)
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
		xo, inA := a[k]
		yo, inB := b[k]
		x, y := xo.shown, yo.shown
		switch {
		case inA && !inB:
			d.Removed = append(d.Removed, k)
		case inB && !inA:
			d.Added = append(d.Added, k)
		case xo.raw != yo.raw:
			d.Changed = append(d.Changed, k)
			if x == y {
				// Only what is hidden changed: the diff says so, and not
				// what (kube#4).
				y += "\n# a value hidden by 221B changed"
			}
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

// rendered is one object of a manifest: its text as shown, masked, and as
// rendered, which says whether it changed and is never shown.
type rendered struct{ shown, raw string }

// objects splits a manifest into its objects, keyed "Kind name" (with the
// namespace when one is set), each trimmed of trailing space. A Secret's
// values are hidden, and every other object is masked as release values
// are (kube.MaskObjects, with the release's values), so a diff shows no
// credential whatever kind holds it.
func objects(manifest string, values []map[string]any) map[string]rendered {
	out := map[string]rendered{}
	var keys, docs []string
	var objs []map[string]any
	for _, doc := range splitDocs(manifest) {
		var o map[string]any
		if yaml.Unmarshal([]byte(doc), &o) != nil {
			continue
		}
		kind, _ := o["kind"].(string)
		meta, _ := o["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		namespace, _ := meta["namespace"].(string)
		if kind == "" || name == "" {
			continue
		}
		k := kind + " " + name
		if namespace != "" {
			k = kind + " " + namespace + "/" + name
		}
		if kind == "Secret" {
			out[k] = rendered{shown: tidy(hideSecretValues(doc)), raw: tidy(doc)}
			continue
		}
		keys, docs, objs = append(keys, k), append(docs, doc), append(objs, o)
	}
	for i, o := range kube.MaskObjects(objs, manifest, values...) {
		raw := tidy(docs[i])
		// An object with nothing to mask keeps its text as rendered.
		if reflect.DeepEqual(o, objs[i]) {
			out[keys[i]] = rendered{shown: raw, raw: raw}
			continue
		}
		b, err := yaml.Marshal(o)
		if err != nil {
			out[keys[i]] = rendered{shown: "# values hidden by 221B", raw: raw}
			continue
		}
		out[keys[i]] = rendered{shown: tidy(string(b)), raw: raw}
	}
	return out
}

// tidy trims an object's text. Helm writes "# Source: chart/templates/x.yaml"
// above each object; a renamed template is not a change.
func tidy(doc string) string {
	var kept []string
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, "# Source:") {
			continue
		}
		kept = append(kept, strings.TrimRight(l, " \t"))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// hideSecretValues replaces a Secret's values in a manifest document with
// their sizes, so a diff says a value changed, never what it is (X-01).
func hideSecretValues(doc string) string {
	var o map[string]any
	if yaml.Unmarshal([]byte(doc), &o) != nil {
		return "# Secret: values hidden by 221B\n"
	}
	for _, key := range []string{"data", "stringData"} {
		m, ok := o[key].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			s, _ := v.(string)
			m[k] = fmt.Sprintf("<%d bytes, hidden by 221B>", len(s))
		}
	}
	b, err := yaml.Marshal(o)
	if err != nil {
		return "# Secret: values hidden by 221B\n"
	}
	return string(b)
}
