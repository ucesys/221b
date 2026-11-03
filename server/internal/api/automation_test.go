package api

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
)

// A run goes to the nodes it names and no others, wherever they sort in the
// fleet: a script on a node nobody picked cannot be taken back.
func TestARunTakesOnlyTheNodesItNames(t *testing.T) {
	fleet := []automationNode{{Name: "a", Paired: true}, {Name: "b"}, {Name: "c", Paired: true}, {Name: "d", Paired: true}}
	names := func(nodes []automationNode) []string {
		out := []string{}
		for _, n := range nodes {
			out = append(out, n.Name)
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		in      []string
		want    []string
		unknown []string
	}{
		{name: "the first node alone", in: []string{"a"}, want: []string{"a"}},
		{name: "a node in the middle", in: []string{"c"}, want: []string{"c"}},
		{name: "the last node alone", in: []string{"d"}, want: []string{"d"}},
		{name: "several, out of order", in: []string{"d", "a"}, want: []string{"a", "d"}},
		{name: "named twice", in: []string{"c", "c"}, want: []string{"c"}},
		{name: "none named is the whole fleet", want: []string{"a", "b", "c", "d"}},
		{name: "a name the fleet lacks", in: []string{"a", "z", "y"}, want: []string{"a"}, unknown: []string{"y", "z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			picked, unknown := pickNodes(fleet, tc.in)
			if got := names(picked); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("picked %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(unknown, tc.unknown) {
				t.Errorf("unknown %v, want %v", unknown, tc.unknown)
			}
		})
	}
}

// The Parameters page lists the scripts that read a parameter: what the
// latest version declares and what the approved one still does.
func TestAScriptSummaryListsWhatItsVersionsRead(t *testing.T) {
	sc := automation.Script{Name: "a", Versions: []automation.Version{
		{N: 1, State: automation.StateApproved, Header: automation.Header{Params: []automation.Param{{Name: "OLD"}, {Name: "BOTH"}}}},
		{N: 2, State: automation.StateDraft, Header: automation.Header{Params: []automation.Param{{Name: "BOTH"}, {Name: "NEW", Required: true}}}},
	}}
	var got []string
	for _, p := range summaryOf(sc, nil).Params {
		got = append(got, p.Name)
	}
	if want := []string{"BOTH", "NEW", "OLD"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
