package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/db"
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
	// Signed says the session was made while the session store was away
	// (DB-65): it is kept in its cookie, which is its ID, and in the memory
	// of each replica that has checked it, never in the store. It ends at
	// Expires, half an hour after sign-in, and is not renewed.
	Signed bool
	// sid tells one signed session from another.
	sid string
}

// SharedStore keeps session records where every replica of the console
// sees them, by key: a hash of the session's ID, never the ID itself
// (P7-02). The Kubernetes one is kube.SessionStore.
type SharedStore interface {
	// Get returns a record; ok is false when there is none.
	Get(ctx context.Context, key string) (b []byte, ok bool, err error)
	// Put replaces the stored record when replace, given it as it is now,
	// says so, and creates one when there is none and replace, given nil,
	// says so; it reports whether it wrote.
	Put(ctx context.Context, key string, b []byte, replace func(current []byte) bool) (bool, error)
	// Delete removes a record; one already gone is not an error.
	Delete(ctx context.Context, key string) error
	// List returns every record, by key. An error says the list is not
	// known: nothing is taken to have ended because of it.
	List(ctx context.Context) (map[string][]byte, error)
}

// ErrStoreAway is what a call to the shared store returns while the store
// is known to be away and is left alone (see StoreAway).
var ErrStoreAway = errors.New("the session store is away")

// StoreAway says whether an error of the shared store means the store
// could not be asked, as opposed to the store refusing what was asked. It
// is the one place that decides it, and what it decides is all that makes
// a signed session (DB-65): a refusal never does. Away are:
//
//   - db.ErrUnavailable, which the database's calls return while it cannot
//     be reached;
//   - an error that says so itself, with a method StoreAway() bool: the
//     Secrets' store marks what the API server did not answer, or answered
//     with "not now" (kube.SessionStore);
//   - ErrStoreAway;
//   - a call that ran out of the time it was given.
func StoreAway(err error) bool {
	if err == nil {
		return false
	}
	var says interface{ StoreAway() bool }
	if errors.As(err, &says) && says.StoreAway() {
		return true
	}
	return errors.Is(err, ErrStoreAway) || errors.Is(err, db.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded)
}

// seenEvery is how often a session's last activity is written back to the
// shared store: often enough for the idle limit, hours long, and rare
// enough that browsing writes almost nothing.
const seenEvery = 5 * time.Minute

const (
	// awayFor is how long a shared store found away is left alone. A store
	// that swallows what it is sent answers each call only when the call's
	// time runs out; asked on every request it would hold every click that
	// long. After awayFor one call at a time tries it again.
	awayFor = 10 * time.Second
	// exchangeEvery is how long a signed session waits, after a stored
	// session could not be started in its place, before that is tried again.
	exchangeEvery = 30 * time.Second
	// maxEnded bounds the sessions remembered as ended while the store
	// could not be told.
	maxEnded = 10000
	// defaultOutage is how long a signed session lasts (DB-65).
	defaultOutage = 30 * time.Minute
)

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

	// key signs the sessions made while the shared store is away, and
	// outage is how long one lasts (DB-65).
	key    *SessionKey
	outage time.Duration
	// awayUntil is set while the shared store is known to be away: until
	// then it is not asked. probing says one call is trying it again.
	awayUntil time.Time
	probing   bool
	// ended are the keys of stored sessions ended here (a sign-out, an
	// account disabled) while the store could not be told: refused on this
	// replica, and deleted from the store when it can be.
	ended map[string]bool
	// revoked are the signed sessions ended here, by sid, with when each
	// would have expired: this replica refuses them until then.
	revoked map[string]time.Time
	// exchanged are the signed sessions a stored session was started for
	// here, by sid, and tryAfter when one that could not be exchanged may
	// be tried again.
	exchanged map[string]exchange
	tryAfter  map[string]time.Time
	// exMu makes the exchanges one at a time, so two requests of one
	// browser do not start two stored sessions.
	exMu sync.Mutex
}

// exchange is the stored session started in place of a signed one, kept
// until the signed one would have expired.
type exchange struct {
	id      string
	expires time.Time
}

