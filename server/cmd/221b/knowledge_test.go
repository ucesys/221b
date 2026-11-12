package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// ask_knowledge gives the open known issues a question matches beside
// the wiki's pages and excerpts, each by its id with its status,
// symptoms and workaround; a resolved one, or one that matches too
// little, is left out (CF-43).
func TestAskKnowledgeGivesTheKnownIssuesCF43(t *testing.T) {
	ctx := context.Background()
	svc := issues.New(issues.Options{Backend: &kv.Memory{}, Cases: &kv.MemoryObjects{}})
	op := issues.Caller{Person: issues.Person{ID: "olivia", Name: "Olivia"}, Operator: true}
	str := func(s string) *string { return &s }
	if _, err := svc.SaveKnown(ctx, op, "", issues.KnownInput{Title: str("Checkout fails with 502 errors"), Symptoms: []string{"checkout returns 502 bad gateway"}, Workaround: str("Retry the payment")}); err != nil {
		t.Fatal(err)
	}
	hits := knownHits(ctx, svc, "Why does checkout return 502 bad gateway?")
	if len(hits) != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	h := hits[0]
	if h.Kind != sherlock.KindKnownIssue || h.Path != "KI-1" || h.Title != "Checkout fails with 502 errors" || !strings.Contains(h.Text, "Symptoms: checkout returns 502 bad gateway.") || !strings.Contains(h.Text, "Workaround: Retry the payment") {
		t.Fatalf("hit: %+v", h)
	}
	if hits := knownHits(ctx, svc, "certificates on the ingress"); len(hits) != 0 {
		t.Fatalf("an unrelated question: %+v", hits)
	}
}

// ask_knowledge gives the active exceptions a question is about, to who
// may read them (KN-32): one on an object the question names, or whose
// finding it names enough of, with its cluster, object, rule,
// justification and last day, scrubbed; an expired or removed one, or one
// on another subject, is left out. The question's asker decides whether
// any is given at all (sherlock.Question.Exceptions; never a reporter's).
func TestAskKnowledgeGivesTheActiveExceptionsKN32(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	clock := now.AddDate(0, 0, -5)
	svc := lessons.New(lessons.Options{Backend: &kv.Memory{}, Now: func() time.Time { return clock }})
	by := lessons.Person{Name: "Olivia"}
	add := func(in lessons.NewException) {
		t.Helper()
		if _, err := svc.AddException(ctx, in, by); err != nil {
			t.Fatal(err)
		}
	}
	add(lessons.NewException{Cluster: "prod", Resource: "host/r820", Check: "inventory.host-unreachable", Title: "r820 does not answer", Reason: "Unplugged on purpose until the new disks come; password: hunter2hunter2 is not needed.", Expires: now.AddDate(0, 0, 20)})
	add(lessons.NewException{Cluster: "prod", Resource: "shop/Deployment/web", Check: "pod.no-limits", Title: "web has no memory limits", Reason: "A test app."})
	add(lessons.NewException{Cluster: "prod", Resource: "host/r710", Check: "inventory.host-unreachable", Title: "r710 does not answer", Reason: "Gone.", Expires: now.AddDate(0, 0, -1)})
	clock = now
	name := func(id string) string { return map[string]string{"prod": "Production"}[id] }

	hits := exceptionHits(ctx, svc, "Why is r820 unreachable?", name)
	if len(hits) != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	h := hits[0]
	if h.Kind != sherlock.KindException || h.Path != "EX-1" || h.Title != "r820 does not answer" || !strings.Contains(h.Text, "Cluster: Production (prod). Object: host/r820. Rule: inventory.host-unreachable") || !strings.Contains(h.Text, "Justification: Unplugged on purpose") || !strings.Contains(h.Text, "Last day: 2026-11-29.") || strings.Contains(h.Text, "hunter2hunter2") {
		t.Fatalf("hit: %+v", h)
	}
	if hits := exceptionHits(ctx, svc, "the web container has no cpu or memory limits", name); len(hits) != 1 || hits[0].Path != "EX-2" || !strings.Contains(hits[0].Text, "Permanent") {
		t.Fatalf("by the finding's words: %+v", hits)
	}
	if hits := exceptionHits(ctx, svc, "Why is r710 unreachable?", name); len(hits) != 0 {
		t.Fatalf("an expired exception: %+v", hits)
	}
	if hits := exceptionHits(ctx, svc, "certificates on the ingress", name); len(hits) != 0 {
		t.Fatalf("an unrelated question: %+v", hits)
	}
}

