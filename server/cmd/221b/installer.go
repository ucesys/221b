package main

import (
	"slices"
	"strings"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

// installerOf says who installed the console, for the Star us on GitHub
// dialog (0.14.0, CU-21): a SuperAdmin the Helm values name. That is a name
// of auth.admins (a username, a subject or an email, as the sign-in
// matches them), a member of a group of auth.adminGroups, of a group given
// superAdmin under auth.ldap.roles or auth.oidc.roles, or a local user of
// auth.local.users with the role superAdmin. A SuperAdmin made in the
// console (a local account given the role there, or the first-run page's)
// is not one.
func installerOf(cfg auth.Config) api.Installer {
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
		return false
	}
}
