package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
// by their sizes, so no value is ever sent (X-01).
func (c *Cluster) ObjectYAML(ctx context.Context, k Kind, namespace, name string) (string, error) {
	path := k.Path()
	if k.Namespaced {
		if k.Group == "" {
			path = fmt.Sprintf("/api/%s/namespaces/%s/%s", k.Version, namespace, k.Resource)
		} else {
			path = fmt.Sprintf("/apis/%s/%s/namespaces/%s/%s", k.Group, k.Version, namespace, k.Resource)
		}
	}
	raw, err := c.rest.Get().AbsPath(path+"/"+name).SetHeader("Accept", "application/json").Do(ctx).Raw()
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
	if k.Kind == "Secret" {
		hideSecretData(obj)
	}
	return ToYAML(obj)
}

// ErrNotFound is returned for an object that does not exist.
var ErrNotFound = apierrors.NewNotFound(SecretGVK.GroupVersion().WithResource("objects").GroupResource(), "")

// hideSecretData replaces every value in a Secret's data with its size.
func hideSecretData(obj map[string]any) {
	for _, field := range []string{"data", "stringData"} {
		data, ok := obj[field].(map[string]any)
		if !ok {
			continue
		}
		for key, v := range data {
			s, _ := v.(string)
			n := len(s)
			if field == "data" {
				if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
					n = len(decoded)
				}
			}
			data[key] = fmt.Sprintf("<%d bytes, hidden by 221B>", n)
		}
	}
}
