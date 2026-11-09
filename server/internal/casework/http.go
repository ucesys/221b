package casework

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
)

// The HTTP API (CW-10, CU-11, CF-01 to CF-19): /api/v1/cases/cases,
// every case whatever opened it. Reading is for the people who work the cases (who may
// approve proposed changes, cases.approverRoles, or triage reports,
// issues.operatorRoles), the console-wide roles and those who may edit
// jobs; changing them is for those who work them. The SLA is read by
// them all and set by a SuperAdmin. Writes are forwarded to the leader,
// where the analyses run (ADR-70).

// Prefix is where the API is served.
const Prefix = "/api/v1/cases/cases"

// SLAPath is where the SLA is served (CU-06); KnownPath the known issues
// a case may be linked to.
const (
	SLAPath   = "/api/v1/cases/sla"
	KnownPath = "/api/v1/cases/known-open"
	// SimilarPath is what New Case shows beside the form (CF-12);
	// SettingsPath Settings › Cases (CF-19).
	SimilarPath  = "/api/v1/cases/similar"
	SettingsPath = "/api/v1/cases/settings"
	// RulesPath is Cases › Mycroft › Validation rules (ADR-138).
	RulesPath = "/api/v1/cases/rules"
)

// Route is one route of the API.
type Route struct {
	Pattern string
	Name    string
	Handler http.HandlerFunc
}

// maxBody bounds a request.
const maxBody = 64 << 10

// Routes are the API's routes. caller says who a request is from, false
// when nobody is signed in.
func (s *Service) Routes(caller func(*http.Request) (Caller, bool)) []Route {
	h := &handler{s: s, caller: caller}
	type fn = func(w http.ResponseWriter, r *http.Request, c Caller)
	read := func(f fn) http.HandlerFunc { return h.wrap(f, false) }
	work := func(f fn) http.HandlerFunc { return h.wrap(f, true) }
	return []Route{
		{"GET " + Prefix, "cases", read(h.list)},
		{"POST " + Prefix, "case-create", work(h.create)},
		// --- Many cases at once (wave 6c, CF-38).
		{"POST " + Prefix + "/bulk", "case-bulk", work(h.bulk)},
		{"GET " + Prefix + "/{id}", "case", read(h.get)},
		{"PUT " + Prefix + "/{id}", "case-update", work(h.update)},
		{"POST " + Prefix + "/{id}/messages", "case-message", work(h.say)},
		{"POST " + Prefix + "/{id}/drafts/{msg}/send", "case-send", work(h.send)},
		{"DELETE " + Prefix + "/{id}/drafts/{msg}", "case-discard", work(h.discard)},
		{"PUT " + Prefix + "/{id}/draft", "case-keep", work(h.keep)},
		{"DELETE " + Prefix + "/{id}/draft", "case-keep-drop", work(h.dropDraft)},
		{"POST " + Prefix + "/{id}/join", "case-join", work(h.join)},
		{"POST " + Prefix + "/{id}/known", "case-known", work(h.known)},
		{"POST " + Prefix + "/{id}/fix", "case-fix", work(h.fix)},
		{"POST " + Prefix + "/{id}/close", "case-close", work(h.close)},
		{"GET " + Prefix + "/{id}/duplicate-check", "case-duplicate-check", read(h.duplicateCheck)},
		{"POST " + Prefix + "/{id}/exception", "case-exception", work(h.exception)},
		{"POST " + Prefix + "/{id}/reopen", "case-reopen", work(h.reopen)},
		{"POST " + Prefix + "/{id}/offer", "case-offer", work(h.offer)},
		{"POST " + Prefix + "/{id}/rca-draft", "case-rca-draft", work(h.draft)},
		{"POST " + Prefix + "/{id}/analyze", "case-analyze", work(h.analyze)},
		// --- The changes that resolve a case (wave 6b, CF-28).
		{"POST " + Prefix + "/{id}/changes", "case-change-link", work(h.linkChange)},
		{"DELETE " + Prefix + "/{id}/changes/{card}", "case-change-unlink", work(h.unlinkChange)},
		{"GET " + SLAPath, "case-sla", read(h.sla)},
		{"PUT " + SLAPath, "case-sla-save", work(h.saveSLA)},
		{"GET " + KnownPath, "case-known-open", read(h.knownOpen)},
		{"POST " + SimilarPath, "case-similar", read(h.similar)},
		{"GET " + SettingsPath, "case-settings", read(h.getSettings)},
		// --- The case on an alert (CF-77): the open cases that cover a
		// cluster's alerts.
		{"GET " + AlertsPath, "case-alert-covers", read(h.alertCovers)},
		{"PUT " + SettingsPath, "case-settings-save", read(h.putSettings)},
		// --- Validation rules (ADR-138): read by who reads the cases,
		// changed by a SuperAdmin (the service says so).
		{"GET " + RulesPath, "case-rules", read(h.rules)},
		{"POST " + RulesPath, "case-rule-add", read(h.addRule)},
		{"POST " + RulesPath + "/promote", "case-rule-promote", read(h.promoteRule)},
		{"GET " + RulesPath + "/suggestions", "case-rule-suggestions", read(h.ruleSuggestions)},
		{"DELETE " + RulesPath + "/suggestions/{id}", "case-rule-dismiss", read(h.dismissRule)},
		{"PATCH " + RulesPath + "/{id}", "case-rule-edit", read(h.editRule)},
		{"DELETE " + RulesPath + "/{id}", "case-rule-delete", read(h.deleteRule)},
		{"POST " + RulesPath + "/{id}/retire", "case-rule-retire", read(h.retireRule)},
		{"POST " + RulesPath + "/{id}/activate", "case-rule-activate", read(h.activateRule)},
		{"POST " + RulesPath + "/{id}/replace", "case-rule-replace", read(h.replaceRule)},
	}
}

