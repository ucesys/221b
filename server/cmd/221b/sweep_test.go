package main

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/knowledge"
)

// What a service registers under its own prefix, which the console's
// list of routes holds as one pattern: the GET routes the browser app
// calls (web/src/api/issues.ts, web/src/report/api.ts,
// web/src/api/knowledge.ts).
var serviceGETs = []string{
	issues.Prefix + "me", issues.Prefix + "settings", issues.Prefix + "reports", issues.Prefix + "reports?open=true", issues.Prefix + "known", issues.Prefix + "known?all=true",
	issues.Prefix + "services", issues.Prefix + "updates?since=2026-11-20T00:00:00Z",
	knowledge.Prefix + "settings", knowledge.Prefix + "status", knowledge.Prefix + "pages", knowledge.Prefix + "wiki", knowledge.Prefix + "uploads", knowledge.Prefix + "items",
	knowledge.Prefix + "requests", knowledge.Prefix + "requests?all=1", knowledge.Prefix + "drafts", knowledge.Prefix + "drafts?all=1", knowledge.Prefix + "drafts/count", knowledge.Prefix + "search?q=etcd",
}

// The same services' GET routes with a name in their path, asked with a
// made-up one: the issue service's every GET route is swept.
var serviceNamedGETs = []string{issues.Prefix + "reports/1", issues.Prefix + "known/x1"}

// What a route is asked with, where it refuses a request without it
// before it reads anything.
var sweepQueries = map[string]string{
	"/api/v1/aiops/alert-cases":   "?cluster=local",
	"/api/v1/aiops/objects":       "?cluster=local&q=a",
	"/api/v1/credentials/choices": "?use=git",
}

// The routes that answer neither 200 nor 503 in this console, and why:
// none of them asked the database. Everything else named without a
// made-up name answers 200 or says the database is unavailable.
var sweepOthers = map[string]struct {
	code int
	why  string
}{
	"/api/v1/hosts":                   {http.StatusConflict, "Automation over SSH is off in this console (toolbox.ssh.enabled)"},
	"/api/v1/clusters/local/hosts":    {http.StatusConflict, "Automation over SSH is off in this console"},
	"/api/v1/netcheck":                {http.StatusBadRequest, "a tool that asks for its from, destination and port first"},
	"/api/v1/clusters/local/netcheck": {http.StatusBadRequest, "a tool that asks for its from, destination and port first"},
	"/api/v1/sherlock/prompt":         {http.StatusConflict, "Sherlock has no API key in this console"},
}

