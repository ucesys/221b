package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestUnreachableExplainsTheCause(t *testing.T) {
	get := func(err error) error { return &url.Error{Op: "Get", URL: "https://10.96.0.1:443/version", Err: err} }
	cases := []struct {
		err  error
		want string
	}{
		{get(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), "The connection was refused."},
		{get(io.EOF), "The connection closed before it answered."},
		{fmt.Errorf("%s - error from a previous attempt: %s", context.DeadlineExceeded, io.EOF), "The connection closed before it answered."},
		{get(context.DeadlineExceeded), "It did not answer within 5 seconds."},
		{get(&net.DNSError{Name: "api.example.internal", Err: "no such host", IsNotFound: true}), "The name api.example.internal does not resolve."},
		{apierrors.NewUnauthorized("token expired"), "It refused the credentials (401 Unauthorized)."},
		{apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("no")), "It refused to give its version (403 Forbidden)."},
		{errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), "Its certificate is not trusted: tls: failed to verify certificate: x509: certificate signed by unknown authority"},
		{errors.New("something odd"), "Something odd."},
	}
	for _, c := range cases {
		if got := unreachable(c.err); got != c.want {
			t.Errorf("unreachable(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// An answer with no text must not stop the console (kube#6): client-go's
// error for a 415 with an empty text/plain body says nothing, and the
// health check runs with no recover.
func TestUnreachableWithoutText(t *testing.T) {
	empty := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 415}}
	if got := unreachable(empty); got != "It answered with an error (HTTP 415) and no reason." {
		t.Errorf("unreachable(415, no text) = %q", got)
	}
	if got := unreachable(errors.New("")); got != "It answered with an error and no reason." {
		t.Errorf("unreachable(no text) = %q", got)
	}
	if got := unreachable(errors.New("élan lost")); got != "Élan lost." {
		t.Errorf("unreachable(élan) = %q", got)
	}
}

// A round of a poller that panics is logged, and the next round runs
// (kube#6).
func TestAPollerThatPanicsRunsAgain(t *testing.T) {
	c := &Cluster{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rounds := make(chan int, 10)
	n := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.loop(ctx, time.Millisecond, func(context.Context) {
			n++
			rounds <- n
			if n == 1 {
				panic("a cluster's odd answer")
			}
			if n == 3 {
				cancel()
			}
		})
	}()
	<-done
	if n < 3 {
		t.Fatalf("%d rounds", n)
	}
}
