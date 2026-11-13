package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/scrub"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/sherlocktools"
)

// Sherlock's UI actions (P6U-04 to P6U-17): the server sends each to the
// window that asked as a ui event and waits for the window's result.

const testWindow = "win0test1234"

const sherlockOn = `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200}`

// uiWindow stands in for a console window: it reads the conversation's
// stream and answers each ui event with respond's result, posted as the
// client c. It returns the requests it saw.
type uiWindow struct {
	mu   sync.Mutex
	seen []sherlock.UIRequest
}

func (u *uiWindow) requests() []sherlock.UIRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]sherlock.UIRequest(nil), u.seen...)
}

func runWindow(t *testing.T, srv *httptest.Server, c *http.Client, path string, respond func(sherlock.UIRequest) (sherlock.UIResult, bool)) *uiWindow {
	t.Helper()
	return runWindowAt(t, srv, c, path, "/api/v1/sherlock/ui/", respond)
}

// runWindowAt is runWindow on another conversation's stream, posting the
// results under results: a case's (SR-19).
func runWindowAt(t *testing.T, srv *httptest.Server, c *http.Client, path, results string, respond func(sherlock.UIRequest) (sherlock.UIResult, bool)) *uiWindow {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	u := &uiWindow{}
	ready := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		buf := []byte{}
		tmp := make([]byte, 1<<15)
		first := true
		for {
			n, err := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if first && bytes.Contains(buf, []byte("conversation")) {
				first = false
				close(ready)
			}
			for {
				i := bytes.Index(buf, []byte("\n\n"))
				if i < 0 {
					break
				}
				ev := string(buf[:i])
				buf = buf[i+2:]
				name, data := "", ""
				for _, l := range strings.Split(ev, "\n") {
					if v, ok := strings.CutPrefix(l, "event: "); ok {
						name = v
					}
					if v, ok := strings.CutPrefix(l, "data: "); ok {
						data = v
					}
				}
				if name != "ui" && name != "sherlock-ui" {
					continue
				}
				var r sherlock.UIRequest
				_ = json.Unmarshal([]byte(data), &r)
				u.mu.Lock()
				u.seen = append(u.seen, r)
				u.mu.Unlock()
				if res, ok := respond(r); ok {
					b, _ := json.Marshal(res)
					go postResult(c, srv.URL+results+r.ID, string(b))
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not start")
	}
	return u
}

// postResult posts a window's result, from any goroutine, and says what
// came back (0 when nothing did).
func postResult(c *http.Client, url, body string) int {
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func askWith(t *testing.T, srv *httptest.Server, c *http.Client, svc *sherlock.Service, body map[string]any) *sherlock.Msg {
	t.Helper()
	b, _ := json.Marshal(body)
	r, text := send(t, c, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b))
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask %d %s", r.StatusCode, text)
	}
	svc.Wait()
	var v sherlock.View
	_, text = send(t, c, http.MethodGet, srv.URL+"/api/v1/sherlock/conversation", "")
	_ = json.Unmarshal([]byte(text), &v)
	return v.Messages[len(v.Messages)-1]
}

func applied(label string) func(sherlock.UIRequest) (sherlock.UIResult, bool) {
	return func(r sherlock.UIRequest) (sherlock.UIResult, bool) {
		if r.Action == "view_state" {
			return sherlock.UIResult{View: json.RawMessage(`{"tab":"pods"}`)}, true
		}
		return sherlock.UIResult{Applied: true, Label: label + " " + r.Action, View: json.RawMessage(`{"tab":"pods","filters":{"status":"failing"}}`)}, true
	}
}

// lastRequest is the latest request the fake Claude API received.
func lastRequest(t *testing.T, fake interface{ Requests() []json.RawMessage }) string {
	t.Helper()
	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached the Claude API")
	}
	return string(reqs[len(reqs)-1])
}

