package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ucesys/221b/server/internal/audit"
)

// Caller is who asks, as the API works it out from the session.
type Caller struct {
	ID    string
	Name  string
	Email string
	// SuperAdmin changes the sources, the store and the publisher.
	SuperAdmin bool
	// Reviewer approves or rejects Mycroft's drafts and decides the
	// publisher's conflicts: an Admin or a SuperAdmin.
	Reviewer bool
	// Reader browses the approved pages: anyone signed in to the console.
	Reader bool
	From   string
}

func (c Caller) label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

func (c Caller) who() audit.Who { return audit.Who{Subject: c.ID, Email: c.Email, Name: c.Name} }

// SecretStore keeps the credentials: one key each of the Secret
// <release>-knowledge, read and written with its version (kube.SecretKey).
type SecretStore interface {
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// MemorySecret keeps the credentials until the server restarts.
type MemorySecret struct {
	mu      sync.Mutex
	data    map[string][]byte
	version int
}

func (m *MemorySecret) LoadKey(_ context.Context, key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return nil, "", false, nil
	}
	b, ok := m.data[key]
	return append([]byte(nil), b...), fmt.Sprint(m.version), ok, nil
}

func (m *MemorySecret) SaveKeyIf(_ context.Context, key string, b []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.data == nil) != (version == "") || (m.data != nil && version != fmt.Sprint(m.version)) {
		return fmt.Errorf("the Secret changed since it was read")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte(nil), b...)
	m.version++
	return nil
}

// The Secret's keys.
const (
	storeKey = "store"
	hookKey  = "hook"
)

func sourceKey(id string) string { return "source-" + id }

// Creds are one key's credentials. They never reach a page, a log, a
// prompt or a backup.
type Creds struct {
	Token           string `json:"token,omitempty"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
}

func (c Creds) empty() bool { return c == Creds{} }

// CredsChange replaces what it sets: a value replaces, "" removes, nil
// keeps.
type CredsChange struct {
	Token           *string `json:"token,omitempty"`
	AccessKeyID     *string `json:"accessKeyId,omitempty"`
	SecretAccessKey *string `json:"secretAccessKey,omitempty"`
}

func (ch CredsChange) apply(c Creds) Creds {
	if ch.Token != nil {
		c.Token = strings.TrimSpace(*ch.Token)
	}
	if ch.AccessKeyID != nil {
		c.AccessKeyID = strings.TrimSpace(*ch.AccessKeyID)
	}
	if ch.SecretAccessKey != nil {
		c.SecretAccessKey = strings.TrimSpace(*ch.SecretAccessKey)
	}
	return c
}

func (ch CredsChange) none() bool {
	return ch.Token == nil && ch.AccessKeyID == nil && ch.SecretAccessKey == nil
}

// CredState is what a page learns of a key: whether each part is set.
type CredState struct {
	Token     bool `json:"token"`
	AccessKey bool `json:"accessKey"`
}

func (c Creds) state() CredState {
	return CredState{Token: c.Token != "", AccessKey: c.AccessKeyID != "" && c.SecretAccessKey != ""}
}

func (s *Service) creds(ctx context.Context, key string) (Creds, error) {
	var c Creds
	b, _, found, err := s.o.Secrets.LoadKey(ctx, key)
	if err != nil || !found || len(b) == 0 {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Creds{}, fmt.Errorf("the knowledge Secret's %s cannot be read: %w", key, err)
	}
	return c, nil
}

func (s *Service) saveCreds(ctx context.Context, key string, ch CredsChange) error {
	if ch.none() {
		return nil
	}
	for try := 0; try < 5; try++ {
		b, version, found, err := s.o.Secrets.LoadKey(ctx, key)
		if err != nil {
			return err
		}
		var c Creds
		if found && len(b) > 0 {
			_ = json.Unmarshal(b, &c)
		}
		c = ch.apply(c)
		raw := []byte{}
		if !c.empty() {
			raw, _ = json.Marshal(c)
		}
		if err := s.o.Secrets.SaveKeyIf(ctx, key, raw, version); err == nil {
			return nil
		} else if try == 4 {
			return fmt.Errorf("saving the credential failed: %w", err)
		}
	}
	return nil
}

func (s *Service) dropCreds(ctx context.Context, key string) error {
	empty := ""
	return s.saveCreds(ctx, key, CredsChange{Token: &empty, AccessKeyID: &empty, SecretAccessKey: &empty})
}

// secretValues are the credentials the service holds, for scrubbing
// whatever it writes: a token a source quotes back stays out.
func (s *Service) secretValues(ctx context.Context) []string {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil
	}
	keys := []string{storeKey, hookKey}
	for _, x := range set.Sources {
		keys = append(keys, sourceKey(x.ID))
	}
	var out []string
	for _, k := range keys {
		c, _ := s.creds(ctx, k)
		for _, v := range []string{c.Token, c.AccessKeyID, c.SecretAccessKey} {
			if v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}
