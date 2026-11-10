package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/schedule"
)

// The HTTP API (JB-15 to JB-18): /api/v1/jobs and /api/v1/runs; a run's
// findings are on the run, each routed into a case (CF-01). Reading is for those who may see what jobs find; a
// change is for those who may edit jobs (a SuperAdmin, and the roles the
// settings name); the settings are a SuperAdmin's. Every route runs on the
// leader, where the jobs run (ADR-70).

// Caller is who a request is from.
type Caller struct {
	Person
	// Admin is a SuperAdmin; Read says they may see the jobs, runs and
	// findings; Roles are the roles they hold, for who may edit.
	Admin bool
	Read  bool
	Roles []string
	// Approve says they hold the approval permission (cases.approverRoles):
	// they see the runs and their findings.
	Approve bool
}

// Route is one route of the API.
type Route struct {
	Pattern string
	Name    string
	Handler http.HandlerFunc
}

// maxBody bounds a request.
const maxBody = 32 << 10

// Routes are the API's routes. caller says who a request is from, false
// when nobody is signed in.
func (s *Service) Routes(caller func(*http.Request) (Caller, bool)) []Route {
	h := &handler{s: s, caller: caller}
	type fn = func(w http.ResponseWriter, r *http.Request, c Caller)
	read := func(f fn) http.HandlerFunc { return h.wrap(f, false) }
	edit := func(f fn) http.HandlerFunc { return h.wrap(f, true) }
	return []Route{
		{"GET /api/v1/jobs", "jobs", read(h.list)},
		{"POST /api/v1/jobs", "job-add", edit(h.add)},
		{"GET /api/v1/jobs/settings", "jobs-settings", read(h.getSettings)},
		{"PUT /api/v1/jobs/settings", "jobs-settings-update", read(h.putSettings)},
		{"POST /api/v1/jobs/schedule", "jobs-schedule", read(h.preview)},
		{"GET /api/v1/jobs/{id}", "job", read(h.get)},
		{"PUT /api/v1/jobs/{id}", "job-update", edit(h.update)},
		{"DELETE /api/v1/jobs/{id}", "job-remove", edit(h.remove)},
		{"POST /api/v1/jobs/{id}/run", "job-run", edit(h.runNow)},
		{"POST /api/v1/jobs/{id}/rebuild", "job-rebuild", edit(h.rebuildNow)},
		{"GET /api/v1/jobs/{id}/runs", "job-runs", read(h.jobRuns)},
		{"GET /api/v1/runs", "runs", read(h.runs)},
		{"GET /api/v1/runs/{id}", "run", read(h.run)},
		{"POST /api/v1/runs/{id}/cancel", "run-cancel", edit(h.cancel)},
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

// failErr answers an error of the service.
func failErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, ErrDenied):
		code = http.StatusForbidden
	case errors.Is(err, ErrRunning):
		code = http.StatusConflict
	case errors.Is(err, ErrBudget):
		code = http.StatusTooManyRequests
	}
	fail(w, code, err.Error())
}

// mayEdit says whether the caller may change jobs.
func (h *handler) mayEdit(ctx context.Context, c Caller) bool {
	if c.Admin {
		return true
	}
	set, err := h.s.Settings(ctx)
	return err == nil && set.MayEdit(false, c.Roles)
}

func (h *handler) wrap(f func(w http.ResponseWriter, r *http.Request, c Caller), change bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.caller(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "sign in first")
			return
		}
		may := h.mayEdit(r.Context(), c)
		switch {
		case change && !may:
			fail(w, http.StatusForbidden, "Only a SuperAdmin, or a role Settings › Jobs names, may change jobs.")
			return
		case !c.Read && !may && !c.Approve:
			fail(w, http.StatusForbidden, "Jobs, runs and findings are for those who may see AIOps › Jobs.")
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

// KindView is a kind as the list offers it for Add job.
type KindView struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Agent       string   `json:"agent,omitempty"`
	AgentName   string   `json:"agentName,omitempty"`
	Does        string   `json:"does,omitempty"`
	Events      []string `json:"events,omitempty"`
	Schedulable bool     `json:"schedulable"`
	External    bool     `json:"external,omitempty"`
	// Task says the kind sends instructions a job may change (JR-06);
	// AddMore that another job of it may be added (JR-07).
	Task    bool `json:"task,omitempty"`
	AddMore bool `json:"addMore,omitempty"`
}

