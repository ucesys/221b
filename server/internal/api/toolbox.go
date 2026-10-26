package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sessions"
	tb "github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/tunnel"
	"github.com/ucesys/221b/server/internal/watson"
)

// The toolbox's API (Phase 9): what a person may use where (TB-05), a
// session's card (TB-36), the sessions themselves, and the WebSockets of a
// terminal (TB-15), a watcher (TB-55) and the CLI's tunnel (TB-19).

func (s *Server) toolboxRoutes() {
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"toolbox", "toolbox", s.getToolbox)
		s.handle("POST "+base+"watson/sessions", "toolbox-request", s.postSessionCard)
		s.handle("POST "+base+"watson/actions", "toolbox-action", s.postToolAction)
		s.handle("GET "+base+"sessions", "sessions", s.getSessions)
		s.handle("GET "+base+"sessions/{id}", "session", s.getSession)
		s.handle("POST "+base+"sessions/{id}/end", "session-end", s.postSessionEnd)
		s.handle("POST "+base+"sessions/{id}/keep", "session-keep", s.postSessionKeep)
		s.handle("POST "+base+"sessions/{id}/extend", "session-extend", s.postSessionExtend)
		s.handle("GET "+base+"sessions/{id}/attach", "session-attach", s.sessionAttach)
		s.handle("GET "+base+"sessions/{id}/watch", "session-watch", s.sessionAttach)
		s.handle("GET "+base+"sessions/{id}/tunnel", "session-tunnel", s.sessionTunnel)
		s.handle("GET "+base+"sessions/{id}/output", "session-output", s.getSessionOutput)
		s.handle("GET "+base+"sessions/{id}/transcript", "session-transcript", s.getSessionTranscript)
	}
}

// IsTunnelPath says whether a path is a port forward's tunnel: the CLI
// signs in with the forward's own token, not a cookie (TB-20).
func IsTunnelPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/api/v1/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 && parts[0] == "clusters" {
		parts = parts[2:]
	}
	return len(parts) == 3 && parts[0] == "sessions" && parts[2] == "tunnel"
}

// sessionWho is the person acting, as sessions know them.
func (s *Server) sessionWho(r *http.Request) sessions.Who {
	w := s.watsonWho(r)
	sess, _ := auth.SessionFrom(r.Context())
	who := sessions.Who{SignIn: w.Session, Person: w.Person, User: w.User, Name: w.Name, Admin: sess.Admin || s.opts.Auth == nil, Audit: w.Audit, From: w.From}
	if cur, ok := s.opts.Auth.(interface {
		Current(auth.Session) (auth.Session, bool)
	}); ok && sess.ID != "" {
		person := s.opts.Impersonation.personOf(sess.User).Key()
		who.Stands = func() bool {
			now, ok := cur.Current(sess)
			return ok && s.opts.Impersonation.personOf(now.User).Key() == person
		}
	}
	return who
}

func (s *Server) toolboxReady(w http.ResponseWriter) bool {
	if s.opts.Sessions == nil || s.opts.Watson == nil {
		jsonError(w, http.StatusNotFound, "the toolbox is not part of this console")
		return false
	}
	return true
}

// ---------- what a person may use (TB-05) ----------

type toolboxImage struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Caps  []string `json:"caps,omitempty"`
	Tools string   `json:"tools,omitempty"`
}

