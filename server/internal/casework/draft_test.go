package casework

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// rhea reads the cases and works none of them.
var rhea = Caller{Person: Person{Subject: "rhea", Name: "Rhea"}, ID: "rhea", Read: true}

// A case has one draft, shared by everyone who works it (CF-59): Save
// keeps it for everyone, a save from a draft someone changed since is
// refused with theirs unless it saves over it, and sending a part clears
// it for everyone, the history naming who drafted it and who sent it.
func TestOneSharedDraft(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if _, err := w.iss.Submit(ctx, rita, issues.Submission{Text: "The shop is down"}); err != nil {
		t.Fatal(err)
	}
	v, err := w.svc.Keep(ctx, "C-1", DraftInput{Reply: "We are on it.", Close: &Closing{Outcome: "duplicate", Explanation: "password=hunter2 same as C-4"}, RCA: &Completion{Cause: "half written"}}, olivia)
	if err != nil || v.Draft == nil || v.Draft.Reply != "We are on it." || v.Draft.By != "Olivia" || v.Draft.Updated.IsZero() || strings.Contains(v.Draft.Close.Explanation, "hunter2") {
		t.Fatalf("saved: %+v %v", v.Draft, err)
	}
	first := v.Draft.Updated
	// Everyone who works the case sees it, and those who read the cases.
	for _, c := range []Caller{sam, rhea} {
		if theirs, _ := w.svc.Get(ctx, c, "C-1"); theirs.Draft == nil || theirs.Draft.Reply != "We are on it." || theirs.Draft.By != "Olivia" {
			t.Errorf("%s does not see the draft: %+v", c.Name, theirs.Draft)
		}
	}
	// Sam edits it from where Olivia left it.
	w.now = w.now.Add(2 * time.Minute)
	v, err = w.svc.Keep(ctx, "C-1", DraftInput{Reply: "We are on it; the shop is back.", Close: v.Draft.Close, RCA: v.Draft.RCA, Base: first}, sam)
	if err != nil || v.Draft.By != "Sam" || !v.Draft.Updated.After(first) || strings.Join(v.Draft.Drafters[partReply], ",") != "Olivia,Sam" || strings.Join(v.Draft.Drafters[partRCA], ",") != "Olivia" {
		t.Fatalf("sam's save: %+v %v", v.Draft, err)
	}
	second := v.Draft.Updated
	// Olivia saves from the draft she started from: refused, with Sam's.
	_, err = w.svc.Keep(ctx, "C-1", DraftInput{Reply: "Mine", Base: first}, olivia)
	var dc *DraftConflict
	if !errors.As(err, &dc) || dc.Draft.By != "Sam" || !dc.Draft.Updated.Equal(second) || dc.Draft.Reply != "We are on it; the shop is back." {
		t.Fatalf("a stale save: %v", err)
	}
	rr := httptest.NewRecorder()
	failErr(rr, err)
	var body struct {
		Error string `json:"error"`
		Draft Draft  `json:"draft"`
	}
	if json.Unmarshal(rr.Body.Bytes(), &body); rr.Code != http.StatusConflict || body.Draft.By != "Sam" || !strings.Contains(body.Error, "Sam changed this draft") {
		t.Errorf("409: %d %s", rr.Code, rr.Body.String())
	}
	// Saving what is there already is no conflict.
	if _, err := w.svc.Keep(ctx, "C-1", DraftInput{Reply: dc.Draft.Reply, Close: dc.Draft.Close, RCA: dc.Draft.RCA, Base: first}, olivia); err != nil {
		t.Errorf("the same draft: %v", err)
	}
	// Overwrite: hers over his, which the history says.
	w.now = w.now.Add(time.Minute)
	v, err = w.svc.Keep(ctx, "C-1", DraftInput{Reply: "Mine", RCA: &Completion{Cause: "half written"}, Base: first, Force: true}, olivia)
	if err != nil || v.Draft.Reply != "Mine" || v.Draft.Close != nil || v.Draft.By != "Olivia" || !strings.Contains(historyOf(v), "Draft saved over Sam's newer changes [Olivia/person]") {
		t.Fatalf("overwrite: %+v %v\n%s", v.Draft, err, historyOf(v))
	}
	// Sending the reply clears it for everyone; the history says who drafted and who sent it.
	w.now = w.now.Add(time.Minute)
	v, err = w.svc.Say(ctx, "C-1", Message{Text: "Mine", Internal: true}, sam)
	if err != nil || v.Draft == nil || v.Draft.Reply != "" || v.Draft.RCA == nil || v.Draft.By != "Sam" || len(v.Draft.Drafters[partReply]) != 0 {
		t.Fatalf("after sending the reply: %+v %v", v.Draft, err)
	}
	if !strings.Contains(historyOf(v), "Internal note added; drafted by Olivia, Sam; sent by Sam [Sam/person]") {
		t.Errorf("history:\n%s", historyOf(v))
	}
	if theirs, _ := w.svc.Get(ctx, olivia, "C-1"); theirs.Draft.Reply != "" {
		t.Errorf("olivia still sees the reply: %+v", theirs.Draft)
	}
	// A note never drafted leaves no line.
	before := len(v.History)
	if v, _ = w.svc.Say(ctx, "C-1", Message{Text: "Another", Internal: true}, sam); len(v.History) != before {
		t.Errorf("a note not drafted: %s", historyOf(v))
	}
	// Emptied, it keeps who emptied it and when, the stamp the next save starts from.
	v, err = w.svc.Keep(ctx, "C-1", DraftInput{Base: v.Draft.Updated}, olivia)
	if err != nil || v.Draft == nil || !v.Draft.Empty() || v.Draft.By != "Olivia" {
		t.Errorf("emptied: %+v %v", v.Draft, err)
	}
	if !strings.Contains(w.audit.String(), `"action":"case.keep"`) || strings.Contains(w.audit.String(), "half written") {
		t.Errorf("audit %s", w.audit.String())
	}
}

