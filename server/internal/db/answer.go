package db

import (
	"encoding/json"
	"errors"
	"net/http"
)

// How an HTTP answer says that the database is unavailable (DB-15), apart
// from its words: a header, set by whoever answers and has the error. The
// console's API turns an answer that carries it into a 503 whatever
// status it was given, and the browser app goes by it to tell "the
// database is away" from every other refusal (a part that is off also
// answers 503). The words are for the person; nothing should be decided
// by reading them.
const (
	UnavailableHeader = "X-221b-Database"
	UnavailableValue  = "unavailable"
)

// IsUnavailable says whether err is, or wraps, ErrUnavailable.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

// MarkUnavailable marks an answer as saying the database is unavailable.
func MarkUnavailable(h http.Header) { h.Set(UnavailableHeader, UnavailableValue) }

// MarkedUnavailable says whether an answer was marked so.
func MarkedUnavailable(h http.Header) bool { return h.Get(UnavailableHeader) == UnavailableValue }

// Refuse answers, for err, that the database is unavailable, when that is
// what err says, and reports whether it did: a 503, marked, with err's
// own words as the JSON error (what was being done, then the console's
// sentence, and nothing of the driver's: see unavailableError). An error
// writer of a service calls it first, with the error it was given, so
// that no rewording of the text on the way out can turn the answer into
// a 500 (the tenth cluster test's N1).
func Refuse(w http.ResponseWriter, err error) bool {
	if !IsUnavailable(err) {
		return false
	}
	MarkUnavailable(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	w.Write(append(b, '\n'))
	return true
}
