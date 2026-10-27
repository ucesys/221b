package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

// Secret editing (Phase 10, SE-01 to SE-06). An Admin or a SuperAdmin may
// change a Secret's values: replace a key's value, add a key, remove one,
// or make a Secret, each written as them, so Kubernetes decides. Seeing a
// value is another matter: only where the chart turned secrets.reveal on
// does the server hand one to a browser, one key at a time, as the person
// and audited; otherwise values are written blind, as before (X-01).

const (
	ActionSecretReveal = "secret.reveal"
	ActionSecretUpdate = "secret.update"
	ActionSecretCreate = "secret.create"
	// maxSecretBody bounds a Secret edit: Kubernetes takes 1 MB.
	maxSecretBody = 1 << 20
)

func (s *Server) secretsRoutes() {
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"secrets/config", "secrets-config", s.getSecretsConfig)
		s.handle("GET "+base+"secrets/{namespace}/{name}/value", "secret-value", s.getSecretValue)
		s.handle("PUT "+base+"secrets/{namespace}/{name}", "secret-update", s.putSecret)
		s.handle("POST "+base+"secrets/{namespace}", "secret-create", s.postSecret)
	}
}

// secretsConfig says what the person may do with Secrets here.
type secretsConfig struct {
	// Read says Secrets are read at all (secrets.read).
	Read bool `json:"read"`
	// Reveal says the chart lets values be shown (secrets.reveal).
	Reveal bool `json:"reveal"`
	// Edit says the person holds Admin or SuperAdmin; Kubernetes still
	// decides each write.
	Edit bool `json:"edit"`
}

// mayEditSecrets says whether the person holds a role that edits Secrets:
// Admin or SuperAdmin (SE-02).
func (s *Server) mayEditSecrets(r *http.Request) bool {
	if s.opts.Auth == nil {
		return true
	}
	sess, ok := auth.SessionFrom(r.Context())
	return ok && (sess.Admin || slices.Contains(sess.Roles, roles.Admin) || slices.Contains(sess.Roles, roles.SuperAdmin))
}

func (s *Server) getSecretsConfig(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	b, _ := json.Marshal(secretsConfig{Read: cl.SecretsRead(), Reveal: cl.SecretsRead() && s.opts.SecretsReveal, Edit: s.mayEditSecrets(r)})
	writeJSON(w, r, b)
}

// secretReady checks the cluster, the names and the role.
func (s *Server) secretReady(w http.ResponseWriter, r *http.Request, ns, name string) (Cluster, bool) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return nil, false
	}
	if !cl.SecretsRead() {
		jsonError(w, http.StatusNotFound, "Secrets are not read: reading Secrets is off (secrets.read in the chart)")
		return nil, false
	}
	if len(validation.IsDNS1123Label(ns)) > 0 || (name != "" && len(validation.IsDNS1123Subdomain(name)) > 0) {
		jsonError(w, http.StatusBadRequest, "name a Secret and its namespace")
		return nil, false
	}
	if !s.mayEditSecrets(r) {
		jsonError(w, http.StatusForbidden, "only Admins and SuperAdmins see or change a Secret's values")
		return nil, false
	}
	return cl, true
}

// getSecretValue hands one key's value to the browser (SE-03): as text
// when it is text, else base64, and only where secrets.reveal is on.
func (s *Server) getSecretValue(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	cl, ok := s.secretReady(w, r, ns, name)
	if !ok {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		jsonError(w, http.StatusBadRequest, "name the key to show")
		return
	}
	if !s.opts.SecretsReveal {
		s.record(r, ActionSecretReveal, ns+"/"+name, "key="+strconv.Quote(key)+" outcome=denied reason=\"secrets.reveal is off\"")
		jsonError(w, http.StatusForbidden, "this console does not show Secret values: whoever installed it can turn on secrets.reveal in the chart's values")
		return
	}
	sec, err := cl.GetSecret(r.Context(), s.person(r), ns, name)
	if err != nil {
		s.record(r, ActionSecretReveal, ns+"/"+name, "key="+strconv.Quote(key)+" outcome=failed")
		jsonError(w, kubeStatus(err), err.Error())
		return
	}
	v, has := sec.Data[key]
	if !has {
		jsonError(w, http.StatusNotFound, "the Secret has no key "+strconv.Quote(key))
		return
	}
	s.record(r, ActionSecretReveal, ns+"/"+name, "key="+strconv.Quote(key)+" bytes="+strconv.Itoa(len(v)))
	out := struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Base64 bool   `json:"base64,omitempty"`
		Bytes  int    `json:"bytes"`
	}{Key: key, Bytes: len(v)}
	if utf8.Valid(v) && !strings.ContainsRune(string(v), 0) {
		out.Value = string(v)
	} else {
		out.Value, out.Base64 = base64.StdEncoding.EncodeToString(v), true
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, b)
}

