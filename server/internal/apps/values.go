package apps

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// The values a release has now (helm get values: its user-supplied
// values, release.Config) and the values an application deploys with.
// An upgrade with the application's values alone resets every value the
// release had that they do not repeat to the chart's default, so an
// application adopted from a release starts from the release's values,
// and every deploy card lists the keys it would remove or change.

// MergeValues lays over on base, as helm -f base -f over does: maps are
// merged key by key, anything else in over wins. Neither is changed.
func MergeValues(base, over map[string]any) map[string]any {
	return mergeMaps(base, over)
}

// ValueChanges compares the values a release has now with the values a
// deploy would give it, and lists by path ("image.tag") the keys the
// deploy removes and the keys whose value it changes, sorted. A map
// removed whole is one key; lists are compared whole. Keys it adds are
// not listed, and no value is: only names.
func ValueChanges(now, next map[string]any) (removed, changed []string) {
	a, b := plain(now), plain(next)
	compareValues(a, b, "", &removed, &changed)
	sort.Strings(removed)
	sort.Strings(changed)
	return removed, changed
}

// plain turns values into what JSON makes of them, so 3 from --set and
// 3 from a release's stored JSON compare equal.
func plain(v map[string]any) map[string]any {
	out := map[string]any{}
	if len(v) == 0 {
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

func compareValues(now, next map[string]any, path string, removed, changed *[]string) {
	for k, v := range now {
		p := k
		if path != "" {
			p = path + "." + k
		}
		w, ok := next[k]
		if !ok {
			*removed = append(*removed, p)
			continue
		}
		vm, vIsMap := v.(map[string]any)
		wm, wIsMap := w.(map[string]any)
		if vIsMap && wIsMap {
			compareValues(vm, wm, p, removed, changed)
			continue
		}
		if !reflect.DeepEqual(v, w) {
			*changed = append(*changed, p)
		}
	}
}

// SelfRelease is the Helm release that runs this console, in the
// cluster it runs in: a change to it can take the console down.
type SelfRelease struct {
	Cluster   string
	Namespace string
	Name      string
}

// ParseSelfRelease reads "namespace/name" for the cluster; anything
// else is no release.
func ParseSelfRelease(cluster, v string) SelfRelease {
	ns, name, ok := strings.Cut(strings.TrimSpace(v), "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		return SelfRelease{}
	}
	return SelfRelease{Cluster: cluster, Namespace: ns, Name: name}
}

// Is says whether the release is this console's own.
func (s SelfRelease) Is(cluster, namespace, name string) bool {
	return s.Name != "" && s.Cluster == cluster && s.Namespace == namespace && s.Name == name
}
