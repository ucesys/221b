package apps

import (
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Image is one container image a manifest names (HA-15).
type Image struct {
	// Image is the reference as written: registry/repository:tag@digest.
	Image      string `json:"image"`
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
	// Kind and Name are the object, Container the container, that uses it.
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Container string `json:"container"`
	Init      bool   `json:"init,omitempty"`
}

// Images lists every image a rendered manifest names: each containers,
// initContainers or ephemeralContainers list, wherever it is (a Pod's
// spec, a workload's template, a CronJob's job template).
func Images(manifest string) []Image {
	var out []Image
	for _, doc := range splitDocs(manifest) {
		var o map[string]any
		if yaml.Unmarshal([]byte(doc), &o) != nil || o["kind"] == nil {
			continue
		}
		kind, _ := o["kind"].(string)
		name := ""
		if m, ok := o["metadata"].(map[string]any); ok {
			name, _ = m["name"].(string)
		}
		walkImages(o, func(c map[string]any, init bool) {
			img, _ := c["image"].(string)
			if img == "" {
				return
			}
			cname, _ := c["name"].(string)
			im := parseImage(img)
			im.Kind, im.Name, im.Container, im.Init = kind, name, cname, init
			out = append(out, im)
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Image != out[j].Image {
			return out[i].Image < out[j].Image
		}
		return out[i].Kind+out[i].Name < out[j].Kind+out[j].Name
	})
	return out
}

func walkImages(v any, f func(c map[string]any, init bool)) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "containers" || k == "initContainers" || k == "ephemeralContainers" {
				if list, ok := child.([]any); ok {
					for _, c := range list {
						if cm, ok := c.(map[string]any); ok {
							f(cm, k != "containers")
						}
					}
					continue
				}
			}
			walkImages(child, f)
		}
	case []any:
		for _, child := range x {
			walkImages(child, f)
		}
	}
}

// parseImage splits a reference into its parts, as containerd does.
func parseImage(ref string) Image {
	im := Image{Image: ref}
	rest := ref
	if i := strings.Index(rest, "@"); i >= 0 {
		im.Digest = rest[i+1:]
		rest = rest[:i]
	}
	// The tag is after the last colon, unless that colon is a port in the
	// registry (before the first slash).
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest[i:], "/") {
		im.Tag = rest[i+1:]
		rest = rest[:i]
	}
	first, after, hasSlash := strings.Cut(rest, "/")
	if hasSlash && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		im.Registry, im.Repository = first, after
	} else {
		im.Repository = rest
	}
	return im
}

// splitDocs splits a manifest into its YAML documents. Only the start of
// a document is trimmed: its end keeps the newline that closes a block
// scalar, or a ConfigMap's last text value would lose it and compare as
// changed.
func splitDocs(manifest string) []string {
	var out []string
	for _, doc := range strings.Split("\n"+manifest, "\n---") {
		doc = strings.TrimLeft(doc, " \t\r\n")
		if t := strings.TrimSpace(doc); t == "" || strings.HasPrefix(t, "#") && !strings.Contains(t, "\n") {
			continue
		}
		out = append(out, doc)
	}
	return out
}