func TestSherlockUIActionsRunInTheAskingWindow(t *testing.T) {
	srv, ops, fake, auditOut, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	win := runWindow(t, srv, ops, "/api/v1/assistants/events", applied("Did"))
	m := askWith(t, srv, ops, svc, map[string]any{
		"question": `Show me the failing pods [[view_filter {"set":{"status":"failing"}}]] [[view_sort {"column":"restarts","direction":"desc"}]] [[view_state]]`,
		"window":   testWindow,
		"view":     `{"cluster":"local","view":"tabs","tab":"pods"}`,
	})
	if m.Status != "done" || len(m.View) != 2 || m.View[0].Label != "Did view_filter" || m.View[1].Label != "Did view_sort" {
		t.Fatalf("answer: %+v", m)
	}
	// Each change is checked and held as the model makes it, then made
	// once the answer is done, in order (SR-18); the read runs at once.
	seen := win.requests()
	var order []string
	for _, r := range seen {
		order = append(order, fmt.Sprintf("%s:%v", r.Action, r.Hold))
	}
	if got := strings.Join(order, ","); got != "view_filter:true,view_sort:true,view_state:false,view_filter:false,view_sort:false" {
		t.Fatalf("ui events: %s", got)
	}
	if seen[0].Window != testWindow || seen[0].Answer != m.ID || !strings.Contains(string(seen[0].Input), "failing") || string(seen[3].Input) != string(seen[0].Input) {
		t.Fatalf("ui events: %+v", seen)
	}
	if len(m.Steps) != 3 || m.Steps[0].Label != "Changed the filters" || m.Steps[2].Label != "Read your view" || m.Steps[1].Status != "done" {
		t.Fatalf("steps: %+v", m.Steps)
	}
	// The view went with the question, and the tool results carry the new view.
	first := string(fake.Requests()[0])
	if !strings.Contains(first, "console-view") || !strings.Contains(first, `{\"cluster\":\"local\"`) {
		t.Fatalf("no view in the question: %s", first)
	}
	if body := lastRequest(t, fake); !strings.Contains(body, `\"held\":true`) || !strings.Contains(body, "made with your other view changes") || !strings.Contains(body, `\"view\":{\"tab\":\"pods\"}`) {
		t.Fatalf("results: %s", body)
	}
	// One audit record names all three (P6U-15).
	var recs []string
	for _, line := range strings.Split(strings.TrimSpace(auditOut.String()), "\n") {
		if strings.Contains(line, `"sherlock.ask"`) {
			recs = append(recs, line)
		}
	}
	if len(recs) != 1 {
		t.Fatalf("%d records: %v", len(recs), recs)
	}
	for _, want := range []string{"ui [view_filter", `failing`, "held", "applied", "view_sort", "view_state {} read"} {
		if !strings.Contains(recs[0], want) {
			t.Errorf("the record lacks %q: %s", want, recs[0])
		}
	}
}

func TestSherlockUIResultsAreThePersonsOwn(t *testing.T) {
	srv, ops, _, _, _, svc := sherlockSetupWith(t, "ops@corp.example", nil, func(o *sherlock.Options) { o.UITimeout = 2 * time.Second })
	enableSherlock(t, srv, ops, sherlockOn)
	other := signIn(t, srv, "viewer@corp.example")
	refused := make(chan int, 1)
	runWindow(t, srv, ops, "/api/v1/sherlock/events", func(r sherlock.UIRequest) (sherlock.UIResult, bool) {
		// Another person's session posts first: refused (P6U-17).
		code := postResult(other, srv.URL+"/api/v1/sherlock/ui/"+r.ID, `{"applied":true,"label":"forged"}`)
		select {
		case refused <- code:
		default:
		}
		return sherlock.UIResult{Applied: true, Label: "Sorted"}, true
	})
	m := askWith(t, srv, ops, svc, map[string]any{"question": `[[view_sort {"column":"name","direction":"asc"}]]`, "window": testWindow})
	if code := <-refused; code != http.StatusNotFound {
		t.Fatalf("another session's result: %d", code)
	}
	if len(m.View) != 1 || m.View[0].Label != "Sorted" {
		t.Fatalf("answer: %+v", m.View)
	}
	// A result for an action nobody waits for is refused too.
	if resp, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/ui/nosuchaction", `{"applied":true}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown action: %d", resp.StatusCode)
	}
}

