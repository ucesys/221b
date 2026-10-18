package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/watson"
)

// Watson's API (Phase 6): the commands of a cluster as one person sees
// them, streamed; submitting, approving, discarding, editing and stopping
// them; and the administrator's two-person rule.

func (s *Server) watsonRoutes() {
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/{cluster}/"} {
		s.handle("GET "+base+"watson", "watson", s.getWatson)
		s.handle("GET "+base+"watson/events", "watson-events", s.watsonEvents)
		s.handle("POST "+base+"watson/commands", "watson-submit", s.postWatsonCommands)
		s.handle("GET "+base+"watson/commands/{id}", "watson-command", s.getWatsonCommand)
		s.handle("POST "+base+"watson/commands/{id}/approve", "watson-approve", s.postWatsonApprove)
		s.handle("POST "+base+"watson/commands/{id}/discard", "watson-discard", s.postWatsonDiscard)
		s.handle("POST "+base+"watson/commands/{id}/stop", "watson-stop", s.postWatsonStop)
		s.handle("PUT "+base+"watson/commands/{id}", "watson-edit", s.putWatsonCommand)
		s.handle("POST "+base+"watson/clear", "watson-clear", s.postWatsonClear)
	}
	s.handle("GET /api/v1/watson/config", "watson-config", s.getWatsonConfig)
	s.handle("PUT /api/v1/watson/config", "watson-config-update", s.putWatsonConfig)
}

// watsonCluster describes the request's cluster to Watson.
func (s *Server) watsonCluster(w http.ResponseWriter, r *http.Request) (watson.Cluster, bool) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return watson.Cluster{}, false
	}
	id := clusterID(r)
	c := watson.Cluster{ID: id, Name: id, Env: clusters.EnvOther}
	var cfg *rest.Config
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(id); ok {
			c.Name, c.Env = sum.Name, sum.Env
			c.ReadOnly = sum.Mode != clusters.ModeWrite
			if sum.Local {
				c.ReadOnlyHow = "To allow changes there, install the chart with localCluster.mode=write."
			} else {
				c.ReadOnlyHow = "An administrator can allow changes in the connection's settings (Read and change)."
			}
		}
		if _, client, ok := s.opts.Clusters.Lookup(id); ok && client != nil {
			cfg = client.RestConfig()
		}
	} else if rc, ok := s.opts.Cluster.(interface{ RestConfig() *rest.Config }); ok {
		cfg = rc.RestConfig()
		c.ReadOnly = !s.opts.WatsonLocalWrite
	}
	if cfg == nil {
		jsonError(w, http.StatusServiceUnavailable, "the connection to "+id+" is not running; see its cluster page")
		return c, false
	}
	c.Config = cfg
	// With rbac.enforce strict, a cluster whose roles have not been compared
	// yet refuses changes (RB-36). A connection just made or a console just
	// started is compared now, from the cache, rather than at the next round.
	if s.opts.Roles.Enforce == "strict" {
		if _, ok := s.monitor().Get(id); !ok {
			s.checkRoles(id, cl)
		}
	}
	c.Locked = s.RoleRefusal(id)
	return c, true
}

// watsonWho is the person acting.
func (s *Server) watsonWho(r *http.Request) watson.Who {
	sess, _ := auth.SessionFrom(r.Context())
	w := watson.Who{Session: sess.ID, Person: s.person(r), Name: firstOf(sess.User.Name, sess.User.Email, sess.User.Username, "Anonymous"),
		Audit: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}, From: fromAddr(r)}
	if !w.Person.Nobody() {
		w.User = sess.User.Username
		if w.User == "" {
			w.User = sess.User.Subject
		}
	}
	return w
}

type watsonStatus struct {
	// Available says Watson runs here at all.
	Available bool `json:"available"`
	// ReadOnly says changes are refused here (P6-05), and How where to
	// allow them.
	ReadOnly bool   `json:"readOnly"`
	How      string `json:"how,omitempty"`
	// TwoPerson says changes here need a second person (P6-07).
	TwoPerson bool   `json:"twoPerson"`
	Env       string `json:"env"`
	// SignedIn says the console can tell people apart.
	SignedIn bool   `json:"signedIn"`
	Me       string `json:"me"`
	Prompt   string `json:"prompt"`
	Lapse    int    `json:"lapseSeconds"`
	// Supported lists the commands, for the help line.
	Reads   []string `json:"reads"`
	Changes []string `json:"changes"`
	Admin   bool     `json:"admin"`
}

func (s *Server) watsonTwoPerson(c watson.Cluster) bool {
	if s.opts.Settings == nil {
		return false
	}
	return c.Env == clusters.EnvProduction && s.opts.Settings.Current().WatsonOrDefault().TwoPerson
}