type toolboxView struct {
	Enabled    bool   `json:"enabled"`
	ReadOnly   bool   `json:"readOnly"`
	Production bool   `json:"production"`
	TwoPerson  bool   `json:"twoPerson"`
	SignedIn   bool   `json:"signedIn"`
	Admin      bool   `json:"admin"`
	Me         string `json:"me"`
	// Off says why a tool is off everywhere on this cluster, by tool.
	Off            map[string]string         `json:"off"`
	IdleSeconds    int                       `json:"idleSeconds"`
	MaxMinutes     int                       `json:"maxMinutes"`
	Transcripts    string                    `json:"transcripts"`
	NodeApproval   string                    `json:"nodeApproval"`
	NodeNamespace  string                    `json:"nodeNamespace"`
	MountVolumes   bool                      `json:"mountVolumes"`
	FileTransfer   bool                      `json:"fileTransfer"`
	CLIMinVersion  string                    `json:"cliMinVersion,omitempty"`
	Images         []toolboxImage            `json:"images"`
	ContainerImgs  []string                  `json:"debugContainerImages"`
	PodProfiles    []string                  `json:"debugPodProfiles"`
	NodeProfiles   []string                  `json:"nodeProfiles"`
	PodCommands    []string                  `json:"podCommands,omitempty"`
	NodeCommands   []string                  `json:"nodeCommands,omitempty"`
	Namespaces     map[string]namespaceTools `json:"namespaces"`
	Nodes          map[string]string         `json:"nodes"`
	ApproverNames  []string                  `json:"approvers,omitempty"`
	Values         tb.Config                 `json:"values"`
	NodeShellPodNS string                    `json:"-"`
}

// namespaceTools is what a person may use in a namespace: "" for a tool
// they may use, else why not; and the namespace's Pod Security level.
type namespaceTools struct {
	Tools map[string]string `json:"tools"`
	Level string            `json:"level"`
}

// toolCache keeps access answers for a minute, per person and namespace.
type toolCache struct {
	mu sync.Mutex
	m  map[string]toolCacheEntry
}

type toolCacheEntry struct {
	at time.Time
	nt namespaceTools
}

func (c *toolCache) get(key string, now time.Time) (namespaceTools, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) > time.Minute {
		return namespaceTools{}, false
	}
	return e.nt, true
}

func (c *toolCache) put(key string, nt namespaceTools, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 4096 {
		c.m = map[string]toolCacheEntry{}
	}
	c.m[key] = toolCacheEntry{at: now, nt: nt}
}

const (
	whyRole     = "your role does not allow it here"
	whyReadOnly = "off: this connection is read-only"
	whyChart    = "off on this console"
	whySuper    = "only a SuperAdmin may"
)

