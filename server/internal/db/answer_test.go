package db_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
)

// An error that says the database is unavailable is answered 503, marked,
// in its own words; any other error is left to whoever asked.
func TestRefuseAnswersADatabaseThatIsAwayByTheError(t *testing.T) {
	for _, err := range []error{db.ErrUnavailable, fmt.Errorf("reading the settings: %w", db.ErrUnavailable), fmt.Errorf("invalid: %w", fmt.Errorf("the cases: %w", db.ErrUnavailable))} {
		rec := httptest.NewRecorder()
		if !db.Refuse(rec, err) {
			t.Fatalf("%v was not refused", err)
		}
		want := `{"error":"` + err.Error() + `"}` + "\n"
		if rec.Code != http.StatusServiceUnavailable || !db.MarkedUnavailable(rec.Header()) || rec.Body.String() != want || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%v: %d, marked %v, %q", err, rec.Code, db.MarkedUnavailable(rec.Header()), rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	// The same words in another error are not the database's.
	if db.Refuse(rec, errors.New(db.ErrUnavailable.Error())) || db.Refuse(rec, nil) || db.Refuse(rec, errors.New("no such report")) {
		t.Error("an error that is not the database's was refused as one")
	}
	if db.MarkedUnavailable(rec.Header()) || rec.Body.Len() != 0 {
		t.Errorf("an answer left to its handler was written to: %q", rec.Body.String())
	}
}
