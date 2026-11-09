package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/nodessh"
)

// HostsService keeps the hosts outside Kubernetes and pairs them (Phase
// 12); *nodessh.Service implements it.
type HostsService interface {
	Hosts(ctx context.Context, cluster string) ([]nodessh.Host, error)
	AddHost(ctx context.Context, h nodessh.Host, by string) (nodessh.Host, error)
	ScanHost(ctx context.Context, cluster, name string) (nodessh.KeyOffer, error)
	TrustHost(ctx context.Context, cluster, name, fingerprint, by string) (nodessh.Host, bool, error)
	PairHost(ctx context.Context, cluster, name string, cred *nodessh.Credential, by string) (nodessh.Host, []string, error)
	CheckHost(ctx context.Context, cluster, name, by string) (nodessh.Host, error)
	SetupCommand(ctx context.Context, cluster, name string) (string, error)
	CleanupCommand(ctx context.Context, cluster, name string) (string, error)
	RemoveHost(ctx context.Context, cluster, name string) (nodessh.Host, string, error)
	RestoreHost(ctx context.Context, h nodessh.Host) (nodessh.Host, error)
}

// How long each step may take: reading a key, the pairing session with
// its setup, a login.
const (
	hostScanTime  = 30 * time.Second
	hostPairTime  = 3 * time.Minute
	hostCheckTime = time.Minute
	// maxCredentialBody bounds a pairing request: a private key and some
	// passwords.
	maxCredentialBody = 32 << 10
)

func (s *Server) hostsRoutes() {
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"hosts", "hosts", s.getHosts)
		s.handle("POST "+base+"hosts", "host-add", s.postHost)
		s.handle("DELETE "+base+"hosts/{name}", "host-remove", s.deleteHost)
		s.handle("POST "+base+"hosts/{name}/scan", "host-scan", s.postHostScan)
		s.handle("POST "+base+"hosts/{name}/trust", "host-trust", s.postHostTrust)
		s.handle("POST "+base+"hosts/{name}/pair", "host-pair", s.postHostPair)
		s.handle("GET "+base+"hosts/{name}/setup", "host-setup", s.getHostSetup)
		s.handle("GET "+base+"hosts/{name}/cleanup", "host-cleanup", s.getHostCleanup)
		s.handle("POST "+base+"hosts/{name}/check", "host-check", s.postHostCheck)
	}
}

