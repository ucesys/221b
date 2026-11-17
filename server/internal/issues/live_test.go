package issues

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A report no known issue explains is checked against the live sources:
// a page's part that matches at the threshold is noted on the case for
// the operators, as a link; a page that does not match leaves no note; a
// report closed as a known issue is not checked (KN-43).
func TestAReportIsCheckedAgainstTheLiveSources(t *testing.T) {
	f := newFixture(t, Options{})
	asked := make(chan string, 4)
	f.s.SetLive(func(_ context.Context, text string) ([]LivePage, error) {
		asked <- text
		return []LivePage{{Source: "Status pages", Title: "All known problems", URL: "https://status.example.com/", Parts: []string{
			"The printer on floor 3 is out of toner.",
			"Mail delivery is slow for the EU tenants; the relay queue on mx-2 is full. Workaround: send again in an hour.",
		}}}, nil
	})
	rec, err := f.s.Submit(ctx, alice, Submission{Text: "Mail delivery is slow for our EU tenants, the relay queue looks full"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-asked:
		if !strings.Contains(got, "Mail delivery") {
			t.Fatalf("asked %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live sources were not asked")
	}
	var note string
	for range 100 {
		r, _ := f.s.Report(ctx, operator, rec.Report.Number)
		for _, m := range r.Messages {
			if m.Kind == FromSystem && strings.Contains(m.Text, "live source") {
				note = m.Text
				if !m.Internal {
					t.Fatal("the note is not internal")
				}
			}
		}
		if note != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(note, "“All known problems” in Status pages, https://status.example.com/") || !strings.Contains(note, "% confidence") {
		t.Fatalf("note %q", note)
	}
	if m, ok := bestLive("The VPN drops every hour", []LivePage{{Title: "All known problems", Parts: []string{"The printer on floor 3 is out of toner."}}}, 80); ok {
		t.Fatalf("matched %+v", m)
	}
}
