package knowledge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/confluence"
)

// Prefix is where the API is served (every route runs on the leader,
// which the console's proxy forwards to).
const Prefix = "/api/v1/knowledge/"

// HookPath says whether a path is the webhook's, which a git host or a
// bucket calls with a token rather than a session.
func HookPath(p string) bool { return strings.HasPrefix(p, Prefix+"hook/") }

// maxBody bounds a request; maxUpload an upload's, base64 included.
const (
	maxBody   = 256 << 10
	maxUpload = MaxDocument/3*4 + 64<<10
)

type handler struct {
	s      *Service
	caller func(*http.Request) (Caller, bool)
	mux    *http.ServeMux
}

// Handler serves the API; caller says who a request is from, false when
// nobody is signed in.
func (s *Service) Handler(caller func(*http.Request) (Caller, bool)) http.Handler {
	h := &handler{s: s, caller: caller, mux: http.NewServeMux()}
	route := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, c Caller)) {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+Prefix+path, func(w http.ResponseWriter, r *http.Request) {
			c, ok := h.caller(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "sign in first")
				return
			}
			fn(w, r, c)
		})
	}
	route("GET settings", h.getSettings)
	route("PUT settings", h.putSettings)
	route("POST sources", h.postSource)
	route("PUT sources/{id}", h.putSource)
	route("DELETE sources/{id}", h.deleteSource)
	route("POST sources/{id}/on", h.postSourceOn)
	route("POST sources/{id}/test", h.postSourceTest)
	// A source's values as typed in its dialog, not saved (KN-26).
	route("POST sources/test", h.postSourceTestTyped)
	route("POST sources/{id}/documents", h.postDocument)
	route("DELETE sources/{id}/documents/{name}", h.deleteDocument)
	route("PUT store", h.putStore)
	route("POST store/test", h.postStoreTest)
	route("POST store/move", h.postStoreMove)
	route("PUT hook", h.putHook)
	route("GET status", h.getStatus)
	route("GET pages", h.getPages)
	route("GET page", h.getPage)
	route("GET search", h.getSearch)
	// --- Mycroft tab (Phase 12, wave 5) ---
	route("GET wiki", h.getWiki)
	route("POST add", h.postAdd)
	route("DELETE added/{name}", h.deleteAdded)
	// A page's own actions (KN-23, KN-24).
	route("POST outdated", h.postOutdated)
	route("GET requests", h.getRequests)
	route("POST requests", h.postRequest)
	route("POST requests/{id}/approve", h.postRequestApprove)
	route("POST requests/{id}/reject", h.postRequestReject)
	route("GET drafts", h.getDrafts)
	route("GET drafts/count", h.getDraftsCount)
	route("GET drafts/{id}", h.getDraft)
	route("POST drafts/{id}/approve", h.postApprove)
	route("POST drafts/{id}/reject", h.postReject)
	route("GET confluence", h.getConfluence)
	route("PUT confluence", h.putConfluence)
	route("POST confluence/test", h.postConfluenceTest)
	route("POST confluence/check", h.postConfluenceCheck)
	route("GET confluence/conflicts", h.getConflicts)
	route("POST confluence/conflicts/{id}", h.postConflict)
	// The webhook: a token, not a session.
	h.mux.HandleFunc("POST "+Prefix+"hook/{target}", h.postHook)
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// fail answers an error with the status it means.
func fail(w http.ResponseWriter, err error) {
	var ce *confluence.Error
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrDenied):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrInvalid), errors.Is(err, confluence.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotLeader):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrNoStore), errors.Is(err, ErrBusy), errors.Is(err, ErrConflictStore), errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.As(err, &ce):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "the request is not the JSON expected: "+err.Error())
		return false
	}
	return true
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.GetView(r.Context(), c)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		StaleDays int `json:"staleDays"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	if err := h.s.SetStaleDays(r.Context(), c, in.StaleDays); err != nil {
		fail(w, err)
		return
	}
	h.getSettings(w, r, c)
}

type sourceInput struct {
	Source Source  `json:"source"`
	Token  *string `json:"token,omitempty"`
	// Keys are a bucket's access key (KN-25): a value replaces, ""
	// removes, absent keeps.
	Keys CredsChange `json:"keys"`
}

// creds is the input's credential change.
func (in sourceInput) creds() CredsChange {
	ch := in.Keys
	if in.Token != nil {
		ch.Token = in.Token
	}
	return ch
}

func (h *handler) postSource(w http.ResponseWriter, r *http.Request, c Caller) {
	var in sourceInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	x, err := h.s.SaveSourceWith(r.Context(), c, in.Source, in.creds(), true)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, x)
}

func (h *handler) putSource(w http.ResponseWriter, r *http.Request, c Caller) {
	var in sourceInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	in.Source.ID = r.PathValue("id")
	x, err := h.s.SaveSourceWith(r.Context(), c, in.Source, in.creds(), false)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}

func (h *handler) deleteSource(w http.ResponseWriter, r *http.Request, c Caller) {
	if err := h.s.RemoveSource(r.Context(), c, r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (h *handler) postSourceOn(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		On bool `json:"on"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	if err := h.s.SetSourceOn(r.Context(), c, r.PathValue("id"), in.On); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": in.On})
}

