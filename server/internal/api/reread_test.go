package api

import (
	"net/http"
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
