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
	}{
		{"a client with no proxy", "203.0.113.9:4000", nil, "203.0.113.9"},
		{"a client naming itself someone else", "203.0.113.9:4000", []string{"198.51.100.1"}, "203.0.113.9"},
		{"through a trusted proxy", "10.42.1.2:4000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"what the client wrote stays to the left", "10.42.1.2:4000", []string{"198.51.100.1, 203.0.113.9"}, "203.0.113.9"},
		{"through two trusted proxies", "10.42.1.2:4000", []string{"203.0.113.9, 192.0.2.7"}, "203.0.113.9"},
		{"on several header lines", "10.42.1.2:4000", []string{"203.0.113.9", "10.42.3.3"}, "203.0.113.9"},
		{"with a port", "10.42.1.2:4000", []string{"203.0.113.9:555"}, "203.0.113.9"},
		{"over IPv6", "[2001:db8::1]:4000", []string{"2001:db9::5"}, "2001:db9::5"},
		{"only proxies on the way", "10.42.1.2:4000", []string{"10.42.3.3"}, "10.42.3.3"},
		{"a trusted proxy that names nobody", "10.42.1.2:4000", nil, "10.42.1.2"},
		{"a trusted proxy that names nonsense", "10.42.1.2:4000", []string{"not-an-address"}, "10.42.1.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := proxies.Resolve(request(tc.peer, tc.forwarded...))
			if from(r) != tc.from {
				t.Fatalf("from %s", from(r))
			}
		})
	}
	// With none listed, nobody's header counts.
	r := Proxies(nil).Resolve(request("10.42.1.2:4000", "203.0.113.9"))
	if from(r) != "10.42.1.2" {
		t.Fatalf("from %s", from(r))
	}
	if _, err := ParseProxies([]string{"ingress"}); err == nil {
		t.Fatal("a name was taken for a CIDR")
	}
}

// A forwarded header does not widen an address's limit, whoever sends it;
// the limit per account holds as well.
func TestAForwardedHeaderDoesNotWidenTheLimit(t *testing.T) {
	proxies, _ := ParseProxies([]string{"10.42.0.0/16"})
	for _, tc := range []struct {
		name string
		r    func() *http.Request
	}{
		{"untrusted peer", func() *http.Request { return proxies.Resolve(request("203.0.113.9:4000", "198.51.100.1")) }},
		{"trusted proxy naming nobody", func() *http.Request { return proxies.Resolve(request("10.42.1.2:4000")) }},
		{"direct", func() *http.Request { return proxies.Resolve(request("203.0.113.9:4000")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{perAddress: newLimiter(3, time.Minute, time.Now), perAccount: newLimiter(100, time.Minute, time.Now)}
			got := 0
			for i := range 10 {
				if s.attempt(tc.r(), fmt.Sprintf("user%d", i)) {
					got++
				}
			}
			if got != 3 {
				t.Fatalf("%d attempts passed, want 3", got)
			}
		})
	}
	s := &Service{perAddress: newLimiter(100, time.Minute, time.Now), perAccount: newLimiter(5, time.Minute, time.Now)}
	for i := range 5 {
		if !s.attempt(request("203.0.113.9:4000"), "ops") {
			t.Fatalf("attempt %d at the account", i)
		}
	}
	if s.attempt(request("203.0.113.9:4000"), "ops") {
		t.Fatal("the limit per account did not hold")
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

// Attempts past the queue are refused before they cost a password check.
func TestAttemptsPastTheHashingQueueAreRefused(t *testing.T) {
	s := &Service{perAddress: newLimiter(100, time.Minute, time.Now), perAccount: newLimiter(100, time.Minute, time.Now)}
	r := httptest.NewRequest(http.MethodPost, "/signin", nil)
	hashWaiting.Add(hashQueue)
	defer hashWaiting.Add(-hashQueue)
	if s.attempt(r, "ann") {
		t.Fatal("an attempt passed a full queue")
	}
}

// Only so many password checks run at once.
func TestPasswordChecksRunInSlots(t *testing.T) {
	hash, err := HashPassword("a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	for range cap(hashSlots) {
		hashSlots <- struct{}{}
	}
	done := make(chan bool, 1)
	go func() { done <- CheckPassword(hash, "a long enough password") }()
	select {
	case <-done:
		t.Fatal("a check ran with no slot free")
	case <-time.After(100 * time.Millisecond):
	}
	for range cap(hashSlots) {
		<-hashSlots
	}
	if !<-done {
		t.Fatal("the check failed once a slot came free")
	}
}