func (h *handler) postSourceTest(w http.ResponseWriter, r *http.Request, c Caller) {
	t, err := h.s.TestSource(r.Context(), c, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": t.Items, "sample": t.Sample, "skipped": t.Skipped, "skippedWhy": t.SkippedWhy})
}

// sourceTestInput is a source's dialog as typed (KN-26): saved is the id
// of the source being edited, whose saved credential a blank field keeps.
type sourceTestInput struct {
	sourceInput
	Saved string `json:"saved,omitempty"`
}

func (h *handler) postSourceTestTyped(w http.ResponseWriter, r *http.Request, c Caller) {
	var in sourceTestInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	t, err := h.s.TestSourceAsTyped(r.Context(), c, in.Source, in.creds(), in.Saved)
	if errors.Is(err, ErrDenied) {
		fail(w, err)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": t.Items, "sample": t.Sample, "skipped": t.Skipped, "skippedWhy": t.SkippedWhy})
}

func (h *handler) postDocument(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	if !decode(w, r, maxUpload, &in) {
		return
	}
	body, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the document is not base64")
		return
	}
	v, err := h.s.Upload(r.Context(), c, r.PathValue("id"), in.Name, body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"version": v})
}

func (h *handler) deleteDocument(w http.ResponseWriter, r *http.Request, c Caller) {
	if err := h.s.RemoveDocument(r.Context(), c, r.PathValue("id"), r.PathValue("name")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (h *handler) putStore(w http.ResponseWriter, r *http.Request, c Caller) {
	var in StoreInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	if _, err := h.s.SaveStore(r.Context(), c, in); err != nil {
		fail(w, err)
		return
	}
	h.getSettings(w, r, c)
}

func (h *handler) postStoreTest(w http.ResponseWriter, r *http.Request, c Caller) {
	var in StoreInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	check, err := h.s.TestStore(r.Context(), c, in)
	if errors.Is(err, ErrDenied) {
		fail(w, err)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "check": check})
}

func (h *handler) postStoreMove(w http.ResponseWriter, r *http.Request, c Caller) {
	var in StoreInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	n, err := h.s.MoveStore(r.Context(), c, in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"files": n})
}

func (h *handler) putHook(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	if err := h.s.SaveHookToken(r.Context(), c, strings.TrimSpace(in.Token)); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"hook": in.Token != ""})
}

