package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/roles"
	"runtime"
	"sigs.k8s.io/yaml"
	"sync/atomic"
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

// A password check costs a good part of a CPU on purpose, so only so many
// run at once, and a queue of attempts stays short whatever arrives.
const hashQueue = 64

var (
	hashSlots   = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)/2))
	hashWaiting atomic.Int32
)

func hashingFull() bool { return hashWaiting.Load() >= hashQueue }

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

// validHash says whether a hash has the form HashPassword makes, with
// settings CheckPassword takes.
func validHash(hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != hashPrefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 10_000_000 {
		return false
	}
	salt, err1 := base64.RawURLEncoding.DecodeString(parts[2])
	key, err2 := base64.RawURLEncoding.DecodeString(parts[3])
	return err1 == nil && err2 == nil && len(salt) > 0 && len(key) > 0
}

// CheckPassword reports whether the password matches the hash, in constant
// time with respect to the hash.
func CheckPassword(hash, password string) bool {
	hashWaiting.Add(1)
	defer hashWaiting.Add(-1)
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
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

// localUserRule says what a local user's name from values or a file may
// be (roles.ValidUsername, not strict): the user name, or the email when
// there is none, read in lowercase.
var localUserRule = fmt.Sprintf("the user name, or the email when there is none, may have letters (read in lowercase), digits, dot, dash, underscore, @ and +, at most %d characters, and may not be . or ..", roles.MaxEmailUsername)

// consoleUserRule says what the name of a user made in the console, on
// the first-run page or the Users page, may be (roles.ValidUsername,
// strict).
var consoleUserRule = fmt.Sprintf("lowercase letters, digits, dot, dash and underscore, at most %d characters (not . or .. alone)", roles.MaxUsername)

// localGroupRule says what a local group's name may be
// (roles.ValidGroupName).
var localGroupRule = fmt.Sprintf("a name of lowercase letters, digits, dot, dash and underscore, at most %d characters, not . or ..", roles.MaxUsername)

// ValidateLocalUsers checks the accounts a file or the chart provides.
func ValidateLocalUsers(users []LocalUser) error {
	seen := map[string]bool{}
	for i, u := range users {
		name := u.Login()
		if name == "" {
			return fmt.Errorf("local user %d: a username or an email address is needed", i+1)
		}
		if !roles.ValidUsername(name, false) {
			return fmt.Errorf("local user %s: %s", name, localUserRule)
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
// users, or an object with a users list and nothing else. A file of any
// other shape is an error, not "no accounts": with local sign-in that
// would open the first-run page to whoever comes first. No accounts is
// written as an empty list ([], users: [] or null, as the chart writes
// it); an empty file is an error too.
func ReadLocalUsers(path string) ([]LocalUser, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	list, err := parseLocalUsers(b)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := ValidateLocalUsers(list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

func parseLocalUsers(b []byte) ([]LocalUser, error) {
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	switch d := doc.(type) {
	case nil:
		if !hasContent(b) {
			return nil, errors.New("the file is empty; write [] for no accounts")
		}
		return nil, nil
	case []any:
		var list []LocalUser
		if err := yaml.Unmarshal(b, &list); err != nil {
			return nil, err
		}
		return list, nil
	case map[string]any:
		if _, ok := d["users"]; !ok || len(d) != 1 {
			return nil, errors.New("an object must hold a users list and nothing else; one account is a list of one, written with a leading \"- \"")
		}
		var obj struct {
			Users []LocalUser `json:"users"`
		}
		if err := yaml.Unmarshal(b, &obj); err != nil {
			return nil, err
		}
		return obj.Users, nil
	}
	return nil, errors.New("the file is not a list of accounts")
}

// hasContent says whether a YAML file holds anything but blank lines and
// comments.
func hasContent(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			return true
		}
	}
	return false
}

// limiterKeys is how many keys a limiter remembers.
const limiterKeys = 10000

// limiter slows password guessing: a few attempts a minute per address and
// per account, whatever the answer. Its memory is bounded whatever is sent:
// keys are kept as hashes, and past limiterKeys of them the ones attempted
// longest ago are forgotten.
type limiter struct {
	mu    sync.Mutex
	hits  map[[sha256.Size]byte][]time.Time
	limit int
	per   time.Duration
	keys  int
	now   func() time.Time
}

func newLimiter(limit int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{hits: map[[sha256.Size]byte][]time.Time{}, limit: limit, per: per, keys: limiterKeys, now: now}
}

// allow records an attempt for the key and says whether it may proceed.
func (l *limiter) allow(key string) bool {
	k := sha256.Sum256([]byte(key))
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var kept []time.Time
	for _, t := range l.hits[k] {
		if now.Sub(t) < l.per {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[k] = kept
		return false
	}
	l.hits[k] = append(kept, now)
	if len(l.hits) > l.keys {
		l.evict(now)
	}
	return true
}

// evict forgets the keys whose attempts have aged out and, when they are
// not enough, the oldest of the rest, down to nine tenths of the bound so
// that the next few attempts need no sweep. A key at its limit is
// forgotten last: filling the limiter with other keys must not let one it
// refuses start again. The caller holds the lock.
func (l *limiter) evict(now time.Time) {
	type last struct {
		key     [sha256.Size]byte
		at      time.Time
		refuses bool
	}
	var live []last
	for k, ts := range l.hits {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.per {
			delete(l.hits, k)
			continue
		}
		live = append(live, last{k, ts[len(ts)-1], len(ts) >= l.limit})
	}
	over := len(live) - (l.keys - l.keys/10)
	if over <= 0 {
		return
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].refuses != live[j].refuses {
			return !live[i].refuses
		}
		return live[i].at.Before(live[j].at)
	})
	for _, e := range live[:over] {
		delete(l.hits, e.key)
	}
}
