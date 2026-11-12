// Package awsv4 signs requests with AWS Signature Version 4 and finds the
// credentials to sign them with: a stored key, the environment's, or the
// pod's own identity (IRSA's web identity, EKS Pod Identity). Sherlock's
// Bedrock calls and the backups to S3 share it, so the console needs no
// AWS SDK.
package awsv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials are what a signature needs.
type Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
}

// UnsignedPayload stands for the body's hash when it is not signed (S3
// only), set as X-Amz-Content-Sha256 before Sign.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// SHA256Hex is the hex SHA-256 of b, as SigV4 hashes a body.
func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// Escape escapes a path or query value as SigV4 wants: everything but
// unreserved characters (and the slashes of a path).
func Escape(s string, path bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || (path && c == '/') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// signed says whether a header is signed: the host, the body's type and
// digest, a range, a date and every x-amz- header. Others (User-Agent,
// Accept-Encoding) may be changed on the way and are left out.
func signed(name string) bool {
	switch name {
	case "content-type", "content-md5", "range", "date":
		return true
	}
	return strings.HasPrefix(name, "x-amz-")
}

// Sign signs a request with AWS Signature Version 4, over its method,
// path, query, the host, the headers signed and the body. For S3 the
// body's hash is sent as X-Amz-Content-Sha256 unless one is set already
// (UnsignedPayload), and the path is escaped once; for every other service
// each segment is escaped twice, as SigV4 wants.
func Sign(r *http.Request, body []byte, k Credentials, region, service string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	r.Header.Set("X-Amz-Date", amzDate)
	if k.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", k.SessionToken)
	}
	payload := SHA256Hex(body)
	if service == "s3" {
		if set := r.Header.Get("X-Amz-Content-Sha256"); set != "" {
			payload = set
		} else {
			r.Header.Set("X-Amz-Content-Sha256", payload)
		}
	}
	host := r.URL.Host
	if r.Host != "" {
		host = r.Host
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		u, err := url.PathUnescape(s)
		if err != nil {
			u = s
		}
		segs[i] = Escape(u, false)
		if service != "s3" {
			segs[i] = Escape(segs[i], false)
		}
	}
	path = strings.Join(segs, "/")
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for key := range q {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var qs []string
	for _, key := range keys {
		vals := append([]string(nil), q[key]...)
		sort.Strings(vals)
		for _, v := range vals {
			qs = append(qs, Escape(key, false)+"="+Escape(v, false))
		}
	}
	headers := map[string]string{"host": host}
	for name, vals := range r.Header {
		if n := strings.ToLower(name); signed(n) {
			headers[n] = strings.Join(vals, ",")
		}
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.Join(strings.Fields(headers[n]), " ") + "\n")
	}
	signedNames := strings.Join(names, ";")
	canonical := strings.Join([]string{r.Method, path, strings.Join(qs, "&"), canonHeaders.String(), signedNames, payload}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + SHA256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+k.SecretAccessKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+k.AccessKeyID+"/"+scope+", SignedHeaders="+signedNames+", Signature="+sig)
}
