package knowledge

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
)

// scaleWiki is a wiki of n synthetic pages, each about one service and
// one topic, written in the same few hundred words as the others, with an
// excerpt of a source for every fourth service.
func scaleWiki(n int) map[string]File {
	r := rand.New(rand.NewSource(7))
	filler := strings.Fields(`the service runs on nodes pods cluster namespace deployment owner team alert latency error budget request response queue worker cache database
		replica volume storage network policy secret config map ingress route certificate gateway timeout retry backoff limit memory cpu node pool upgrade
		version release rollout canary monitor dashboard metric log trace incident page escalate approve change window maintenance backup restore snapshot
		schedule cron job batch stream topic consumer producer partition broker leader follower quorum shard index search query read write commit sync`)
	topics := []string{"restart after an oomkilled crash", "rotate the tls certificate", "restore the database from a snapshot", "scale out under load", "drain a node for maintenance",
		"clear the kafka consumer lag", "fix dns resolution failures", "renew the ingress route", "free disk pressure on the volume", "roll back a bad release"}
	kinds := []string{"application", "runbook", "system", "practice"}
	files := map[string]File{}
	sentence := func() string {
		w := make([]string, 8+r.Intn(10))
		for i := range w {
			w[i] = filler[r.Intn(len(filler))]
		}
		return strings.Join(w, " ") + "."
	}
	for i := range n {
		svc := fmt.Sprintf("svc-%03d", i)
		var body strings.Builder
		fmt.Fprintf(&body, "%s is owned by team-%d and depends on db-%02d.\n\n", svc, i%17, i%40)
		for p := 0; p < 6; p++ {
			for s := 0; s < 4; s++ {
				body.WriteString(sentence() + " ")
			}
			body.WriteString("\n\n")
		}
		fmt.Fprintf(&body, "To %s on %s, follow the steps below and check its dashboard.", topics[i%len(topics)], svc)
		p := Page{Path: fmt.Sprintf("pages/%s/%s.md", kinds[i%len(kinds)], svc), Title: fmt.Sprintf("%s: %s", svc, topics[i%len(topics)]), Kind: kinds[i%len(kinds)],
			Updated: time.Date(2026, 10, 1+i%28, 0, 0, 0, 0, time.UTC), Body: body.String()}
		files[p.Path] = File{Path: p.Path, Body: p.Format()}
		if i%4 == 0 {
			key := fmt.Sprintf("infra:%d", i)
			ex, _ := json.Marshal(Excerpt{Key: key, Title: "Inventory of " + svc, Chunks: []string{sentence() + " " + svc + " " + sentence(), sentence()}})
			files[excerptPath(key)] = File{Path: excerptPath(key), Body: ex}
		}
	}
	return files
}

// With 500 pages, ask_knowledge's search and the search for people still
// give the page a question is about among the first three, each query in
// well under 50 ms (KN-36).
func TestTheIndexScalesToHundredsOfPages(t *testing.T) {
	const n = 500
	files := scaleWiki(n)
	start := time.Now()
	idx := NewIndex(files, nil, 0, time.Now())
	built := time.Since(start)
	if len(idx.Pages()) != n {
		t.Fatalf("pages: %d", len(idx.Pages()))
	}
	if built > 3*time.Second {
		t.Fatalf("building the index of %d pages took %s", n, built)
	}
	topics := []string{"how do I restart svc-%03d after it was OOMKilled", "rotate the TLS certificate of svc-%03d", "restore the database of svc-%03d from a snapshot", "scale out svc-%03d under load",
		"drain the node of svc-%03d", "svc-%03d kafka consumer lag", "dns failures in svc-%03d", "renew the ingress route for svc-%03d", "disk pressure on svc-%03d", "roll back the release of svc-%03d"}
	var spent time.Duration
	queries := 0
	for i := 3; i < n; i += 23 {
		want := fmt.Sprintf("svc-%03d.md", i)
		q := fmt.Sprintf(topics[i%len(topics)], i)
		t0 := time.Now()
		hits := idx.Search(q, "", 6)
		found := idx.Find(q, nil, 30, nil)
		spent += time.Since(t0)
		queries++
		top := func(paths []string) bool {
			for _, p := range paths[:min(3, len(paths))] {
				if strings.HasSuffix(p, "/"+want) {
					return true
				}
			}
			return false
		}
		var a, b []string
		for _, h := range hits {
			a = append(a, h.Path)
		}
		for _, h := range found.Hits {
			if h.Kind == "page" {
				b = append(b, h.Path)
			}
		}
		if !top(a) || !top(b) {
			t.Errorf("%q: ask_knowledge %v, people's search %v", q, a[:min(3, len(a))], b[:min(3, len(b))])
		}
	}
	if per := spent / time.Duration(2*queries); per > 50*time.Millisecond {
		t.Fatalf("a query takes %s on %d pages", per, n)
	} else {
		t.Logf("index of %d pages and %d documents built in %s; a query takes %s", n, idx.Size(), built, per)
	}
}

