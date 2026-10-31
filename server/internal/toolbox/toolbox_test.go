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
		t.Fatalf("durations %v %v", c.IdleTimeout.D(), c.MaxDuration.D())
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
		ssh := tool == ToolSSH || tool == ToolSSHPair || tool == ToolSSHUnpair
		if c.On(tool) == ssh {
			t.Errorf("%s: on by default is %v", tool, c.On(tool))
		}
	}
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
