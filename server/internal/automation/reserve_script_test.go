package automation

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const reserveScript = "../../../web/src/scripts/reserve-node-resources.sh"

func TestTheReserveScriptPassesItsChecks(t *testing.T) {
	text, err := os.ReadFile(reserveScript)
	if err != nil {
		t.Fatal(err)
	}
	h, fs := Check(string(text))
	if HasErrors(fs) {
		t.Fatalf("findings: %+v", fs)
	}
	if !h.DryRun || h.Privilege != PrivilegeFull || len(h.Params) != 4 {
		t.Fatalf("header: %+v", h)
	}
}

// stubNode is a node of files: the script's /etc lives in a directory, and
// systemctl, curl and sleep are small scripts that write what they are
// asked to a log.
type stubNode struct {
	t      *testing.T
	script string
	bin    string
	conf   string
	log    string
}

func newStubNode(t *testing.T, service string, restartWorks bool) stubNode {
	t.Helper()
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip("needs /proc/meminfo")
	}
	dir := t.TempDir()
	n := stubNode{t: t, bin: filepath.Join(dir, "bin"), conf: filepath.Join(dir, "etc"), log: filepath.Join(dir, "log")}
	text, err := os.ReadFile(reserveScript)
	if err != nil {
		t.Fatal(err)
	}
	n.script = filepath.Join(dir, "script.sh")
	if err := os.WriteFile(n.script, []byte(strings.ReplaceAll(string(text), "/etc/rancher", n.conf)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(n.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// A restart fails while the new setting is on the node when it must,
	// and works once the file is back, as a bad setting does.
	restart := `echo "restart $2" >> "` + n.log + `"`
	if !restartWorks {
		restart += `
if [ -f "` + filepath.Join(n.conf, "rke2/config.yaml.d/50-221b-reserved.yaml") + `" ] && grep -q cpu=1, "` + filepath.Join(n.conf, "rke2/config.yaml.d/50-221b-reserved.yaml") + `"; then exit 1; fi`
	}
	stubs := map[string]string{
		"systemctl": `case "$1" in
is-active) [ "$3" = "` + service + `" ] ;;
restart) ` + restart + ` ;;
esac`,
		"curl":  "exit 0",
		"sleep": "exit 0",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(n.bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func (n stubNode) run(dry string, env ...string) (result, int) {
	n.t.Helper()
	cmd := exec.Command("/bin/sh", n.script)
	cmd.Env = append([]string{"PATH=" + n.bin + ":" + os.Getenv("PATH"), "DRY_RUN=" + dry, "NODE=n1"}, env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			n.t.Fatal(err)
		}
		code = e.ExitCode()
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var r result
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		n.t.Fatalf("the last line is not a result: %q", out.String())
	}
	return r, code
}

type result struct {
	Changed bool   `json:"changed"`
	Msg     string `json:"msg"`
}

func (n stubNode) restarts() int {
	b, _ := os.ReadFile(n.log)
	return strings.Count(string(b), "restart ")
}

func (n stubNode) file(agent string) string {
	return filepath.Join(n.conf, agent, "config.yaml.d/50-221b-reserved.yaml")
}

// A dry run changes nothing, a run writes the setting and restarts the
// agent, and the same values again leave the agent alone.
func TestTheReserveScriptSetsTheKubeletAndRestartsTheAgent(t *testing.T) {
	n := newStubNode(t, "rke2-agent", true)
	env := []string{"SYSTEM_RESERVED_CPU=500m", "SYSTEM_RESERVED_MEMORY=64Mi", "KUBE_RESERVED_MEMORY=32Mi"}
	r, code := n.run("1", env...)
	if code != 0 || !r.Changed || !strings.Contains(r.Msg, "would set") {
		t.Fatalf("dry run: %d %+v", code, r)
	}
	if _, err := os.Stat(n.file("rke2")); err == nil || n.restarts() != 0 {
		t.Fatalf("the dry run changed the node: %d restarts", n.restarts())
	}
	if r, code = n.run("0", env...); code != 0 || !r.Changed {
		t.Fatalf("run: %d %+v", code, r)
	}
	b, err := os.ReadFile(n.file("rke2"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kubelet-arg+:", `"system-reserved=cpu=500m,memory=64Mi"`, `"kube-reserved=memory=32Mi"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the file lacks %s:\n%s", want, b)
		}
	}
	if n.restarts() != 1 {
		t.Fatalf("%d restarts", n.restarts())
	}
	if r, code = n.run("0", env...); code != 0 || r.Changed || n.restarts() != 1 {
		t.Fatalf("the same again: %d %+v, %d restarts", code, r, n.restarts())
	}
}

func TestTheReserveScriptFindsTheAgentItIsOn(t *testing.T) {
	for service, agent := range map[string]string{"rke2-server": "rke2", "k3s-agent": "k3s"} {
		n := newStubNode(t, service, true)
		if r, code := n.run("0", "SYSTEM_RESERVED_CPU=100m", "SYSTEM_RESERVED_MEMORY=0"); code != 0 || !strings.Contains(r.Msg, service) {
			t.Fatalf("%s: %d %+v", service, code, r)
		}
		if _, err := os.Stat(n.file(agent)); err != nil {
			t.Fatalf("%s: %v", service, err)
		}
	}
}

func TestTheReserveScriptRefusesWhatCannotWork(t *testing.T) {
	n := newStubNode(t, "rke2-agent", true)
	for _, env := range [][]string{
		{"SYSTEM_RESERVED_CPU=two", "SYSTEM_RESERVED_MEMORY=1Gi"},
		{"SYSTEM_RESERVED_CPU=1", "SYSTEM_RESERVED_MEMORY=8GB"},
		{"SYSTEM_RESERVED_CPU=1; reboot", "SYSTEM_RESERVED_MEMORY=1Gi"},
		{"SYSTEM_RESERVED_CPU=100000", "SYSTEM_RESERVED_MEMORY=1Mi"},
		{"SYSTEM_RESERVED_CPU=100m", "SYSTEM_RESERVED_MEMORY=4000Ti"},
	} {
		if r, code := n.run("0", env...); code == 0 || r.Changed {
			t.Errorf("%v: %d %+v", env, code, r)
		}
	}
	if _, err := os.Stat(n.file("rke2")); err == nil || n.restarts() != 0 {
		t.Fatalf("a refusal changed the node: %d restarts", n.restarts())
	}
}

// An agent that does not come back gets its old setting and a restart.
func TestTheReserveScriptPutsTheNodeBackWhenTheAgentFails(t *testing.T) {
	n := newStubNode(t, "rke2-agent", false)
	if r, code := n.run("0", "SYSTEM_RESERVED_CPU=100m", "SYSTEM_RESERVED_MEMORY=1Mi"); code != 0 {
		t.Fatalf("first run: %d %+v", code, r)
	}
	before, _ := os.ReadFile(n.file("rke2"))
	r, code := n.run("0", "SYSTEM_RESERVED_CPU=1", "SYSTEM_RESERVED_MEMORY=1Mi")
	if code == 0 || r.Changed || !strings.Contains(r.Msg, "back as it was") {
		t.Fatalf("%d %+v", code, r)
	}
	after, _ := os.ReadFile(n.file("rke2"))
	if !bytes.Equal(before, after) {
		t.Fatalf("the file stayed changed:\n%s", after)
	}
	if n.restarts() != 3 {
		t.Fatalf("%d restarts, want the first run's, the failed one's and the one putting it back", n.restarts())
	}
}
