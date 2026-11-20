package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Administration › Credentials (0.12.1, CR-01 to CR-14) ---
//
// A SuperAdmin adds, changes, tests and removes the credentials, and sees
// what uses each; anyone signed in reads their names, kinds and endpoints,
// which the pickers of the Helm tab, Administration › Settings ›
// Repositories and Administration › AI › Knowledge offer. A secret is
// written and never read back: no answer holds one, nor does the audit
// log, nor an SSH key's host key, shown by its fingerprint.

// The audit log's actions of the credentials.
const (
	ActionCredentialSave   = "credential.save"
	ActionCredentialRemove = "credential.remove"
	ActionCredentialTest   = "credential.test"
	// A certificate trusted, or let go (CR-16, CR-19).
	ActionCredentialTrust  = "credential.trust"
	ActionCredentialForget = "credential.forget"
	// A secret no credential names, taken out of the Secret (DB-75).
	ActionCredentialSecretRemove = "credential.secret.remove"
)

func (s *Server) credentialsRoutes() {
	s.handle("GET /api/v1/credentials", "credentials", s.getCredentials)
	s.handle("GET /api/v1/credentials/choices", "credential-choices", s.getCredentialChoices)
	s.handle("POST /api/v1/credentials", "credential-create", s.postCredential)
	s.handle("POST /api/v1/credentials/migration/dismiss", "credential-migration-dismiss", s.postCredentialMigrationDismiss)
	s.handle("PUT /api/v1/credentials/{id}", "credential-save", s.putCredential)
	s.handle("DELETE /api/v1/credentials/{id}", "credential-remove", s.deleteCredential)
	s.handle("POST /api/v1/credentials/{id}/test", "credential-test", s.postCredentialTest)
	s.handle("POST /api/v1/credentials/{id}/trust", "credential-trust", s.postCredentialTrust)
	s.handle("DELETE /api/v1/credentials/{id}/trust", "credential-forget", s.deleteCredentialTrust)
	s.handle("GET /api/v1/credential-secrets", "credential-unnamed-secrets", s.getUnnamedSecrets)
	s.handle("DELETE /api/v1/credential-secrets/{id}", "credential-secret-remove", s.deleteUnnamedSecret)
}

// getUnnamedSecrets lists, for a SuperAdmin, the ids of the secrets the
// credentials' Secret keeps that no credential names (DB-75): never used,
// and named in the log at each start. Never a value.
func (s *Server) getUnnamedSecrets(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	ids, err := s.opts.Credentials.UnnamedSecrets(r.Context())
	if err != nil {
		credentialFailed(w, err)
		return
	}
	b, _ := json.Marshal(struct {
		Secrets []string `json:"secrets"`
	}{ids})
	writeJSON(w, r, b)
}

// deleteUnnamedSecret takes a secret no credential names out of the
// credentials' Secret (DB-75); one a credential names is refused (409).
func (s *Server) deleteUnnamedSecret(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.opts.Credentials.RemoveUnnamedSecret(r.Context(), id); err != nil {
		credentialFailed(w, err)
		return
	}
	s.record(r, ActionCredentialSecretRemove, id, "the secret of this id, which no credential names, removed from the credentials' Secret")
	w.WriteHeader(http.StatusNoContent)
}

// credentialView is a credential as the page sees it: everything but the
// secret and the host key for a SuperAdmin, with its uses; its name, kind
// and endpoint for others.
type credentialView struct {
	credentials.Credential
	// UsedBy is what uses it, for a SuperAdmin; empty for others (CR-06).
	UsedBy []credentials.Ref `json:"usedBy"`
	// HostKeyInfo is an SSH key's host key, by type, fingerprint and cut
	// short (CR-02).
	HostKeyInfo *credentials.HostKeyInfo `json:"hostKeyInfo,omitempty"`
	// TokenHost is the host an OAuth2 client's secret goes to besides the
	// endpoint (CR-04).
	TokenHost string `json:"tokenHost,omitempty"`
	// Expiring says it expires within 14 days, or has (CR-08).
	Expiring bool `json:"expiring,omitempty"`
	// How says how it signs in, in words.
	How string `json:"how"`
}

// credentialsView is the page's list.
type credentialsView struct {
	Credentials []credentialView `json:"credentials"`
	MayEdit     bool             `json:"mayEdit"`
	// Migration is what the upgrade made, until a SuperAdmin dismisses it
	// (CR-12).
	Migration *credentials.Migration `json:"migration,omitempty"`
}

// credentialViewOf is a credential as a SuperAdmin sees it, with its
// uses: never its secret, nor its host key.
func (s *Server) credentialViewOf(c credentials.Credential, used []credentials.Ref) credentialView {
	v := credentialView{Credential: c, UsedBy: used, How: credentials.Describe(c), Expiring: c.Expiring(s.opts.Now()), HostKeyInfo: c.HostKeyInfo(), TokenHost: tokenHost(c)}
	v.Credential.HostKey = ""
	if v.UsedBy == nil {
		v.UsedBy = []credentials.Ref{}
	}
	return v
}

