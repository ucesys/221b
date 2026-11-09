package s3

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/s3/s3test"
)

var testKey = awsv4.Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "secret/key+test"}

func client(t *testing.T, f *s3test.Fake, pathStyle bool) *Client {
	t.Helper()
	cfg := Config{Bucket: f.Bucket, Region: f.Region, PathStyle: pathStyle, Credentials: func(context.Context) (awsv4.Credentials, error) { return testKey, nil }}
	if pathStyle {
		cfg.Endpoint = f.URL
		cfg.HTTP = f.Client()
	} else {
		cfg.Endpoint = "http://s3.example.test:9000"
		cfg.HTTP = &http.Client{Transport: f.Transport()}
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Put, head, get, list and delete, path-style and virtual-host, with keys
// that need escaping; the fake checks every signature as S3 would.
func TestTheClientAgainstAFakeS3(t *testing.T) {
	for _, pathStyle := range []bool{true, false} {
		t.Run(fmt.Sprint("pathStyle=", pathStyle), func(t *testing.T) {
			ctx := context.Background()
			f := s3test.New("backups", "eu-central-1", testKey)
			defer f.Close()
			c := client(t, f, pathStyle)
			keys := []string{"221b/a b+c$.json.gz", "221b/ü/2.json.gz", "other/x"}
			for i, k := range keys {
				if err := c.Put(ctx, k, []byte(strings.Repeat("x", i+1)), PutOptions{ContentType: "application/gzip", SSE: SSEKMS, KMSKeyID: "alias/backups"}); err != nil {
					t.Fatalf("put %s: %v", k, err)
				}
			}
			if o := f.Objects()[keys[0]]; o.SSE != SSEKMS || o.KMSKeyID != "alias/backups" {
				t.Fatalf("the encryption asked for: %+v", o)
			}
			if o, err := c.Head(ctx, keys[1]); err != nil || o.Size != 2 || o.Modified.IsZero() {
				t.Fatalf("head: %+v %v", o, err)
			}
			if b, err := c.Get(ctx, keys[0]); err != nil || string(b) != "x" {
				t.Fatalf("get: %q %v", b, err)
			}
			list, err := c.List(ctx, "221b/")
			if err != nil || len(list) != 2 || list[0].Key != keys[0] || list[1].Size != 2 {
				t.Fatalf("list: %+v %v", list, err)
			}
			if err := c.Delete(ctx, keys[0]); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(ctx, keys[0]); !NotFound(err) {
				t.Fatalf("after delete: %v", err)
			}
			if err := c.Delete(ctx, keys[0]); err != nil {
				t.Fatalf("deleting what is gone: %v", err)
			}
		})
	}
}

// A listing of many pages is read to its end.
func TestAListingOfManyPages(t *testing.T) {
	f := s3test.New("b", "us-east-1", testKey)
	defer f.Close()
	f.PageSize = 2
	for i := 0; i < 5; i++ {
		f.Set(fmt.Sprintf("p/%d", i), []byte("x"), time.Now())
	}
	list, err := client(t, f, true).List(context.Background(), "p/")
	if err != nil || len(list) != 5 || list[4].Key != "p/4" || f.Requests["LIST"] != 3 {
		t.Fatalf("%+v %v %v", list, err, f.Requests)
	}
}

// S3's refusals come back as errors that say what S3 said; a wrong key is
// refused by the signature check.
func TestRefusals(t *testing.T) {
	ctx := context.Background()
	f := s3test.New("b", "us-east-1", testKey)
	defer f.Close()
	f.Fail = func(r *http.Request) (int, string) {
		if r.Method == http.MethodPut {
			return http.StatusForbidden, "AccessDenied"
		}
		return 0, ""
	}
	c := client(t, f, true)
	if err := c.Put(ctx, "k", []byte("x"), PutOptions{}); err == nil || !strings.Contains(err.Error(), "403 AccessDenied") {
		t.Fatalf("%v", err)
	}
	wrong, _ := New(Config{Endpoint: f.URL, Bucket: "b", PathStyle: true, HTTP: f.Client(), Credentials: func(context.Context) (awsv4.Credentials, error) {
		return awsv4.Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "wrong"}, nil
	}})
	if _, err := wrong.List(ctx, ""); err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("a wrong secret: %v", err)
	}
	if err := c.Put(ctx, "k", nil, PutOptions{SSE: "rot13"}); err == nil {
		t.Fatal("an unknown encryption was sent")
	}
	for _, ep := range []string{"s3.example.com", "https://s3.example.com/path", "ftp://x"} {
		if _, err := New(Config{Endpoint: ep, Bucket: "b", Credentials: func(context.Context) (awsv4.Credentials, error) { return testKey, nil }}); err == nil {
			t.Errorf("the endpoint %q was taken", ep)
		}
	}
	if _, err := New(Config{Bucket: "b", CAPEM: "not pem", Credentials: func(context.Context) (awsv4.Credentials, error) { return testKey, nil }}); err == nil {
		t.Error("a CA that is not PEM was taken")
	}
}

// The default address is AWS's, the bucket in the host name.
func TestTheDefaultAddress(t *testing.T) {
	c, err := New(Config{Bucket: "b", Region: "eu-west-1", Credentials: func(context.Context) (awsv4.Credentials, error) { return testKey, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if u := c.url("x/y z", nil).String(); u != "https://b.s3.eu-west-1.amazonaws.com/x/y%20z" {
		t.Fatalf("%s", u)
	}
	if !bytes.Equal([]byte(c.url("", nil).Path), []byte("/")) {
		t.Fatal("the bucket's path")
	}
}

// Conditional writes (Phase 12, the knowledge store): a put over an
// object's ETag succeeds only while it is unchanged, a put where there is
// no object only while there is none; and the bucket's versioning reads.
func TestConditionalWritesAndVersioning(t *testing.T) {
	ctx := context.Background()
	f := s3test.New("wiki", "eu-west-1", testKey)
	defer f.Close()
	c := client(t, f, true)
	etag, err := c.PutObject(ctx, "pages/a.md", []byte("one"), PutOptions{IfNoneMatch: true})
	if err != nil || etag == "" {
		t.Fatalf("a new object: %q, %v", etag, err)
	}
	if _, err := c.PutObject(ctx, "pages/a.md", []byte("again"), PutOptions{IfNoneMatch: true}); !Conflict(err) {
		t.Fatalf("a new object where one is: %v", err)
	}
	b, got, err := c.GetObject(ctx, "pages/a.md")
	if err != nil || string(b) != "one" || got != etag {
		t.Fatalf("get: %q %q %v", b, got, err)
	}
	if _, err := c.PutObject(ctx, "pages/a.md", []byte("two"), PutOptions{IfMatch: etag}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, "pages/a.md", []byte("three"), PutOptions{IfMatch: etag}); !Conflict(err) {
		t.Fatalf("over a changed object: %v", err)
	}
	list, _ := c.List(ctx, "pages/")
	if len(list) != 1 || list[0].ETag == "" || list[0].ETag == etag {
		t.Fatalf("the listing carries the new ETag: %+v", list)
	}
	if v, err := c.Versioning(ctx); err != nil || v != "" {
		t.Fatalf("never on: %q %v", v, err)
	}
	f.Versioning = "Enabled"
	if v, err := c.Versioning(ctx); err != nil || v != "Enabled" {
		t.Fatalf("on: %q %v", v, err)
	}
}