// hostView is a host as the page sees it: never its host key, which stays
// on the server, only the fingerprint.
type hostView struct {
	Name        string            `json:"name"`
	Address     string            `json:"address"`
	Port        int               `json:"port"`
	LoginUser   string            `json:"loginUser"`
	Labels      map[string]string `json:"labels,omitempty"`
	State       string            `json:"state"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	KeyChanged  bool              `json:"keyChanged,omitempty"`
	AddedBy     string            `json:"addedBy,omitempty"`
	AddedAt     time.Time         `json:"addedAt"`
	TrustedBy   string            `json:"trustedBy,omitempty"`
	TrustedAt   *time.Time        `json:"trustedAt,omitempty"`
	PairedBy    string            `json:"pairedBy,omitempty"`
	PairedAt    *time.Time        `json:"pairedAt,omitempty"`
}

func viewOfHost(h nodessh.Host) hostView {
	return hostView{Name: h.Name, Address: h.Address, Port: h.Port, LoginUser: h.LoginUser, Labels: h.Labels, State: h.State, Fingerprint: h.Fingerprint, KeyChanged: h.Mismatch != "",
		AddedBy: h.AddedBy, AddedAt: h.AddedAt, TrustedBy: h.TrustedBy, TrustedAt: h.TrustedAt, PairedBy: h.PairedBy, PairedAt: h.PairedAt}
}

// hostsReady lets a SuperAdmin through to the hosts of a console that
// reaches machines by SSH, and names the cluster.
func (s *Server) hostsReady(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.automationReady(w, r) {
		return "", false
	}
	if s.opts.Hosts == nil {
		jsonError(w, http.StatusNotFound, "Hosts are not part of this console.")
		return "", false
	}
	if off := s.automationOff(); off != "" {
		jsonError(w, http.StatusConflict, off)
		return "", false
	}
	c, ok := s.watsonCluster(w, r)
	if !ok {
		return "", false
	}
	return c.ID, true
}

func hostStatus(err error) int {
	switch {
	case errors.Is(err, nodessh.ErrNoHost):
		return http.StatusNotFound
	case errors.Is(err, nodessh.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, nodessh.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// hostError answers a failure in the words it has: the service's errors
// say what to do and hold no credential.
func hostError(w http.ResponseWriter, err error) {
	msg := err.Error()
	for _, p := range []error{nodessh.ErrInvalid, nodessh.ErrConflict, nodessh.ErrNoHost} {
		msg = strings.TrimPrefix(msg, p.Error()+": ")
	}
	if errors.Is(err, nodessh.ErrNoHost) {
		msg = "no host " + msg + " here"
	}
	jsonError(w, hostStatus(err), upperFirst(msg)+".")
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) recordHost(r *http.Request, action, cluster, name, outcome, detail string) {
	s.recordAutomation(r, action, cluster, "host "+name, outcome, detail)
}

func (s *Server) getHosts(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	list, err := s.opts.Hosts.Hosts(r.Context(), cluster)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "the hosts could not be read: "+err.Error())
		return
	}
	out := []hostView{}
	for _, h := range list {
		out = append(out, viewOfHost(h))
	}
	cfg := s.opts.Sessions.Config().SSH
	answerJSON(w, r, map[string]any{"hosts": out, "loginUser": cfg.LoginUser, "port": cfg.Port})
}

func (s *Server) postHost(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	var in struct {
		Name      string            `json:"name"`
		Address   string            `json:"address"`
		Port      int               `json:"port"`
		LoginUser string            `json:"loginUser"`
		Labels    map[string]string `json:"labels"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	if in.LoginUser == "" {
		in.LoginUser = s.opts.Sessions.Config().SSH.LoginUser
	}
	who := s.watsonWho(r)
	h, err := s.opts.Hosts.AddHost(r.Context(), nodessh.Host{Cluster: cluster, Name: in.Name, Address: in.Address, Port: in.Port, LoginUser: in.LoginUser, Labels: in.Labels}, who.Name)
	if err != nil {
		hostError(w, err)
		return
	}
	s.recordHost(r, audit.ActionHostAdd, cluster, h.Name, audit.OutcomeOK, fmt.Sprintf("host %s added at %s port %d, login user %s; pending until paired", h.Name, h.Address, h.Port, h.LoginUser))
	b, _ := json.Marshal(viewOfHost(h))
	writeJSONStatus(w, r, http.StatusCreated, b)
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	h, cleanup, err := s.opts.Hosts.RemoveHost(r.Context(), cluster, r.PathValue("name"))
	if err != nil {
		hostError(w, err)
		return
	}
	s.recordHost(r, audit.ActionHostRemove, cluster, h.Name, audit.OutcomeOK, fmt.Sprintf("host %s removed, with its pinned key and pairing (was %s)", h.Name, h.State))
	answerJSON(w, r, map[string]string{"cleanup": cleanup})
}

// postHostScan reads the host's key with no credential and answers its
// fingerprint, which the SuperAdmin compares on the host.
func (s *Server) postHostScan(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostScanTime)
	defer cancel()
	offer, err := s.opts.Hosts.ScanHost(ctx, cluster, r.PathValue("name"))
	if err != nil {
		hostError(w, err)
		return
	}
	answerJSON(w, r, offer)
}

func (s *Server) postHostTrust(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	who := s.watsonWho(r)
	name := r.PathValue("name")
	h, again, err := s.opts.Hosts.TrustHost(r.Context(), cluster, name, in.Fingerprint, who.Name)
	if err != nil {
		hostError(w, err)
		return
	}
	what := "trusted"
	if again {
		what = "trusted again"
	}
	s.recordHost(r, audit.ActionHostTrust, cluster, h.Name, audit.OutcomeOK, fmt.Sprintf("host %s %s: host key %s pinned", h.Name, what, h.Fingerprint))
	answerJSON(w, r, viewOfHost(h))
}

