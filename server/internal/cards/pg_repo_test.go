package cards

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// xmins is the transaction that last wrote each row of the changes'
// tables, by table, change and place: a row whose xmin did not change was
// not written.
func xmins(t *testing.T, d *db.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	q := `SELECT 'changes ' || g.change_key, g.xmin::text FROM changes g`
	for _, table := range changeTables {
		col := "x.position::text"
		if table == "change_decisions" {
			col = "x.verdict || x.position::text"
		}
		q += ` UNION ALL SELECT '` + table + ` ' || g.change_key || ' ' || ` + col + `, x.xmin::text FROM ` + table + ` x JOIN changes g USING (change_id)`
	}
	rows, err := d.Pool().Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, x string
		if err := rows.Scan(&k, &x); err != nil {
			t.Fatal(err)
		}
		out[k] = x
	}
	return out
}

// written lists the rows written since was: changed or new.
func written(was, now map[string]string) map[string]bool {
	out := map[string]bool{}
	for k, x := range now {
		if was[k] != x {
			out[k] = true
		}
	}
	return out
}

// Each of the store's operations on one change writes that change's rows
// and no other's (DB-79): a linked case, a logged command, a recheck, an
// approval; and of the change's own rows only what changed, an entry
// added to a list being one insert.
func TestAChangeIsWrittenWithoutTheOthers(t *testing.T) {
	d := dbtest.Open(t)
	w := worldOn(t, d)
	ctx := context.Background()
	w.fire("pod:shop/web-7f9", true)
	id, err := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/web"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/api"))
	if err != nil {
		t.Fatal(err)
	}
	pat := Person{Key: "pat", Name: "Pat"}
	steps := []struct {
		what string
		do   func() error
		// want are the rows of id written, besides its row.
		want []string
	}{
		{"a case linked", func() error { _, err := w.st.Link(ctx, id, "C-14", pat); return err }, []string{"change_resolves " + id + " 0", "change_events " + id + " 1"}},
		{"a command logged", func() error {
			return w.st.LogCommands(ctx, id, []Command{{ID: "w1", Line: "kubectl get pods -n shop", By: "Pat", At: w.clock.Now(), Status: "running"}}, pat)
		}, []string{"change_commands " + id + " 0"}},
		{"a check", func() error { _, err := w.st.Recheck(ctx, id, pat); return err }, nil},
	}
	for _, s := range steps {
		was := xmins(t, d)
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.what, err)
		}
		got := written(was, xmins(t, d))
		for k := range got {
			if k == "changes "+id {
				continue
			}
			ok := false
			for _, x := range s.want {
				ok = ok || x == k
			}
			if !ok {
				t.Errorf("%s wrote %s", s.what, k)
			}
		}
		for _, x := range s.want {
			if !got[x] {
				t.Errorf("%s did not write %s", s.what, x)
			}
		}
	}
	// Approved and applied: the other change is never written.
	was := xmins(t, d)
	if _, err := w.st.Approve(ctx, id, person("sam"), "go"); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, id, Applied)
	for k := range written(was, xmins(t, d)) {
		if k == "changes "+other || strings.Contains(k, " "+other+" ") {
			t.Errorf("approving %s wrote %s", id, k)
		}
	}
	// What was read back is what was kept.
	c, err := w.st.Get(ctx, id)
	if err != nil || len(c.Approvals) != 1 || len(c.Results) != 1 || len(c.Resolves) != 1 || c.Recheck == nil {
		t.Fatalf("%+v %v", c, err)
	}
}

// Two replicas approve one change at once: one approval counts and the
// other is told the change is decided (ErrState, a 409 in the API); two
// replicas proposing at once give two changes with two numbers.
func TestTwoReplicasDecideOneChangeOnce(t *testing.T) {
	d := dbtest.Open(t)
	a, b := worldOn(t, d), worldOn(t, d)
	ctx := context.Background()
	a.fire("pod:shop/web-7f9", true)
	b.fire("pod:shop/web-7f9", true)
	for round := range 5 {
		id, err := a.st.Propose(ctx, restart("dev-1", "race:"+string(rune('a'+round))))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, w := range []*world{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = w.st.Approve(ctx, id, person([]string{"sam", "ann"}[i]), "")
			}()
		}
		wg.Wait()
		ok, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrState):
				refused++
			default:
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if ok != 1 || refused != 1 {
			t.Fatalf("round %d: %d approved and %d refused: %v", round, ok, refused, errs)
		}
		a.st.Wait()
		b.st.Wait()
		c, err := a.st.Get(ctx, id)
		if err != nil || len(c.Approvals) != 1 {
			t.Fatalf("round %d: %+v %v", round, c, err)
		}
	}
	var wg sync.WaitGroup
	ids := make([]string, 2)
	for i, w := range []*world{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _ = w.st.Propose(ctx, restart("dev-1", "both:"+string(rune('a'+i))))
		}()
	}
	wg.Wait()
	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("two proposals at once: %v", ids)
	}
}

// The newest decided changes are kept, keepFinished of them, and the
// older ones go when a change is added; a waiting change always stays.
func TestTheOldestDecidedChangesGo(t *testing.T) {
	d := dbtest.Open(t)
	p := NewCardsPG(d)
	ctx := context.Background()
	add := func(state string) {
		t.Helper()
		if err := p.Add(ctx, func(_ []Card, next int) (*Card, []Card, error) {
			c := plainCard(next)
			c.State = state
			return &c, nil, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	add(Queued)
	for range keepFinished + 3 {
		add(Rejected)
	}
	rec, err := p.All(ctx)
	if err != nil || len(rec.Cards) != keepFinished+1 || rec.Cards[0].ID != "PC-1" || rec.Cards[1].ID != "PC-5" || rec.Next != keepFinished+4 {
		t.Fatalf("%d changes, the first %s %s, next %d: %v", len(rec.Cards), rec.Cards[0].ID, rec.Cards[1].ID, rec.Next, err)
	}
}