func (s *Server) getWatson(w http.ResponseWriter, r *http.Request) {
	c, ok := s.watsonCluster(w, r)
	if !ok {
		return
	}
	b, _ := json.Marshal(s.watsonStatusFor(r, c))
	writeJSON(w, r, b)
}

func (s *Server) watsonStatusFor(r *http.Request, c watson.Cluster) watsonStatus {
	who := s.watsonWho(r)
	sess, _ := auth.SessionFrom(r.Context())
	st := watsonStatus{Available: s.opts.Watson != nil, ReadOnly: c.ReadOnly, TwoPerson: s.watsonTwoPerson(c), Env: c.Env, SignedIn: who.User != "", Me: who.Name,
		Lapse: 600, Reads: watson.Supported(false), Changes: watson.Supported(true), Admin: sess.Admin || s.opts.Auth == nil}
	if c.ReadOnly {
		st.How = c.ReadOnlyHow
	}
	user := "watson"
	if who.User != "" {
		user, _, _ = strings.Cut(who.User, "@")
	}
	st.Prompt = user + "@" + c.Name + ":~$"
	return st
}

// watsonItem is a command as one person sees it.
type watsonItem struct {
	ID        string        `json:"id"`
	Command   string        `json:"command"`
	Kind      string        `json:"kind"` // read or change
	From      string        `json:"from"`
	Why       string        `json:"why,omitempty"`
	Status    string        `json:"status"`
	By        string        `json:"by"`
	Mine      bool          `json:"mine"`
	Created   time.Time     `json:"created"`
	Expires   *time.Time    `json:"expires,omitempty"`
	Plan      *watson.Plan  `json:"plan,omitempty"`
	Approver  string        `json:"approver,omitempty"`
	Started   *time.Time    `json:"started,omitempty"`
	Finished  *time.Time    `json:"finished,omitempty"`
	Exit      int           `json:"exit"`
	Reason    string        `json:"reason,omitempty"`
	Rule      string        `json:"rule,omitempty"`
	CanAct    bool          `json:"canApprove"`
	Waiting   string        `json:"waiting,omitempty"`
	Lines     []watson.Line `json:"lines,omitempty"`
	LineStart int           `json:"lineStart"`
	Rev       int           `json:"rev"`
}

func (s *Server) watsonView(it watson.Item, who watson.Who, c watson.Cluster, lines []watson.Line, start int) watsonItem {
	v := watsonItem{ID: it.ID, Command: it.Line, Kind: map[bool]string{true: "change", false: "read"}[it.Change], From: it.From, Why: it.Why, Status: it.Status,
		By: it.Requester.Name, Mine: it.Requester.Session == who.Session || (who.User != "" && it.Requester.User == who.User),
		Created: it.Created, Plan: it.Plan, Exit: it.Exit, Reason: it.Reason, Rule: it.Rule, Lines: lines, LineStart: start, Rev: it.Rev}
	if !it.Expires.IsZero() {
		e := it.Expires
		v.Expires = &e
	}
	if it.Approver != nil {
		v.Approver = it.Approver.Name
	}
	if !it.Started.IsZero() {
		t := it.Started
		v.Started = &t
	}
	if !it.Finished.IsZero() {
		t := it.Finished
		v.Finished = &t
	}
	if it.Status == watson.StatusPending {
		v.CanAct = true
		if it.Change && s.watsonTwoPerson(c) && v.Mine {
			v.CanAct, v.Waiting = false, "Waiting for a second person: changes to "+c.Name+" need someone else's approval."
		}
		if !it.Change && !v.Mine {
			v.CanAct = false
		}
	}
	return v
}

func (s *Server) watsonEvents(w http.ResponseWriter, r *http.Request) {
	if s.opts.Watson == nil {
		jsonError(w, http.StatusNotFound, "Watson is not part of this console")
		return
	}
	c, ok := s.watsonCluster(w, r)
	if !ok {
		return
	}
	s.serveStream(w, r, map[string]streamPart{"": s.watsonPart(r, c)})
}