func TestSherlockUIActionTimesOutAndIgnoresALateResult(t *testing.T) {
	srv, ops, fake, auditOut, _, svc := sherlockSetupWith(t, "ops@corp.example", nil, func(o *sherlock.Options) { o.UITimeout = 300 * time.Millisecond })
	enableSherlock(t, srv, ops, sherlockOn)
	late := make(chan int, 1)
	runWindow(t, srv, ops, "/api/v1/sherlock/events", func(r sherlock.UIRequest) (sherlock.UIResult, bool) {
		go func() {
			time.Sleep(600 * time.Millisecond)
			late <- postResult(ops, srv.URL+"/api/v1/sherlock/ui/"+r.ID, `{"applied":true,"label":"late"}`)
		}()
		return sherlock.UIResult{}, false
	})
	m := askWith(t, srv, ops, svc, map[string]any{"question": `[[view_filter {"set":{"status":"failing"}}]]`, "window": testWindow})
	if len(m.View) != 0 || m.Steps[0].Status != "failed" {
		t.Fatalf("answer: %+v", m)
	}
	if body := lastRequest(t, fake); !strings.Contains(body, "did not answer within") {
		t.Fatalf("result: %s", body)
	}
	if code := <-late; code != http.StatusNotFound {
		t.Fatalf("late result: %d", code)
	}
	if !strings.Contains(auditOut.String(), "view_filter") || !strings.Contains(auditOut.String(), "no window") {
		t.Fatalf("audit: %s", auditOut.String())
	}
}

func TestSherlockUIWithoutAWindowAndPastTheLimit(t *testing.T) {
	srv, ops, fake, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	// No window: the tool says so at once (P6U-08).
	askWith(t, srv, ops, svc, map[string]any{"question": `[[view_filter {"set":{"status":"failing"}}]]`})
	if body := lastRequest(t, fake); !strings.Contains(body, "No console window is open") {
		t.Fatalf("result: %s", body)
	}
	// An eleventh action is refused (P6U-09).
	win := runWindow(t, srv, ops, "/api/v1/sherlock/events", applied("Did"))
	q := strings.Repeat(`[[view_highlight {"row":"a"}]] `, 11)
	m := askWith(t, srv, ops, svc, map[string]any{"question": q, "window": testWindow})
	checked := 0
	for _, r := range win.requests() {
		if r.Hold {
			checked++
		}
	}
	if checked != sherlock.MaxUIActions {
		t.Fatalf("%d actions reached the window", checked)
	}
	if len(m.View) != sherlock.MaxUIActions || m.Steps[10].Status != "failed" {
		t.Fatalf("answer: %d changes, steps %+v", len(m.View), m.Steps)
	}
	if body := lastRequest(t, fake); !strings.Contains(body, "at most 10 view actions") {
		t.Fatalf("result: %s", body)
	}
}

func TestSherlockViewChangesSwitch(t *testing.T) {
	srv, ops, fake, auditOut, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	askWith(t, srv, ops, svc, map[string]any{"question": "What needs attention?", "window": testWindow})
	on := lastRequest(t, fake)
	for _, name := range []string{"view_state", "view_open", "view_filter", "view_search", "view_sort", "view_columns", "view_details", "view_highlight"} {
		if !strings.Contains(on, `"name":"`+name+`"`) {
			t.Errorf("%s is not offered", name)
		}
	}
	// Off (P6U-14): only view_state, and the change is audited.
	enableSherlock(t, srv, ops, `{"clusters":{"local":true},"model":"claude-opus-5-5","dailyUSD":20,"monthlyUSD":200,"viewControl":false}`)
	askWith(t, srv, ops, svc, map[string]any{"question": "What needs attention?", "window": testWindow})
	off := lastRequest(t, fake)
	if !strings.Contains(off, `"name":"view_state"`) || strings.Contains(off, `"name":"view_filter"`) || strings.Contains(off, `"name":"view_open"`) {
		t.Fatalf("with view changes off the tools are wrong")
	}
	if !strings.Contains(auditOut.String(), "view changes off") {
		t.Fatalf("not audited: %s", auditOut.String())
	}
}

