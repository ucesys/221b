// Package nodessh pairs cluster nodes for SSH and opens SSH sessions to
// them (Phase 11, ADR-72). The console holds one SSH certificate
// authority; pairing a node adds the authority's public key and the
// console's principal to the files on it through a node command, and
// every session runs with a short-lived certificate the authority signs
// for the person. Several consoles may pair one node: each keeps its own
// line in those files.
package nodessh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/kube"
)

// oldPrincipal is what pairings made before consoles had names of their
// own recorded, and the start of a console's principal. SSH sessions are
// for SuperAdmins (TB-32).
const oldPrincipal = "221b-superadmins"

// laptopConsole names a console with no Secrets to be named after.
const laptopConsole = "laptop"

// ConsoleName names a console among those that may pair one node, from
// the namespace and the prefix of its Secrets. The dot keeps two consoles
// apart however the dashes fall: a namespace has none. Anything a Secret's
// name may not hold becomes a dash, since the name ends up in sshd's files.
func ConsoleName(namespace, prefix string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		}
		return '-'
	}, namespace+"."+prefix)
}

// PrincipalOf is the name a console's certificates carry and its lines in
// the principals files admit. Each console has its own, or one console's
// certificates could log in as the login user of another's.
func PrincipalOf(console string) string { return oldPrincipal + "-" + console }

// keyPrivate is the key of the CA's Secret.
const keyPrivate = "ca.key"

// caComment is the comment of the authority's public key.
const caComment = "221B SSH CA"

const (
	// keyWait bounds one read of the authority's key.
	keyWait = 15 * time.Second
	// keyTries is how often the first write is tried: a conflict is
	// another replica making the key, and the next read finds it.
	keyTries = 4
)

// CA is the console's SSH certificate authority: one ed25519 key, made on
// first use and kept in a Secret of the console's namespace, or, with no
// Secret on a laptop, in memory until the server restarts.
type CA struct {
	store *kube.SecretKey
	// busy holds one reader of the store; waiting for it follows the
	// caller's context, so a stalled read holds up nobody for longer.
	busy   chan struct{}
	mu     sync.Mutex
	signer ssh.Signer
	// pem is the key as kept, for a backup (ADR-137): what the Secret
	// holds, or, in memory, what was made.
	pem []byte
}

// NewCA returns the authority over its Secret; nil keeps the key in memory.
func NewCA(store *kube.SecretKey) *CA { return &CA{store: store, busy: make(chan struct{}, 1)} }

// Load reads the key pair, making one when there is none.
func (c *CA) Load(ctx context.Context) error { return c.load(ctx, true) }

// Peek reads the key pair if there is one, and makes none: looking at a
// menu must not create the authority (it is made on the first pairing).
func (c *CA) Peek(ctx context.Context) error { return c.load(ctx, false) }

func (c *CA) loaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signer != nil
}

func (c *CA) use(s ssh.Signer, pem []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signer, c.pem = s, pem
}

func (c *CA) load(ctx context.Context, makeOne bool) error {
	if c.loaded() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, keyWait)
	defer cancel()
	select {
	case c.busy <- struct{}{}:
		defer func() { <-c.busy }()
	case <-ctx.Done():
		return fmt.Errorf("reading the SSH authority's key: %w", ctx.Err())
	}
	if c.loaded() {
		return nil
	}
	if c.store == nil {
		if !makeOne {
			return nil
		}
		encoded, signer, err := newKey()
		if err != nil {
			return err
		}
		c.use(signer, encoded)
		return nil
	}
	// The first write is conditional, as the accounts' is: of two replicas
	// that find no key one makes it, and the other reads that one rather
	// than keep a signer the Secret does not hold.
	for try := 0; try < keyTries; try++ {
		saved, version, found, err := c.store.LoadKey(ctx, keyPrivate)
		if err != nil {
			return fmt.Errorf("reading the SSH authority's key: %w", err)
		}
		if found && len(saved) > 0 {
			signer, err := ssh.ParsePrivateKey(saved)
			if err != nil {
				return fmt.Errorf("the saved SSH authority key is not readable: %w", err)
			}
			c.use(signer, saved)
			return nil
		}
		if !makeOne {
			return nil
		}
		encoded, signer, err := newKey()
		if err != nil {
			return err
		}
		err = c.store.SaveKeyIf(ctx, keyPrivate, encoded, version)
		if errors.Is(err, kube.ErrConflict) {
			continue
		}
		if err != nil {
			return fmt.Errorf("saving the SSH authority's key: %w", err)
		}
		c.use(signer, encoded)
		return nil
	}
	return errors.New("saving the SSH authority's key: its Secret keeps changing")
}

