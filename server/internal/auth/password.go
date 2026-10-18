package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// Changing one's own password from the sign-in page (6.4): the user name,
// the current password and the new one twice. It is checked like a
// sign-in, refused in the same words whatever the reason, and ends every
// session of the account, as a SuperAdmin's change does.

type passwordData struct {
	Title string
	Error string
	// Field is the field the error is about, shown beside it; with none,
	// the error is shown above the form.
	Field    string
	Form     string
	Next     string
	Username string
	// Current and Password are given back when the error is about a later
	// field, so a mistyped repeat does not cost typing them all again.
	Current  string
	Password string
	// Directory says that directory accounts change their password there.
	Directory bool
}

// Fields of the change-password form an error can be about.
const (
	fieldNew       = "password"
	fieldNewAgain  = "again"
	passwordRefuse = "Your password was not changed. Check your username and current password."
)

func (s *Service) passwordOn() bool { return s.cfg.Mode == ModeLocal || s.cfg.Mode == ModeLDAP }

func (s *Service) renderPassword(w http.ResponseWriter, r *http.Request, msg, field string, code int) {
	d := passwordData{Title: s.cfg.Title, Error: msg, Field: field, Form: randomID(), Next: safeNext(r.URL.Query().Get("next")), Directory: s.dir != nil}
	if r.Method == http.MethodPost {
		d.Username, d.Next = r.PostFormValue("username"), safeNext(r.PostFormValue("next"))
		switch field {
		case fieldNew:
			d.Current = r.PostFormValue("current")
		case fieldNewAgain:
			d.Current, d.Password = r.PostFormValue("current"), r.PostFormValue("password")
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: d.Form, Path: "/signin", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := s.passwordPage.Execute(w, d); err != nil {
		s.log.Warn("rendering the change-password page", "err", err)
	}
}

func (s *Service) getPassword(w http.ResponseWriter, r *http.Request) {
	if !s.passwordOn() {
		http.Redirect(w, r, "/signin", http.StatusFound)
		return
	}
	s.renderPassword(w, r, "", "", http.StatusOK)
}

// postPassword changes a local account's password, given the current one.
func (s *Service) postPassword(w http.ResponseWriter, r *http.Request) {
	if !s.passwordOn() {
		http.Error(w, "local sign-in is off", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderPassword(w, r, "The form could not be read.", "", http.StatusBadRequest)
		return
	}
	if !sameSiteHeaders(r, true) || !s.formToken(r) {
		s.renderPassword(w, r, "That request did not come from this console. Try again from this page.", "", http.StatusForbidden)
		return
	}
	login := Login(r.PostFormValue("username"))
	current, password := r.PostFormValue("current"), r.PostFormValue("password")
	who := User{Username: login, Email: login}
	if !s.perAddress.allow(from(r)) || !s.perAccount.allow(login) {
		s.record(r, audit.ActionPasswordChange, who, audit.OutcomeDenied, "too many attempts")
		s.renderPassword(w, r, "Too many attempts. Wait a minute and try again.", "", http.StatusTooManyRequests)
		return
	}
	// As at sign-in, an unknown account is checked against a dummy hash, so
	// the answer takes as long either way.
	acct, local := s.accounts.Find(login)
	hash := acct.PasswordHash
	if !local {
		hash = dummyHash
	}
	if !CheckPassword(hash, current) || !local || acct.Disabled {
		reason := "wrong user name or current password"
		if local && acct.Disabled {
			reason = "the account is disabled"
		}
		s.record(r, audit.ActionPasswordChange, who, audit.OutcomeFailed, reason)
		s.renderPassword(w, r, passwordRefuse, "", http.StatusUnauthorized)
		return
	}
	who = User{Subject: "local:" + acct.Username, Username: acct.Username, Email: acct.Email, Name: acct.Name}
	if err := CheckPasswordRules(acct.Username, password); err != nil {
		s.renderPassword(w, r, sentence(err.Error()), fieldNew, http.StatusBadRequest)
		return
	}
	if password == current {
		s.renderPassword(w, r, "Choose a new password, not the current one.", fieldNew, http.StatusBadRequest)
		return
	}
	if password != r.PostFormValue("again") {
		s.renderPassword(w, r, "The two passwords are not the same.", fieldNewAgain, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, _, err := s.accounts.Update(ctx, acct.Username, Change{Password: &password}); err != nil {
		s.record(r, audit.ActionPasswordChange, who, audit.OutcomeFailed, err.Error())
		msg := sentence(err.Error())
		if errors.Is(err, ErrManaged) {
			msg = "This account is defined in the deployment’s values, so its password is changed there, with helm upgrade."
		}
		s.renderPassword(w, r, msg, "", http.StatusBadRequest)
		return
	}
	n := s.endSessions(acct.Username)
	s.record(r, audit.ActionPasswordChange, who, audit.OutcomeOK, fmt.Sprintf("changed from the sign-in page; %d sessions ended", n))
	http.SetCookie(w, &http.Cookie{Name: cookieForm, Value: "", Path: "/signin", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/signin?changed=1&next="+url.QueryEscape(safeNext(r.PostFormValue("next"))), http.StatusSeeOther)
}