// postHostPair pairs a host with a credential given once. The body is read
// here, so no decoder's error can quote it, and cleared once read; the
// credential goes to the pairing alone, which wipes it, and only its user
// name reaches the record.
func (s *Server) postHostPair(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send it as application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCredentialBody))
	var cred nodessh.Credential
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		err = dec.Decode(&cred)
	}
	clear(body)
	if err != nil {
		cred.Wipe()
		jsonError(w, http.StatusBadRequest, "The request is not valid: send {user, password or privateKey, passphrase, sudoPassword} as JSON, at most 32 KiB.")
		return
	}
	who := s.watsonWho(r)
	name := r.PathValue("name")
	user := cred.User
	ctx, cancel := context.WithTimeout(r.Context(), hostPairTime)
	defer cancel()
	h, steps, err := s.opts.Hosts.PairHost(ctx, cluster, name, &cred, who.Name)
	cred.Wipe()
	if err != nil {
		if !errors.Is(err, nodessh.ErrInvalid) && !errors.Is(err, nodessh.ErrNoHost) {
			s.recordHost(r, audit.ActionHostPair, cluster, name, audit.OutcomeFailed, fmt.Sprintf("pairing host %s with the account %s failed: %v", name, user, err))
		}
		code := hostStatus(err)
		msg := err.Error()
		for _, p := range []error{nodessh.ErrInvalid, nodessh.ErrNoHost} {
			msg = strings.TrimPrefix(msg, p.Error()+": ")
		}
		b, _ := json.Marshal(map[string]any{"error": upperFirst(msg) + ".", "steps": nonNilSteps(steps)})
		writeJSONStatus(w, r, code, b)
		return
	}
	s.recordHost(r, audit.ActionHostPair, cluster, name, audit.OutcomeOK, fmt.Sprintf("host %s paired with the account %s; the console logs in as %s with its certificate", name, user, h.LoginUser))
	s.recordRenamed(r, cluster, name, h)
	answerJSON(w, r, map[string]any{"host": viewOfHost(h), "steps": nonNilSteps(steps)})
}

// recordRenamed records that a host added under its address took its
// hostname as it was paired.
func (s *Server) recordRenamed(r *http.Request, cluster, was string, h nodessh.Host) {
	if h.Name != was {
		s.recordHost(r, audit.ActionHostRename, cluster, h.Name, audit.OutcomeOK, fmt.Sprintf("host %s, added under its address, takes its hostname %s; its pairing and the lists of nodes that named it follow", was, h.Name))
	}
}

func nonNilSteps(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) getHostSetup(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	cmd, err := s.opts.Hosts.SetupCommand(r.Context(), cluster, r.PathValue("name"))
	if err != nil {
		hostError(w, err)
		return
	}
	answerJSON(w, r, map[string]string{"command": cmd})
}

// getHostCleanup is the command that takes the console's lines off a
// host, shown before it is removed.
func (s *Server) getHostCleanup(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	cmd, err := s.opts.Hosts.CleanupCommand(r.Context(), cluster, r.PathValue("name"))
	if err != nil {
		hostError(w, err)
		return
	}
	answerJSON(w, r, map[string]string{"command": cmd})
}

// postHostCheck checks the login of a host whose setup someone ran.
func (s *Server) postHostCheck(w http.ResponseWriter, r *http.Request) {
	cluster, ok := s.hostsReady(w, r)
	if !ok {
		return
	}
	who := s.watsonWho(r)
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), hostCheckTime)
	defer cancel()
	h, err := s.opts.Hosts.CheckHost(ctx, cluster, name, who.Name)
	if err != nil {
		if !errors.Is(err, nodessh.ErrInvalid) && !errors.Is(err, nodessh.ErrNoHost) {
			s.recordHost(r, audit.ActionHostPair, cluster, name, audit.OutcomeFailed, fmt.Sprintf("checking host %s failed: %v", name, err))
		}
		hostError(w, err)
		return
	}
	s.recordHost(r, audit.ActionHostPair, cluster, name, audit.OutcomeOK, fmt.Sprintf("host %s paired by its setup command, run on it by hand; the console logs in as %s with its certificate", name, h.LoginUser))
	s.recordRenamed(r, cluster, name, h)
	answerJSON(w, r, viewOfHost(h))
}