func (s *Server) getToolbox(w http.ResponseWriter, r *http.Request) {
	if !s.toolboxReady(w) {
		return
	}
	c, ok := s.watsonCluster(w, r)
	if !ok {
		return
	}
	who := s.sessionWho(r)
	cfg := s.opts.Sessions.Config()
	v := toolboxView{Enabled: cfg.Enabled, ReadOnly: c.ReadOnly || c.Locked != "", Production: c.Env == clusters.EnvProduction, TwoPerson: s.watsonTwoPerson(c), SignedIn: who.User != "", Admin: who.Admin, Me: who.Name,
		Off: map[string]string{}, IdleSeconds: int(cfg.IdleTimeout.D() / time.Second), MaxMinutes: int(cfg.MaxDuration.D() / time.Minute), Transcripts: cfg.Transcripts, NodeApproval: cfg.NodeShell.Approval, NodeNamespace: cfg.NodeShell.Namespace,
		MountVolumes: cfg.DebugContainer.MountVolumes, FileTransfer: cfg.Shell.FileTransfer, CLIMinVersion: cfg.PortForward.CLIMinVersion,
		ContainerImgs: cfg.ImageNames(tb.ToolDebugContainer), PodProfiles: cfg.ImageNames(tb.ToolDebugPod), NodeProfiles: cfg.NodeShell.Profiles,
		PodCommands: cfg.Shell.QuickCommands, NodeCommands: cfg.NodeShell.QuickCommands, Namespaces: map[string]namespaceTools{}, Nodes: map[string]string{}, Values: cfg}
	if v.Transcripts == "" {
		v.Transcripts = tb.TranscriptsNone
	}
	names := make([]string, 0, len(cfg.Images))
	for n := range cfg.Images {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		im := cfg.Images[n]
		v.Images = append(v.Images, toolboxImage{Name: n, Image: im.Image, Caps: im.Caps, Tools: im.Tools})
	}
	for _, t := range tb.Tools {
		switch {
		case !cfg.On(t):
			v.Off[t] = whyChart
		case c.Locked != "":
			v.Off[t] = "off: " + c.Locked
		case c.ReadOnly:
			v.Off[t] = whyReadOnly
		}
	}
	console, err := sessions.NewKubeBackend(c.Config, kube.Person{})
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	asPerson, err := sessions.NewKubeBackend(c.Config, who.Person)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	now := s.opts.Now()
	key := c.ID + "\x00" + who.Person.Key()
	var nsList []string
	for _, ns := range strings.Split(r.URL.Query().Get("namespaces"), ",") {
		if ns = strings.TrimSpace(ns); ns != "" && len(nsList) < 100 {
			nsList = append(nsList, ns)
		}
	}
	type nsTool struct{ tool, verb, group, resource, sub string }
	checks := []nsTool{
		{tb.ToolShell, "create", "", "pods", "exec"},
		{tb.ToolCommand, "create", "", "pods", "exec"},
		{tb.ToolForward, "create", "", "pods", "portforward"},
		{tb.ToolDebugContainer, "patch", "", "pods", "ephemeralcontainers"},
		{tb.ToolDebugPod, "create", "batch", "jobs", ""},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, ns := range nsList {
		if nt, ok := s.toolCache.get(key+"\x00"+ns, now); ok {
			v.Namespaces[ns] = nt
			continue
		}
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			nt := namespaceTools{Tools: map[string]string{}, Level: sessions.LevelPrivileged}
			if n, err := console.Namespace(ctx, ns); err == nil {
				nt.Level = sessions.PodSecurity(n)
			}
			answers := map[string]string{}
			for _, ck := range checks {
				k := ck.verb + ck.resource + ck.sub
				if why, done := answers[k]; done {
					nt.Tools[ck.tool] = why
					continue
				}
				ok, _, err := asPerson.Can(ctx, ck.verb, ck.group, ck.resource, ck.sub, ns, "")
				why := ""
				if err != nil || !ok {
					why = whyRole
				}
				answers[k] = why
				nt.Tools[ck.tool] = why
			}
			mu.Lock()
			v.Namespaces[ns] = nt
			mu.Unlock()
			s.toolCache.put(key+"\x00"+ns, nt, now)
		}(ns)
	}
	if r.URL.Query().Get("nodes") != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			why := ""
			if !who.Admin {
				why = whySuper
			} else if ok, _, err := asPerson.Can(ctx, "create", "", "pods", "", cfg.NodeShell.Namespace, ""); err != nil || !ok {
				why = whyRole
			}
			mu.Lock()
			v.Nodes[tb.ToolNodeShell], v.Nodes[tb.ToolNodeCommand] = why, why
			mu.Unlock()
		}()
	}
	wg.Wait()
	b, _ := json.Marshal(v)
	writeJSON(w, r, b)
}

// ---------- a session's card (TB-36) ----------

