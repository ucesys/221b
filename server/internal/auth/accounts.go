package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/roles"
)

// Errors the account rules give; the handlers turn them into messages.
var (
	// ErrNoAccounts is what a Backend returns when nothing was saved yet.
	ErrNoAccounts = errors.New("no accounts saved")
	// ErrManaged refuses a change to an account defined in values (RB-46).
	ErrManaged = errors.New("this account is defined in the deployment's values; change it there")
	// ErrLastSuperAdmin refuses to remove the last SuperAdmin (RB-30).
	ErrLastSuperAdmin = errors.New("this is the last SuperAdmin; give the role to someone else first")
	// ErrNotFound says no such account.
	ErrNotFound = errors.New("no such user")
	// ErrFirstRunClosed says the first administrator exists already (RB-26).
	ErrFirstRunClosed = errors.New("the first administrator exists already")
	// ErrUnavailable says the accounts could not be read, so nothing is
	// allowed to change them or to open the first-run page.
	ErrUnavailable = errors.New("the accounts could not be read; check the console's log")
)

// Backend keeps the accounts created in the console: a Secret of the
// console's own namespace in a cluster, memory on a laptop (RB-46).
type Backend interface {
	Load(ctx context.Context) ([]byte, error)
	Save(ctx context.Context, b []byte) error
}

// MemoryBackend keeps the accounts until the server restarts.
type MemoryBackend struct {
	mu sync.Mutex
	b  []byte
}

// Load returns what was saved.
func (m *MemoryBackend) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.b == nil {
		return nil, ErrNoAccounts
	}
	return append([]byte(nil), m.b...), nil
}

// Save keeps b.
func (m *MemoryBackend) Save(_ context.Context, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.b = append([]byte(nil), b...)
	return nil
}

// Account is a local user: someone whose password the console holds
// (RB-46). Groups are names of local groups (RB-50); Super are the roles
// given in 221B itself (RB-05).
type Account struct {
	Username     string     `json:"username"`
	Name         string     `json:"name,omitempty"`
	Email        string     `json:"email,omitempty"`
	PasswordHash string     `json:"passwordHash"`
	Groups       []string   `json:"groups,omitempty"`
	Super        []roles.ID `json:"super,omitempty"`
	Disabled     bool       `json:"disabled,omitempty"`
	Created      time.Time  `json:"created"`
	LastSignIn   time.Time  `json:"lastSignIn,omitzero"`
	// Managed accounts come from the deployment's values, and are never
	// changed or kept here.
	Managed bool `json:"-"`
}

// LocalGroup is a group of local users and the role it gives, in the
// namespaces the chart binds it to (RB-50).
type LocalGroup struct {
	Name       string   `json:"name"`
	Role       roles.ID `json:"role"`
	Namespaces []string `json:"namespaces"`
}

// MinPassword is the shortest password of a local user (RB-46).
const MinPassword = 12

// CheckPasswordRules says why a password is not allowed, in words for the
// person.
func CheckPasswordRules(username, password string) error {
	if len([]rune(password)) < MinPassword {
		return fmt.Errorf("use at least %d characters", MinPassword)
	}
	if strings.EqualFold(password, username) {
		return errors.New("the password cannot be the user name")
	}
	return nil
}

// stored is what the backend holds.
type stored struct {
	Users []Account `json:"users"`
}

// Accounts holds the local users: those from values, and those created in
// the console. All changes go through it, so the rules of RB-30, RB-46 and
// RB-51 are kept in one place.
type Accounts struct {
	mu      sync.Mutex
	backend Backend
	managed map[string]*Account
	stored  map[string]*Account
	groups  map[string]LocalGroup
	loaded  bool
	// external says that SuperAdmins exist that are not accounts (the
	// listed groups of LDAP and OIDC, auth.admins), so the last account may
	// lose the role.
	external bool
	now      func() time.Time
}

