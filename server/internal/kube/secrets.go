package kube

import (
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/ucesys/221b/server/internal/scrub"
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
	// Cert is what the certificate of a Secret that serves TLS says of
	// itself: the public half only, read from tls.crt (P4-02). Nothing of
	// tls.key is read.
	Cert *CertInfo
}

// CertInfo is the public facts of a certificate: whom it names, who
// issued it, and when it is valid.
type CertInfo struct {
	Subject   string    `json:"subject,omitempty"`
	Issuer    string    `json:"issuer,omitempty"`
	DNSNames  []string  `json:"dnsNames"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// Error says why tls.crt could not be read as a certificate.
	Error string `json:"error,omitempty"`
}

// certInfo reads the first certificate of a PEM bundle.
func certInfo(pemBytes []byte) *CertInfo {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return &CertInfo{DNSNames: []string{}, Error: "tls.crt holds no PEM certificate"}
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return &CertInfo{DNSNames: []string{}, Error: "tls.crt is not a valid certificate"}
	}
	names := append([]string{}, c.DNSNames...)
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = []string{c.Subject.CommonName}
	}
	return &CertInfo{Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName, DNSNames: names, NotBefore: c.NotBefore.UTC(), NotAfter: c.NotAfter.UTC()}
}

// DeepCopyObject implements runtime.Object.
func (s *SecretSummary) DeepCopyObject() runtime.Object {
	if s == nil {
		return nil
	}
	out := *s
	out.ObjectMeta = *s.ObjectMeta.DeepCopy()
	out.Keys = append([]KeySize(nil), s.Keys...)
	if s.Cert != nil {
		c := *s.Cert
		c.DNSNames = append([]string(nil), s.Cert.DNSNames...)
		out.Cert = &c
	}
	if s.Release != nil {
		r := *s.Release
		r.Resources = append([]ReleaseResource(nil), s.Release.Resources...)
		r.Images = append([]string(nil), s.Release.Images...)
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
	// Images are the container images the manifest names, each once, as
	// written (HA-21): what the check for newer images looks up.
	Images []string `json:"images,omitempty"`
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
	out.Annotations = HideSecretValues(out.Annotations, secretValues(valuesOf(s.Type, s.Namespace, s.Data), s.StringData))
	holds := func(key string) bool {
		_, data := s.Data[key]
		_, text := s.StringData[key]
		return data || text
	}
	// Ingress controllers take any Secret with the two keys, whatever its
	// type.
	if s.Type == corev1.SecretTypeTLS || (holds(corev1.TLSCertKey) && holds(corev1.TLSPrivateKeyKey)) {
		if crt, ok := s.Data[corev1.TLSCertKey]; ok {
			out.Cert = certInfo(crt)
		} else if crt, ok := s.StringData[corev1.TLSCertKey]; ok {
			out.Cert = certInfo([]byte(crt))
		}
	}
	if s.Type == HelmSecretType {
		out.Release = releaseOf(s)
	}
	return out, nil
}

// decodeRelease reads a release record; tests replace it.
var decodeRelease = DecodeHelmRelease

// releaseOf reads the release a Helm Secret records, or nil. It runs in the
// Secret informer's transform, where a panic would end the process on
// every relist: a record that cannot be read, however it fails, costs that
// record its release and nothing else (kube#1). The log names the Secret,
// never what it holds.
func releaseOf(s *corev1.Secret) (r *HelmRelease) {
	defer func() {
		if p := recover(); p != nil {
			slog.Warn("a Helm release record could not be read; its release is not shown", "namespace", s.Namespace, "secret", s.Name)
			r = nil
		}
	}()
	r, err := decodeRelease(s.Data["release"])
	if err != nil {
		return nil
	}
	return r
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

// valuesOf is a Secret's data without what is public anyway: a ServiceAccount
// token's namespace key, when it says only the Secret's own namespace. Looked
// for in the annotations it would hide the ServiceAccount's name wherever
// that contains the namespace. Every other value stays.
func valuesOf(t corev1.SecretType, namespace string, data map[string][]byte) map[string][]byte {
	if t != corev1.SecretTypeServiceAccountToken || namespace == "" || string(data[corev1.ServiceAccountNamespaceKey]) != namespace {
		return data
	}
	out := make(map[string][]byte, len(data))
	for k, v := range data {
		if k != corev1.ServiceAccountNamespaceKey {
			out[k] = v
		}
	}
	return out
}

// secretValues lists every value a Secret holds, in the forms an annotation
// might carry it: raw, base64, and escaped as a JSON string is, with and
// without HTML escaping (kube#2).
func secretValues(data map[string][]byte, stringData map[string]string) [][]byte {
	var out [][]byte
	add := func(v []byte) {
		out = append(out, v, []byte(base64.StdEncoding.EncodeToString(v)))
		for _, html := range []bool{true, false} {
			if e := jsonEscaped(v, html); !bytes.Equal(e, v) {
				out = append(out, e)
			}
		}
	}
	for _, v := range data {
		if len(v) > 0 {
			add(v)
		}
	}
	for _, v := range stringData {
		if v != "" {
			add([]byte(v))
		}
	}
	return out
}

// jsonEscaped is a value as it stands inside a JSON string, quotes left out.
func jsonEscaped(v []byte, escapeHTML bool) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	if enc.Encode(string(v)) != nil {
		return v
	}
	e := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	return e[1 : len(e)-1]
}

// wholeObjectAnnotations are the annotations tools keep the whole applied
// object in, values included: hidden whatever they hold (kube#2).
var wholeObjectAnnotations = map[string]bool{
	LastApplied:                       true,
	"kapp.k14s.io/original":           true,
	"kapp.k14s.io/original-diff":      true,
	"objectset.rio.cattle.io/applied": true,
}

// HideSecretValues returns a Secret's annotations with every value that
// could carry a Secret value replaced by its size (X-01): those tools keep
// the whole applied object in (the last-applied configuration, kapp's
// original, Rancher's applied), and any annotation holding one of the
// Secret's values raw, in base64 or escaped as JSON, or holding JSON with
// a string that holds one (kube#2). Other annotations stay.
func HideSecretValues(annotations map[string]string, values [][]byte) map[string]string {
	if annotations == nil {
		return nil
	}
	out := make(map[string]string, len(annotations))
	for k, v := range annotations {
		if wholeObjectAnnotations[k] || holdsValue(v, values) {
			out[k] = fmt.Sprintf("<%d bytes, hidden by 221B>", len(v))
		} else {
			out[k] = v
		}
	}
	return out
}

// holdsValue says whether an annotation holds one of the values: in its
// text, or in a string of the JSON it holds.
func holdsValue(v string, values [][]byte) bool {
	for _, secret := range values {
		if bytes.Contains([]byte(v), secret) {
			return true
		}
	}
	t := strings.TrimSpace(v)
	if t == "" || t[0] != '{' && t[0] != '[' && t[0] != '"' {
		return false
	}
	var doc any
	if json.Unmarshal([]byte(t), &doc) != nil {
		return false
	}
	found := false
	var walk func(x any)
	walk = func(x any) {
		switch y := x.(type) {
		case map[string]any:
			for k, val := range y {
				walk(k)
				walk(val)
			}
		case []any:
			for _, val := range y {
				walk(val)
			}
		case string:
			for _, secret := range values {
				if !found && strings.Contains(y, string(secret)) {
					found = true
				}
			}
		}
	}
	walk(doc)
	return found
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
		// Values are the chart's defaults, which its notes can print as
		// well as what was installed.
		Values map[string]any `json:"values,omitempty"`
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
		// A Secret holds at most 1 MiB, and Helm's records, chart and all,
		// inflate to a few MiB; one that inflates past maxRecord is refused
		// unread (kube#3).
		if raw, err = io.ReadAll(io.LimitReader(zr, maxRecord+1)); err != nil {
			return nil, err
		}
		if len(raw) > maxRecord {
			return nil, fmt.Errorf("the release record inflates past %d MiB", maxRecord>>20)
		}
	}
	var rec HelmRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	r := &HelmRelease{
		Name: rec.Name, Namespace: rec.Namespace, Revision: rec.Version,
		Status: rec.Info.Status, Chart: rec.Chart.Metadata.Name, Version: rec.Chart.Metadata.Version,
		App: rec.Chart.Metadata.AppVersion, Desc: cut(rec.Info.Description, maxDescription),
		Notes:    MaskNotes(rec.Info.Notes, rec.Config, rec.Chart.Values, rec.Manifest),
		Deployed: parseHelmTime(rec.Info.LastDeployed), First: parseHelmTime(rec.Info.FirstDeployed),
	}
	if len(rec.Config) > 0 {
		var masked []string
		values := MaskValues(rec.Config, "", &masked)
		if b, err := json.Marshal(values); err == nil {
			r.Values = b
		}
		if len(r.Values) > maxValues {
			r.Values, _ = json.Marshal(fmt.Sprintf("(hidden by 221B: these values take %d KiB, more than the %d KiB it keeps of a release)", len(r.Values)>>10, maxValues>>10))
		}
		sort.Strings(masked)
		if len(masked) > maxMasked {
			masked = append(masked[:maxMasked:maxMasked], fmt.Sprintf("(and %d more)", len(masked)-maxMasked))
		}
		r.Masked = masked
	}
	r.Resources, r.Images = manifestResources(rec.Manifest)
	return r, nil
}

// What the cache keeps of a release record is bounded, so a small Secret
// cannot make it keep much more than the Secret holds (kube#3).
const (
	// maxRecord is the most a record may inflate to.
	maxRecord = 32 << 20
	// maxNotes is the most of a release's notes kept: longer notes, before
	// or after masking, are replaced by NotesTooLong.
	maxNotes = 64 << 10
	// maxValues is the most of a release's masked values kept, as JSON.
	maxValues = 256 << 10
	// maxMasked is the most masked paths listed.
	maxMasked = 1000
	// maxKnown is the most distinct credentials notes are masked of: the
	// notes of a release with more are hidden whole.
	maxKnown = 1000
	// maxResources and maxImages bound the lists read from a manifest.
	maxResources = 5000
	maxImages    = 500
	// maxDescription is the most of a revision's description kept.
	maxDescription = 4 << 10
)

// NotesTooLong stands for notes longer than the console keeps.
const NotesTooLong = "(hidden by 221B: these notes are longer than the 64 KiB it keeps of a release's notes)"

// cut shortens s to at most n bytes and an ellipsis, at a character
// boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
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

// helmCredential matches the value names that hold credentials or key
// material in Helm values, beyond what scrub.SecretName takes for a
// secret anywhere.
var helmCredential = regexp.MustCompile(`(?i)(private|cert|license)`)

// credentialKey says whether a value's name usually holds a credential.
func credentialKey(name string) bool {
	return scrub.SecretName(name) || helmCredential.MatchString(name)
}

// MaskedValue replaces a credential in masked values.
const MaskedValue = "•••••• (hidden by 221B)"

// MaskValues returns a copy of Helm values with every string or number
// under a credential-looking key replaced, in lists and maps under it too,
// and with the value of a {name, value} entry whose name looks like one,
// the form Helm charts take environment variables in. A string that holds
// a credential under any key, a URL with a password say, goes as well. It
// lists the keys it masked (P2-07). Keys are paths like "auth.password".
func MaskValues(v any, path string, masked *[]string) any {
	m := masker{paths: *masked}
	out := m.mask(v, path)
	*masked = m.paths
	return out
}

// masker walks Helm values, keeping the paths it masked and what was
// under them.
type masker struct {
	paths  []string
	values []string
	// refs says whether a field whose name looks like a credential only
	// refers to one; such a field is masked by the ordinary rules. Nil for
	// Helm values.
	refs func(key string, value any) bool
}

// isRef says whether a field of a Kubernetes object refers to a Secret or
// a token rather than holding one (kube#4): secretName, or a reference
// object or list of them under secret, secretRef, *SecretRef and the
// like. A plain value under those names is still masked.
func isRef(key string, value any) bool {
	if refNames[key] {
		return true
	}
	if !refObjects[key] && !strings.HasSuffix(key, "SecretRef") {
		return false
	}
	switch x := value.(type) {
	case map[string]any:
		return true
	case []any:
		for _, e := range x {
			if _, ok := e.(map[string]any); !ok {
				return false
			}
		}
		return len(x) > 0
	}
	return false
}

// refNames are fields that name a Secret, or say whether a token is
// mounted, by their value.
var refNames = map[string]bool{"secretName": true, "automountServiceAccountToken": true}

// refObjects are fields that hold a reference to a Secret or a token: a
// Pod volume's or a projected source's secret, an environment variable's
// secretKeyRef, envFrom's secretRef, a CSI volume's nodePublishSecretRef,
// a ServiceAccount's secrets and imagePullSecrets, a projected
// serviceAccountToken.
var refObjects = map[string]bool{"secret": true, "secretKeyRef": true, "secretRef": true, "imagePullSecrets": true,
	"secrets": true, "serviceAccountToken": true}

func (m *masker) hide(path string, value any) string {
	m.paths = append(m.paths, path)
	if s, ok := value.(string); ok {
		m.values = append(m.values, s)
	} else if b, err := json.Marshal(value); err == nil {
		m.values = append(m.values, string(b))
	}
	return MaskedValue
}

func (m *masker) mask(v any, path string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		name, _ := x["name"].(string)
		for k, val := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			switch {
			case credentialKey(k) && (m.refs == nil || !m.refs(k, val)):
				out[k] = m.maskAll(val, p)
			case k == "value" && credentialKey(name):
				out[k] = m.maskAll(val, strings.TrimPrefix(path+"."+name, "."))
			default:
				out[k] = m.mask(val, p)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			// The argument after a flag that names a credential is one
			// (kube#5): args: ["--password", "…"].
			if s, ok := val.(string); ok && s != "" && i > 0 && secretFlag(x[i-1]) {
				out[i] = m.hide(path, s)
				continue
			}
			out[i] = m.mask(val, path)
		}
		return out
	case string:
		if strings.Contains(x, "PRIVATE KEY-----") || scrub.Text(x) != x {
			return m.hide(path, x)
		}
		return x
	default:
		return v
	}
}

// maskAll replaces every string and number in a value under a
// credential-looking key: passwords: [...] or password: 12345678 as much as
// password: "...". Booleans and empty values say nothing and are kept.
func (m *masker) maskAll(v any, path string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if m.refs != nil && m.refs(k, val) {
				// A reference under a credential-looking name: a
				// credentials block's secretRef (kube#4).
				out[k] = m.mask(val, path+"."+k)
				continue
			}
			out[k] = m.maskAll(val, path+"."+k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = m.maskAll(val, fmt.Sprintf("%s[%d]", path, i))
		}
		return out
	case string:
		if x == "" {
			return x
		}
	case nil, bool:
		return v
	}
	return m.hide(path, v)
}

// secretFlag says whether a command-line argument is a flag that names a
// credential and leaves its value to the next argument: --password,
// -db-token.
func secretFlag(v any) bool {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "-") || strings.ContainsAny(s, "= ") {
		return false
	}
	name := strings.TrimLeft(s, "-")
	return name != "" && credentialKey(name)
}

// shortCredential is the length below which a credential is not replaced
// wherever it occurs: it would take pieces out of ordinary words.
const shortCredential = 3

// NotesHidden stands for notes that print a credential too short to mask.
const NotesHidden = "(hidden by 221B: these notes print a value that cannot be masked)"

// MaskNotes hides what a release's rendered notes say of its credentials,
// which they can print under no name at all: every value MaskValues hides
// in the values it was installed with or the chart's defaults, and every
// value of a Secret in its manifest, also in base64, and what looks like a
// credential by itself. Notes that print a value too short to find safely
// are replaced whole (X-01).
func MaskNotes(notes string, config, defaults map[string]any, manifest string) string {
	if notes == "" {
		return ""
	}
	// Notes longer than the console keeps are not read at all (kube#3).
	if len(notes) > maxNotes {
		return NotesTooLong
	}
	m := &masker{}
	if config != nil {
		m.mask(config, "")
	}
	if defaults != nil {
		m.mask(defaults, "")
	}
	known := distinct(append(m.values, manifestSecrets(manifest)...))
	if len(known) > maxKnown {
		return NotesHidden
	}
	out := scrub.Text(notes)
	for _, v := range known {
		if len(v) < shortCredential {
			if v != "" && wholeWord(out, v) {
				return NotesHidden
			}
			continue
		}
		out = strings.ReplaceAll(out, v, MaskedValue)
		out = strings.ReplaceAll(out, base64.StdEncoding.EncodeToString([]byte(v)), MaskedValue)
		// Each value masked can make the notes longer.
		if len(out) > maxNotes {
			return NotesTooLong
		}
	}
	return out
}

// distinct lists each value once, the longest first, so a longer value is
// replaced before one it holds.
func distinct(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// MaskObjects masks the objects of a rendered manifest, Secrets left out
// (they are hidden whole elsewhere), as a release's values and notes are
// masked: every value under a credential-looking name, the value of a
// {name, value} pair named like one, the argument after a flag named like
// one, a string that looks like a credential by itself, and wherever it
// occurs, also in base64, any value masked so, held by one of the
// manifest's Secrets or masked in the release's values given (X-01,
// kube#5). A field that only refers to a Secret is kept (kube#4). The
// objects are returned as copies.
func MaskObjects(objs []map[string]any, manifest string, values ...map[string]any) []map[string]any {
	m := &masker{refs: isRef}
	out := make([]map[string]any, len(objs))
	for i, o := range objs {
		out[i], _ = m.mask(o, "").(map[string]any)
	}
	fromValues := &masker{}
	for _, v := range values {
		if v != nil {
			fromValues.mask(v, "")
		}
	}
	var known []string
	for _, k := range distinct(slices.Concat(m.values, fromValues.values, manifestSecrets(manifest))) {
		if replaceable(k) {
			known = append(known, k)
		}
	}
	var hide func(v any) any
	hide = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				x[k] = hide(val)
			}
		case []any:
			for i, val := range x {
				x[i] = hide(val)
			}
		case string:
			for _, k := range known {
				x = strings.ReplaceAll(x, k, MaskedValue)
				x = strings.ReplaceAll(x, base64.StdEncoding.EncodeToString([]byte(k)), MaskedValue)
			}
			return x
		}
		return v
	}
	for _, o := range out {
		hide(o)
	}
	return out
}

// replaceable says whether a credential is masked wherever it occurs in a
// manifest's objects: not a short one, nor a short number such as a mode
// (420), a port or a key size (2048), which would mask unrelated text
// (kube#4). It is still masked where it stands.
func replaceable(v string) bool {
	if len(v) < 4 {
		return false
	}
	digits := strings.TrimLeft(v, "0123456789") == ""
	return !digits || len(v) >= 6
}

// wholeWord says whether s holds word with no letter or digit against it.
// Bytes that are not UTF-8 count as neither, so any value, text or not,
// is looked for without a pattern that could fail to compile (kube#1).
func wholeWord(s, word string) bool {
	if word == "" {
		return false
	}
	wordChar := func(r rune, size int) bool {
		return size > 0 && r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsNumber(r))
	}
	for from := 0; from <= len(s)-len(word); {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return false
		}
		i += from
		before, bs := utf8.DecodeLastRuneInString(s[:i])
		after, as := utf8.DecodeRuneInString(s[i+len(word):])
		if !wordChar(before, bs) && !wordChar(after, as) {
			return true
		}
		from = i + 1
	}
	return false
}

// manifestSecrets lists the values of the Secrets a release's manifest
// creates, which is where a chart keeps a password it made up itself.
func manifestSecrets(manifest string) []string {
	var out []string
	for _, doc := range strings.Split(manifest, "\n---") {
		if !strings.Contains(doc, "Secret") {
			continue
		}
		var secret struct {
			Kind       string            `json:"kind"`
			Data       map[string]string `json:"data"`
			StringData map[string]string `json:"stringData"`
		}
		if err := yaml.Unmarshal([]byte(doc), &secret); err != nil || secret.Kind != "Secret" {
			continue
		}
		for _, v := range secret.StringData {
			out = append(out, v)
		}
		for _, v := range secret.Data {
			if b, err := base64.StdEncoding.DecodeString(v); err == nil {
				out = append(out, string(b))
			}
		}
	}
	return out
}

// manifestResources lists the objects a release's manifest creates, and
// the images its containers run, each once, sorted.
func manifestResources(manifest string) ([]ReleaseResource, []string) {
	if manifest == "" {
		return nil, nil
	}
	var out []ReleaseResource
	seen := map[string]bool{}
	var images []string
	for _, doc := range strings.Split(manifest, "\n---") {
		// A release of more objects than any chart makes keeps the first
		// (kube#3).
		if len(out) >= maxResources {
			break
		}
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
		// Only the kinds that hold containers are read whole.
		if !strings.Contains(doc, "image:") {
			continue
		}
		var body map[string]any
		if yaml.Unmarshal([]byte(doc), &body) != nil {
			continue
		}
		containerImages(body, func(img string) {
			if !seen[img] && len(images) < maxImages {
				seen[img] = true
				images = append(images, img)
			}
		})
	}
	sort.Strings(images)
	return out, images
}

// containerImages calls f with the image of every container,
// initContainer and ephemeralContainer wherever it is in an object: a
// Pod's spec, a workload's template, a CronJob's job template.
func containerImages(v any, f func(image string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if list, ok := child.([]any); ok && (k == "containers" || k == "initContainers" || k == "ephemeralContainers") {
				for _, c := range list {
					if cm, ok := c.(map[string]any); ok {
						if img, _ := cm["image"].(string); img != "" {
							f(img)
						}
					}
				}
				continue
			}
			containerImages(child, f)
		}
	case []any:
		for _, child := range x {
			containerImages(child, f)
		}
	}
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