func (s *Server) postSessionCard(w http.ResponseWriter, r *http.Request) {
	if !s.toolboxReady(w) {
		return
	}
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the request as application/json")
		return
	}
	var in struct {
		sessions.Request
		From string `json:"from"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return
	}
	cfg := s.opts.Sessions.Config()
	req := in.Request
	needsTwo := sessions.NeedsTwo(cfg, req.Tool)
	sw := s.sessionWho(r)
	if (req.Tool == tb.ToolNodeShell || req.Tool == tb.ToolNodeCommand) && !sw.Admin {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin may open a node shell or run a command on a node.")
		return
	}
	// A reason is asked for whoever approves, and for a node on production.
	onNode := req.Tool == tb.ToolNodeShell || req.Tool == tb.ToolNodeCommand
	if err := req.Validate(cfg, s.watsonTwoPerson(c) || needsTwo || (onNode && c.Env == clusters.EnvProduction)); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	from := strings.TrimSpace(in.From)
	if from == "" {
		from = "Tools menu"
	}
	if len(from) > 200 {
		from = from[:200]
	}
	s.rememberOwner(who.Session, sw)
	// A dialog the person filled in and sent is their approval: nothing in
	// it was written by a model unseen. Only a second person's approval,
	// where the two-person rule or the tool asks for one, goes to Watson.
	if !s.watsonTwoPerson(c) && !needsTwo {
		if ref := s.opts.Watson.Refusal(c, who); ref != nil {
			jsonError(w, http.StatusForbidden, ref.Message)
			return
		}
		sw.From = from
		sess, err := s.opts.Sessions.Start(sessions.Cluster{ID: c.ID, Name: c.Name, Config: c.Config}, req, sw, "", "")
		if err != nil {
			jsonError(w, http.StatusBadGateway, err.Error())
			return
		}
		ss, _ := s.opts.Sessions.Get(sess.ID)
		b, _ := json.Marshal(struct {
			Opened sessionView `json:"opened"`
		}{s.sessionViewOf(r, ss, sw)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write(b)
		return
	}
	it := s.opts.Watson.SubmitSession(c, who, watson.SessionCard{Tool: req.Tool, Summary: req.Summary(), Request: req, NeedsTwo: needsTwo, Minutes: req.Minutes, Reason: req.Reason}, from)
	snap, lines, _ := s.opts.Watson.Snapshot(it.ID, 0)
	b, _ := json.Marshal(s.watsonView(snap, who, c, lines, 0))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(b)
}

// toolActions are the Tools menu's fixed changes (TB-03): the line comes
// from the action and the object's name, never from text the person or a
// model typed.
var toolActions = map[string]func(node, namespace, pod string) string{
	"cordon":   func(node, _, _ string) string { return "kubectl cordon " + node },
	"uncordon": func(node, _, _ string) string { return "kubectl uncordon " + node },
	"drain": func(node, _, _ string) string {
		return "kubectl drain " + node + " --ignore-daemonsets --delete-emptydir-data"
	},
	"delete-pod": func(_, namespace, pod string) string {
		return "kubectl delete pod " + pod + " -n " + namespace
	},
}

// postToolAction runs one of the Tools menu's fixed changes at once, as the
// person who chose it; Watson shows it running and keeps its record.
func (s *Server) postToolAction(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the action as application/json")
		return
	}
	var in struct {
		Action    string `json:"action"`
		Node      string `json:"node"`
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the action is not valid: "+err.Error())
		return
	}
	line, known := toolActions[in.Action]
	if !known {
		jsonError(w, http.StatusBadRequest, "no such action: "+in.Action)
		return
	}
	if in.Action == "delete-pod" {
		if len(validation.IsDNS1123Label(in.Namespace)) > 0 || len(validation.IsDNS1123Subdomain(in.Pod)) > 0 || in.Node != "" {
			jsonError(w, http.StatusBadRequest, "name a pod and its namespace")
			return
		}
	} else if len(validation.IsDNS1123Subdomain(in.Node)) > 0 || in.Namespace != "" || in.Pod != "" {
		jsonError(w, http.StatusBadRequest, "name a node")
		return
	}
	it := s.opts.Watson.SubmitAction(c, who, line(in.Node, in.Namespace, in.Pod), "Tools menu")
	snap, lines, _ := s.opts.Watson.Snapshot(it.ID, 0)
	b, _ := json.Marshal(s.watsonView(snap, who, c, lines, 0))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(b)
}

// rememberOwner keeps the sign-in check of a person who asked for a
// session, so the session can end with their sign-in (TB-13).
func (s *Server) rememberOwner(signIn string, who sessions.Who) {
	if signIn == "" || who.Stands == nil {
		return
	}
	s.owners.Store(signIn, who.Stands)
}

// planSession and startSession are Watson's hooks for session cards.
func (s *Server) planSession(ctx context.Context, cl watson.Cluster, it watson.Item) *watson.Plan {
	req, ok := it.Session.Request.(sessions.Request)
	if !ok {
		return &watson.Plan{Ready: true, DryRun: []watson.Line{{K: "err", T: "the request is not a toolbox request"}}, DryRunExit: 1}
	}
	p := s.opts.Sessions.PlanFor(ctx, sessions.Cluster{ID: cl.ID, Name: cl.Name, Config: cl.Config}, req, it.Requester.Person, it.Requester.Name)
	out := &watson.Plan{Ready: true, Targets: p.Targets, Impact: p.Impact, DryRunExit: p.DryRunExit, DryRunNote: p.DryRunNote, Object: p.Object}
	for _, l := range p.DryRun {
		k := "out"
		if p.DryRunExit != 0 {
			k = "err"
		}
		out.DryRun = append(out.DryRun, watson.Line{K: k, T: l})
	}
	return out
}

func (s *Server) startSession(cl watson.Cluster, it watson.Item, approver watson.Who) (string, error) {
	req, ok := it.Session.Request.(sessions.Request)
	if !ok {
		return "", errors.New("the request is not a toolbox request")
	}
	rq := it.Requester
	owner := sessions.Who{SignIn: rq.Session, Person: rq.Person, User: rq.User, Name: rq.Name, Admin: rq.Admin, Audit: rq.Audit, From: rq.From}
	if f, ok := s.owners.Load(rq.Session); ok {
		owner.Stands = f.(func() bool)
	}
	name := approver.Name
	if approver.Session == rq.Session {
		name = ""
	}
	sess, err := s.opts.Sessions.Start(sessions.Cluster{ID: cl.ID, Name: cl.Name, Config: cl.Config}, req, owner, name, it.ID)
	if err != nil {
		return "", err
	}
	return sess.ID, nil
}

// ---------- the sessions ----------

type sessionView struct {
	ID          string                 `json:"id"`
	Cluster     string                 `json:"cluster"`
	Tool        string                 `json:"tool"`
	Summary     string                 `json:"summary"`
	Request     sessions.Request       `json:"request"`
	Owner       string                 `json:"owner"`
	Mine        bool                   `json:"mine"`
	Approver    string                 `json:"approver,omitempty"`
	Card        string                 `json:"card,omitempty"`
	State       string                 `json:"state"`
	Created     time.Time              `json:"created"`
	Started     *time.Time             `json:"started,omitempty"`
	LastActive  time.Time              `json:"lastActive"`
	Ends        *time.Time             `json:"ends,omitempty"`
	Ended       *time.Time             `json:"ended,omitempty"`
	EndedBy     string                 `json:"endedBy,omitempty"`
	EndNote     string                 `json:"endNote,omitempty"`
	Exit        *int                   `json:"exit,omitempty"`
	Namespace   string                 `json:"namespace,omitempty"`
	Pod         string                 `json:"pod,omitempty"`
	Container   string                 `json:"container,omitempty"`
	Notes       []string               `json:"notes,omitempty"`
	Objects     []sessions.Created     `json:"objects,omitempty"`
	Forward     *sessions.ForwardStats `json:"forward,omitempty"`
	CLI         string                 `json:"cli,omitempty"`
	Watchers    []string               `json:"watchers,omitempty"`
	Recorded    bool                   `json:"recorded"`
	Interactive bool                   `json:"interactive"`
	IdleSeconds int                    `json:"idleSeconds"`
	Rev         int                    `json:"rev"`
}

func (s *Server) sessionViewOf(r *http.Request, ss sessions.Session, who sessions.Who) sessionView {
	cfg := s.opts.Sessions.Config()
	v := sessionView{ID: ss.ID, Cluster: ss.Cluster, Tool: ss.Req.Tool, Summary: ss.Req.Summary(), Request: ss.Req, Owner: ss.Owner.Name, Approver: ss.Approver, Card: ss.Card,
		State: ss.State, Created: ss.Created, LastActive: ss.LastActive, EndedBy: ss.EndedBy, EndNote: ss.EndNote, Exit: ss.Exit, Namespace: ss.Ns, Pod: ss.Pod, Container: ss.Container,
		Notes: ss.Notes, Objects: ss.Objects, Watchers: ss.Watchers, Recorded: ss.Recorded, Interactive: sessions.Interactive(ss.Req.Tool), IdleSeconds: int(cfg.IdleTimeout.D() / time.Second), Rev: ss.Rev}
	v.Mine = (ss.Owner.User != "" && ss.Owner.User == who.User) || (ss.Owner.User == "" && ss.Owner.SignIn == who.SignIn)
	if !v.Mine {
		v.Request.Reason = ss.Req.Reason
	}
	if !ss.Started.IsZero() {
		t := ss.Started
		v.Started = &t
	}
	if !ss.Ends.IsZero() {
		t := ss.Ends
		v.Ends = &t
	}
	if !ss.Ended.IsZero() {
		t := ss.Ended
		v.Ended = &t
	}
	if ss.Req.Tool == tb.ToolForward {
		f := ss.Forward
		v.Forward = &f
		if v.Mine && ss.State != sessions.StateEnded {
			if tok, err := s.opts.Sessions.Token(ss.ID, who); err == nil {
				v.CLI = cliCommand(r, ss, tok)
			}
		}
	}
	return v
}

// cliCommand is the one line the owner of a forward runs (TB-21).
func cliCommand(r *http.Request, ss sessions.Session, token string) string {
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	cmd := fmt.Sprintf("221b forward %s --cluster %s --session %s --token %s", base, ss.Cluster, ss.ID, token)
	if ss.Req.LocalPort > 0 {
		cmd += fmt.Sprintf(" --local %d", ss.Req.LocalPort)
	}
	return cmd
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	if !s.toolboxReady(w) {
		return
	}
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	who := s.sessionWho(r)
	if r.URL.Query().Get("history") != "" {
		recs := s.opts.Sessions.History(clusterID(r), who)
		if recs == nil {
			recs = []sessions.Record{}
		}
		b, _ := json.Marshal(recs)
		writeJSON(w, r, b)
		return
	}
	out := []sessionView{}
	for _, ss := range s.opts.Sessions.List(clusterID(r), who) {
		out = append(out, s.sessionViewOf(r, ss, who))
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// session finds a session of the request's cluster the person may see.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (sessions.Session, sessions.Who, bool) {
	if !s.toolboxReady(w) {
		return sessions.Session{}, sessions.Who{}, false
	}
	who := s.sessionWho(r)
	ss, ok := s.opts.Sessions.Get(r.PathValue("id"))
	if !ok || ss.Cluster != clusterID(r) || !sessions.Visible(ss, who) {
		jsonError(w, http.StatusNotFound, "There is no such session, or it is not yours to see.")
		return ss, who, false
	}
	return ss, who, true
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	ss, who, ok := s.session(w, r)
	if !ok {
		return
	}
	b, _ := json.Marshal(s.sessionViewOf(r, ss, who))
	writeJSON(w, r, b)
}

func (s *Server) sessionAnswer(w http.ResponseWriter, r *http.Request, ss *sessions.Session, who sessions.Who, err error) {
	switch {
	case errors.Is(err, sessions.ErrNotFound):
		jsonError(w, http.StatusNotFound, "There is no such session.")
		return
	case errors.Is(err, sessions.ErrNotYours):
		jsonError(w, http.StatusForbidden, "That session is someone else's.")
		return
	case errors.Is(err, sessions.ErrEnded):
		jsonError(w, http.StatusConflict, "The session has ended.")
		return
	case err != nil:
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	cur, _ := s.opts.Sessions.Get(ss.ID)
	b, _ := json.Marshal(s.sessionViewOf(r, cur, who))
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (s *Server) postSessionEnd(w http.ResponseWriter, r *http.Request) {
	ss, who, ok := s.session(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
	if len(in.Reason) > 500 {
		in.Reason = in.Reason[:500]
	}
	_, err := s.opts.Sessions.End(ss.ID, who, in.Reason)
	s.sessionAnswer(w, r, &ss, who, err)
}

func (s *Server) postSessionKeep(w http.ResponseWriter, r *http.Request) {
	ss, who, ok := s.session(w, r)
	if !ok {
		return
	}
	_, err := s.opts.Sessions.Keep(ss.ID, who)
	s.sessionAnswer(w, r, &ss, who, err)
}

func (s *Server) postSessionExtend(w http.ResponseWriter, r *http.Request) {
	ss, who, ok := s.session(w, r)
	if !ok {
		return
	}
	_, err := s.opts.Sessions.Extend(ss.ID, who)
	s.sessionAnswer(w, r, &ss, who, err)
}

func (s *Server) getSessionOutput(w http.ResponseWriter, r *http.Request) {
	ss, _, ok := s.session(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "session-"+ss.ID+".txt"))
	w.Write(s.opts.Sessions.Output(ss.ID))
}

func (s *Server) getSessionTranscript(w http.ResponseWriter, r *http.Request) {
	ss, _, ok := s.session(w, r)
	if !ok {
		return
	}
	cfg := s.opts.Sessions.Config()
	if !ss.Recorded || cfg.TranscriptDir == "" {
		jsonError(w, http.StatusNotFound, "No transcript was kept of this session.")
		return
	}
	f, err := os.Open(sessions.TranscriptPath(cfg.TranscriptDir, ss.Cluster, ss.ID))
	if err != nil {
		jsonError(w, http.StatusNotFound, "The transcript is gone.")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "session-"+ss.ID+".cast"))
	http.ServeContent(w, r, "", time.Time{}, f)
}

// ---------- WebSockets ----------

var browserUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// Only the console's own pages open a terminal (X-06): the browser
	// sends no CORS check for a WebSocket, so the origin is checked here.
	CheckOrigin: func(r *http.Request) bool { return auth.SameSite(r) },
}

var cliUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 << 10,
	WriteBufferSize: 64 << 10,
	// The CLI is no browser: it sends no Origin, and the forward's token
	// says who it is.
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

// control is a text message on a terminal's WebSocket.
type control struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	By   string `json:"by,omitempty"`
	Note string `json:"note,omitempty"`
}

// sessionAttach serves a terminal: its output as binary messages, what the
// owner types back as binary messages, and resizes as text (TB-10, TB-15);
// on /watch a SuperAdmin sees it without typing (TB-55).
func (s *Server) sessionAttach(w http.ResponseWriter, r *http.Request) {
	ss, who, ok := s.session(w, r)
	if !ok {
		return
	}
	watch := strings.HasSuffix(r.URL.Path, "/watch")
	if !auth.SameSite(r) {
		jsonError(w, http.StatusForbidden, "this can only be done from the console itself")
		return
	}
	v, replay, err := s.opts.Sessions.Attach(ss.ID, who, watch)
	if err != nil {
		s.sessionAnswer(w, r, &ss, who, err)
		return
	}
	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.opts.Sessions.Detach(ss.ID, v)
		return
	}
	defer conn.Close()
	defer s.opts.Sessions.Detach(ss.ID, v)
	var wmu sync.Mutex
	send := func(kind int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return conn.WriteMessage(kind, b)
	}
	if len(replay) > 0 && send(websocket.BinaryMessage, replay) != nil {
		return
	}
	ended := func() {
		cur, _ := s.opts.Sessions.Get(ss.ID)
		b, _ := json.Marshal(control{Type: "ended", By: cur.EndedBy, Note: cur.EndNote})
		_ = send(websocket.TextMessage, b)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			kind, b, err := conn.ReadMessage()
			if err != nil {
				return
			}
			switch kind {
			case websocket.BinaryMessage:
				if err := s.opts.Sessions.Input(ss.ID, v, b); err != nil && !errors.Is(err, sessions.ErrNotYours) {
					return
				}
			case websocket.TextMessage:
				var c control
				if json.Unmarshal(b, &c) != nil {
					continue
				}
				switch c.Type {
				case "resize":
					s.opts.Sessions.Resize(ss.ID, v, sessions.Size{Cols: c.Cols, Rows: c.Rows})
				case "keep":
					_, _ = s.opts.Sessions.Keep(ss.ID, who)
				}
			}
		}
	}()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b := <-v.Out:
			if send(websocket.BinaryMessage, b) != nil {
				return
			}
		case <-v.Done():
			// Drain what is still queued, then say how it ended.
		drain:
			for {
				select {
				case b := <-v.Out:
					_ = send(websocket.BinaryMessage, b)
				default:
					break drain
				}
			}
			ended()
			return
		case <-done:
			return
		case <-ping.C:
			wmu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			wmu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// wsFrames carries tunnel frames over a WebSocket.
type wsFrames struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (f *wsFrames) ReadFrame() (tunnel.Frame, error) {
	for {
		kind, b, err := f.c.ReadMessage()
		if err != nil {
			return tunnel.Frame{}, err
		}
		if kind == websocket.BinaryMessage {
			return tunnel.Decode(b)
		}
	}
}

func (f *wsFrames) WriteFrame(fr tunnel.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.c.SetWriteDeadline(time.Now().Add(60 * time.Second))
	return f.c.WriteMessage(websocket.BinaryMessage, tunnel.Encode(fr))
}

// sessionTunnel serves the 221b CLI of a port forward (TB-19, TB-20): it
// carries no cookie; the forward's token, shown only in its owner's pane,
// is its credential, and the session runs as its owner.
func (s *Server) sessionTunnel(w http.ResponseWriter, r *http.Request) {
	if !s.toolboxReady(w) {
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		jsonError(w, http.StatusUnauthorized, "the CLI sends the forward's token")
		return
	}
	ss, ok := s.opts.Sessions.Get(r.PathValue("id"))
	if !ok || ss.Cluster != clusterID(r) {
		jsonError(w, http.StatusNotFound, "There is no such session.")
		return
	}
	conn, err := cliUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	fc := &wsFrames{c: conn}
	err = s.opts.Sessions.Tunnel(r.Context(), ss.ID, token, fc)
	switch {
	case errors.Is(err, sessions.ErrToken), errors.Is(err, sessions.ErrNotFound):
		_ = fc.WriteFrame(tunnel.Frame{Type: tunnel.End, Payload: []byte("The token does not match this session.")})
	case errors.Is(err, sessions.ErrEnded):
		_ = fc.WriteFrame(tunnel.Frame{Type: tunnel.End, Payload: []byte("The session has ended.")})
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

// sessionsPart streams the person's sessions on a cluster, for the dock:
// all of them, then each that changes.
func (s *Server) sessionsPart(r *http.Request) streamPart {
	return func(ctx context.Context, write func(string, any) error) {
		cl := clusterID(r)
		who := s.sessionWho(r)
		sub := s.opts.Sessions.Subscribe(cl)
		defer s.opts.Sessions.Unsubscribe(sub)
		all := []sessionView{}
		for _, ss := range s.opts.Sessions.List(cl, who) {
			all = append(all, s.sessionViewOf(r, ss, who))
		}
		if write("all", all) != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-sub.C:
				changed := map[string]bool{id: true}
				timer := time.After(100 * time.Millisecond)
			batch:
				for {
					select {
					case id := <-sub.C:
						changed[id] = true
					case <-timer:
						break batch
					case <-ctx.Done():
						return
					}
				}
				for id := range changed {
					ss, ok := s.opts.Sessions.Get(id)
					if !ok || !sessions.Visible(ss, who) {
						if write("gone", map[string]string{"id": id}) != nil {
							return
						}
						continue
					}
					if write("session", s.sessionViewOf(r, ss, who)) != nil {
						return
					}
				}
			}
		}
	}
}
