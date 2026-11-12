package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// A backup carries each secret sealed with the install's backup key
// (ADR-137), not in clear and not as a placeholder: AES-256-GCM, a random
// nonce per value, the key's fingerprint beside it and authenticated with
// it. A sealed value reads
//
//	<sealed:<fingerprint>:<base64 of the nonce and the sealed bytes>>
//
// and the file's header names the key it was sealed with, by fingerprint
// only. A restore on an install with the same key opens each value; on
// another install, or one without the key, it opens none, and every secret
// is listed to set again by hand, as a placeholder always was.
//
// The key is 256 random bits, kept as 64 hexadecimal characters under
// KeyData in the Secret <release>-backup-key of the console's namespace.
// The console makes it at start when there is none, and never changes or
// deletes it; the console never shows it or sends it anywhere either: a
// SuperAdmin keeps a copy from the Secret itself.
const (
	// KeySecretSuffix ends the name of the key's Secret, after the
	// release's: <release>-backup-key.
	KeySecretSuffix = "-backup-key"
	// KeyData is the key of the Secret that holds the backup key.
	KeyData = "key"
	// Cipher names how secrets are sealed, in the file's header.
	Cipher = "aes-256-gcm"
	// sealedPrefix starts a sealed value.
	sealedPrefix = "<sealed:"
	// keyTries is how often making the key is tried: a conflict is another
	// replica making it, and the next read finds that one.
	keyTries = 5
)

// ErrOtherKey is a sealed secret this install's key does not open: sealed
// with another key, or changed since.
var ErrOtherKey = errors.New("the secret is sealed with another key")

// SecretsKey seals and opens a backup's secrets. The key itself never
// leaves it: not in a log, an error, the audit log or a backup.
type SecretsKey struct {
	aead cipher.AEAD
	fp   string
}

// NewSecretsKey makes a key from its 32 bytes.
func NewSecretsKey(raw []byte) (*SecretsKey, error) {
	if len(raw) != 32 {
		return nil, errors.New("a backup key is 32 bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("221b-backup-key\x00"), raw...))
	return &SecretsKey{aead: aead, fp: hex.EncodeToString(sum[:16])}, nil
}

// ParseSecretsKey reads a key as the Secret keeps it: 64 hexadecimal
// characters. The error never quotes what it read.
func ParseSecretsKey(text []byte) (*SecretsKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(string(text)))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("the backup key is not 64 hexadecimal characters")
	}
	return NewSecretsKey(raw)
}

// NewKeyText makes a new random key as the Secret keeps it.
func NewKeyText() ([]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return []byte(hex.EncodeToString(raw)), nil
}

// Fingerprint names the key without giving it away: the first 16 bytes
// of a SHA-256 of it, in hexadecimal.
func (k *SecretsKey) Fingerprint() string { return k.fp }

// SealValue seals a secret; an empty one stays empty.
func (k *SecretsKey) SealValue(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := k.aead.Seal(nonce, nonce, []byte(plain), []byte(sealedPrefix+k.fp))
	return sealedPrefix + k.fp + ":" + base64.StdEncoding.EncodeToString(out) + ">", nil
}

