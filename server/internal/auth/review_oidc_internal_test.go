package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// rotatingIdP is an OIDC provider whose signing key can be replaced under
// the same kid, or with none.
type rotatingIdP struct {
	mu  sync.Mutex
	key *rsa.PrivateKey
	kid string
	srv *httptest.Server
	// discoveries counts the reads of the configuration.
	discoveries int
}

func newRotatingIdP(t *testing.T) *rotatingIdP {
	t.Helper()
	p := &rotatingIdP{kid: "k1"}
	p.rotate(t)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		b64 := base64.RawURLEncoding.EncodeToString
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			p.discoveries++
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/auth", "token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/keys"})
		case "/keys":
			k := map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "n": b64(p.key.PublicKey.N.Bytes()), "e": b64(big.NewInt(int64(p.key.PublicKey.E)).Bytes())}
			if p.kid != "" {
				k["kid"] = p.kid
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{k}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *rotatingIdP) rotate(t *testing.T) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.key = k
	p.mu.Unlock()
}

func (p *rotatingIdP) token(t *testing.T, now time.Time) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	b64 := base64.RawURLEncoding.EncodeToString
	h := map[string]string{"alg": "RS256", "typ": "JWT"}
	if p.kid != "" {
		h["kid"] = p.kid
	}
	hb, _ := json.Marshal(h)
	pb, _ := json.Marshal(map[string]any{"iss": p.srv.URL, "aud": "c", "sub": "ann", "email": "ann@corp.example", "email_verified": true, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "n"})
	signed := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(sig)
}

// Cut by the review's cap (oidc.go:237): a key rotated under the same kid,
// or with no kid, has the keys read again, as a new kid does.
func TestARotatedKeyWithTheSameKidOrNoneIsFetched(t *testing.T) {
	for _, kid := range []string{"k1", ""} {
		idp := newRotatingIdP(t)
		idp.kid = kid
		now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
		p := NewProvider(OIDCConfig{Issuer: idp.srv.URL, ClientID: "c"}, nil, func() time.Time { return now })
		if _, err := p.Verify(context.Background(), idp.token(t, now), "n"); err != nil {
			t.Fatalf("kid %q, before the rotation: %v", kid, err)
		}
		idp.rotate(t)
		now = now.Add(2 * time.Minute)
		if _, err := p.Verify(context.Background(), idp.token(t, now), "n"); err != nil {
			t.Errorf("kid %q, after the rotation: %v", kid, err)
		}
	}
}