// NewAccounts makes the set. Managed accounts come from values.
func NewAccounts(backend Backend, managed []Account, groups []LocalGroup, external bool, now func() time.Time) *Accounts {
	if now == nil {
		now = time.Now
	}
	a := &Accounts{backend: backend, managed: map[string]*Account{}, stored: map[string]*Account{}, groups: map[string]LocalGroup{}, external: external, now: now}
	for i := range managed {
		m := managed[i]
		m.Managed = true
		a.managed[m.Username] = &m
	}
	for _, g := range groups {
		a.groups[g.Name] = g
	}
	return a
}

// Persistent says whether accounts created here survive a restart.
func (a *Accounts) Persistent() bool {
	_, memory := a.backend.(*MemoryBackend)
	return !memory
}

// Load reads the accounts kept by the backend. Until it has succeeded,
// nothing can be created and the first-run page stays closed: an unreadable
// backend is not an empty one.
func (a *Accounts) Load(ctx context.Context) error {
	b, err := a.backend.Load(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case errors.Is(err, ErrNoAccounts):
		a.loaded = true
		return nil
	case err != nil:
		return err
	}
	var s stored
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("the saved accounts are not valid JSON: %w", err)
	}
	a.stored = map[string]*Account{}
	for i := range s.Users {
		u := s.Users[i]
		a.stored[u.Username] = &u
	}
	a.loaded = true
	return nil
}

func (a *Accounts) save(ctx context.Context) error {
	var s stored
	for _, u := range a.stored {
		s.Users = append(s.Users, *u)
	}
	sort.Slice(s.Users, func(i, j int) bool { return s.Users[i].Username < s.Users[j].Username })
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return a.backend.Save(ctx, b)
}

// get finds an account; callers hold the lock.
func (a *Accounts) get(username string) *Account {
	if u, ok := a.managed[username]; ok {
		return u
	}
	return a.stored[username]
}

// Find looks an account up by its user name, or by its email when the name
// has an @ and the email is unique.
func (a *Accounts) Find(login string) (Account, bool) {
	login = strings.ToLower(strings.TrimSpace(login))
	a.mu.Lock()
	defer a.mu.Unlock()
	if u := a.get(login); u != nil {
		return *u, true
	}
	if !strings.Contains(login, "@") {
		return Account{}, false
	}
	var found *Account
	for _, set := range []map[string]*Account{a.managed, a.stored} {
		for _, u := range set {
			if strings.EqualFold(u.Email, login) {
				if found != nil {
					return Account{}, false
				}
				found = u
			}
		}
	}
	if found == nil {
		return Account{}, false
	}
	return *found, true
}

