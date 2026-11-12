package awsv4

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The credentials and time of AWS's SigV4 test suite
// (aws-sig-v4-test-suite): service "service" in us-east-1, at
// example.amazonaws.com, on 30 August 2015.
var (
	suiteKey  = Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	suiteTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
)

func TestTheSigV4TestSuite(t *testing.T) {
	for _, c := range []struct {
		name, method, url, contentType, body, signed, sig string
	}{
		{"get-vanilla", "GET", "/", "", "", "host;x-amz-date", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"get-vanilla-empty-query-key", "GET", "/?Param1=value1", "", "", "host;x-amz-date", "a67d582fa61cc504c4bae71f336f98b97f1ea3c7a6bfe1b6e45aec72011b9aeb"},
		{"get-vanilla-query-order-key-case", "GET", "/?Param2=value2&Param1=value1", "", "", "host;x-amz-date", "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
		{"get-unreserved", "GET", "/-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", "", "", "host;x-amz-date", "07ef7494c76fa4850883e2b006601f940f8a34d404d0cfa977f52a65bbf5f24f"},
		{"get-vanilla-utf8-query", "GET", "/?%E1%88%B4=bar", "", "", "host;x-amz-date", "2cdec8eed098649ff3a119c94853b13c643bcf08f8b0a1d91e12c9027818dd04"},
		{"post-vanilla", "POST", "/", "", "", "host;x-amz-date", "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"post-vanilla-query", "POST", "/?Param1=value1", "", "", "host;x-amz-date", "28038455d6de14eafc1f9222cf5aa6f1a96197d7deb8263271d420d138af7f11"},
		{"post-x-www-form-urlencoded", "POST", "/", "application/x-www-form-urlencoded", "Param1=value1", "content-type;host;x-amz-date", "ff11897932ad3f4e8b18135d722051e5ac45fc38421b1da7b9d196a0fe09473a"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := http.NewRequest(c.method, "https://example.amazonaws.com"+c.url, strings.NewReader(c.body))
			if c.contentType != "" {
				r.Header.Set("Content-Type", c.contentType)
			}
			Sign(r, []byte(c.body), suiteKey, "us-east-1", "service", suiteTime)
			want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=" + c.signed + ", Signature=" + c.sig
			if got := r.Header.Get("Authorization"); got != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
		})
	}
}

// The example of AWS's documentation of Signature Version 4: IAM's
// ListUsers, signed on 30 August 2015.
func TestTheIAMExample(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	Sign(r, nil, suiteKey, "us-east-1", "iam", suiteTime)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := r.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// The examples of S3's documentation of header signing (examplebucket, 24
// May 2013): a ranged GET, a PUT with a storage class and a Date, a
// bucket's lifecycle and a listing.
func TestTheS3Examples(t *testing.T) {
	key := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, method, url, body string
		headers                 map[string]string
		signed, sig             string
	}{
		{"get-object", "GET", "/test.txt", "", map[string]string{"Range": "bytes=0-9"}, "host;range;x-amz-content-sha256;x-amz-date", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"put-object", "PUT", "/test$file.text", "Welcome to Amazon S3.", map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"}, "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class", "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"get-bucket-lifecycle", "GET", "/?lifecycle", "", nil, "host;x-amz-content-sha256;x-amz-date", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"list-objects", "GET", "/?max-keys=2&prefix=J", "", nil, "host;x-amz-content-sha256;x-amz-date", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := http.NewRequest(c.method, "https://examplebucket.s3.amazonaws.com"+c.url, strings.NewReader(c.body))
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			Sign(r, []byte(c.body), key, "us-east-1", "s3", at)
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=" + c.signed + ", Signature=" + c.sig
			if got := r.Header.Get("Authorization"); got != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
			if got := r.Header.Get("X-Amz-Content-Sha256"); got != SHA256Hex([]byte(c.body)) {
				t.Fatalf("the body's hash %s", got)
			}
		})
	}
}

// A session token is sent and signed; a body not signed says so.
func TestASessionTokenAndAnUnsignedBody(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPut, "https://b.s3.amazonaws.com/k", nil)
	r.Header.Set("X-Amz-Content-Sha256", UnsignedPayload)
	Sign(r, []byte("ignored"), Credentials{AccessKeyID: "A", SecretAccessKey: "S", SessionToken: "tok"}, "eu-west-1", "s3", suiteTime)
	if r.Header.Get("X-Amz-Security-Token") != "tok" || r.Header.Get("X-Amz-Content-Sha256") != UnsignedPayload || !strings.Contains(r.Header.Get("Authorization"), "SignedHeaders=host;x-amz-content-sha256;x-amz-date;x-amz-security-token,") {
		t.Fatalf("%v", r.Header)
	}
}

func TestTheRolesWebIdentityIsKeptUntilItNearlyExpires(t *testing.T) {
	calls := 0
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("WebIdentityToken") != "projected-jwt" || r.Form.Get("RoleArn") != "arn:aws:iam::123:role/backup" || r.Form.Get("RoleSessionName") != "221b-backup" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>ASIAROLE</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>st</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer sts.Close()
	env := map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123:role/backup", "AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/token"}
	id := &Identity{Session: "221b-backup", Ambient: &Ambient{Getenv: func(k string) string { return env[k] }, ReadFile: func(string) ([]byte, error) { return []byte("projected-jwt\n"), nil }, STSURL: sts.URL}}
	if !id.Available() {
		t.Fatal("a web identity is not seen")
	}
	for i := 0; i < 2; i++ {
		k, err := id.Credentials(context.Background(), "eu-west-1")
		if err != nil || k.AccessKeyID != "ASIAROLE" || k.SessionToken != "st" {
			t.Fatalf("%+v %v", k, err)
		}
	}
	if calls != 1 {
		t.Fatalf("STS was asked %d times", calls)
	}
	none := &Identity{Ambient: &Ambient{Getenv: func(string) string { return "" }}}
	if _, err := none.Credentials(context.Background(), ""); none.Available() || err == nil || !strings.Contains(err.Error(), "IRSA") {
		t.Fatalf("no identity: %v", err)
	}
}
