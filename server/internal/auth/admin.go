package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/roles"
)

// The Users page's data and the changes a SuperAdmin makes to local users
// (RB-43, RB-46, RB-51). Nothing here writes to a cluster: roles and
// bindings come from the chart (RB-52). Hashes never leave the server.

func (s *Service) adminRoutes() []Route {
	return []Route{
		{"GET /api/v1/admin/users", "admin-users", s.getUsers},
		{"POST /api/v1/admin/users", "admin-users-create", s.postUser},
		{"PATCH /api/v1/admin/users/{username}", "admin-users-update", s.patchUser},
		{"DELETE /api/v1/admin/users/{username}", "admin-users-delete", s.deleteUser},
		{"POST /api/v1/admin/users/{username}/groups", "admin-users-groups", s.postGroups},
		{"PUT /api/v1/admin/users/{username}/super", "admin-users-super", s.putSuper},
		{"DELETE /api/v1/admin/users/{username}/sessions", "admin-users-sessions", s.deleteSessions},
		{"POST /api/v1/admin/ldap/refresh", "admin-ldap-refresh", s.postRefresh},
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// superAdmin lets a SuperAdmin through. Everyone else, and a console with
// sign-in off, is refused: there are no users to manage (RB-31).
func (s *Service) superAdmin(w http.ResponseWriter, r *http.Request, change bool) (Session, bool) {
	sess, ok := SessionFrom(r.Context())
	if !ok || s.cfg.Mode == ModeNone {
		fail(w, http.StatusForbidden, "There are no users to manage when sign-in is off.")
		return Session{}, false
	}
	if !sess.Admin {
		fail(w, http.StatusForbidden, "Only a SuperAdmin can do this.")
		return Session{}, false
	}
	if change && !SameSite(r) {
		fail(w, http.StatusForbidden, "This change did not come from the console itself.")
		return Session{}, false
	}
	return sess, true
}

type holding struct {
	Role       roles.ID `json:"role"`
	Via        string   `json:"via,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
}

// UserRow is one person on the Users page.
type UserRow struct {
	ID         string     `json:"id"`
	Username   string     `json:"username"`
	Name       string     `json:"name,omitempty"`
	Email      string     `json:"email,omitempty"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	StatusNote string     `json:"statusNote,omitempty"`
	LastSignIn *time.Time `json:"lastSignIn,omitempty"`
	Holdings   []holding  `json:"holdings"`
	Groups     []string   `json:"groups"`
	SuperRoles []roles.ID `json:"superRoles"`
	Managed    bool       `json:"managed"`
	KubeName   string     `json:"kubeName"`
	SignedIn   bool       `json:"signedIn,omitempty"`
	Self       bool       `json:"self,omitempty"`
}

type groupRow struct {
	Name       string   `json:"name"`
	Role       roles.ID `json:"role"`
	Namespaces []string `json:"namespaces"`
	Members    int      `json:"members"`
}

type ldapRow struct {
	URL      string     `json:"url"`
	State    string     `json:"state"`
	Message  string     `json:"message,omitempty"`
	LastSync *time.Time `json:"lastSync,omitempty"`
	NextSync *time.Time `json:"nextSync,omitempty"`
	People   int        `json:"people"`
	Groups   int        `json:"groups"`
	Errors   []string   `json:"errors"`
	Running  bool       `json:"running"`
}

type usersResponse struct {
	Users      []UserRow  `json:"users"`
	Groups     []groupRow `json:"groups"`
	LDAP       *ldapRow   `json:"ldap,omitempty"`
	CanCreate  bool       `json:"canCreate"`
	Persistent bool       `json:"persistent"`
}

func ptr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// signedIn lists the user names with a live session, by method.
func (s *Service) signedIn() map[string]bool {
	out := map[string]bool{}
	for _, x := range s.sessions.Live() {
		out[x.Method+":"+x.User.Username] = true
	}
	return out
}

// holdings says which roles a person holds and through what.
func (s *Service) holdings(src roles.Source, u User, supers []roles.ID) []holding {
	var out []holding
	for _, id := range supers {
		out = append(out, holding{Role: id})
	}
	switch src {
	case roles.Local:
		for _, name := range u.Groups {
			for _, g := range s.cfg.LocalGroups {
				if g.Name == name {
					out = append(out, holding{Role: g.Role, Via: g.Name, Namespaces: g.Namespaces})
				}
			}
		}
	default:
		for _, id := range roles.All {
			for _, g := range s.cfg.Lists[id] {
				if slices.Contains(u.Groups, g) {
					out = append(out, holding{Role: id, Via: g})
				}
			}
		}
	}
	if s.isAdmin(u) && !slices.ContainsFunc(out, func(h holding) bool { return h.Role == roles.SuperAdmin }) {
		out = append(out, holding{Role: roles.SuperAdmin})
	}
	slices.SortStableFunc(out, func(a, b holding) int { return slices.Index(roles.All, a.Role) - slices.Index(roles.All, b.Role) })
	return out
}

func (s *Service) accountRow(a Account, on map[string]bool, self string) UserRow {
	u := User{Username: a.Username, Email: a.Email, Groups: a.Groups}
	row := UserRow{ID: "local:" + a.Username, Username: a.Username, Name: a.Name, Email: a.Email, Source: "local", Status: "active", LastSignIn: ptr(a.LastSignIn),
		Holdings: s.holdings(roles.Local, u, a.Super), Groups: append([]string{}, a.Groups...), SuperRoles: append([]roles.ID{}, a.Super...), Managed: a.Managed,
		KubeName: s.cfg.Prefixes.User(roles.Local, a.Username), SignedIn: on["local:"+a.Username], Self: a.Username == self}
	if a.Disabled {
		row.Status = "disabled"
	}
	if row.Holdings == nil {
		row.Holdings = []holding{}
	}
	return row
}

func (s *Service) usersView(self Session) usersResponse {
	on := s.signedIn()
	out := usersResponse{Users: []UserRow{}, Groups: []groupRow{}, CanCreate: s.cfg.Mode == ModeLocal, Persistent: true}
	if s.accounts != nil {
		out.Persistent = s.accounts.Persistent()
		selfName := ""
		if self.Method == string(ModeLocal) {
			selfName = self.User.Username
		}
		for _, a := range s.accounts.List() {
			out.Users = append(out.Users, s.accountRow(a, on, selfName))
		}
		for _, g := range s.accounts.Groups() {
			ns := g.Namespaces
			if ns == nil {
				ns = []string{}
			}
			out.Groups = append(out.Groups, groupRow{Name: g.Name, Role: g.Role, Namespaces: ns, Members: s.accounts.Members(g.Name)})
		}
	}
	if s.people != nil {
		st, list := s.people.snapshot()
		out.LDAP = &ldapRow{URL: st.URL, State: st.State, Message: st.Message, LastSync: ptr(st.Last), NextSync: ptr(st.Next), People: st.People, Groups: st.Groups, Errors: st.Errors, Running: st.Running}
		for _, d := range list {
			u := User{Username: d.Username, Email: d.Email, Groups: d.Groups}
			row := UserRow{ID: "ldap:" + d.Username, Username: d.Username, Name: d.Name, Email: d.Email, Source: "ldap", Status: "active", StatusNote: d.Note, LastSignIn: ptr(d.SignedIn),
				Holdings: s.holdings(roles.LDAP, u, nil), Groups: append([]string{}, d.Groups...), SuperRoles: []roles.ID{}, KubeName: s.cfg.Prefixes.User(roles.LDAP, d.Username),
				SignedIn: on["ldap:"+d.Username], Self: self.Method == string(ModeLDAP) && self.User.Username == d.Username}
			if d.Disabled {
				row.Status, row.Holdings = "disabled", []holding{}
			}
			out.Users = append(out.Users, row)
		}
	}
	return out
}

func (s *Service) getUsers(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.usersView(sess))
}

// recordUser writes the audit record of a change to a user (RB-54).
func (s *Service) recordUser(r *http.Request, by Session, action, target, detail, outcome string) {
	if s.audit == nil {
		return
	}
	s.audit.Record(audit.Event{Action: action, Who: audit.Who{Subject: by.User.Subject, Email: by.User.Email, Name: by.User.Name}, Target: target, Detail: detail, Outcome: outcome, From: from(r)})
}

func (s *Service) row(username string, by Session) UserRow {
	a, _ := s.accounts.Find(username)
	self := ""
	if by.Method == string(ModeLocal) {
		self = by.User.Username
	}
	return s.accountRow(a, s.signedIn(), self)
}

// refuseChange turns an account error into the response.
func refuseChange(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, sentence(err.Error()))
	case errors.Is(err, ErrManaged):
		fail(w, http.StatusForbidden, sentence(err.Error()))
	case errors.Is(err, ErrLastSuperAdmin):
		fail(w, http.StatusConflict, sentence(err.Error()))
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrNewerAccounts):
		fail(w, http.StatusServiceUnavailable, sentence(err.Error()))
	default:
		fail(w, http.StatusBadRequest, sentence(err.Error()))
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "The request is not valid JSON of the expected shape.")
		return false
	}
	return true
}