// The names a question holds are matched as written, and an object's name
// is its last part (KN-32).
func TestQuestionNamesKN32(t *testing.T) {
	n := questionNames("Is r820's disk (shop/Deployment/web-7f9) OK?")
	for _, w := range []string{"r820", "disk", "web-7f9", "shop", "deployment"} {
		if !n[w] {
			t.Errorf("%q is missing: %v", w, n)
		}
	}
	if objectName("shop/Deployment/Web-7f9") != "web-7f9" || objectName("r820") != "r820" || objectName("") != "" {
		t.Fatal("objectName")
	}
}

// Mycroft is given only active fixes: those of a check, those a text
// matches, and those seen in enough cases; never a pending or an inactive
// one (KN-33, KN-34).
func TestMycroftIsGivenOnlyActiveFixesKN33(t *testing.T) {
	ctx := context.Background()
	svc := fixes.New(fixes.Options{Backend: &kv.Memory{}})
	by := fixes.Person{Name: "Olivia"}
	save := func(in fixes.FromCase) string {
		t.Helper()
		s, err := svc.SaveFromCase(ctx, in, by)
		if err != nil {
			t.Fatal(err)
		}
		return s.Fix.ID
	}
	disk := save(fixes.FromCase{Cases: []string{"C-1", "C-2", "C-3"}, Title: "Disk pressure on worker nodes", Key: "node.disk-pressure", Steps: "Prune the images", Commands: []string{"crictl rmi --prune"}})
	off := save(fixes.FromCase{Cases: []string{"C-4", "C-5", "C-6"}, Title: "Certificate expired on the ingress", Steps: "Renew the certificate"})
	inactive := fixes.Inactive
	if _, err := svc.Update(ctx, off, fixes.Edit{Status: &inactive}, by); err != nil {
		t.Fatal(err)
	}
	k := knowledgeFixes{svc}
	list, err := k.For(ctx, "node.disk-pressure", "", 4)
	if err != nil || len(list) != 1 || list[0].ID != disk || list[0].Cases != 3 || list[0].Check != "node.disk-pressure" || list[0].Commands[0] != "crictl rmi --prune" {
		t.Fatalf("by check: %+v %v", list, err)
	}
	if list, _ := k.For(ctx, "", "the certificate expired on the ingress", 4); len(list) != 0 {
		t.Fatalf("an inactive fix was given: %+v", list)
	}
	if list, _ := k.Proven(ctx, 3); len(list) != 1 || list[0].ID != disk {
		t.Fatalf("proven: %+v", list)
	}
}