// With the database away, every GET route of the API either answers (200:
// read live from the cluster, or from what the console holds in memory)
// or says the database is unavailable: a 503, marked
// (db.UnavailableHeader), with the console's sentence. None answers 500,
// 502 or 504, and none of those asked without a made-up name answers 400
// or anything else that reads as a fault of the request or of the
// console. The tenth cluster test's N1: the issue service's /me and
// /settings answered 500, their sentence worded apart (a capital and a
// full stop), and a proposed change's own routes did the same.
//
// The routes are the console's own list (every pattern it registered),
// so a new route is swept without being added here; a route with a name
// in its path is asked with a made-up one, where a 404 or a 400 for the
// name is an answer too.
func TestWithTheDatabaseAwayEveryGETRouteAnswersOrSaysSo(t *testing.T) {
	away, cfg, _, _ := plantedDatabase(t)
	c := newSimCluster(t, nil)
	con := c.start(t, consoleOptions{database: &cfg})
	if con.url == "" {
		t.Fatalf("the console did not start with its database: %v\n%s", con.stopped(time.Second), con.logs.String())
	}
	b := browser()
	con.signIn(b)
	// Before: the two routes of the finding answer.
	for _, path := range []string{issues.Prefix + "me", issues.Prefix + "settings", "/api/v1/database"} {
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
	// The copy of the session's row a replica answers from runs out, so
	// that the requests from here would ask the database.
	time.Sleep(5200 * time.Millisecond)
	away.Cut()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		con.get(b, "/api/v1/credentials")
		if _, body, _ := con.get(b, "/api/v1/database"); strings.Contains(body, `"available":false`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/api/v1/database does not say the database is away; the log:\n%s", con.logs.String())
		}
	}

	type route struct {
		pattern, url string
		// named: asked with a made-up name in its path.
		named bool
	}
	var routes []route
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, p := range con.w.handler.Patterns() {
		method, path, ok := strings.Cut(p, " ")
		// A prefix is a service's own routes (below), or the answer for a
		// path nothing serves.
		if !ok || method != http.MethodGet || !strings.HasPrefix(path, "/api/") || strings.HasSuffix(path, "/") {
			continue
		}
		url := strings.ReplaceAll(path, "{cluster}", "local")
		named := param.MatchString(url)
		url = param.ReplaceAllString(url, "x1")
		routes = append(routes, route{path, url + sweepQueries[path], named})
	}
	for _, u := range serviceGETs {
		routes = append(routes, route{u, u, false})
	}
	for _, u := range serviceNamedGETs {
		routes = append(routes, route{u, u, true})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].url < routes[j].url })
	if len(routes) < 150 {
		t.Fatalf("only %d GET routes to ask", len(routes))
	}

	var served, said, streams, other, named int
	for _, rt := range routes {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, con.url+rt.url, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Accept", "application/json")
		begun := time.Now()
		resp, err := b.Do(req)
		if err != nil {
			cancel()
			t.Errorf("GET %s: %v", rt.url, err)
			continue
		}
		var body string
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			// A stream that opened: it is served, and never ends.
			body = "(an event stream)"
			streams++
		} else {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			body = string(raw)
		}
		resp.Body.Close()
		cancel()
		code, marked, sentence := resp.StatusCode, db.MarkedUnavailable(resp.Header), strings.Contains(body, db.ErrUnavailable.Error())
		t.Logf("%d %s%s", code, rt.url, map[bool]string{true: "  (the database is unavailable)", false: ""}[code == http.StatusServiceUnavailable && marked])
		if took := time.Since(begun); took > 3*time.Second {
			t.Errorf("GET %s took %s", rt.url, took)
		}
		// Whatever the route: never a fault of the console's, and the
		// sentence never under another status than 503 or 200 (a page
		// that answers may say which of its parts it could not read).
		if code == http.StatusInternalServerError || code == http.StatusBadGateway || code == http.StatusGatewayTimeout {
			t.Errorf("GET %s: %d %.300s", rt.url, code, body)
			continue
		}
		if (sentence || marked) && code != http.StatusServiceUnavailable && code != http.StatusOK {
			t.Errorf("GET %s says the database is unavailable with a %d: %.300s", rt.url, code, body)
			continue
		}
		switch {
		case code == http.StatusServiceUnavailable:
			// A 503 here is the database's, said in the console's words
			// and marked for the browser app.
			if !marked || !sentence {
				t.Errorf("GET %s: a 503 that is not the database's (marked %v): %.300s", rt.url, marked, body)
			}
			if strings.Contains(body, "The database is unavailable") || strings.Contains(body, "still work.") {
				t.Errorf("GET %s words the sentence apart: %.300s", rt.url, body)
			}
			said++
		case marked:
			t.Errorf("GET %s: marked as the database's with a %d", rt.url, code)
		case code == http.StatusOK:
			served++
		case rt.named:
			// A made-up name: no such thing (404), or not a name (400), or
			// a part that is off (409), each before anything is read.
			if code != http.StatusNotFound && code != http.StatusBadRequest && code != http.StatusConflict && code != http.StatusUnauthorized {
				t.Errorf("GET %s (a made-up name): %d %.300s", rt.url, code, body)
			}
			named++
		default:
			if o, ok := sweepOthers[rt.url]; !ok || o.code != code {
				t.Errorf("GET %s: %d, neither served nor the database's: %.300s", rt.url, code, body)
			}
			other++
		}
	}
	// The finding itself, by name.
	for _, path := range []string{issues.Prefix + "me", issues.Prefix + "settings", issues.Prefix + "reports", issues.Prefix + "known", "/api/v1/aiops/proposals/x1", "/api/v1/admin/approvals", "/api/v1/aiops/proposals"} {
		code, body, _ := con.get(b, path)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, `"error":"`) || !strings.Contains(body, db.ErrUnavailable.Error()) {
			t.Errorf("GET %s with the database away: %d %.300s", path, code, body)
		}
	}
	if said < 40 || served < 40 {
		t.Errorf("%d routes said the database is unavailable and %d answered: too few of one of them for a console whose database is away", said, served)
	}
	t.Logf("%d GET routes asked with the database away: %d answered 200 (%d of them event streams), %d said the database is unavailable (503, marked), %d refused a made-up name (404, 400, 409, 401), %d refused for a reason listed in the test", len(routes), served, streams, said, named, other)

	// Back: the routes of the finding answer again, without a restart.
	away.Back()
	for _, path := range []string{issues.Prefix + "me", issues.Prefix + "settings"} {
		var code int
		var body string
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if code, body, _ = con.get(b, path); code == http.StatusOK || time.Now().After(deadline) {
				break
			}
		}
		if code != http.StatusOK {
			t.Errorf("%s after the outage: %d %.300s", path, code, body)
		}
	}
}