func (h *handler) postHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the request is too large")
		return
	}
	if !h.s.HookAuth(r.Context(), r.Header.Get, body) {
		h.s.audit(audit.Event{Action: ActionHook, Who: audit.Who{Name: "webhook"}, Target: r.PathValue("target"), Outcome: audit.OutcomeDenied, Detail: "a webhook without the knowledge webhook's token was refused", From: r.RemoteAddr})
		writeError(w, http.StatusUnauthorized, "the webhook's token is missing or wrong")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	if err := h.s.Hook(ctx, r.PathValue("target")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (h *handler) getStatus(w http.ResponseWriter, r *http.Request, c Caller) {
	if !c.SuperAdmin {
		fail(w, wrapf(ErrDenied, "only a SuperAdmin sees the syncs"))
		return
	}
	st, err := h.s.Status(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *handler) reader(w http.ResponseWriter, c Caller) bool {
	if !c.Reader {
		fail(w, wrapf(ErrDenied, "sign in to the console to read the wiki"))
		return false
	}
	return true
}

func (h *handler) getPages(w http.ResponseWriter, r *http.Request, c Caller) {
	if !h.reader(w, c) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": h.s.Pages(), "retired": h.s.RetiredPages(), "connected": h.s.Connected()})
}

// --- Mycroft tab (Phase 12, wave 5) ---

func (h *handler) getWiki(w http.ResponseWriter, r *http.Request, c Caller) {
	st, err := h.s.Wiki(r.Context(), c)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *handler) postAdd(w http.ResponseWriter, r *http.Request, c Caller) {
	var in AddInput
	if !decode(w, r, maxUpload, &in) {
		return
	}
	res, err := h.s.Add(r.Context(), c, in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (h *handler) deleteAdded(w http.ResponseWriter, r *http.Request, c Caller) {
	if err := h.s.RemoveAdded(r.Context(), c, r.PathValue("name")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

// --- A page's own actions (KN-23, KN-24) ---

func (h *handler) postOutdated(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if !decode(w, r, MaxNote+64<<10, &in) {
		return
	}
	res, err := h.s.Outdated(r.Context(), c, in.Path, in.Text)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (h *handler) getRequests(w http.ResponseWriter, r *http.Request, c Caller) {
	if !c.Reviewer {
		fail(w, wrapf(ErrDenied, "only an Admin or a SuperAdmin decides a request to retire a page"))
		return
	}
	list, err := h.s.PageRequests(r.Context(), r.URL.Query().Get("all") == "1")
	if err != nil {
		fail(w, err)
		return
	}
	type row struct {
		PageRequest
		CanApprove bool `json:"canApprove"`
		Mine       bool `json:"mine"`
	}
	out := make([]row, 0, len(list))
	for _, x := range list {
		out = append(out, row{PageRequest: x, CanApprove: x.CanApprove(c.ID), Mine: x.By == c.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (h *handler) postRequest(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Action string `json:"action"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	req, err := h.s.AskPage(r.Context(), c, in.Action, in.Path, in.Reason)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (h *handler) postRequestApprove(w http.ResponseWriter, r *http.Request, c Caller) {
	req, err := h.s.DecidePage(r.Context(), c, r.PathValue("id"), true, "")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *handler) postRequestReject(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Comment string `json:"comment"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	req, err := h.s.DecidePage(r.Context(), c, r.PathValue("id"), false, in.Comment)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// --- end of the Mycroft tab.

// pageView is a page as its view shows it: the page, the correction that
// waits for it and the request to retire it or make it active again that
// waits, if any (KN-23, KN-24).
type pageView struct {
	Page
	Correction *Correction  `json:"correction,omitempty"`
	Request    *PageRequest `json:"request,omitempty"`
}

func (h *handler) getPage(w http.ResponseWriter, r *http.Request, c Caller) {
	if !h.reader(w, c) {
		return
	}
	p, ok := h.s.PageAt(r.URL.Query().Get("path"))
	if !ok {
		fail(w, wrapf(ErrNotFound, "no approved page %q", r.URL.Query().Get("path")))
		return
	}
	v := pageView{Page: p}
	// What waits for the page is said to those who may act on the wiki;
	// a failure to read it leaves it out rather than the page.
	if c.MayAdd() {
		v.Correction, _ = h.s.PendingCorrection(r.Context(), p.Path)
		v.Request, _ = h.s.PageRequestFor(r.Context(), p.Path)
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) getSearch(w http.ResponseWriter, r *http.Request, c Caller) {
	if !h.reader(w, c) {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{"hits": h.s.Search(q.Get("q"), q.Get("kind"), 20)})
}

func (h *handler) getDrafts(w http.ResponseWriter, r *http.Request, c Caller) {
	if !c.Reviewer {
		fail(w, wrapf(ErrDenied, "only an Admin or a SuperAdmin reviews the wiki's changes"))
		return
	}
	runs, err := h.s.Runs(r.Context(), r.URL.Query().Get("all") == "1")
	if err != nil {
		fail(w, err)
		return
	}
	type row struct {
		Run
		CanApprove bool `json:"canApprove"`
		Mine       bool `json:"mine"`
	}
	out := make([]row, 0, len(runs))
	for _, x := range runs {
		out = append(out, row{Run: x, CanApprove: x.CanApprove(c.ID), Mine: x.StartedBy != "" && x.StartedBy == c.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (h *handler) getDraftsCount(w http.ResponseWriter, r *http.Request, c Caller) {
	n, err := h.s.Waiting(r.Context(), c)
	if err != nil {
		fail(w, err)
		return
	}
	if p := h.s.o.Publisher; p != nil && c.Reviewer {
		if list, err := p.Conflicts(r.Context()); err == nil {
			n += len(list)
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

func (h *handler) getDraft(w http.ResponseWriter, r *http.Request, c Caller) {
	if !c.Reviewer {
		fail(w, wrapf(ErrDenied, "only an Admin or a SuperAdmin reviews the wiki's changes"))
		return
	}
	run, err := h.s.RunByID(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *handler) postApprove(w http.ResponseWriter, r *http.Request, c Caller) {
	run, err := h.s.Approve(r.Context(), c, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *handler) postReject(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Comment string `json:"comment"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	run, err := h.s.Reject(r.Context(), c, r.PathValue("id"), in.Comment)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// The Confluence publisher (Settings › Issues › Confluence).

func (h *handler) publisher(w http.ResponseWriter) *confluence.Publisher {
	p := h.s.o.Publisher
	if p == nil {
		writeError(w, http.StatusNotFound, "the Confluence publisher is not offered here")
	}
	return p
}

func (h *handler) superAdmin(w http.ResponseWriter, c Caller) bool {
	if !c.SuperAdmin {
		fail(w, wrapf(ErrDenied, "only a SuperAdmin sets up publishing to Confluence"))
		return false
	}
	return true
}

func (h *handler) getConfluence(w http.ResponseWriter, r *http.Request, c Caller) {
	p := h.publisher(w)
	if p == nil || !h.superAdmin(w, c) {
		return
	}
	v, err := p.View(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type confluenceInput struct {
	Settings confluence.Settings `json:"settings"`
	Token    *string             `json:"token,omitempty"`
}

func (h *handler) putConfluence(w http.ResponseWriter, r *http.Request, c Caller) {
	p := h.publisher(w)
	if p == nil || !h.superAdmin(w, c) {
		return
	}
	if err := h.s.leads(); err != nil {
		fail(w, err)
		return
	}
	var in confluenceInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	detail, err := p.Save(r.Context(), in.Settings, in.Token, c.label())
	if err != nil {
		fail(w, err)
		return
	}
	h.s.audit(audit.Event{Action: ActionPublish, Who: c.who(), Outcome: audit.OutcomeOK, From: c.From, Detail: detail})
	h.getConfluence(w, r, c)
}

func (h *handler) postConfluenceTest(w http.ResponseWriter, r *http.Request, c Caller) {
	p := h.publisher(w)
	if p == nil || !h.superAdmin(w, c) {
		return
	}
	var in confluenceInput
	if !decode(w, r, maxBody, &in) {
		return
	}
	res, err := p.Test(r.Context(), in.Settings, in.Token)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "spaces": res})
}

func (h *handler) postConfluenceCheck(w http.ResponseWriter, r *http.Request, c Caller) {
	p := h.publisher(w)
	if p == nil || !h.superAdmin(w, c) {
		return
	}
	if err := h.s.leads(); err != nil {
		fail(w, err)
		return
	}
	h.s.audit(audit.Event{Action: ActionPublish, Who: c.who(), Outcome: audit.OutcomeOK, From: c.From, Detail: "the Confluence pages' check started by hand"})
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
		defer cancel()
		if _, err := p.Check(ctx); err != nil {
			h.s.o.Logger.Warn("the Confluence pages' check failed", "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (h *handler) getConflicts(w http.ResponseWriter, r *http.Request, c Caller) {
	p := h.publisher(w)
	if p == nil {
		return
	}
	if !c.Reviewer {
		fail(w, wrapf(ErrDenied, "only an Admin or a SuperAdmin decides the Confluence pages' edits"))
		return
	}
	list, err := p.Conflicts(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	type row struct {
		confluence.Conflict
		Mine bool `json:"mine"`
	}
	out := make([]row, 0, len(list))
	for _, x := range list {
		out = append(out, row{Conflict: x, Mine: x.Owner != "" && x.Owner == c.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": out})
}

func (h *handler) postConflict(w http.ResponseWriter, r *http.Request, c Caller) {
	p := h.publisher(w)
	if p == nil {
		return
	}
	if !c.Reviewer {
		fail(w, wrapf(ErrDenied, "only an Admin or a SuperAdmin decides the Confluence pages' edits"))
		return
	}
	if err := h.s.leads(); err != nil {
		fail(w, err)
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if !decode(w, r, maxBody, &in) {
		return
	}
	detail, err := p.Resolve(r.Context(), r.PathValue("id"), in.Action, confluence.Person{ID: c.ID, Name: c.label(), Email: c.Email})
	if err != nil {
		fail(w, err)
		return
	}
	h.s.audit(audit.Event{Action: ActionPublish, Who: c.who(), Target: r.PathValue("id"), Outcome: audit.OutcomeOK, From: c.From, Detail: detail})
	writeJSON(w, http.StatusOK, map[string]bool{"resolved": true})
}
