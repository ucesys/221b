package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Run History is one list of the runs and the run requests (AU-24,
// DB-37), newest first: a request waiting, rejected with who and why,
// withdrawn by whoever asked, and the runs; a request approved is the run
// it started, which names it. ?state= keeps one state.
func TestRunHistoryListsTheRequestsAU24(t *testing.T) {
	e := valuesSetup(t)
	other := signIn(t, e.srv, "ops2@corp.example")
	ask := func() {
		t.Helper()
		if code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"MODE":"wipe"}`)); code != http.StatusAccepted {
			t.Fatalf("%d %s", code, body)
		}
	}
	ask() // 1 waits
	ask() // 2 is rejected
	if code, body := doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/2/reject", `{"comment":"not this week"}`); code != http.StatusOK {
		t.Fatalf("reject: %d %s", code, body)
	}
	ask() // 3 is withdrawn
	if code, body := e.do(t, "POST", "/api/v1/automation/waiting-runs/3/reject", `{"comment":"wrong zone"}`); code != http.StatusOK {
		t.Fatalf("withdraw: %d %s", code, body)
	}
	ask() // 4 is approved: run 1
	if code, body := doAs(t, other, e, "POST", "/api/v1/automation/waiting-runs/4/approve", ""); code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, body)
	}
	if code, body := e.do(t, "POST", "/api/v1/automation/runs", runBody(`{"ZONE":"var:OTHER"}`)); code != http.StatusOK {
		t.Fatalf("a run: %d %s", code, body)
	}
	type entry struct {
		Kind  string    `json:"kind"`
		State string    `json:"state"`
		At    time.Time `json:"at"`
		Run   *struct {
			ID             int `json:"id"`
			ValuesApproved *struct {
				Request int `json:"request"`
			} `json:"valuesApproved"`
		} `json:"run"`
		Request *struct {
			ID        int    `json:"id"`
			Comment   string `json:"comment"`
			DecidedBy *struct {
				Name string `json:"name"`
			} `json:"decidedBy"`
		} `json:"request"`
	}
	list := func(query string) []entry {
		t.Helper()
		code, body := e.do(t, "GET", "/api/v1/automation/history"+query, "")
		if code != http.StatusOK {
			t.Fatalf("history%s: %d %s", query, code, body)
		}
		var ans struct {
			Entries []entry `json:"entries"`
		}
		if err := json.Unmarshal([]byte(body), &ans); err != nil {
			t.Fatal(err)
		}
		return ans.Entries
	}
	all := list("")
	runs, requests := map[int]entry{}, map[int]entry{}
	for i, x := range all {
		if i > 0 && x.At.After(all[i-1].At) {
			t.Errorf("not newest first: %+v after %+v", x, all[i-1])
		}
		switch {
		case x.Kind == "run" && x.Run != nil:
			runs[x.Run.ID] = x
		case x.Kind == "request" && x.Request != nil:
			requests[x.Request.ID] = x
		default:
			t.Errorf("an entry: %+v", x)
		}
	}
	if len(all) != 5 || len(runs) != 2 || len(requests) != 3 {
		t.Fatalf("the history: %+v", all)
	}
	if requests[1].State != "waiting" || requests[2].State != "rejected" || requests[3].State != "withdrawn" {
		t.Errorf("the requests' states: %+v", requests)
	}
	if r := requests[2].Request; r.Comment != "not this week" || r.DecidedBy == nil || r.DecidedBy.Name != "Ops Two" {
		t.Errorf("the rejection: %+v", r)
	}
	if r := runs[1].Run; r.ValuesApproved == nil || r.ValuesApproved.Request != 4 {
		t.Errorf("the approved request's run: %+v", r)
	}
	if got := list("?state=rejected"); len(got) != 1 || got[0].Request == nil || got[0].Request.ID != 2 {
		t.Errorf("rejected only: %+v", got)
	}
	if got := list("?state=withdrawn"); len(got) != 1 || got[0].Request.ID != 3 {
		t.Errorf("withdrawn only: %+v", got)
	}
}