func (s *Service) localOnly(w http.ResponseWriter) bool {
	if s.accounts == nil {
		fail(w, http.StatusNotFound, "This console has no local users.")
		return false
	}
	return true
}

func (s *Service) postUser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, true)
	if !ok || !s.localOnly(w) {
		return
	}
	if s.cfg.Mode != ModeLocal {
		fail(w, http.StatusConflict, "Users come from the directory; local users are created in the deployment's values.")
		return
	}
	var in struct {
		Username string   `json:"username"`
		Name     string   `json:"name"`
		Email    string   `json:"email"`
		Password string   `json:"password"`
		Groups   []string `json:"groups"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a, err := s.accounts.Create(ctx, NewAccount{Username: in.Username, Name: in.Name, Email: in.Email, Password: in.Password, Groups: in.Groups})
	if err != nil {
		s.recordUser(r, sess, audit.ActionUserCreate, strings.TrimSpace(in.Username), err.Error(), audit.OutcomeFailed)
		refuseChange(w, err)
		return
	}
	s.recordUser(r, sess, audit.ActionUserCreate, a.Username, "groups: "+strings.Join(a.Groups, ", "), audit.OutcomeOK)
	writeJSON(w, http.StatusCreated, s.row(a.Username, sess))
}

func (s *Service) patchUser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, true)
	if !ok || !s.localOnly(w) {
		return
	}
	name := r.PathValue("username")
	var in struct {
		Disabled *bool   `json:"disabled"`
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		Password *string `json:"password"`
		// CurrentPassword is the SuperAdmin's own, which a password change
		// asks for (6.4).
		CurrentPassword string `json:"currentPassword"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Password != nil && !s.ownPassword(w, r, sess, in.CurrentPassword) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	_, end, err := s.accounts.Update(ctx, name, Change{Name: in.Name, Email: in.Email, Password: in.Password, Disabled: in.Disabled})
	if err != nil {
		s.recordUser(r, sess, audit.ActionUserUpdate, name, err.Error(), audit.OutcomeFailed)
		refuseChange(w, err)
		return
	}
	var what []string
	if in.Password != nil {
		what = append(what, "password changed")
	}
	if in.Disabled != nil {
		what = append(what, map[bool]string{true: "disabled", false: "enabled"}[*in.Disabled])
	}
	if in.Name != nil || in.Email != nil {
		what = append(what, "details changed")
	}
	if end {
		s.endSessions(name)
		what = append(what, "sessions ended")
	}
	s.recordUser(r, sess, audit.ActionUserUpdate, name, strings.Join(what, ", "), audit.OutcomeOK)
	writeJSON(w, http.StatusOK, s.row(name, sess))
}

