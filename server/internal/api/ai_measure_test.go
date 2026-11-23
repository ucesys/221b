package api

import (
	"net/http"
	"strings"
	"testing"
)

// Testing an agent that is off is refused with a conflict that says so,
// not a server error (the measurement of November 23, 2026, F4; AI-54).
func TestTestingAnAgentThatIsOffIsAConflictAI54(t *testing.T) {
	srv, ops, _, _, _, _ := aiSetupWith(t, nil)
	for _, body := range []string{`{"live":true,"estimate":true}`, `{"live":true,"effort":"low","replays":0}`} {
		r, text := send(t, ops, http.MethodPost, srv.URL+"/api/v1/ai/agents/summarizer/test", body)
		if r.StatusCode != http.StatusConflict || !strings.Contains(text, "the Summarizer is off") {
			t.Fatalf("%s: %d %s", body, r.StatusCode, text)
		}
	}
}
