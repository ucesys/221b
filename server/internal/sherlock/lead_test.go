package sherlock

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/fakeclaude"
)

// The answer written before a view call stays the answer (the measurement
// of November 23, 2026, F6; AI-51): a model that wrote its diagnosis, then
// called view_open with no console window to open it in, then said only
// that the view could not be opened, answers with the diagnosis and that
// line; text before a call that reads the cluster is still a note.
func TestTheAnswerBeforeAViewCallIsKeptAI51(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Key: func() string { return "sk-ant-test-0123456789" }}
	s := New(Options{Client: client})
	diagnosis := "r620-worker stopped because its kubelet lost the API server at 01:12: the node's disk filled, the kubelet was evicted and the node went NotReady. Free the disk, then restart the kubelet."
	ask := func(session, text string) Msg {
		t.Helper()
		k := Key{Session: session, Cluster: "local"}
		if _, err := s.Ask(Question{Key: k, Text: text, Cluster: "local", Tools: stubTools{}, ViewChanges: true}); err != nil {
			t.Fatal(err)
		}
		s.Wait()
		m := s.Conversation(k).View().Messages
		return *m[len(m)-1]
	}
	got := ask("s1", `Why did r620-worker stop? [[get_pod {"name":"x"}]][[say {"text":"`+diagnosis+`"}]][[view_open {"tab":"nodes"}]]`)
	if got.Status != "done" || !strings.HasPrefix(got.Text, diagnosis) || !strings.Contains(got.Text, "No console window") || got.Note != "" {
		t.Fatalf("the answer lost the diagnosis: %q (note %q)", got.Text, got.Note)
	}
	// A line before the view call, then the full answer: the answer alone.
	got = ask("s2", `Show the nodes. [[say {"text":"Opening it."}]][[view_open {"tab":"nodes"}]]`)
	if strings.Contains(got.Text, "Opening it.") || got.Text == "" {
		t.Fatalf("the line before the view call is in the answer: %q", got.Text)
	}
	// Text before a call that reads the cluster is never the answer.
	got = ask("s3", `Why? [[say {"text":"`+diagnosis+`"}]][[get_pod {"name":"x"}]]`)
	if strings.Contains(got.Text, "kubelet lost") {
		t.Fatalf("thinking aloud before a read is in the answer: %q", got.Text)
	}
}
