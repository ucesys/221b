package awsv4

import (
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Presign signs a request in its query, as a presigned URL is signed: the
// signature and what it covers are query parameters (X-Amz-Algorithm,
// -Credential, -Date, -Expires, -SignedHeaders, -Security-Token and
// -Signature), the host is the one header signed, and payload is the
// body's hash as the service wants it (UnsignedPayload for S3). The URL's
// path is escaped once for S3 and twice for every other service, as Sign
// does. It returns the URL with the query added.
func Presign(method string, u *url.URL, k Credentials, region, service string, now time.Time, expires time.Duration, payload string) *url.URL {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", k.AccessKeyID+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires/time.Second)))
	q.Set("X-Amz-SignedHeaders", "host")
	if k.SessionToken != "" {
		q.Set("X-Amz-Security-Token", k.SessionToken)
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		p, err := url.PathUnescape(s)
		if err != nil {
			p = s
		}
		segs[i] = Escape(p, false)
		if service != "s3" {
			segs[i] = Escape(segs[i], false)
		}
	}
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
	query := strings.Join(qs, "&")
	canonical := strings.Join([]string{method, strings.Join(segs, "/"), query, "host:" + u.Host + "\n", "host", payload}, "\n")
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + SHA256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+k.SecretAccessKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	out := *u
	out.RawQuery = query + "&X-Amz-Signature=" + hex.EncodeToString(hmacSHA256(key, toSign))
	return &out
}

// RDSTokenLife is how long an RDS IAM authentication token is taken for a
// sign-in: AWS takes it for 15 minutes from when it was made.
const RDSTokenLife = 15 * time.Minute

// RDSAuthToken is an RDS IAM authentication token, given as the password
// of a sign-in as user to the database at endpoint (host:port): a GET of
// Action=connect presigned for the service rds-db, without its scheme, as
// the AWS SDKs' BuildAuthToken makes it. It is made where it is used, with
// no call to AWS.
func RDSAuthToken(endpoint, region, user string, k Credentials, now time.Time) string {
	u := &url.URL{Scheme: "https", Host: endpoint, Path: "/", RawQuery: "Action=connect&DBUser=" + url.QueryEscape(user)}
	signed := Presign("GET", u, k, region, "rds-db", now, RDSTokenLife, SHA256Hex(nil))
	return endpoint + "?" + signed.RawQuery
}