func TestSherlockViewIsScrubbed(t *testing.T) {
	srv, ops, fake, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	token := "ghp_" + strings.Repeat("Ab3", 12)
	askWith(t, srv, ops, svc, map[string]any{"question": "What needs attention?", "window": testWindow, "view": `{"tab":"pods","filters":{"q":"` + token + `"}}`})
	body := string(fake.Requests()[0])
	if strings.Contains(body, token) || !strings.Contains(body, scrub.Redacted) {
		t.Fatalf("the token reached the Claude API: %s", body)
	}
}

// The UI actions are one list (P6U-04): the tool definitions come from
// ui_actions.json, which the browser imports too, and they are exactly
// these. TestSherlockToolsOnlyRead covers the tools that read the cluster.
func TestSherlockUIActionsList(t *testing.T) {
	var names []string
	for _, a := range sherlock.UIActions() {
		names = append(names, a.Name)
		var schema map[string]any
		if err := json.Unmarshal(a.InputSchema, &schema); err != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s: the schema is not a closed object", a.Name)
		}
		if a.Changes == (a.Name == "view_state") {
			t.Errorf("%s: changes is %v", a.Name, a.Changes)
		}
		for _, d := range sherlocktools.Defs() {
			if d.Name == a.Name {
				t.Errorf("%s is both a cluster tool and a UI action", a.Name)
			}
		}
	}
	// open_tool_dialog opens a dialog the person reads and sends (TB-42).
	want := "view_state,view_open,view_filter,view_search,view_sort,view_columns,view_details,view_highlight,view_aiops,view_admin,open_tool_dialog"
	if strings.Join(names, ",") != want {
		t.Fatalf("the UI actions changed; review that each changes only the asking person's view (P6U-12), then update this list:\n got %v\nwant %v", names, want)
	}
}

// A change the window leaves undone, because the person has unsaved
// changes open or moved the view themselves, is kept under the answer for
// its Show in 221B button, with the model told why (SR-14, SR-15); a
// refused one and a read are not.
func TestSherlockKeepsWhatTheWindowLeft(t *testing.T) {
	srv, ops, fake, auditOut, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	runWindow(t, srv, ops, "/api/v1/sherlock/events", func(r sherlock.UIRequest) (sherlock.UIResult, bool) {
		switch r.Action {
		case "view_admin":
			return sherlock.UIResult{Deferred: true, View: json.RawMessage(`{"view":"admin"}`)}, true
		case "view_filter":
			return sherlock.UIResult{Dropped: true}, true
		case "view_sort":
			return sherlock.UIResult{Error: "Table pods has no column \"x\"."}, true
		}
		return sherlock.UIResult{View: json.RawMessage(`{"view":"admin"}`)}, true
	})
	m := askWith(t, srv, ops, svc, map[string]any{
		"question": `[[view_admin {"page":"agents","item":"sherlock"}]] [[view_filter {"set":{"status":"failing"}}]] [[view_sort {"column":"x","direction":"asc"}]] [[view_state]]`,
		"window":   testWindow,
	})
	if len(m.View) != 0 || len(m.Show) != 2 {
		t.Fatalf("answer: view %+v show %+v", m.View, m.Show)
	}
	if m.Show[0].Action != "view_admin" || m.Show[0].Why != "unsaved" || !strings.Contains(string(m.Show[0].Input), `"item":"sherlock"`) || m.Show[1].Action != "view_filter" || m.Show[1].Why != "moved" {
		t.Fatalf("show: %+v", m.Show)
	}
	if m.Steps[0].Label != "Opened a page" {
		t.Fatalf("steps: %+v", m.Steps)
	}
	all := ""
	for _, r := range fake.Requests() {
		all += string(r)
	}
	if !strings.Contains(all, "unsaved changes open in this window") || !strings.Contains(all, "Show in 221B") {
		t.Fatalf("the model was not told: %s", all)
	}
	if !strings.Contains(auditOut.String(), "view_admin") || !strings.Contains(auditOut.String(), "deferred") {
		t.Fatalf("audit: %s", auditOut.String())
	}
}

