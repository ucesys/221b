package issues

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// listCases makes a varied set of cases in a service kept in the
// database: every kind of reporter, filer, follower, owner, service,
// status, outcome (an old one among them), source, key, type, incident
// and title, people who wrote, closed and approved; one object that holds
// only work.
func listCases(t *testing.T) (*Service, *CasesPG) {
	t.Helper()
	d := dbtest.Open(t)
	set := NewSetPG(d)
	cases := NewCasesPG(d, nil)
	s := New(Options{Backend: &kv.Keyed{Keys: map[string]kv.Backend{IndexKey: set, KnownKey: set, ConfigKey: set}, Rest: &kv.Memory{}}, Cases: cases})
	if _, err := s.SaveService(ctx, operator, "", ServiceDef{Name: "Shop", Contacts: []string{"carol", "dave"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveService(ctx, operator, "", ServiceDef{Name: "Reports", Contacts: []string{"erin"}}); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(221))
	pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
	person := func(id string) Person {
		switch id {
		case "":
			return Person{}
		case "anon":
			return Person{ID: "anon"} // no name: shown by id
		}
		return Person{ID: id, Name: "Name " + id, Email: id + "@example.com"}
	}
	opt := func(id string) *Person {
		if id == "" {
			return nil
		}
		p := person(id)
		return &p
	}
	ids := []string{"alice", "bob", "carol", "dave", "erin", "anon", ""}
	at := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	for n := 1; n <= 160; n++ {
		c := Case{Number: n, Reporter: person(pick(ids...)), FiledBy: opt(pick("", "", "bob", "olga", "alice")), Owner: opt(pick("", "", "olga", "dave", "carol")),
			Status: Status(pick("new", "inProgress", "closed")), Priority: r.Intn(5), Service: pick("", "", "shop", "reports", "gone"),
			Source: pick("", CaseReport, CaseFinding, "alert", "console"), Cluster: pick("", "prod", "stage"), Object: pick("", "deploy/web", "Pod/Ünïcode-API"),
			Type: pick("", TypePerformance, TypeSecurity, TypeOther), Incident: pick("", "", "INC42", "inc-7"), KnownIssue: pick("", "", "KI-1", "KI-2"),
			Title: pick("", "Checkout is SLOW", "Web down", "Ärger mit Login"), Text: pick("Login fails\nmore", "The reports page is slow", "ÉCHEC du paiement"),
			Created: at.Add(time.Duration(n) * time.Hour), Updated: at.Add(time.Duration(n) * time.Hour), StatusAt: at}
		if c.Status == StatusClosed {
			c.Outcome = Outcome(pick("fix", "duplicate", "noAction", "knownIssue", "exception"))
			c.ClosedBy = opt(pick("olga", "erin", "", "dave"))
		}
		if c.Source == CaseFinding && r.Intn(4) > 0 {
			c.Key = &Key{Cluster: pick("prod", "stage"), Object: c.Object, Check: pick("CrashLoop", "ImagePull")}
		}
		for _, w := range ids[:6] {
			if r.Intn(6) == 0 {
				c.Watchers = append(c.Watchers, person(w))
			}
		}
		for i := r.Intn(4); i > 0; i-- {
			c.Messages = append(c.Messages, Message{ID: i, At: at, Kind: MessageKind(pick(string(FromReporter), string(FromOperator), string(FromSystem))), From: person(pick(ids...)),
				Text: "a message", Draft: r.Intn(5) == 0, SentBy: opt(pick("", "", "erin"))})
		}
		if r.Intn(6) == 0 {
			c.Approvers = append(c.Approvers, person(pick("erin", "bob", "dave")))
		}
		b, _ := json.Marshal(c)
		if _, err := cases.Save(ctx, strconv.Itoa(n), CaseKey, b, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Outcomes from before CF-90, as a row may hold them.
	if _, err := d.Pool().Exec(ctx, "UPDATE cases SET outcome = 'notAProblem' WHERE case_number % 7 = 0 AND status = 'closed'"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, "UPDATE cases SET outcome = 'noFixNeeded' WHERE case_number % 11 = 0 AND status = 'closed'"); err != nil {
		t.Fatal(err)
	}
	// An object that holds only the work on a case is not a case.
	if _, err := d.Pool().Exec(ctx, "INSERT INTO cases (case_id, object_name, case_number, reporter_key) VALUES ($1, '999', 999, 'alice')", db.NewID()); err != nil {
		t.Fatal(err)
	}
	return s, cases
}

// The list of cases is a query (DB-88): for every caller and every filter,
// the database gives the very lines the index filtered in Go gives, in
// the same order, so nobody sees a case they could not see before and
// nobody misses one they could.
func TestTheListOfCasesIsTheGoFilter(t *testing.T) {
	s, cases := listCases(t)
	st, err := s.load(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.index.Lines) != 160 {
		t.Fatalf("the index has %d lines, want 160", len(st.index.Lines))
	}
	callers := map[string]Caller{
		"an operator": operator, "an operator without an id": {Operator: true},
		"alice": alice, "bob": bob, "carol, a contact of Shop": carol, "dave": {Person: Person{ID: "dave"}, Report: true}, "erin": {Person: Person{ID: "erin"}, Report: true},
		"anon": {Person: Person{ID: "anon"}}, "carol as a system account": {Person: Person{ID: "carol", System: true}, Report: true},
		"nobody": nobody, "no id": {Report: true}, "olga as a person": {Person: Person{ID: "olga"}, Report: true},
	}
	p := func(n int) *int { return &n }
	filters := []Filter{{}, {Mine: true}, {Status: StatusNew}, {Status: StatusClosed}, {Outcome: OutcomeNoAction}, {Outcome: legacyNotAProblem}, {Outcome: OutcomeFix},
		{Open: true}, {Pending: true}, {Service: "shop"}, {Service: "gone"}, {Priority: p(0)}, {Priority: p(3)}, {Known: "KI-1"}, {Owner: "olga"}, {Owner: "dave"},
		{Involved: true}, {Source: CaseReport}, {Source: CaseFinding}, {Source: "alert"}, {Agents: true}, {People: true}, {Cluster: "prod"}, {Unassigned: true},
		{Type: TypeOther}, {Type: TypeSecurity}, {Query: "slow"}, {Query: "C-1"}, {Query: "#12"}, {Query: "checkout SLOW"}, {Query: "crashloop"}, {Query: "inc42"},
		{Query: "name alice"}, {Query: "anon"}, {Query: "deploy/web prod"}, {Query: "ärger"}, {Query: "ünïcode"}, {Query: "login"}, {Query: "nothing-matches"},
		{Mine: true, Open: true, Query: "web"}, {Involved: true, Status: StatusClosed}, {Agents: true, Outcome: OutcomeNoAction, Cluster: "stage"},
		{Unassigned: true, Type: TypePerformance, Service: "reports"}, {Source: "console", Priority: p(2)}}
	numbers := func(ls []Line) []int {
		out := []int{}
		for _, l := range ls {
			out = append(out, l.Number)
		}
		return out
	}
	seen := 0
	for who, c := range callers {
		for _, f := range filters {
			want := st.lines(c, f)
			got, err := cases.listLines(ctx, c, f)
			if err != nil {
				t.Fatalf("%s, %+v: %v", who, f, err)
			}
			if !reflect.DeepEqual(numbers(got), numbers(want)) {
				t.Errorf("%s, %+v: the query gives %v, the Go filter %v", who, f, numbers(got), numbers(want))
				continue
			}
			for i := range got {
				if a, b := asKept(t, got[i]), asKept(t, want[i]); !reflect.DeepEqual(a, b) {
					t.Errorf("%s, %+v: C-%d reads differently:\n%+v\n%+v", who, f, got[i].Number, a, b)
				}
			}
			seen += len(got)
		}
	}
	if seen == 0 {
		t.Fatal("no filter kept any case: the test proves nothing")
	}
	t.Logf("%d lines compared over %d callers and %d filters", seen, len(callers), len(filters))
	// The counts of no action needed are the index's.
	want := map[string]int{}
	for _, l := range st.index.Lines {
		if l.AgentFound && l.Key != nil && l.Outcome.Canonical() == OutcomeNoAction {
			want[l.Key.Cluster+" "+l.Key.Check]++
		}
	}
	got, err := cases.noActionCounts(ctx)
	if err != nil || !reflect.DeepEqual(got, want) || len(want) == 0 {
		t.Fatalf("no action counts: %v, want %v: %v", got, want, err)
	}
	// The service lists through the query.
	views, lines, err := s.CaseViews(ctx, carol, Filter{})
	if err != nil || len(views) != len(lines) || fmt.Sprint(numbers(lines)) != fmt.Sprint(numbers(st.lines(carol, Filter{}))) {
		t.Fatalf("the service's list: %d views, %v", len(views), err)
	}
}

// A case's save touches its own rows, and of those only what changed: a
// message added is one row inserted, the others left as they were (DB-88).
func TestAMessageAddedIsOneInsert(t *testing.T) {
	d := dbtest.Open(t)
	s := NewCasesPG(d, nil)
	at := time.Date(2026, 11, 1, 8, 0, 0, 123456000, time.UTC)
	c := Case{Number: 1, Reporter: Person{ID: "ann"}, Status: StatusNew, Created: at, Updated: at, StatusAt: at, Watchers: []Person{{ID: "bob"}, {ID: "carol"}},
		Messages: []Message{{ID: 1, At: at, Kind: FromReporter, From: Person{ID: "ann"}, Text: "first"}}}
	b, _ := json.Marshal(c)
	v, err := s.Save(ctx, "1", CaseKey, b, "")
	if err != nil {
		t.Fatal(err)
	}
	xmin := func(what string) []string {
		rows, err := d.Pool().Query(ctx, "SELECT xmin::text FROM "+what+" ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var x string
			_ = rows.Scan(&x)
			out = append(out, x)
		}
		return out
	}
	msgs, people := xmin("case_messages"), xmin("case_people")
	c.Messages = append(c.Messages, Message{ID: 2, At: at, Kind: FromOperator, From: Person{ID: "olga"}, Text: "second"})
	b, _ = json.Marshal(c)
	if v, err = s.Save(ctx, "1", CaseKey, b, v); err != nil {
		t.Fatal(err)
	}
	after := xmin("case_messages")
	if len(after) != 2 || !contains(after, msgs[0]) {
		t.Fatalf("the first message was written again: %v, then %v", msgs, after)
	}
	if !reflect.DeepEqual(xmin("case_people"), people) {
		t.Fatal("the watchers were written again though they did not change")
	}
	// A follower goes, a message changes: the rows follow.
	c.Watchers, c.Messages[0].Text = c.Watchers[1:], "first, edited"
	b, _ = json.Marshal(c)
	if _, err = s.Save(ctx, "1", CaseKey, b, v); err != nil {
		t.Fatal(err)
	}
	keys, _, err := s.Load(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	sameAs(t, "the case", keys[CaseKey], asKept(t, c))
}