// OpenValue opens a sealed secret: ErrOtherKey when this key did not seal
// it, or it was changed since.
func (k *SecretsKey) OpenValue(sealed string) (string, error) {
	fp, b64, ok := splitSealed(sealed)
	if !ok {
		return "", errors.New("the value is not a sealed secret")
	}
	if fp != k.fp {
		return "", ErrOtherKey
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	ns := k.aead.NonceSize()
	if err != nil || len(b) < ns+k.aead.Overhead() {
		return "", ErrOtherKey
	}
	out, err := k.aead.Open(nil, b[:ns], b[ns:], []byte(sealedPrefix+fp))
	if err != nil {
		return "", ErrOtherKey
	}
	return string(out), nil
}

// splitSealed reads a sealed value's fingerprint and sealed bytes.
func splitSealed(v string) (fp, b64 string, ok bool) {
	rest, ok := strings.CutPrefix(v, sealedPrefix)
	if !ok || !strings.HasSuffix(rest, ">") {
		return "", "", false
	}
	fp, b64, ok = strings.Cut(strings.TrimSuffix(rest, ">"), ":")
	return fp, b64, ok && fp != "" && b64 != ""
}

// SealedValue says whether a backup's value is a sealed secret.
func SealedValue(v string) bool {
	_, _, ok := splitSealed(v)
	return ok
}

// LoadSecretsKey reads the key from its Secret: nil when there is none.
func LoadSecretsKey(ctx context.Context, st SecretStore) (*SecretsKey, error) {
	b, _, found, err := st.LoadKey(ctx, KeyData)
	if err != nil {
		return nil, fmt.Errorf("reading the backup key: %w", err)
	}
	if !found || len(b) == 0 {
		return nil, nil
	}
	return ParseSecretsKey(b)
}

// EnsureSecretsKey reads the key from its Secret, making one when there
// is none (ADR-137). Two replicas starting at once both find none and
// both try to write theirs; the write is conditional (a create, or an
// update at the version read), so the Kubernetes API takes exactly one,
// and the other replica's write fails and it reads the winner's key. A key
// that is there is never replaced, even one that cannot be read: that
// would lose every backup sealed with it.
func EnsureSecretsKey(ctx context.Context, st SecretStore) (*SecretsKey, error) {
	var last error
	for try := 0; try < keyTries; try++ {
		b, version, found, err := st.LoadKey(ctx, KeyData)
		if err != nil {
			return nil, fmt.Errorf("reading the backup key: %w", err)
		}
		if found && len(b) > 0 {
			return ParseSecretsKey(b)
		}
		text, err := NewKeyText()
		if err != nil {
			return nil, err
		}
		if last = st.SaveKeyIf(ctx, KeyData, text, version); last == nil {
			return ParseSecretsKey(text)
		}
	}
	return nil, fmt.Errorf("saving the backup key: %w", last)
}

// KeySource gives the install's backup key: nil when the install keeps
// none (its Secrets are in memory), an error when it cannot be read.
type KeySource func(ctx context.Context) (*SecretsKey, error)

// Sealing is the file's header entry that says how its secrets are
// sealed: the cipher and the key's fingerprint, never the key.
type Sealing struct {
	Cipher string `json:"cipher"`
	Key    string `json:"key"`
}

// SecretToSet is a secret a restore did not bring back, to set again by
// hand: what it is and where it is set.
type SecretToSet struct {
	// Part is the label of the part it belongs to.
	Part string `json:"part"`
	// Name says which secret: "the token of the connection Prod".
	Name string `json:"name"`
	// Where is the page and field that take it: "Clusters › Prod › Edit
	// connection".
	Where string `json:"where"`
}

// Seal is what an export writes for a secret: sealed with the install's
// key, the placeholder when the install keeps no key, nothing for none.
// A value that cannot be sealed is written as the placeholder, never in
// clear.
func (e *Env) Seal(value string) string {
	if value == "" {
		return ""
	}
	if e.key == nil {
		return Placeholder
	}
	out, err := e.key.SealValue(value)
	if err != nil {
		return Placeholder
	}
	return out
}

// Sealing says whether an export seals its secrets, rather than write
// placeholders.
func (e *Env) Sealing() bool { return e.key != nil }

// Open is the secret a restore takes from a field of the file: the value
// when the file holds it sealed with this install's key. When the file
// holds the placeholder, or a value sealed with another key (or this
// install has no key), it is "" and the secret is listed to set again,
// named name and set at where. Nothing in the file is "" and listed
// nowhere. A value in clear is ErrSecretInFile: a file is no way around
// the pages that take secrets.
func (e *Env) Open(inFile, name, where string) (string, error) {
	switch {
	case inFile == "":
		return "", nil
	case inFile == Placeholder:
	case SealedValue(inFile):
		if e.key != nil {
			if v, err := e.key.OpenValue(inFile); err == nil {
				return v, nil
			}
		}
	default:
		return "", ErrSecretInFile
	}
	e.SetAgain(name, where)
	return "", nil
}

// SetAgain lists a secret a restore did not bring back, under the part
// being restored.
func (e *Env) SetAgain(name, where string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.secrets = append(e.secrets, SecretToSet{Part: e.part, Name: name, Where: where})
}

// ToSetAgain are the secrets listed so far to set again.
func (e *Env) ToSetAgain() []SecretToSet {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]SecretToSet(nil), e.secrets...)
}

// CheckSecret says whether a field of the file may stand where a secret
// is: nothing, the placeholder or a sealed value, not a value in clear.
func CheckSecret(inFile string) error {
	if inFile == "" || inFile == Placeholder || SealedValue(inFile) {
		return nil
	}
	return ErrSecretInFile
}

// KeyCheck is what a restore learns of a file's secrets before applying
// anything: whether they are sealed, and whether this install's key opens
// them.
type KeyCheck struct {
	// Sealed: the file's secrets are sealed (its header names a key).
	Sealed bool `json:"sealed"`
	// Same: this install's key is the one they were sealed with.
	Same bool `json:"same"`
	// File is the fingerprint of the key the file names.
	File string `json:"file,omitempty"`
	// Here is this install's key's fingerprint; "" when it keeps none.
	Here string `json:"here,omitempty"`
}