// The view changes of an answer move the person's screen only once the
// answer is done (SR-18): none is made before the model's last reply, and
// an answer stopped part way makes none, keeping them for Show in 221B.
func TestSherlockMakesViewChangesWhenTheAnswerIsDone(t *testing.T) {
	srv, ops, fake, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	var mu sync.Mutex
	madeAt := map[string]int{}
	runWindow(t, srv, ops, "/api/v1/sherlock/events", func(r sherlock.UIRequest) (sherlock.UIResult, bool) {
		if r.Hold {
			return sherlock.UIResult{Held: true, View: json.RawMessage(`{"tab":"nodes"}`)}, true
		}
		mu.Lock()
		madeAt[r.Action] = len(fake.Requests())
		mu.Unlock()
		return sherlock.UIResult{Applied: true, Label: "Made " + r.Action}, true
	})
	m := askWith(t, srv, ops, svc, map[string]any{
		"question": `[[view_open {"tab":"pods"}]] [[view_filter {"set":{"status":"failing"}}]] [[view_state]]`,
		"window":   testWindow,
	})
	total := len(fake.Requests())
	mu.Lock()
	if madeAt["view_open"] != total || madeAt["view_filter"] != total {
		t.Fatalf("changes made after %v of %d model requests", madeAt, total)
	}
	if m.Status != "done" || len(m.View) != 2 || m.View[0].Label != "Made view_open" || m.View[1].Label != "Made view_filter" || len(m.Show) != 0 {
		t.Fatalf("answer: %+v", m)
	}

	// Stopped part way: nothing is made, and the held change waits under
	// the answer for Show in 221B.
	madeAt = map[string]int{}
	mu.Unlock()
	b, _ := json.Marshal(map[string]any{"question": `[[view_filter {"set":{"status":"pending"}}]] [[sleep 3000]] [[view_state]]`, "window": testWindow})
	if r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/ask", string(b)); r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask %d %s", r.StatusCode, text)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.Requests()) < total+2 {
		if time.Now().After(deadline) {
			t.Fatal("the answer did not reach its second round")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/stop", "{}"); r.StatusCode != 200 {
		t.Fatalf("stop: %d", r.StatusCode)
	}
	svc.Wait()
	mu.Lock()
	defer mu.Unlock()
	var v sherlock.View
	_, text := send(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock/conversation", "")
	_ = json.Unmarshal([]byte(text), &v)
	last := v.Messages[len(v.Messages)-1]
	if len(madeAt) != 0 || last.Status != "stopped" || len(last.View) != 0 || len(last.Show) != 1 || last.Show[0].Action != "view_filter" || last.Show[0].Why != "stopped" {
		t.Fatalf("stopped: made %v, answer %+v", madeAt, last)
	}
}

// A view change the window refuses as it checks it is not held: the model
// reads why, and nothing is made at the end.
func TestSherlockHoldsOnlyWhatTheWindowAccepts(t *testing.T) {
	srv, ops, fake, _, _, svc := sherlockSetup(t, "ops@corp.example", nil)
	enableSherlock(t, srv, ops, sherlockOn)
	win := runWindow(t, srv, ops, "/api/v1/sherlock/events", func(r sherlock.UIRequest) (sherlock.UIResult, bool) {
		return sherlock.UIResult{Error: "Your role does not open that page."}, true
	})
	m := askWith(t, srv, ops, svc, map[string]any{"question": `[[view_admin {"page":"users"}]]`, "window": testWindow})
	if n := len(win.requests()); n != 1 || len(m.View) != 0 || len(m.Show) != 0 || m.Steps[0].Status != "failed" {
		t.Fatalf("%d requests, answer %+v", n, m)
	}
	if body := lastRequest(t, fake); !strings.Contains(body, "Your role does not open that page.") || strings.Contains(body, `\"held\":true`) {
		t.Fatalf("result: %s", body)
	}
}
