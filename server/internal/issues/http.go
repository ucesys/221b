package issues

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// The HTTP API (IS-23, docs/issues-api.yaml): versioned under
// /api/issues/v1/, JSON in and out. The mount says who a request is from.
// The token intake (IS-24) was removed in 0.12.1.

// Prefix is where the API is mounted.
const Prefix = "/api/issues/v1/"

// maxBody bounds a request: a report and its fields.
const maxBody = 64 << 10

// Handler serves the API. caller says who a request is from, false when
// nobody is signed in.
func (s *Service) Handler(caller func(*http.Request) (Caller, bool)) http.Handler {
	h := &handler{s: s, caller: caller, mux: http.NewServeMux()}
	route := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, c Caller)) {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+Prefix+path, func(w http.ResponseWriter, r *http.Request) {
			c, ok := h.caller(r)
			if !ok {
				fail(w, http.StatusUnauthorized, "sign in to report a problem")
				return
			}
			fn(w, r, c)
		})
	}
	route("GET me", h.me)
	route("GET reports", h.listReports)
	route("POST reports", h.submit)
	route("GET reports/{n}", h.getReport)
	route("PATCH reports/{n}", h.triage)
	route("POST reports/{n}/messages", h.message)
	route("POST reports/{n}/drafts", h.draft)
	route("POST reports/{n}/drafts/{id}/send", h.send)
	route("DELETE reports/{n}/drafts/{id}", h.discard)
	route("POST reports/{n}/confirm", h.confirm)
	route("POST reports/{n}/reopen", h.reopen)
	route("POST reports/{n}/unlink", h.unlink)
	route("PUT reports/{n}/watch", h.watch(true))
	route("DELETE reports/{n}/watch", h.watch(false))
	route("GET known", h.listKnown)
	route("POST known", h.saveKnown)
	route("GET known/{id}", h.getKnown)
	route("PATCH known/{id}", h.saveKnown)
	route("DELETE known/{id}", h.deleteKnown)
	route("POST known/{id}/updates", h.knownUpdate)
	route("POST known/{id}/accept", h.accept)
	route("PUT known/{id}/follow", h.follow(true))
	route("DELETE known/{id}/follow", h.follow(false))
	route("GET services", h.listServices)
	route("POST services", h.saveService)
	route("PUT services/{id}", h.saveService)
	route("DELETE services/{id}", h.deleteService)
	route("POST match", h.match)
	route("GET settings", h.getSettings)
	route("PUT settings", h.putSettings)
	route("GET updates", h.updates)
	h.mux.HandleFunc(Prefix, func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "no such path in the issue API")
	})
	return h.mux
}

type handler struct {
	s      *Service
	caller func(*http.Request) (Caller, bool)
	mux    *http.ServeMux
}

func from(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(append(b, '\n'))
}

// failErr answers an error with its status and its words, without the
// marker of its kind. A database that is away is answered first, by the
// error and in the console's own words (DB-15): a 503, as on every other
// route, where the sentence with a capital and a full stop was a 500
// (the tenth cluster test's N1).
func failErr(w http.ResponseWriter, err error) {
	if db.Refuse(w, err) {
		return
	}
	code := http.StatusInternalServerError
	msg := err.Error()
	for _, k := range []struct {
		err  error
		code int
	}{{ErrInvalid, http.StatusBadRequest}, {ErrDuplicate, http.StatusConflict}, {ErrNotFound, http.StatusNotFound}, {ErrDenied, http.StatusForbidden}, {ErrRateLimited, http.StatusTooManyRequests}, {ErrFull, http.StatusInsufficientStorage}, {kv.ErrConflict, http.StatusConflict}} {
		if errors.Is(err, k.err) {
			code = k.code
			msg = strings.TrimPrefix(msg, k.err.Error()+": ")
			break
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = http.StatusGatewayTimeout
	}
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
		if !strings.HasSuffix(msg, ".") {
			msg += "."
		}
	}
	fail(w, code, msg)
}

func answer(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fail(w, http.StatusInternalServerError, "the answer could not be written")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(append(b, '\n'))
}

// decode reads a JSON body, refusing fields it does not know.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "send it as application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return false
	}
	return true
}

func number(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(r.PathValue(name), "#"))
	if err != nil || n <= 0 {
		fail(w, http.StatusBadRequest, "a report is named by its number")
		return 0, false
	}
	return n, true
}

// meView is who the page is for and what it may do, with the settings
// the page shows.
type meView struct {
	Person   Person `json:"person"`
	Report   bool   `json:"report"`
	Operator bool   `json:"operator"`
	Admin    bool   `json:"admin"`
	Company  string `json:"company"`
	// ReportURL is the chart's issues.reportURL, /report when empty.
	ReportURL string `json:"reportURL"`
	// Clusters are the connected clusters a report may be about, by id
	// and display name only (CF-64).
	Clusters []Cluster `json:"clusters"`
}

