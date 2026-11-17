package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// The test dialog and making a draft live untested (Phase 12.1).

// A SuperAdmin puts a draft live untested only with a reason; a second
// SuperAdmin approves it as any other, the request and the version say it
// went untested, and so does the audit log. Without the flag an untested
// draft is still refused, and nobody else may do either.
func TestAIMakeLiveUntestedAPI(t *testing.T) {
	srv, ops, _, _, _, aud := aiSetupWith(t, nil)
	ops2 := signIn(t, srv, "ops2@corp.example")
	viewer := signIn(t, srv, "viewer@corp.example")
	var v ai.InstructionsView
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI findings first."}`, 200, &v)
	live := srv.URL + "/api/v1/ai/agents/auditor/live"
	if r, _ := send(t, viewer, http.MethodPost, live, `{"untested":true,"reason":"hotfix"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer made it live: %d", r.StatusCode)
	}
	for _, body := range []string{`{"untested":true}`, `{"untested":true,"reason":"   "}`, `{"untested":true,"reason":"` + strings.Repeat("x", ai.MaxUntestedReason+1) + `"}`} {
		if r, text := send(t, ops, http.MethodPost, live, body); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, "Say why") {
			t.Fatalf("%s: %d %s", body, r.StatusCode, text)
		}
	}
	for _, body := range []string{"", `{}`, `{"reason":"hotfix"}`} {
		if r, text := send(t, ops, http.MethodPost, live, body); r.StatusCode != http.StatusBadRequest || !strings.Contains(text, "test the draft first") {
			t.Fatalf("untested to live with %q: %d %s", body, r.StatusCode, text)
		}
	}
	var waiting struct {
		Waiting bool              `json:"waiting"`
		Request twoperson.Request `json:"request"`
	}
	aiJSON(t, ops, http.MethodPost, live, `{"untested":true,"reason":"The outage  needs it now."}`, http.StatusAccepted, &waiting)
	if !waiting.Waiting || waiting.Request.Untested != "The outage needs it now." || !strings.Contains(waiting.Request.What(), "go live untested (reason: The outage needs it now.)") {
		t.Fatalf("%+v", waiting)
	}
	aiJSON(t, ops2, http.MethodPost, srv.URL+"/api/v1/admin/requests/"+itoa(waiting.Request.ID)+"/approve", "", 200, nil)
	v = ai.InstructionsView{}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/auditor/instructions", "", 200, &v)
	if v.Live != 1 || v.Draft != nil || len(v.Versions) == 0 || v.Versions[0].Why != "made live untested: The outage needs it now." {
		t.Fatalf("%+v", v)
	}
	found := 0
	for _, e := range auditEvents(t, aud.String()) {
		if e.Action == ActionTwoPerson && strings.Contains(e.Detail, "untested (reason: The outage needs it now.)") {
			found++
		}
	}
	if found < 2 {
		t.Fatalf("the request and its approval are not recorded as untested:\n%s", aud.String())
	}
}

// The test dialog reads a run's progress while it is in flight and stops
// it, the cases running together (AI-25); the run ends stopped, kept so, and both are recorded. Only a
// SuperAdmin may do either, and no planted secret shows.
func TestAITestRunProgressAndStopAPI(t *testing.T) {
	gate := make(chan struct{})
	held := make(chan struct{}, 16)
	srv, ops, _, aiSvc, _, aud := aiSetupWith(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			if bytes.Contains(b, []byte("HOLD-HERE")) {
				select {
				case held <- struct{}{}:
				default:
				}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
			h.ServeHTTP(w, r)
		})
	})
	t.Cleanup(func() { close(gate) })
	viewer := signIn(t, srv, "viewer@corp.example")
	run := srv.URL + "/api/v1/ai/agents/auditor/tests/run"
	stop := srv.URL + "/api/v1/ai/agents/auditor/tests/stop"
	if r, _ := send(t, ops, http.MethodGet, run, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a run before any: %d", r.StatusCode)
	}
	if r, _ := send(t, ops, http.MethodPost, stop, ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("stopped nothing: %d", r.StatusCode)
	}
	if _, err := aiSvc.SaveTest(context.Background(), ai.TestCase{Agent: ai.Auditor, Name: "held", Question: "HOLD-HERE: is PCI met?"}, "ops"); err != nil {
		t.Fatal(err)
	}
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/auditor/draft", `{"house":"Weigh PCI findings first."}`, 200, nil)
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/test", `{}`, http.StatusAccepted, nil)
	// Claimed before the answer: running at once, and a second start is refused.
	var p ai.TestRun
	aiJSON(t, ops, http.MethodGet, run, "", 200, &p)
	if p.State != ai.RunRunning {
		t.Fatalf("not running at once: %+v", p)
	}
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/test", `{}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a second run: %d", r.StatusCode)
	}
	select {
	case <-held:
	case <-time.After(20 * time.Second):
		t.Fatal("the held case never ran")
	}
	for _, req := range [][2]string{{http.MethodGet, run}, {http.MethodPost, stop}} {
		if r, _ := send(t, viewer, req[0], req[1], ""); r.StatusCode != http.StatusForbidden {
			t.Fatalf("a viewer: %s %d", req[1], r.StatusCode)
		}
	}
	// The cases run together (AI-25): every case but the held one ends,
	// and the held one runs on.
	var text string
	for i := 0; i < 800; i++ {
		_, text = send(t, ops, http.MethodGet, run, "")
		p = ai.TestRun{}
		aiJSON(t, ops, http.MethodGet, run, "", 200, &p)
		if len(p.Planned) > 0 && p.Cases == len(p.Planned)-1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	last := len(p.Planned) - 1
	if p.State != ai.RunRunning || last < 0 || p.Planned[last].Name != "held" || p.Planned[last].State != ai.CaseRunning || p.Cases != last || len(p.Results) != last {
		t.Fatalf("the progress: %s", text)
	}
	aiJSON(t, ops, http.MethodPost, stop, "", 200, nil)
	for i := 0; i < 400 && p.State == ai.RunRunning; i++ {
		time.Sleep(25 * time.Millisecond)
		_, text = send(t, ops, http.MethodGet, run, "")
		aiJSON(t, ops, http.MethodGet, run, "", 200, &p)
	}
	if p.State != ai.RunStopped || p.StoppedAt != last+1 || p.Error != "stopped" {
		t.Fatalf("the stopped run: %s", text)
	}
	for _, c := range ai.BaseTests() {
		for _, sec := range c.Expect.Secrets {
			if sec != "" && strings.Contains(text, sec) {
				t.Fatalf("a planted secret of %s shows", c.Name)
			}
		}
	}
	var v ai.InstructionsView
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/auditor/instructions", "", 200, &v)
	if v.Draft.Test == nil || v.Draft.Test.State != ai.RunStopped {
		t.Fatalf("the run kept: %+v", v.Draft.Test)
	}
	var stopped, ended bool
	for i := 0; i < 200 && !(stopped && ended); i++ {
		for _, e := range auditEvents(t, aud.String()) {
			stopped = stopped || (e.Action == audit.ActionAITest && e.Detail == "test stopped")
			ended = ended || (e.Action == audit.ActionAITest && strings.HasPrefix(e.Detail, "test stopped after"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !stopped || !ended {
		t.Fatalf("the stop is not recorded:\n%s", aud.String())
	}
}

// Saving a new draft discards the one before (AI-26): its pilot ends, the
// go-live request it waited for is withdrawn and recorded so, and the
// answer says it in a notice; a request for the new draft is then taken,
// and the withdrawn one can no longer be approved.
func TestAINewDraftDiscardsTheEarlierAPI(t *testing.T) {
	srv, ops, _, aiSvc, _, aud := aiSetupWith(t, nil)
	ops2 := signIn(t, srv, "ops2@corp.example")
	ctx := context.Background()
	draft := srv.URL + "/api/v1/ai/agents/auditor/draft"
	live := srv.URL + "/api/v1/ai/agents/auditor/live"
	var first struct {
		ai.InstructionsView
		Discarded *struct{} `json:"discarded"`
	}
	aiJSON(t, ops, http.MethodPut, draft, `{"house":"Weigh PCI findings first."}`, 200, &first)
	if first.Draft == nil || first.Draft.Rev != 1 || first.Discarded != nil {
		t.Fatalf("the first draft: %+v", first)
	}
	if _, err := aiSvc.RunTests(ctx, "auditor", ai.TestOptions{Draft: true, By: "ops"}); err != nil {
		t.Fatal(err)
	}
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/auditor/pilot", `{"people":["dev@corp.example"],"hours":24}`, 200, nil)
	var waiting struct {
		Request twoperson.Request `json:"request"`
	}
	aiJSON(t, ops, http.MethodPost, live, `{}`, http.StatusAccepted, &waiting)
	if r, text := send(t, ops, http.MethodPost, live, `{}`); r.StatusCode != http.StatusConflict || !strings.Contains(text, "already waits") {
		t.Fatalf("a second request for the same draft: %d %s", r.StatusCode, text)
	}
	var second struct {
		ai.InstructionsView
		Discarded struct {
			Drafts     []int  `json:"drafts"`
			PilotEnded bool   `json:"pilotEnded"`
			Requests   []int  `json:"requests"`
			Notice     string `json:"notice"`
		} `json:"discarded"`
	}
	aiJSON(t, ops, http.MethodPut, draft, `{"house":"Weigh PCI findings first, then SOC 2."}`, 200, &second)
	want := "Earlier draft 1 was discarded; the pilot of draft 1 ended; request " + itoa(waiting.Request.ID) + " was withdrawn."
	if second.Draft == nil || second.Draft.Rev != 2 || second.Draft.Test != nil || second.Draft.Pilot != nil || second.Draft.Request != 0 || second.Discarded.Notice != want ||
		!second.Discarded.PilotEnded || len(second.Discarded.Drafts) != 1 || len(second.Discarded.Requests) != 1 {
		t.Fatalf("the second draft: %+v", second)
	}
	// The new draft may be asked for at once; untested, as it is.
	var again struct {
		Waiting bool              `json:"waiting"`
		Request twoperson.Request `json:"request"`
	}
	aiJSON(t, ops, http.MethodPost, live, `{"untested":true,"reason":"Checked by hand."}`, http.StatusAccepted, &again)
	if !again.Waiting || again.Request.Rev != 2 || again.Request.ID == waiting.Request.ID {
		t.Fatalf("the request for the new draft: %+v", again)
	}
	if r, text := send(t, ops2, http.MethodPost, srv.URL+"/api/v1/admin/requests/"+itoa(waiting.Request.ID)+"/approve", ""); r.StatusCode != http.StatusConflict || !strings.Contains(text, "withdrawn") {
		t.Fatalf("the withdrawn request approved: %d %s", r.StatusCode, text)
	}
	aiJSON(t, ops2, http.MethodPost, srv.URL+"/api/v1/admin/requests/"+itoa(again.Request.ID)+"/approve", "", 200, nil)
	v := ai.InstructionsView{}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/ai/agents/auditor/instructions", "", 200, &v)
	if v.Live != 1 || v.Draft != nil || len(v.Versions) != 1 || v.Versions[0].House != "Weigh PCI findings first, then SOC 2." {
		t.Fatalf("live: %+v", v)
	}
	withdrawn, saved := false, false
	for _, e := range auditEvents(t, aud.String()) {
		withdrawn = withdrawn || e.Action == ActionTwoPerson && strings.Contains(e.Detail, "request "+itoa(waiting.Request.ID)+" withdrawn: Auditor: instructions, draft 1, go live")
		saved = saved || e.Action == audit.ActionAIPrompt && strings.Contains(e.Detail, "draft 2 saved") && strings.Contains(e.Detail, "earlier drafts discarded [1], the pilot of draft 1 ended, requests withdrawn ["+itoa(waiting.Request.ID)+"]")
	}
	if !withdrawn || !saved {
		t.Fatalf("not recorded (withdrawn %v, saved %v):\n%s", withdrawn, saved, aud.String())
	}
}
