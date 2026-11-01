package auth

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(peer string, forwarded ...string) *http.Request {
	r := httptest.NewRequest("POST", "/signin", nil)
	r.RemoteAddr = peer
	for _, v := range forwarded {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestOnlyATrustedProxyNamesTheClient(t *testing.T) {
	proxies, err := ParseProxies([]string{"10.42.0.0/16", "192.0.2.7", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer string
		forwarded  []string
		from       string
		shared     bool
	}{
		{"a client with no proxy", "203.0.113.9:4000", nil, "203.0.113.9", false},
		{"a client naming itself someone else", "203.0.113.9:4000", []string{"198.51.100.1"}, "203.0.113.9", true},
		{"through a trusted proxy", "10.42.1.2:4000", []string{"203.0.113.9"}, "203.0.113.9", false},
		{"what the client wrote stays to the left", "10.42.1.2:4000", []string{"198.51.100.1, 203.0.113.9"}, "203.0.113.9", false},
		{"through two trusted proxies", "10.42.1.2:4000", []string{"203.0.113.9, 192.0.2.7"}, "203.0.113.9", false},
		{"on several header lines", "10.42.1.2:4000", []string{"203.0.113.9", "10.42.3.3"}, "203.0.113.9", false},
		{"with a port", "10.42.1.2:4000", []string{"203.0.113.9:555"}, "203.0.113.9", false},
		{"over IPv6", "[2001:db8::1]:4000", []string{"2001:db9::5"}, "2001:db9::5", false},
		{"only proxies on the way", "10.42.1.2:4000", []string{"10.42.3.3"}, "10.42.3.3", false},
		{"a trusted proxy that names nobody", "10.42.1.2:4000", nil, "10.42.1.2", true},
		{"a trusted proxy that names nonsense", "10.42.1.2:4000", []string{"not-an-address"}, "10.42.1.2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := proxies.Resolve(request(tc.peer, tc.forwarded...))
			if from(r) != tc.from || SharedAddress(r) != tc.shared {
				t.Fatalf("from %s shared %v", from(r), SharedAddress(r))
			}
		})
	}
	// With none listed, nobody's header counts.
	r := Proxies(nil).Resolve(request("10.42.1.2:4000", "203.0.113.9"))
	if from(r) != "10.42.1.2" || !SharedAddress(r) {
		t.Fatalf("from %s shared %v", from(r), SharedAddress(r))
	}
	if _, err := ParseProxies([]string{"ingress"}); err == nil {
		t.Fatal("a name was taken for a CIDR")
	}
}

// Behind a proxy the console does not trust, one client's attempts cannot
// use up an address everyone shares; the limit per account still holds.
func TestOneClientBehindAProxyDoesNotLockEveryoneOut(t *testing.T) {
	s := &Service{perAddress: newLimiter(3, time.Minute, time.Now), perAccount: newLimiter(5, time.Minute, time.Now)}
	behind := func() *http.Request { return Proxies(nil).Resolve(request("10.42.1.2:4000", "203.0.113.9")) }
	for i := range 50 {
		if !s.attempt(behind(), fmt.Sprintf("guess-%d", i)) {
			t.Fatalf("attempt %d was refused for the proxy's address", i)
		}
	}
	for i := range 5 {
		if !s.attempt(behind(), "ops") {
			t.Fatalf("attempt %d at the account", i)
		}
	}
	if s.attempt(behind(), "ops") {
		t.Fatal("the limit per account did not hold")
	}
	// A client's own address keeps its limit.
	direct := func() *http.Request { return Proxies(nil).Resolve(request("203.0.113.9:4000")) }
	for i := range 3 {
		if !s.attempt(direct(), fmt.Sprintf("spray-%d", i)) {
			t.Fatalf("attempt %d", i)
		}
	}
	if s.attempt(direct(), "spray-last") {
		t.Fatal("the limit per address did not hold")
	}
}

// The limiter's memory is bounded whatever is sent: key length does not
// matter, and past the bound it forgets rather than grows.
func TestTheLimiterForgetsRatherThanGrows(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	l := newLimiter(5, time.Minute, func() time.Time { return now })
	l.keys = 100
	for i := range 1000 {
		now = now.Add(time.Millisecond)
		if !l.allow(strings.Repeat("x", 1<<20) + fmt.Sprint(i)) {
			t.Fatalf("attempt %d", i)
		}
		if len(l.hits) > l.keys {
			t.Fatalf("%d keys after %d attempts", len(l.hits), i)
		}
	}
	// The newest are the ones kept.
	k := sha256.Sum256([]byte(strings.Repeat("x", 1<<20) + "999"))
	if len(l.hits[k]) != 1 {
		t.Fatal("the newest key was forgotten")
	}
	// Keys past their window go first.
	now = now.Add(time.Hour)
	l.allow("one more")
	for i := range 100 {
		l.allow(fmt.Sprint("again-", i))
	}
	if len(l.hits) > l.keys {
		t.Fatalf("%d keys", len(l.hits))
	}
}
