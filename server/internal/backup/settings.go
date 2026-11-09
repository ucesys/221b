package backup

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/s3"
	"github.com/ucesys/221b/server/internal/schedule"
)

// The scheduled backups' settings and their runs are kept beside the
// console's settings (in its settings ConfigMap), under these keys; their
// keys and passphrase in a Secret of the console's namespace,
// <release>-backup, under SecretKey.
const (
	SettingsKey = "backup-s3.json"
	StatusKey   = "backup-s3-status.json"
	SecretKey   = "s3.json"
)

// Where the credentials come from.
const (
	// CredentialsKeys is an access key kept in the Secret.
	CredentialsKeys = "keys"
	// CredentialsIdentity is the pod's own: IRSA's web identity, EKS Pod
	// Identity, or keys in its environment.
	CredentialsIdentity = "identity"
)

// How old backups go.
const (
	KeepCount     = "count"
	KeepDays      = "days"
	KeepLifecycle = "lifecycle"
)

// S3Settings are Administration › Backup's scheduled backups to S3.
type S3Settings struct {
	Enabled bool `json:"enabled"`
	// Destination.
	Bucket string `json:"bucket"`
	// Prefix is put before each file's name; it ends with "/" when set.
	Prefix   string `json:"prefix,omitempty"`
	Region   string `json:"region,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// PathStyle puts the bucket in the path, as most S3-compatible stores
	// (MinIO) want.
	PathStyle bool `json:"pathStyle,omitempty"`
	// CAPEM is a private CA to trust for Endpoint, PEM.
	CAPEM string `json:"caPem,omitempty"`
	// Credentials is CredentialsKeys or CredentialsIdentity.
	Credentials string        `json:"credentials"`
	Schedule    schedule.Spec `json:"schedule"`
	Retention   Retention     `json:"retention"`
	// SSE is the server-side encryption asked for: "", s3.SSES3 or
	// s3.SSEKMS with KMSKeyID (empty: the account's default key).
	SSE      string `json:"sse,omitempty"`
	KMSKeyID string `json:"kmsKeyId,omitempty"`
	// Encrypt seals each file on the console with the Secret's
	// passphrase before it leaves (ADR-82).
	Encrypt bool `json:"encrypt,omitempty"`
	// Optional takes the optional parts too (chat conversations).
	Optional bool `json:"optional,omitempty"`
	// Since is when the schedule was last set: a schedule never run counts
	// its times from then. By is who set it.
	Since time.Time `json:"since,omitzero"`
	By    string    `json:"by,omitempty"`
}

// Retention says which backups the console deletes after each run:
// beyond the newest Count, or older than Days; with KeepLifecycle none,
// the bucket's lifecycle rules decide.
type Retention struct {
	Mode  string `json:"mode"`
	Count int    `json:"count,omitempty"`
	Days  int    `json:"days,omitempty"`
}

// DefaultSettings are the page's before anything is saved.
func DefaultSettings() S3Settings {
	return S3Settings{Prefix: "221b/", Region: "us-east-1", Credentials: CredentialsKeys, Schedule: schedule.Spec{Kind: schedule.Daily, At: "03:00", TimeZone: "UTC"}, Retention: Retention{Mode: KeepCount, Count: 14}, SSE: s3.SSES3}
}

var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Normalize trims what a person typed and ends the prefix with "/".
func (s S3Settings) Normalize() S3Settings {
	s.Bucket = strings.TrimSpace(s.Bucket)
	s.Region = strings.TrimSpace(s.Region)
	s.Endpoint = strings.TrimRight(strings.TrimSpace(s.Endpoint), "/")
	s.Prefix = strings.TrimLeft(strings.TrimSpace(s.Prefix), "/")
	if s.Prefix != "" && !strings.HasSuffix(s.Prefix, "/") {
		s.Prefix += "/"
	}
	s.KMSKeyID = strings.TrimSpace(s.KMSKeyID)
	s.CAPEM = strings.TrimSpace(s.CAPEM)
	if s.Region == "" {
		s.Region = "us-east-1"
	}
	if s.SSE != s3.SSEKMS {
		s.KMSKeyID = ""
	}
	return s
}

// Validate says what is wrong with settings, as the page shows it.
func (s S3Settings) Validate() error {
	if !bucketRE.MatchString(s.Bucket) || strings.Contains(s.Bucket, "..") {
		return errors.New("the bucket's name is 3 to 63 lowercase letters, digits, dots and dashes")
	}
	if len(s.Prefix) > 512 || strings.Contains(s.Prefix, "//") {
		return errors.New("the prefix is at most 512 characters, without an empty segment")
	}
	if s.Endpoint != "" {
		u, err := url.Parse(s.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return errors.New("the endpoint is an address like https://minio.example.com:9000, without a path")
		}
	}
	if s.CAPEM != "" {
		if len(s.CAPEM) > 12<<10 {
			return errors.New("the CA certificates are over 12 KiB")
		}
		rest, n := []byte(s.CAPEM), 0
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			if _, err := x509.ParseCertificate(block.Bytes); block.Type != "CERTIFICATE" || err != nil {
				return errors.New("the CA is not a PEM certificate")
			}
			n++
		}
		if n == 0 {
			return errors.New("the CA is not a PEM certificate")
		}
	}
	switch s.Credentials {
	case CredentialsKeys, CredentialsIdentity:
	default:
		return errors.New("the credentials are an access key, or the pod's identity")
	}
	if err := s.Schedule.Validate(); err != nil {
		return err
	}
	switch s.Retention.Mode {
	case KeepCount:
		if s.Retention.Count < 1 || s.Retention.Count > 1000 {
			return errors.New("keep 1 to 1,000 backups")
		}
	case KeepDays:
		if s.Retention.Days < 1 || s.Retention.Days > 3650 {
			return errors.New("keep backups 1 to 3,650 days")
		}
	case KeepLifecycle:
	default:
		return errors.New("retention is a number of backups, a number of days, or the bucket's lifecycle")
	}
	switch s.SSE {
	case "", s3.SSES3, s3.SSEKMS:
	default:
		return errors.New("server-side encryption is SSE-S3 or SSE-KMS")
	}
	if len(s.KMSKeyID) > 2048 {
		return errors.New("the KMS key is at most 2,048 characters")
	}
	return nil
}

// Keys are the Secret's contents. They never reach a page.
type Keys struct {
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	Passphrase      string `json:"passphrase,omitempty"`
}

// KeysChange replaces what it sets: a value replaces, "" removes, nil
// keeps.
type KeysChange struct {
	AccessKeyID     *string `json:"accessKeyId,omitempty"`
	SecretAccessKey *string `json:"secretAccessKey,omitempty"`
	Passphrase      *string `json:"passphrase,omitempty"`
}

func (c KeysChange) apply(k Keys) Keys {
	if c.AccessKeyID != nil {
		k.AccessKeyID = strings.TrimSpace(*c.AccessKeyID)
	}
	if c.SecretAccessKey != nil {
		k.SecretAccessKey = strings.TrimSpace(*c.SecretAccessKey)
	}
	if c.Passphrase != nil {
		k.Passphrase = *c.Passphrase
	}
	return k
}

// changed names what a change replaces, for the audit log.
func (c KeysChange) changed() []string {
	var out []string
	for _, x := range []struct {
		v    *string
		name string
	}{{c.AccessKeyID, "access key ID"}, {c.SecretAccessKey, "secret access key"}, {c.Passphrase, "passphrase"}} {
		switch {
		case x.v == nil:
		case *x.v == "":
			out = append(out, x.name+" removed")
		default:
			out = append(out, x.name+" replaced")
		}
	}
	return out
}

// KeysState is what a page learns of the Secret: whether each is set.
type KeysState struct {
	AccessKey  bool `json:"accessKey"`
	Passphrase bool `json:"passphrase"`
}

func (k Keys) state() KeysState {
	return KeysState{AccessKey: k.AccessKeyID != "" && k.SecretAccessKey != "", Passphrase: k.Passphrase != ""}
}

// check holds settings and keys together: what is on needs its key.
func check(s S3Settings, k Keys) error {
	if s.Enabled && s.Credentials == CredentialsKeys && (k.AccessKeyID == "" || k.SecretAccessKey == "") {
		return errors.New("an access key is needed: give its ID and secret, or use the pod's identity")
	}
	if (k.AccessKeyID == "") != (k.SecretAccessKey == "") {
		return errors.New("an access key needs both its ID and its secret")
	}
	if s.Encrypt && len(k.Passphrase) < MinPassphrase {
		return fmt.Errorf("encrypting the files needs a passphrase of %d characters or more", MinPassphrase)
	}
	if k.Passphrase != "" && len(k.Passphrase) < MinPassphrase {
		return fmt.Errorf("the passphrase is shorter than %d characters", MinPassphrase)
	}
	return nil
}

// SecretStore keeps the keys: one key of a Secret, read and written with
// its version (kube.SecretKey). found is false when there is none.
type SecretStore interface {
	LoadKey(ctx context.Context, key string) (b []byte, version string, found bool, err error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// MemorySecret keeps the keys until the server restarts.
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
		return errors.New("the Secret changed since it was read")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte(nil), b...)
	m.version++
	return nil
}

// loadKeys reads the Secret's keys.
func loadKeys(ctx context.Context, st SecretStore) (Keys, string, error) {
	var k Keys
	b, version, found, err := st.LoadKey(ctx, SecretKey)
	if err != nil || !found {
		return k, version, err
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return k, version, fmt.Errorf("the backup's Secret cannot be read: %w", err)
	}
	return k, version, nil
}
