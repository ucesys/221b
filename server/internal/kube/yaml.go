package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation/path"
	"sigs.k8s.io/yaml"
)

// ToYAML renders a value as YAML with keys in order, as kubectl does.
func ToYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ObjectYAML reads an object from the API server as it is now and renders
// it as YAML the way `kubectl get -o yaml` does, apart from two changes
// (P2-08): managed fields are left out, and a Secret's values are replaced
// by their sizes, so no value is ever sent (X-01). The read is made as the
// person, so their RBAC decides whether they get it (P3-02).
func (c *Cluster) ObjectYAML(ctx context.Context, p Person, k Kind, namespace, name string) (string, error) {
	// The names go into the request path as they are: a "../secrets/x"
	// would read another kind's object, a Secret unhidden (X-01).
	if err := CheckPathNames(namespace, name); err != nil {
		return "", err
	}
	cs, err := c.as(p)
	if err != nil {
		return "", err
	}
	path := k.Path()
	if k.Namespaced {
		if k.Group == "" {
			path = fmt.Sprintf("/api/%s/namespaces/%s/%s", k.Version, namespace, k.Resource)
		} else {
			path = fmt.Sprintf("/apis/%s/%s/namespaces/%s/%s", k.Group, k.Version, namespace, k.Resource)
		}
	}
	raw, err := cs.CoreV1().RESTClient().Get().AbsPath(path+"/"+name).SetHeader("Accept", "application/json").Do(ctx).Raw()
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("the API server's answer is not JSON: %w", err)
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
	}
	// By what the API server returned, not only what was asked for.
	if k.Kind == "Secret" || obj["kind"] == "Secret" {
		hideSecretData(obj)
	}
	return ToYAML(obj)
}

// CheckPathNames refuses a namespace or name that is not a single path
// segment, such as "..", or one holding a "/" or a "%".
func CheckPathNames(namespace, name string) error {
	if name == "" {
		return apierrors.NewBadRequest("no name given")
	}
	for _, n := range []string{namespace, name} {
		if n == "" {
			continue
		}
		if bad := path.IsValidPathSegmentName(n); len(bad) > 0 {
			return apierrors.NewBadRequest(fmt.Sprintf("%q is not a valid name: %s", n, bad[0]))
		}
	}
	return nil
}

// ErrNotFound is returned for an object that does not exist.
var ErrNotFound = apierrors.NewNotFound(SecretGVK.GroupVersion().WithResource("objects").GroupResource(), "")

// hideSecretData replaces every value in a Secret's data with its size, and
// hides the annotations that could carry a value (the last-applied
// configuration, or any annotation holding one of the values).
func hideSecretData(obj map[string]any) {
	data := map[string][]byte{}
	stringData := map[string]string{}
	for _, field := range []string{"data", "stringData"} {
		m, ok := obj[field].(map[string]any)
		if !ok {
			continue
		}
		for key, v := range m {
			s, _ := v.(string)
			n := len(s)
			if field == "data" {
				if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
					n = len(decoded)
					data[key] = decoded
				} else {
					data[key] = []byte(s)
				}
			} else {
				stringData[key] = s
			}
			m[key] = fmt.Sprintf("<%d bytes, hidden by 221B>", n)
		}
	}
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return
	}
	raw, ok := meta["annotations"].(map[string]any)
	if !ok {
		return
	}
	annotations := make(map[string]string, len(raw))
	for k, v := range raw {
		annotations[k], _ = v.(string)
	}
	hidden := HideSecretValues(annotations, secretValues(data, stringData))
	for k, v := range hidden {
		raw[k] = v
	}
}