// tokenHost is the host of an OAuth2 client's token URL.
func tokenHost(c credentials.Credential) string {
	if c.Kind != credentials.KindOAuth2 {
		return ""
	}
	if u, err := url.Parse(c.TokenURL); err == nil {
		return u.Host
	}
	return ""
}

func (s *Server) getCredentials(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Credentials.List(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	admin := s.isSuperAdmin(r)
	// What uses each is a SuperAdmin's to see: it names applications in
	// namespaces others may not open (CR-06).
	used := map[string][]credentials.Ref{}
	if admin {
		if used, err = s.opts.Credentials.UsedBy(r.Context()); err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	out := credentialsView{Credentials: []credentialView{}, MayEdit: admin}
	for _, c := range list {
		var v credentialView
		if admin {
			v = s.credentialViewOf(c, used[c.ID])
		} else {
			v = credentialView{Credential: credentials.Credential{ID: c.ID, Name: c.Name, Kind: c.Kind, Endpoint: c.Endpoint}, UsedBy: []credentials.Ref{}, TokenHost: tokenHost(c)}
		}
		out.Credentials = append(out.Credentials, v)
	}
	if admin {
		if m, err := s.opts.Credentials.Migration(r.Context()); err == nil && !m.Dismissed && (len(m.Created) > 0 || len(m.Notes) > 0) {
			out.Migration = &m
		}
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// credentialChoice is a credential a picker offers.
type credentialChoice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Endpoint string `json:"endpoint"`
}

// getCredentialChoices lists the credentials that serve a use at an
// address (CR-10): the picker preselects the only one, and makes the
// person choose among several.
func (s *Server) getCredentialChoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	use, address := q.Get("use"), strings.TrimSpace(q.Get("address"))
	known := false
	for _, u := range credentials.Uses {
		known = known || u == use
	}
	if !known {
		jsonError(w, http.StatusBadRequest, "name the use: git, helm, s3, confluence-cloud, confluence-dc or http")
		return
	}
	list, err := s.opts.Credentials.Matching(r.Context(), use, address)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := []credentialChoice{}
	for _, c := range list {
		out = append(out, credentialChoice{ID: c.ID, Name: c.Name, Kind: c.Kind, Endpoint: c.Endpoint})
	}
	b, _ := json.Marshal(map[string]any{"choices": out})
	writeJSON(w, r, b)
}

// credentialInput is a credential as the page sends it; Secret replaces
// the kept one when given.
type credentialInput struct {
	credentials.Credential
	Secret *credentials.Secret `json:"secret,omitempty"`
}

func (s *Server) mayEditCredentials(w http.ResponseWriter, r *http.Request) bool {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin adds, changes, tests or removes credentials.")
		return false
	}
	return true
}

func (s *Server) readCredential(w http.ResponseWriter, r *http.Request) (credentialInput, bool) {
	var in credentialInput
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the credential as application/json")
		return in, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the credential is not valid: "+err.Error())
		return in, false
	}
	// What the page may not set: the server keeps these.
	in.Set, in.LastTest, in.From, in.PinnedCert = nil, nil, nil, nil
	in.Created, in.Changed = time.Time{}, time.Time{}
	in.CreatedBy, in.ChangedBy = "", ""
	return in, true
}

// credentialFailed answers a refused change: not valid or in use is the
// person's to fix, not there is 404, the rest the store's.
func credentialFailed(w http.ResponseWriter, err error) {
	msg := strings.TrimPrefix(err.Error(), credentials.ErrInvalid.Error()+": ")
	switch {
	case errors.Is(err, credentials.ErrInvalid):
		jsonError(w, http.StatusBadRequest, msg)
	case errors.Is(err, credentials.ErrNotFound):
		jsonError(w, http.StatusNotFound, msg)
	case errors.Is(err, credentials.ErrNotSeen):
		jsonError(w, http.StatusConflict, msg)
	case errors.Is(err, credentials.ErrInUse):
		jsonError(w, http.StatusConflict, strings.TrimPrefix(msg, credentials.ErrInUse.Error()+": "))
	default:
		jsonError(w, http.StatusBadGateway, msg)
	}
}

// credentialAudit is a credential as the audit log names it: never a
// secret.
func credentialAudit(c credentials.Credential) string {
	return c.Kind + " for " + c.Endpoint + ", " + credentials.Describe(c)
}