func (h *handler) me(w http.ResponseWriter, r *http.Request, c Caller) {
	set, err := h.s.Settings(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, meView{Person: c.Person, Report: c.Report, Operator: c.Operator, Admin: c.Admin, Company: set.Company, ReportURL: h.s.ReportURL(), Clusters: h.s.Clusters()})
}

// views turns reports into what the caller sees.
func (h *handler) views(ctx context.Context, c Caller, list []Case) ([]ReportView, error) {
	st, err := h.s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	now := h.s.now()
	out := make([]ReportView, 0, len(list))
	for i := range list {
		out = append(out, st.reportView(c, &list[i], now))
	}
	return out, nil
}

func (h *handler) one(w http.ResponseWriter, r *http.Request, c Caller, rep Case, err error) {
	if err != nil {
		failErr(w, err)
		return
	}
	v, err := h.views(r.Context(), c, []Case{rep})
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, v[0])
}

func (h *handler) listReports(w http.ResponseWriter, r *http.Request, c Caller) {
	q := r.URL.Query()
	f := Filter{Mine: q.Get("scope") != "all", Status: Status(q.Get("status")), Outcome: Outcome(q.Get("outcome")), Open: q.Get("open") == "true", Service: q.Get("service"), Owner: q.Get("owner"), Known: q.Get("known"), Type: q.Get("type"), Query: q.Get("q")}
	if p := strings.TrimPrefix(strings.ToUpper(q.Get("priority")), "P"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			f.Priority = &n
		}
	}
	list, err := h.s.Reports(r.Context(), c, f)
	if err != nil {
		failErr(w, err)
		return
	}
	views, err := h.views(r.Context(), c, list)
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, map[string]any{"reports": views})
}

// receiptView is what a report's sender is told.
type receiptView struct {
	Report  ReportView `json:"report"`
	Known   *KnownView `json:"known,omitempty"`
	Removed bool       `json:"removed"`
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request, c Caller) {
	var in Submission
	if !decode(w, r, &in) {
		return
	}
	rec, err := h.s.Submit(r.Context(), c, in)
	if err != nil {
		failErr(w, err)
		return
	}
	st, err := h.s.load(r.Context(), false)
	if err != nil {
		failErr(w, err)
		return
	}
	out := receiptView{Report: st.reportView(c, &rec.Report, h.s.now()), Removed: rec.Removed}
	if rec.Known != nil {
		out.Known = st.knownView(c, rec.Known)
	}
	answer(w, http.StatusCreated, out)
}

func (h *handler) getReport(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	rep, err := h.s.Report(r.Context(), c, n)
	h.one(w, r, c, rep, err)
}

func (h *handler) triage(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	var t Triage
	if !decode(w, r, &t) {
		return
	}
	rep, err := h.s.Triage(r.Context(), c, n, t)
	h.one(w, r, c, rep, err)
}

type textInput struct {
	Text     string `json:"text"`
	Internal bool   `json:"internal,omitempty"`
}

func (h *handler) message(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	var in textInput
	if !decode(w, r, &in) {
		return
	}
	rep, err := h.s.AddMessage(r.Context(), c, n, in.Text, in.Internal)
	h.one(w, r, c, rep, err)
}

func (h *handler) draft(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	var in textInput
	if !decode(w, r, &in) {
		return
	}
	rep, err := h.s.Draft(r.Context(), c, n, in.Text, "")
	h.one(w, r, c, rep, err)
}

func (h *handler) send(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	id, ok := number(w, r, "id")
	if !ok {
		return
	}
	var in textInput
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	rep, err := h.s.Send(r.Context(), c, n, id, in.Text)
	h.one(w, r, c, rep, err)
}

func (h *handler) discard(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	id, ok := number(w, r, "id")
	if !ok {
		return
	}
	rep, err := h.s.Discard(r.Context(), c, n, id)
	h.one(w, r, c, rep, err)
}

func (h *handler) confirm(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	var in struct {
		Works bool `json:"works"`
	}
	if !decode(w, r, &in) {
		return
	}
	rep, err := h.s.Confirm(r.Context(), c, n, in.Works)
	h.one(w, r, c, rep, err)
}

func (h *handler) reopen(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason,omitempty"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	rep, err := h.s.Reopen(r.Context(), c, n, in.Reason)
	h.one(w, r, c, rep, err)
}

func (h *handler) unlink(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := number(w, r, "n")
	if !ok {
		return
	}
	rep, err := h.s.Unlink(r.Context(), c, n)
	h.one(w, r, c, rep, err)
}

func (h *handler) watch(on bool) func(w http.ResponseWriter, r *http.Request, c Caller) {
	return func(w http.ResponseWriter, r *http.Request, c Caller) {
		n, ok := number(w, r, "n")
		if !ok {
			return
		}
		rep, err := h.s.Watch(r.Context(), c, n, on)
		h.one(w, r, c, rep, err)
	}
}

