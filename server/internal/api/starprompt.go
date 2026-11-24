package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/settings"
)

// --- Star us on GitHub (0.14.0, CU-21; the owner's decisions) ---
//
// After the installation, a dialog asks the people who installed the
// console to star 221B on GitHub, with a link to the repository, Close and
// Never show this again. Who installed it the console knows as the
// SuperAdmins the Helm values name (auth.admins and auth.adminGroups, the
// groups given superAdmin under auth.ldap.roles or auth.oidc.roles, and
// the local users of auth.local.users with the role superAdmin) and, in
// local mode, the SuperAdmin the first-run page created; never a
// SuperAdmin made later in the console (Options.Installer). It is a
// console-wide setting of the general settings (settings.StarPrompt), on
// by default, a switch in Administration › Settings › General; Never show
// this again turns it off, audited as any change of General is. Closed
// without it, the dialog shows again at the next sign-in: the page keeps
// that it was closed under a key of the session, which the server gives
// as a hash of the session's ID. The console never calls GitHub and keeps
// nothing about starring.

// Installer says whether a signed-in person installed the console: a
// SuperAdmin the Helm values name or the first-run page created (CU-21).
type Installer func(auth.Session) bool

// starSetter turns the dialog on or off; *settings.Service is one.
type starSetter interface {
	UpdateStarPrompt(ctx context.Context, on bool) (settings.State, error)
}

// starPrompt is what the page is told: whether to ask, and the key of the
// session under which it keeps that the dialog was closed.
type starPrompt struct {
	Show    bool   `json:"show"`
	Session string `json:"session,omitempty"`
}

// starSession is the key of a session for the dialog: a hash of its ID,
// never the ID.
func starSession(sess auth.Session) string {
	h := sha256.Sum256([]byte("221b-star-prompt\x00" + sess.StateID()))
	return hex.EncodeToString(h[:8])
}

// asksForStar says whether the dialog asks this person: sign-in on, a
// SuperAdmin the values name, the setting on.
func (s *Server) asksForStar(r *http.Request) (auth.Session, bool) {
	sess, ok := auth.SessionFrom(r.Context())
	if s.opts.Auth == nil || !ok || !sess.Admin || s.opts.Installer == nil || !s.opts.Installer(sess) {
		return sess, false
	}
	return sess, s.opts.Settings.Current().AsksForStar()
}

func (s *Server) getStarPrompt(w http.ResponseWriter, r *http.Request) {
	sess, show := s.asksForStar(r)
	out := starPrompt{Show: show}
	if show {
		out.Session = starSession(sess)
	}
	writeValue(w, r, out)
}

// postStarNever is the dialog's Never show this again: the setting off,
// for everyone, audited as a change of General. Only the people it asks
// send it; a SuperAdmin turns it on again in General.
func (s *Server) postStarNever(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.SessionFrom(r.Context())
	if s.opts.Auth == nil || !ok || !sess.Admin || s.opts.Installer == nil || !s.opts.Installer(sess) {
		jsonError(w, http.StatusForbidden, "Only the SuperAdmins who installed the console are asked to star 221B.")
		return
	}
	set, ok := s.opts.Settings.(starSetter)
	if !ok {
		jsonError(w, http.StatusNotImplemented, "This console cannot change whether it asks.")
		return
	}
	if s.opts.Settings.Current().AsksForStar() {
		if _, err := set.UpdateStarPrompt(r.Context(), false); err != nil {
			s.opts.Logger.Warn("the star prompt's setting not saved", "err", err)
			jsonError(w, settingsStatus(err, http.StatusInternalServerError), "The setting was not saved: "+err.Error())
			return
		}
		s.writeAudit(r, "", audit.OutcomeOK, audit.ActionSettings, "settings", "asking to star 221B on GitHub turned off: Never show this again")
	}
	writeValue(w, r, starPrompt{})
}

// --- end of Star us on GitHub.