type handler struct {
	s      *Service
	caller func(*http.Request) (Caller, bool)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(b)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// failErr answers an error of the service, or of what it reads; a
// duplicate refused answers 409 with its check and what fits instead, a
// draft saved over a newer one 409 with the draft as it is (CF-59), and
// an edit in place over a newer value 409 with that value (CF-60).
func failErr(w http.ResponseWriter, err error) {
	var d *DuplicateError
	if errors.As(err, &d) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Not a duplicate: " + d.Check.Reason + ".", "check": d.Check})
		return
	}
	var dc *DraftConflict
	if errors.As(err, &dc) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": upper(dc.Error()) + ".", "draft": dc.Draft})
		return
	}
	var fc *FieldConflict
	if errors.As(err, &fc) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": upper(fc.Error()) + ".", "field": fc.Field, "current": fc.Current, "by": fc.By, "at": fc.At})
		return
	}
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, jobs.ErrNotFound), errors.Is(err, issues.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrInvalid), errors.Is(err, jobs.ErrInvalid), errors.Is(err, issues.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, ErrDenied), errors.Is(err, jobs.ErrDenied), errors.Is(err, issues.ErrDenied):
		code = http.StatusForbidden
	case errors.Is(err, ErrState), errors.Is(err, ErrOff), errors.Is(err, kv.ErrConflict):
		code = http.StatusConflict
	case errors.Is(err, ErrFull), errors.Is(err, issues.ErrFull):
		code = http.StatusInsufficientStorage
	}
	fail(w, code, err.Error())
}

func (h *handler) wrap(f func(w http.ResponseWriter, r *http.Request, c Caller), change bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.caller(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "sign in first")
			return
		}
		switch {
		case change && !c.Approver:
			fail(w, http.StatusForbidden, "Only the people who work the cases (who may approve proposed changes, cases.approverRoles, or triage reports, issues.operatorRoles) open and change cases.")
			return
		case !c.Approver && !c.Read:
			fail(w, http.StatusForbidden, "Cases are for the people who work them, the console-wide roles and those who may edit jobs.")
			return
		}
		f(w, r, c)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "send JSON")
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

