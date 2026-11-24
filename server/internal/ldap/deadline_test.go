package ldap

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// The context a directory call is given bounds every request on the
// connection, not only the dial: a directory that takes the bind and
// never answers holds a 5-second call no longer than 5 seconds (the
// review of the auth package's final sweep: setup.go:231's bound on
// reading a person's groups again was not honoured).
func TestTheCallersDeadlineBoundsEveryRequest(t *testing.T) {
	cfg := Config{URL: "ldap://ldap.example:389", Timeout: 15 * time.Second, Dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, server) }()
		return client, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.c.Close()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- c.Bind("cn=svc", "pw") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a bind nobody answered went through")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("the bind took %s", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bind outlived its caller's deadline by seconds")
	}
}