// ownPassword checks the password a SuperAdmin gives to change someone's
// password: their own, as they sign in with it, and as often as a sign-in.
func (s *Service) ownPassword(w http.ResponseWriter, r *http.Request, sess Session, password string) bool {
	if sess.Method != string(ModeLocal) {
		fail(w, http.StatusForbidden, "Sign in with a local account to change passwords.")
		return false
	}
	if !s.perAccount.allow(sess.User.Username) {
		s.recordUser(r, sess, audit.ActionUserUpdate, r.PathValue("username"), "too many attempts at the SuperAdmin’s own password", audit.OutcomeDenied)
		fail(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
		return false
	}
	acct, ok := s.accounts.Find(sess.User.Username)
	if !ok || !CheckPassword(acct.PasswordHash, password) {
		s.recordUser(r, sess, audit.ActionUserUpdate, r.PathValue("username"), "the SuperAdmin’s own password was wrong", audit.OutcomeDenied)
		fail(w, http.StatusForbidden, "Your password is not right.")
		return false
	}
	return true
}

// endSessions signs a local user out everywhere.
func (s *Service) endSessions(username string) int {
	return s.sessions.DeleteWhere(func(x Session) bool { return x.Method == string(ModeLocal) && x.User.Username == username })
}

func (s *Service) deleteUser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, true)
	if !ok || !s.localOnly(w) {
		return
	}
	name := r.PathValue("username")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.accounts.Delete(ctx, name); err != nil {
		s.recordUser(r, sess, audit.ActionUserDelete, name, err.Error(), audit.OutcomeFailed)
		refuseChange(w, err)
		return
	}
	n := s.endSessions(name)
	s.recordUser(r, sess, audit.ActionUserDelete, name, fmt.Sprintf("memberships and super roles removed with the user; %d sessions ended", n), audit.OutcomeOK)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) postGroups(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, true)
	if !ok || !s.localOnly(w) {
		return
	}
	name := r.PathValue("username")
	var in struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.accounts.ChangeGroups(ctx, name, in.Add, in.Remove); err != nil {
		s.recordUser(r, sess, audit.ActionUserGroups, name, err.Error(), audit.OutcomeFailed)
		refuseChange(w, err)
		return
	}
	s.recordUser(r, sess, audit.ActionUserGroups, name, "added to: "+strings.Join(in.Add, ", ")+"; removed from: "+strings.Join(in.Remove, ", "), audit.OutcomeOK)
	writeJSON(w, http.StatusOK, s.row(name, sess))
}

