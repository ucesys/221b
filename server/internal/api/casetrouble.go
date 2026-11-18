package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Troubleshooting a case in its drawer (Phase 12, wave 6f, CF-46 to
// CF-49, ADR-134) ---
//
// Sherlock on a case: one conversation per case, kept with the case
// (casework.Chats), on the case's cluster, shared by the people who work
// the cases; the console-wide roles and those who may edit jobs, who read
// the cases, follow it without asking. Each question runs as its asker,
// with their own tools and rights, and the first carries the case as
// data. Watson on a case is Watson's own API with from = "case:C-12":
// checked here (the caller works the cases, the case is open and on that
// cluster), and logged on the case, as each command ends too.

// caseTroubleRoutes serves a case's conversation under the case.
func (s *Server) caseTroubleRoutes() {
	base := casework.Prefix + "/{id}/sherlock"
	s.handle("GET "+base+"/events", "case-sherlock-events", s.caseSherlockEvents)
	s.handle("GET "+base+"/conversation", "case-sherlock-conversation", s.getCaseSherlock)
	s.handle("POST "+base+"/ask", "case-sherlock-ask", s.postCaseSherlockAsk)
	s.handle("POST "+base+"/stop", "case-sherlock-stop", s.postCaseSherlockStop)
	s.handle("POST "+base+"/ui/{uid}", "case-sherlock-ui", s.postCaseSherlockUI)
}

// caseworkFail answers an error of the cases.
func caseworkFail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, casework.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, casework.ErrDenied):
		code = http.StatusForbidden
	case errors.Is(err, casework.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, casework.ErrState):
		code = http.StatusConflict
	}
	jsonError(w, code, err.Error())
}

// caseChat reads the case a request is about, for its conversation: who
// asks, the case, its key and its cluster. work asks the case-working
// permission, otherwise reading the cases is enough; withContext reads
// what the conversation starts from.
func (s *Server) caseChat(w http.ResponseWriter, r *http.Request, work, withContext bool) (casework.Caller, casework.Trouble, sherlock.Key, Cluster, bool) {
	var none sherlock.Key
	c, ok := s.caseworkCaller(r)
	if !ok {
		jsonError(w, http.StatusUnauthorized, "sign in first")
		return c, casework.Trouble{}, none, nil, false
	}
	switch {
	case work && !c.Approver:
		jsonError(w, http.StatusForbidden, "Only the people who work the cases ask Sherlock on a case.")
		return c, casework.Trouble{}, none, nil, false
	case !c.Approver && !c.Read:
		jsonError(w, http.StatusForbidden, "Cases are for the people who work them, the console-wide roles and those who may edit jobs.")
		return c, casework.Trouble{}, none, nil, false
	}
	t, err := s.opts.Casework.Trouble(r.Context(), c, r.PathValue("id"), withContext)
	if err != nil {
		caseworkFail(w, err)
		return c, t, none, nil, false
	}
	if t.Cluster == "" {
		jsonError(w, http.StatusConflict, t.ID+" names no cluster: name one (Edit…) to troubleshoot it here.")
		return c, t, none, nil, false
	}
	r.SetPathValue("cluster", t.Cluster)
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return c, t, none, nil, false
	}
	return c, t, sherlock.Key{Session: casework.ChatSession(t.Number), Cluster: t.Cluster}, cl, true
}

// caseSherlockEvents streams a case's conversation as the person's own is
// streamed: its status, the conversation, then each change.
func (s *Server) caseSherlockEvents(w http.ResponseWriter, r *http.Request) {
	_, t, key, _, ok := s.caseChat(w, r, false, false)
	if !ok {
		return
	}
	s.serveStream(w, r, map[string]streamPart{"": s.conversationPart(r, key, t.Cluster)})
}

// getCaseSherlock is a case's conversation as it is now, for scripts and
// tests; the drawer follows the stream.
func (s *Server) getCaseSherlock(w http.ResponseWriter, r *http.Request) {
	_, _, key, _, ok := s.caseChat(w, r, false, false)
	if !ok {
		return
	}
	b, _ := json.Marshal(s.opts.Sherlock.Conversation(key).View())
	writeJSON(w, r, b)
}

// postCaseSherlockAsk asks Sherlock on a case's conversation, as the
// person asking: their tools and rights, their name on the question, the
// case as data before the first (CF-47).
func (s *Server) postCaseSherlockAsk(w http.ResponseWriter, r *http.Request) {
	c, t, key, cl, ok := s.caseChat(w, r, true, true)
	if !ok {
		return
	}
	if !t.Open {
		jsonError(w, http.StatusConflict, t.ID+" is closed: reopen it to troubleshoot it.")
		return
	}
	conv, ok := s.askShared(w, r, key, cl, audit.Who{Subject: c.Subject, Email: c.Email, Name: c.Name}, t.Context, "Sherlock is still answering the last question on this case; wait for it or stop it.")
	if !ok {
		return
	}
	s.opts.Casework.ChatAsked(c, t.Number, t.Cluster)
	b, _ := json.Marshal(conv.View())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(b)
}

