package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// With the database away, what fails at every look says so once, and
// once when it works again (DB-15; the ninth cluster test logged about
// 200 lines a minute): the AI configuration's reads, Sherlock's spend,
// the saved settings, the audit log's saves, each route answered 503
// for it, and (the twelfth cluster test's N1) Getting started's record,
// read at every look at Getting started, the jobs, the hosts, the
// proposals' sweep, the Wiki sync jobs' sources and the cases' catch-up
// with the proposed changes.
func TestTheLogSaysAnOutageOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("an outage of half a minute")
	}
	away, cfg, _, _ := plantedDatabase(t)
	c := newSimCluster(t, nil)
	con := c.start(t, consoleOptions{database: &cfg})
	if con.url == "" {
		t.Fatalf("the console did not start with its database: %v\n%s", con.stopped(time.Second), con.logs.String())
	}
	b := browser()
	con.signIn(b)
	for _, path := range []string{"/api/v1/jobs", "/api/v1/sherlock/spend"} {
		con.get(b, path)
	}
	before := len(con.logs.String())
	away.Cut()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		con.get(b, "/api/v1/home")
		con.get(b, "/api/v1/jobs")
		con.get(b, "/api/v1/jobs")
		con.get(b, "/api/v1/setup")
	}
	during := con.logs.String()[before:]
	for _, line := range []string{"reading the AI providers failed", "reading the Sherlock spend failed", "cannot read the saved settings", "path=/api/v1/jobs code=503", "saving the audit log failed", "expiring the run requests failed",
		"reading Getting started's record failed", "the jobs cannot be read", "the hosts could not be read", "the proposals could not be read",
		"the sources could not be read for their Wiki sync jobs", "the proposed changes could not be read to catch up with them"} {
		if n := strings.Count(during, line); n > 1 {
			t.Errorf("%q is said %d times in half a minute of the outage", line, n)
		}
	}
	if n := strings.Count(during, "\n"); n > 25 {
		t.Errorf("%d lines in half a minute of the outage:\n%s", n, during)
	}
	away.Back()
	back := len(con.logs.String())
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		code, _, _ := con.get(b, "/api/v1/jobs")
		con.get(b, "/api/v1/setup")
		after := con.logs.String()[back:]
		if code == http.StatusOK && strings.Contains(after, "requests are answered again") && strings.Contains(after, "the AI configuration is read again") && strings.Contains(after, "the audit log is saved again") &&
			strings.Contains(after, "Getting started's record is read again") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log does not say the reads work again (%d):\n%s", code, after)
		}
	}
}