// What keeps an inventory or a drift difference from being proposed
// (KN-31): an active exception for the cluster, object and rule, counted
// as a match; an open known issue whose service runs on the host, or
// whose title names it whole, or whose keys name the object. A resolved
// known issue, an exception for another rule or cluster, and a name too
// short to tell do not cover.
func TestKnowledgeCoversKN31(t *testing.T) {
	ctx := context.Background()
	is := issues.New(issues.Options{Backend: &kv.Memory{}, Cases: &kv.MemoryObjects{}})
	op := issues.Caller{Person: issues.Person{ID: "olivia", Name: "Olivia"}, Operator: true}
	str := func(s string) *string { return &s }
	svc, err := is.SaveService(ctx, op, "", issues.ServiceDef{Name: "Backups", RunsOn: []issues.RunsOn{{Kind: "host", Value: "r730"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []issues.KnownInput{
		{Title: str("Backups are slow"), Services: []string{svc.ID}},
		{Title: str("rke2cp1 is down for repairs")},
		{Title: str("The web shop is slow")},
	} {
		if _, err := is.SaveKnown(ctx, op, "", in); err != nil {
			t.Fatal(err)
		}
	}
	resolved := issues.KnownResolved
	k, err := is.SaveKnown(ctx, op, "", issues.KnownInput{Title: str("worker9 lost its disk")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := is.SaveKnown(ctx, op, k.ID, issues.KnownInput{Status: &resolved}); err != nil {
		t.Fatal(err)
	}
	ls := lessons.New(lessons.Options{})
	ex, err := ls.AddException(ctx, lessons.NewException{Cluster: "local", Resource: "Host/r820", Check: "inventory.host-unreachable", Title: "Not answering", Reason: "Powered off until the new PSU comes"}, lessons.Person{Name: "Olivia"})
	if err != nil {
		t.Fatal(err)
	}
	c := knowledgeCovers{issues: is, lessons: ls}
	cover := c.Cover(ctx)
	for _, tc := range []struct{ cluster, object, rule, known, exception string }{
		{"local", "Host/r820", "inventory.host-unreachable", "", ex.ID},
		{"local", "host/R820", "inventory.host-unreachable", "", ex.ID},
		{"local", "Host/r820", "inventory.address-differs", "", ""},
		{"prod", "Host/r820", "inventory.host-unreachable", "", ""},
		{"local", "Host/r730", "inventory.host-unlisted", "KI-1", ""},
		{"local", "Node/rke2cp1", "inventory.host-unreachable", "KI-2", ""},
		{"local", "Node/rke2cp10", "inventory.host-unreachable", "", ""},
		{"local", "Host/web", "inventory.host-unlisted", "", ""},
		{"local", "Node/worker9", "inventory.host-unreachable", "", ""},
	} {
		known, exception := cover(tc.cluster, tc.object, tc.rule)
		if known != tc.known || exception != tc.exception {
			t.Errorf("%s %s %s: %q %q, want %q %q", tc.cluster, tc.object, tc.rule, known, exception, tc.known, tc.exception)
		}
	}
	c.Matched(ctx, map[string]int{ex.ID: 2})
	if got, _ := ls.Exception(ctx, ex.ID); got.Matches != 2 || got.LastMatched.IsZero() {
		t.Errorf("the matches: %+v", got)
	}
	// A key a case closed as the known issue left names the object.
	key := issues.KnownIssue{ID: "KI-9", Keys: []issues.KnownFound{{Key: issues.Key{Cluster: "local", Object: "Node/rke2cp2", Check: "node.not-ready"}}}}
	if !knownCovers(key, nil, "local", "node/rke2cp2") || knownCovers(key, nil, "prod", "Node/rke2cp2") {
		t.Error("a known issue's key")
	}
}

// A fleet's members are the nodes and hosts it takes, sorted (KN-30).
func TestFleetMembersKN30(t *testing.T) {
	nodes := []fleets.Node{
		{Name: "rke2cp2", Labels: map[string]string{"role": "cp"}},
		{Name: "rke2cp1", Labels: map[string]string{"role": "cp"}},
		{Name: "r820", Labels: map[string]string{"role": "storage"}},
	}
	if got := takes(fleets.Fleet{Name: "CP", Labels: []string{"role=cp"}}, nodes); strings.Join(got, ",") != "rke2cp1,rke2cp2" {
		t.Errorf("by labels: %v", got)
	}
	if got := takes(fleets.Fleet{Name: "Burst", Labels: []string{"pool=burst"}}, nodes); got == nil || len(got) != 0 {
		t.Errorf("none: %#v", got)
	}
	if got := takes(fleets.Fleet{Name: "List", Kind: fleets.KindNodes, Nodes: []string{"r820"}}, nodes); strings.Join(got, ",") != "r820" {
		t.Errorf("a list: %v", got)
	}
}