// listView is the cases, for the Inbox.
type listView struct {
	Cases    []Brief        `json:"cases"`
	Clusters []jobs.Cluster `json:"clusters"`
	// MayChange says the person works the cases: opens, changes,
	// completes and closes them.
	MayChange bool `json:"mayChange"`
	// Me is how the cases name the person (the issue service's id), for
	// Mine.
	Me string `json:"me"`
}

func truthy(v string) bool { return v == "1" || v == "true" }

func (h *handler) list(w http.ResponseWriter, r *http.Request, c Caller) {
	q := r.URL.Query()
	f := Filter{Status: q.Get("status"), Outcome: q.Get("outcome"), Open: truthy(q.Get("open")), Pending: truthy(q.Get("pending")) || truthy(q.Get("waiting")),
		Cluster: q.Get("cluster"), Owner: q.Get("owner"), Unassigned: truthy(q.Get("unassigned")), Type: q.Get("type"), Q: q.Get("q")}
	if p := strings.TrimPrefix(strings.ToUpper(q.Get("priority")), "P"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			f.Priority = &n
		}
	}
	switch src := q.Get("source"); src {
	case "agents":
		f.Agents = true
	case "people":
		f.People = true
	default:
		f.Source = src
	}
	if truthy(q.Get("mine")) {
		f.Owner = c.key()
	}
	list, err := h.s.List(r.Context(), c, f)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listView{Cases: list, Clusters: h.s.ClusterList(), MayChange: c.Approver, Me: c.key()})
}

// caseView is one case for the person: whether they may work it, and
// accept or remove an exception; and for those who work it, the clusters
// it may be put on, as New Case lists them (CF-60).
type caseView struct {
	View
	MayChange  bool           `json:"mayChange"`
	MayApprove bool           `json:"mayApprove"`
	Clusters   []jobs.Cluster `json:"clusters,omitempty"`
}

