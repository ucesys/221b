package jobs

import "testing"

// A validation rule's meaning is the catalogue's, beside its id; an id the
// catalogues do not hold has none.
func TestRuleMeaning(t *testing.T) {
	for id, want := range map[string]string{
		"pod.root-user":       "a container runs as root",
		"pod.crashloop":       "a container restarts over and over",
		"digest.spend":        "AI agents' spend out of the ordinary",
		"pvc.nearly-full":     "a volume is 85% full or more",
		"other.my-own-thing":  "",
		"title:volume # full": "",
		"":                    "",
		"POD.ROOT-USER":       "",
	} {
		if got := RuleMeaning(id); got != want {
			t.Errorf("RuleMeaning(%q) = %q, want %q", id, got, want)
		}
	}
}

// A run's view says what the validation rules of its findings and the
// ones new on the cluster mean, by id; none when the catalogue says
// nothing.
func TestRunRuleMeanings(t *testing.T) {
	run := Run{Findings: []Finding{{Check: "pod.root-user"}, {Check: "other.odd", Key: &Key{Check: "other.odd"}}, {Key: &Key{Check: "pvc.unbound"}}}, NewChecks: []string{"netpol.missing"}}
	got := ruleMeanings(run)
	want := map[string]string{"pod.root-user": "a container runs as root", "pvc.unbound": "a claim cannot bind", "netpol.missing": "a namespace has no network policy"}
	if len(got) != len(want) {
		t.Fatalf("meanings: %v", got)
	}
	for id, m := range want {
		if got[id] != m {
			t.Fatalf("meanings: %v", got)
		}
	}
	if ruleMeanings(Run{Findings: []Finding{{Check: "other.odd"}}}) != nil {
		t.Fatal("meanings for none")
	}
}
