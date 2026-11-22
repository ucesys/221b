// Package dbtest gives a test a PostgreSQL database of its own.
//
// The tests that need one run against the server TEST_DATABASE names, as
// host:port (make test-db starts one), and are skipped without it, so
// `go test ./...` passes on a laptop with no PostgreSQL. CI sets it.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// Env is the variable that names the test server.
const Env = "TEST_DATABASE"

// Config is the settings of an empty database made for this test and
// dropped when it ends. The test is skipped when no server is named.
func Config(t testing.TB) db.Config {
	t.Helper()
	addr := os.Getenv(Env)
	if addr == "" {
		t.Skipf("no PostgreSQL to test against: set %s=host:port", Env)
	}
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("%s=%q: give host:port", Env, addr)
	}
	port, _ := strconv.Atoi(p)
	user := os.Getenv(Env + "_USER")
	if user == "" {
		user = "postgres"
	}
	cfg := db.Config{Host: host, Port: port, User: user, SSLMode: "disable", MaxConns: 8, Poll: 200 * time.Millisecond, AppName: "221b-test"}
	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=postgres sslmode=disable", host, port, user)
	if pw := os.Getenv(Env + "_PASSWORD"); pw != "" {
		file := filepath.Join(t.TempDir(), "password")
		if err := os.WriteFile(file, []byte(pw+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.PasswordFile = file
		dsn += " password=" + pw
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	cfg.Name = "t_" + hex.EncodeToString(b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the test PostgreSQL at %s: %v", addr, err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+cfg.Name); err != nil {
		t.Fatalf("making the test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return
		}
		defer admin.Close(ctx)
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+cfg.Name+" WITH (FORCE)")
	})
	return cfg
}

// Open is a migrated database of this test's own, closed when it ends.
func Open(t testing.TB) *db.DB {
	t.Helper()
	d := OpenConfig(t, Config(t))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrating the test database: %v", err)
	}
	return d
}

// OpenConfig opens cfg without migrating.
func OpenConfig(t testing.TB, cfg db.Config) *db.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := db.Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the test database: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}
