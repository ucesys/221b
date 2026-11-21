package issues

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// A case's links are rows keyed to the cases they name (DB-89): a case
// made after the case that names it takes its key then; a case that goes
// empties the key and leaves the number, which the case still shows.
func TestACasesLinksAreKeys(t *testing.T) {
	d := dbtest.Open(t)
	s := NewCasesPG(d, nil)
	at := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	save := func(c Case) {
		t.Helper()
		b, _ := json.Marshal(c)
		if _, err := s.Save(ctx, strconv.Itoa(c.Number), CaseKey, b, ""); err != nil {
			t.Fatal(err)
		}
	}
	one := Case{Number: 1, Reporter: Person{ID: "ann"}, Status: StatusNew, Created: at, Updated: at, StatusAt: at}
	two := one
	two.Number, two.DuplicateOf, two.RegressionOf, two.SameAs, two.PossibleDuplicateOf, two.Similar = 2, 1, 3, 1, 3, []int{3, 1}
	save(one)
	save(two)
	keyed := func() int {
		var n int
		if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM case_links WHERE linked_case_id IS NOT NULL").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := keyed(); n != 3 {
		t.Fatalf("%d links keyed, want the three to C-1", n)
	}
	three := one
	three.Number = 3
	save(three)
	if n := keyed(); n != 6 {
		t.Fatalf("%d links keyed once C-3 is there, want 6", n)
	}
	keys, _, err := s.Load(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	sameAs(t, "the case with its links", keys[CaseKey], asKept(t, two))
	if err := s.Delete(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if n := keyed(); n != 3 {
		t.Fatalf("%d links keyed once C-1 is gone, want the three to C-3", n)
	}
	keys, _, _ = s.Load(ctx, "2")
	sameAs(t, "the case once C-1 is gone", keys[CaseKey], asKept(t, two))
	// The list shows the numbers too.
	lines, err := s.listLines(ctx, operator, Filter{})
	if err != nil || len(lines) != 2 || lines[1].DuplicateOf != 1 || lines[1].RegressionOf != 3 || lines[1].SameAs != 1 || lines[1].PossibleDup != 3 {
		t.Fatalf("the list: %+v, %v", lines, err)
	}
}

// A known issue's services are rows keyed to the services (DB-89): a
// service changed keeps its row, so they keep their keys; one deleted
// leaves the known issues that listed it.
func TestAKnownIssuesServicesAreKeys(t *testing.T) {
	d := dbtest.Open(t)
	set := NewSetPG(d)
	f := newFixture(t, Options{Backend: &kv.Keyed{Keys: map[string]kv.Backend{IndexKey: set, KnownKey: set, ConfigKey: set}, Rest: &kv.Memory{}}, Cases: NewCasesPG(d, nil)})
	k := seed(t, f)
	keyed := func() int {
		var n int
		if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM known_issue_services WHERE service_id IS NOT NULL").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := keyed(); n != 1 {
		t.Fatalf("%d services keyed, want shop", n)
	}
	if _, err := f.s.SaveService(ctx, operator, "shop", ServiceDef{Name: "Shop", Description: "The store, renamed", Contacts: []string{"carol"}}); err != nil {
		t.Fatal(err)
	}
	if n := keyed(); n != 1 {
		t.Fatalf("a service changed lost its key: %d keyed", n)
	}
	if err := f.s.DeleteService(ctx, operator, "shop"); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.KnownIssue(ctx, operator, k.ID)
	if err != nil || len(got.Services) != 0 || keyed() != 0 {
		t.Fatalf("the known issue's services once shop is gone: %v, %v", got.Services, err)
	}
}
