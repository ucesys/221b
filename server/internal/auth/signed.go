package auth

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// A session made while the session store is away (DB-65, ADR-165) is kept
// in its cookie: who the person is, signed by the console, so any replica
// can check it with the key and the clock alone.
//
// The cookie's value is
//
//	s1.<payload>.<mac>
//
// where the payload is a JSON object, deflated, in unpadded URL-safe
// base64, and the MAC is HMAC-SHA-256, with the session key, over the
// text before the second dot ("s1." and the payload as it is written), in
// the same base64: 43 characters. A session's ID has no dot, so neither
// kind of value can be read as the other. The MAC is checked before the
// payload is opened, so nothing a stranger sends is inflated or parsed.

const (
	// signedPrefix names the format; another format takes another prefix.
	signedPrefix = "s1."
	// maxSigned is the longest value a signed session's cookie may have:
	// 3.5 KiB. A browser keeps about 4 KiB a cookie, its name and its
	// attributes counted, and drops a longer one without a word.
	maxSigned = 3584
	// maxPayload bounds what a payload inflates to.
	maxPayload = 64 << 10
	// keyReread is how often, at most, the key's Secret is read again:
	// when a signature does not verify, and before signing.
	keyReread = 30 * time.Second
	keyTries  = 5

	// SessionKeySecretSuffix ends the name of the session key's Secret,
	// after the release's: <release>-session-key. SessionKeyData is the
	// key in it: 32 random bytes as 64 hexadecimal characters.
	SessionKeySecretSuffix = "-session-key"
	SessionKeyData         = "key"
)

// ErrSessionTooLarge says a session made while the store is away would not
// fit a cookie: the person is in too many groups.
var ErrSessionTooLarge = errors.New("the session does not fit a cookie")

// errNoKey says there is no session key to sign or check with.
var errNoKey = errors.New("there is no session key")

// signedPayload is what a signed session's cookie carries: who the person
// is, as the source of the sign-in gave it. What the console works out
// from that (the roles, the names the clusters see) is not in it: it is
// worked out again on every request, from the configuration and the
// account as they are then.
type signedPayload struct {
	// Sid tells one signed session from another, to end one on a replica.
	Sid string `json:"sid"`
	// Issued and Expires are Unix seconds.
	Issued   int64    `json:"iat"`
	Expires  int64    `json:"exp"`
	Subject  string   `json:"sub"`
	Username string   `json:"usr"`
	Email    string   `json:"eml,omitempty"`
	Name     string   `json:"nam,omitempty"`
	Groups   []string `json:"grp,omitempty"`
	Method   string   `json:"mth"`
	Ref      string   `json:"ref,omitempty"`
}

// signedShape says whether a cookie's value is to be read as a signed
// session and never as an ID.
func signedShape(v string) bool { return strings.HasPrefix(v, signedPrefix) }

func base64URL(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return s != ""
}

// splitSigned takes a signed session's value apart: what the MAC is over,
// the payload and the MAC. ok is false for anything this console could not
// have written, which then costs nothing more.
func splitSigned(v string) (signedText, payload, mac string, ok bool) {
	if len(v) > maxSigned || !signedShape(v) {
		return "", "", "", false
	}
	payload, mac, ok = strings.Cut(v[len(signedPrefix):], ".")
	if !ok || len(mac) != 43 || !base64URL(mac) || !base64URL(payload) {
		return "", "", "", false
	}
	return v[:len(signedPrefix)+len(payload)], payload, mac, true
}

func macOf(key []byte, signedText string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(signedText))
	return h.Sum(nil)
}

// verified says whether the MAC is the key's over the text, in constant
// time.
func verified(key []byte, signedText, mac string) bool {
	if len(key) == 0 {
		return false
	}
	// Strict: one MAC has one spelling.
	got, err := base64.RawURLEncoding.Strict().DecodeString(mac)
	if err != nil {
		return false
	}
	return hmac.Equal(got, macOf(key, signedText))
}

// sealSigned writes a signed session's value.
func sealSigned(key []byte, p signedPayload) (string, error) {
	if len(key) == 0 {
		return "", errNoKey
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	var z bytes.Buffer
	w, err := flate.NewWriter(&z, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(b); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	text := signedPrefix + base64.RawURLEncoding.EncodeToString(z.Bytes())
	v := text + "." + base64.RawURLEncoding.EncodeToString(macOf(key, text))
	if len(v) > maxSigned {
		return "", ErrSessionTooLarge
	}
	return v, nil
}

// openPayload reads a payload whose MAC has been checked.
func openPayload(payload string) (signedPayload, error) {
	var p signedPayload
	z, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return p, err
	}
	b, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(z)), maxPayload+1))
	if err != nil {
		return p, err
	}
	if len(b) > maxPayload {
		return p, errors.New("the payload is too large")
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	switch {
	case p.Sid == "" || p.Subject == "" || p.Username == "":
		return p, errors.New("the payload names no session or no person")
	case p.Issued <= 0 || p.Expires <= p.Issued:
		return p, errors.New("the payload's times are not a lifetime")
	case p.Method != string(ModeLocal) && p.Method != string(ModeLDAP) && p.Method != string(ModeOIDC):
		return p, errors.New("the payload names no way of signing in")
	}
	return p, nil
}

