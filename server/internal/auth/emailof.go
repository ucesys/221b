package auth

import "strings"

// EmailOf is a person's email address by their user name, for the emails
// about cases (EM-05): a local account's, unless it is disabled, or the
// directory's for a person it lists. "" when neither knows one: a person
// who signs in through OIDC is known by the address their sign-in gave,
// which the case keeps.
func (s *Service) EmailOf(username string) string {
	if s == nil || strings.TrimSpace(username) == "" {
		return ""
	}
	if s.accounts != nil {
		if u, ok := s.accounts.Find(username); ok && !u.Disabled {
			return u.Email
		}
	}
	if s.people != nil {
		s.people.mu.Lock()
		defer s.people.mu.Unlock()
		if d := s.people.byName[username]; d != nil && !d.Disabled {
			return d.Email
		}
	}
	return ""
}
