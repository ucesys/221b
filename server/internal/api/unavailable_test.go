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
func TestAnAnswerThatSaysTheDatabaseIsUnavailableIs503(t *testing.T) {
	away := fmt.Errorf("reading the credentials: %w: connection refused", db.ErrUnavailable).Error()
	for _, c := range []struct {
		name   string
		handle func(w http.ResponseWriter)
		want   int
		body   string
	}{
		{"a 500 that says so", func(w http.ResponseWriter) { jsonError(w, http.StatusInternalServerError, away) }, 503, away},
		{"a 502 that says so", func(w http.ResponseWriter) { jsonError(w, http.StatusBadGateway, away) }, 503, away},
		{"a 400 that says so", func(w http.ResponseWriter) { http.Error(w, away, http.StatusBadRequest) }, 503, away},
		{"a 503 that says so", func(w http.ResponseWriter) { jsonError(w, http.StatusServiceUnavailable, away) }, 503, away},
		{"another 500", func(w http.ResponseWriter) {
			jsonError(w, http.StatusInternalServerError, "the chart could not be rendered")
		}, 500, "rendered"},
		{"a 404", func(w http.ResponseWriter) { jsonError(w, http.StatusNotFound, "no such credential") }, 404, "no such"},
		{"a 204", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }, 204, ""},
		{"a 200 that names the sentence", func(w http.ResponseWriter) { _, _ = w.Write([]byte(away)) }, 200, away},
		{"a 409 with no text", func(w http.ResponseWriter) { w.WriteHeader(http.StatusConflict) }, 409, ""},
		{"a header given twice", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
		}, 500, "x"},
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
	}
	// What the leader answered is passed on as it is.
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, asIs: true}
	sw.WriteHeader(http.StatusBadGateway)
	if rec.Code != http.StatusBadGateway || sw.held {
		t.Errorf("a forwarded answer was held: %d", rec.Code)
	}
}