func (h *handler) answer(w http.ResponseWriter, code int, c Caller, v View, err error) {
	if err != nil {
		failErr(w, err)
		return
	}
	out := caseView{View: v, MayChange: c.Approver, MayApprove: c.Approve}
	if c.Approver {
		out.Clusters = h.s.ClusterList()
	}
	writeJSON(w, code, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.Get(r.Context(), c, r.PathValue("id"))
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) create(w http.ResponseWriter, r *http.Request, c Caller) {
	var in NewCase
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Create(r.Context(), in, c)
	h.answer(w, http.StatusCreated, c, v, err)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request, c Caller) {
	var in Change
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Update(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) say(w http.ResponseWriter, r *http.Request, c Caller) {
	var in Message
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Say(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func msgID(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("msg"))
	if err != nil || n <= 0 {
		fail(w, http.StatusBadRequest, "a drafted reply is named by its number")
		return 0, false
	}
	return n, true
}

func (h *handler) send(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := msgID(w, r)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	v, err := h.s.SendDraft(r.Context(), r.PathValue("id"), n, in.Text, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) discard(w http.ResponseWriter, r *http.Request, c Caller) {
	n, ok := msgID(w, r)
	if !ok {
		return
	}
	v, err := h.s.DiscardDraft(r.Context(), r.PathValue("id"), n, c)
	h.answer(w, http.StatusOK, c, v, err)
}

// keep saves the case's one draft (CF-59): its parts, the stamp it
// started from (base) and force to save over a newer one.
func (h *handler) keep(w http.ResponseWriter, r *http.Request, c Caller) {
	var in DraftInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Keep(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

// dropDraft clears the case's draft: ?base= is the stamp it started from,
// ?force=1 clears a newer one too.
func (h *handler) dropDraft(w http.ResponseWriter, r *http.Request, c Caller) {
	in := DraftInput{Force: truthy(r.URL.Query().Get("force"))}
	if b := r.URL.Query().Get("base"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			fail(w, http.StatusBadRequest, "base is the draft's updated stamp, as the case gave it")
			return
		}
		in.Base = t
	}
	v, err := h.s.Keep(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) known(w http.ResponseWriter, r *http.Request, c Caller) {
	var in KnownLink
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Known(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) join(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Join(r.Context(), r.PathValue("id"), in.Text, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) fix(w http.ResponseWriter, r *http.Request, c Caller) {
	var in FixInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Fix(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) close(w http.ResponseWriter, r *http.Request, c Caller) {
	var in CloseInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Close(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) duplicateCheck(w http.ResponseWriter, r *http.Request, c Caller) {
	chk, err := h.s.DuplicateCheck(r.Context(), r.PathValue("id"), r.URL.Query().Get("original"), c)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, chk)
}

func (h *handler) exception(w http.ResponseWriter, r *http.Request, c Caller) {
	var in ExceptionInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Exception(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) reopen(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Reopen(r.Context(), r.PathValue("id"), in.Reason, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) offer(w http.ResponseWriter, r *http.Request, c Caller) {
	var in issues.Offer
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Offer(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}

func (h *handler) similar(w http.ResponseWriter, r *http.Request, c Caller) {
	var in SimilarInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.s.SimilarTo(r.Context(), in)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	set, err := h.s.Settings(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	var in Settings
	if !decode(w, r, &in) {
		return
	}
	set, err := h.s.SaveSettings(r.Context(), in, c)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (h *handler) draft(w http.ResponseWriter, r *http.Request, c Caller) {
	d, err := h.s.DraftRCA(r.Context(), r.PathValue("id"), c)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handler) analyze(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.Analyze(r.Context(), r.PathValue("id"), c)
	h.answer(w, http.StatusAccepted, c, v, err)
}

// bulk applies one action to many cases (CF-38): 200 with each case's
// result, 400 for none or more than MaxBulk.
func (h *handler) bulk(w http.ResponseWriter, r *http.Request, c Caller) {
	var in BulkInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.s.Bulk(r.Context(), in, c)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// linkChange links a card to the case: {"card": "PC-12"} (CF-28); the
// c12 of before names the same card (CF-56).
func (h *handler) linkChange(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Card string `json:"card"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Card) == "" {
		fail(w, http.StatusBadRequest, "name the change to link: {\"card\": \"PC-12\"}")
		return
	}
	v, err := h.s.LinkChange(r.Context(), r.PathValue("id"), in.Card, c)
	h.answer(w, http.StatusOK, c, v, err)
}

// unlinkChange takes a card a person linked off the case (CF-28).
func (h *handler) unlinkChange(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.UnlinkChange(r.Context(), r.PathValue("id"), r.PathValue("card"), c)
	h.answer(w, http.StatusOK, c, v, err)
}

// slaView is the SLA, with whether the person may set it.
type slaView struct {
	Targets map[string]issues.Target `json:"targets"`
	MaySet  bool                     `json:"maySet"`
}

func (h *handler) sla(w http.ResponseWriter, r *http.Request, c Caller) {
	t, err := h.s.SLA(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, slaView{Targets: t, MaySet: c.Admin})
}

func (h *handler) saveSLA(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Targets map[string]issues.Target `json:"targets"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := h.s.SaveSLA(r.Context(), in.Targets, c)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, slaView{Targets: t, MaySet: c.Admin})
}

// knownBrief is a known issue a case may be linked to.
type knownBrief struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Status issues.KnownStatus `json:"status"`
}

func (h *handler) knownOpen(w http.ResponseWriter, r *http.Request, c Caller) {
	list, err := h.s.KnownOpen(r.Context(), c)
	if err != nil {
		failErr(w, err)
		return
	}
	out := make([]knownBrief, 0, len(list))
	for _, k := range list {
		out = append(out, knownBrief{ID: k.ID, Title: k.Title, Status: k.Status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"known": out})
}
