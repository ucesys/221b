package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// A replica that does not lead checks the session of every request and
// forwards the leader's requests to it (P7-02), where the session is
// checked again. While the session store is away the leader cannot ask
// about a session it has never seen, and the replica that forwarded the
// request may hold it in memory: the leader then sent the person to sign
// in, and the forwarding replica's sign-in page, which knows them, sent
// them back to the console (the review of November 23, 2026,
// session.go:663-666). So the forwarding replica vouches for the session
// it checked, and the leader takes the vouch for a session it cannot ask
// about, and for nothing else.
//
// The vouch is the session's record as the store keeps it (no cookie's
// value: the key, a hash of the ID, says which session it is for), and
// what makes it believed is the forwarding's own proof, the replicas'
// token: the api package reads it only from a request whose token
// verifies, and drops it from every other request. A browser that
// sends one is not believed; a vouch for another cookie is not taken.

// vouchKey holds, in a request's context, the session a replica vouched
// for.
type vouchKey struct{}

// vouchRecord is a session another replica vouched for.
type vouchRecord struct {
	key  string
	sess Session
}

// VouchFor is what a replica that forwards a request tells the leader of
// the session it checked: "" for none, for no person signed in, and for a
// signed session (DB-65), which every replica checks with the key alone.
func VouchFor(sess Session) string {
	if sess.ID == "" || sess.Signed || signedShape(sess.ID) {
		return ""
	}
	b, err := encodeSession(sess, nil)
	if err != nil {
		return ""
	}
	return keyOf(sess.ID) + "." + base64.RawURLEncoding.EncodeToString(b)
}

// WithVouch puts in a request the session another replica vouched for.
// The caller has checked that a replica sent the request (the replicas'
// token); one that cannot be read is no vouch.
func WithVouch(r *http.Request, v string) *http.Request {
	key, rec, ok := strings.Cut(v, ".")
	if !ok || len(key) != 64 {
		return r
	}
	b, err := base64.RawURLEncoding.DecodeString(rec)
	if err != nil {
		return r
	}
	sess, _, _, err := decodeSession(b)
	if err != nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), vouchKey{}, vouchRecord{key: key, sess: sess}))
}

// vouchedFor is the session another replica vouched for in a request, if
// it is the one of the cookie id.
func vouchedFor(r *http.Request, id string) (Session, bool) {
	v, ok := r.Context().Value(vouchKey{}).(vouchRecord)
	if !ok || subtle.ConstantTimeCompare([]byte(v.key), []byte(keyOf(id))) != 1 {
		return Session{}, false
	}
	sess := v.sess
	sess.ID = id
	return sess, true
}

// takeVouched takes a session another replica vouched for, given the cookie's
// id, only while nothing else can be said of it: the shared store cannot
// be asked and this replica does not hold it. A session the store or this
// replica knows is decided by them, a signed one by the key, and one that
// has outlived its idle or maximum time by this replica's clock is not
// taken. Nothing of it is kept here: the next request is vouched for again.
func (s *Sessions) takeVouched(id string, sess Session) (Session, bool) {
	if signedShape(id) || sess.Signed {
		return Session{}, false
	}
	if _, _, state := s.find(id); state != unknown {
		return Session{}, false
	}
	if s.expired(&sess, s.now()) {
		return Session{}, false
	}
	return sess, true
}

// liveVouched is live for a session another replica vouched for. A
// directory person's groups are not read again here: rereadGroups works
// on the session this replica holds, and it holds none. The replica that
// vouched read them again as they aged, for this very request, or kept
// them when the directory could not answer, as rereadGroups does.
func (s *Service) liveVouched(sess Session) (Session, bool) {
	if sess.Method == string(ModeLDAP) && s.signsIn(sess.Method) {
		return sess, true
	}
	return s.live(sess)
}
