package fixes

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// --- AIOps › Mycroft › Fixes (CF-51) ---
//
// The fixes under /api/v1/cases/fixes: those who see the cases read
// them; those who work the cases edit, accept, turn off and delete them.

// Caller is who asks.
type Caller struct {
	Person
	Read   bool
	Change bool
}

// Route is one of the service's routes.
type Route struct {
	Pattern string
	Name    string
	Handler http.HandlerFunc
}

const maxBody = 64 << 10

// Routes are the service's routes.
func (s *Service) Routes(caller func(*http.Request) (Caller, bool)) []Route {
	h := &handler{s: s, caller: caller}
	return []Route{
		{"GET /api/v1/cases/fixes", "cases-fixes", h.wrap(h.list, false)},
		{"GET /api/v1/cases/fixes/{id}", "cases-fix", h.wrap(h.get, false)},
		{"PATCH /api/v1/cases/fixes/{id}", "cases-fix-edit", h.wrap(h.edit, true)},
		{"DELETE /api/v1/cases/fixes/{id}", "cases-fix-delete", h.wrap(h.remove, true)},
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
			fail(w, http.StatusForbidden, "Only the people who work the cases change the fixes.")
		case !c.Read && !c.Change:
			fail(w, http.StatusForbidden, "The fixes are for the people who work the cases, the console-wide roles and those who may edit jobs.")
		default:
			f(w, r, c)
		}
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, c Caller) {
	list, err := h.s.List(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	if list == nil {
		list = []Fix{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"fixes": list, "mayChange": c.Change})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, c Caller) {
	f, err := h.s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *handler) edit(w http.ResponseWriter, r *http.Request, c Caller) {
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "send JSON")
		return
	}
	var in Edit
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		return
	}
	f, err := h.s.Update(r.Context(), r.PathValue("id"), in, c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request, c Caller) {
	f, err := h.s.Delete(r.Context(), r.PathValue("id"), c.Person)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}