// askShared asks Sherlock on a shared conversation, a case's or a
// proposed change's (CF-47, CF-61): as the person asking, their tools and
// rights, their name on the question, the record as data before the first.
// It answers the request when the question is not asked.
func (s *Server) askShared(w http.ResponseWriter, r *http.Request, key sherlock.Key, cl Cluster, who audit.Who, data, busy string) (*sherlock.Conversation, bool) {
	if status := s.sherlockStatusFor(r, key.Cluster); status.Reason != "" {
		jsonError(w, http.StatusConflict, status.Reason)
		return nil, false
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the question as application/json")
		return nil, false
	}
	var in struct {
		Question string `json:"question"`
		// Window and View are the asker's console window and its view, as
		// in their own conversation: Sherlock's view actions change only
		// that window (SR-19).
		Window string `json:"window"`
		View   string `json:"view"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQuestionBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the question is not valid: "+err.Error())
		return nil, false
	}
	name, version := key.Cluster, ""
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(key.Cluster); ok {
			name, version = sum.Name, sum.Version
		}
	}
	notes, instructions := s.sherlockSettings().Notes, s.sherlockSettings().Instructions
	if s.opts.AI != nil {
		notes, instructions = "", ""
	}
	sess, _ := auth.SessionFrom(r.Context())
	conv, err := s.opts.Sherlock.Ask(sherlock.Question{
		Key:          key,
		Text:         in.Question,
		Cluster:      name,
		Version:      version,
		Platform:     s.platformPrompt(r, cl, name, version),
		Notes:        notes,
		Instructions: instructions,
		Tools:        s.toolboxFor(r, key.Cluster, cl),
		Who:          who,
		From:         fromAddr(r),
		// The view actions go to the asker's window only (SR-19).
		Window:        windowID(in.Window),
		WindowSession: sess.ID,
		View:          in.View,
		ViewChanges:   s.sherlockSettings().ChangesView(),
		// Asked from a case or a proposed change, Sherlock knows the Cases
		// pages (IB-10).
		Cases:   true,
		Context: data,
		// The exceptions are for those who see the cases (KN-32): one
		// who only reads this conversation may not.
		Exceptions: s.readsExceptions(r),
	})
	var capErr *sherlock.ErrCap
	switch {
	case errors.As(err, &capErr):
		jsonError(w, http.StatusTooManyRequests, err.Error())
		return nil, false
	case errors.Is(err, sherlock.ErrBusy):
		jsonError(w, http.StatusConflict, busy)
		return nil, false
	case err != nil:
		jsonError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return conv, true
}

// postCaseSherlockUI takes the asker's window's result of a view action
// on a case's conversation (SR-19): only the session that asked may post
// it, as in a person's own conversation (P6U-17).
func (s *Server) postCaseSherlockUI(w http.ResponseWriter, r *http.Request) {
	_, _, key, _, ok := s.caseChat(w, r, true, false)
	if !ok {
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	s.deliverUI(w, r, key, r.PathValue("uid"), sess.ID)
}

// postCaseSherlockStop stops the answer coming on a case's conversation:
// anyone who works the case may.
func (s *Server) postCaseSherlockStop(w http.ResponseWriter, r *http.Request) {
	_, _, key, _, ok := s.caseChat(w, r, true, false)
	if !ok {
		return
	}
	b, _ := json.Marshal(map[string]bool{"stopped": s.opts.Sherlock.Stop(key)})
	writeJSON(w, r, b)
}

// caseCommandFrom checks a Watson command sent from a case's drawer
// ("case:C-12"): the caller works the cases, the case is open and on the
// command's cluster (CF-48). It answers the request when not.
func (s *Server) caseCommandFrom(w http.ResponseWriter, r *http.Request, n int, cluster string) (casework.Caller, bool) {
	if s.opts.Casework == nil {
		jsonError(w, http.StatusBadRequest, "there are no cases here to run a command from")
		return casework.Caller{}, false
	}
	c, ok := s.caseworkCaller(r)
	if !ok {
		jsonError(w, http.StatusUnauthorized, "sign in first")
		return c, false
	}
	if err := s.opts.Casework.CheckCommand(r.Context(), c, n, cluster); err != nil {
		caseworkFail(w, err)
		return c, false
	}
	return c, true
}

// caseCommand is a Watson command as a case logs it: never its output.
func caseCommand(it watson.Item) casework.Command {
	cmd := casework.Command{ID: it.ID, Line: it.Line, By: it.Requester.Name, At: it.Created, Change: it.Change, Status: it.Status}
	if !it.Started.IsZero() && !it.Finished.IsZero() {
		exit := it.Exit
		cmd.Exit = &exit
	}
	if !it.Finished.IsZero() {
		t := it.Finished
		cmd.Finished = &t
	}
	if it.Approver != nil && it.Change {
		cmd.Approver = it.Approver.Name
	}
	return cmd
}

// logCaseCommands logs the commands just submitted from a case.
func (s *Server) logCaseCommands(r *http.Request, c casework.Caller, n int, items []*watson.Item) {
	var cmds []casework.Command
	for _, it := range items {
		snap, _, ok := s.opts.Watson.Snapshot(it.ID, 0)
		if !ok {
			continue
		}
		cmds = append(cmds, caseCommand(snap))
	}
	if err := s.opts.Casework.LogCommands(r.Context(), n, cmds, c); err != nil {
		s.opts.Logger.Warn("a case's command log could not be kept", "case", n, "err", err)
	}
}

// caseCommandEnded hears every Watson command that ends: one from a case
// is logged there with how it ended.
func (s *Server) caseCommandEnded(it watson.Item) {
	n, ok := casework.CaseFrom(it.From)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.opts.Casework.CommandEnded(ctx, n, caseCommand(it)); err != nil {
		s.opts.Logger.Warn("how a case's command ended could not be kept", "case", n, "err", err)
	}
}

// --- end of troubleshooting a case.
