package sim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// What Phase 3 needs from an API server besides reads: Secrets written
// and deleted (the console's connection store), who-am-I and may-I
// reviews (connection tests and per-person access), and a bearer token
// the simulator can be told to require.

// phase3Routes adds those endpoints.
func (s *Sim) phase3Routes(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/namespaces/{ns}/secrets", s.api(s.createSecret))
	m.HandleFunc("PUT /api/v1/namespaces/{ns}/secrets/{name}", s.api(s.updateSecret))
	m.HandleFunc("DELETE /api/v1/namespaces/{ns}/secrets/{name}", s.api(s.deleteSecret))
	m.HandleFunc("POST /apis/authorization.k8s.io/v1/selfsubjectaccessreviews", s.api(s.selfAccessReview))
	m.HandleFunc("POST /apis/authorization.k8s.io/v1/subjectaccessreviews", s.api(s.accessReview))
	m.HandleFunc("POST /apis/authentication.k8s.io/v1/selfsubjectreviews", s.api(s.selfReview))
	m.HandleFunc("POST /sim/token", s.admin(s.setToken))
	m.HandleFunc("POST /sim/deny", s.admin(s.setDeny))
}

// authorized checks the bearer token when one is required. The identity
// is the token's service account when it is a token the simulator knows,
// otherwise the console's.
func (s *Sim) authorized(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	want := s.token
	s.mu.Unlock()
	if want == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == want {
		return true
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	status(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
	return false
}

// setToken makes the simulator require a bearer token: POST /sim/token
// with {"token": "..."}; an empty token requires none.
func (s *Sim) setToken(r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return err
	}
	s.mu.Lock()
	s.token = in.Token
	s.mu.Unlock()
	return nil
}

// setDeny makes access reviews refuse some things: POST /sim/deny with
// {"verbs": ["impersonate"], "namespaces": ["kube-system"], "users":
// ["x"], "only": {"dev@corp.example": ["web", "analytics"]}}; empty
// lists allow everything again. A denied namespace refuses every
// namespaced verb for people (impersonated or named), a denied user
// everything, and "only" limits a user to the namespaces listed, with
// nothing cluster-wide; none of it applies to the console's own account,
// which watches everything.
func (s *Sim) setDeny(r *http.Request) error {
	var in struct {
		Verbs      []string            `json:"verbs"`
		Namespaces []string            `json:"namespaces"`
		Users      []string            `json:"users"`
		Only       map[string][]string `json:"only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return err
	}
	s.mu.Lock()
	s.denyVerbs, s.denyNamespaces, s.denyUsers, s.onlyNamespaces = in.Verbs, in.Namespaces, in.Users, in.Only
	s.mu.Unlock()
	return nil
}

func (s *Sim) createSecret(w http.ResponseWriter, r *http.Request) {
	var sec corev1.Secret
	if err := json.NewDecoder(r.Body).Decode(&sec); err != nil {
		status(w, http.StatusBadRequest, "BadRequest", "invalid Secret: "+err.Error())
		return
	}
	sec.TypeMeta = metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"}
	sec.Namespace = r.PathValue("ns")
	key := sec.Namespace + "/" + sec.Name
	s.mu.Lock()
	if s.objectLocked("secrets", key) != nil {
		s.mu.Unlock()
		status(w, http.StatusConflict, "AlreadyExists", fmt.Sprintf("secrets %q already exists", sec.Name))
		return
	}
	sec.UID = uid(key)
	sec.CreationTimestamp = metav1.Time{Time: s.opts.Now().UTC()}
	if sec.StringData != nil {
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		for k, v := range sec.StringData {
			sec.Data[k] = []byte(v)
		}
		sec.StringData = nil
	}
	s.storeLocked(&sec, watch.Added)
	out, _ := json.Marshal(&sec)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(out)
}

func (s *Sim) updateSecret(w http.ResponseWriter, r *http.Request) {
	var sec corev1.Secret
	if err := json.NewDecoder(r.Body).Decode(&sec); err != nil {
		status(w, http.StatusBadRequest, "BadRequest", "invalid Secret: "+err.Error())
		return
	}
	key := r.PathValue("ns") + "/" + r.PathValue("name")
	s.mu.Lock()
	old, ok := s.objectLocked("secrets", key).(*corev1.Secret)
	if !ok {
		s.mu.Unlock()
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("secrets %q not found", r.PathValue("name")))
		return
	}
	sec.TypeMeta = metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"}
	sec.Namespace, sec.Name, sec.UID, sec.CreationTimestamp = old.Namespace, old.Name, old.UID, old.CreationTimestamp
	s.storeLocked(&sec, watch.Modified)
	out, _ := json.Marshal(&sec)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func (s *Sim) deleteSecret(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("ns") + "/" + r.PathValue("name")
	s.mu.Lock()
	old, ok := s.objectLocked("secrets", key).(*corev1.Secret)
	if ok {
		s.storeLocked(old, watch.Deleted)
	}
	s.mu.Unlock()
	if !ok {
		status(w, http.StatusNotFound, "NotFound", fmt.Sprintf("secrets %q not found", r.PathValue("name")))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
}

// person is who a request acts as: the impersonated user when the
// headers say so, otherwise the console's own account.
func person(r *http.Request) (user string, groups []string, impersonated bool) {
	if u := r.Header.Get("Impersonate-User"); u != "" {
		return u, r.Header.Values("Impersonate-Group"), true
	}
	return "system:serviceaccount:221b:k8s-console-221b", []string{"system:serviceaccounts", "system:authenticated"}, false
}

// decide answers an access review: the console's own account may do
// anything but what /sim/deny lists by verb; a person may do anything
// but the denied verbs, in the denied namespaces nothing, and a denied
// user nothing at all.
func (s *Sim) decide(user string, impersonated bool, attrs *authzv1.ResourceAttributes) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if attrs == nil {
		return true, ""
	}
	for _, v := range s.denyVerbs {
		if v == attrs.Verb {
			return false, "the simulator denies " + v
		}
	}
	if impersonated {
		for _, u := range s.denyUsers {
			if u == user {
				return false, "the simulator denies " + user
			}
		}
		for _, ns := range s.denyNamespaces {
			if ns != "" && ns == attrs.Namespace {
				return false, "the simulator denies " + user + " in " + ns
			}
		}
		if only, ok := s.onlyNamespaces[user]; ok {
			if attrs.Namespace == "" {
				return false, "the simulator limits " + user + " to " + strings.Join(only, ", ")
			}
			for _, ns := range only {
				if ns == attrs.Namespace {
					return true, ""
				}
			}
			return false, "the simulator limits " + user + " to " + strings.Join(only, ", ")
		}
	}
	return true, ""
}

func (s *Sim) selfAccessReview(w http.ResponseWriter, r *http.Request) {
	var rev authzv1.SelfSubjectAccessReview
	if err := json.NewDecoder(r.Body).Decode(&rev); err != nil {
		status(w, http.StatusBadRequest, "BadRequest", "invalid review: "+err.Error())
		return
	}
	user, _, imp := person(r)
	rev.TypeMeta = metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}
	rev.Status.Allowed, rev.Status.Reason = s.decide(user, imp, rev.Spec.ResourceAttributes)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(&rev)
}

func (s *Sim) accessReview(w http.ResponseWriter, r *http.Request) {
	var rev authzv1.SubjectAccessReview
	if err := json.NewDecoder(r.Body).Decode(&rev); err != nil {
		status(w, http.StatusBadRequest, "BadRequest", "invalid review: "+err.Error())
		return
	}
	rev.TypeMeta = metav1.TypeMeta{Kind: "SubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}
	rev.Status.Allowed, rev.Status.Reason = s.decide(rev.Spec.User, rev.Spec.User != "", rev.Spec.ResourceAttributes)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(&rev)
}

func (s *Sim) selfReview(w http.ResponseWriter, r *http.Request) {
	user, groups, _ := person(r)
	rev := authnv1.SelfSubjectReview{TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectReview", APIVersion: "authentication.k8s.io/v1"}}
	rev.Status.UserInfo = authnv1.UserInfo{Username: user, Groups: groups}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(&rev)
}