// listView is AIOps › Jobs.
type listView struct {
	Jobs     []View     `json:"jobs"`
	Kinds    []KindView `json:"kinds"`
	Clusters []Cluster  `json:"clusters"`
	Settings Settings   `json:"settings"`
	MayEdit  bool       `json:"mayEdit"`
	Admin    bool       `json:"admin"`
	TimeZone string     `json:"timeZone"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, c Caller) {
	jobs, err := h.s.List(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	set, _ := h.s.Settings(r.Context())
	v := listView{Jobs: jobs, Clusters: h.s.clusters(), Settings: set, MayEdit: h.mayEdit(r.Context(), c), Admin: c.Admin, TimeZone: h.s.o.TimeZone}
	if v.Jobs == nil {
		v.Jobs = []View{}
	}
	for _, k := range h.s.o.Kinds.Kinds() {
		v.Kinds = append(v.Kinds, KindView{ID: k.ID, Label: k.Label, Agent: k.Agent, AgentName: k.AgentName, Does: k.Does, Events: k.Events, Schedulable: k.Schedulable, External: k.External != nil, Task: k.Task != "", AddMore: k.AddMore()})
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, c Caller) {
	v, err := h.s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) add(w http.ResponseWriter, r *http.Request, c Caller) {
	var in struct {
		Kind string `json:"kind"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Add(r.Context(), in.Kind, c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request, c Caller) {
	var in Change
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Update(r.Context(), r.PathValue("id"), in, c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request, c Caller) {
	if err := h.s.Remove(r.Context(), r.PathValue("id"), c.Person); err != nil {
		failErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) runNow(w http.ResponseWriter, r *http.Request, c Caller) {
	b, err := h.s.RunNow(r.Context(), r.PathValue("id"), c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, b)
}

func (h *handler) rebuildNow(w http.ResponseWriter, r *http.Request, c Caller) {
	b, err := h.s.RebuildNow(r.Context(), r.PathValue("id"), c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, b)
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request, c Caller) {
	var sp schedule.Spec
	if !decode(w, r, &sp) {
		return
	}
	res := map[string]any{}
	if desc, next, err := h.s.Preview(sp); err != nil {
		res["error"] = err.Error()
	} else {
		res["description"], res["next"] = desc, next
	}
	writeJSON(w, http.StatusOK, res)
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
	if !c.Admin {
		fail(w, http.StatusForbidden, "Only a SuperAdmin sets who may edit jobs and what agents on their own read with.")
		return
	}
	var in Settings
	if !decode(w, r, &in) {
		return
	}
	set, err := h.s.SaveSettings(r.Context(), in, c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// runsView is a page of runs.
type runsView struct {
	Runs  []RunBrief `json:"runs"`
	Total int        `json:"total"`
}

func (h *handler) listRuns(w http.ResponseWriter, r *http.Request, c Caller, job string) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := RunFilter{Job: job, Status: q.Get("status"), Ref: q.Get("ref"), Limit: limit}
	if q.Get("mine") == "1" || q.Get("mine") == "true" {
		f.SetBy = c.Label()
	}
	runs, total, err := h.s.Runs(r.Context(), f)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runsView{Runs: runs, Total: total})
}

func (h *handler) jobRuns(w http.ResponseWriter, r *http.Request, c Caller) {
	h.listRuns(w, r, c, r.PathValue("id"))
}

func (h *handler) runs(w http.ResponseWriter, r *http.Request, c Caller) {
	h.listRuns(w, r, c, r.URL.Query().Get("job"))
}

// runView is a run with what the caller may do with it: cancel it while
// it runs.
type runView struct {
	Run
	MayCancel bool `json:"mayCancel"`
	// Health and Severities are the run's health (JR-01).
	Health     string `json:"health,omitempty"`
	Severities Counts `json:"severities"`
	// RuleMeanings are what the validation rules (checks) of its findings
	// and the ones new on the cluster mean, by id, when the catalogue
	// says (build 14).
	RuleMeanings map[string]string `json:"ruleMeanings,omitempty"`
}

// ruleMeanings are what a run's validation rules mean, by id.
func ruleMeanings(run Run) map[string]string {
	out := map[string]string{}
	add := func(id string) {
		if m := RuleMeaning(id); m != "" {
			out[id] = m
		}
	}
	for _, f := range run.Findings {
		add(f.Check)
		if f.Key != nil {
			add(f.Key.Check)
		}
	}
	for _, id := range run.NewChecks {
		add(id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (h *handler) run(w http.ResponseWriter, r *http.Request, c Caller) {
	run, err := h.s.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	// What its proposals resolve, in words (CF-39).
	if t, ok := h.s.o.Router.(Titler); ok {
		var ids []string
		for _, p := range run.Proposals {
			ids = append(ids, p.Resolves...)
		}
		if len(ids) > 0 {
			titles := t.Titles(r.Context(), ids)
			run.Proposals = append([]ProposalRef(nil), run.Proposals...)
			for i, p := range run.Proposals {
				for _, id := range p.Resolves {
					if title, ok := titles[id]; ok {
						if run.Proposals[i].ResolvesTitles == nil {
							run.Proposals[i].ResolvesTitles = map[string]string{}
						}
						run.Proposals[i].ResolvesTitles[id] = title
					}
				}
			}
		}
	}
	v := runView{Run: run, MayCancel: h.mayEdit(r.Context(), c), RuleMeanings: ruleMeanings(run)}
	v.Health, v.Severities = run.Health()
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request, c Caller) {
	b, err := h.s.Cancel(r.Context(), r.PathValue("id"), c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}
