package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
)

// Whatever status a handler gives a failure, one whose text says the
// database is unavailable is answered 503 (DB-15); every other answer is
// sent as the handler gave it, and an error with no text is still sent.
// An answer marked as the database's (db.MarkUnavailable) is a 503
// whatever its words are, and every 503 of the database's leaves marked,
// which is what the browser app goes by; a 503 for another reason (a part
// that is off) does not.
func TestAnAnswerThatSaysTheDatabaseIsUnavailableIs503(t *testing.T) {
	away := fmt.Errorf("reading the credentials: %w: connection refused", db.ErrUnavailable).Error()
	for _, c := range []struct {
		name   string
		handle func(w http.ResponseWriter)
		want   int
		body   string
		marked bool
	}{
		{"a 500 that says so", func(w http.ResponseWriter) { jsonError(w, http.StatusInternalServerError, away) }, 503, away, true},
		{"a 502 that says so", func(w http.ResponseWriter) { jsonError(w, http.StatusBadGateway, away) }, 503, away, true},
		{"a 400 that says so", func(w http.ResponseWriter) { http.Error(w, away, http.StatusBadRequest) }, 503, away, true},
		{"a 503 that says so", func(w http.ResponseWriter) { jsonError(w, http.StatusServiceUnavailable, away) }, 503, away, true},
		{"a 500 marked, its sentence worded apart", func(w http.ResponseWriter) {
			db.MarkUnavailable(w.Header())
			jsonError(w, http.StatusInternalServerError, "The database is unavailable; sign-in and the pages read live from the clusters still work.")
		}, 503, "The database is unavailable", true},
		{"a 400 marked, with no words of the database", func(w http.ResponseWriter) {
			db.MarkUnavailable(w.Header())
			jsonError(w, http.StatusBadRequest, "the report could not be read")
		}, 503, "could not be read", true},
		{"what an error writer that has the error answers", func(w http.ResponseWriter) {
			db.Refuse(w, fmt.Errorf("reading the issue settings: %w", db.ErrUnavailable))
		}, 503, "reading the issue settings: " + db.ErrUnavailable.Error(), true},
		{"a 503 of a part that is off", func(w http.ResponseWriter) {
			jsonError(w, http.StatusServiceUnavailable, "Cases are off on this console.")
		}, 503, "Cases are off", false},
		{"a 503 with no text", func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }, 503, "", false},
		{"another 500", func(w http.ResponseWriter) {
			jsonError(w, http.StatusInternalServerError, "the chart could not be rendered")
		}, 500, "rendered", false},
		{"a 404", func(w http.ResponseWriter) { jsonError(w, http.StatusNotFound, "no such credential") }, 404, "no such", false},
		{"a 204", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }, 204, "", false},
		{"a 200 that names the sentence", func(w http.ResponseWriter) { _, _ = w.Write([]byte(away)) }, 200, away, false},
		{"a 409 with no text", func(w http.ResponseWriter) { w.WriteHeader(http.StatusConflict) }, 409, "", false},
		{"a header given twice", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
		}, 500, "x", false},
	} {
		rec := httptest.NewRecorder()
		sw := &statusWriter{ResponseWriter: rec}
		c.handle(sw)
		sw.sendHeld()
		if rec.Code != c.want || sw.code != c.want {
			t.Errorf("%s: answered %d (noted %d), want %d", c.name, rec.Code, sw.code, c.want)
		}
		if c.body != "" && !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: the answer reads %q", c.name, rec.Body.String())
		}
		if db.MarkedUnavailable(rec.Header()) != c.marked {
			t.Errorf("%s: marked as the database's: %v, want %v", c.name, !c.marked, c.marked)
		}
	}
	// What the leader answered is passed on as it is.
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, asIs: true}
	sw.WriteHeader(http.StatusBadGateway)
	if rec.Code != http.StatusBadGateway || sw.held {
		t.Errorf("a forwarded answer was held: %d", rec.Code)
	}
}