// The search for people ranks the pages, the excerpts and the records
// together: by score, each source's best excerpt once, a record found by
// its id, the kinds asked listed and every kind counted; each hit has a
// snippet around the words it matched.
func TestFindRanksEveryKindTogether(t *testing.T) {
	shop := Page{Path: "pages/application/shop.md", Title: "Application: shop", Kind: "application", Body: "The shop runs on the web pods and the database db-3.\n\n" + strings.Repeat("Filler words about nothing much at all. ", 30) + "\n\nWhen checkout hangs, restart the web pods."}
	ex, _ := json.Marshal(Excerpt{Key: "infra:3", Title: "Network notes", Chunks: []string{"The database db-3 sits behind the firewall fw-1.", "db-3 db-3 checkout firewall rules", "Nothing here."}})
	files := map[string]File{shop.Path: {Path: shop.Path, Body: shop.Format()}, excerptPath("infra:3"): {Path: excerptPath("infra:3"), Body: ex}}
	idx := NewIndex(files, nil, 0, time.Now())
	records := []Record{
		{Kind: KindKnownIssue, ID: "KI-3", Title: "Checkout hangs in the EU store", Status: "identified", Text: "Symptoms: checkout hangs after pressing pay.\n\nWorkaround: restart the web pods."},
		{Kind: KindFix, ID: "F-2", Title: "Raise the web pods' memory", Status: "active", Text: "Cause: OOMKilled under load."},
		{Kind: KindException, ID: "EX-4", Title: "Web runs without a PDB", Status: "active", Text: "Justification: one replica on purpose."},
	}
	got := idx.Find("checkout hangs", nil, 10, records)
	if len(got.Hits) < 2 || got.Hits[0].ID != "KI-3" || got.Hits[0].Kind != KindKnownIssue {
		t.Fatalf("the known issue about it comes first: %+v", got.Hits)
	}
	if !slices.Contains(got.Hits[0].Terms, "checkout") || !strings.Contains(strings.ToLower(got.Hits[0].Text), "checkout hangs") {
		t.Fatalf("a hit's terms and snippet: %+v", got.Hits[0])
	}
	var shopHit *Hit
	for i := range got.Hits {
		if got.Hits[i].Path == shop.Path {
			shopHit = &got.Hits[i]
		}
	}
	if shopHit == nil || !strings.Contains(shopHit.Text, "checkout hangs") || len([]rune(shopHit.Text)) > snippetRunes+2 || strings.Contains(shopHit.Text, "\n") {
		t.Fatalf("the page's snippet is one line around its match: %+v", shopHit)
	}
	got = idx.Find("db-3 firewall", nil, 10, records)
	excerpts := 0
	for _, h := range got.Hits {
		if h.Kind == "excerpt" {
			excerpts++
		}
	}
	if excerpts != 1 || got.Counts["excerpt"] != 1 {
		t.Fatalf("a source is listed once, by its best excerpt: %+v", got)
	}
	got = idx.Find("web pods", []string{KindFix, KindException}, 10, records)
	if len(got.Hits) != 2 || got.Hits[0].Kind == KindKnownIssue || got.Hits[1].Kind == KindKnownIssue || got.Counts["page"] != 1 || got.Counts[KindKnownIssue] != 1 {
		t.Fatalf("the kinds asked are listed, every kind counted: %+v", got)
	}
	if got = idx.Find("ex-4", nil, 10, records); len(got.Hits) != 1 || got.Hits[0].ID != "EX-4" {
		t.Fatalf("a record by its id: %+v", got.Hits)
	}
	if got = idx.Find("the of", nil, 10, records); len(got.Hits) != 0 || got.Hits == nil {
		t.Fatalf("stop words find nothing, an empty list: %+v", got)
	}
	if got = NewIndex(nil, nil, 0, time.Now()).Find("web", nil, 0, records); len(got.Hits) != 3 {
		t.Fatalf("records alone, without a wiki: %+v", got.Hits)
	}
}

func TestSnippetIsAroundTheFirstMatch(t *testing.T) {
	text := strings.Repeat("alpha beta gamma ", 40) + "the restarting pod " + strings.Repeat("delta ", 40)
	s := snippet(text, []string{"restart"})
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "restarting pod") {
		t.Fatalf("snippet: %q", s)
	}
	if strings.HasPrefix(strings.TrimPrefix(s, "…"), "lpha") || strings.HasPrefix(strings.TrimPrefix(s, "…"), "eta") {
		t.Fatalf("the snippet starts at a word: %q", s)
	}
	if s := snippet("short text", []string{"x"}); s != "short text" {
		t.Fatalf("a short text is whole: %q", s)
	}
}
