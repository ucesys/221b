package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
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

// SharedStore keeps session records where every replica of the console
// sees them, by key: a hash of the session's ID, never the ID itself
// (P7-02). The Kubernetes one is kube.SessionStore.
type SharedStore interface {
	// Get returns a record; ok is false when there is none.
	Get(ctx context.Context, key string) (b []byte, ok bool, err error)
	// Put creates a record, or replaces the stored one when replace, given
	// it as it is now, says so; it reports whether it wrote.
	Put(ctx context.Context, key string, b []byte, replace func(current []byte) bool) (bool, error)
	// Delete removes a record; one already gone is not an error.
	Delete(ctx context.Context, key string) error
	// List returns the records this replica knows of, by key.
	List() map[string][]byte
}

// seenEvery is how often a session's last activity is written back to the
// shared store: often enough for the idle limit, hours long, and rare
// enough that browsing writes almost nothing.
const seenEvery = 5 * time.Minute

// Sessions keeps the signed-in browsers. With a shared store every replica
// sees every session and a restart or an upgrade signs no one out; without
// one (a laptop, the simulator) they are kept in memory, and a restart
// signs everyone out.
type Sessions struct {
	idle, max time.Duration
	now       func() time.Time
	mu        sync.Mutex
	// byID holds the sessions by key (keyOf their ID): all of them in
	// memory, or the ones this replica has used with a shared store.
	byID   map[string]*Session
	shared SharedStore
	// written is when each session's last activity was last stored.
	written map[string]time.Time
	// extra keeps the fields of a stored record this version does not
	// know, so writing it back keeps them (P7-03).
	extra map[string]map[string]json.RawMessage
	// newer marks records a newer console wrote: read, never written back.
	newer map[string]bool
	log   *slog.Logger
}

// NewSessions makes a store with the idle and maximum lifetimes, in memory.
func NewSessions(idle, max time.Duration, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{idle: idle, max: max, now: now, byID: map[string]*Session{}, written: map[string]time.Time{}, extra: map[string]map[string]json.RawMessage{}, newer: map[string]bool{}, log: slog.Default()}
}

// Share keeps the sessions in a store every replica sees.
func (s *Sessions) Share(store SharedStore, log *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shared = store
	if log != nil {
		s.log = log
	}
}

// keyOf is the key a session is stored under: a hash of its ID, so the
// stored record cannot be used as a cookie.
func keyOf(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])
}

