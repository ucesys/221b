package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/version"
)

// --- One flow for findings and cases (Phase 12, wave 6) ---

// ConfigMapObjects keeps objects in ConfigMaps of their own, one each,
// named <prefix><name> in one namespace and labelled
// app.kubernetes.io/instance=<instance> and <label>=<name>: the cases,
// <release>-case-<n> with 221b.io/case=<n>. The console makes and
// deletes them itself, so its Role grants create, list, get, update,
// patch and delete on ConfigMaps in its namespace (resourceNames cannot
// name what does not exist yet); it lists only those with its labels.
type ConfigMapObjects struct {
	rest      rest.Interface
	namespace string
	prefix    string
	instance  string
	label     string
}

// NewConfigMapObjects opens the objects named "namespace/prefix",
// labelled with instance and label.
func NewConfigMapObjects(cfg *rest.Config, ref, instance, label string) (*ConfigMapObjects, error) {
	ns, prefix, ok := strings.Cut(ref, "/")
	if !ok || ns == "" || prefix == "" || instance == "" || label == "" {
		return nil, fmt.Errorf("the cases' ConfigMaps: give namespace/prefix and the instance, got %q and %q", ref, instance)
	}
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	core, err := corev1client.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for the cases' ConfigMaps: %w", err)
	}
	return &ConfigMapObjects{rest: core.RESTClient(), namespace: ns, prefix: prefix, instance: instance, label: label}, nil
}

// Ref names the objects: namespace/prefix*.
func (o *ConfigMapObjects) Ref() string { return o.namespace + "/" + o.prefix + "*" }

func (o *ConfigMapObjects) selector() string {
	return "app.kubernetes.io/instance=" + o.instance + "," + o.label
}

func (o *ConfigMapObjects) Load(ctx context.Context, name string) (map[string][]byte, string, error) {
	raw, err := o.rest.Get().Namespace(o.namespace).Resource("configmaps").Name(o.prefix + name).Do(ctx).Raw()
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", kv.ErrNotFound
		}
		return nil, "", describeConfigMapError("reading", o.namespace+"/"+o.prefix+name, err)
	}
	var cm configMap
	if err := json.Unmarshal(raw, &cm); err != nil {
		return nil, "", fmt.Errorf("the ConfigMap %s%s is not valid JSON: %w", o.prefix, name, err)
	}
	out := make(map[string][]byte, len(cm.Data))
	for k, v := range cm.Data {
		out[k] = []byte(v)
	}
	return out, cm.Metadata.ResourceVersion, nil
}

func (o *ConfigMapObjects) Save(ctx context.Context, name, key string, b []byte, ver string) (string, error) {
	ref := o.namespace + "/" + o.prefix + name
	var raw []byte
	var err error
	if ver == "" {
		body := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": o.prefix + name, "namespace": o.namespace,
				"labels": map[string]string{"app.kubernetes.io/instance": o.instance, "app.kubernetes.io/managed-by": "221b", o.label: name}},
			"data": map[string]string{key: string(b)}}
		payload, _ := json.Marshal(body)
		raw, err = o.rest.Post().Namespace(o.namespace).Resource("configmaps").Body(payload).Do(ctx).Raw()
		if apierrors.IsAlreadyExists(err) {
			return "", kv.ErrConflict
		}
	} else {
		patch, _ := json.Marshal(map[string]any{"data": map[string]string{key: string(b)}, "metadata": map[string]string{"resourceVersion": ver}})
		raw, err = o.rest.Patch(types.MergePatchType).Namespace(o.namespace).Resource("configmaps").Name(o.prefix + name).Body(patch).Do(ctx).Raw()
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return "", kv.ErrConflict
		}
	}
	if err != nil {
		if tooLarge(err) {
			return "", fmt.Errorf("%w: the ConfigMap %s would pass the 1 MiB Kubernetes keeps in one object, saving %s", kv.ErrFull, ref, key)
		}
		return "", describeConfigMapError("saving to", ref, err)
	}
	var cm configMap
	_ = json.Unmarshal(raw, &cm)
	return cm.Metadata.ResourceVersion, nil
}

func (o *ConfigMapObjects) Delete(ctx context.Context, name string) error {
	err := o.rest.Delete().Namespace(o.namespace).Resource("configmaps").Name(o.prefix + name).Do(ctx).Error()
	if err != nil && !apierrors.IsNotFound(err) {
		return describeConfigMapError("deleting", o.namespace+"/"+o.prefix+name, err)
	}
	return nil
}

func (o *ConfigMapObjects) List(ctx context.Context) ([]string, error) {
	raw, err := o.rest.Get().Namespace(o.namespace).Resource("configmaps").Param("labelSelector", o.selector()).Do(ctx).Raw()
	if err != nil {
		return nil, describeConfigMapError("listing", o.Ref(), err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("the list of %s is not valid JSON: %w", o.Ref(), err)
	}
	var out []string
	for _, it := range list.Items {
		if n, ok := strings.CutPrefix(it.Metadata.Name, o.prefix); ok && it.Metadata.Labels[o.label] == n {
			out = append(out, n)
		}
	}
	return out, nil
}

var _ kv.Objects = (*ConfigMapObjects)(nil)
