// Package s3 is the little of Amazon S3's API the console's backups need:
// put, get, head, delete and list objects in one bucket, signed with
// SigV4 (awsv4), at AWS or at any S3-compatible address (MinIO, Ceph,
// …), path-style or virtual-host, with server-side encryption and a
// private CA. It is not the AWS SDK, which would add megabytes to the
// image for these five calls.
package s3

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/awsv4"
)

// Server-side encryption, as S3 names it.
const (
	SSES3  = "AES256"
	SSEKMS = "aws:kms"
)

// MaxObject is the most Get reads.
const MaxObject = 256 << 20

// Config says where the bucket is and how to sign.
type Config struct {
	// Endpoint is the S3 address, https://host[:port]; empty is AWS's,
	// https://s3.<region>.amazonaws.com.
	Endpoint string
	Region   string
	Bucket   string
	// PathStyle puts the bucket in the path (host/bucket/key) rather than
	// the host name (bucket.host/key); most S3-compatible stores want it.
	PathStyle bool
	// CAPEM, when set, is a PEM bundle trusted besides the system's.
	CAPEM string
	// Credentials signs each request.
	Credentials func(ctx context.Context) (awsv4.Credentials, error)
	// HTTP replaces the client (tests); CAPEM is then not used.
	HTTP *http.Client
	Now  func() time.Time
}

// Client talks to one bucket.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client
}

// Object is one listed object.
type Object struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// ETag is the object's entity tag, quoted as S3 gives it: what a
	// conditional write names (Phase 12, the knowledge store).
	ETag string `json:"etag,omitempty"`
}

// Error is S3's refusal: its status, code and message.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	msg := e.Code
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("S3 answered %d %s", e.Status, msg)
}

// Conflict says whether err is S3's refusal of a conditional write: the
// object changed since its ETag was read (412), or another write to it was
// under way (409).
func Conflict(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusPreconditionFailed || e.Status == http.StatusConflict)
}

// NotFound says whether err is S3's answer that there is no such object.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusNotFound || e.Code == "NoSuchKey")
}

