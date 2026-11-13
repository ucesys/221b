package issues

import (
	"strings"
	"testing"
)

// Who is involved in a case is read from what it records (EM-05): its
// reporter and filer, who opened it, its assignee, its followers, who
// wrote in its conversation (a draft not sent is no one's yet), who
// closed it while it is closed, who approved its fix; never a system
// account. The line names the rest by id (EM-08).
func TestWhoIsInvolvedInACase(t *testing.T) {
	r := &Case{Number: 3, Status: StatusClosed,
		Reporter: Person{ID: "rita", Name: "Rita", Email: "rita@corp.example"},
		FiledBy:  &Person{ID: "sue", Name: "Sue"},
		OpenedBy: &Person{ID: "system:intake", System: true},
		Owner:    &Person{ID: "olivia", Name: "Olivia"},
		Watchers: []Person{{ID: "wes"}, {ID: "rita"}},
		Messages: []Message{
			{Kind: FromOperator, From: Person{ID: "otto", Name: "Otto", Email: "otto@corp.example"}, Internal: true},
			{Kind: FromOperator, From: Person{ID: "dora"}, Draft: true},
			{Kind: FromSystem, From: Person{ID: "system", System: true}},
			{Kind: FromReporter, From: Person{ID: "rita"}},
			{Kind: FromOperator, From: Person{ID: "agent"}, SentBy: &Person{ID: "sam", Name: "Sam"}},
		},
		ClosedBy:  &Person{ID: "olivia"},
		Approvers: []Person{{ID: "abe", Name: "Abe"}},
	}
	var got []string
	for _, p := range Involved(r) {
		got = append(got, p.ID+":"+strings.Join(p.Why, "+")+map[bool]string{true: "*", false: ""}[p.Works])
	}
	want := "rita:reported+follows+wrote sue:filed olivia:assigned+closed* wes:follows otto:wrote* agent:wrote* sam:wrote* abe:approved*"
	if strings.Join(got, " ") != want {
		t.Fatalf("involved:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if p := Involved(r)[0]; p.Email != "rita@corp.example" || p.Works {
		t.Errorf("rita %+v", p)
	}
	r.Status = StatusInProgress
	for _, p := range Involved(r) {
		if strings.Contains(strings.Join(p.Why, "+"), InvolvedClosed) {
			t.Errorf("closed while open: %+v", p)
		}
	}
	l := lineOf(r)
	if strings.Join(l.Involved, ",") != "otto,agent,sam,abe" {
		t.Errorf("the line's: %v", l.Involved)
	}
	for _, id := range []string{"rita", "sue", "olivia", "wes", "otto", "sam", "abe"} {
		if !l.Involves(id) {
			t.Errorf("%s is not involved", id)
		}
	}
	for _, id := range []string{"", "dora", "system", "system:intake", "nobody"} {
		if l.Involves(id) {
			t.Errorf("%q is involved", id)
		}
	}
	AddApprovers(r, Person{ID: "abe"}, Person{ID: "system:x", System: true}, Person{ID: "ann", Name: "Ann"})
	if len(r.Approvers) != 2 || r.Approvers[1].ID != "ann" {
		t.Errorf("approvers %+v", r.Approvers)
	}
}
