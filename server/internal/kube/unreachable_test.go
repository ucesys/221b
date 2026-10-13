package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
