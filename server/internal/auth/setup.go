package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/roles"
)

// firstRun says whether the first-run page is what to show: local accounts,
// and none exists yet (RB-26). With LDAP there is no first-run page; the
// listed groups are the first administrators (RB-28).
func (s *Service) firstRun() bool {
	return s.cfg.Mode == ModeLocal && s.accounts != nil && s.accounts.FirstRun()
}

type setupData struct {
	Title string
	Error string
	// Field is the field the error is about, shown beside it; with none,
	// the error is shown above the form.
	Field string
	Form  string
	// NeedToken asks for the setup token: only when the deployment set one.
	NeedToken bool
	Username  string
	Name      string
	// Password and Again are given back when the error is not about them,
	// so a wrong token or name does not cost typing them again.
	Password string
	Again    string
}

// Fields of the first-run form an error can be about.
const (
	fieldToken    = "token"
	fieldUsername = "username"
	fieldPassword = "password"
	fieldAgain    = "again"
)

func (s *Service) renderSetup(w http.ResponseWriter, r *http.Request, msg string, code int) {
	s.renderSetupField(w, r, msg, "", code)
}

func (s *Service) renderSetupField(w http.ResponseWriter, r *http.Request, msg, field string, code int) {
	d := setupData{Title: s.cfg.Title, Error: msg, Field: field, Form: randomID(), NeedToken: s.cfg.SetupToken != ""}
	if r.Method == http.MethodPost {
		d.Username, d.Name = r.PostFormValue("username"), r.PostFormValue("name")
		switch field {
		case fieldPassword:
		case fieldAgain:
			d.Password = r.PostFormValue("password")
		default:
			d.Password, d.Again = r.PostFormValue("password"), r.PostFormValue("again")
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: d.Form, Path: "/setup", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := s.setupPage.Execute(w, d); err != nil {
		s.log.Warn("rendering the first-run page", "err", err)
	}
}

func (s *Service) getSetup(w http.ResponseWriter, r *http.Request) {
	if !s.firstRun() {
		http.Redirect(w, r, "/signin", http.StatusFound)
		return
	}
	s.renderSetup(w, r, "", http.StatusOK)
}

// postSetup creates the first administrator (RB-26, RB-27).
func (s *Service) postSetup(w http.ResponseWriter, r *http.Request) {
	if !s.firstRun() {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderSetup(w, r, "The form could not be read.", http.StatusBadRequest)
		return
	}
	c, err := r.Cookie(cookieForm)
	if !sameSiteHeaders(r, true) || err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostFormValue("form"))) != 1 {
		s.renderSetup(w, r, "That request did not come from this console. Try again from this page.", http.StatusForbidden)
		return
	}
	who := User{Username: strings.TrimSpace(r.PostFormValue("username"))}
	if !s.setup.allow(from(r)) {
		s.record(r, audit.ActionSetupFailed, who, audit.OutcomeDenied, "too many attempts")
		s.renderSetup(w, r, "Too many attempts. Wait a minute and try again.", http.StatusTooManyRequests)
		return
	}
	if tok := s.cfg.SetupToken; tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(r.PostFormValue("token"))) != 1 {
		s.record(r, audit.ActionSetupFailed, who, audit.OutcomeDenied, "the setup token was wrong or missing")
		s.renderSetupField(w, r, "The setup token is wrong. Ask whoever installed the console: it is auth.setupToken in the deployment’s values.", fieldToken, http.StatusUnauthorized)
		return
	}
	password := r.PostFormValue("password")
	if !roles.ValidUsername(who.Username, true) {
		s.renderSetupField(w, r, "Use lowercase letters, digits, dot, dash and underscore.", fieldUsername, http.StatusBadRequest)
		return
	}
	if err := CheckPasswordRules(who.Username, password); err != nil {
		s.renderSetupField(w, r, sentence(err.Error()), fieldPassword, http.StatusBadRequest)
		return
	}
	if password != r.PostFormValue("again") {
		s.renderSetupField(w, r, "The two passwords are not the same.", fieldAgain, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	acct, err := s.accounts.Setup(ctx, NewAccount{Username: who.Username, Name: r.PostFormValue("name"), Password: password})
	switch {
	case errors.Is(err, ErrFirstRunClosed):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		s.record(r, audit.ActionSetupFailed, who, audit.OutcomeFailed, err.Error())
		s.renderSetup(w, r, sentence(err.Error()), http.StatusBadRequest)
		return
	}
	user := User{Subject: "local:" + acct.Username, Username: acct.Username, Name: acct.Name}
	s.accounts.SignedIn(acct.Username, s.now())
	s.record(r, audit.ActionSetup, user, audit.OutcomeOK, "the first administrator was created")
	http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: "", Path: "/setup", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	s.start(w, r, Session{User: user, Method: string(ModeLocal)}, roles.Local, acct.Super, "first-run setup")
}

// sentence capitalizes a message and ends it with a period.
func sentence(m string) string {
	if m == "" {
		return m
	}
	b := []rune(m)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	if last := b[len(b)-1]; last != '.' && last != '?' && last != '!' {
		b = append(b, '.')
	}
	return string(b)
}

// live re-reads what a person may be, so a change takes effect in an open
// session within the access lifetime (RB-05, RB-23, RB-51). A local user is
// looked up on every request, which costs nothing; a directory person's
// groups are read again when they have stood for AccessTTL.
func (s *Service) live(sess Session) (Session, bool) {
	switch sess.Method {
	case string(ModeLocal):
		if s.accounts == nil {
			return sess, true
		}
		a, ok := s.accounts.Find(sess.User.Username)
		if !ok || a.Disabled {
			s.sessions.Delete(sess.ID)
			return Session{}, false
		}
		sess.User.Groups = a.Groups
		s.grant(roles.Local, sess.User, a.Super).apply(&sess)
		return sess, true
	case string(ModeLDAP):
		if s.dir != nil && s.now().Sub(sess.GroupsAt) >= s.cfg.AccessTTL {
			return s.rereadGroups(sess, false)
		}
	}
	return sess, true
}

// rereadGroups reads a directory person's groups again. A directory that
// cannot answer leaves the groups as they were and is asked again soon; a
// person no longer in any listed group loses the session (RB-23).
func (s *Service) rereadGroups(sess Session, force bool) (Session, bool) {
	s.reread.Lock()
	defer s.reread.Unlock()
	// Another request may have done it while this one waited.
	if cur, ok := s.sessions.Get(sess.ID); ok {
		sess = cur
		if !force && s.now().Sub(sess.GroupsAt) < s.cfg.AccessTTL {
			return s.withGrant(sess), true
		}
	} else {
		return Session{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	groups, err := s.dir.Groups(ctx, sess.Ref)
	if err != nil {
		s.log.Warn("reading a person's groups again failed; the old ones stand", "user", sess.User.Username, "err", cleanErr(err))
		retry := s.now().Add(-s.cfg.AccessTTL + 30*time.Second)
		s.sessions.Update(sess.ID, func(x *Session) { x.GroupsAt = retry })
		return s.withGrant(sess), true
	}
	user := sess.User
	user.Groups = groups
	g := s.grant(roles.LDAP, user, nil)
	if len(g.Roles) == 0 {
		s.sessions.Delete(sess.ID)
		if s.audit != nil {
			s.audit.Record(audit.Event{Action: audit.ActionSignOut, Who: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}, Outcome: audit.OutcomeDenied, Detail: "no longer in a group listed for a role; the session ended"})
		}
		return Session{}, false
	}
	now := s.now()
	s.sessions.Update(sess.ID, func(x *Session) {
		x.User.Groups, x.GroupsAt = groups, now
		g.apply(x)
	})
	sess.User.Groups, sess.GroupsAt = groups, now
	g.apply(&sess)
	return sess, true
}

func (s *Service) withGrant(sess Session) Session {
	s.grant(roles.LDAP, sess.User, nil).apply(&sess)
	return sess
}

// Reread reads a directory person's groups again, as when they approve a
// change (RB-23). Other people's sessions are returned as they are.
func (s *Service) Reread(sess Session) (Session, bool) {
	if s == nil || s.dir == nil || sess.Method != string(ModeLDAP) {
		return sess, true
	}
	return s.rereadGroups(sess, true)
}
