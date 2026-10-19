package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/version"
)

const serviceAccountNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// ConfigMapStorage keeps the console's settings in one key of a ConfigMap
// the chart creates in the console's namespace. It needs get and patch on
// that one ConfigMap, and nothing else. Every save carries the version
// (resourceVersion) of the ConfigMap it was based on, so with several
// replicas one never overwrites another's change (P7-02): the API server
// refuses a stale one with a conflict, and the writer reads again.
type ConfigMapStorage struct {
	rest      rest.Interface
	namespace string
	name      string
	key       string
}

// NewConfigMapStorage resolves "namespace/name" or "name" (in the pod's
// own namespace) to a storage backed by that ConfigMap.
func NewConfigMapStorage(cfg *rest.Config, ref string) (*ConfigMapStorage, error) {
	ns, name, ok := strings.Cut(ref, "/")
	if !ok {
		ns, name = "", ref
	}
	if ns == "" {
		b, err := os.ReadFile(serviceAccountNamespace)
		if err != nil {
			return nil, fmt.Errorf("the settings ConfigMap %q has no namespace and the pod's namespace is not mounted: %w", ref, err)
		}
		ns = strings.TrimSpace(string(b))
	}
	if name == "" || ns == "" {
		return nil, fmt.Errorf("settings ConfigMap: give namespace/name, got %q", ref)
	}
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	core, err := corev1client.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for the settings ConfigMap: %w", err)
	}
	return &ConfigMapStorage{rest: core.RESTClient(), namespace: ns, name: name, key: "settings.json"}, nil
}

// Ref returns namespace/name.
func (c *ConfigMapStorage) Ref() string { return c.namespace + "/" + c.name }

func (c *ConfigMapStorage) Kind() string { return "configmap" }

// Load reads the settings and the ConfigMap's version; a ConfigMap
// without them, or none at all, means nothing has been saved yet.
func (c *ConfigMapStorage) Load(ctx context.Context) ([]byte, string, error) {
	b, version, err := c.LoadKey(ctx, c.key)
	if errors.Is(err, kv.ErrNotFound) {
		return nil, version, settings.ErrNotFound
	}
	return b, version, err
}

// Save writes the settings if the ConfigMap is still at version.
func (c *ConfigMapStorage) Save(ctx context.Context, b []byte, version string) (string, error) {
	return c.SaveKey(ctx, c.key, b, version)
}

// configMap is the part of a ConfigMap the storage reads.
type configMap struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// LoadKey reads one key of the ConfigMap (the settings, or Sherlock's
// spend) and the ConfigMap's resourceVersion.
func (c *ConfigMapStorage) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	raw, err := c.rest.Get().Namespace(c.namespace).Resource("configmaps").Name(c.name).Do(ctx).Raw()
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", kv.ErrNotFound
		}
		return nil, "", describeConfigMapError("reading", c.Ref(), err)
	}
	var cm configMap
	if err := json.Unmarshal(raw, &cm); err != nil {
		return nil, "", fmt.Errorf("the settings ConfigMap %s is not valid JSON: %w", c.Ref(), err)
	}
	v, ok := cm.Data[key]
	if !ok {
		return nil, cm.Metadata.ResourceVersion, kv.ErrNotFound
	}
	return []byte(v), cm.Metadata.ResourceVersion, nil
}

// SaveKey writes one key with a merge patch, which leaves the other keys
// and the chart's labels as they are. With a version, the patch carries it
// as the ConfigMap's resourceVersion, which the API server checks: a
// ConfigMap changed since is a conflict (kv.ErrConflict). It returns the
// ConfigMap's new version.
func (c *ConfigMapStorage) SaveKey(ctx context.Context, key string, b []byte, version string) (string, error) {
	body := map[string]any{"data": map[string]string{key: string(b)}}
	if version != "" {
		body["metadata"] = map[string]string{"resourceVersion": version}
	}
	patch, _ := json.Marshal(body)
	raw, err := c.rest.Patch(types.MergePatchType).Namespace(c.namespace).Resource("configmaps").Name(c.name).Body(patch).Do(ctx).Raw()
	if err != nil {
		if apierrors.IsConflict(err) {
			return "", kv.ErrConflict
		}
		return "", describeConfigMapError("saving to", c.Ref(), err)
	}
	var cm configMap
	_ = json.Unmarshal(raw, &cm)
	return cm.Metadata.ResourceVersion, nil
}

func describeConfigMapError(doing, ref string, err error) error {
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("not allowed to %s the settings ConfigMap %s; check the Role the chart installs", strings.TrimSuffix(doing, " to"), ref)
	case apierrors.IsNotFound(err):
		return fmt.Errorf("the settings ConfigMap %s does not exist; the chart creates it", ref)
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return fmt.Errorf("%s the settings ConfigMap %s: %s", doing, ref, status.Status().Message)
	}
	return fmt.Errorf("%s the settings ConfigMap %s: %s", doing, ref, unreachable(err))
}
