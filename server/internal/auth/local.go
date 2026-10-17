package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/roles"
	"sigs.k8s.io/yaml"
)

// LocalUser is an account defined in values or in a file: kept by the
// console's chart, never changed in the console (RB-46). The password is
// stored as a PBKDF2 hash, which 221b hash-password prints. Accounts made
// before roles existed name themselves by their email, which stays their
// user name.
type LocalUser struct {
	// Username is the name Kubernetes sees; it defaults to the email.
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	// Groups are names of local groups (RB-50).
	Groups []string `json:"groups,omitempty"`
	// Roles are super roles the account holds: superAdmin, superViewer.
	Roles        []string `json:"roles,omitempty"`
	PasswordHash string   `json:"passwordHash"`
}

// Login is the name the account signs in with, in lowercase.
func (u LocalUser) Login() string {
	n := u.Username
	if n == "" {
		n = u.Email
	}
	return strings.ToLower(strings.TrimSpace(n))
}

// Account turns the definition into an account.
func (u LocalUser) Account() Account {
	a := Account{Username: u.Login(), Name: u.Name, Email: strings.ToLower(strings.TrimSpace(u.Email)), PasswordHash: u.PasswordHash, Groups: append([]string(nil), u.Groups...), Managed: true}
	for _, r := range u.Roles {
		a.Super = append(a.Super, roles.ID(r))
	}
	return a
}

// The hash format: pbkdf2-sha256$<iterations>$<salt>$<hash>, base64 raw URL.
const (
	hashPrefix     = "pbkdf2-sha256"
	hashIterations = 600_000
	hashKeyLength  = 32
)

// HashPassword derives a hash to keep in place of a password.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPassword {
		return "", fmt.Errorf("use at least %d characters", MinPassword)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, hashKeyLength)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{hashPrefix, strconv.Itoa(hashIterations), base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(key)}, "$"), nil
}

// CheckPassword reports whether the password matches the hash, in constant
// time with respect to the hash.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != hashPrefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 10_000_000 {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ValidateLocalUsers checks the accounts a file or the chart provides.
func ValidateLocalUsers(users []LocalUser) error {
	seen := map[string]bool{}
	for i, u := range users {
		name := u.Login()
		if name == "" {
			return fmt.Errorf("local user %d: a username or an email address is needed", i+1)
		}
		if !roles.ValidUsername(name, false) {
			return fmt.Errorf("local user %s: use lowercase letters, digits, dot, dash, underscore and @", name)
		}
		if seen[name] {
			return fmt.Errorf("local user %s appears twice", name)
		}
		seen[name] = true
		if !strings.HasPrefix(u.PasswordHash, hashPrefix+"$") {
			return fmt.Errorf("local user %s: passwordHash is not a %s hash; make one with: 221b hash-password", name, hashPrefix)
		}
		for _, r := range u.Roles {
			if !roles.ID(r).Super() {
				return fmt.Errorf("local user %s: %q is not a role that can be given to a user; use superAdmin or superViewer", name, r)
			}
		}
	}
	return nil
}

// ReadLocalUsers reads the accounts from a JSON or YAML file: a list of
// users, or an object with a users list.
func ReadLocalUsers(path string) ([]LocalUser, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []LocalUser
	if err := yaml.Unmarshal(b, &list); err != nil {
		var obj struct {
			Users []LocalUser `json:"users"`
		}
		if err2 := yaml.Unmarshal(b, &obj); err2 != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		list = obj.Users
	}
	if err := ValidateLocalUsers(list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

// limiter slows password guessing: a few attempts a minute per address and
// per account, whatever the answer.
type limiter struct {
	mu    sync.Mutex
	hits  map[string][]time.Time
	limit int
	per   time.Duration
	now   func() time.Time
}

func newLimiter(limit int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{hits: map[string][]time.Time{}, limit: limit, per: per, now: now}
}

// allow records an attempt for the key and says whether it may proceed.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var kept []time.Time
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.per {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	if len(l.hits) > 10000 {
		// Forget the oldest keys rather than grow without bound.
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.per {
				delete(l.hits, k)
			}
		}
	}
	return true
}
