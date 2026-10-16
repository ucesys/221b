package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/sherlock"
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
					go postResult(c, srv.URL+"/api/v1/sherlock/ui/"+r.ID, string(b))
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
	seen := win.requests()
	if len(seen) != 3 || seen[0].Window != testWindow || seen[0].Answer != m.ID || seen[0].Action != "view_filter" || !strings.Contains(string(seen[0].Input), "failing") {
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
	if body := lastRequest(t, fake); !strings.Contains(body, `\"applied\":true`) || !strings.Contains(body, `\"view\":{\"tab\":\"pods\"`) {
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
	for _, want := range []string{"ui [view_filter", `failing`, "applied", "view_sort", "view_state {} read"} {
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
		refused <- postResult(other, srv.URL+"/api/v1/sherlock/ui/"+r.ID, `{"applied":true,"label":"forged"}`)
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
	if n := len(win.requests()); n != sherlock.MaxUIActions {
		t.Fatalf("%d actions reached the window", n)
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
	if strings.Contains(body, token) || !strings.Contains(body, sherlock.Scrubbed) {
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
		for _, tl := range sherlockTools {
			if tl.def.Name == a.Name {
				t.Errorf("%s is both a cluster tool and a UI action", a.Name)
			}
		}
	}
	want := "view_state,view_open,view_filter,view_search,view_sort,view_columns,view_details,view_highlight"
	if strings.Join(names, ",") != want {
		t.Fatalf("the UI actions changed; review that each changes only the asking person's view (P6U-12), then update this list:\n got %v\nwant %v", names, want)
	}
}
