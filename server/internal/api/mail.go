package api

import (
	"errors"
	"net/http"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/mail"
)

// --- Settings › Email (EM-01 to EM-04, ADR-158) ---
//
// A SuperAdmin's: the SMTP server the console emails the people involved
// in a case through. The password is written, never read back: the
// answer says only whether one is set. A test sends one email to the
// address given, the SuperAdmin's own by default, and email is on only
// once a test with the settings as they stand has passed.

func (s *Server) mailRoutes() {
	s.handle("GET /api/v1/settings/email", "settings-email", s.getMail)
	s.handle("PUT /api/v1/settings/email", "settings-email-save", s.putMail)
	s.handle("POST /api/v1/settings/email/test", "settings-email-test", s.postMailTest)
}

// mailView is Settings › Email for the page, with the SuperAdmin's own
// address for the test's default.
type mailView struct {
	mail.View
	You string `json:"you,omitempty"`
}

func (s *Server) mailAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin sets Settings › Email.")
		return false
	}
	return true
}

// mailPerson is who changes or tests the settings, as the audit log names
// them.
func (s *Server) mailPerson(r *http.Request) mail.Person {
	p := mail.Person{Name: "anonymous", From: fromAddr(r)}
	if sess, ok := auth.SessionFrom(r.Context()); ok && s.opts.Auth != nil {
		p.Subject, p.Email, p.Name = sess.User.Subject, sess.User.Email, firstOf(sess.User.Name, sess.User.Username, sess.User.Email)
	}
	return p
}

func (s *Server) answerMail(w http.ResponseWriter, r *http.Request, v mail.View) {
	writeValue(w, r, mailView{View: v, You: s.mailPerson(r).Email})
}

func mailError(w http.ResponseWriter, err error) {
	if errors.Is(err, mail.ErrInvalid) {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonError(w, http.StatusServiceUnavailable, err.Error())
}

func (s *Server) getMail(w http.ResponseWriter, r *http.Request) {
	if !s.mailAdmin(w, r) {
		return
	}
	v, err := s.opts.Mail.Settings(r.Context())
	if err != nil {
		mailError(w, err)
		return
	}
	s.answerMail(w, r, v)
}

func (s *Server) putMail(w http.ResponseWriter, r *http.Request) {
	if !s.mailAdmin(w, r) {
		return
	}
	var in mail.Input
	if !decodeAutomation(w, r, &in) {
		return
	}
	v, err := s.opts.Mail.Save(r.Context(), in, s.mailPerson(r))
	if err != nil {
		mailError(w, err)
		return
	}
	s.answerMail(w, r, v)
}

// postMailTest sends the test email. Not getting through is an answer,
// not an error: the view says why, and email stays off.
func (s *Server) postMailTest(w http.ResponseWriter, r *http.Request) {
	if !s.mailAdmin(w, r) {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	v, err := s.opts.Mail.Test(r.Context(), in.To, s.mailPerson(r))
	if err != nil {
		mailError(w, err)
		return
	}
	s.answerMail(w, r, v)
}

// --- end of Settings › Email.