func (s *Server) postCredential(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	in, ok := s.readCredential(w, r)
	if !ok {
		return
	}
	sec := credentials.Secret{}
	if in.Secret != nil {
		sec = *in.Secret
	}
	c, err := s.opts.Credentials.Create(r.Context(), in.Credential, sec, repoBy(r))
	if err != nil {
		credentialFailed(w, err)
		return
	}
	s.record(r, ActionCredentialSave, c.Name, "credential added: "+credentialAudit(c))
	b, _ := json.Marshal(s.credentialViewOf(c, nil))
	writeJSONStatus(w, r, http.StatusCreated, b)
}

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	in, ok := s.readCredential(w, r)
	if !ok {
		return
	}
	c, err := s.opts.Credentials.Update(r.Context(), r.PathValue("id"), in.Credential, in.Secret, repoBy(r))
	if err != nil {
		credentialFailed(w, err)
		return
	}
	detail := "credential changed: " + credentialAudit(c)
	if in.Secret != nil {
		// Replaced for every use from now on (CR-05); the value is never
		// written here.
		detail += "; its secret replaced"
	}
	s.record(r, ActionCredentialSave, c.Name, detail)
	used, _ := s.opts.Credentials.UsedBy(r.Context())
	b, _ := json.Marshal(s.credentialViewOf(c, used[c.ID]))
	writeJSON(w, r, b)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	c, err := s.opts.Credentials.Delete(r.Context(), r.PathValue("id"))
	if err != nil {
		credentialFailed(w, err)
		return
	}
	s.record(r, ActionCredentialRemove, c.Name, "credential removed: "+credentialAudit(c))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postCredentialTest(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	var in struct {
		Target string `json:"target,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	if err := dec.Decode(&in); err != nil && err.Error() != "EOF" {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return
	}
	id := r.PathValue("id")
	t, err := s.opts.Credentials.RunTest(r.Context(), id, in.Target, repoBy(r))
	if err != nil {
		credentialFailed(w, err)
		return
	}
	outcome := audit.OutcomeOK
	if !t.OK {
		outcome = audit.OutcomeFailed
	}
	name := id
	if c, err := s.opts.Credentials.Get(r.Context(), id); err == nil {
		name = c.Name
	}
	s.recordOutcome(r, ActionCredentialTest, name, "credential tested against "+t.Target+": "+t.Text, outcome)
	b, _ := json.Marshal(t)
	writeJSON(w, r, b)
}

// pinAudit is a pinned certificate as the audit log names it.
func pinAudit(host string, p *certpin.Pin) string {
	if p == nil {
		return host
	}
	out := host + ": SHA-256 " + p.SHA256
	if p.Subject != "" {
		out += ", subject " + p.Subject
	}
	if p.Issuer != "" {
		out += ", issuer " + p.Issuer
	}
	if !p.NotAfter.IsZero() {
		out += ", valid until " + p.NotAfter.Format(time.DateOnly)
	}
	return out
}

// trustInput is Trust this certificate: the fingerprint a Test showed and,
// for a credential, the address it asked.
type trustInput struct {
	SHA256 string `json:"sha256"`
	Target string `json:"target,omitempty"`
}

func decodeTrust(w http.ResponseWriter, r *http.Request) (trustInput, bool) {
	var in trustInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return in, false
	}
	return in, true
}

// postCredentialTrust pins the certificate the credential's address
// presents, when it is the one its Test showed (CR-16).
func (s *Server) postCredentialTrust(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	in, ok := decodeTrust(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	c, cert, err := s.opts.Credentials.Trust(r.Context(), id, in.Target, in.SHA256, repoBy(r))
	if err != nil {
		name := id
		if c, err := s.opts.Credentials.Get(r.Context(), id); err == nil {
			name = c.Name
		}
		s.recordOutcome(r, ActionCredentialTrust, name, "certificate not trusted: "+strings.TrimPrefix(err.Error(), credentials.ErrInvalid.Error()+": "), audit.OutcomeFailed)
		credentialFailed(w, err)
		return
	}
	s.record(r, ActionCredentialTrust, c.Name, "certificate trusted for "+pinAudit(credentials.TestTarget(c, in.Target), c.PinnedCert))
	b, _ := json.Marshal(map[string]any{"pinnedCert": c.PinnedCert, "certificate": cert})
	writeJSON(w, r, b)
}

// deleteCredentialTrust lets the credential's pinned certificate go
// (CR-19).
func (s *Server) deleteCredentialTrust(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	old, err := s.opts.Credentials.Forget(r.Context(), r.PathValue("id"))
	if err != nil {
		credentialFailed(w, err)
		return
	}
	s.record(r, ActionCredentialForget, old.Name, "pinned certificate forgotten for "+pinAudit(old.Endpoint, old.PinnedCert))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postCredentialMigrationDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.mayEditCredentials(w, r) {
		return
	}
	if err := s.opts.Credentials.DismissMigration(r.Context()); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
