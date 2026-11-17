package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/watson"
)

// An application's values as the page sees them, and a release adopted
// as an application (asked November 17, 2026, after an upgrade with an
// adopted application's few values reset the rest of the release to the
// chart's defaults): the record's values start from the release's own,
// credentials go to the page masked as the Values tab shows them, and a
// masked value sent back keeps what it hid.

// viewOf is a record as the page sees it: its values with credentials
// masked, and whether it is the console's own release.
func (s *Server) viewOf(rec apps.Record) appView {
	v := appView{Record: rec, Self: s.opts.SelfRelease.Is(rec.Cluster, rec.Namespace, rec.Name)}
	v.Values, v.ValuesMasked = maskRecordValues(rec.Values)
	return v
}

// maskRecordValues masks the credentials in a values file as the
// release's Values tab does (P2-07), and names the keys it masked. A file
// with none is sent as it was typed, comments and all.
func maskRecordValues(text string) (string, []string) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	vals, err := apps.Values(text, nil)
	if err != nil {
		return text, nil
	}
	var masked []string
	out, _ := kube.MaskValues(vals, "", &masked).(map[string]any)
	if len(masked) == 0 {
		return text, nil
	}
	return apps.ValuesYAML(out), masked
}

// restoreMasked puts back what a masked placeholder in values sent from
// the page hid, from the values kept: the placeholder is never saved as a
// value. One with nothing kept at its place is an error.
func restoreMasked(text, kept string) (string, error) {
	if !strings.Contains(text, kube.MaskedValue) {
		return text, nil
	}
	in, err := apps.Values(text, nil)
	if err != nil {
		return "", err
	}
	old, err := apps.Values(kept, nil)
	if err != nil {
		old = map[string]any{}
	}
	var missing []string
	out, _ := restoreValue(in, old, true, "", &missing).(map[string]any)
	if len(missing) > 0 {
		return "", fmt.Errorf("%s still %s the masked placeholder, and the console keeps no value there: type the value", strings.Join(missing, ", "), map[bool]string{true: "holds", false: "hold"}[len(missing) == 1])
	}
	return apps.ValuesYAML(out), nil
}

func restoreValue(v, old any, found bool, path string, missing *[]string) any {
	switch x := v.(type) {
	case string:
		if x != kube.MaskedValue {
			return x
		}
		if !found {
			*missing = append(*missing, strings.TrimPrefix(path, "."))
			return x
		}
		return old
	case map[string]any:
		om, _ := old.(map[string]any)
		out := make(map[string]any, len(x))
		for k, val := range x {
			ov, ok := om[k]
			out[k] = restoreValue(val, ov, ok, path+"."+k, missing)
		}
		return out
	case []any:
		ol, _ := old.([]any)
		out := make([]any, len(x))
		for i, val := range x {
			var ov any
			ok := i < len(ol)
			if ok {
				ov = ol[i]
			}
			out[i] = restoreValue(val, ov, ok, fmt.Sprintf("%s[%d]", path, i), missing)
		}
		return out
	}
	return v
}

// releaseValues reads, as the person, the user-supplied values of a
// release's newest revision (helm get values) and that revision; 0 when
// there is no release.
func (s *Server) releaseValues(ctx context.Context, c watson.Cluster, who watson.Who, ns, name string) (map[string]any, int, error) {
	if c.Config == nil {
		return nil, 0, nil
	}
	pc, err := clientsAs(c.Config, who.Person)
	if err != nil {
		return nil, 0, err
	}
	rels, err := pc.releases(ns, name)
	if err != nil || len(rels) == 0 {
		return nil, 0, err
	}
	return rels[0].Config, rels[0].Version, nil
}

// adoptedValues is the values file of an application made for a release
// that runs already: the release's values, with whatever the person typed
// laid over them, theirs winning.
func adoptedValues(release map[string]any, typed string) (string, error) {
	over, err := apps.Values(typed, nil)
	if err != nil {
		return "", err
	}
	return apps.ValuesYAML(apps.MergeValues(release, over)), nil
}
