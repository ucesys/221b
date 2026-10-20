package sherlock

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

// KeyBackend keeps the Claude API key: a Secret in the console's
// namespace (X-04), or memory.
type KeyBackend interface {
	// Load returns the Secret's data, or ErrNoKey.
	Load(ctx context.Context) (map[string][]byte, error)
	Save(ctx context.Context, data map[string][]byte) error
	Delete(ctx context.Context) error
	// Kind is "secret" or "memory".
	Kind() string
}

// ErrNoKey is what a backend returns when no key was saved.
var ErrNoKey = errors.New("no Claude API key saved")

// KeyInfo says what is known about the key, never the key itself.
type KeyInfo struct {
	Set bool `json:"set"`
	// Source is "console" for a key an administrator saved, "chart" for
	// one the chart passes in, which the console cannot change.
	Source string     `json:"source,omitempty"`
	SetAt  *time.Time `json:"setAt,omitempty"`
	SetBy  string     `json:"setBy,omitempty"`
	// Store is where a saved key is kept: "secret" or "memory".
	Store string `json:"store"`
}

const (
	dataKey   = "apiKey"
	dataSetAt = "setAt"
	dataSetBy = "setBy"
)

// Keys holds the API key, read from its backend and kept in memory.
type Keys struct {
	backend KeyBackend
	fixed   string // from the chart
	now     func() time.Time

	mu    sync.Mutex
	key   string
	info  KeyInfo
	ready bool
}

// NewKeys returns the keys over a backend (memory when nil). A fixed key,
// from the chart, wins over a saved one and cannot be changed.
func NewKeys(b KeyBackend, fixed string, now func() time.Time) *Keys {
	if b == nil {
		b = &MemoryKey{}
	}
	if now == nil {
		now = time.Now
	}
	return &Keys{backend: b, fixed: strings.TrimSpace(fixed), now: now}
}

// Load reads the saved key.
func (k *Keys) Load(ctx context.Context) error {
	data, err := k.backend.Load(ctx)
	k.mu.Lock()
	defer k.mu.Unlock()
	if errors.Is(err, ErrNoKey) {
		k.key, k.info, k.ready = "", KeyInfo{}, true
		return nil
	}
	if err != nil {
		return err
	}
	k.key = strings.TrimSpace(string(data[dataKey]))
	k.info = KeyInfo{Set: k.key != "", Source: "console", SetBy: string(data[dataSetBy])}
	if t, err := time.Parse(time.RFC3339, string(data[dataSetAt])); err == nil {
		k.info.SetAt = &t
	}
	k.ready = true
	return nil
}

// Reload reads the saved key again, so a key set or removed through
// another replica takes effect here (P7-02). A failed read keeps the key
// in force.
func (k *Keys) Reload(ctx context.Context) error {
	if k.fixed != "" {
		return nil
	}
	return k.Load(ctx)
}

// Run reloads the key every interval until ctx is done.
func (k *Keys) Run(ctx context.Context, every time.Duration, onErr func(error)) {
	if every <= 0 || k.fixed != "" {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := k.Reload(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

// Key returns the key to send, or "" when there is none.
func (k *Keys) Key() string {
	if k.fixed != "" {
		return k.fixed
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.key
}

// Info says whether a key is set, and by whom.
func (k *Keys) Info() KeyInfo {
	if k.fixed != "" {
		return KeyInfo{Set: true, Source: "chart", Store: k.backend.Kind()}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	i := k.info
	i.Store = k.backend.Kind()
	return i
}

// Fixed says whether the chart sets the key.
func (k *Keys) Fixed() bool { return k.fixed != "" }

// ErrFixed is returned when the chart sets the key.
var ErrFixed = errors.New("the key is set by the chart (sherlock.existingSecret); change it there")

// Set saves a new API key.
func (k *Keys) Set(ctx context.Context, key, by string) error {
	return k.SetFor(ctx, AuthHeader, key, by)
}

// SetFor saves a new credential of the kind the provider's authentication
// takes: an API key, AWS credentials or a Google service account key.
func (k *Keys) SetFor(ctx context.Context, auth, key, by string) error {
	if k.fixed != "" {
		return ErrFixed
	}
	key = strings.TrimSpace(key)
	if err := ValidCredential(auth, key); err != nil {
		return err
	}
	now := k.now().UTC()
	data := map[string][]byte{dataKey: []byte(key), dataSetAt: []byte(now.Format(time.RFC3339)), dataSetBy: []byte(by)}
	if err := k.backend.Save(ctx, data); err != nil {
		return err
	}
	k.mu.Lock()
	k.key, k.info, k.ready = key, KeyInfo{Set: true, Source: "console", SetAt: &now, SetBy: by}, true
	k.mu.Unlock()
	return nil
}

// Delete removes the saved key.
func (k *Keys) Delete(ctx context.Context) error {
	if k.fixed != "" {
		return ErrFixed
	}
	if err := k.backend.Delete(ctx); err != nil && !errors.Is(err, ErrNoKey) {
		return err
	}
	k.mu.Lock()
	k.key, k.info = "", KeyInfo{}
	k.mu.Unlock()
	return nil
}

// ValidKey checks an API key's shape: printable, without spaces, of a
// sensible length. Whether the API accepts it shows at the first question.
func ValidKey(key string) error {
	if key == "" {
		return errors.New("paste the API key")
	}
	if len(key) < 8 || len(key) > 2048 {
		return errors.New("that does not look like an API key: it is too short or too long")
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return errors.New("that does not look like an API key: it has spaces or other characters a key does not have")
		}
	}
	return nil
}

// SecretsOf are the parts of a credential never to send anywhere: the key
// itself, and the secret fields of AWS credentials or a service account key.
func SecretsOf(key string) []string {
	out := []string{key}
	var m map[string]any
	if json.Unmarshal([]byte(key), &m) == nil {
		for _, f := range []string{"secretAccessKey", "sessionToken", "accessKeyId", "private_key", "private_key_id"} {
			if v, ok := m[f].(string); ok && v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

var reAccessKey = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)

// ValidCredential checks a credential of the kind an authentication takes.
func ValidCredential(auth, key string) error {
	switch auth {
	case AuthAWS:
		var k AWSCredentials
		if err := json.Unmarshal([]byte(key), &k); err != nil || !reAccessKey.MatchString(k.AccessKeyID) || len(k.SecretAccessKey) < 16 || len(k.SecretAccessKey) > 128 || len(k.SessionToken) > 4096 {
			return errors.New("that does not look like AWS credentials: give the access key ID and the secret access key")
		}
		return nil
	case AuthGoogle:
		if key == "" {
			return errors.New("paste the service account key file")
		}
		if len(key) > 16<<10 {
			return errors.New("that does not look like a service account key: it is too long")
		}
		_, _, err := ParseGoogleKey(key)
		return err
	}
	return ValidKey(key)
}

// MemoryKey keeps the key until the server restarts.
type MemoryKey struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *MemoryKey) Load(context.Context) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return nil, ErrNoKey
	}
	out := map[string][]byte{}
	for k, v := range m.data {
		out[k] = append([]byte(nil), v...)
	}
	return out, nil
}

func (m *MemoryKey) Save(_ context.Context, d map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = map[string][]byte{}
	for k, v := range d {
		m.data[k] = append([]byte(nil), v...)
	}
	return nil
}

func (m *MemoryKey) Delete(context.Context) error {
	m.mu.Lock()
	m.data = nil
	m.mu.Unlock()
	return nil
}

func (m *MemoryKey) Kind() string { return "memory" }
