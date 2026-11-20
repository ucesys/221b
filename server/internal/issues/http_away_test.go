package issues

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
)

// The issue service's error writer makes a sentence of an error's words
// (a capital, a full stop). With the database away it answers by the
// error, before any of that: a 503, marked, with the console's sentence
// as it is (DB-15; the tenth cluster test's N1, where /me and /settings
// answered 500 with "The database is unavailable; … still work.").
func TestFailErrAnswers503ForADatabaseThatIsAway(t *testing.T) {
	for _, err := range []error{
		db.ErrUnavailable,
		fmt.Errorf("reading the issue settings: %w", db.ErrUnavailable),
		// Under one of the service's own kinds, which has a status too.
		fmt.Errorf("%w: %w", ErrInvalid, db.ErrUnavailable),
		fmt.Errorf("%w: the report: %w", ErrNotFound, db.ErrUnavailable),
	} {
		rec := httptest.NewRecorder()
		failErr(rec, err)
		want := `{"error":"` + err.Error() + `"}` + "\n"
		if rec.Code != http.StatusServiceUnavailable || !db.MarkedUnavailable(rec.Header()) || rec.Body.String() != want {
			t.Errorf("%v: %d, marked %v, %q", err, rec.Code, db.MarkedUnavailable(rec.Header()), rec.Body.String())
		}
	}
	// Every other error as before: its status, and a sentence.
	rec := httptest.NewRecorder()
	failErr(rec, fmt.Errorf("%w: there is no report 7", ErrNotFound))
	if rec.Code != http.StatusNotFound || rec.Body.String() != `{"error":"There is no report 7."}`+"\n" || db.MarkedUnavailable(rec.Header()) {
		t.Errorf("a report that is not there: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	failErr(rec, errors.New("the store answered nothing"))
	if rec.Code != http.StatusInternalServerError || db.MarkedUnavailable(rec.Header()) {
		t.Errorf("another failure: %d %q", rec.Code, rec.Body.String())
	}
}