// watsonPart streams the person's commands on a cluster: Watson's status,
// every command, then each change.
func (s *Server) watsonPart(r *http.Request, c watson.Cluster) streamPart {
	return func(ctx context.Context, write func(string, any) error) {
		who := s.watsonWho(r)
		sub := s.opts.Watson.Subscribe(c.ID)
		defer s.opts.Watson.Unsubscribe(sub)
		status := s.watsonStatusFor(r, c)
		if write("status", status) != nil {
			return
		}
		// What each item has had: its revision and how many lines.
		sent := map[string][2]int{}
		var all []watsonItem
		for _, it := range s.opts.Watson.Items(c, who) {
			snap, lines, ok := s.opts.Watson.Snapshot(it.ID, 0)
			if !ok {
				continue
			}
			all = append(all, s.watsonView(snap, who, c, lines, 0))
			sent[it.ID] = [2]int{snap.Rev, len(lines)}
		}
		if all == nil {
			all = []watsonItem{}
		}
		if write("items", all) != nil {
			return
		}
		statusTick := time.NewTicker(5 * time.Second)
		defer statusTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-statusTick.C:
				c2, ok := s.watsonClusterQuiet(r)
				if ok {
					c = c2
				}
				if now := s.watsonStatusFor(r, c); fmt.Sprint(now) != fmt.Sprint(status) {
					status = now
					if write("status", status) != nil {
						return
					}
				}
			case id := <-sub.C:
				// Batch what changes within 60 ms: output arrives a line at a time.
				changed := map[string]bool{id: true}
				timer := time.After(60 * time.Millisecond)
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
					prev, known := sent[id]
					snap, lines, ok := s.opts.Watson.Snapshot(id, prev[1])
					if !ok || !s.opts.Watson.Visible(&snap, who, c) {
						if known {
							delete(sent, id)
							if write("gone", map[string]string{"id": id}) != nil {
								return
							}
						}
						continue
					}
					if known && snap.Rev == prev[0] {
						continue
					}
					v := s.watsonView(snap, who, c, lines, prev[1])
					sent[id] = [2]int{snap.Rev, prev[1] + len(lines)}
					if write("item", v) != nil {
						return
					}
				}
			}
		}
	}
}

// watsonClusterQuiet resolves the cluster without answering the request.
func (s *Server) watsonClusterQuiet(r *http.Request) (watson.Cluster, bool) {
	rec := &discardWriter{h: http.Header{}}
	return s.watsonCluster(rec, r)
}

type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

const maxCommandBody = 32 << 10

func (s *Server) watsonReady(w http.ResponseWriter, r *http.Request) (watson.Cluster, watson.Who, bool) {
	if s.opts.Watson == nil {
		jsonError(w, http.StatusNotFound, "Watson is not part of this console")
		return watson.Cluster{}, watson.Who{}, false
	}
	c, ok := s.watsonCluster(w, r)
	if !ok {
		return c, watson.Who{}, false
	}
	return c, s.watsonWho(r), true
}

