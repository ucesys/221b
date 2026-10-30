package apps

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/pmezard/go-difflib/difflib"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// DriftState says how a deployed object compares with its manifest.
const (
	DriftSame    = "same"
	DriftChanged = "changed"
	DriftMissing = "missing"
	DriftUnknown = "unknown"
)

// DriftObject is one object of the release, compared (HA-09).
type DriftObject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	State     string `json:"state"`
	// Fields are the paths that differ, for a changed object; values are
	// never shown, since the object may be a Secret.
	Fields []string `json:"fields,omitempty"`
	Error  string   `json:"error,omitempty"`
	// Diff is what changed, as a unified diff of the fields the manifest
	// sets: the manifest's side against the cluster's (R2-10). A Secret's
	// values show as their sizes.
	Diff string `json:"diff,omitempty"`
}

// Drift is a release's objects compared with what Helm last applied.
type Drift struct {
	Checked time.Time     `json:"checked"`
	Same    int           `json:"same"`
	Changed int           `json:"changed"`
	Missing int           `json:"missing"`
	Unknown int           `json:"unknown"`
	Objects []DriftObject `json:"objects"`
}

// State sums a drift up for the table: in sync, drifted or unknown.
func (d Drift) State() string {
	switch {
	case d.Changed+d.Missing > 0:
		return "drifted"
	case d.Unknown > 0 && d.Same == 0:
		return DriftUnknown
	}
	return "in sync"
}

// CheckDrift reads each object of the manifest from the cluster, as the
// person, and compares what the manifest sets with what is there: a field
// the manifest sets that the cluster has differently is drift; fields the
// cluster added (defaults, status, Helm's own labels) are not. A hook, a
// test or an object Helm keeps out is skipped.
func CheckDrift(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, manifest, namespace string, now time.Time) Drift {
	d := Drift{Checked: now, Objects: []DriftObject{}}
	for _, doc := range splitDocs(manifest) {
		var want map[string]any
		if yaml.Unmarshal([]byte(doc), &want) != nil || want["kind"] == nil {
			continue
		}
		u := &unstructured.Unstructured{Object: want}
		if u.GetKind() == "" || u.GetName() == "" || isHook(u) {
			continue
		}
		o := DriftObject{Kind: u.GetKind(), Name: u.GetName()}
		gvk := u.GroupVersionKind()
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
		if err != nil {
			o.State, o.Error = DriftUnknown, "the cluster does not serve "+gvk.String()
			d.Objects = append(d.Objects, o)
			d.Unknown++
			continue
		}
		var res dynamic.ResourceInterface = dyn.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			ns := u.GetNamespace()
			if ns == "" {
				ns = namespace
			}
			o.Namespace = ns
			res = dyn.Resource(mapping.Resource).Namespace(ns)
		}
		live, err := res.Get(ctx, u.GetName(), metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			o.State = DriftMissing
			d.Missing++
		case err != nil:
			o.State, o.Error = DriftUnknown, err.Error()
			d.Unknown++
		default:
			want := normalize(u.Object)
			fields := compare(want, live.Object, "")
			if len(fields) == 0 {
				o.State = DriftSame
				d.Same++
			} else {
				o.Diff = objectDiff(want, live.Object, o.Kind)
				sort.Strings(fields)
				if len(fields) > 20 {
					fields = append(fields[:20], fmt.Sprintf("and %d more", len(fields)-20))
				}
				o.State, o.Fields = DriftChanged, fields
				d.Changed++
			}
		}
		d.Objects = append(d.Objects, o)
	}
	return d
}

// objectDiff is the manifest's side of an object against the cluster's,
// the cluster's cut down to the fields the manifest sets so defaults and
// status do not show, as a unified diff; a Secret's values are sizes.
func objectDiff(want, live map[string]any, kind string) string {
	left, err := yaml.Marshal(want)
	if err != nil {
		return ""
	}
	right, err := yaml.Marshal(prune(want, live))
	if err != nil {
		return ""
	}
	a, b := string(left), string(right)
	if kind == "Secret" {
		a, b = hideSecretValues(a), hideSecretValues(b)
	}
	text, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(a), B: difflib.SplitLines(b), FromFile: "deployed manifest", ToFile: "in the cluster", Context: 3})
	return text
}

// prune keeps of live the keys want has, table by table; lists of the
// same length are pruned item by item, others kept whole.
func prune(want, live map[string]any) map[string]any {
	out := map[string]any{}
	for k, w := range want {
		l, ok := live[k]
		if !ok {
			continue
		}
		out[k] = pruneAny(w, l)
	}
	return out
}

func pruneAny(want, live any) any {
	switch w := want.(type) {
	case map[string]any:
		if l, ok := live.(map[string]any); ok {
			return prune(w, l)
		}
	case []any:
		if l, ok := live.([]any); ok && len(l) == len(w) {
			out := make([]any, len(l))
			for i := range l {
				out[i] = pruneAny(w[i], l[i])
			}
			return out
		}
	}
	return live
}

func isHook(u *unstructured.Unstructured) bool {
	_, ok := u.GetAnnotations()["helm.sh/hook"]
	return ok
}

// normalize drops from the manifest what the cluster never stores as
// written: a Secret's stringData becomes data, and metadata the server
// owns is left out.
func normalize(want map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range want {
		out[k] = v
	}
	if md, ok := out["metadata"].(map[string]any); ok {
		m := map[string]any{}
		for k, v := range md {
			switch k {
			case "name", "namespace", "labels", "annotations", "finalizers":
				m[k] = v
			}
		}
		out["metadata"] = m
	}
	if sd, ok := out["stringData"].(map[string]any); ok {
		data, _ := out["data"].(map[string]any)
		if data == nil {
			data = map[string]any{}
		}
		for k, v := range sd {
			if s, ok := v.(string); ok {
				data[k] = base64.StdEncoding.EncodeToString([]byte(s))
			}
		}
		out["data"] = data
		delete(out, "stringData")
	}
	delete(out, "status")
	return out
}

// compare lists the paths where want is not contained in live. A nil, an
// empty map or an empty list in want asks for nothing; a list is compared
// element by element, as far as want's goes.
func compare(want, live any, path string) []string {
	switch w := want.(type) {
	case nil:
		return nil
	case map[string]any:
		if len(w) == 0 {
			return nil
		}
		l, ok := live.(map[string]any)
		if !ok {
			return []string{path}
		}
		var out []string
		for k, v := range w {
			p := k
			if path != "" {
				p = path + "." + k
			}
			lv, has := l[k]
			if !has {
				if empty(v) {
					continue
				}
				out = append(out, p)
				continue
			}
			out = append(out, compare(v, lv, p)...)
		}
		return out
	case []any:
		if len(w) == 0 {
			return nil
		}
		l, ok := live.([]any)
		if !ok || len(l) < len(w) {
			return []string{path}
		}
		var out []string
		for i := range w {
			out = append(out, compare(w[i], l[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	if scalar(want) != scalar(live) {
		return []string{path}
	}
	return nil
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case string:
		return x == ""
	}
	return false
}

// scalar is a value as text, so 1 and "1" and 1.0 compare equal, as the
// API server stores them.
func scalar(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	case string:
		return x
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
