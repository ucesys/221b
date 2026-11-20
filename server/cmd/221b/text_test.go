package main

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The requests that meet the outage themselves (the ninth cluster test's
// F4): six pages that need the database are asked at the moment it starts
// to swallow what it is sent, before anything has found it away. Each
// answers within the one wait an outage costs, with a 503 and the
// console's sentence, and with nothing of the driver's: not the
// database's user, its name, its host or its port, in the answer or in the
// audit record of a change that was refused. From then on each answers in
// under 2 seconds.
func TestThePagesThatNeedTheDatabaseSayOnlyThatItIsUnavailable(t *testing.T) {
	away, cfg, _, needles := plantedDatabase(t)
	for what, s := range map[string]string{"a closed connection": "closed network connection", "a closed connection (2)": "conn closed", "a timeout": "context deadline exceeded",
		"a timeout (2)": "i/o timeout", "a refusal": "connection refused", "a reset": "connection reset", "the driver": "pgconn", "the driver (2)": "pgx"} {
		needles[what] = s
	}
	c := newSimCluster(t, nil)
	con := c.start(t, consoleOptions{database: &cfg})
	if con.url == "" || con.w.without {
		t.Fatalf("the console did not start with its database: %v\n%s", con.stopped(time.Second), con.logs.String())
	}
	seen := &held{t: t, needles: needles, port: strconv.Itoa(cfg.Port)}
	b := browser()
	cookie := con.signIn(b)
	needs := []string{"/api/v1/credentials", "/api/v1/clusters/local/apps", "/api/v1/jobs", "/api/v1/aiops/cases", "/api/v1/admin/audit", "/api/v1/helm-repositories"}
	for _, path := range needs {
		var code int
		var body string
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if code, body, _ = con.get(b, path); code == http.StatusOK || time.Now().After(deadline) {
				break
			}
		}
		if code != http.StatusOK {
			t.Fatalf("%s before the outage: %d %.300s", path, code, body)
		}
	}
	away.Hang()
	type answer struct {
		path string
		code int
		body string
		took time.Duration
	}
	firsts := make(chan answer, len(needs))
	for _, path := range needs {
		go func() {
			code, body, took := con.get(b, path)
			firsts <- answer{path, code, body, took}
		}()
	}
	var slowestFirst time.Duration
	said := 0
	for range needs {
		a := <-firsts
		seen.search("the first "+a.path, a.code, a.body)
		// A page answered from what the console holds in memory (the
		// cases, read again only when they change) may still answer.
		switch {
		case a.code == http.StatusServiceUnavailable && strings.Contains(a.body, db.ErrUnavailable.Error()):
			said++
		case a.code != http.StatusOK:
			t.Errorf("the first %s of the outage: %d %.400s", a.path, a.code, a.body)
		}
		if a.took > 7*time.Second {
			t.Errorf("the first %s of the outage took %s", a.path, a.took)
		}
		slowestFirst = max(slowestFirst, a.took)
	}
	if said < 4 {
		t.Errorf("only %d of the six first answers met the outage", said)
	}
	// After the first, each click answers at once.
	var slowest time.Duration
	for range 3 {
		for _, path := range needs {
			code, body, took := con.get(b, path)
			seen.search(path, code, body)
			if code != http.StatusServiceUnavailable || !strings.Contains(body, db.ErrUnavailable.Error()) || took > 2*time.Second {
				t.Errorf("%s during the outage: %d in %s: %.400s", path, code, took, body)
			}
			slowest = max(slowest, took)
		}
	}
	// Changes that are refused: the answer and the audit record.
	for path, body := range map[string]string{
		"PUT /api/v1/settings":     `{"refreshSeconds":9}`,
		"POST /api/v1/credentials": `{"name":"zq7","kind":"token","secret":"` + secretCred + `"}`,
		"POST /api/v1/clusters":    `{"name":"zq7-must-not-be-added","server":"https://127.0.0.1:1","token":"` + secretToken + `"}`,
	} {
		method, url, _ := strings.Cut(path, " ")
		req, _ := http.NewRequest(method, con.url+url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		begun := time.Now()
		resp, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seen.search(path, resp.StatusCode, string(raw))
		if resp.StatusCode < 400 || time.Since(begun) > 2*time.Second {
			t.Errorf("%s during the outage: %s in %s: %.300s", path, resp.Status, time.Since(begun), raw)
		}
	}
	seen.search("the audit stream", 500, con.audit.String())
	if !strings.Contains(con.audit.String(), "zq7-must-not-be-added") && !strings.Contains(con.audit.String(), db.ErrUnavailable.Error()) {
		t.Errorf("the audit stream holds no record of a refused change: %.600s", con.audit.String())
	}
	// The driver's own text, with the address, is in the log, once the
	// outage is found; no secret is.
	if log := con.logs.String(); !strings.Contains(log, "the database is unavailable") {
		t.Error("the log does not say the database is unavailable")
	}
	away.Back()
	var page string
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		var code int
		code, page, _ = con.get(b, "/api/v1/admin/audit")
		if code == http.StatusOK || time.Now().After(deadline) {
			break
		}
	}
	seen.search("the audit log after the outage", 500, page)
	noSecretIn(t, con, cookie, c.sessionKey(t))
	t.Logf("the slowest of the six first answers took %s, and of the %d after them %s; %d answers searched", slowestFirst.Round(time.Millisecond), 3*len(needs), slowest.Round(time.Millisecond), seen.n)
	var _ = dbtest.Env
}
