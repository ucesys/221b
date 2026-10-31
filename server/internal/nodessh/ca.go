// Package nodessh pairs cluster nodes for SSH and opens SSH sessions to
// them (Phase 11, ADR-72). The console holds one SSH certificate
// authority; pairing a node installs the authority's public key and a
// principals file on it through a node command, and every session runs
// with a short-lived certificate the authority signs for the person.
package nodessh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Principal is the name every certificate carries and every principals
// file admits: SSH sessions are for SuperAdmins (TB-32).
const Principal = "221b-superadmins"

// Keys of the CA's Secret.
const (
	keyPrivate = "ca.key"
	keyPublic  = "ca.pub"
)

// KeyStore keeps the authority's key pair: a Secret of the console's
// namespace, or memory on a laptop.
type KeyStore interface {
	Load(ctx context.Context) (map[string][]byte, error)
	Save(ctx context.Context, data map[string][]byte) error
}

// MemoryKeys keeps the key pair until the server restarts.
type MemoryKeys struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (m *MemoryKeys) Load(context.Context) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range m.m {
		out[k] = append([]byte(nil), v...)
	}
	return out, nil
}

func (m *MemoryKeys) Save(_ context.Context, data map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m = map[string][]byte{}
	for k, v := range data {
		m.m[k] = append([]byte(nil), v...)
	}
	return nil
}

// CA is the console's SSH certificate authority: one ed25519 key, made on
// first use and kept in the store.
type CA struct {
	store KeyStore
	// busy holds one reader of the store; waiting for it follows the
	// caller's context, so a stalled read holds up nobody for longer.
	busy   chan struct{}
	mu     sync.Mutex
	signer ssh.Signer
}

func NewCA(store KeyStore) *CA { return &CA{store: store, busy: make(chan struct{}, 1)} }

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

func (c *CA) use(s ssh.Signer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signer = s
}

func (c *CA) load(ctx context.Context, makeOne bool) error {
	if c.loaded() {
		return nil
	}
	select {
	case c.busy <- struct{}{}:
		defer func() { <-c.busy }()
	case <-ctx.Done():
		return fmt.Errorf("reading the SSH authority's key: %w", ctx.Err())
	}
	if c.loaded() {
		return nil
	}
	data, err := c.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("reading the SSH authority's key: %w", err)
	}
	if pem := data[keyPrivate]; len(pem) > 0 {
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			return fmt.Errorf("the saved SSH authority key is not readable: %w", err)
		}
		c.use(signer)
		return nil
	}
	if !makeOne {
		return nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "221B SSH CA")
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string][]byte{}
	}
	data[keyPrivate] = pemBytes(block.Type, block.Bytes)
	data[keyPublic] = []byte(authorizedKey(signer.PublicKey()))
	if err := c.store.Save(ctx, data); err != nil {
		return fmt.Errorf("saving the SSH authority's key: %w", err)
	}
	c.use(signer)
	return nil
}

func pemBytes(typ string, der []byte) []byte {
	return []byte("-----BEGIN " + typ + "-----\n" + wrap64(der) + "-----END " + typ + "-----\n")
}

func wrap64(b []byte) string {
	const line = 64
	enc := base64Std(b)
	var out strings.Builder
	for len(enc) > line {
		out.WriteString(enc[:line])
		out.WriteByte('\n')
		enc = enc[line:]
	}
	out.WriteString(enc)
	out.WriteByte('\n')
	return out.String()
}

// authorizedKey is a public key as authorized_keys and TrustedUserCAKeys
// take it, with the authority's comment.
func authorizedKey(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " 221B SSH CA"
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