// PrivateKey is the authority's private key as kept, PEM, for a backup
// that seals it (ADR-137); nil when there is no authority yet. It never
// reaches a log or a page.
func (c *CA) PrivateKey(ctx context.Context) ([]byte, error) {
	if err := c.Peek(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.pem...), nil
}

// ErrHasAuthority is a restore of an authority's key on a console that
// has one: it stays, since its pairings depend on it.
var ErrHasAuthority = errors.New("this console has an SSH authority of its own")

// Restore puts back an authority's private key from a backup, on a
// console with none yet: the pairings made with it work again.
func (c *CA) Restore(ctx context.Context, pem []byte) error {
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return errors.New("the backup's SSH authority key is not readable")
	}
	if err := c.Peek(ctx); err != nil {
		return err
	}
	if c.loaded() {
		return ErrHasAuthority
	}
	if c.store != nil {
		_, version, found, err := c.store.LoadKey(ctx, keyPrivate)
		if err != nil {
			return fmt.Errorf("reading the SSH authority's key: %w", err)
		}
		if found {
			return ErrHasAuthority
		}
		if err := c.store.SaveKeyIf(ctx, keyPrivate, pem, version); err != nil {
			if errors.Is(err, kube.ErrConflict) {
				return ErrHasAuthority
			}
			return fmt.Errorf("saving the SSH authority's key: %w", err)
		}
	}
	c.use(signer, append([]byte(nil), pem...))
	return nil
}

// newKey makes a key pair: the private key as PEM, and its signer.
func newKey() ([]byte, ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, caComment)
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(block), signer, nil
}

// authorizedKey is a public key as authorized_keys and TrustedUserCAKeys
// take it, with the authority's comment.
func authorizedKey(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + caComment
}

// PublicKey is the authority's public key, as TrustedUserCAKeys takes it.
func (c *CA) PublicKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.signer == nil {
		return ""
	}
	return authorizedKey(c.signer.PublicKey())
}

// Fingerprint is the authority's SHA256 fingerprint, as ssh-keygen -l
// prints it.
func (c *CA) Fingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.signer == nil {
		return ""
	}
	return ssh.FingerprintSHA256(c.signer.PublicKey())
}

// Sign makes a fresh key pair and a certificate for it: the principals,
// the key ID (who, and which session), valid from a minute ago for ttl.
func (c *CA) Sign(keyID string, principals []string, ttl time.Duration, now time.Time) (ssh.Signer, error) {
	c.mu.Lock()
	ca := c.signer
	c.mu.Unlock()
	if ca == nil {
		return nil, errors.New("the SSH authority has no key yet")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	cert := &ssh.Certificate{
		Key:             sshPub,
		KeyId:           keyID,
		ValidPrincipals: principals,
		ValidAfter:      uint64(now.Add(-time.Minute).Unix()),
		ValidBefore:     uint64(now.Add(ttl).Unix()),
		CertType:        ssh.UserCert,
		Permissions:     ssh.Permissions{Extensions: map[string]string{"permit-pty": "", "permit-user-rc": ""}},
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	return ssh.NewCertSigner(cert, signer)
}