// NewSessions makes a store with the idle and maximum lifetimes, in memory.
func NewSessions(idle, max time.Duration, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{idle: idle, max: max, now: now, byID: map[string]*Session{}, written: map[string]time.Time{}, extra: map[string]map[string]json.RawMessage{}, newer: map[string]bool{}, log: slog.Default(),
		key: NewSessionKey(nil, now), outage: defaultOutage, ended: map[string]bool{}, revoked: map[string]time.Time{}, exchanged: map[string]exchange{}, tryAfter: map[string]time.Time{}}
}

// Outage sets the key that signs the sessions made while the shared store
// is away, and how long one lasts; a nil key and a time of zero keep what
// there is: a key of this process only, and half an hour.
func (s *Sessions) Outage(key *SessionKey, lasts time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != nil {
		s.key = key
	}
	if lasts > 0 {
		s.outage = lasts
	}
}

// OutageLasts is how long a session made while the shared store is away
// lasts.
func (s *Sessions) OutageLasts() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outage
}

// reach asks the shared store, unless it is known to be away: then it
// returns ErrStoreAway at once. A call that finds the store away marks it
// so for awayFor; after that one call at a time tries it again.
func (s *Sessions) reach(fn func(ctx context.Context, store SharedStore) error) error {
	s.mu.Lock()
	shared := s.shared
	if shared == nil {
		s.mu.Unlock()
		return ErrStoreAway
	}
	probe := false
	if !s.awayUntil.IsZero() {
		if s.probing || s.now().Before(s.awayUntil) {
			s.mu.Unlock()
			return ErrStoreAway
		}
		s.probing, probe = true, true
	}
	s.mu.Unlock()
	ctx, cancel := timeout()
	err := fn(ctx, shared)
	cancel()
	s.mu.Lock()
	if probe {
		s.probing = false
	}
	was := !s.awayUntil.IsZero()
	switch {
	case StoreAway(err):
		s.awayUntil = s.now().Add(awayFor)
	case probe || err == nil:
		s.awayUntil = time.Time{}
	}
	is := !s.awayUntil.IsZero()
	s.mu.Unlock()
	switch {
	case is && !was:
		s.log.Warn("the session store is away: the sessions this replica holds stand, and a sign-in is kept in its cookie until the store is back", "err", err)
	case was && !is:
		s.log.Info("the session store is back")
	}
	return err
}

// list reads the shared store's list, unless the store is known to be
// away. A list that fails as away marks the store so; one that is read
// does not say the store is back, since a store may list from a copy it
// keeps (the Secrets' store does).
func (s *Sessions) list() (map[string][]byte, error) {
	if s.away() {
		return nil, ErrStoreAway
	}
	s.mu.Lock()
	shared := s.shared
	s.mu.Unlock()
	ctx, cancel := timeout()
	defer cancel()
	stored, err := shared.List(ctx)
	if StoreAway(err) {
		s.mu.Lock()
		was := !s.awayUntil.IsZero()
		s.awayUntil = s.now().Add(awayFor)
		s.mu.Unlock()
		if !was {
			s.log.Warn("the session store is away: the sessions this replica holds stand, and a sign-in is kept in its cookie until the store is back", "err", err)
		}
	}
	return stored, err
}

// away says whether the shared store is being left alone just now.
func (s *Sessions) away() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shared != nil && !s.awayUntil.IsZero() && (s.probing || s.now().Before(s.awayUntil))
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

// Start stores a session made by the caller, giving it an ID and its
// times, as Open does. It is for callers with no one to refuse: a session
// Open would refuse is kept in this replica's memory.
func (s *Sessions) Start(sess Session) *Session {
	started, err := s.open(sess, false)
	if err != nil {
		panic("starting a session: " + err.Error())
	}
	return started
}

// Open starts a session at sign-in, giving it an ID and its times. With a
// shared store that is away (StoreAway) the session is a signed one: its
// ID, which is the cookie's value, carries who the person is under the
// console's signature, so every replica can check it without the store
// (DB-65). ErrSessionTooLarge says such a session would not fit a cookie,
// and nothing was started. A store that refuses the write makes no signed
// session: the session is then this replica's only, as it always was.
func (s *Sessions) Open(sess Session) (*Session, error) {
	return s.open(sess, true)
}