func (s *Server) postWatsonCommands(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the command as application/json")
		return
	}
	var in struct {
		Command string `json:"command"`
		From    string `json:"from"`
		Why     string `json:"why"`
		// Run runs reads at once; changes always wait for approval.
		Run bool `json:"run"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the command is not valid: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Command) == "" {
		jsonError(w, http.StatusBadRequest, "Type a kubectl or helm command.")
		return
	}
	from := strings.TrimSpace(in.From)
	if from == "" {
		from = "Typed"
	}
	if len(from) > 200 {
		from = from[:200]
	}
	why := strings.TrimSpace(in.Why)
	if len(why) > 2000 {
		why = why[:2000]
	}
	items := s.opts.Watson.Submit(r.Context(), c, who, in.Command, from, why, in.Run)
	out := make([]watsonItem, 0, len(items))
	for _, it := range items {
		snap, lines, _ := s.opts.Watson.Snapshot(it.ID, 0)
		out = append(out, s.watsonView(snap, who, c, lines, 0))
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(b)
}

// watsonAnswer turns the service's answer into a response.
func (s *Server) watsonAnswer(w http.ResponseWriter, c watson.Cluster, who watson.Who, it *watson.Item, err error) {
	var ref *watson.Refusal
	switch {
	case errors.Is(err, watson.ErrNotFound):
		jsonError(w, http.StatusNotFound, "There is no such command, or it is not yours to see.")
		return
	case errors.Is(err, watson.ErrNotYours):
		jsonError(w, http.StatusForbidden, "That command is someone else's.")
		return
	case errors.As(err, &ref):
		jsonError(w, http.StatusForbidden, ref.Message)
		return
	case errors.Is(err, watson.ErrState):
		jsonError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), watson.ErrState.Error()+": "))
		return
	case err != nil:
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	snap, lines, _ := s.opts.Watson.Snapshot(it.ID, 0)
	b, _ := json.Marshal(s.watsonView(snap, who, c, lines, 0))
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// getWatsonCommand is one command with all its output, for scripts and
// tests; the panel follows the stream.
func (s *Server) getWatsonCommand(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	snap, lines, ok := s.opts.Watson.Snapshot(r.PathValue("id"), 0)
	if !ok || snap.Cluster != c.ID || !s.opts.Watson.Visible(&snap, who, c) {
		s.watsonAnswer(w, c, who, nil, watson.ErrNotFound)
		return
	}
	b, _ := json.Marshal(s.watsonView(snap, who, c, lines, 0))
	writeJSON(w, r, b)
}

// ofCluster says whether the command was planned on the cluster in the
// URL. It is approved, discarded, stopped or edited only there, where its
// card, dry run and guards were made; asked through another cluster's URL
// it does not exist.
func (s *Server) ofCluster(c watson.Cluster, id string) bool {
	snap, _, ok := s.opts.Watson.Snapshot(id, 0)
	return ok && snap.Cluster == c.ID
}

func (s *Server) postWatsonApprove(w http.ResponseWriter, r *http.Request) {
	// A change is approved with the groups the directory gives now, not
	// the ones from a few minutes ago (RB-37).
	r, ok := s.rereadSession(w, r)
	if !ok {
		return
	}
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if !s.ofCluster(c, r.PathValue("id")) {
		s.watsonAnswer(w, c, who, nil, watson.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	it, err := s.opts.Watson.Approve(ctx, c, who, r.PathValue("id"))
	s.watsonAnswer(w, c, who, it, err)
}

func (s *Server) postWatsonDiscard(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if !s.ofCluster(c, r.PathValue("id")) {
		s.watsonAnswer(w, c, who, nil, watson.ErrNotFound)
		return
	}
	it, err := s.opts.Watson.Discard(c, who, r.PathValue("id"))
	s.watsonAnswer(w, c, who, it, err)
}

func (s *Server) postWatsonStop(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if !s.ofCluster(c, r.PathValue("id")) {
		s.watsonAnswer(w, c, who, nil, watson.ErrNotFound)
		return
	}
	it, err := s.opts.Watson.Stop(c, who, r.PathValue("id"))
	s.watsonAnswer(w, c, who, it, err)
}

func (s *Server) putWatsonCommand(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	if !s.ofCluster(c, r.PathValue("id")) {
		s.watsonAnswer(w, c, who, nil, watson.ErrNotFound)
		return
	}
	var in struct {
		Command string `json:"command"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the command is not valid: "+err.Error())
		return
	}
	items, err := s.opts.Watson.Edit(r.Context(), c, who, r.PathValue("id"), in.Command)
	if err != nil {
		s.watsonAnswer(w, c, who, nil, err)
		return
	}
	out := make([]watsonItem, 0, len(items))
	for _, it := range items {
		snap, lines, _ := s.opts.Watson.Snapshot(it.ID, 0)
		out = append(out, s.watsonView(snap, who, c, lines, 0))
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (s *Server) postWatsonClear(w http.ResponseWriter, r *http.Request) {
	c, who, ok := s.watsonReady(w, r)
	if !ok {
		return
	}
	s.opts.Watson.Clear(c, who)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- the administrator's setting ----------

func (s *Server) watsonAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Settings == nil {
		jsonError(w, http.StatusNotFound, "settings are not part of this console")
		return false
	}
	if s.opts.Auth != nil {
		sess, _ := auth.SessionFrom(r.Context())
		if !sess.Admin {
			jsonError(w, http.StatusForbidden, "Only administrators change Watson's settings.")
			return false
		}
	}
	return true
}

func (s *Server) getWatsonConfig(w http.ResponseWriter, r *http.Request) {
	if !s.watsonAdmin(w, r) {
		return
	}
	b, _ := json.Marshal(s.opts.Settings.Current().WatsonOrDefault())
	writeJSON(w, r, b)
}

func (s *Server) putWatsonConfig(w http.ResponseWriter, r *http.Request) {
	if !s.watsonAdmin(w, r) {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the settings as application/json")
		return
	}
	var in settings.Watson
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the settings are not valid: "+err.Error())
		return
	}
	before := s.opts.Settings.Current().WatsonOrDefault()
	if _, err := s.opts.Settings.UpdateWatson(r.Context(), in); err != nil {
		jsonError(w, http.StatusBadGateway, "The settings were not saved: "+err.Error())
		return
	}
	detail := "no change"
	if before.TwoPerson != in.TwoPerson {
		detail = map[bool]string{true: "two-person rule on for production clusters", false: "two-person rule off"}[in.TwoPerson]
	}
	s.record(r, ActionWatsonSettings, "watson", detail)
	s.getWatsonConfig(w, r)
}

// ActionWatsonSettings is the audit action of a change to Watson's settings.
const ActionWatsonSettings = "watson.settings"

// rereadSession reads the person's directory groups again before a change
// is approved; a person no longer in a listed group is signed out.
func (s *Server) rereadSession(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	again, ok := s.opts.Auth.(interface {
		Reread(auth.Session) (auth.Session, bool)
	})
	if !ok {
		return r, true
	}
	sess, has := auth.SessionFrom(r.Context())
	if !has || sess.ID == "" {
		return r, true
	}
	fresh, ok := again.Reread(sess)
	if !ok {
		jsonError(w, http.StatusUnauthorized, "Your session has ended: you are no longer in a group that may use the console. Sign in again.")
		return r, false
	}
	return r.WithContext(auth.WithSession(r.Context(), fresh)), true
}