func (h *handler) knownViews(ctx context.Context, c Caller, list []KnownIssue) ([]*KnownView, error) {
	st, err := h.s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]*KnownView, 0, len(list))
	for i := range list {
		out = append(out, st.knownView(c, &list[i]))
	}
	return out, nil
}

func (h *handler) knownAnswer(w http.ResponseWriter, r *http.Request, c Caller, code int, k KnownIssue, err error) {
	if err != nil {
		failErr(w, err)
		return
	}
	v, err := h.knownViews(r.Context(), c, []KnownIssue{k})
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, code, v[0])
}

func (h *handler) listKnown(w http.ResponseWriter, r *http.Request, c Caller) {
	list, err := h.s.Known(r.Context(), c, r.URL.Query().Get("all") == "true")
	if err != nil {
		failErr(w, err)
		return
	}
	views, err := h.knownViews(r.Context(), c, list)
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, map[string]any{"known": views})
}

func (h *handler) getKnown(w http.ResponseWriter, r *http.Request, c Caller) {
	k, err := h.s.KnownIssue(r.Context(), c, r.PathValue("id"))
	h.knownAnswer(w, r, c, http.StatusOK, k, err)
}

func (h *handler) saveKnown(w http.ResponseWriter, r *http.Request, c Caller) {
	var in KnownInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	k, err := h.s.SaveKnown(r.Context(), c, id, in)
	code := http.StatusOK
	if id == "" {
		code = http.StatusCreated
	}
	h.knownAnswer(w, r, c, code, k, err)
}

func (h *handler) deleteKnown(w http.ResponseWriter, r *http.Request, c Caller) {
	if err := h.s.DeleteKnown(r.Context(), c, r.PathValue("id")); err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *handler) knownUpdate(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Text   string      `json:"text"`
		Status KnownStatus `json:"status,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	k, err := h.s.AddUpdate(r.Context(), c, r.PathValue("id"), in.Text, in.Status)
	h.knownAnswer(w, r, c, http.StatusOK, k, err)
}

func (h *handler) accept(w http.ResponseWriter, r *http.Request, c Caller) {
	k, err := h.s.Accept(r.Context(), c, r.PathValue("id"))
	h.knownAnswer(w, r, c, http.StatusOK, k, err)
}

func (h *handler) follow(on bool) func(w http.ResponseWriter, r *http.Request, c Caller) {
	return func(w http.ResponseWriter, r *http.Request, c Caller) {
		k, err := h.s.Follow(r.Context(), c, r.PathValue("id"), on)
		h.knownAnswer(w, r, c, http.StatusOK, k, err)
	}
}

func (h *handler) listServices(w http.ResponseWriter, r *http.Request, c Caller) {
	list, err := h.s.Services(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	if !c.Operator {
		// Anyone may know the services by name; where they run and who
		// looks after them is the operators'.
		for i := range list {
			list[i].RunsOn, list[i].Contacts = nil, nil
		}
	}
	if list == nil {
		list = []ServiceDef{}
	}
	answer(w, http.StatusOK, map[string]any{"services": list})
}

func (h *handler) saveService(w http.ResponseWriter, r *http.Request, c Caller) {
	var in ServiceDef
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	sv, err := h.s.SaveService(r.Context(), c, id, in)
	if err != nil {
		failErr(w, err)
		return
	}
	code := http.StatusOK
	if id == "" {
		code = http.StatusCreated
	}
	answer(w, code, sv)
}

func (h *handler) deleteService(w http.ResponseWriter, r *http.Request, c Caller) {
	if err := h.s.DeleteService(r.Context(), c, r.PathValue("id")); err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *handler) match(w http.ResponseWriter, r *http.Request, c Caller) {
	if !c.Operator {
		fail(w, http.StatusForbidden, "Only operators preview matches.")
		return
	}
	var in textInput
	if !decode(w, r, &in) {
		return
	}
	m, err := h.s.MatchText(r.Context(), in.Text)
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, m)
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	if !c.Admin {
		fail(w, http.StatusForbidden, "Only a SuperAdmin sees the issue settings.")
		return
	}
	set, err := h.s.Settings(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, set)
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	var set Settings
	if !decode(w, r, &set) {
		return
	}
	out, err := h.s.SaveSettings(r.Context(), c, set)
	if err != nil {
		failErr(w, err)
		return
	}
	answer(w, http.StatusOK, out)
}

func (h *handler) updates(w http.ResponseWriter, r *http.Request, c Caller) {
	now := h.s.now()
	list, err := h.s.Updates(r.Context(), c, Since(r.URL.Query().Get("since"), now))
	if err != nil {
		failErr(w, err)
		return
	}
	if list == nil {
		list = []Notice{}
	}
	answer(w, http.StatusOK, map[string]any{"updates": list, "now": now.UTC().Format(time.RFC3339Nano)})
}
