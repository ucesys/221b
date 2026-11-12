package issues

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The scopes a token may hold (IS-24).
const (
	// ScopeCreate files reports as the token's account.
	ScopeCreate = "reports:create"
	// ScopeRead reads the reports the account filed.
	ScopeRead = "reports:read"
	// ScopeOnBehalf files a report for a person, named in the request.
	ScopeOnBehalf = "reports:onBehalf"
)

// Scopes lists them.
var Scopes = []string{ScopeCreate, ScopeRead, ScopeOnBehalf}

// Token lets a system file reports through the API as a service account
// (IS-24): a monitoring system, a chat bot, a help desk. Only a hash of
// the secret is kept; the secret is shown once, when it is made.
type Token struct {
	ID string `json:"id"`
	// Name says what it is for; Account is the system it files as.
	Name    string   `json:"name"`
	Account string   `json:"account"`
	Scopes  []string `json:"scopes"`
	// Hash is the SHA-256 of the whole token, hex.
	Hash      string     `json:"hash"`
	Created   time.Time  `json:"created"`
	CreatedBy Person     `json:"createdBy"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

// tokenPrefix starts every token, so a leaked one is easy to find.
const tokenPrefix = "221bis_"

var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func hashToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// TokenInput is a token to make.
type TokenInput struct {
	Name    string   `json:"name"`
	Account string   `json:"account"`
	Scopes  []string `json:"scopes"`
}

// Tokens lists the tokens, never their hashes, for an administrator.
func (s *Service) Tokens(ctx context.Context, c Caller) ([]Token, error) {
	if !c.Admin {
		return nil, fmt.Errorf("%w: only a SuperAdmin sees the API tokens", ErrDenied)
	}
	st, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]Token, len(st.config.Tokens))
	for i, t := range st.config.Tokens {
		t.Hash = ""
		out[i] = t
	}
	return out, nil
}

// CreateToken makes a token and returns it with its secret, which is
// never shown again (IS-24).
func (s *Service) CreateToken(ctx context.Context, c Caller, in TokenInput) (Token, string, error) {
	if !c.Admin {
		return Token{}, "", fmt.Errorf("%w: only a SuperAdmin makes API tokens", ErrDenied)
	}
	name, err := short("the name", in.Name, 120)
	if err != nil {
		return Token{}, "", err
	}
	account := strings.ToLower(strings.TrimSpace(in.Account))
	if name == "" || !accountName.MatchString(account) {
		return Token{}, "", invalid("give the token a name, and an account of lowercase letters, digits, '.', '-' and '_'")
	}
	if len(in.Scopes) == 0 {
		return Token{}, "", invalid("give the token at least one scope: %s", strings.Join(Scopes, ", "))
	}
	for _, sc := range in.Scopes {
		if !contains(Scopes, sc) {
			return Token{}, "", invalid("%q is not a scope; use %s", sc, strings.Join(Scopes, ", "))
		}
	}
	var id [4]byte
	var secret [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Token{}, "", err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return Token{}, "", err
	}
	t := Token{ID: hex.EncodeToString(id[:]), Name: name, Account: account, Scopes: append([]string(nil), in.Scopes...), Created: s.now(), CreatedBy: c.Person}
	full := tokenPrefix + t.ID + "_" + base64.RawURLEncoding.EncodeToString(secret[:])
	t.Hash = hashToken(full)
	err = s.update(ctx, func(st *state, ch *change) (int, error) {
		if len(st.config.Tokens) >= MaxTokens {
			return 0, invalid("at most %d tokens; revoke one no longer used", MaxTokens)
		}
		st.config.Tokens = append(st.config.Tokens, t)
		ch.audit(AuditRecord{Action: ActionToken, Who: c.Person, Target: "token " + t.ID, Detail: fmt.Sprintf("API token %s (%s) made for the account %s with %s", t.ID, t.Name, t.Account, strings.Join(t.Scopes, ", ")), From: c.From})
		return saveConfig, nil
	})
	if err != nil {
		return Token{}, "", err
	}
	t.Hash = ""
	return t, full, nil
}

// RevokeToken deletes a token: it stops working at once, on every
// replica, since each request reads the tokens.
func (s *Service) RevokeToken(ctx context.Context, c Caller, id string) error {
	if !c.Admin {
		return fmt.Errorf("%w: only a SuperAdmin revokes API tokens", ErrDenied)
	}
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		kept := st.config.Tokens[:0:0]
		var gone *Token
		for _, t := range st.config.Tokens {
			if t.ID == id {
				gone = &t
				continue
			}
			kept = append(kept, t)
		}
		if gone == nil {
			return 0, fmt.Errorf("%w: no token %s", ErrNotFound, id)
		}
		st.config.Tokens = kept
		ch.audit(AuditRecord{Action: ActionToken, Who: c.Person, Target: "token " + id, Detail: fmt.Sprintf("API token %s (%s, account %s) revoked", id, gone.Name, gone.Account), From: c.From})
		return saveConfig, nil
	})
}

// usedEvery is how often a token's last use is written: a busy system
// must not write the store on every report.
const usedEvery = time.Hour

// Authenticate turns a bearer token into the caller it files as, or
// ErrDenied with the same words whatever is wrong with it (IS-24).
func (s *Service) Authenticate(ctx context.Context, bearer string) (Caller, error) {
	refused := fmt.Errorf("%w: the token is not valid", ErrDenied)
	rest, ok := strings.CutPrefix(bearer, tokenPrefix)
	if !ok {
		return Caller{}, refused
	}
	id, _, ok := strings.Cut(rest, "_")
	if !ok {
		return Caller{}, refused
	}
	st, err := s.load(ctx, true)
	if err != nil {
		return Caller{}, err
	}
	sum := hashToken(bearer)
	for _, t := range st.config.Tokens {
		if t.ID != id || subtle.ConstantTimeCompare([]byte(t.Hash), []byte(sum)) != 1 {
			continue
		}
		s.markUsed(ctx, t.ID)
		return Caller{Person: Person{ID: "system:" + t.Account, Name: t.Account, System: true}, Report: contains(t.Scopes, ScopeCreate), Scopes: t.Scopes}, nil
	}
	return Caller{}, refused
}

func (s *Service) markUsed(ctx context.Context, id string) {
	now := s.now()
	s.usedMu.Lock()
	due := now.Sub(s.used[id]) >= usedEvery
	if due {
		s.used[id] = now
	}
	s.usedMu.Unlock()
	if !due {
		return
	}
	err := s.update(ctx, func(st *state, ch *change) (int, error) {
		for i := range st.config.Tokens {
			if st.config.Tokens[i].ID == id {
				st.config.Tokens[i].LastUsed = ptr(now)
				return saveConfig, nil
			}
		}
		return 0, nil
	})
	if err != nil {
		s.log.Warn("a token's last use could not be saved", "token", id, "err", err)
	}
}
