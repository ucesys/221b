package kube

import (
	"slices"
	"testing"
)

// Acting as a ServiceAccount, the groups sent are the account's own as
// well (kube#9): with an Impersonate-Group header the API server adds none
// of them, so a right granted to system:serviceaccounts:<ns> was refused.
func TestAServiceAccountKeepsItsOwnGroups(t *testing.T) {
	sa := Person{User: "system:serviceaccount:ci:deployer"}
	got := sa.ImpersonationGroups()
	for _, want := range []string{"system:serviceaccounts", "system:serviceaccounts:ci", "system:authenticated"} {
		if !slices.Contains(got, want) {
			t.Errorf("groups %v lack %s", got, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("groups %v", got)
	}
	// A person is not given them, nor a name that only looks like one.
	for _, p := range []Person{{User: "alice", Groups: []string{"dev"}}, {User: "system:serviceaccount:ci"}, {User: "system:serviceaccount::x"}, {User: "system:serviceaccount:a:b:c"}} {
		for _, g := range p.ImpersonationGroups() {
			if g == "system:serviceaccounts" {
				t.Errorf("%s got the ServiceAccounts' groups: %v", p.User, p.ImpersonationGroups())
			}
		}
	}
}
