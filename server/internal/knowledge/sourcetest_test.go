package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/s3/s3test"
)

// Test in the source dialog (Phase 12, KN-26).

// typedBucket is a bucket source as its dialog sends it, not saved.
func typedBucket(b *s3test.Fake) Source {
	return Source{Name: "Ops bucket", Kind: SourceS3, Bucket: b.Bucket, Prefix: "ops", Region: b.Region, Endpoint: b.URL, PathStyle: true}
}

// TestTestingUnsavedValuesKN26: POST sources/test lists a source as typed,
// before it is saved: nothing is saved, nothing audited, no object read;
// only a SuperAdmin tests; a new source needs its credential typed.
func TestTestingUnsavedValuesKN26(t *testing.T) {
	f := newFixture(t)
	b := s3test.New("ops-docs", "eu-west-1", docsKey)
	defer b.Close()
	for _, k := range []string{"ops/a.md", "ops/b.csv", "ops/c.png"} {
		b.Set(k, []byte("x"), f.clock.now())
	}
	before := len(f.actions())
	id, secret := docsKeyID, docsSecret
	got, err := f.s.TestSourceAsTyped(ctx, admin, typedBucket(b), CredsChange{AccessKeyID: &id, SecretAccessKey: &secret}, "")
	if err != nil || got.Items != 2 || got.Sample[0] != "ops/a.md" || got.Skipped != 1 || !strings.Contains(got.SkippedWhy, ".png") {
		t.Fatalf("a test as typed: %+v, %v", got, err)
	}
	if b.Requests["GET"] != 0 {
		t.Fatal("a test reads no object")
	}
	set, _ := f.s.Settings(ctx)
	for _, x := range set.Sources {
		if x.Kind == SourceS3 {
			t.Fatalf("a test saves nothing: %+v", x)
		}
	}
	if len(f.actions()) != before {
		t.Fatalf("a test is not audited: %v", f.actions()[before:])
	}
	if _, err := f.s.TestSourceAsTyped(ctx, admin, typedBucket(b), CredsChange{}, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("a new source's key is typed: %v", err)
	}
	if _, err := f.s.TestSourceAsTyped(ctx, bob, typedBucket(b), CredsChange{AccessKeyID: &id, SecretAccessKey: &secret}, ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("only a SuperAdmin: %v", err)
	}

	// Through the API: the answer, 403 for an Admin, and never the key.
	body, _ := json.Marshal(map[string]any{"source": typedBucket(b), "keys": map[string]string{"accessKeyId": docsKeyID, "secretAccessKey": docsSecret}})
	w := serve(t, f.s, &admin, "POST", "sources/test", string(body), nil)
	var res struct {
		OK     bool     `json:"ok"`
		Items  int      `json:"items"`
		Sample []string `json:"sample"`
		Error  string   `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != http.StatusOK || !res.OK || res.Items != 2 || strings.Contains(w.Body.String(), docsSecret) || strings.Contains(w.Body.String(), docsKeyID) {
		t.Fatalf("the API: %d %s", w.Code, w.Body)
	}
	if w := serve(t, f.s, &bob, "POST", "sources/test", string(body), nil); w.Code != http.StatusForbidden {
		t.Fatalf("an Admin tests a source: %d", w.Code)
	}
	wrong, _ := json.Marshal(map[string]any{"source": typedBucket(b), "keys": map[string]string{"accessKeyId": docsKeyID, "secretAccessKey": "typed-wrong-secret-1234"}})
	w = serve(t, f.s, &admin, "POST", "sources/test", string(wrong), nil)
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.OK || !strings.Contains(res.Error, "refused the credentials") || strings.Contains(w.Body.String(), "typed-wrong-secret-1234") || strings.Contains(w.Body.String(), docsKeyID) {
		t.Fatalf("a wrong key: %s", w.Body)
	}
}

// TestABlankCredentialIsTheSavedOneKN26: on Edit, a credential left blank
// is the saved one, read on the server by the source's id; one typed
// replaces it for the test only; the saved source keeps its kind.
func TestABlankCredentialIsTheSavedOneKN26(t *testing.T) {
	f := newFixture(t)
	b := s3test.New("ops-docs", "eu-west-1", docsKey)
	defer b.Close()
	b.Set("ops/a.md", []byte("x"), f.clock.now())
	b.Set("ops/runbooks/b.md", []byte("x"), f.clock.now())
	x := f.bucketSource(t, b)

	// The prefix changed in the dialog, the key left blank.
	x.Prefix = "ops/runbooks/"
	got, err := f.s.TestSourceAsTyped(ctx, admin, x, CredsChange{}, "docs")
	if err != nil || got.Items != 1 || got.Sample[0] != "ops/runbooks/b.md" {
		t.Fatalf("the saved key: %+v, %v", got, err)
	}
	// A key typed is tested, not saved.
	wrong := "typed-wrong-secret-5678"
	_, err = f.s.TestSourceAsTyped(ctx, admin, x, CredsChange{SecretAccessKey: &wrong}, "docs")
	if err == nil || strings.Contains(err.Error(), wrong) || strings.Contains(err.Error(), docsSecret) {
		t.Fatalf("a typed key: %v", err)
	}
	if c, _ := f.s.creds(ctx, sourceKey("docs")); c.SecretAccessKey != docsSecret {
		t.Fatal("a test saves no key")
	}
	if set, _ := f.s.Settings(ctx); set.Sources[len(set.Sources)-1].Prefix != "ops/" {
		t.Fatal("a test saves no setting")
	}
	// The saved one is used only for the source being edited.
	if _, err := f.s.TestSourceAsTyped(ctx, admin, x, CredsChange{}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a new source has no saved key: %v", err)
	}
	if _, err := f.s.TestSourceAsTyped(ctx, admin, Source{Kind: SourceGit, URL: "https://git.example.com/r.git"}, CredsChange{}, "docs"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another kind: %v", err)
	}
	if _, err := f.s.TestSourceAsTyped(ctx, admin, x, CredsChange{}, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such source: %v", err)
	}
	// Through the API, with saved: the key never comes back.
	body, _ := json.Marshal(map[string]any{"source": x, "keys": map[string]string{}, "saved": "docs"})
	w := serve(t, f.s, &admin, "POST", "sources/test", string(body), nil)
	if !strings.Contains(w.Body.String(), `"ok":true`) || strings.Contains(w.Body.String(), docsSecret) || strings.Contains(w.Body.String(), docsKeyID) {
		t.Fatalf("the API: %s", w.Body)
	}
}

// timeout is a dial's error after its time ran out.
type timeout struct{}

func (timeout) Error() string   { return "i/o timeout" }
func (timeout) Timeout() bool   { return true }
func (timeout) Temporary() bool { return true }

// TestATestSaysWhatIsWrongKN26: an address that does not answer within
// the 10 s, one where nothing listens, plain HTTP where https was typed,
// and a key that may not read the bucket, each said plainly, naming
// host:port and what to check, never with the key.
func TestATestSaysWhatIsWrongKN26(t *testing.T) {
	f := newFixture(t)
	id, secret := docsKeyID, docsSecret
	keys := CredsChange{AccessKeyID: &id, SecretAccessKey: &secret}

	// The MinIO console's port, which does not answer the S3 API.
	var dialed string
	f.s.dial = func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		return nil, &net.OpError{Op: "dial", Net: network, Err: timeout{}}
	}
	minio := Source{Name: "MinIO", Kind: SourceS3, Bucket: "docs", Endpoint: "https://minio.camellio.com:9001", PathStyle: true}
	_, err := f.s.TestSourceAsTyped(ctx, admin, minio, keys, "")
	want := "Could not connect to minio.camellio.com:9001 within 10 s. Check the address and port (for MinIO, the S3 API is not the 9001 web console)."
	if err == nil || err.Error() != want || dialed != "minio.camellio.com:9001" {
		t.Fatalf("a dial timeout: %v (dialed %q)", err, dialed)
	}
	// Virtual-host style, the bucket is in the host name; AWS by its region.
	aws := Source{Name: "AWS", Kind: SourceS3, Bucket: "docs", Region: "eu-west-1"}
	_, err = f.s.TestSourceAsTyped(ctx, admin, aws, keys, "")
	if dialed != "docs.s3.eu-west-1.amazonaws.com:443" || err == nil || strings.Contains(err.Error(), "MinIO") {
		t.Fatalf("AWS: %v (dialed %q)", err, dialed)
	}
	f.s.dial = nil

	// Nothing listens.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	refused := Source{Name: "Refused", Kind: SourceS3, Bucket: "docs", Endpoint: "http://" + closed, PathStyle: true}
	if _, err := f.s.TestSourceAsTyped(ctx, admin, refused, keys, ""); err == nil || !strings.Contains(err.Error(), "Could not connect to "+closed+": nothing listens there") {
		t.Fatalf("nothing listens: %v", err)
	}

	// https typed, plain HTTP served.
	b := s3test.New("ops-docs", "eu-west-1", docsKey)
	defer b.Close()
	plain := typedBucket(b)
	plain.Endpoint = strings.Replace(b.URL, "http://", "https://", 1)
	if _, err := f.s.TestSourceAsTyped(ctx, admin, plain, keys, ""); err == nil || !strings.Contains(err.Error(), "does not answer in TLS") {
		t.Fatalf("plain HTTP: %v", err)
	}

	// A certificate not trusted: the private CA is asked for.
	tb := httptest.NewTLSServer(b.Handler())
	defer tb.Close()
	untrusted := typedBucket(b)
	untrusted.Endpoint, untrusted.CAPEM = tb.URL, otherCA(t)
	if _, err := f.s.TestSourceAsTyped(ctx, admin, untrusted, keys, ""); err == nil || !strings.Contains(err.Error(), "TLS certificate the console does not trust: set the private CA") {
		t.Fatalf("an untrusted certificate: %v", err)
	}

	// The key is known but may not read the bucket.
	b.Fail = func(*http.Request) (int, string) { return http.StatusForbidden, "AccessDenied" }
	_, err = f.s.TestSourceAsTyped(ctx, admin, typedBucket(b), keys, "")
	if err == nil || !strings.Contains(err.Error(), "The bucket answered: access denied — check the key's read permission on ops-docs") || strings.Contains(err.Error(), docsSecret) || strings.Contains(err.Error(), docsKeyID) {
		t.Fatalf("access denied: %v", err)
	}
}
