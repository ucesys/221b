package db

import (
	"context"
	"sync"
	"time"
)

// The ways of signing in with a token of the cloud's IAM in place of a
// password (DB-83).
const (
	IAMNone = "none"
	// IAMAWS signs in to RDS or Aurora PostgreSQL with an RDS IAM
	// authentication token, made from the pod's AWS identity.
	IAMAWS = "aws"
	// IAMGCP signs in to Cloud SQL with an OAuth2 access token of the
	// pod's Google identity.
	IAMGCP = "gcp"
)

// Token is a password made for a sign-in, and when the database stops
// taking it. A zero Expires is a token used once.
type Token struct {
	Value   string
	Expires time.Time
}

// TokenSource makes the password of each new connection where the
// database takes an IAM token in place of a password (DB-83). It is asked
// in BeforeConnect, so a token is made, or taken from what is kept, when
// a connection is opened and at no other time.
type TokenSource interface {
	Token(ctx context.Context) (Token, error)
}

// TokenFunc is a function that is a TokenSource.
type TokenFunc func(ctx context.Context) (Token, error)

// Token calls f.
func (f TokenFunc) Token(ctx context.Context) (Token, error) { return f(ctx) }

// RefreshBefore is how long before it expires a kept token is made again,
// so that no sign-in is given a token that expires on its way.
const RefreshBefore = time.Minute

// Refreshing keeps the token src makes until RefreshBefore its expiry,
// and makes a new one then; a token that names no expiry is not kept. A
// failure is not kept either: the next connection asks again.
func Refreshing(src TokenSource, now func() time.Time) TokenSource {
	if now == nil {
		now = time.Now
	}
	return &refreshing{src: src, now: now}
}

type refreshing struct {
	src TokenSource
	now func() time.Time
	mu  sync.Mutex
	tok Token
}

func (r *refreshing) Token(ctx context.Context) (Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tok.Value != "" && r.now().Add(RefreshBefore).Before(r.tok.Expires) {
		return r.tok, nil
	}
	t, err := r.src.Token(ctx)
	if err != nil {
		r.tok = Token{}
		return Token{}, err
	}
	r.tok = Token{}
	if !t.Expires.IsZero() {
		r.tok = t
	}
	return t, nil
}
