package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// The signature algorithms accepted on an ID token (RFC 7518): RSA with
// PKCS #1 v1.5 or PSS padding, and ECDSA. HMAC is never accepted: a shared
// secret would let anyone with the client secret mint tokens.
var algorithms = map[string]struct {
	hash  crypto.Hash
	kind  string // "RSA", "PS" or "EC"
	curve elliptic.Curve
}{
	"RS256": {crypto.SHA256, "RSA", nil},
	"RS384": {crypto.SHA384, "RSA", nil},
	"RS512": {crypto.SHA512, "RSA", nil},
	"PS256": {crypto.SHA256, "PS", nil},
	"PS384": {crypto.SHA384, "PS", nil},
	"PS512": {crypto.SHA512, "PS", nil},
	"ES256": {crypto.SHA256, "EC", elliptic.P256()},
	"ES384": {crypto.SHA384, "EC", elliptic.P384()},
	"ES512": {crypto.SHA512, "EC", elliptic.P521()},
}

// JWK is one key of a JSON Web Key Set, as far as ID tokens need it.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	// RSA.
	N string `json:"n"`
	E string `json:"e"`
	// EC.
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JWKS is a key set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// publicKey turns a JWK into a Go public key.
func (k JWK) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("key %s: bad modulus: %w", k.Kid, err)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("key %s: bad exponent: %w", k.Kid, err)
		}
		if len(n) < 256 {
			return nil, fmt.Errorf("key %s: RSA key shorter than 2048 bits", k.Kid)
		}
		exp := new(big.Int).SetBytes(e)
		if !exp.IsInt64() || exp.Int64() < 3 || exp.Int64() > 1<<31 {
			return nil, fmt.Errorf("key %s: bad exponent", k.Kid)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("key %s: unsupported curve %q", k.Kid, k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("key %s: bad x: %w", k.Kid, err)
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("key %s: bad y: %w", k.Kid, err)
		}
		pub := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !curve.IsOnCurve(pub.X, pub.Y) {
			return nil, fmt.Errorf("key %s: point not on the curve", k.Kid)
		}
		return pub, nil
	}
	return nil, fmt.Errorf("key %s: unsupported key type %q", k.Kid, k.Kty)
}

// jwsHeader is the part of a JWS header that picks the key.
type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// ErrNoKey says the token names a key the set does not hold, which
// usually means the provider rotated its keys.
var ErrNoKey = errors.New("no key in the set signs this token")

// verifyJWS checks a compact JWS against the keys and returns its payload.
func verifyJWS(token string, keys []JWK) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a signed token: expected three parts")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("bad header: %w", err)
	}
	var h jwsHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, fmt.Errorf("bad header: %w", err)
	}
	alg, ok := algorithms[h.Alg]
	if !ok {
		return nil, fmt.Errorf("signature algorithm %q is not accepted", h.Alg)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("bad payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("bad signature: %w", err)
	}
	// The keys that may have signed it: by kid when the token names one,
	// otherwise every key of the right type (providers that publish one key
	// sometimes omit the kid).
	var candidates []JWK
	for _, k := range keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if k.Alg != "" && k.Alg != h.Alg {
			continue
		}
		if h.Kid != "" && k.Kid != h.Kid {
			continue
		}
		if (alg.kind == "EC") != (k.Kty == "EC") {
			continue
		}
		candidates = append(candidates, k)
	}
	if len(candidates) == 0 {
		return nil, ErrNoKey
	}
	signed := []byte(parts[0] + "." + parts[1])
	digest := hashOf(alg.hash, signed)
	for _, k := range candidates {
		pub, err := k.publicKey()
		if err != nil {
			continue
		}
		switch alg.kind {
		case "RSA":
			if rpub, ok := pub.(*rsa.PublicKey); ok && rsa.VerifyPKCS1v15(rpub, alg.hash, digest, sig) == nil {
				return payload, nil
			}
		case "PS":
			if rpub, ok := pub.(*rsa.PublicKey); ok && rsa.VerifyPSS(rpub, alg.hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil {
				return payload, nil
			}
		case "EC":
			epub, ok := pub.(*ecdsa.PublicKey)
			if !ok || epub.Curve != alg.curve {
				continue
			}
			size := (alg.curve.Params().BitSize + 7) / 8
			if len(sig) != 2*size {
				continue
			}
			r := new(big.Int).SetBytes(sig[:size])
			s := new(big.Int).SetBytes(sig[size:])
			if ecdsa.Verify(epub, digest, r, s) {
				return payload, nil
			}
		}
	}
	return nil, errors.New("the signature does not verify")
}

func hashOf(h crypto.Hash, b []byte) []byte {
	switch h {
	case crypto.SHA384:
		d := sha512.Sum384(b)
		return d[:]
	case crypto.SHA512:
		d := sha512.Sum512(b)
		return d[:]
	default:
		d := sha256.Sum256(b)
		return d[:]
	}
}

// claims are the ID token claims the console reads.
type claims struct {
	Issuer    string
	Subject   string
	Audience  []string
	Expires   int64
	IssuedAt  int64
	NotBefore int64
	Nonce     string
	All       map[string]any
}

// parseClaims reads the standard claims and keeps the rest for the
// configured username and groups claims.
func parseClaims(payload []byte) (claims, error) {
	var all map[string]any
	if err := json.Unmarshal(payload, &all); err != nil {
		return claims{}, fmt.Errorf("bad claims: %w", err)
	}
	c := claims{All: all}
	c.Issuer, _ = all["iss"].(string)
	c.Subject, _ = all["sub"].(string)
	c.Nonce, _ = all["nonce"].(string)
	switch v := all["aud"].(type) {
	case string:
		c.Audience = []string{v}
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}
	c.Expires = numClaim(all["exp"])
	c.IssuedAt = numClaim(all["iat"])
	c.NotBefore = numClaim(all["nbf"])
	return c, nil
}

func numClaim(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// stringClaim reads a claim as a string.
func stringClaim(all map[string]any, name string) string {
	s, _ := all[name].(string)
	return s
}

// stringsClaim reads a claim as a list of strings; a single string counts
// as a list of one, and nested claims use dots ("realm_access.roles").
func stringsClaim(all map[string]any, name string) []string {
	var v any = all
	for _, part := range strings.Split(name, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