func (s *Service) putSuper(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, true)
	if !ok || !s.localOnly(w) {
		return
	}
	name := r.PathValue("username")
	var in struct {
		Roles []roles.ID `json:"roles"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.accounts.SetSuper(ctx, name, in.Roles); err != nil {
		s.recordUser(r, sess, audit.ActionUserSuper, name, err.Error(), audit.OutcomeFailed)
		refuseChange(w, err)
		return
	}
	names := make([]string, len(in.Roles))
	for i, id := range in.Roles {
		names[i] = id.Name()
	}
	s.recordUser(r, sess, audit.ActionUserSuper, name, "super roles now: "+strings.Join(names, ", "), audit.OutcomeOK)
	writeJSON(w, http.StatusOK, s.row(name, sess))
}

func (s *Service) deleteSessions(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.superAdmin(w, r, true)
	if !ok || !s.localOnly(w) {
		return
	}
	name := r.PathValue("username")
	if _, found := s.accounts.Find(name); !found {
		fail(w, http.StatusNotFound, "No such user.")
		return
	}
	n := s.endSessions(name)
	s.recordUser(r, sess, audit.ActionUserSessions, name, fmt.Sprintf("%d sessions ended", n), audit.OutcomeOK)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) postRefresh(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.superAdmin(w, r, true); !ok {
		return
	}
	if s.people == nil {
		fail(w, http.StatusNotFound, "This console does not use a directory.")
		return
	}
	s.SyncDirectoryAsync()
	st, _ := s.people.snapshot()
	row := ldapRow{URL: st.URL, State: st.State, Message: st.Message, LastSync: ptr(st.Last), NextSync: ptr(st.Next), People: st.People, Groups: st.Groups, Errors: st.Errors, Running: true}
	writeJSON(w, http.StatusAccepted, row)
}

// SyncDirectoryAsync starts a sync in the background unless one is running
// (RB-25).
func (s *Service) SyncDirectoryAsync() {
	if s.people == nil || !s.people.begin() {
		return
	}
	go func() {
		defer s.people.end()
		s.syncDirectory(context.Background())
	}()
}

// syncDirectory reads the members of the listed groups and merges them into
// the list the Users page shows (RB-24). A failure keeps the old list.
func (s *Service) syncDirectory(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	next := s.now().Add(s.cfg.SyncInterval)
	listed := s.cfg.Lists.Listed()
	members, err := s.dir.Members(ctx, listed)
	if err != nil {
		s.people.failed(err, next)
		s.log.Warn("the directory sync failed; the old list stands", "err", cleanErr(err))
		if s.audit != nil {
			s.audit.Record(audit.Event{Action: audit.ActionLDAPSync, Outcome: audit.OutcomeFailed, Detail: cleanErr(err)})
		}
		return
	}
	s.people.apply(members, len(listed), next)
	if s.audit != nil {
		s.audit.Record(audit.Event{Action: audit.ActionLDAPSync, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%d people in %d listed groups", len(members), len(listed))})
	}
}

// RunDirectory syncs at start and then every interval, until ctx ends.
func (s *Service) RunDirectory(ctx context.Context) {
	if s.people == nil {
		return
	}
	s.SyncDirectoryAsync()
	t := time.NewTicker(s.cfg.SyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncDirectoryAsync()
		}
	}
}