// List returns every account, by user name.
func (a *Accounts) List() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Account
	for _, set := range []map[string]*Account{a.managed, a.stored} {
		for _, u := range set {
			out = append(out, *u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// Groups returns the defined groups with their member counts.
func (a *Accounts) Groups() []LocalGroup { return a.groupList() }

func (a *Accounts) groupList() []LocalGroup {
	var out []LocalGroup
	for _, g := range a.groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Group returns a defined group.
func (a *Accounts) Group(name string) (LocalGroup, bool) {
	g, ok := a.groups[name]
	return g, ok
}

// Members counts the accounts in a group.
func (a *Accounts) Members(group string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, set := range []map[string]*Account{a.managed, a.stored} {
		for _, u := range set {
			if slices.Contains(u.Groups, group) {
				n++
			}
		}
	}
	return n
}

// Loaded says whether the saved accounts were read.
func (a *Accounts) Loaded() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loaded
}

// FirstRun says whether the first-run page is open: no account exists, and
// the stored ones were read (RB-26).
func (a *Accounts) FirstRun() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loaded && len(a.managed) == 0 && len(a.stored) == 0
}

// NewAccount is what creating a local user takes.
type NewAccount struct {
	Username string
	Name     string
	Email    string
	Password string
	Groups   []string
}

func (a *Accounts) validate(n NewAccount) error {
	if !roles.ValidUsername(n.Username, true) {
		return errors.New("use lowercase letters, digits, dot, dash and underscore for the user name")
	}
	if err := CheckPasswordRules(n.Username, n.Password); err != nil {
		return err
	}
	if n.Email != "" && !strings.Contains(n.Email, "@") {
		return errors.New("that is not an email address")
	}
	for _, g := range n.Groups {
		if _, ok := a.groups[g]; !ok {
			return fmt.Errorf("there is no group %q", g)
		}
	}
	return nil
}

// Setup creates the first administrator, a SuperAdmin, once, when no
// account exists (RB-26).
func (a *Accounts) Setup(ctx context.Context, n NewAccount) (Account, error) {
	n.Username = strings.TrimSpace(n.Username)
	n.Groups = nil
	if err := a.validate(n); err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(n.Password)
	if err != nil {
		return Account{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		return Account{}, ErrUnavailable
	}
	if len(a.managed) > 0 || len(a.stored) > 0 {
		return Account{}, ErrFirstRunClosed
	}
	u := &Account{Username: n.Username, Name: strings.TrimSpace(n.Name), Email: strings.ToLower(strings.TrimSpace(n.Email)), PasswordHash: hash, Super: []roles.ID{roles.SuperAdmin}, Created: a.now()}
	a.stored[u.Username] = u
	if err := a.save(ctx); err != nil {
		delete(a.stored, u.Username)
		return Account{}, err
	}
	return *u, nil
}

// Create adds a local user (RB-46).
func (a *Accounts) Create(ctx context.Context, n NewAccount) (Account, error) {
	n.Username = strings.TrimSpace(n.Username)
	if err := a.validate(n); err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(n.Password)
	if err != nil {
		return Account{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		return Account{}, ErrUnavailable
	}
	if a.get(n.Username) != nil {
		return Account{}, fmt.Errorf("%s exists already", n.Username)
	}
	u := &Account{Username: n.Username, Name: strings.TrimSpace(n.Name), Email: strings.ToLower(strings.TrimSpace(n.Email)), PasswordHash: hash, Groups: slices.Clone(n.Groups), Created: a.now()}
	a.stored[u.Username] = u
	if err := a.save(ctx); err != nil {
		delete(a.stored, u.Username)
		return Account{}, err
	}
	return *u, nil
}

// Change is an edit to an account; nil fields stay as they are.
type Change struct {
	Name     *string
	Email    *string
	Password *string
	Disabled *bool
}

// edit applies fn to a copy of a stored account, saves, and puts it back, or
// leaves everything as it was when the save fails.
func (a *Accounts) edit(ctx context.Context, username string, fn func(u *Account) error) (Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		return Account{}, ErrUnavailable
	}
	cur := a.get(username)
	if cur == nil {
		return Account{}, ErrNotFound
	}
	if cur.Managed {
		return Account{}, ErrManaged
	}
	next := *cur
	next.Groups = slices.Clone(cur.Groups)
	next.Super = slices.Clone(cur.Super)
	if err := fn(&next); err != nil {
		return Account{}, err
	}
	wasSuper := !cur.Disabled && slices.Contains(cur.Super, roles.SuperAdmin)
	isSuper := !next.Disabled && slices.Contains(next.Super, roles.SuperAdmin)
	if wasSuper && !isSuper && !a.external && a.superAdmins(username, &next) == 0 {
		return Account{}, ErrLastSuperAdmin
	}
	a.stored[username] = &next
	if err := a.save(ctx); err != nil {
		a.stored[username] = cur
		return Account{}, err
	}
	return next, nil
}

// superAdmins counts the enabled SuperAdmins, with account `name` taken as
// `as`. Callers hold the lock.
func (a *Accounts) superAdmins(name string, as *Account) int {
	n := 0
	for _, set := range []map[string]*Account{a.managed, a.stored} {
		for k, u := range set {
			if k == name && as != nil {
				u = as
			}
			if !u.Disabled && slices.Contains(u.Super, roles.SuperAdmin) {
				n++
			}
		}
	}
	return n
}

// Update changes a local user's details, password or whether they may sign
// in. It reports whether the person's sessions must end: a reset or a
// disable (RB-46).
func (a *Accounts) Update(ctx context.Context, username string, c Change) (Account, bool, error) {
	var end bool
	var hash string
	if c.Password != nil {
		if err := CheckPasswordRules(username, *c.Password); err != nil {
			return Account{}, false, err
		}
		h, err := HashPassword(*c.Password)
		if err != nil {
			return Account{}, false, err
		}
		hash = h
	}
	if c.Email != nil && *c.Email != "" && !strings.Contains(*c.Email, "@") {
		return Account{}, false, errors.New("that is not an email address")
	}
	u, err := a.edit(ctx, username, func(u *Account) error {
		if c.Name != nil {
			u.Name = strings.TrimSpace(*c.Name)
		}
		if c.Email != nil {
			u.Email = strings.ToLower(strings.TrimSpace(*c.Email))
		}
		if c.Password != nil {
			u.PasswordHash = hash
			end = true
		}
		if c.Disabled != nil {
			if *c.Disabled && !u.Disabled {
				end = true
			}
			u.Disabled = *c.Disabled
		}
		return nil
	})
	return u, end, err
}

// Delete removes a local user, their group memberships and super roles with
// them.
func (a *Accounts) Delete(ctx context.Context, username string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		return ErrUnavailable
	}
	cur := a.get(username)
	if cur == nil {
		return ErrNotFound
	}
	if cur.Managed {
		return ErrManaged
	}
	if !a.external && slices.Contains(cur.Super, roles.SuperAdmin) && !cur.Disabled && a.superAdmins(username, &Account{Disabled: true}) == 0 {
		return ErrLastSuperAdmin
	}
	delete(a.stored, username)
	if err := a.save(ctx); err != nil {
		a.stored[username] = cur
		return err
	}
	return nil
}

// ChangeGroups adds a local user to groups and removes them from others
// (RB-51). Adding needs a defined group; removing does not, so a group
// taken out of values can still be cleaned up.
func (a *Accounts) ChangeGroups(ctx context.Context, username string, add, remove []string) (Account, error) {
	for _, g := range add {
		if _, ok := a.groups[g]; !ok {
			return Account{}, fmt.Errorf("there is no group %q; groups are defined in the deployment's values", g)
		}
	}
	return a.edit(ctx, username, func(u *Account) error {
		for _, g := range remove {
			u.Groups = slices.DeleteFunc(u.Groups, func(x string) bool { return x == g })
		}
		for _, g := range add {
			if !slices.Contains(u.Groups, g) {
				u.Groups = append(u.Groups, g)
			}
		}
		sort.Strings(u.Groups)
		return nil
	})
}

// SetSuper gives a local user exactly these super roles (RB-05, RB-51).
func (a *Accounts) SetSuper(ctx context.Context, username string, set []roles.ID) (Account, error) {
	var clean []roles.ID
	for _, r := range set {
		if !r.Super() {
			return Account{}, fmt.Errorf("%q is not a role that can be given here", r)
		}
		if !slices.Contains(clean, r) {
			clean = append(clean, r)
		}
	}
	slices.SortFunc(clean, func(x, y roles.ID) int { return slices.Index(roles.All, x) - slices.Index(roles.All, y) })
	return a.edit(ctx, username, func(u *Account) error {
		u.Super = clean
		return nil
	})
}

// SignedIn notes a sign-in, in memory: it is saved with the next change,
// since a Secret should not be written at every sign-in.
func (a *Accounts) SignedIn(username string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if u := a.get(username); u != nil {
		u.LastSignIn = at
	}
}
