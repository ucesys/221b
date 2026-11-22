package db_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// fakeTokens counts what it is asked, and makes token-1, token-2, …
// valid for life.
type fakeTokens struct {
	asked atomic.Int32
	life  time.Duration
	now   func() time.Time
	fail  error
}

func (f *fakeTokens) Token(context.Context) (db.Token, error) {
	n := f.asked.Add(1)
	if f.fail != nil {
		return db.Token{}, f.fail
	}
	t := db.Token{Value: "iam-token-marker-" + string(rune('0'+n))}
	if f.life > 0 {
		t.Expires = f.now().Add(f.life)
	}
	return t, nil
}

func iamConfig(mode string, src db.TokenSource) db.Config {
	return db.Config{Host: "db", Port: 5432, Name: "221b", User: "app_221b", SSLMode: "verify-full", MaxConns: 10, IAMAuth: mode, AWSRegion: "eu-west-1", Tokens: src}
}

// DB-83: the values of iamAuth, and what goes with them.
func TestIAMAuthIsCheckedWithWhatGoesWithIt(t *testing.T) {
	for _, mode := range []string{"", db.IAMNone, db.IAMAWS, db.IAMGCP} {
		if err := iamConfig(mode, nil).Validate(); err != nil {
			t.Errorf("%q: %v", mode, err)
		}
	}
	for name, c := range map[string]db.Config{
		"an unknown way": iamConfig("azure", nil),
		"with a password": func() db.Config {
			c := iamConfig(db.IAMAWS, nil)
			c.PasswordFile = "/etc/221b/db/password"
			return c
		}(),
		"without TLS": func() db.Config {
			c := iamConfig(db.IAMGCP, nil)
			c.SSLMode = "disable"
			return c
		}(),
		"aws without a region": func() db.Config {
			c := iamConfig(db.IAMAWS, nil)
			c.AWSRegion = ""
			return c
		}(),
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
	// No source: refused at the open, not at the first connection.
	if _, err := db.Open(context.Background(), iamConfig(db.IAMGCP, nil), nil); err == nil || !strings.Contains(err.Error(), "nothing makes its tokens") {
		t.Fatalf("an open without a source: %v", err)
	}
}

// The token is the password of each new connection, asked for in
// BeforeConnect.
func TestAnIAMTokenIsEachConnectionsPassword(t *testing.T) {
	src := &fakeTokens{}
	before := db.BeforeConnect(iamConfig(db.IAMAWS, src))
	for i := 1; i <= 3; i++ {
		cc := &pgx.ConnConfig{}
		if err := before(context.Background(), cc); err != nil {
			t.Fatal(err)
		}
		if want := "iam-token-marker-" + string(rune('0'+i)); cc.Password != want {
			t.Fatalf("connection %d: password %q, want %q", i, cc.Password, want)
		}
	}
	// A source that fails fails the connection, in words that name the way.
	src.fail = errors.New("no identity")
	if err := before(context.Background(), &pgx.ConnConfig{}); err == nil || !strings.Contains(err.Error(), "aws IAM token") {
		t.Fatalf("a failed token: %v", err)
	}
}

// A kept token is made again a minute before it expires, and a failure
// is not kept.
func TestAKeptTokenIsMadeAgainBeforeItExpires(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	src := &fakeTokens{life: time.Hour, now: clock}
	r := db.Refreshing(src, clock)
	get := func() string {
		t.Helper()
		tok, err := r.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return tok.Value
	}
	if a, b := get(), get(); a != b || src.asked.Load() != 1 {
		t.Fatalf("kept: %q %q, asked %d", a, b, src.asked.Load())
	}
	now = now.Add(58 * time.Minute)
	if get(); src.asked.Load() != 1 {
		t.Fatalf("two minutes before it expires: asked %d", src.asked.Load())
	}
	now = now.Add(time.Minute)
	if v := get(); src.asked.Load() != 2 || v != "iam-token-marker-2" {
		t.Fatalf("a minute before it expires: %q, asked %d", v, src.asked.Load())
	}
	src.fail = errors.New("the metadata server did not answer")
	now = now.Add(2 * time.Hour)
	if _, err := r.Token(context.Background()); err == nil {
		t.Fatal("a failure was not passed on")
	}
	src.fail = nil
	if v := get(); v != "iam-token-marker-4" {
		t.Fatalf("after a failure: %q", v)
	}
	// A token with no expiry is never kept.
	once := &fakeTokens{}
	r = db.Refreshing(once, clock)
	r.Token(context.Background())
	r.Token(context.Background())
	if once.asked.Load() != 2 {
		t.Fatalf("a token without an expiry was kept: asked %d", once.asked.Load())
	}
}

// The token is in no log line and no error, when the database cannot be
// reached with it.
func TestAnIAMTokenIsNeverLogged(t *testing.T) {
	src := &fakeTokens{}
	cfg := iamConfig(db.IAMGCP, src)
	cfg.Host, cfg.Port, cfg.SSLMode = "127.0.0.1", 1, "require"
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.Open(ctx, cfg, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err == nil {
		t.Fatal("nothing listens on port 1")
	}
	if src.asked.Load() == 0 {
		t.Fatal("no token was made for the connection")
	}
	if strings.Contains(err.Error(), "iam-token-marker") || strings.Contains(logs.String(), "iam-token-marker") {
		t.Fatalf("the token was shown:\n%v\n%s", err, logs.String())
	}
}
