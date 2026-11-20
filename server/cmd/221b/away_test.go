package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The database taken away under a running console (the ninth cluster
// test's F3 and F4), on the whole console as the chart starts it: serve,
// the simulated API server, and a PostgreSQL behind a relay that is cut,
// or made to swallow what it is sent, while people use the console.

const (
	// The names the test's database is reached by: no answer a person
	// gets may hold one of them.
	plantedUser = "zq7_console_user"
	plantedHost = "zq7-database.localhost"
)

// plantedDatabase is a database of the test's own behind a relay, reached
// under a user, a name, a host and a port the test can search for, with
// those names: the user and the host are the planted ones where the test
// server and the resolver allow it, and what the test server gave where
// they do not.
func plantedDatabase(t *testing.T) (*dbtest.Away, db.Config, db.Config, map[string]string) {
	t.Helper()
	direct := dbtest.Config(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable", direct.Host, direct.Port, direct.User, direct.Name)
	password := os.Getenv(dbtest.Env + "_PASSWORD")
	if password != "" {
		dsn += " password=" + password
	}
	if admin, err := pgx.Connect(ctx, dsn); err == nil {
		_, err = admin.Exec(ctx, `DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '`+plantedUser+`') THEN CREATE ROLE `+plantedUser+` LOGIN SUPERUSER; END IF; END $$`)
		if err == nil && password != "" {
			_, err = admin.Exec(ctx, "ALTER ROLE "+plantedUser+" PASSWORD "+quoteLiteral(password))
		}
		admin.Close(ctx)
		if err == nil {
			as := strings.Replace(dsn, "user="+direct.User, "user="+plantedUser, 1)
			if c, err := pgx.Connect(ctx, as); err == nil {
				c.Close(ctx)
				direct.User = plantedUser
			}
		}
	}
	away, relayed := dbtest.NewAway(t, direct)
	if addrs, err := net.DefaultResolver.LookupHost(ctx, plantedHost); err == nil && len(addrs) > 0 && addrs[0] == "127.0.0.1" {
		relayed.Host = plantedHost
	}
	port := strconv.Itoa(relayed.Port)
	needles := map[string]string{
		"the database's user":       relayed.User,
		"the database's name":       relayed.Name,
		"the database's address":    net.JoinHostPort(relayed.Host, port),
		"the driver's words":        "failed to connect",
		"the driver's words (2)":    "dial error",
		"the driver's words (3)":    "user=",
		"the driver's words (4)":    "SQLSTATE",
		"the relay's address":       net.JoinHostPort("127.0.0.1", port),
		"the test server's address": net.JoinHostPort(direct.Host, strconv.Itoa(direct.Port)),
	}
	if relayed.Host == plantedHost {
		needles["the database's host"] = plantedHost
	}
	return away, relayed, direct, needles
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// held is every answer a person was given, to be searched.
type held struct {
	t       *testing.T
	needles map[string]string
	port    string
	n       int
}

// search fails the test when an answer holds one of the names the
// database is reached by; an answer that is an error is searched for the
// port alone as well.
func (h *held) search(what string, code int, body string) {
	h.t.Helper()
	h.n++
	for name, s := range h.needles {
		if s != "" && strings.Contains(body, s) {
			h.t.Errorf("%s (%d) shows %s: %.400s", what, code, name, body)
		}
	}
	if code >= 400 && strings.Contains(body, h.port) {
		h.t.Errorf("%s (%d) shows the database's port: %.400s", what, code, body)
	}
}

func metric(body, name string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") {
			return strings.TrimPrefix(line, name+" ")
		}
	}
	return ""
}

// With the database taken away while the console runs, by a cut and by a
// database that swallows what it is sent: /metrics answers from memory at
// every moment and says the database is away; the first request to meet
// the outage may wait once, for the probe; from then on the home page and
// every page that needs the database answer at once, the second with the
// console's sentence and a 503 and never with the driver's text; a person
// signs in and lands on a home page that answers; and when the database
// is back everything works again within seconds, without a restart, with
// the audit events of the outage in the audit log.
func TestARunningConsoleDoesNotWaitForADatabaseThatIsAway(t *testing.T) {
	for name, takeAway := range map[string]func(*dbtest.Away){
		"a database that is not there":             (*dbtest.Away).Cut,
		"a database that swallows what it is sent": (*dbtest.Away).Hang,
	} {
		t.Run(name, func(t *testing.T) {
			away, cfg, _, needles := plantedDatabase(t)
			c := newSimCluster(t, nil)
			con := c.start(t, consoleOptions{database: &cfg})
			if con.url == "" || con.w.without {
				t.Fatalf("the console did not start with its database: %v\n%s", con.stopped(time.Second), con.logs.String())
			}
			seen := &held{t: t, needles: needles, port: strconv.Itoa(cfg.Port)}
			b := browser()
			cookie := con.signIn(b)
			if strings.HasPrefix(cookie, "s1.") || cookie == "" {
				t.Fatal("the session of a sign-in with the database is not a stored one")
			}
			needs := []string{"/api/v1/credentials", "/api/v1/clusters/local/apps", "/api/v1/jobs", "/api/v1/aiops/cases", "/api/v1/admin/audit", "/api/v1/helm-repositories"}
			// Before: everything answers.
			for _, path := range append([]string{"/api/v1/home", "/api/v1/database"}, needs...) {
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
			if code, body, _ := con.get(http.DefaultClient, "/metrics"); code != http.StatusOK || metric(body, "b221_database_up") != "1" {
				t.Fatalf("/metrics before the outage: %d, b221_database_up %q", code, metric(body, "b221_database_up"))
			}
			// The copy of the session's row a replica answers from runs out,
			// so that the requests from here would ask the database.
			time.Sleep(5200 * time.Millisecond)

			takeAway(away)
			gone := time.Now()
			scrape := func(when string) string {
				t.Helper()
				code, body, took := con.get(http.DefaultClient, "/metrics")
				seen.search("/metrics "+when, code, body)
				if code != http.StatusOK || took > 2*time.Second {
					t.Errorf("/metrics %s: %d in %s", when, code, took)
				}
				return metric(body, "b221_database_up")
			}
			// The metrics answer at once from the first moment, whatever
			// they can say yet.
			scrape("as the database goes")
			// The first request to meet the outage: the home page, as the
			// browser asks it. It may wait once.
			code, body, first := con.get(b, "/api/v1/home")
			seen.search("the first /api/v1/home", code, body)
			if code != http.StatusOK || first > 7*time.Second {
				t.Errorf("the first request after the database went answered %d in %s: %.300s", code, first, body)
			}
			var found time.Duration
			for {
				if scrape("while the outage is being found") == "0" {
					found = time.Since(gone)
					break
				}
				if time.Since(gone) > 20*time.Second {
					t.Fatalf("/metrics does not say the database is away after %s; the log:\n%s", time.Since(gone), con.logs.String())
				}
				time.Sleep(100 * time.Millisecond)
			}

			// From here on nothing waits. Three rounds, some seconds apart,
			// so that every breaker and every held copy has run out between.
			var slowestHome, slowestMetrics, slowestNeeds time.Duration
			for round := range 3 {
				code, body, took := con.get(http.DefaultClient, "/metrics")
				seen.search("/metrics", code, body)
				if code != http.StatusOK || metric(body, "b221_database_up") != "0" || took > 2*time.Second {
					t.Errorf("/metrics during the outage: %d, b221_database_up %q, in %s", code, metric(body, "b221_database_up"), took)
				}
				slowestMetrics = max(slowestMetrics, took)
				code, body, took = con.get(b, "/api/v1/home")
				seen.search("/api/v1/home", code, body)
				if code != http.StatusOK || !strings.Contains(body, `"id":"local"`) || !strings.Contains(body, `"nodes":{"total":3`) || took > 3*time.Second {
					t.Errorf("/api/v1/home during the outage: %d in %s: %.300s", code, took, body)
				}
				slowestHome = max(slowestHome, took)
				for _, path := range needs {
					code, body, took := con.get(b, path)
					seen.search(path, code, body)
					if code != http.StatusServiceUnavailable || !strings.Contains(body, db.ErrUnavailable.Error()) {
						t.Errorf("%s during the outage: %d %.300s", path, code, body)
					}
					if took > 2*time.Second {
						t.Errorf("%s took %s to say the database is unavailable", path, took)
					}
					slowestNeeds = max(slowestNeeds, took)
				}
				code, body, took = con.get(b, "/api/v1/database")
				seen.search("/api/v1/database", code, body)
				if code != http.StatusOK || !strings.Contains(body, `"available":false`) || !strings.Contains(body, `"error":"`) || took > 2*time.Second {
					t.Errorf("/api/v1/database during the outage: %d in %s: %.300s", code, took, body)
				}
				// What is read live goes on.
				if code, body, took := con.get(b, "/api/v1/clusters/local/snapshot"); code != http.StatusOK || !strings.Contains(body, `"nodes"`) || took > 2*time.Second {
					t.Errorf("the local cluster's snapshot during the outage: %d in %s", code, took)
				}
				if round < 2 {
					time.Sleep(4 * time.Second)
				}
			}
			// A change is refused with the same sentence, and its audit
			// record carries no more.
			req, _ := http.NewRequest(http.MethodPut, con.url+"/api/v1/settings", strings.NewReader(`{"refreshSeconds":9}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			begun := time.Now()
			if resp, err := b.Do(req); err != nil {
				t.Error(err)
			} else {
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				seen.search("saving the settings", resp.StatusCode, string(raw))
				if resp.StatusCode != http.StatusServiceUnavailable || time.Since(begun) > 2*time.Second {
					t.Errorf("saving the settings during the outage: %s in %s: %s", resp.Status, time.Since(begun), raw)
				}
			}
			// A person signs in during the outage, and lands on a home page
			// that answers.
			other := browser()
			begun = time.Now()
			signed := con.signIn(other)
			if !strings.HasPrefix(signed, "s1.") || time.Since(begun) > 3*time.Second {
				t.Errorf("a sign-in during the outage took %s and gave a cookie of %d characters", time.Since(begun), len(signed))
			}
			if code, body, took := con.get(other, "/api/v1/home"); code != http.StatusOK || !strings.Contains(body, `"id":"local"`) || took > 3*time.Second {
				t.Errorf("the home page after a sign-in during the outage: %d in %s: %.300s", code, took, body)
			}
			seen.search("the audit stream during the outage", 500, con.audit.String())

			// The database is back: within seconds, and without a restart.
			away.Back()
			returned := time.Now()
			var back time.Duration
			for {
				if code, _, _ := con.get(b, "/api/v1/credentials"); code == http.StatusOK {
					back = time.Since(returned)
					break
				}
				if time.Since(returned) > 15*time.Second {
					t.Fatalf("the credentials do not answer %s after the database is back; the log:\n%s", time.Since(returned), con.logs.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
			for _, path := range append([]string{"/api/v1/home", "/api/v1/database"}, needs...) {
				code, body, took := con.get(b, path)
				seen.search(path+" after the outage", code, body)
				if code != http.StatusOK || took > 3*time.Second {
					t.Errorf("%s after the outage: %d in %s: %.300s", path, code, took, body)
				}
			}
			if up := scrape("after the outage"); up != "1" {
				t.Errorf("/metrics after the outage: b221_database_up %q", up)
			}
			// The person who signed in during the outage goes on, with a
			// stored session that ends when the signed one would have, and
			// the exchange is recorded.
			if code, _, _ := con.get(other, "/api/v1/me"); code != http.StatusOK {
				t.Errorf("the person signed in during the outage, after it: %d", code)
			}
			if exchanged := con.cookie(other); exchanged == signed || len(exchanged) != 43 {
				t.Errorf("the signed session was not exchanged after the outage (%d characters)", len(exchanged))
			}
			// The audit log's queue is written out: the events of the
			// outage are in the audit log's page.
			var page string
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
				_, page, _ = con.get(b, "/api/v1/admin/audit")
				if (strings.Contains(page, "session.exchange") && strings.Contains(page, "the session store was unavailable")) || time.Now().After(deadline) {
					break
				}
			}
			if !strings.Contains(page, "session.exchange") || !strings.Contains(page, "the session store was unavailable") {
				t.Errorf("the audit log after the outage lacks the outage's events: %.600s", page)
			}
			seen.search("the audit log after the outage", 500, page)
			seen.search("the audit stream", 500, con.audit.String())
			if seen.n < 40 {
				t.Errorf("only %d answers were searched", seen.n)
			}
			// The log has the driver's text, with the address, once the
			// outage is found; it has no cookie and no key.
			if !strings.Contains(con.logs.String(), "the database is unavailable") || !strings.Contains(con.logs.String(), "the database answers again") {
				t.Errorf("the log does not say when the database left and came back")
			}
			noSecretIn(t, con, cookie, signed, con.cookie(other), c.sessionKey(t))
			t.Logf("%s: planted %v; the first request after it went took %s; /metrics said so %s after it went; after that the slowest home page took %s, the slowest scrape %s, the slowest page that needs the database %s; it was back %s after it returned; %d answers searched",
				name, needles, first.Round(time.Millisecond), found.Round(time.Millisecond), slowestHome.Round(time.Millisecond), slowestMetrics.Round(time.Millisecond), slowestNeeds.Round(time.Millisecond), back.Round(time.Millisecond), seen.n)
		})
	}
}
