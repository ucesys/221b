package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/roles"
)

// User is who a session belongs to, as Kubernetes and the audit log name
// them: the username and groups are what impersonation sends.
type User struct {
	// Subject is the provider's stable identifier for the person.
	Subject string `json:"subject"`
	// Username is the name Kubernetes RBAC binds roles to: the email by
	// default.
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	// KubeName and KubeGroups are the user and groups the clusters know the
	// person as, with their prefixes and the reserved groups (RB-15, RB-05).
	// Impersonation and access reviews send these and nothing else (RB-37).
	KubeName   string   `json:"-"`
	KubeGroups []string `json:"-"`
}

// Session is one signed-in browser.
type Session struct {
	ID   string
	User User
	// Admin is true for a SuperAdmin: the console administrator of ADR-26
	// (RB-13).
	Admin bool
	// Roles are every role the person holds, from the most to the least
	// privileged; Super are the ones held in 221B (RB-05).
	Roles  []roles.ID
	Super  []roles.ID
	Method string // "oidc", "ldap" or "local"
	// Ref names the person in their directory, to read their groups again.
	Ref string
	// GroupsAt is when the groups were last read from the source (RB-23).
	GroupsAt time.Time
	Created  time.Time
	LastSeen time.Time
	// Expires is the latest the session may live, whatever the activity.
	Expires time.Time
}

// Sessions keeps the signed-in browsers in memory: a restart signs everyone
// out, which a sign-in puts right in seconds.
type Sessions struct {
	idle, max time.Duration
	now       func() time.Time
	mu        sync.Mutex
	byID      map[string]*Session
}

// NewSessions makes a store with the idle and maximum lifetimes.
func NewSessions(idle, max time.Duration, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{idle: idle, max: max, now: now, byID: map[string]*Session{}}
}

// randomID is 256 bits of randomness, URL-safe.
func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Create starts a session.
func (s *Sessions) Create(u User, admin bool, method string) *Session {
	now := s.now()
	sess := &Session{ID: randomID(), User: u, Admin: admin, Method: method, Created: now, LastSeen: now, Expires: now.Add(s.max)}
	s.mu.Lock()
	s.byID[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

// Start stores a session made by the caller, giving it an ID and its times.
func (s *Sessions) Start(sess Session) *Session {
	now := s.now()
	sess.ID, sess.Created, sess.LastSeen, sess.Expires = randomID(), now, now, now.Add(s.max)
	if sess.GroupsAt.IsZero() {
		sess.GroupsAt = now
	}
	s.mu.Lock()
	s.byID[sess.ID] = &sess
	s.mu.Unlock()
	return &sess
}

// Update changes a live session in place; it reports whether there was one.
func (s *Sessions) Update(id string, fn func(*Session)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if ok {
		fn(sess)
	}
	return ok
}

// Live returns copies of the sessions that have not expired.
func (s *Sessions) Live() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []Session
	for _, sess := range s.byID {
		if now.Sub(sess.LastSeen) <= s.idle && !now.After(sess.Expires) {
			out = append(out, *sess)
		}
	}
	return out
}

// DeleteWhere ends every session the test selects and says how many.
func (s *Sessions) DeleteWhere(match func(Session) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, sess := range s.byID {
		if match(*sess) {
			delete(s.byID, id)
			n++
		}
	}
	return n
}

// Get finds a live session and notes the activity; an expired one is
// dropped and not returned. The copy returned is the caller's.
func (s *Sessions) Get(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return Session{}, false
	}
	now := s.now()
	if now.Sub(sess.LastSeen) > s.idle || now.After(sess.Expires) {
		delete(s.byID, id)
		return Session{}, false
	}
	sess.LastSeen = now
	return *sess, true
}

// Alive says whether a session exists and has not expired, without
// counting as activity.
func (s *Sessions) Alive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return false
	}
	now := s.now()
	return now.Sub(sess.LastSeen) <= s.idle && !now.After(sess.Expires)
}

// Delete ends a session.
func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// Sweep drops expired sessions; it is called now and then.
func (s *Sessions) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, sess := range s.byID {
		if now.Sub(sess.LastSeen) > s.idle || now.After(sess.Expires) {
			delete(s.byID, id)
		}
	}
}

// Count is how many sessions are live.
func (s *Sessions) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// IdleAfter is how long a session lives without activity.
func (s *Sessions) IdleAfter() time.Duration { return s.idle }
