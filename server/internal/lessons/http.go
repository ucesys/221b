package lessons

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ucesys/221b/server/internal/jobs"
)

// The HTTP API (FX-06, FX-10): /api/v1/cases/exceptions, which AIOps ›
// Mycroft lists. Reading is for those who see the cases (approvers, the
// console-wide roles and job editors); creating, removing (making
// inactive) and making active again an exception is the approvers'
// (cases.approverRoles). An exception is added by closing a case as one
// (package casework), or by hand with New exception (build 14). Every
// route runs on the leader, as every API route does (ADR-70).

// Caller is who a request is from.
type Caller struct {
	Person
	// Read says they may see the records; Change that they may create,
	// remove and make active again an exception.
	Read   bool
	Change bool
}

// Route is one route of the API.
type Route struct {
	Pattern string
	Name    string
	Handler http.HandlerFunc
}

// Routes are the API's routes. caller says who a request is from, false
// when nobody is signed in.
func (s *Service) Routes(caller func(*http.Request) (Caller, bool)) []Route {
	h := &handler{s: s, caller: caller}
	return []Route{
		{"GET /api/v1/cases/exceptions", "cases-exceptions", h.wrap(h.exceptions, false)},
		{"POST /api/v1/cases/exceptions", "cases-exception-add", h.wrap(h.addException, true)},
		{"GET /api/v1/cases/exceptions/{id}", "cases-exception", h.wrap(h.exception, false)},
		{"DELETE /api/v1/cases/exceptions/{id}", "cases-exception-remove", h.wrap(h.removeException, true)},
		{"POST /api/v1/cases/exceptions/{id}/reactivate", "cases-exception-reactivate", h.wrap(h.reactivateException, true)},
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

func failErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, ErrFull):
		code = http.StatusInsufficientStorage
	case errors.Is(err, ErrConflict):
		code = http.StatusConflict
	}
	fail(w, code, err.Error())
}

func (h *handler) wrap(f func(w http.ResponseWriter, r *http.Request, c Caller), change bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.caller(r)
		switch {
		case !ok:
			fail(w, http.StatusUnauthorized, "sign in first")
		case change && !c.Change:
			fail(w, http.StatusForbidden, "Only the people who may approve proposed changes (cases.approverRoles) create, remove or make active again an exception.")
		case !c.Read && !c.Change:
			fail(w, http.StatusForbidden, "The exceptions are for those who see Cases.")
		default:
			f(w, r, c)
		}
	}
}

// ExceptionView is an exception as the page shows it: with its state now
// ("removed" is shown as Inactive), whether it is stale (CF-13), its
// cluster's name (the id when the console does not know it), and what its
// validation rule (check) means, when the catalogue says (build 14).
type ExceptionView struct {
	Exception
	State       string `json:"state"`
	Stale       bool   `json:"stale"`
	ClusterName string `json:"clusterName,omitempty"`
	RuleMeaning string `json:"ruleMeaning,omitempty"`
}

// RuleOption is a validation rule the New exception form offers, with
// what it means.
type RuleOption struct {
	ID      string `json:"id"`
	Meaning string `json:"meaning,omitempty"`
}

// ClusterOption is a cluster a new exception may be on, by its name, with
// the validation rules seen on it.
type ClusterOption struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Rules []RuleOption `json:"rules"`
}

func (h *handler) clusters() []jobs.Cluster {
	h.s.mu.Lock()
	f := h.s.o.Clusters
	h.s.mu.Unlock()
	if f == nil {
		return nil
	}
	return f()
}

// namer gives each exception its cluster's name.
func (h *handler) namer() func(string) string {
	names := map[string]string{}
	for _, c := range h.clusters() {
		names[c.ID] = c.Name
	}
	return func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
}

func (h *handler) view(e Exception, now time.Time, name func(string) string) ExceptionView {
	days := 0
	if h.s.o.StaleDays != nil {
		days = h.s.o.StaleDays()
	}
	e.Updated = e.LastChange()
	v := ExceptionView{Exception: e, State: e.State(now), Stale: e.Stale(now, days), RuleMeaning: jobs.RuleMeaning(e.Check)}
	if e.Cluster != "" {
		v.ClusterName = name(e.Cluster)
	}
	return v
}

// options are the clusters a new exception may be on, with the rules seen
// on each, sorted.
func (h *handler) options(r *http.Request) []ClusterOption {
	h.s.mu.Lock()
	seenOf := h.s.o.Seen
	h.s.mu.Unlock()
	var seen map[string][]string
	if seenOf != nil {
		seen = seenOf(r.Context())
	}
	out := []ClusterOption{}
	for _, c := range h.clusters() {
		o := ClusterOption{ID: c.ID, Name: c.Name, Rules: []RuleOption{}}
		if o.Name == "" {
			o.Name = c.ID
		}
		ids := slices.Clone(seen[c.ID])
		slices.Sort(ids)
		for _, id := range slices.Compact(ids) {
			o.Rules = append(o.Rules, RuleOption{ID: id, Meaning: jobs.RuleMeaning(id)})
		}
		out = append(out, o)
	}
	return out
}