// session is the signed session a payload describes, as a replica holds
// it: its ID is the cookie's value.
func (p signedPayload) session(id string, now time.Time) *Session {
	at := time.Unix(p.Issued, 0).UTC()
	return &Session{
		ID:     id,
		User:   User{Subject: p.Subject, Username: p.Username, Email: p.Email, Name: p.Name, Groups: p.Groups},
		Method: p.Method, Ref: p.Ref,
		GroupsAt: at, Created: at, LastSeen: now, Expires: time.Unix(p.Expires, 0).UTC(),
		Signed: true, sid: p.Sid,
	}
}

// sign makes the signed session of a session that could not be stored.
func (s *Sessions) sign(sess Session) (*Session, error) {
	s.mu.Lock()
	keys, lasts := s.key, s.outage
	s.mu.Unlock()
	key := keys.fresh()
	if key == nil {
		return nil, errNoKey
	}
	sid := make([]byte, 16)
	if _, err := rand.Read(sid); err != nil {
		panic("no randomness: " + err.Error())
	}
	at := sess.Created.Unix()
	p := signedPayload{
		Sid: base64.RawURLEncoding.EncodeToString(sid), Issued: at, Expires: at + int64(lasts/time.Second),
		Subject: sess.User.Subject, Username: sess.User.Username, Email: sess.User.Email, Name: sess.User.Name, Groups: sess.User.Groups,
		Method: sess.Method, Ref: sess.Ref,
	}
	if p.Subject == "" {
		// A session started by the console's own code may name no subject;
		// a sign-in always does.
		p.Subject = p.Username
	}
	id, err := sealSigned(key, p)
	if err != nil {
		return nil, err
	}
	signed := p.session(id, sess.LastSeen)
	// What the sign-in worked out stands on the replica that signed; every
	// other replica works it out again (Service.live).
	signed.User.KubeName, signed.User.KubeGroups = sess.User.KubeName, sess.User.KubeGroups
	signed.Admin, signed.Roles, signed.Super = sess.Admin, sess.Roles, sess.Super
	return signed, nil
}

