package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/roles"
)

// One search across Mycroft (KN-35): the known issues for anyone signed
// in, ranked with the exceptions for those who may read them only, as
// ask_knowledge gives them (KN-32); a credential in a record is never
// shown (X-01).
func TestMycroftSearchHonoursEachKindsReaders(t *testing.T) {
	ctx := context.Background()
	is := issues.New(issues.Options{Logger: quietLog()})
	ls := lessons.New(lessons.Options{})
	srv, _, _ := jobsSetup(t, func(o *Options) {
		o.CardRoles = []roles.ID{roles.SuperAdmin, roles.Admin}
		o.Issues = is
		o.Lessons = ls
	})
	operator := issues.Caller{Person: issues.Person{ID: "op", Name: "Op"}, Operator: true}
	title := "Kafka consumer lag on the orders topic"
	if _, err := is.SaveKnown(ctx, operator, "", issues.KnownInput{Title: &title, Symptoms: []string{"orders arrive late", "kafka lag grows past 10000"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.AddException(ctx, lessons.NewException{Cluster: "prod", Resource: "kafka/StatefulSet/broker", Title: "Kafka broker runs without a PodDisruptionBudget", Check: "no-pdb",
		Reason: "the kafka operator handles disruptions; password=hunter2"}, lessons.Person{Name: "Adam"}); err != nil {
		t.Fatal(err)
	}
	search := func(c *http.Client, query string) (int, mycroftFound) {
		t.Helper()
		resp, body := send(t, c, "GET", srv.URL+mycroftSearchPath+"?q="+query, "")
		var out mycroftFound
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatalf("%s: %v", body, err)
			}
		}
		return resp.StatusCode, out
	}
	vera, sue := signIn(t, srv, "vera"), signIn(t, srv, "sue")

	code, got := search(vera, "kafka+lag")
	if code != http.StatusOK || len(got.Hits) != 1 || got.Hits[0].ID != "KI-1" || got.Hits[0].Kind != "knownIssue" {
		t.Fatalf("a Viewer finds the known issue: %d %+v", code, got)
	}
	if slices.Contains(got.Kinds, "exception") || got.Counts["exception"] != 0 {
		t.Fatalf("a Viewer may not read the exceptions, nor learn how many match: %+v", got)
	}
	if code, got = search(vera, "kafka+broker+disruption"); code != http.StatusOK || len(got.Hits) != 1 || got.Hits[0].Kind == "exception" {
		t.Fatalf("a Viewer's search never lists an exception: %d %+v", code, got)
	}
	if code, got = search(vera, "kafka&kind=exception"); code != http.StatusOK || len(got.Hits) != 0 {
		t.Fatalf("asking for the exceptions does not give them: %d %+v", code, got)
	}

	code, got = search(sue, "kafka+broker+disruption")
	if code != http.StatusOK || !slices.Contains(got.Kinds, "exception") || len(got.Hits) != 2 || got.Hits[0].ID != "EX-1" {
		t.Fatalf("a SuperViewer finds the exception first, then the known issue: %d %+v", code, got)
	}
	for _, h := range got.Hits {
		if h.Kind == "exception" && (len(h.Terms) == 0 || h.Title == "" || h.Status != "active") {
			t.Fatalf("an exception's hit: %+v", h)
		}
	}
	code, got = search(sue, "password+hunter2")
	for _, h := range got.Hits {
		if code != http.StatusOK || slices.Contains(h.Terms, "hunter2") {
			t.Fatalf("a credential is searched or shown: %+v", h)
		}
	}
	if code, got = search(sue, "kafka&kind=knownIssue"); code != http.StatusOK || len(got.Hits) != 1 || got.Hits[0].Kind != "knownIssue" || got.Counts["exception"] != 1 {
		t.Fatalf("one kind listed, every kind counted: %d %+v", code, got)
	}
	if code, got = search(sue, "KI-1"); code != http.StatusOK || len(got.Hits) == 0 || got.Hits[0].ID != "KI-1" {
		t.Fatalf("a record is found by its id: %d %+v", code, got)
	}
	if code, got = search(sue, ""); code != http.StatusOK || len(got.Hits) != 0 {
		t.Fatalf("an empty query: %d %+v", code, got)
	}
}

// Who may search nothing is told so.
func TestMycroftSearchNothingToSearch(t *testing.T) {
	srv, _, _ := jobsSetup(t, func(o *Options) { o.Lessons = lessons.New(lessons.Options{}) })
	if resp, _ := send(t, signIn(t, srv, "vera"), "GET", srv.URL+mycroftSearchPath+"?q=x", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a Viewer with nothing to search: %d", resp.StatusCode)
	}
}
