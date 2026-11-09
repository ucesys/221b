// Package s3test is a fake S3 for tests: one bucket in memory, reached
// path-style or virtual-host, that checks every request's SigV4 signature
// against the key it is given, as S3 does, and keeps the server-side
// encryption asked for with each object.
package s3test

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/awsv4"
)

// Object is what the fake keeps of one.
type Object struct {
	Body     []byte
	Modified time.Time
	SSE      string
	KMSKeyID string
	// ETag is the body's MD5, quoted, as S3 gives it for a single upload.
	ETag string
}

func etag(b []byte) string {
	sum := md5.Sum(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// Fake is the server.
type Fake struct {
	*httptest.Server
	Bucket string
	Region string
	Key    awsv4.Credentials
	// Fail, when set, answers a request instead (status and S3 code).
	Fail func(r *http.Request) (int, string)
	// PageSize cuts listings into pages, to test the continuation.
	PageSize int
	Now      func() time.Time
	mu       sync.Mutex
	objects  map[string]Object
	// Requests counts what was asked: "PUT", "GET", "HEAD", "DELETE", "LIST".
	Requests map[string]int
	// Versioning is what GET ?versioning answers: "Enabled", "Suspended"
	// or "".
	Versioning string
}

// New starts a fake for bucket, signed by key in region.
func New(bucket, region string, key awsv4.Credentials) *Fake {
	f := &Fake{Bucket: bucket, Region: region, Key: key, objects: map[string]Object{}, Requests: map[string]int{}, Now: time.Now}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// NewHandler is a fake with no server of its own, for a program that
// serves it beside other things (the simulator's wiki bucket, Phase 12):
// its Handler answers path-style requests for the bucket.
func NewHandler(bucket, region string, key awsv4.Credentials) *Fake {
	return &Fake{Bucket: bucket, Region: region, Key: key, objects: map[string]Object{}, Requests: map[string]int{}, Now: time.Now}
}

// Handler serves the fake.
func (f *Fake) Handler() http.Handler { return http.HandlerFunc(f.serve) }

// Objects returns a copy of what is stored, by key.
func (f *Fake) Objects() map[string]Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]Object{}
	for k, o := range f.objects {
		out[k] = o
	}
	return out
}

// Set stores an object as if it had been put at t.
func (f *Fake) Set(key string, body []byte, t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = Object{Body: body, Modified: t, ETag: etag(body)}
}

// Transport sends every request to the fake, whatever its host, so a
// virtual-host address (bucket.s3.example) reaches it too.
func (f *Fake) Transport() http.RoundTripper {
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = "http", strings.TrimPrefix(f.URL, "http://")
		r2.Host = r.URL.Host
		return http.DefaultTransport.RoundTrip(r2)
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (rt roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }

func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

// verify signs the request again as it arrived and compares: the path
// and query on the wire must be those signed.
func (f *Fake) verify(r *http.Request, body []byte) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+f.Key.AccessKeyID+"/") || !strings.Contains(auth, "/"+f.Region+"/s3/aws4_request") {
		return "InvalidAccessKeyId"
	}
	sum := sha256.Sum256(body)
	if h := r.Header.Get("X-Amz-Content-Sha256"); h != hex.EncodeToString(sum[:]) && h != awsv4.UnsignedPayload {
		return "XAmzContentSHA256Mismatch"
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "AccessDenied"
	}
	again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	_, list, _ := strings.Cut(auth, "SignedHeaders=")
	list, _, _ = strings.Cut(list, ",")
	for _, h := range strings.Split(list, ";") {
		if h != "host" && h != "x-amz-date" {
			again.Header.Set(h, r.Header.Get(h))
		}
	}
	key := f.Key
	key.SessionToken = r.Header.Get("X-Amz-Security-Token")
	awsv4.Sign(again, body, key, f.Region, "s3", at)
	if again.Header.Get("Authorization") != auth {
		return "SignatureDoesNotMatch"
	}
	return ""
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if code := f.verify(r, body); code != "" {
		fail(w, http.StatusForbidden, code)
		return
	}
	// Path-style /bucket/key, or virtual-host bucket.host/key.
	path := r.URL.Path
	key := ""
	if host, _, _ := strings.Cut(r.Host, ":"); strings.HasPrefix(host, f.Bucket+".") {
		key = strings.TrimPrefix(path, "/")
	} else {
		rest, ok := strings.CutPrefix(path, "/"+f.Bucket)
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			fail(w, http.StatusNotFound, "NoSuchBucket")
			return
		}
		key = strings.TrimPrefix(rest, "/")
	}
	op := r.Method
	if r.Method == http.MethodGet && key == "" {
		op = "LIST"
		if _, ok := r.URL.Query()["versioning"]; ok {
			op = "VERSIONING"
		}
	}
	f.mu.Lock()
	f.Requests[op]++
	f.mu.Unlock()
	if f.Fail != nil {
		if status, code := f.Fail(r); status != 0 {
			fail(w, status, code)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case http.MethodPut:
		sse := r.Header.Get("X-Amz-Server-Side-Encryption")
		if sse != "" && sse != "AES256" && sse != "aws:kms" {
			fail(w, http.StatusBadRequest, "InvalidArgument")
			return
		}
		old, exists := f.objects[key]
		if m := r.Header.Get("If-Match"); m != "" && (!exists || m != old.ETag) {
			fail(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if r.Header.Get("If-None-Match") == "*" && exists {
			fail(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		o := Object{Body: body, Modified: f.Now().UTC(), SSE: sse, KMSKeyID: r.Header.Get("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"), ETag: etag(body)}
		f.objects[key] = o
		w.Header().Set("ETag", o.ETag)
	case http.MethodGet, http.MethodHead:
		o, ok := f.objects[key]
		if !ok {
			fail(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(o.Body)))
		w.Header().Set("Last-Modified", o.Modified.UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", o.ETag)
		if r.Method == http.MethodGet {
			w.Write(o.Body)
		}
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case "LIST":
		f.list(w, r)
	case "VERSIONING":
		w.Header().Set("Content-Type", "application/xml")
		status := ""
		if f.Versioning != "" {
			status = "<Status>" + f.Versioning + "</Status>"
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">%s</VersioningConfiguration>`, status)
	default:
		fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		fail(w, http.StatusBadRequest, "InvalidArgument")
		return
	}
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, q.Get("prefix")) && k > q.Get("continuation-token") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type content struct {
		Key          string `xml:"Key"`
		Size         int    `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
	}
	type result struct {
		XMLName               xml.Name  `xml:"ListBucketResult"`
		Contents              []content `xml:"Contents"`
		IsTruncated           bool      `xml:"IsTruncated"`
		NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
	}
	var res result
	for i, k := range keys {
		if f.PageSize > 0 && i == f.PageSize {
			res.IsTruncated, res.NextContinuationToken = true, keys[i-1]
			break
		}
		o := f.objects[k]
		res.Contents = append(res.Contents, content{Key: k, Size: len(o.Body), LastModified: o.Modified.UTC().Format(time.RFC3339), ETag: o.ETag})
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}