func (h *handler) exceptions(w http.ResponseWriter, r *http.Request, c Caller) {
	list, err := h.s.Exceptions(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	now := h.s.o.Now()
	stale := r.URL.Query().Get("stale") == "1" || r.URL.Query().Get("stale") == "true"
	name := h.namer()
	out := make([]ExceptionView, 0, len(list))
	for _, e := range list {
		if v := h.view(e, now, name); !stale || v.Stale {
			out = append(out, v)
		}
	}
	body := map[string]any{"exceptions": out, "mayChange": c.Change}
	// Who may create one gets what the New exception form offers.
	if c.Change {
		body["clusters"] = h.options(r)
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *handler) exception(w http.ResponseWriter, r *http.Request, c Caller) {
	e, err := h.s.Exception(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(e, h.s.o.Now(), h.namer()))
}

func (h *handler) removeException(w http.ResponseWriter, r *http.Request, c Caller) {
	e, err := h.s.RemoveException(r.Context(), r.PathValue("id"), c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(e, h.s.o.Now(), h.namer()))
}

func (h *handler) reactivateException(w http.ResponseWriter, r *http.Request, c Caller) {
	e, err := h.s.ReactivateException(r.Context(), r.PathValue("id"), c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(e, h.s.o.Now(), h.namer()))
}

// NewExceptionInput is what New exception sends (build 14): the cluster,
// the object (empty: the cluster as a whole), the validation rule (a
// check id, or a finding's title in words), the justification (required)
// and the last day it applies ("YYYY-MM-DD"; none is permanent).
type NewExceptionInput struct {
	Cluster  string `json:"cluster"`
	Resource string `json:"resource,omitempty"`
	Check    string `json:"check"`
	Reason   string `json:"reason"`
	Expires  string `json:"expires,omitempty"`
}

// fromInput reads New exception's input into what AddException records:
// the object written one way (CF-05), so it matches the findings' keys.
func (h *handler) fromInput(in NewExceptionInput) (NewException, error) {
	cluster := strings.TrimSpace(in.Cluster)
	if cluster == "" {
		return NewException{}, fmt.Errorf("%w: pick the cluster", ErrInvalid)
	}
	if list := h.clusters(); list != nil && !slices.ContainsFunc(list, func(c jobs.Cluster) bool { return c.ID == cluster }) {
		return NewException{}, fmt.Errorf("%w: %q is not a connected cluster", ErrInvalid, clip(cluster, 60))
	}
	o, err := jobs.NormalizeObject(in.Resource)
	if err != nil {
		return NewException{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	rule := strings.TrimSpace(in.Check)
	if rule == "" {
		return NewException{}, fmt.Errorf("%w: name the validation rule", ErrInvalid)
	}
	out := NewException{Cluster: cluster, Resource: o.String(), Reason: in.Reason, Unique: true}
	// A rule in words is a finding's title, made plain as Key makes it;
	// an id is kept as it is, its meaning (when known) the title.
	if strings.ContainsFunc(rule, unicode.IsSpace) {
		out.Title = rule
	} else {
		out.Check = rule
		out.Title = firstUpper(jobs.RuleMeaning(Key(rule, "")))
		if out.Title == "" {
			out.Title = rule
		}
	}
	if Key(out.Check, out.Title) == "title:" {
		return NewException{}, fmt.Errorf("%w: name the validation rule", ErrInvalid)
	}
	if strings.TrimSpace(in.Reason) == "" {
		return NewException{}, fmt.Errorf("%w: give the justification", ErrInvalid)
	}
	if in.Expires != "" {
		d, err := time.Parse(time.DateOnly, in.Expires)
		if err != nil {
			return NewException{}, fmt.Errorf("%w: the last day is YYYY-MM-DD", ErrInvalid)
		}
		out.Expires = d.Add(24*time.Hour - time.Second)
	}
	return out, nil
}

func firstUpper(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func (h *handler) addException(w http.ResponseWriter, r *http.Request, c Caller) {
	var in NewExceptionInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "the body is not a new exception")
		return
	}
	ne, err := h.fromInput(in)
	if err != nil {
		failErr(w, err)
		return
	}
	e, err := h.s.AddException(r.Context(), ne, c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.view(e, h.s.o.Now(), h.namer()))
}