// Closing as fixed clears what was done and the RCA from the draft, for
// everyone (CF-59).
func TestAFixClearsItsDraft(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	c, err := w.svc.Create(ctx, NewCase{Title: "Disk full"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Keep(ctx, c.ID, DraftInput{Reply: "a note", Fix: "Grew the disk", RCA: &Completion{Cause: "Logs"}}, olivia); err != nil {
		t.Fatal(err)
	}
	v, err := w.svc.Fix(ctx, c.ID, FixInput{Text: "Grew the disk", RCA: &Completion{Cause: "Logs"}}, sam)
	if err != nil || v.Draft == nil || v.Draft.Fix != "" || v.Draft.RCA != nil || v.Draft.Reply != "a note" {
		t.Fatalf("after the fix: %+v %v", v.Draft, err)
	}
	if !strings.Contains(historyOf(v), "Closed as fixed: Grew the disk (with its RCA); drafted by Olivia; sent by Sam") {
		t.Errorf("history:\n%s", historyOf(v))
	}
}

// The drafts each person kept before CF-59 become the case's one draft
// when it is read: for each part, the newest; reading it again changes
// nothing, and the next write keeps it.
func TestDraftsBeforeCF59AreShared(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	c, err := w.svc.Create(ctx, NewCase{Title: "Disk full"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	keys, version, err := w.svc.cases().Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(keys[issues.WorkKey], &raw)
	at := func(m int) string { return w.now.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	raw["drafts"] = map[string]any{
		"olivia": map[string]any{"reply": "Hers", "fix": "Her fix", "at": at(1)},
		"sam":    map[string]any{"reply": "His, newer", "close": map[string]any{"outcome": "noFixNeeded", "explanation": "Gone"}, "at": at(5)},
	}
	b, _ := json.Marshal(raw)
	if _, err := w.svc.cases().Save(ctx, "1", issues.WorkKey, b, version); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		v := w.get(t, c.ID)
		d := v.Draft
		if d == nil || d.Reply != "His, newer" || d.Fix != "Her fix" || d.Close == nil || d.Close.Explanation != "Gone" || d.Close.Outcome != issues.OutcomeNoAction || d.By != "sam" || d.Updated.Format(time.RFC3339) != at(5) {
			t.Fatalf("shared: %+v", d)
		}
		if d.Drafters[partReply][0] != "sam" || d.Drafters[partFix][0] != "olivia" {
			t.Errorf("drafters: %+v", d.Drafters)
		}
	}
	// The next write keeps the shared one, and the drafts of each go.
	if _, err := w.svc.Keep(ctx, c.ID, DraftInput{Reply: "Ours", Fix: "Her fix", Close: &Closing{Outcome: "noFixNeeded", Explanation: "Gone"}, Base: w.get(t, c.ID).Draft.Updated}, olivia); err != nil {
		t.Fatal(err)
	}
	keys, _, _ = w.svc.cases().Load(ctx, "1")
	if s := string(keys[issues.WorkKey]); strings.Contains(s, `"drafts"`) || !strings.Contains(s, `"reply":"Ours"`) {
		t.Errorf("stored: %s", s)
	}
}

// An edit in place sends what it started from for each field it changes
// (CF-60): a field changed since is refused with its value now, compared
// value by value, unless the person saves over it, which the history says.
func TestEditInPlaceComparesEachField(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	c, err := w.svc.Create(ctx, NewCase{Title: "One", Detail: "first", Cluster: "prod", Resource: "shop/Deployment/web", Incident: "INC1"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	v, err := w.svc.Update(ctx, c.ID, Change{Title: str("Two"), Base: &FieldBase{Title: str("One")}}, sam)
	if err != nil || v.Title != "Two" {
		t.Fatalf("an edit from the value there: %+v %v", v.Brief, err)
	}
	w.now = w.now.Add(time.Minute)
	_, err = w.svc.Update(ctx, c.ID, Change{Title: str("Three"), Base: &FieldBase{Title: str("One")}}, olivia)
	var fc *FieldConflict
	if !errors.As(err, &fc) || fc.Field != "title" || fc.Current != "Two" || fc.By != "Sam" || fc.At == nil {
		t.Fatalf("a stale title: %#v", err)
	}
	if v := w.get(t, c.ID); v.Title != "Two" {
		t.Errorf("the title changed anyway: %s", v.Title)
	}
	rr := httptest.NewRecorder()
	failErr(rr, err)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), `"current":"Two"`) || !strings.Contains(rr.Body.String(), `"field":"title"`) || !strings.Contains(rr.Body.String(), `"by":"Sam"`) {
		t.Errorf("409: %d %s", rr.Code, rr.Body.String())
	}
	// Each field is compared by its value.
	for _, ch := range []struct {
		field string
		ch    Change
	}{
		{"detail", Change{Detail: str("second"), Base: &FieldBase{Detail: str("other")}}},
		{"resource", Change{Resource: str("Node/n1"), Base: &FieldBase{Resource: str("shop/Deployment/api")}}},
		{"incident", Change{Incident: str("INC2"), Base: &FieldBase{Incident: str("")}}},
		{"cluster", Change{Cluster: str("local"), Base: &FieldBase{Cluster: str("local")}}},
	} {
		if _, err := w.svc.Update(ctx, c.ID, ch.ch, olivia); !errors.As(err, &fc) || fc.Field != ch.field {
			t.Errorf("%s: %v", ch.field, err)
		}
	}
	// A field unchanged elsewhere saves, with others compared too.
	if v, err = w.svc.Update(ctx, c.ID, Change{Incident: str("INC2"), Cluster: str(""), Base: &FieldBase{Incident: str("INC1"), Cluster: str("prod")}}, olivia); err != nil || v.Incident != "INC2" || v.Cluster != "" {
		t.Fatalf("%+v %v", v.Brief, err)
	}
	// Overwrite saves over it, and says so.
	v, err = w.svc.Update(ctx, c.ID, Change{Title: str("Three"), Force: true}, olivia)
	if err != nil || v.Title != "Three" || !strings.Contains(historyOf(v), "Title changed, saved over a newer change [Olivia/person]") {
		t.Errorf("overwrite: %+v %v\n%s", v.Brief, err, historyOf(v))
	}
}

// A case and an Inbox row name their cluster (CF-60), and say when the
// console no longer has it; the case keeps it until someone picks another.
func TestTheCaseNamesItsCluster(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	if _, err := w.svc.Create(ctx, NewCase{Title: "On prod", Cluster: "prod"}, olivia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Create(ctx, NewCase{Title: "Nowhere"}, olivia); err != nil {
		t.Fatal(err)
	}
	v := w.get(t, "C-1")
	if v.ClusterName != "Production" || v.ClusterConnected == nil || !*v.ClusterConnected {
		t.Fatalf("named: %q %v", v.ClusterName, v.ClusterConnected)
	}
	if v := w.get(t, "C-2"); v.ClusterName != "" || v.ClusterConnected != nil {
		t.Errorf("no cluster: %q %v", v.ClusterName, v.ClusterConnected)
	}
	// prod is let go of in Settings › Clusters.
	w.svc.o.Clusters = func() []jobs.Cluster { return []jobs.Cluster{{ID: "local", Name: "Lakehouse"}} }
	v = w.get(t, "C-1")
	if v.Cluster != "prod" || v.ClusterName != "prod" || v.ClusterConnected == nil || *v.ClusterConnected {
		t.Errorf("gone: %q %q %v", v.Cluster, v.ClusterName, v.ClusterConnected)
	}
	list, err := w.svc.List(ctx, olivia, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range list {
		if b.ID == "C-1" && (b.ClusterName != "prod" || *b.ClusterConnected) {
			t.Errorf("row: %+v", b)
		}
	}
	str := "local"
	if v, err := w.svc.Update(ctx, "C-1", Change{Cluster: &str, Base: &FieldBase{Cluster: &v.Cluster}}, olivia); err != nil || v.ClusterName != "Lakehouse" || !*v.ClusterConnected {
		t.Errorf("picked another: %+v %v", v.Brief, err)
	}
}