// randomID is 256 bits of randomness, URL-safe.
func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// plausibleID says whether a cookie could be an ID this console issued,
// so a made-up one costs no read of the shared store.
func plausibleID(id string) bool {
	if len(id) != 43 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// Create starts a session.
func (s *Sessions) Create(u User, admin bool, method string) *Session {
	return s.Start(Session{User: u, Admin: admin, Method: method})
}

// Start stores a session made by the caller, giving it an ID and its times.
func (s *Sessions) Start(sess Session) *Session {
	now := s.now()
	sess.ID, sess.Created, sess.LastSeen, sess.Expires = randomID(), now, now, now.Add(s.max)
	if sess.GroupsAt.IsZero() {
		sess.GroupsAt = now
	}
	key := keyOf(sess.ID)
	s.mu.Lock()
	s.byID[key] = &sess
	s.mu.Unlock()
	s.store(key, sess)
	return &sess
}

// store writes a session to the shared store, if there is one.
func (s *Sessions) store(key string, sess Session) {
	s.mu.Lock()
	shared := s.shared
	extra := s.extra[key]
	newer := s.newer[key]
	s.mu.Unlock()
	if shared == nil || newer {
		return
	}
	b, err := encodeSession(sess, extra)
	if err != nil {
		s.log.Warn("a session was not stored", "err", err)
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	// This replica's copy can lag the store: the record as stored now
	// decides whether a newer console wrote it since (P7-03).
	written, err := shared.Put(ctx, key, b, func(current []byte) bool {
		_, _, version, err := decodeSession(current)
		if err != nil || version <= sessionVersion {
			return true
		}
		s.mu.Lock()
		s.newer[key] = true
		s.mu.Unlock()
		return false
	})
	if err != nil {
		s.log.Warn("a session was not stored; the other replicas do not see it", "err", err)
		return
	}
	if !written {
		return
	}
	s.mu.Lock()
	s.written[key] = sess.LastSeen
	s.mu.Unlock()
}

// forget drops a session here and from the shared store.
func (s *Sessions) forget(key string) {
	s.mu.Lock()
	delete(s.byID, key)
	delete(s.written, key)
	delete(s.extra, key)
	delete(s.newer, key)
	shared := s.shared
	s.mu.Unlock()
	if shared == nil {
		return
	}
	ctx, cancel := timeout()
	defer cancel()
	if err := shared.Delete(ctx, key); err != nil {
		s.log.Warn("a session that ended was not removed from the shared store", "err", err)
	}
}

// expired says whether a session has outlived its idle or maximum time.
func (s *Sessions) expired(sess *Session, now time.Time) bool {
	return now.Sub(sess.LastSeen) > s.idle || now.After(sess.Expires)
}

// lookup finds a session by ID: in memory, or in the shared store, which
// is the truth when there is one (a session ended on another replica is
// gone here too). The local last activity is kept when it is newer.
func (s *Sessions) lookup(id string) (string, *Session, bool) {
	key := keyOf(id)
	s.mu.Lock()
	shared := s.shared
	local := s.byID[key]
	s.mu.Unlock()
	if shared == nil {
		if local == nil {
			return key, nil, false
		}
		return key, local, true
	}
	if !plausibleID(id) {
		return key, nil, false
	}
	ctx, cancel := timeout()
	b, ok, err := shared.Get(ctx, key)
	cancel()
	if err != nil {
		// The API server cannot be asked: a session this replica knows
		// stands until it can.
		s.log.Warn("cannot read the shared sessions; using this replica's", "err", err)
		return key, local, local != nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		delete(s.byID, key)
		delete(s.written, key)
		delete(s.extra, key)
		delete(s.newer, key)
		return key, nil, false
	}
	sess, extra, version, err := decodeSession(b)
	if err != nil {
		s.log.Warn("a stored session cannot be read", "err", err)
		return key, nil, false
	}
	sess.ID = id
	s.newer[key] = version > sessionVersion
	if local != nil && local.LastSeen.After(sess.LastSeen) {
		sess.LastSeen = local.LastSeen
	}
	if _, seen := s.written[key]; !seen {
		s.written[key] = sess.LastSeen
	}
	s.byID[key] = &sess
	s.extra[key] = extra
	return key, &sess, true
}

// Update changes a live session; it reports whether there was one. The
// change is stored at once, so every replica has it.
func (s *Sessions) Update(id string, fn func(*Session)) bool {
	key, sess, ok := s.lookup(id)
	if !ok {
		return false
	}
	s.mu.Lock()
	fn(sess)
	cp := *sess
	s.mu.Unlock()
	s.store(key, cp)
	return true
}

// all returns every session, from the shared store when there is one.
func (s *Sessions) all() map[string]Session {
	s.mu.Lock()
	shared := s.shared
	out := map[string]Session{}
	for k, v := range s.byID {
		out[k] = *v
	}
	s.mu.Unlock()
	if shared == nil {
		return out
	}
	stored := shared.List()
	for k := range out {
		if _, ok := stored[k]; !ok {
			delete(out, k)
		}
	}
	for k, b := range stored {
		sess, _, _, err := decodeSession(b)
		if err != nil {
			continue
		}
		if cur, ok := out[k]; ok && cur.LastSeen.After(sess.LastSeen) {
			sess.LastSeen = cur.LastSeen
		}
		sess.ID = out[k].ID
		out[k] = sess
	}
	return out
}

// Live returns copies of the sessions that have not expired. A session
// another replica started has no ID here.
func (s *Sessions) Live() []Session {
	now := s.now()
	var out []Session
	for _, sess := range s.all() {
		if !s.expired(&sess, now) {
			out = append(out, sess)
		}
	}
	return out
}

// DeleteWhere ends every session the test selects, on every replica, and
// says how many.
func (s *Sessions) DeleteWhere(match func(Session) bool) int {
	n := 0
	for key, sess := range s.all() {
		if match(sess) {
			s.forget(key)
			n++
		}
	}
	return n
}

// Get finds a live session and notes the activity; an expired one is
// dropped and not returned. The copy returned is the caller's.
func (s *Sessions) Get(id string) (Session, bool) {
	key, sess, ok := s.lookup(id)
	if !ok {
		return Session{}, false
	}
	now := s.now()
	s.mu.Lock()
	if s.expired(sess, now) {
		s.mu.Unlock()
		s.forget(key)
		return Session{}, false
	}
	sess.LastSeen = now
	cp := *sess
	due := s.shared != nil && now.Sub(s.written[key]) >= seenEvery
	s.mu.Unlock()
	if due {
		s.store(key, cp)
	}
	return cp, true
}

// Alive says whether a session exists and has not expired, without
// counting as activity.
func (s *Sessions) Alive(id string) bool {
	_, ok := s.peek(id)
	return ok
}

// peek is Get without counting as activity.
func (s *Sessions) peek(id string) (Session, bool) {
	_, sess, ok := s.lookup(id)
	if !ok {
		return Session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired(sess, s.now()) {
		return Session{}, false
	}
	return *sess, true
}

// Delete ends a session, on every replica.
func (s *Sessions) Delete(id string) { s.forget(keyOf(id)) }

// Sweep drops expired sessions; it is called now and then, on every
// replica. With a shared store a session is dropped once its stored last
// activity is older than the idle limit by more than the write-back
// interval, so a replica's unwritten activity never counts against it.
func (s *Sessions) Sweep() {
	now := s.now()
	s.mu.Lock()
	shared := s.shared
	s.mu.Unlock()
	for key, sess := range s.all() {
		late := now.Sub(sess.LastSeen) > s.idle
		if shared != nil {
			late = now.Sub(sess.LastSeen) > s.idle+seenEvery
		}
		if late || now.After(sess.Expires) {
			s.forget(key)
		}
	}
	if shared != nil {
		// Drop what this replica holds of sessions it no longer uses.
		stored := shared.List()
		s.mu.Lock()
		for key := range s.byID {
			if _, ok := stored[key]; !ok {
				delete(s.byID, key)
				delete(s.written, key)
				delete(s.extra, key)
				delete(s.newer, key)
			}
		}
		s.mu.Unlock()
	}
}

// Count is how many sessions are live.
func (s *Sessions) Count() int { return len(s.Live()) }

// IdleAfter is how long a session lives without activity.
func (s *Sessions) IdleAfter() time.Duration { return s.idle }

// sessionVersion is the format of a stored session (P7-03): a newer one is
// read as far as it goes and never written back.
const sessionVersion = 1

// sessionRecord is a session as it is stored. The ID is not: the record's
// key is a hash of it.
type sessionRecord struct {
	Version  int        `json:"version"`
	User     userRecord `json:"user"`
	Admin    bool       `json:"admin,omitempty"`
	Roles    []roles.ID `json:"roles,omitempty"`
	Super    []roles.ID `json:"super,omitempty"`
	Method   string     `json:"method"`
	Ref      string     `json:"ref,omitempty"`
	GroupsAt time.Time  `json:"groupsAt"`
	Created  time.Time  `json:"created"`
	LastSeen time.Time  `json:"lastSeen"`
	Expires  time.Time  `json:"expires"`
}

type userRecord struct {
	Subject    string   `json:"subject"`
	Username   string   `json:"username"`
	Email      string   `json:"email,omitempty"`
	Name       string   `json:"name,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	KubeName   string   `json:"kubeName,omitempty"`
	KubeGroups []string `json:"kubeGroups,omitempty"`
}

var sessionFields = []string{"version", "user", "admin", "roles", "super", "method", "ref", "groupsAt", "created", "lastSeen", "expires"}

func encodeSession(sess Session, extra map[string]json.RawMessage) ([]byte, error) {
	r := sessionRecord{
		Version: sessionVersion,
		User:    userRecord{Subject: sess.User.Subject, Username: sess.User.Username, Email: sess.User.Email, Name: sess.User.Name, Groups: sess.User.Groups, KubeName: sess.User.KubeName, KubeGroups: sess.User.KubeGroups},
		Admin:   sess.Admin, Roles: sess.Roles, Super: sess.Super, Method: sess.Method, Ref: sess.Ref,
		GroupsAt: sess.GroupsAt, Created: sess.Created, LastSeen: sess.LastSeen, Expires: sess.Expires,
	}
	return mergeKnown(r, extra)
}

// decodeSession reads a stored session, and returns the fields it does not
// know, so they can be written back, and the record's format. A record
// without a version is the format before versions, 0, which needs nothing
// more to be read as 1.
func decodeSession(b []byte) (Session, map[string]json.RawMessage, int, error) {
	var r sessionRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return Session{}, nil, 0, err
	}
	extra, err := unknownFields(b, sessionFields)
	if err != nil {
		return Session{}, nil, 0, err
	}
	u := r.User
	return Session{
		User:  User{Subject: u.Subject, Username: u.Username, Email: u.Email, Name: u.Name, Groups: u.Groups, KubeName: u.KubeName, KubeGroups: u.KubeGroups},
		Admin: r.Admin, Roles: r.Roles, Super: r.Super, Method: r.Method, Ref: r.Ref,
		GroupsAt: r.GroupsAt, Created: r.Created, LastSeen: r.LastSeen, Expires: r.Expires,
	}, extra, r.Version, nil
}

// unknownFields returns the top-level fields of a JSON object other than
// the known ones.
func unknownFields(b []byte, known []string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// mergeKnown marshals v and adds the fields of extra it does not set, so a
// record written by a newer console keeps what this one does not know.
func mergeKnown(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for k, raw := range extra {
		if _, ok := out[k]; !ok {
			out[k] = raw
		}
	}
	return json.Marshal(out)
}