// findSigned checks a signed session's cookie: its shape, its MAC, its
// times, and whether this replica ended it. It reads no store. A value
// this console could not have written costs nothing; one whose MAC does
// not verify costs at most a read of the key's Secret, and that at most
// once every keyReread, so a key put back by hand reaches every replica
// and a stranger's guesses reach nothing.
func (s *Sessions) findSigned(id string) (string, *Session, int) {
	key := keyOf(id)
	signedText, payload, mac, ok := splitSigned(id)
	if !ok {
		return key, nil, gone
	}
	now := s.now()
	s.mu.Lock()
	keys := s.key
	// The key of the map is a hash of the whole value: what is here was
	// verified when it was put here.
	sess := s.byID[key]
	s.mu.Unlock()
	if sess == nil {
		if !verified(keys.current(), signedText, mac) && (!keys.reread() || !verified(keys.current(), signedText, mac)) {
			return key, nil, gone
		}
		p, err := openPayload(payload)
		if err != nil {
			s.log.Warn("a signed session this console signed cannot be read", "err", err)
			return key, nil, gone
		}
		if !now.Before(time.Unix(p.Expires, 0)) {
			return key, nil, gone
		}
		s.mu.Lock()
		if _, ended := s.revoked[p.Sid]; ended {
			s.mu.Unlock()
			return key, nil, gone
		}
		if cur := s.byID[key]; cur != nil {
			sess = cur
		} else {
			sess = p.session(id, now)
			s.byID[key] = sess
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	sid, expires := sess.sid, sess.Expires
	ex, exchanged := s.exchanged[sid]
	s.mu.Unlock()
	if !now.Before(expires) {
		s.forget(key, false)
		return key, nil, gone
	}
	if exchanged {
		// A stored session was started in its place here: when that one has
		// ended, so has this.
		if _, _, state := s.find(ex.id); state == gone {
			s.forget(key, true)
			return key, nil, gone
		}
	}
	return key, sess, here
}

// Exchange starts a stored session in place of a signed one, once the
// shared store can be written again: for the same person, signed in the
// same way at the same time. cur is the signed session as it stands now,
// with what the person may do worked out. It returns the stored session,
// whose ID goes in the cookie; false leaves the signed session standing
// until it expires, and the exchange is tried again later. The signed
// session is not ended by it: no other replica could be told, and what
// was opened with it (a stream, a terminal) goes on until it expires.
func (s *Sessions) Exchange(cur Session) (*Session, bool) {
	if !cur.Signed || cur.sid == "" {
		return nil, false
	}
	s.exMu.Lock()
	defer s.exMu.Unlock()
	now := s.now()
	s.mu.Lock()
	shared := s.shared
	ex, done := s.exchanged[cur.sid]
	wait := s.tryAfter[cur.sid]
	s.mu.Unlock()
	if shared == nil {
		return nil, false
	}
	if done {
		// Another request of the same browser got there first, or the
		// answer that carried the new cookie was lost.
		if _, st, ok := s.lookup(ex.id); ok {
			s.mu.Lock()
			cp := *st
			s.mu.Unlock()
			return &cp, true
		}
		return nil, false
	}
	if now.Before(wait) || s.away() {
		return nil, false
	}
	st := Session{
		ID: randomID(), User: cur.User, Admin: cur.Admin, Roles: cur.Roles, Super: cur.Super, Method: cur.Method, Ref: cur.Ref,
		GroupsAt: cur.GroupsAt, Created: cur.Created, LastSeen: cur.LastSeen, Expires: cur.Created.Add(s.max),
	}
	key := keyOf(st.ID)
	written, err := s.store(key, st, true)
	if err != nil || !written {
		// A store that is away is left alone for a while as it is (reach);
		// one that refuses is not asked on every request.
		if !StoreAway(err) {
			s.mu.Lock()
			s.tryAfter[cur.sid] = now.Add(exchangeEvery)
			s.mu.Unlock()
		}
		return nil, false
	}
	s.mu.Lock()
	s.byID[key] = &st
	s.exchanged[cur.sid] = exchange{id: st.ID, expires: cur.Expires}
	s.mu.Unlock()
	s.log.Info("a session made while the session store was away is now a stored one", "user", st.User.Username, "method", st.Method)
	cp := st
	return &cp, true
}

// KeyStore is where the session key is kept: one key of a Secret, written
// only when nothing was written since it was read (kube.SecretKey).
type KeyStore interface {
	// LoadKey reads the key with the Secret's version; found is false when
	// there is no Secret or no such key.
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	// SaveKeyIf writes the key only if the Secret is still at version.
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// SessionKey is the key that signs the sessions made while the session
// store is away (DB-65): 32 random bytes. With a KeyStore it is kept in a
// Secret every replica reads, <release>-session-key, made at start when
// there is none and never changed by the console; without one it is this
// process's own, and a restart ends the sessions it signed. The key is
// never logged, shown, exported or put in a backup or the database, and
// nothing here returns it but to the code that signs and checks.
type SessionKey struct {
	store KeyStore
	now   func() time.Time

	mu  sync.Mutex
	key []byte
	// read is when the Secret was last read, or tried.
	read time.Time
}

// NewSessionKey makes the key's holder. With a store, call Ensure to read
// the key; with nil the key is made here, for this process only.
func NewSessionKey(store KeyStore, now func() time.Time) *SessionKey {
	if now == nil {
		now = time.Now
	}
	k := &SessionKey{store: store, now: now}
	if store == nil {
		k.key = randomKey()
	}
	return k
}

func randomKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("no randomness: " + err.Error())
	}
	return b
}

// parseSessionKey reads the key as its Secret holds it. Its error never
// carries what was read.
func parseSessionKey(b []byte) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("the session key's Secret does not hold 32 bytes as 64 hexadecimal characters; it is left as it is")
	}
	return key, nil
}

// Ensure reads the key from its Secret, making one when there is none. Two
// replicas starting at once both find none and both try to write theirs;
// the write is conditional, so the API server takes exactly one, and the
// other replica reads the winner's. A key that is there is never replaced,
// even one that cannot be used.
func (k *SessionKey) Ensure(ctx context.Context) error {
	if k.store == nil {
		return nil
	}
	k.mu.Lock()
	k.read = k.now()
	k.mu.Unlock()
	key, err := k.load(ctx)
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.key = key
	k.mu.Unlock()
	return nil
}

func (k *SessionKey) load(ctx context.Context) ([]byte, error) {
	var last error
	for range keyTries {
		b, version, found, err := k.store.LoadKey(ctx, SessionKeyData)
		if err != nil {
			return nil, fmt.Errorf("reading the session key: %w", err)
		}
		if found && len(b) > 0 {
			return parseSessionKey(b)
		}
		key := randomKey()
		if last = k.store.SaveKeyIf(ctx, SessionKeyData, []byte(hex.EncodeToString(key)), version); last == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("saving the session key: %w", last)
}

// current is the key as last read: nil when it has never been.
func (k *SessionKey) current() []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.key
}

// reread reads the key's Secret again, unless it was read within
// keyReread, and reports whether the key changed. A read that fails
// leaves the key as it was.
func (k *SessionKey) reread() bool {
	if k.store == nil {
		return false
	}
	k.mu.Lock()
	if now := k.now(); !k.read.IsZero() && now.Sub(k.read) < keyReread {
		k.mu.Unlock()
		return false
	} else {
		k.read = now
	}
	old := k.key
	k.mu.Unlock()
	// Short: the API server may be what is away, and a sign-in waits here.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key, err := k.load(ctx)
	if err != nil || bytes.Equal(key, old) {
		return false
	}
	k.mu.Lock()
	k.key = key
	k.mu.Unlock()
	return true
}

// fresh is the key to sign with: read again first when it has not been
// for keyReread, so a replica does not go on signing with a key that was
// replaced.
func (k *SessionKey) fresh() []byte {
	k.reread()
	return k.current()
}
