package main

import (
	"testing"

	"github.com/ucesys/221b/server/internal/config"
)

// Every ConfigMap of before is named after the release in one place
// (sourceRef, caseSources): <namespace>/<fullname>-<what>, the settings'
// <fullname>, each case's <fullname>-case-<n> labelled with the release's
// name; nothing when the release is not known.
func TestTheConfigMapsOfBeforeAreNamedAfterTheRelease(t *testing.T) {
	cfg := config.Config{ReleaseNamespace: "consoles", ReleaseFullname: "k8s-console-221b", SelfRelease: "consoles/k8s-console"}
	objs := map[string]string{"": objSettings, "-audit": objAudit, "-sessions": objSessions, "-ai": objAI, "-knowledge": objKnowledge, "-automation": objAutomation,
		"-automation-runs": objAutomationRuns, "-issues": objIssues, "-proposals": objProposals, "-runs": objRuns, "-casework": objCasework, "-wiki": objWiki}
	for _, suffix := range recordSetConfigMaps {
		if suffix == "-cases" {
			// The proposed changes of before CF-104, read beside -proposals.
			if got := oldProposalsRef(sourceRef(cfg, objProposals)); got != "consoles/k8s-console-221b-cases" {
				t.Errorf("the proposals of before CF-104: %q", got)
			}
			continue
		}
		obj, ok := objs[suffix]
		if !ok {
			t.Fatalf("no object for %q", suffix)
		}
		if got := sourceRef(cfg, obj); got != "consoles/k8s-console-221b"+suffix {
			t.Errorf("%s: %q", obj, got)
		}
	}
	if prefix, instance := caseSources(cfg); prefix != "consoles/k8s-console-221b-case-" || instance != "k8s-console" {
		t.Errorf("the cases: %q, %q", prefix, instance)
	}
	if prefix, instance := caseSources(config.Config{ReleaseNamespace: "consoles", ReleaseFullname: "k8s-console-221b"}); prefix != "consoles/k8s-console-221b-case-" || instance != "k8s-console-221b" {
		t.Errorf("the cases with no release name: %q, %q", prefix, instance)
	}
	if got := sourceRef(config.Config{}, objSettings); got != "" {
		t.Errorf("with no release: %q", got)
	}
	if prefix, _ := caseSources(config.Config{}); prefix != "" {
		t.Errorf("the cases with no release: %q", prefix)
	}
}
