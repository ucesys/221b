package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/watson"
)

type rereadAuth struct {
	sess          auth.Session
	current, read bool
}

func (rereadAuth) Gate(http.ResponseWriter, *http.Request) *http.Request { return nil }
func (rereadAuth) Routes() []auth.Route                                  { return nil }
func (a rereadAuth) Current(auth.Session) (auth.Session, bool)           { return a.sess, a.current }
func (a rereadAuth) Reread(s auth.Session) (auth.Session, bool)          { return s, a.read }

// A standing approval's holder is read again as the sign-in sees them: a
// session that ended, or a person the directory took out, no longer stands.
func TestAStandingApprovalsHolderIsReadAgain(t *testing.T) {
	holder := watson.Who{Session: "s1", User: "pat", From: "10.0.0.1"}
	sess := auth.Session{ID: "s1", Admin: true}
	sess.User.Username = "pat"
	for _, c := range []struct {
		current, read, want bool
	}{{true, true, true}, {false, true, false}, {true, false, false}} {
		s := &Server{opts: Options{Auth: rereadAuth{sess, c.current, c.read}}}
		who, ok := s.rereadWho(holder)
		if ok != c.want {
			t.Fatalf("%+v: stands %v", c, ok)
		}
		if ok && (!who.Admin || !who.ManageApps || who.From != "10.0.0.1" || who.Session != "s1") {
			t.Fatalf("read again as %+v", who)
		}
	}
}

// Approving in the Inbox reads the person's groups again, as Watson's
// approve does: a person the directory took out is refused (review of
// November 24, 2026, pre_existing: api/cards.go:292).
func TestApprovingInTheInboxReadsTheSessionAgain(t *testing.T) {
	sess := auth.Session{ID: "s1", Admin: true}
	s := &Server{opts: Options{Auth: rereadAuth{sess, true, false}}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/aiops/proposals/PC-1/approve", nil)
	r = r.WithContext(auth.WithSession(r.Context(), sess))
	w := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }()
		s.postProposalApprove(w, r)
	}()
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
