package toolbox

import (
	"strings"
	"testing"
	"time"
)

func TestParseKeepsDefaultsAndReplacesLists(t *testing.T) {
	c, err := Parse([]byte(`{"idleTimeout":"5m","retention":"30d","debugContainer":{"images":["general"]},"nodeShell":{"approval":"always"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.IdleTimeout.D() != 5*time.Minute || c.MaxDuration.D() != time.Hour || c.Retention.D() != 30*24*time.Hour {
		t.Fatalf("durations %v %v %v", c.IdleTimeout.D(), c.MaxDuration.D(), c.Retention.D())
	}
	if len(c.DebugContainer.Images) != 1 || !c.DebugContainer.Enabled || c.NodeShell.Approval != ApprovalAlways {
		t.Fatalf("%+v", c)
	}
	if c.Images["network"].Image == "" {
		t.Fatal("the images were dropped")
	}
	if c.Limit(0) != time.Hour || c.Limit(15*time.Minute) != 15*time.Minute || c.Limit(5*time.Hour) != time.Hour {
		t.Fatal("limits")
	}
}

func TestParseRefusesValuesThatCannotWork(t *testing.T) {
	for in, want := range map[string]string{
		`{"idleTimeout":"10s"}`:                     "at least 1m",
		`{"idleTimeout":"30m","maxDuration":"10m"}`: "maxDuration",
		`{"transcripts":"s3"}`:                      "none or pvc",
		`{"transcripts":"pvc"}`:                     "directory",
		`{"debugContainer":{"images":["gpu"]}}`:     `"gpu"`,
		`{"nodeShell":{"profiles":["root"]}}`:       `"root"`,
		`{"nodeShell":{"approval":"never"}}`:        "always or cluster",
		`{"images":{"x":{"image":"a"}}}`:            "does not define",
		`{"idleTimeout":"soon"}`:                    "not valid",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestOn(t *testing.T) {
	c := Default()
	for _, tool := range Tools {
		// Unpairing works with SSH off, for the nodes paired before (the
		// toolbox review, finding 4).
		ssh := tool == ToolSSH || tool == ToolSSHPair
		if c.On(tool) == ssh {
			t.Errorf("%s: on by default is %v", tool, c.On(tool))
		}
	}
	c.NodeShell.Enabled = false
	if c.On(ToolSSHUnpair) {
		t.Error("unpairing runs a node pod: off without node shells")
	}
	c.NodeShell.Enabled = true
	c.SSH.Enabled = true
	if !c.On(ToolSSH) || !c.On(ToolSSHPair) || !c.On(ToolSSHUnpair) {
		t.Fatal("SSH asked for")
	}
	c.SSH.Enabled = false
	c.Shell.OneCommand = false
	if c.On(ToolCommand) || !c.On(ToolShell) {
		t.Fatal("one command")
	}
	c.Enabled = false
	if c.On(ToolShell) {
		t.Fatal("the toolbox is off")
	}
}

// An emptied list is an empty list, never nil: the API sends it as [] (the
// toolbox review, finding 1).
func TestImageNamesOfAnEmptyListIsEmpty(t *testing.T) {
	c := Default()
	c.DebugContainer.Images, c.DebugPod.Profiles = nil, []string{}
	for _, tool := range []string{ToolDebugContainer, ToolDebugPod, ToolShell} {
		if n := c.ImageNames(tool); n == nil || len(n) != 0 {
			t.Errorf("%s: %#v", tool, n)
		}
	}
}

// A null in an allowlist is an empty name Check refuses, not the default's
// item at that index; a list given as null is empty, not the defaults (the
// toolbox review, finding 2). A list left out keeps its default.
func TestParseReadsNullsInListsAsNothing(t *testing.T) {
	for in, want := range map[string]string{
		`{"nodeShell":{"profiles":["general",null,null]}}`:                                                      `""`,
		`{"debugContainer":{"images":["general",null]}}`:                                                        `""`,
		`{"debugPod":{"profiles":[null]}}`:                                                                      `""`,
		`{"shell":{"quickCommands":["env",null]}}`:                                                              "quickCommands",
		`{"nodeShell":{"quickCommands":[null]}}`:                                                                "quickCommands",
		`{"images":{"general":{"image":"busybox","caps":[null]},"network":{"image":"x"},"node":{"image":"y"}}}`: "caps",
	} {
		if c, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (%+v), want %q", in, err, c.NodeShell.Profiles, want)
		}
	}
	c, err := Parse([]byte(`{"nodeShell":{"profiles":null,"quickCommands":null},"debugContainer":{"images":null},"debugPod":{"profiles":null},"shell":{"quickCommands":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.NodeShell.Profiles)+len(c.NodeShell.QuickCommands)+len(c.DebugContainer.Images)+len(c.DebugPod.Profiles)+len(c.Shell.QuickCommands) != 0 {
		t.Fatalf("a null list brought the defaults back: %+v", c)
	}
	c, err = Parse([]byte(`{"images":null,"debugContainer":{"images":[]},"debugPod":{"profiles":[]},"nodeShell":{"enabled":false}}`))
	if err != nil || len(c.Images) != 0 {
		t.Fatalf("a null image map: %v %v", err, c.Images)
	}
	c, err = Parse([]byte(`{}`))
	if err != nil || len(c.NodeShell.Profiles) != 3 || len(c.Images) != 3 || len(c.Shell.QuickCommands) != 3 {
		t.Fatalf("a list left out keeps its default: %v %+v", err, c)
	}
}

// A misspelled or mis-cased key is refused, not dropped (the toolbox
// review, finding 3): encoding/json alone ignores unknown keys and matches
// keys whatever their case.
func TestParseRefusesKeysItDoesNotKnow(t *testing.T) {
	for in, want := range map[string]string{
		`{"nodeShell":{"aproval":"always"}}`:                       "toolbox.nodeShell.aproval",
		`{"nodeShell":{"Approval":"always","approval":"cluster"}}`: "toolbox.nodeShell.Approval",
		`{"debugPod":{"enable":false}}`:                            "toolbox.debugPod.enable",
		`{"IdleTimeout":"5m"}`:                                     "toolbox.IdleTimeout",
		`{"images":{"x":{"repository":"a","tag":"1"}}}`:            "toolbox.images.x.repository",
		`{"history":{"keep":5,"max":9}}`:                           "toolbox.history.max",
		`{} {}`:                                                    "not valid",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

// A node tool is on only where a request for it could pass: a node shell
// needs a profile, and a node command and SSH pairing the general profile
// they run with (the toolbox review, finding 6).
func TestOnNeedsTheProfilesARequestNeeds(t *testing.T) {
	c := Default()
	c.SSH.Enabled = true
	c.NodeShell.Profiles = []string{ProfileNetAdmin}
	if !c.On(ToolNodeShell) || c.On(ToolNodeCommand) || c.On(ToolSSHPair) {
		t.Errorf("without general: node shell %v, node command %v, pair %v", c.On(ToolNodeShell), c.On(ToolNodeCommand), c.On(ToolSSHPair))
	}
	c.NodeShell.Profiles = []string{}
	if c.On(ToolNodeShell) || c.On(ToolNodeCommand) || c.On(ToolSSHPair) {
		t.Error("with no profile a node tool is on")
	}
	c.NodeShell.Profiles = []string{ProfileGeneral}
	if !c.On(ToolNodeShell) || !c.On(ToolNodeCommand) || !c.On(ToolSSHPair) {
		t.Error("general alone")
	}
}

// With transcripts kept, the retention is a day at least and the longest
// session at least: a bare number is seconds, and retention: 400 pruned
// every transcript within the hour (the toolbox review, finding 5).
func TestParseRefusesARetentionThatPrunesLiveTranscripts(t *testing.T) {
	pvc := `"transcripts":"pvc","transcriptDir":"/t",`
	for in, want := range map[string]string{
		`{` + pvc + `"retention":400}`:                      "toolbox.retention",
		`{` + pvc + `"retention":"0"}`:                      "toolbox.retention",
		`{` + pvc + `"retention":"-24h"}`:                   "toolbox.retention",
		`{` + pvc + `"retention":"12h"}`:                    "toolbox.retention",
		`{` + pvc + `"retention":"1d","maxDuration":"48h"}`: "toolbox.maxDuration",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	for _, in := range []string{`{` + pvc + `"retention":"1d"}`, `{` + pvc + `"retention":"400d"}`, `{"retention":400}`} {
		if _, err := Parse([]byte(in)); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
}
