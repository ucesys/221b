package main

import (
	"slices"
	"strings"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

// installerOf says who installed the console, for the Star us on GitHub
// dialog (0.13.2, CU-21): a SuperAdmin the Helm values name. That is a name
// of auth.admins (a username, a subject or an email, as the sign-in
// matches them), a member of a group of auth.adminGroups, of a group given
// superAdmin under auth.ldap.roles or auth.oidc.roles, or a local user of
// auth.local.users with the role superAdmin. In local mode the SuperAdmin
// the first-run page created installed the console too (its account's
// Installer mark), while it is still a SuperAdmin. A SuperAdmin made later
// in the console (a local account given the role there) is not one.
// accounts gives the local accounts; nil, or nil returned, without them.
func installerOf(cfg auth.Config, accounts func() *auth.Accounts) api.Installer {
	return func(sess auth.Session) bool {
		if !sess.Admin {
			return false
		}
		u := sess.User
		for _, a := range cfg.Admins {
			if a != "" && (strings.EqualFold(a, u.Username) || a == u.Subject || (u.Email != "" && strings.EqualFold(a, u.Email))) {
				return true
			}
		}
		for _, g := range cfg.AdminGroups {
			if g != "" && slices.Contains(u.Groups, g) {
				return true
			}
		}
		if sess.Method != string(auth.ModeLocal) {
			for _, g := range cfg.Lists[roles.SuperAdmin] {
				if g != "" && slices.Contains(u.Groups, g) {
					return true
				}
			}
			return false
		}
		for _, l := range cfg.Local {
			if l.Login() == strings.ToLower(u.Username) && slices.Contains(l.Roles, string(roles.SuperAdmin)) {
				return true
			}
		}
		if accounts == nil {
			return false
		}
		acc := accounts()
		if acc == nil {
			return false
		}
		a, ok := acc.Find(u.Username)
		return ok && a.Installer && a.Username == strings.ToLower(u.Username)
	}
}

// localAccounts is the local accounts, nil without sign-in or without them.
func (w *wiring) localAccounts() *auth.Accounts {
	if w.authSvc == nil {
		return nil
	}
	return w.authSvc.Accounts()
}
