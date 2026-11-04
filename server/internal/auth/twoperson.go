package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// Two people for what would let one person loosen what needs two (review
// item 14): making a local user a SuperAdmin waits for another SuperAdmin,
// when there is one. The deployment's values stay the way around it.

// PersonKey tells people apart in a request: the subject, or the username.
func PersonKey(u User) string {
	if u.Subject != "" {
		return u.Subject
	}
	return u.Username
}

// WhoOf is a session's person as a request names them.
func WhoOf(sess Session) twoperson.Who {
	name := sess.User.Name
	if name == "" {
		name = sess.User.Email
	}
	if name == "" {
		name = sess.User.Username
	}
	return twoperson.Who{Key: PersonKey(sess.User), Name: name}
}

// accountAdmin says whether a local account is a SuperAdmin: by its own
// super roles, a group that gives the role, or the deployment's admin list.
func (s *Service) accountAdmin(a Account) bool {
	return s.grant(roles.Local, User{Username: a.Username, Email: a.Email, Groups: a.Groups}, a.Super).Admin()
}

// OtherSuperAdmin says whether someone besides the person signed in could
// approve as a SuperAdmin: an enabled local account that is one, another
// name in the deployment's admin list, or, with a directory or a provider,
// a group that gives the role, whose members the console cannot count.
func (s *Service) OtherSuperAdmin(sess Session) bool {
	if s.cfg.Mode == ModeNone {
		return false
	}
	me := sess.User
	if s.cfg.Mode != ModeLocal && (len(s.cfg.AdminGroups) > 0 || len(s.cfg.Lists[roles.SuperAdmin]) > 0) {
		return true
	}
	for _, a := range s.cfg.Admins {
		if a != "" && !strings.EqualFold(a, me.Username) && a != me.Subject && (me.Email == "" || !strings.EqualFold(a, me.Email)) {
			return true
		}
	}
	if s.accounts != nil {
		for _, a := range s.accounts.List() {
			if a.Disabled || (sess.Method == string(ModeLocal) && a.Username == me.Username) {
				continue
			}
			if s.accountAdmin(a) {
				return true
			}
		}
	}
	return false
}

// SuperChange is a change to a local account's super roles and groups,
// split into what applies now and what waits.
type SuperChange struct {
	Super  []roles.ID
	Groups []string
	// Hold is what waits for another SuperAdmin, nil when nothing does.
	Hold *twoperson.Request
	// Alone says the change makes a SuperAdmin at once because nobody else
	// could approve it.
	Alone bool
}

// SuperAdminHold splits a change to a local account: when it would make
// someone a SuperAdmin who is not one, and another SuperAdmin could
// approve, the SuperAdmin role and the groups that give it wait.
func (s *Service) SuperAdminHold(sess Session, before Account, super []roles.ID, groups []string) SuperChange {
	out := SuperChange{Super: super, Groups: groups}
	after := before
	after.Super, after.Groups = super, groups
	if s.accountAdmin(before) || !s.accountAdmin(after) || s.cfg.TwoPerson == nil {
		return out
	}
	if !s.OtherSuperAdmin(sess) {
		out.Alone = true
		return out
	}
	hold := twoperson.Request{Kind: twoperson.KindSuperAdmin, User: before.Username, By: WhoOf(sess)}
	out.Super = nil
	for _, r := range super {
		if r == roles.SuperAdmin {
			hold.Super = true
		} else {
			out.Super = append(out.Super, r)
		}
	}
	out.Groups = nil
	for _, g := range groups {
		if !slices.Contains(before.Groups, g) && s.accountAdmin(Account{Username: before.Username, Email: before.Email, Groups: []string{g}}) {
			hold.Groups = append(hold.Groups, g)
		} else {
			out.Groups = append(out.Groups, g)
		}
	}
	out.Hold = &hold
	return out
}

// HoldText is what the person is told of a change that waits.
func HoldText(r twoperson.Request) string {
	return "Making " + r.User + " a SuperAdmin waits for another SuperAdmin to approve it on Administration › Approvals (request " + strconv.Itoa(r.ID) + "); the rest of the change is made."
}

// RequestSuperAdmin keeps what waits, and records it.
func (s *Service) RequestSuperAdmin(ctx context.Context, r *http.Request, sess Session, hold twoperson.Request) (twoperson.Request, error) {
	if s.cfg.TwoPerson == nil {
		return hold, errors.New("this console keeps no requests")
	}
	saved, err := s.cfg.TwoPerson.Add(ctx, hold)
	outcome, detail := audit.OutcomeOK, "waits for another SuperAdmin: "+hold.What()
	if err != nil {
		outcome, detail = audit.OutcomeFailed, err.Error()
	}
	s.recordUser(r, sess, audit.ActionUserSuper, hold.User, detail, outcome)
	return saved, err
}

// ApplySuperAdmin makes the change an approved request asked for.
func (s *Service) ApplySuperAdmin(ctx context.Context, r twoperson.Request) error {
	if s.accounts == nil {
		return errors.New("this console has no local users")
	}
	a, ok := s.accounts.Find(r.User)
	if !ok {
		return ErrNotFound
	}
	if r.Super && !slices.Contains(a.Super, roles.SuperAdmin) {
		if _, err := s.accounts.SetSuper(ctx, r.User, append(append([]roles.ID{}, a.Super...), roles.SuperAdmin)); err != nil {
			return err
		}
	}
	if len(r.Groups) > 0 {
		if _, err := s.accounts.ChangeGroups(ctx, r.User, r.Groups, nil); err != nil {
			return err
		}
	}
	return nil
}
