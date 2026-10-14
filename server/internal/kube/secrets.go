package kube

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// KeySize is one key of a Secret or ConfigMap and the size of its value.
// The value itself is never kept (X-01).
type KeySize struct {
	Key   string `json:"key"`
	Bytes int    `json:"bytes"`
}

// SecretSummary is what the cache holds of a Secret: its metadata, type
// and keys with sizes. A Helm release record is decoded into Release and
// the record itself dropped. Secret values are gone by the time the
// object enters the cache.
type SecretSummary struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Type      corev1.SecretType
	Immutable bool
	Keys      []KeySize
	Release   *HelmRelease
}

// DeepCopyObject implements runtime.Object.
func (s *SecretSummary) DeepCopyObject() runtime.Object {
	if s == nil {
		return nil
	}
	out := *s
	out.ObjectMeta = *s.ObjectMeta.DeepCopy()
	out.Keys = append([]KeySize(nil), s.Keys...)
	if s.Release != nil {
		r := *s.Release
		r.Resources = append([]ReleaseResource(nil), s.Release.Resources...)
		out.Release = &r
	}
	return &out
}

// ConfigMapSummary is what the cache holds of a ConfigMap: its metadata
// and keys with sizes.
type ConfigMapSummary struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Immutable bool
	Keys      []KeySize
}

// DeepCopyObject implements runtime.Object.
func (c *ConfigMapSummary) DeepCopyObject() runtime.Object {
	if c == nil {
		return nil
	}
	out := *c
	out.ObjectMeta = *c.ObjectMeta.DeepCopy()
	out.Keys = append([]KeySize(nil), c.Keys...)
	return &out
}

var (
	_ runtime.Object = (*SecretSummary)(nil)
	_ runtime.Object = (*ConfigMapSummary)(nil)
)

// HelmRelease is one revision of a Helm release, as Helm records it in a
// Secret of type helm.sh/release.v1. Only what the Helm tab shows is kept:
// no manifest, no chart templates, and values with credentials masked.
type HelmRelease struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Revision  int               `json:"revision"`
	Status    string            `json:"status"`
	Chart     string            `json:"chart"`
	Version   string            `json:"chartVersion"`
	App       string            `json:"appVersion"`
	Desc      string            `json:"description"`
	Notes     string            `json:"notes,omitempty"`
	Deployed  *time.Time        `json:"deployed,omitempty"`
	First     *time.Time        `json:"firstDeployed,omitempty"`
	Values    json.RawMessage   `json:"values,omitempty"`
	Masked    []string          `json:"maskedValues,omitempty"`
	Resources []ReleaseResource `json:"resources,omitempty"`
}

// ReleaseResource is one object a release's manifest creates.
type ReleaseResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// HelmSecretType is the Secret type Helm 3 and 4 use for release records.
const HelmSecretType corev1.SecretType = "helm.sh/release.v1"

// SummarizeSecret replaces a Secret with its summary before it is cached.
func SummarizeSecret(obj any) (any, error) {
	s, ok := obj.(*corev1.Secret)
	if !ok {
		return obj, nil
	}
	out := &SecretSummary{
		TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: s.ObjectMeta,
		Type:       s.Type,
		Immutable:  s.Immutable != nil && *s.Immutable,
		Keys:       keySizes(s.Data, s.StringData),
	}
	out.ManagedFields = nil
	out.Annotations = HideSecretValues(out.Annotations, secretValues(s.Data, s.StringData))
	if s.Type == HelmSecretType {
		if r, err := DecodeHelmRelease(s.Data["release"]); err == nil {
			out.Release = r
		}
	}
	return out, nil
}

// SummarizeConfigMap replaces a ConfigMap with its summary before it is
// cached.
func SummarizeConfigMap(obj any) (any, error) {
	c, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return obj, nil
	}
	keys := make(map[string][]byte, len(c.Data)+len(c.BinaryData))
	for k, v := range c.Data {
		keys[k] = []byte(v)
	}
	for k, v := range c.BinaryData {
		keys[k] = v
	}
	out := &ConfigMapSummary{
		TypeMeta:   metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
		ObjectMeta: c.ObjectMeta,
		Immutable:  c.Immutable != nil && *c.Immutable,
		Keys:       keySizes(keys, nil),
	}
	out.ManagedFields = nil
	out.Annotations = trimAnnotations(out.Annotations)
	return out, nil
}

// LastApplied is the annotation kubectl apply keeps the whole object in,
// values included.
const LastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// secretValues lists every value a Secret holds, in the forms an annotation
// might carry it: raw and base64.
func secretValues(data map[string][]byte, stringData map[string]string) [][]byte {
	var out [][]byte
	for _, v := range data {
		if len(v) > 0 {
			out = append(out, v, []byte(base64.StdEncoding.EncodeToString(v)))
		}
	}
	for _, v := range stringData {
		if v != "" {
			out = append(out, []byte(v), []byte(base64.StdEncoding.EncodeToString([]byte(v))))
		}
	}
	return out
}

// HideSecretValues returns a Secret's annotations with every value that
// could carry a Secret value replaced by its size (X-01): the last-applied
// configuration, which kubectl apply writes with the whole object in it,
// and any annotation holding one of the Secret's values in raw or base64
// form, which tools such as kapp keep. Other annotations stay.
func HideSecretValues(annotations map[string]string, values [][]byte) map[string]string {
	if annotations == nil {
		return nil
	}
	out := make(map[string]string, len(annotations))
	for k, v := range annotations {
		hide := k == LastApplied
		for _, secret := range values {
			if hide {
				break
			}
			hide = bytes.Contains([]byte(v), secret)
		}
		if hide {
			out[k] = fmt.Sprintf("<%d bytes, hidden by 221B>", len(v))
		} else {
			out[k] = v
		}
	}
	return out
}

