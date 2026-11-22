// Package iamauth makes the tokens a console signs in to an external
// database with in place of a password (DB-83): an RDS IAM authentication
// token from the pod's AWS identity, or a Cloud SQL login token, an OAuth2
// access token of the pod's Google identity. Neither calls the database's
// cloud for anything but the identity's own credentials.
package iamauth

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/db"
)

// CloudSQLScope is the scope of a Cloud SQL login token: the one Google's
// own Cloud SQL connectors and `gcloud sql generate-login-token` ask for,
// which lets the token sign in to a database and do nothing else.
const CloudSQLScope = "https://www.googleapis.com/auth/sqlservice.login"

// Source is the token source cfg asks for, or nil for none.
func Source(ctx context.Context, cfg db.Config) (db.TokenSource, error) {
	switch cfg.IAMAuth {
	case db.IAMAWS:
		return &AWS{Identity: &awsv4.Identity{Session: "221b-database"}, Region: cfg.AWSRegion, Endpoint: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), User: cfg.User}, nil
	case db.IAMGCP:
		// The token's own context: it outlives the start that made it.
		ts, err := google.DefaultTokenSource(context.WithoutCancel(ctx), CloudSQLScope)
		if err != nil {
			return nil, fmt.Errorf("the pod has no Google identity for iamAuth gcp (Workload Identity, or GOOGLE_APPLICATION_CREDENTIALS): %w", err)
		}
		return GCP(ts, nil), nil
	}
	return nil, nil
}

// AWS makes an RDS IAM authentication token for each connection: a
// presigned request to connect as User at Endpoint, valid for 15 minutes,
// signed with the pod's AWS identity (IRSA, EKS Pod Identity or the
// environment's keys; the identity keeps its credentials until five
// minutes before they expire). Making one is a signature: no call to AWS.
type AWS struct {
	Identity interface {
		Credentials(ctx context.Context, region string) (awsv4.Credentials, error)
	}
	Region, Endpoint, User string
	Now                    func() time.Time
}

// Token makes a token for one sign-in.
func (a *AWS) Token(ctx context.Context) (db.Token, error) {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	k, err := a.Identity.Credentials(ctx, a.Region)
	if err != nil {
		return db.Token{}, err
	}
	return db.Token{Value: awsv4.RDSAuthToken(a.Endpoint, a.Region, a.User, k, now), Expires: now.Add(awsv4.RDSTokenLife)}, nil
}

// GCP is a Cloud SQL login token from ts, kept until a minute before it
// expires (an hour, as Google gives them) and made again then.
func GCP(ts oauth2.TokenSource, now func() time.Time) db.TokenSource {
	return db.Refreshing(db.TokenFunc(func(context.Context) (db.Token, error) {
		t, err := ts.Token()
		if err != nil {
			return db.Token{}, err
		}
		return db.Token{Value: t.AccessToken, Expires: t.Expiry}, nil
	}), now)
}
