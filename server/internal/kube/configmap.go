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

	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/version"
)

const serviceAccountNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// ConfigMapStorage keeps the console's settings in one key of a ConfigMap
// the chart creates in the console's namespace. It needs get and patch on
// that one ConfigMap, and nothing else.
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

// Load reads the key; a ConfigMap without it, or none at all, means
// nothing has been saved yet.
func (c *ConfigMapStorage) Load(ctx context.Context) ([]byte, error) {
	raw, err := c.rest.Get().Namespace(c.namespace).Resource("configmaps").Name(c.name).Do(ctx).Raw()
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, settings.ErrNotFound
		}
		return nil, describeConfigMapError("reading", c.Ref(), err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &cm); err != nil {
		return nil, fmt.Errorf("the settings ConfigMap %s is not valid JSON: %w", c.Ref(), err)
	}
	v, ok := cm.Data[c.key]
	if !ok {
		return nil, settings.ErrNotFound
	}
	return []byte(v), nil
}

// Save writes the key with a merge patch, which leaves any other keys and
// the chart's labels as they are.
func (c *ConfigMapStorage) Save(ctx context.Context, b []byte) error {
	patch, _ := json.Marshal(map[string]any{"data": map[string]string{c.key: string(b)}})
	err := c.rest.Patch(types.MergePatchType).Namespace(c.namespace).Resource("configmaps").Name(c.name).Body(patch).Do(ctx).Error()
	if err != nil {
		return describeConfigMapError("saving to", c.Ref(), err)
	}
	return nil
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