func keySizes(data map[string][]byte, stringData map[string]string) []KeySize {
	out := make([]KeySize, 0, len(data)+len(stringData))
	for k, v := range data {
		out = append(out, KeySize{Key: k, Bytes: len(v)})
	}
	for k, v := range stringData {
		out = append(out, KeySize{Key: k, Bytes: len(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// HelmRecord is the part of Helm's release record the console reads. The
// simulator and tests write records in the same shape.
type HelmRecord struct {
	Name      string         `json:"name"`
	Namespace string         `json:"namespace"`
	Version   int            `json:"version"`
	Info      HelmRecordInfo `json:"info"`
	Chart     struct {
		Metadata HelmChartMetadata `json:"metadata"`
	} `json:"chart"`
	Config   map[string]any `json:"config"`
	Manifest string         `json:"manifest"`
}

// HelmRecordInfo is a record's status and times.
type HelmRecordInfo struct {
	Status        string `json:"status"`
	Description   string `json:"description"`
	Notes         string `json:"notes"`
	FirstDeployed string `json:"first_deployed"`
	LastDeployed  string `json:"last_deployed"`
}

// HelmChartMetadata names the chart a record was made from.
type HelmChartMetadata struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	AppVersion string `json:"appVersion"`
}

// EncodeHelmRelease writes a record the way Helm stores it in a Secret:
// base64 text of a gzip stream of JSON.
func EncodeHelmRelease(rec HelmRecord) []byte {
	raw, _ := json.Marshal(rec)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// DecodeHelmRelease reads a Helm release record: base64 text of a gzip
// stream of JSON (Helm 3 and 4), or plain JSON.
func DecodeHelmRelease(data []byte) (*HelmRelease, error) {
	raw := data
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data))); err == nil {
		raw = decoded
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		// A release record is small once the chart is dropped; 64 MB is far
		// beyond anything Helm stores.
		if raw, err = io.ReadAll(io.LimitReader(zr, 64<<20)); err != nil {
			return nil, err
		}
	}
	var rec HelmRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	r := &HelmRelease{
		Name: rec.Name, Namespace: rec.Namespace, Revision: rec.Version,
		Status: rec.Info.Status, Chart: rec.Chart.Metadata.Name, Version: rec.Chart.Metadata.Version,
		App: rec.Chart.Metadata.AppVersion, Desc: rec.Info.Description, Notes: rec.Info.Notes,
		Deployed: parseHelmTime(rec.Info.LastDeployed), First: parseHelmTime(rec.Info.FirstDeployed),
	}
	if len(rec.Config) > 0 {
		var masked []string
		values := MaskValues(rec.Config, "", &masked)
		if b, err := json.Marshal(values); err == nil {
			r.Values = b
		}
		sort.Strings(masked)
		r.Masked = masked
	}
	r.Resources = manifestResources(rec.Manifest)
	return r, nil
}

func parseHelmTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// credentialKey matches value names that usually hold credentials.
var credentialKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|apikey|api_key|api-key|accesskey|access_key|credential|private|cert|auth|license)`)

// MaskedValue replaces a credential in masked values.
const MaskedValue = "•••••• (hidden by 221B)"

// MaskValues returns a copy of Helm values with every string under a
// credential-looking key replaced, and lists the keys it masked (P2-07).
// Keys are paths like "auth.password".
func MaskValues(v any, path string, masked *[]string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if s, isString := val.(string); isString && s != "" && credentialKey.MatchString(k) {
				out[k] = MaskedValue
				*masked = append(*masked, p)
				continue
			}
			out[k] = MaskValues(val, p, masked)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = MaskValues(val, path, masked)
		}
		return out
	case string:
		if strings.Contains(x, "PRIVATE KEY-----") {
			*masked = append(*masked, path)
			return MaskedValue
		}
		return x
	default:
		return v
	}
}

// manifestResources lists the objects a release's manifest creates.
func manifestResources(manifest string) []ReleaseResource {
	if manifest == "" {
		return nil
	}
	var out []ReleaseResource
	for _, doc := range strings.Split(manifest, "\n---") {
		doc = strings.TrimSpace(doc)
		if doc == "" || strings.HasPrefix(doc, "#") && !strings.Contains(doc, "\nkind:") {
			continue
		}
		var head struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil || head.Kind == "" || head.Metadata.Name == "" {
			continue
		}
		out = append(out, ReleaseResource{Kind: head.Kind, Name: head.Metadata.Name, Namespace: head.Metadata.Namespace})
	}
	return out
}

// Secret-type labels the Namespaces tab shows.
var secretTypeNames = map[corev1.SecretType]string{
	corev1.SecretTypeOpaque:              "Opaque",
	corev1.SecretTypeServiceAccountToken: "service account token",
	corev1.SecretTypeDockercfg:           "docker config",
	corev1.SecretTypeDockerConfigJson:    "docker config",
	corev1.SecretTypeBasicAuth:           "basic auth",
	corev1.SecretTypeSSHAuth:             "SSH auth",
	corev1.SecretTypeTLS:                 "TLS",
	corev1.SecretTypeBootstrapToken:      "bootstrap token",
	HelmSecretType:                       "Helm release",
}

// SecretTypeName names a Secret type for people.
func SecretTypeName(t corev1.SecretType) string {
	if n, ok := secretTypeNames[t]; ok {
		return n
	}
	return string(t)
}

// SecretGVK is the Secret kind, for the YAML view's filtering.
var SecretGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