// secretEdit is what the page sends: values to set (as typed; base64
// when the key is in Base64) and keys to remove.
type secretEdit struct {
	Set    map[string]string `json:"set,omitempty"`
	Base64 []string          `json:"base64,omitempty"`
	Remove []string          `json:"remove,omitempty"`
	// ResourceVersion, when given, is the one the page read: a Secret
	// changed since is refused rather than overwritten.
	ResourceVersion string `json:"resourceVersion,omitempty"`
	// For a new Secret: its name and type.
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

func readSecretEdit(w http.ResponseWriter, r *http.Request) (secretEdit, bool) {
	var in secretEdit
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the change as application/json")
		return in, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSecretBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the change is not valid: "+err.Error())
		return in, false
	}
	for k := range in.Set {
		if len(validation.IsConfigMapKey(k)) > 0 {
			jsonError(w, http.StatusBadRequest, "a key is letters, digits, dots, dashes and underscores: "+strconv.Quote(k))
			return in, false
		}
	}
	return in, true
}

// decodeSet turns the typed values into bytes.
func decodeSet(in secretEdit) (map[string][]byte, error) {
	out := map[string][]byte{}
	b64 := map[string]bool{}
	for _, k := range in.Base64 {
		b64[k] = true
	}
	for k, v := range in.Set {
		if b64[k] {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("the value of %s is not base64: %w", strconv.Quote(k), err)
			}
			out[k] = b
			continue
		}
		out[k] = []byte(v)
	}
	return out, nil
}

// putSecret changes a Secret's keys (SE-04): the values set replace or
// add keys, the keys removed go; everything else stays as it was.
func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	cl, ok := s.secretReady(w, r, ns, name)
	if !ok {
		return
	}
	in, ok := readSecretEdit(w, r)
	if !ok {
		return
	}
	set, err := decodeSet(in)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(set)+len(in.Remove) == 0 {
		jsonError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	p := s.person(r)
	sec, err := cl.GetSecret(r.Context(), p, ns, name)
	if err != nil {
		jsonError(w, kubeStatus(err), err.Error())
		return
	}
	if in.ResourceVersion != "" && sec.ResourceVersion != in.ResourceVersion {
		jsonError(w, http.StatusConflict, "the Secret changed since you opened it: open it again and make the change on what is there now")
		return
	}
	if sec.Immutable != nil && *sec.Immutable {
		jsonError(w, http.StatusConflict, "the Secret is immutable: Kubernetes refuses to change it")
		return
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for _, k := range in.Remove {
		delete(sec.Data, k)
	}
	for k, v := range set {
		sec.Data[k] = v
	}
	sec.StringData = nil
	detail := editDetail(set, in.Remove)
	if _, err := cl.UpdateSecret(r.Context(), p, sec); err != nil {
		s.record(r, ActionSecretUpdate, ns+"/"+name, detail+" outcome=failed")
		jsonError(w, kubeStatus(err), err.Error())
		return
	}
	s.record(r, ActionSecretUpdate, ns+"/"+name, detail)
	w.WriteHeader(http.StatusNoContent)
}

// postSecret makes a Secret (SE-05): Opaque unless a type is given.
func (s *Server) postSecret(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	cl, ok := s.secretReady(w, r, ns, "")
	if !ok {
		return
	}
	in, ok := readSecretEdit(w, r)
	if !ok {
		return
	}
	if len(validation.IsDNS1123Subdomain(in.Name)) > 0 {
		jsonError(w, http.StatusBadRequest, "a Secret's name is lower-case letters, digits, dots and dashes")
		return
	}
	set, err := decodeSet(in)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	typ := corev1.SecretType(in.Type)
	if typ == "" {
		typ = corev1.SecretTypeOpaque
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: in.Name, Namespace: ns}, Type: typ, Data: set}
	detail := "type=" + strconv.Quote(string(typ)) + " " + editDetail(set, nil)
	if _, err := cl.CreateSecret(r.Context(), s.person(r), sec); err != nil {
		s.record(r, ActionSecretCreate, ns+"/"+in.Name, detail+" outcome=failed")
		jsonError(w, kubeStatus(err), err.Error())
		return
	}
	s.record(r, ActionSecretCreate, ns+"/"+in.Name, detail)
	w.WriteHeader(http.StatusCreated)
}

// editDetail names the keys an edit touched, never their values.
func editDetail(set map[string][]byte, removed []string) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "set=" + strconv.Quote(strings.Join(keys, ","))
	if len(removed) > 0 {
		r := append([]string(nil), removed...)
		sort.Strings(r)
		out += " removed=" + strconv.Quote(strings.Join(r, ","))
	}
	return out
}

// kubeStatus maps the API server's answer to the console's.
func kubeStatus(err error) int {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		switch code := int(st.Status().Code); code {
		case http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadRequest:
			return code
		}
	}
	return http.StatusBadGateway
}
