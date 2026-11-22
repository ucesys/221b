package iamauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/ucesys/221b/server/internal/awsv4"
)

type fixedIdentity struct {
	k      awsv4.Credentials
	err    error
	asked  int
	region string
}

func (f *fixedIdentity) Credentials(_ context.Context, region string) (awsv4.Credentials, error) {
	f.asked++
	f.region = region
	return f.k, f.err
}

// An RDS token is made for each connection from the identity's
// credentials, with no call to AWS, and lasts 15 minutes.
func TestAnRDSTokenIsMadeForEachConnection(t *testing.T) {
	now := time.Date(2026, 11, 20, 21, 37, 55, 0, time.UTC)
	id := &fixedIdentity{k: awsv4.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", SessionToken: "IQoJb3JpZ2luX2VjEXAMPLE/+=="}}
	a := &AWS{Identity: id, Region: "eu-west-1", Endpoint: "db.abc.eu-west-1.rds.amazonaws.com:5432", User: "app_221b", Now: func() time.Time { return now }}
	tok, err := a.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Value, "db.abc.eu-west-1.rds.amazonaws.com:5432?Action=connect&DBUser=app_221b&") || !tok.Expires.Equal(now.Add(15*time.Minute)) || id.region != "eu-west-1" {
		t.Fatalf("%q until %v (region %q)", tok.Value, tok.Expires, id.region)
	}
	a.Token(context.Background())
	if id.asked != 2 {
		t.Fatalf("the identity was asked %d times for two connections", id.asked)
	}
	id.err = errors.New("the pod has no AWS identity")
	if _, err := a.Token(context.Background()); err == nil {
		t.Fatal("a missing identity made a token")
	}
}

type fakeOAuth struct {
	asked int
	now   *time.Time
}

func (f *fakeOAuth) Token() (*oauth2.Token, error) {
	f.asked++
	return &oauth2.Token{AccessToken: "ya29.token-" + string(rune('0'+f.asked)), Expiry: f.now.Add(time.Hour)}, nil
}

// A Cloud SQL token is the access token of the pod's Google identity,
// kept until a minute before it expires.
func TestACloudSQLTokenIsKeptUntilItNearlyExpires(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := &fakeOAuth{now: &now}
	src := GCP(f, func() time.Time { return now })
	a, _ := src.Token(context.Background())
	b, _ := src.Token(context.Background())
	if a.Value != "ya29.token-1" || b.Value != a.Value || f.asked != 1 {
		t.Fatalf("%q %q, asked %d", a.Value, b.Value, f.asked)
	}
	now = now.Add(59 * time.Minute)
	if c, _ := src.Token(context.Background()); c.Value != "ya29.token-2" {
		t.Fatalf("a minute before it expires: %q", c.Value)
	}
}
