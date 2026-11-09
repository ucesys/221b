package lessons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/kv"
)

var t0 = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

// fakeWiki keeps the pages written; noStore answers as a console with no
// wiki does.
type fakeWiki struct {
	mu      sync.Mutex
	pages   map[string]string
	noStore bool
	fail    error
}

var errNoStore = errors.New("no wiki")

func (f *fakeWiki) WriteRecord(_ context.Context, kind, name, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noStore {
		return "", errNoStore
	}
	if f.fail != nil {
		return "", f.fail
	}
	p := "pages/" + kind + "/" + name + ".md"
	if f.pages == nil {
		f.pages = map[string]string{}
	}
	f.pages[p] = title + "\n" + body
	return p, nil
}

type world struct {
	s     *Service
	now   time.Time
	audit *bytes.Buffer
	wiki  *fakeWiki
	kv    *kv.Memory
}

func setup(t *testing.T) *world {
	t.Helper()
	w := &world{now: t0, audit: &bytes.Buffer{}, wiki: &fakeWiki{}, kv: &kv.Memory{}}
	w.s = New(Options{Backend: w.kv, Audit: audit.New(audit.Options{Out: w.audit}), Now: func() time.Time { return w.now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w.s.SetWiki(w.wiki, func(err error) bool { return errors.Is(err, errNoStore) })
	return w
}

var bob = Person{Email: "bob@example.com", From: "10.0.0.9"}

func TestKey(t *testing.T) {
	for _, c := range [][3]string{
		{"RBAC.Cluster-Admin Binding", "", "rbac.cluster-admin-binding"},
		{"", "Volume data-0 91% full", "title:volume data # # full"},
		{"", "Volume data-0 93% full!", "title:volume data # # full"},
		{"!!!", "No probes", "title:no probes"},
	} {
		if got := Key(c[0], c[1]); got != c[2] {
			t.Fatalf("Key(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// An exception is recorded with why and until when, audited, and kept in
// the console's records only, never written to the wiki (MY-09); it
// applies on its cluster until it expires or is removed (FX-03 to FX-05).
func TestExceptions(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if _, err := w.s.AddException(ctx, NewException{Title: "x", Reason: "  "}, bob); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := w.s.AddException(ctx, NewException{Title: "x", Reason: "y", Expires: t0.Add(-time.Hour)}, bob); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired already: %v", err)
	}
	e, err := w.s.AddException(ctx, NewException{Cluster: "prod", Resource: "shop/Namespace/shop", Check: "netpol.missing", Title: "No network policy", Detail: "Pods accept traffic from anywhere.",
		Finding: "run-4.1", Run: "run-4", Job: "Nightly audit", Reason: "Shop sits behind its own firewall. token=abcdef0123456789abcdef", Expires: t0.Add(48 * time.Hour)}, bob)
	if err != nil || e.ID != "EX-1" || e.Check != "netpol.missing" || e.By != "bob@example.com" || e.Wiki != "" || e.WikiError != "" || strings.Contains(e.Reason, "abcdef0123456789abcdef") {
		t.Fatalf("added: %+v %v", e, err)
	}
	if len(w.wiki.pages) != 0 {
		t.Fatalf("an exception reached the wiki: %v", w.wiki.pages)
	}
	if _, err := w.s.AddException(ctx, NewException{Cluster: "dev", Title: "Volume 91% full", Reason: "fine"}, bob); err != nil {
		t.Fatal(err)
	}
	if a := w.s.Active(ctx, "prod"); len(a) != 1 || !a[0].Covers("prod", "shop/namespace/shop", "netpol.missing") || a[0].Covers("dev", "shop/Namespace/shop", "netpol.missing") {
		t.Fatalf("active on prod: %+v", a)
	}
	if a := w.s.Active(ctx, "dev"); len(a) != 1 || a[0].Check != "title:volume # full" {
		t.Fatalf("active on dev: %+v", a)
	}
	// Past its expiry it stops applying.
	w.now = t0.Add(49 * time.Hour)
	if a := w.s.Active(ctx, "prod"); len(a) != 0 {
		t.Fatalf("expired: %+v", a)
	}
	ex, _ := w.s.Exception(ctx, "EX-1")
	if ex.State(w.now) != "expired" {
		t.Fatalf("state: %s", ex.State(w.now))
	}
	// Removed, it stops applying and stays listed.
	r, err := w.s.RemoveException(ctx, "EX-2", Person{Email: "carol"})
	if err != nil || r.RemovedBy != "carol" || r.State(w.now) != "removed" {
		t.Fatalf("removed: %+v %v", r, err)
	}
	if _, err := w.s.RemoveException(ctx, "EX-2", Person{Email: "carol"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("removed twice: %v", err)
	}
	if _, err := w.s.RemoveException(ctx, "EX-9", Person{Email: "carol"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if a := w.s.Active(ctx, "dev"); len(a) != 0 {
		t.Fatalf("removed still applies: %+v", a)
	}
	if len(w.wiki.pages) != 0 {
		t.Fatalf("a removed exception reached the wiki: %v", w.wiki.pages)
	}
	if list, _ := w.s.Exceptions(ctx); len(list) != 2 || list[0].ID != "EX-2" {
		t.Fatalf("list: %+v", list)
	}
	for _, want := range []string{`"action":"exception.add"`, "exception EX-1 accepted for finding run-4.1 (No network policy) on shop/Namespace/shop, check netpol.missing, until 2026-11-08", `"action":"exception.remove"`} {
		if !strings.Contains(w.audit.String(), want) {
			t.Fatalf("audit lacks %q: %s", want, w.audit.String())
		}
	}
	if strings.Contains(w.audit.String(), "abcdef0123456789abcdef") {
		t.Fatal("a token reached the audit log")
	}
}

// Without a wiki the records stay the console's own (FX-09).
func TestWikiOff(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.wiki.noStore = true
	e, err := w.s.AddException(ctx, NewException{Title: "t", Reason: "r"}, bob)
	if err != nil || e.Wiki != "" || e.WikiError != "" {
		t.Fatalf("no wiki: %+v %v", e, err)
	}
}

// An RCA is its case's (CF-44): the service keeps no RCAs, serves none,
// and writes no page of one, whatever rcas.json a build before wave 6e
// left.
func TestNoRCAsHere(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if _, err := w.kv.SaveKey(ctx, RCAsKey, []byte(`{"next":1,"items":[{"id":"RCA-1","case":"C-1","cause":"c"}]}`), ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return Caller{Read: true}, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	for _, path := range []string{"/api/v1/cases/rcas", "/api/v1/cases/rcas/RCA-1"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	x, err := w.s.Export(ctx)
	if err != nil || x != nil {
		t.Fatalf("a backup of no exceptions: %+v %v", x, err)
	}
	if len(w.wiki.pages) != 0 {
		t.Fatalf("pages: %v", w.wiki.pages)
	}
}

// The exceptions keep to their limit: those no longer applying go first,
// and active ones are never let go.
func TestLimits(t *testing.T) {
	w := setup(t)
	w.s.o.Limits = Limits{ExceptionsBytes: 3000}
	w.s.SetWiki(nil, nil)
	ctx := context.Background()
	long := strings.Repeat("why ", 100)
	var first Exception
	for i := 0; ; i++ {
		e, err := w.s.AddException(ctx, NewException{Title: "t", Reason: long}, bob)
		if errors.Is(err, ErrFull) {
			break
		}
		if err != nil || i > 20 {
			t.Fatalf("add %d: %v", i, err)
		}
		if i == 0 {
			first = e
		}
	}
	if _, err := w.s.RemoveException(ctx, first.ID, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.AddException(ctx, NewException{Title: "t", Reason: long}, bob); err != nil {
		t.Fatalf("after a removal: %v", err)
	}
	if _, err := w.s.Exception(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the removed one stayed: %v", err)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if x, err := w.s.Export(ctx); err != nil || x != nil {
		t.Fatalf("empty: %+v %v", x, err)
	}
	_, _ = w.s.AddException(ctx, NewException{Title: "a", Reason: "r"}, bob)
	_, _ = w.s.AddException(ctx, NewException{Title: "b", Reason: "r"}, bob)
	x, _ := w.s.Export(ctx)
	raw, _ := json.Marshal(x)
	// A backup of a build before wave 6e holds RCAs too: a restore leaves
	// them out (CF-44).
	raw = append(raw[:len(raw)-1], []byte(`,"rcas":[{"id":"RCA-1","case":"C-1","cause":"c"}],"nextRCA":1}`)...)
	other := setup(t)
	_, _ = other.s.AddException(ctx, NewException{Title: "a", Reason: "here"}, bob)
	n, err := other.s.Import(ctx, raw)
	if err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	list, _ := other.s.Exceptions(ctx)
	if len(list) != 2 || list[0].ID != "EX-2" || list[1].Reason != "here" {
		t.Fatalf("after: %+v", list)
	}
	if e, _ := other.s.AddException(ctx, NewException{Title: "c", Reason: "r"}, bob); e.ID != "EX-3" {
		t.Fatalf("numbering after a restore: %s", e.ID)
	}
	if _, err := other.s.Import(ctx, json.RawMessage(`{"exceptions": [{"id": "nope"}]}`)); err == nil {
		t.Fatal("a bad exception was taken")
	}
}

// The API: those who see the cases read; the approvers remove.
func TestAPI(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	_, _ = w.s.AddException(ctx, NewException{Title: "a", Reason: "r"}, bob)
	var caller Caller
	signed := true
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return caller, signed }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	do := func(method, path string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	signed = false
	if code, _ := do("GET", "/api/v1/cases/exceptions"); code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", code)
	}
	signed = true
	caller = Caller{Person: Person{Email: "viewer"}}
	if code, _ := do("GET", "/api/v1/cases/exceptions"); code != http.StatusForbidden {
		t.Fatalf("a viewer: %d", code)
	}
	caller = Caller{Person: Person{Email: "sv"}, Read: true}
	if code, out := do("GET", "/api/v1/cases/exceptions"); code != http.StatusOK || out["mayChange"] != false || len(out["exceptions"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := do("GET", "/api/v1/cases/exceptions/EX-1"); code != http.StatusOK || out["state"] != "active" {
		t.Fatalf("one: %d %v", code, out)
	}
	if code, _ := do("DELETE", "/api/v1/cases/exceptions/EX-1"); code != http.StatusForbidden {
		t.Fatalf("a reader removes: %d", code)
	}
	caller = Caller{Person: Person{Email: "approver"}, Change: true}
	if code, out := do("DELETE", "/api/v1/cases/exceptions/EX-1"); code != http.StatusOK || out["state"] != "removed" || out["removedBy"] != "approver" {
		t.Fatalf("remove: %d %v", code, out)
	}
	if code, _ := do("DELETE", "/api/v1/cases/exceptions/EX-1"); code != http.StatusBadRequest {
		t.Fatalf("remove again: %d", code)
	}
}

// An exception written to the wiki before exceptions stopped being pages
// there keeps its page, which says so once the exception is removed
// (MY-09).
func TestOldExceptionPage(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	old := exceptionsRecord{Next: 1, Items: []Exception{{ID: "EX-1", Cluster: "prod", Check: "netpol.missing", Title: "No network policy", Reason: "Firewalled.", By: "bob", At: t0, Wiki: "pages/exception/ex-1.md"}}}
	b, _ := json.Marshal(old)
	if _, err := w.kv.SaveKey(ctx, ExceptionsKey, b, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RemoveException(ctx, "EX-1", Person{Email: "carol"}); err != nil {
		t.Fatal(err)
	}
	if page := w.wiki.pages["pages/exception/ex-1.md"]; !strings.Contains(page, "**Removed** by carol") {
		t.Fatalf("old page: %q", page)
	}
}

// An exception counts the findings it dropped and when it last did; one
// that matched nothing for the days Settings › Cases names is stale, and
// the list filters the stale ones (CF-13).
func TestExceptionMatchesAndStale(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	days := 10
	w.s.SetStaleDays(func() int { return days })
	a, _ := w.s.AddException(ctx, NewException{Cluster: "prod", Resource: "shop/Pod/web", Check: "pod.no-limits", Title: "No limits", Reason: "batch", Case: "C-4"}, bob)
	b, _ := w.s.AddException(ctx, NewException{Cluster: "prod", Resource: "Node/n1", Check: "node.disk-pressure", Title: "Disk", Reason: "known"}, bob)
	if a.Case != "C-4" || a.Matches != 0 || !a.LastMatched.IsZero() {
		t.Fatalf("new: %+v", a)
	}
	if !strings.Contains(w.audit.String(), "accepted for case C-4") {
		t.Fatalf("audit: %s", w.audit.String())
	}
	w.now = t0.Add(5 * 24 * time.Hour)
	if err := w.s.Match(ctx, map[string]int{a.ID: 2}, w.now); err != nil {
		t.Fatal(err)
	}
	got, _ := w.s.Exception(ctx, a.ID)
	if got.Matches != 2 || !got.LastMatched.Equal(w.now) {
		t.Fatalf("matched: %+v", got)
	}
	// Eleven days on: b never matched since it was accepted, a matched six
	// days ago.
	w.now = t0.Add(11 * 24 * time.Hour)
	x, _ := w.s.Exception(ctx, b.ID)
	if got.Stale(w.now, days) || !x.Stale(w.now, days) {
		t.Fatalf("stale: a %v, b %v", got.Stale(w.now, days), x.Stale(w.now, days))
	}
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return Caller{Read: true}, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/cases/exceptions?stale=1", nil))
	var out struct {
		Exceptions []ExceptionView `json:"exceptions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Exceptions) != 1 || out.Exceptions[0].ID != b.ID || !out.Exceptions[0].Stale {
		t.Fatalf("stale list: %s", rec.Body.String())
	}
	// A removed exception is never stale.
	_, _ = w.s.RemoveException(ctx, b.ID, bob)
	x, _ = w.s.Exception(ctx, b.ID)
	if x.Stale(w.now, days) {
		t.Fatal("a removed exception is stale")
	}
}

// apiOf serves the routes as a caller who may change the exceptions.
func apiOf(w *world, caller *Caller) func(method, path, body string) (int, map[string]any) {
	mux := http.NewServeMux()
	for _, rt := range w.s.Routes(func(*http.Request) (Caller, bool) { return *caller, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	return func(method, path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
}

// New exception (build 14): an approver creates one by hand on a connected
// cluster, for an object or the cluster as a whole, naming the validation
// rule by its id (or a finding's title in words), with a justification
// and an optional last day; audited as exception.add. The list names each
// exception's cluster and what its rule means, and offers who may create
// one the clusters with the rules seen on each.
func TestNewExceptionAPI(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.s.SetClusters(func() []jobs.Cluster { return []jobs.Cluster{{ID: "local", Name: "Lakehouse"}, {ID: "prod", Name: ""}} },
		func(context.Context) map[string][]string {
			return map[string][]string{"local": {"pod.root-user", "other.odd-thing", "pod.root-user", "netpol.missing"}}
		})
	caller := Caller{Person: Person{Email: "sv"}, Read: true}
	do := apiOf(w, &caller)
	if code, _ := do("POST", "/api/v1/cases/exceptions", `{"cluster":"local","check":"pod.root-user","reason":"r"}`); code != http.StatusForbidden {
		t.Fatalf("a reader creates: %d", code)
	}
	caller = Caller{Person: Person{Email: "ann@example.com", From: "10.0.0.1"}, Change: true}
	for _, c := range []struct{ body, want string }{
		{`{"check":"pod.root-user","reason":"r"}`, "pick the cluster"},
		{`{"cluster":"elsewhere","check":"pod.root-user","reason":"r"}`, "not a connected cluster"},
		{`{"cluster":"local","resource":"web","check":"pod.root-user","reason":"r"}`, "name the object"},
		{`{"cluster":"local","check":"  ","reason":"r"}`, "name the validation rule"},
		{`{"cluster":"local","check":"!!!","reason":"r"}`, "name the validation rule"},
		{`{"cluster":"local","check":"pod.root-user","reason":"  "}`, "give the justification"},
		{`{"cluster":"local","check":"pod.root-user","reason":"r","expires":"next week"}`, "YYYY-MM-DD"},
		{`{"cluster":"local","check":"pod.root-user","reason":"r","expires":"2026-11-01"}`, "in the past"},
	} {
		code, out := do("POST", "/api/v1/cases/exceptions", c.body)
		if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), c.want) {
			t.Fatalf("%s: %d %v, want %q", c.body, code, out, c.want)
		}
	}
	if code, _ := do("POST", "/api/v1/cases/exceptions", `not json`); code != http.StatusBadRequest {
		t.Fatalf("not json: %d", code)
	}
	code, out := do("POST", "/api/v1/cases/exceptions", `{"cluster":"local","resource":"deploy/shop/web","check":"Pod.Root-User","reason":"The image needs root; tracked in JIRA-12.","expires":"2026-12-31"}`)
	if code != http.StatusCreated || out["id"] != "EX-1" || out["resource"] != "shop/Deployment/web" || out["check"] != "pod.root-user" || out["title"] != "A container runs as root" ||
		out["clusterName"] != "Lakehouse" || out["ruleMeaning"] != "a container runs as root" || out["state"] != "active" || out["by"] != "ann@example.com" || out["expires"] != "2026-12-31T23:59:59Z" {
		t.Fatalf("created: %d %v", code, out)
	}
	// The same cluster, object and rule while it is active: refused.
	if code, out := do("POST", "/api/v1/cases/exceptions", `{"cluster":"local","resource":"shop/Deployment/web","check":"pod.root-user","reason":"again"}`); code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "EX-1 already covers") {
		t.Fatalf("a duplicate: %d %v", code, out)
	}
	// Cluster-wide, a rule in words (a finding's title), on a cluster without a name of its own.
	code, out = do("POST", "/api/v1/cases/exceptions", `{"cluster":"prod","check":"Volume 91% full","reason":"Scratch volumes fill up."}`)
	if code != http.StatusCreated || out["resource"] != nil || out["check"] != "title:volume # full" || out["title"] != "Volume 91% full" || out["clusterName"] != "prod" || out["ruleMeaning"] != nil || out["expires"] != nil {
		t.Fatalf("cluster-wide: %d %v", code, out)
	}
	if !strings.Contains(w.audit.String(), "exception EX-1 created in Mycroft › Exceptions (A container runs as root) on shop/Deployment/web, check pod.root-user, until 2026-12-31") {
		t.Fatalf("audit: %s", w.audit.String())
	}
	// The list: names and meanings, and the form's clusters with the rules seen, sorted, once each.
	code, out = do("GET", "/api/v1/cases/exceptions", "")
	clusters, _ := json.Marshal(out["clusters"])
	want := `[{"id":"local","name":"Lakehouse","rules":[{"id":"netpol.missing","meaning":"a namespace has no network policy"},{"id":"other.odd-thing"},{"id":"pod.root-user","meaning":"a container runs as root"}]},{"id":"prod","name":"prod","rules":[]}]`
	if code != http.StatusOK || string(clusters) != want {
		t.Fatalf("clusters: %d %s", code, clusters)
	}
	// A reader gets no form.
	caller = Caller{Read: true}
	if _, out := do("GET", "/api/v1/cases/exceptions", ""); out["clusters"] != nil {
		t.Fatalf("a reader's list: %v", out)
	}
	if e, _ := w.s.Exception(ctx, "EX-1"); !e.Covers("local", "shop/Deployment/web", "pod.root-user") {
		t.Fatalf("kept: %+v", e)
	}
}

// Make active again (build 14): an approver makes an inactive exception
// active, which clears who turned it off and what it duplicated and sets
// Updated; audited exception.reactivate. Refused while an active one
// covers the same cluster, object and rule, for one active already, and
// for one whose last day has passed.
func TestReactivateException(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	add := func(at time.Time, expires time.Time) Exception {
		w.now = at
		e, err := w.s.AddException(ctx, NewException{Cluster: "prod", Resource: "shop/Pod/web", Check: "pod.no-limits", Title: "No limits", Reason: "batch", Expires: expires}, bob)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	a := add(t0, time.Time{})
	b := add(t0.Add(time.Hour), time.Time{})
	w.now = t0.Add(2 * time.Hour)
	if d, err := w.s.RemoveDuplicates(ctx, Person{Name: "housekeeping"}); err != nil || len(d) != 1 || d[0].ID != b.ID {
		t.Fatalf("duplicates: %+v %v", d, err)
	}
	caller := Caller{Read: true}
	do := apiOf(w, &caller)
	if code, _ := do("POST", "/api/v1/cases/exceptions/"+b.ID+"/reactivate", ""); code != http.StatusForbidden {
		t.Fatalf("a reader reactivates: %d", code)
	}
	caller = Caller{Person: Person{Email: "carol"}, Change: true}
	// b repeats a, which is active: refused, saying which covers it.
	if code, out := do("POST", "/api/v1/cases/exceptions/"+b.ID+"/reactivate", ""); code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), a.ID+" already covers") {
		t.Fatalf("covered: %d %v", code, out)
	}
	if code, _ := do("POST", "/api/v1/cases/exceptions/"+a.ID+"/reactivate", ""); code != http.StatusBadRequest {
		t.Fatalf("active already: %d", code)
	}
	if code, _ := do("POST", "/api/v1/cases/exceptions/EX-9/reactivate", ""); code != http.StatusNotFound {
		t.Fatalf("unknown: %d", code)
	}
	// a turned off, b may come back.
	w.now = t0.Add(3 * time.Hour)
	if _, err := w.s.RemoveException(ctx, a.ID, Person{Email: "sam"}); err != nil {
		t.Fatal(err)
	}
	w.now = t0.Add(4 * time.Hour)
	code, out := do("POST", "/api/v1/cases/exceptions/"+b.ID+"/reactivate", "")
	if code != http.StatusOK || out["state"] != "active" || out["removedBy"] != nil || out["removedAt"] != nil || out["duplicateOf"] != nil || out["updated"] != w.now.Format(time.RFC3339) {
		t.Fatalf("reactivated: %d %v", code, out)
	}
	if got := w.s.Active(ctx, "prod"); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("active: %+v", got)
	}
	if !strings.Contains(w.audit.String(), `"action":"exception.reactivate"`) || !strings.Contains(w.audit.String(), "exception "+b.ID+" made active again (No limits on shop/Pod/web, check pod.no-limits), was a duplicate of "+a.ID) {
		t.Fatalf("audit: %s", w.audit.String())
	}
	// Now a is covered by b.
	if _, err := w.s.ReactivateException(ctx, a.ID, bob); !errors.Is(err, ErrConflict) {
		t.Fatalf("a covered by b: %v", err)
	}
	// One past its last day stays expired.
	c := add(t0.Add(5*time.Hour), t0.Add(30*time.Hour))
	_, _ = w.s.RemoveException(ctx, c.ID, bob)
	w.now = t0.Add(31 * time.Hour)
	if _, err := w.s.ReactivateException(ctx, c.ID, bob); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "last day") {
		t.Fatalf("expired: %v", err)
	}
}