// New checks the configuration and makes the client.
func New(c Config) (*Client, error) {
	if c.Bucket == "" {
		return nil, errors.New("no bucket")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	ep := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if ep == "" {
		ep = "https://s3." + c.Region + ".amazonaws.com"
	}
	u, err := url.Parse(ep)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("the endpoint %q is not an address like https://s3.example.com:9000", c.Endpoint)
	}
	u.Path = ""
	hc := c.HTTP
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if strings.TrimSpace(c.CAPEM) != "" {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM([]byte(c.CAPEM)) {
				return nil, errors.New("the CA certificate is not PEM")
			}
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		hc = &http.Client{Transport: tr, Timeout: 10 * time.Minute}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Credentials == nil {
		return nil, errors.New("no credentials")
	}
	return &Client{cfg: c, base: u, http: hc}, nil
}

// url is an object's (or with key "", the bucket's) address, with the
// key escaped as SigV4 signs it.
func (c *Client) url(key string, q url.Values) *url.URL {
	u := *c.base
	path := "/" + key
	if c.cfg.PathStyle {
		path = "/" + c.cfg.Bucket
		if key != "" {
			path += "/" + key
		}
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
	}
	u.Path, u.RawPath = path, awsv4.Escape(path, true)
	if len(q) > 0 {
		var parts []string
		for k, vs := range q {
			for _, v := range vs {
				parts = append(parts, awsv4.Escape(k, false)+"="+awsv4.Escape(v, false))
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	return &u
}

// do signs and sends a request, and turns an S3 error into *Error.
func (c *Client) do(ctx context.Context, method, key string, q url.Values, body []byte, h http.Header) (*http.Response, error) {
	creds, err := c.cfg.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(key, q).String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	if body == nil {
		req.Body, req.GetBody = http.NoBody, nil
	}
	for k, vs := range h {
		req.Header[k] = vs
	}
	awsv4.Sign(req, body, creds, c.cfg.Region, "s3", c.cfg.Now())
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(b, &e)
		return nil, &Error{Status: res.StatusCode, Code: e.Code, Message: e.Message}
	}
	return res, nil
}

// PutOptions are an upload's headers.
type PutOptions struct {
	ContentType string
	// SSE is SSES3 or SSEKMS, or empty for the bucket's default.
	SSE string
	// KMSKeyID is the KMS key of SSEKMS; empty is the account's default.
	KMSKeyID string
	// IfMatch writes only over the object with this ETag; IfNoneMatch
	// only where there is no object yet. S3 refuses either with 412,
	// which Conflict tells.
	IfMatch     string
	IfNoneMatch bool
}

// Put uploads an object in one request (S3 takes up to 5 GiB so).
func (c *Client) Put(ctx context.Context, key string, body []byte, o PutOptions) error {
	_, err := c.PutObject(ctx, key, body, o)
	return err
}

// PutObject is Put that returns the new object's ETag.
func (c *Client) PutObject(ctx context.Context, key string, body []byte, o PutOptions) (string, error) {
	h := http.Header{}
	if o.IfMatch != "" {
		h.Set("If-Match", o.IfMatch)
	}
	if o.IfNoneMatch {
		h.Set("If-None-Match", "*")
	}
	if o.ContentType != "" {
		h.Set("Content-Type", o.ContentType)
	}
	switch o.SSE {
	case "":
	case SSES3:
		h.Set("X-Amz-Server-Side-Encryption", SSES3)
	case SSEKMS:
		h.Set("X-Amz-Server-Side-Encryption", SSEKMS)
		if o.KMSKeyID != "" {
			h.Set("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", o.KMSKeyID)
		}
	default:
		return "", fmt.Errorf("server-side encryption %q is not %s or %s", o.SSE, SSES3, SSEKMS)
	}
	if body == nil {
		body = []byte{}
	}
	res, err := c.do(ctx, http.MethodPut, key, nil, body, h)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	return res.Header.Get("ETag"), nil
}

// Get downloads an object, MaxObject at most.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	b, _, err := c.GetObject(ctx, key)
	return b, err
}

// GetObject is Get with the object's ETag.
func (c *Client) GetObject(ctx context.Context, key string) ([]byte, string, error) {
	res, err := c.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxObject+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxObject {
		return nil, "", fmt.Errorf("the object is over %d MiB", MaxObject>>20)
	}
	return b, res.Header.Get("ETag"), nil
}

// Versioning is the bucket's versioning state: "Enabled", "Suspended", or
// "" for a bucket that never had it on.
func (c *Client) Versioning(ctx context.Context) (string, error) {
	res, err := c.do(ctx, http.MethodGet, "", url.Values{"versioning": {""}}, nil, nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var v struct {
		Status string `xml:"Status"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the bucket's versioning: %w", err)
	}
	return v.Status, nil
}

// Head reads an object's size and time.
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	res, err := c.do(ctx, http.MethodHead, key, nil, nil, nil)
	if err != nil {
		return Object{}, err
	}
	res.Body.Close()
	o := Object{Key: key, Size: res.ContentLength, ETag: res.Header.Get("ETag")}
	o.Modified, _ = http.ParseTime(res.Header.Get("Last-Modified"))
	return o, nil
}

// Delete removes an object; one that is not there is no error.
func (c *Client) Delete(ctx context.Context, key string) error {
	res, err := c.do(ctx, http.MethodDelete, key, nil, nil, nil)
	if err != nil {
		if NotFound(err) {
			return nil
		}
		return err
	}
	res.Body.Close()
	return nil
}

// List lists the objects whose keys start with prefix (ListObjectsV2),
// every page of them, in key order.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for page := 0; page < 1000; page++ {
		q := url.Values{"list-type": {"2"}}
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		res, err := c.do(ctx, http.MethodGet, "", q, nil, nil)
		if err != nil {
			return nil, err
		}
		var r struct {
			Contents []struct {
				Key          string `xml:"Key"`
				Size         string `xml:"Size"`
				LastModified string `xml:"LastModified"`
				ETag         string `xml:"ETag"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&r)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading the bucket's listing: %w", err)
		}
		for _, o := range r.Contents {
			size, _ := strconv.ParseInt(o.Size, 10, 64)
			mod, _ := time.Parse(time.RFC3339, o.LastModified)
			out = append(out, Object{Key: o.Key, Size: size, Modified: mod, ETag: o.ETag})
		}
		if !r.IsTruncated || r.NextContinuationToken == "" {
			return out, nil
		}
		token = r.NextContinuationToken
	}
	return out, errors.New("the bucket's listing did not end")
}