func (s *Sessions) open(sess Session, refuse bool) (*Session, error) {
	now := s.now()
	sess.ID, sess.Created, sess.LastSeen, sess.Expires = randomID(), now, now, now.Add(s.max)
	sess.Signed, sess.sid = false, ""
	if sess.GroupsAt.IsZero() {
		sess.GroupsAt = now
	}
	key := keyOf(sess.ID)
	s.mu.Lock()
	s.byID[key] = &sess
	shared := s.shared
	s.mu.Unlock()
	if shared == nil {
		return &sess, nil
	}
	_, err := s.store(key, sess, true)
	if !StoreAway(err) {
		return &sess, nil
	}
	signed, err := s.sign(sess)
	switch {
	case errors.Is(err, ErrSessionTooLarge) && refuse:
		s.mu.Lock()
		delete(s.byID, key)
		s.mu.Unlock()
		return nil, err
	case err != nil:
		s.log.Warn("the session store is away and the session cannot be kept in its cookie; it is this replica's only", "err", err)
		return &sess, nil
	}
	s.mu.Lock()
	delete(s.byID, key)
	s.byID[keyOf(signed.ID)] = signed
	s.mu.Unlock()
	return signed, nil
}

// store writes a session to the shared store, if there is one and the
// session is a stored one. It reports whether it wrote, and the store's
// error.
func (s *Sessions) store(key string, sess Session, create bool) (bool, error) {
	s.mu.Lock()
	shared := s.shared
	extra := s.extra[key]
	newer := s.newer[key]
	s.mu.Unlock()
	if shared == nil || newer || sess.Signed {
		return false, nil
	}
	b, err := encodeSession(sess, extra)
	if err != nil {
		s.log.Warn("a session was not stored", "err", err)
		return false, err
	}
	// This replica's copy can lag the store: the record as stored now
	// decides whether a newer console wrote it since (P7-03).
	var written bool
	err = s.reach(func(ctx context.Context, shared SharedStore) error {
		var err error
		written, err = shared.Put(ctx, key, b, func(current []byte) bool {
			// Only signing in makes a record. A later write that finds none
			// comes after the session ended, and must not bring it back.
			if current == nil {
				return create
			}
			_, _, version, err := decodeSession(current)
			if err != nil || version <= sessionVersion {
				return true
			}
			s.mu.Lock()
			s.newer[key] = true
			s.mu.Unlock()
			return false
		})
		return err
	})
	if err != nil {
		// A store that is away is said once, when it leaves (reach).
		if !StoreAway(err) {
			s.log.Warn("a session was not stored; the other replicas do not see it", "err", err)
		}
		return false, err
	}
	if !written {
		return false, nil
	}
	s.mu.Lock()
	s.written[key] = sess.LastSeen
	s.mu.Unlock()
	return true, nil
}

