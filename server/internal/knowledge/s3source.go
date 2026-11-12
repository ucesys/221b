package knowledge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/s3"
	"github.com/ucesys/221b/server/internal/scrub"
)

// A bucket of documents (Phase 12, KN-25, ADR-140): Mycroft reads the
// objects under a prefix of an S3 bucket (AWS, MinIO or any S3-compatible
// store) as it reads uploaded documents: the kinds KN-02 reads, each up to
// MaxDocument; other kinds and larger objects are skipped and counted.
// Each object is an item whose id is its key and whose version is its
// ETag, so an unchanged object is marked seen and not read again (KN-03),
// and the job of the changes only lists the bucket and compares ETags. An
// object gone from the bucket leaves the index at the next read. Its link
// back is s3://bucket/key: never a presigned address, never a credential.

// MaxBucketObjects bounds the objects one read of a bucket lists, and so
// the objects it reads.
const MaxBucketObjects = 1000

// bucketClient is the little of the S3 client a bucket of documents needs.
type bucketClient interface {
	ListMax(ctx context.Context, prefix string, max int) ([]s3.Object, bool, error)
	GetObject(ctx context.Context, key string) ([]byte, string, error)
}

// skipper is a reader that leaves some of a source's items out, and says
// how many and why, for the source's state (KN-25).
type skipper interface {
	Skipped() (int, string)
}

// s3Reader reads a bucket's documents.
type s3Reader struct {
	c      bucketClient
	bucket string
	prefix string
	// secrets are the access key's, kept out of every error.
	secrets []string
	// identity says the bucket is read with the pod's identity.
	identity bool
	skipped  int
	why      string
}

// bucketReader makes a bucket source's reader from its settings and keys.
func (s *Service) bucketReader(x Source, c Creds) (*s3Reader, error) {
	creds := func(ctx context.Context) (awsv4.Credentials, error) {
		if x.Credentials == CredentialsIdentity {
			return s.o.Identity.Credentials(ctx, x.Region)
		}
		if c.AccessKeyID == "" || c.SecretAccessKey == "" {
			return awsv4.Credentials{}, invalid("no access key is saved for %s: give one, or use the pod's identity", x.Name)
		}
		return awsv4.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey}, nil
	}
	hc := s.o.HTTP
	if x.CAPEM != "" {
		hc = nil
	}
	client, err := s3.New(s3.Config{Endpoint: x.Endpoint, Region: x.Region, Bucket: x.Bucket, PathStyle: x.PathStyle, CAPEM: x.CAPEM, Credentials: creds, HTTP: hc, Now: s.o.Now})
	if err != nil {
		return nil, invalid("%v", err)
	}
	r := &s3Reader{c: client, bucket: x.Bucket, prefix: x.Prefix, identity: x.Credentials == CredentialsIdentity}
	for _, v := range []string{c.AccessKeyID, c.SecretAccessKey} {
		if v != "" {
			r.secrets = append(r.secrets, v)
		}
	}
	return r, nil
}

// s3URL is where an object is, as a page's sources name it.
func s3URL(bucket, key string) string { return "s3://" + bucket + "/" + key }

func (r *s3Reader) List(ctx context.Context, _ time.Time) ([]Item, bool, error) {
	objs, more, err := r.c.ListMax(ctx, r.prefix, MaxBucketObjects)
	if err != nil {
		return nil, false, r.describe(err)
	}
	var out []Item
	big := 0
	kinds := map[string]int{}
	other := 0
	for _, o := range objs {
		switch {
		case strings.HasSuffix(o.Key, "/"):
			// A folder's marker, as consoles make them.
		case !Readable(o.Key):
			ext := strings.ToLower(path.Ext(o.Key))
			if ext == "" {
				ext = "no extension"
			}
			kinds[ext]++
			other++
		case o.Size > MaxDocument:
			big++
		default:
			out = append(out, Item{ID: o.Key, Title: path.Base(o.Key), Version: strings.Trim(o.ETag, `"`), Modified: o.Modified, URL: s3URL(r.bucket, o.Key)})
		}
	}
	r.skipped = other + big
	var why []string
	if other > 0 {
		exts := make([]string, 0, len(kinds))
		for k := range kinds {
			exts = append(exts, k)
		}
		sort.Strings(exts)
		if len(exts) > 5 {
			exts = append(exts[:5], "…")
		}
		why = append(why, fmt.Sprintf("%d not a kind Mycroft reads (%s)", other, strings.Join(exts, ", ")))
	}
	if big > 0 {
		why = append(why, fmt.Sprintf("%d over %d MiB", big, MaxDocument>>20))
	}
	if more {
		why = append(why, fmt.Sprintf("only the first %d objects under the prefix are read: narrow the prefix to read the rest", MaxBucketObjects))
	}
	r.why = strings.Join(why, "; ")
	// A listing cut short is not every item: what it did not reach is
	// not gone.
	return out, !more, nil
}

func (r *s3Reader) Read(ctx context.Context, it Item) (string, error) {
	b, _, err := r.c.GetObject(ctx, it.ID)
	if err != nil {
		return "", r.describe(err)
	}
	return Extract(it.ID, b)
}

func (r *s3Reader) Skipped() (int, string) { return r.skipped, r.why }

// describe says what is wrong with a bucket in words a person can act on:
// no such bucket, the credentials refused, another region, a certificate
// not trusted; never with the access key in it.
func (r *s3Reader) describe(err error) error {
	var e *s3.Error
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	msg := err.Error()
	switch {
	case errors.As(err, &e) && (e.Code == "NoSuchBucket" || e.Status == http.StatusNotFound && e.Code == ""):
		msg = fmt.Sprintf("there is no bucket %q at this address (%s)", r.bucket, msg)
	case errors.As(err, &e) && (e.Code == "InvalidAccessKeyId" || e.Code == "SignatureDoesNotMatch"):
		msg = fmt.Sprintf("the bucket %q refused the credentials: check the access key's ID and secret (%s)", r.bucket, msg)
	case errors.As(err, &e) && (e.Status == http.StatusForbidden || e.Code == "AccessDenied"):
		// The key is known, but may not read the bucket (KN-26).
		who := "the key's"
		if r.identity {
			who = "the pod's identity's"
		}
		msg = fmt.Sprintf("The bucket answered: access denied — check %s read permission on %s (%s)", who, r.bucket, msg)
	case errors.As(err, &e) && (e.Code == "PermanentRedirect" || e.Code == "AuthorizationHeaderMalformed" || e.Status == http.StatusMovedPermanently):
		msg = fmt.Sprintf("the bucket %q is in another region: give its region (%s)", r.bucket, msg)
	case errors.As(err, &verify), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalidCert):
		msg = "the bucket's address has a TLS certificate the console does not trust: give its private CA, or check the endpoint (" + msg + ")"
	}
	return errors.New(scrub.Known(msg, r.secrets))
}