// forget drops a session here and from the shared store. ended says a
// person or an administrator ended it, rather than its time running out:
// then a store that cannot be told is told later, and this replica refuses
// the session meanwhile. A signed session is in no store: this replica
// refuses it until it would have expired.
func (s *Sessions) forget(key string, ended bool) {
	s.mu.Lock()
	sess := s.byID[key]
	delete(s.byID, key)
	delete(s.written, key)
	delete(s.extra, key)
	delete(s.newer, key)
	shared := s.shared
	if sess != nil && sess.Signed {
		s.revoked[sess.sid] = sess.Expires
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if shared == nil {
		return
	}
	err := s.reach(func(ctx context.Context, shared SharedStore) error { return shared.Delete(ctx, key) })
	if err == nil {
		return
	}
	if !ended {
		if !StoreAway(err) {
			s.log.Warn("a session that ended was not removed from the shared store", "err", err)
		}
		return
	}
	s.mu.Lock()
	kept := len(s.ended) < maxEnded
	if kept {
		s.ended[key] = true
	}
	s.mu.Unlock()
	if kept {
		s.log.Warn("a session that was ended could not be removed from the shared store; this replica refuses it and removes it when the store can be reached", "err", err)
	} else {
		s.log.Warn("a session that was ended could not be removed from the shared store", "err", err)
	}
}

// expired says whether a session has outlived its idle or maximum time.
func (s *Sessions) expired(sess *Session, now time.Time) bool {
	return now.Sub(sess.LastSeen) > s.idle || now.After(sess.Expires)
}

// What find says of a session.
const (
	// gone: there is no such session.
	gone = iota
	// here: the session, as the shared store or this replica has it.
	here
	// unknown: the shared store could not be asked and this replica does
	// not hold the session, so nothing says whether it lives.
	unknown
)

// lookup finds a session by ID: in memory, or in the shared store, which
// is the truth when there is one (a session ended on another replica is
// gone here too). The local last activity is kept when it is newer.
func (s *Sessions) lookup(id string) (string, *Session, bool) {
	key, sess, state := s.find(id)
	return key, sess, state == here
}

// find is lookup with the third answer: unknown.
func (s *Sessions) find(id string) (string, *Session, int) {
	if signedShape(id) {
		return s.findSigned(id)
	}
	key := keyOf(id)
	s.mu.Lock()
	shared := s.shared
	local := s.byID[key]
	ended := s.ended[key]
	s.mu.Unlock()
	if shared == nil {
		if local == nil {
			return key, nil, gone
		}
		return key, local, here
	}
	if !plausibleID(id) || ended {
		return key, nil, gone
	}
	var b []byte
	var ok bool
	err := s.reach(func(ctx context.Context, shared SharedStore) error {
		var err error
		b, ok, err = shared.Get(ctx, key)
		return err
	})
	if err != nil {
		// The store cannot be asked: a session this replica knows stands
		// until it can. A store that is away is said once (reach).
		if !StoreAway(err) {
			s.log.Warn("cannot read the shared sessions; using this replica's", "err", err)
		}
		if local == nil {
			return key, nil, unknown
		}
		return key, local, here
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		delete(s.byID, key)
		delete(s.written, key)
		delete(s.extra, key)
		delete(s.newer, key)
		return key, nil, gone
	}
	sess, extra, version, err := decodeSession(b)
	if err != nil {
		s.log.Warn("a stored session cannot be read", "err", err)
		return key, nil, gone
	}
	sess.ID = id
	s.newer[key] = version > sessionVersion
	if s.written[key].Before(sess.LastSeen) {
		s.written[key] = sess.LastSeen
	}
	if local != nil && local.LastSeen.After(sess.LastSeen) {
		sess.LastSeen = local.LastSeen
	}
	s.byID[key] = &sess
	s.extra[key] = extra
	return key, &sess, here
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
	_, _ = s.store(key, cp, false)
	return true
}

// all returns every session: the shared store's when there is one, with
// the signed ones this replica holds, which are in no store. When the
// store's list cannot be had it returns the error with what this replica
// holds, all of it: a list that failed says of no session that it ended.
func (s *Sessions) all() (map[string]Session, error) {
	s.mu.Lock()
	shared := s.shared
	out := map[string]Session{}
	for k, v := range s.byID {
		out[k] = *v
	}
	ended := map[string]bool{}
	for k := range s.ended {
		ended[k] = true
	}
	s.mu.Unlock()
	if shared == nil {
		return out, nil
	}
	stored, err := s.list()
	if err != nil {
		return out, err
	}
	for k, v := range out {
		if _, ok := stored[k]; !ok && !v.Signed {
			delete(out, k)
		}
	}
	for k, b := range stored {
		if ended[k] {
			// Ended here; the store has not been told yet.
			delete(out, k)
			continue
		}
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
	return out, nil
}

// Live returns copies of the sessions that have not expired. A session
// another replica started has no ID here. While the shared store's list
// cannot be had these are the sessions this replica holds.
func (s *Sessions) Live() []Session {
	now := s.now()
	var out []Session
	all, _ := s.all()
	for _, sess := range all {
		if !s.expired(&sess, now) {
			out = append(out, sess)
		}
	}
	return out
}

// DeleteWhere ends every session the test selects, on every replica, and
// says how many. While the shared store's list cannot be had it ends the
// ones this replica holds, and no other: those it refuses from then on and
// removes from the store when the store can be reached. A signed session
// is ended on this replica only (DB-65).
func (s *Sessions) DeleteWhere(match func(Session) bool) int {
	n := 0
	all, _ := s.all()
	for key, sess := range all {
		if match(sess) {
			s.forget(key, true)
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
		s.forget(key, false)
		return Session{}, false
	}
	sess.LastSeen = now
	cp := *sess
	due := s.shared != nil && !cp.Signed && now.Sub(s.written[key]) >= seenEvery
	s.mu.Unlock()
	if due {
		_, _ = s.store(key, cp, false)
	}
	return cp, true
}

// Alive says whether a session exists and has not expired, without
// counting as activity. A session nothing can be said of, because the
// shared store cannot be asked and this replica does not hold it, is
// alive: what is kept for a session (a conversation with Sherlock) is not
// dropped for an outage.
func (s *Sessions) Alive(id string) bool {
	key, sess, state := s.find(id)
	if state == unknown {
		return true
	}
	_, ok := s.peekAt(key, sess, state == here)
	return ok
}

// peek is Get without counting as activity, and without dropping an expired
// session, which Sweep does. Activity noted here and not stored yet is
// written back once it is due: no later request may ever count, and the
// other replicas must not take this session for idle before it is.
func (s *Sessions) peek(id string) (Session, bool) {
	key, sess, ok := s.lookup(id)
	return s.peekAt(key, sess, ok)
}

func (s *Sessions) peekAt(key string, sess *Session, ok bool) (Session, bool) {
	if !ok {
		return Session{}, false
	}
	now := s.now()
	s.mu.Lock()
	if s.expired(sess, now) {
		s.mu.Unlock()
		return Session{}, false
	}
	cp := *sess
	due := s.shared != nil && !cp.Signed && cp.LastSeen.After(s.written[key]) && now.Sub(s.written[key]) >= seenEvery
	s.mu.Unlock()
	if due {
		_, _ = s.store(key, cp, false)
	}
	return cp, true
}

// Delete ends a session, on every replica. A signed session is ended on
// this replica, which refuses it from then on; no other replica can be
// told (DB-65).
func (s *Sessions) Delete(id string) {
	if signedShape(id) {
		// Checking it puts it in this replica's memory, where forget finds
		// which one it is.
		if key, _, state := s.findSigned(id); state == here {
			s.forget(key, true)
		}
		return
	}
	s.forget(keyOf(id), true)
}

// Sweep drops expired sessions; it is called now and then, on every
// replica. With a shared store a session is dropped once its stored last
// activity is older than the idle limit by more than the write-back
// interval, so a replica's unwritten activity never counts against it.
// While the store's list cannot be had nothing stored is dropped, here or
// there: this replica's memory is then what keeps people signed in.
func (s *Sessions) Sweep() {
	now := s.now()
	// What is in no store: the signed sessions and what is remembered of
	// them, all of which end by the clock.
	s.mu.Lock()
	shared := s.shared
	for key, sess := range s.byID {
		if sess.Signed && s.expired(sess, now) {
			delete(s.byID, key)
		}
	}
	for sid, until := range s.revoked {
		if now.After(until) {
			delete(s.revoked, sid)
		}
	}
	for sid, ex := range s.exchanged {
		if now.After(ex.expires) {
			delete(s.exchanged, sid)
			delete(s.tryAfter, sid)
		}
	}
	for sid, t := range s.tryAfter {
		if now.After(t) {
			delete(s.tryAfter, sid)
		}
	}
	var ended []string
	for key := range s.ended {
		ended = append(ended, key)
	}
	s.mu.Unlock()
	if shared != nil && s.away() {
		return
	}
	all, err := s.all()
	if err != nil {
		if !StoreAway(err) {
			s.log.Warn("the shared sessions cannot be listed; nothing is dropped", "err", err)
		}
		return
	}
	for key, sess := range all {
		if sess.Signed {
			continue
		}
		late := now.Sub(sess.LastSeen) > s.idle
		if shared != nil {
			late = now.Sub(sess.LastSeen) > s.idle+seenEvery
		}
		if late || now.After(sess.Expires) {
			s.forget(key, false)
		}
	}
	if shared == nil {
		return
	}
	// What was ended here while the store could not be told.
	for _, key := range ended {
		err := s.reach(func(ctx context.Context, shared SharedStore) error { return shared.Delete(ctx, key) })
		if err != nil {
			if StoreAway(err) {
				return
			}
			continue
		}
		s.mu.Lock()
		delete(s.ended, key)
		s.mu.Unlock()
	}
	// Drop what this replica holds of sessions it no longer uses.
	stored, err := s.list()
	if err != nil {
		return
	}
	s.mu.Lock()
	for key, sess := range s.byID {
		if _, ok := stored[key]; !ok && !sess.Signed {
			delete(s.byID, key)
			delete(s.written, key)
			delete(s.extra, key)
			delete(s.newer, key)
		}
	}
	s.mu.Unlock()
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
