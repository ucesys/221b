package automation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

const goodScript = `#!/bin/sh
# 221b:describe Report mounts above a threshold
#     and the largest directories on each
# 221b:param THRESHOLD optional percent full
# 221b:param MOUNT required the mount to look at
# 221b:privilege none
# 221b:dry-run yes
set -eu
if [ "$DRY_RUN" = 1 ]; then echo would look; fi
df -P "$MOUNT"
echo '{"changed": false, "msg": "fine"}'
`

// The new-script template writes its header with spaces and colons.
func TestHeaderSpacesAndColonsAreOptional(t *testing.T) {
	for _, text := range []string{
		"# 221b:describe Say hello\n# 221b:param NAME required who\n# 221b:privilege none\n# 221b:dry-run yes\necho \"$DRY_RUN\"\n",
		"# 221b: describe: Say hello\n# 221b: param: NAME required who\n# 221b: privilege: none\n# 221b: dry-run: yes\necho \"$DRY_RUN\"\n",
		"#221b:  describe  Say hello\n#221b:param: NAME required who\n#221b: privilege none\n# 221b:dry-run: yes\necho \"$DRY_RUN\"\n",
	} {
		h, fs := ParseHeader(text)
		if len(fs) != 0 {
			t.Errorf("%q: %+v", text, fs)
		}
		if h.Describe != "Say hello" || h.Privilege != PrivilegeNobody || !h.DryRun || len(h.Params) != 1 || h.Params[0].Name != "NAME" {
			t.Errorf("%q: %+v", text, h)
		}
	}
}

func TestParseHeader(t *testing.T) {
	h, fs := ParseHeader(goodScript)
	if HasErrors(fs) {
		t.Fatalf("findings: %+v", fs)
	}
	if h.Describe != "Report mounts above a threshold and the largest directories on each" {
		t.Errorf("describe = %q", h.Describe)
	}
	if h.Privilege != PrivilegeNobody || !h.DryRun {
		t.Errorf("privilege %q dry-run %v", h.Privilege, h.DryRun)
	}
	if len(h.Params) != 2 || h.Params[0].Name != "THRESHOLD" || h.Params[0].Required || !h.Params[1].Required || h.Params[1].Desc != "the mount to look at" {
		t.Errorf("params = %+v", h.Params)
	}
	_, fs = ParseHeader("# 221b:param lower required\n# 221b:param NODE\n# 221b:privilege limited\necho\n")
	codes := map[string]bool{}
	for _, f := range fs {
		codes[f.Code] = true
	}
	for _, c := range []string{"param-name", "param-reserved", "privilege-limited", "describe"} {
		if !codes[c] {
			t.Errorf("no %s finding in %+v", c, fs)
		}
	}
	// The header ends at the first command.
	h, _ = ParseHeader("# 221b:describe x\n# 221b:privilege full\necho hi\n# 221b:param LATE\n")
	if len(h.Params) != 0 {
		t.Errorf("a param after the first command was read: %+v", h.Params)
	}
}

func TestCheck(t *testing.T) {
	if _, fs := Check(goodScript); len(fs) != 0 {
		t.Fatalf("a clean script has findings: %+v", fs)
	}
	head := "# 221b:describe x\n# 221b:privilege none\n"
	cases := map[string]string{
		"sudo-undeclared":    head + "sudo systemctl restart x\n",
		"dry-run-unused":     "# 221b:describe x\n# 221b:privilege full\n# 221b:dry-run yes\necho\n",
		"download-unchecked": head + "curl -fsSLo /tmp/a https://example.com/a\n",
		"deny-rm-root":       head + "rm -rf /\n",
		"deny-mkfs":          head + "mkfs.ext4 /dev/sdb\n",
		"deny-dd":            head + "dd if=/dev/zero of=/dev/sda\n",
		"deny-forkbomb":      head + ":(){ :|:& };:\n",
		"deny-iptables":      head + "iptables -F\n",
		"deny-pipe-shell":    head + "curl -fsSL https://x/install.sh | sh\n",
		"apps":               head + "helm upgrade x ./chart\n",
		"reboot":             "# 221b:describe x\n# 221b:privilege full\nreboot\n",
	}
	for code, text := range cases {
		_, fs := Check(text)
		found := false
		for _, f := range fs {
			found = found || f.Code == code
		}
		if !found {
			t.Errorf("%s: not found in %+v", code, fs)
		}
	}
	// A checked download is fine.
	_, fs := Check(head + "curl -fsSLo /tmp/a https://x/a\necho \"$SUM  /tmp/a\" | sha256sum -c -\n")
	for _, f := range fs {
		if f.Code == "download-unchecked" {
			t.Errorf("a checked download was flagged: %+v", fs)
		}
	}
}

func newStore(now *time.Time) *Store {
	return NewStore(&kv.Memory{}, func() time.Time { return *now })
}

func TestVersionsAndApproval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	st := newStore(&now)
	ann, bob := Who{Name: "Ann", User: "ann"}, Who{Name: "Bob", User: "bob"}
	s, err := st.Save(ctx, "disk-report", "Diagnostics", goodScript, ann, true)
	if err != nil || len(s.Versions) != 1 || s.Latest().State != StateDraft {
		t.Fatalf("create: %+v %v", s, err)
	}
	if _, err := st.Save(ctx, "disk-report", "", goodScript, ann, true); !errors.Is(err, ErrExists) {
		t.Errorf("a second create: %v", err)
	}
	if s, _ = st.Save(ctx, "disk-report", "Diag", goodScript, ann, false); len(s.Versions) != 1 || s.Group != "Diag" {
		t.Errorf("an unchanged save made a version: %+v", s)
	}
	if _, err := st.Save(ctx, "disk-report", "", strings.Repeat("x", MaxText+1), ann, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("too long: %v", err)
	}
	if _, err := st.Save(ctx, "Bad Name", "", goodScript, ann, true); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := Runnable(s); err == nil {
		t.Error("a draft is runnable")
	}
	// Saves stay in the draft, until it is submitted.
	s, err = st.Save(ctx, "disk-report", "", goodScript+"echo one\n", ann, false)
	if err != nil || len(s.Versions) != 1 || s.Latest().N != 1 || !strings.Contains(s.Latest().Text, "echo one") {
		t.Fatalf("a save of a draft: %+v %v", s, err)
	}
	if _, err := st.Approve(ctx, "disk-report", 1, bob); err == nil {
		t.Error("a draft was approved without review")
	}
	if _, err := st.Submit(ctx, "disk-report", 1, ann); err != nil {
		t.Fatal(err)
	}
	// Whoever wrote it cannot approve it.
	if _, err := st.Approve(ctx, "disk-report", 1, ann); !errors.Is(err, ErrOwnWork) {
		t.Fatalf("the author approved: %v", err)
	}
	if s, err = st.Approve(ctx, "disk-report", 1, bob); err != nil || s.Latest().State != StateApproved {
		t.Fatalf("approve: %+v %v", s, err)
	}
	if _, err := st.Approve(ctx, "disk-report", 1, bob); err == nil {
		t.Error("the same person approved twice")
	}
	if v, err := Runnable(s); err != nil || v.N != 1 {
		t.Errorf("approved by someone else: %v %v", v.N, err)
	}
	// A version once submitted never changes: the next save starts v2.
	s, _ = st.Save(ctx, "disk-report", "", goodScript+"echo two\n", bob, false)
	if s.Latest().N != 2 {
		t.Fatalf("v2: %+v", s.Latest())
	}
	if v1, _ := s.Version(1); strings.Contains(v1.Text, "echo two") {
		t.Error("an approved version changed")
	}
	// Someone who saved a draft cannot approve it either, nor can the one
	// who submitted it.
	cid := Who{Name: "Cid", User: "cid"}
	s, _ = st.Save(ctx, "disk-report", "", goodScript+"echo three\n", ann, false)
	if s.Latest().N != 2 {
		t.Fatalf("a second person's save: %+v", s.Latest())
	}
	st.Submit(ctx, "disk-report", 2, cid)
	for _, who := range []Who{ann, bob, cid} {
		if _, err := st.Approve(ctx, "disk-report", 2, who); !errors.Is(err, ErrOwnWork) {
			t.Errorf("%s approved: %v", who.Name, err)
		}
	}
	s, err = st.Approve(ctx, "disk-report", 2, Who{Name: "Dee", User: "dee"})
	if err != nil {
		t.Fatal(err)
	}
	if v1, _ := s.Version(1); v1.State != StateApproved {
		t.Errorf("v1 is %s", v1.State)
	}
	// A newer submission replaces one still in review.
	s, _ = st.Save(ctx, "disk-report", "", goodScript+"echo four\n", ann, false)
	st.Submit(ctx, "disk-report", 3, ann)
	s, _ = st.Save(ctx, "disk-report", "", goodScript+"echo five\n", ann, false)
	s, _ = st.Submit(ctx, "disk-report", 4, ann)
	if v3, _ := s.Version(3); v3.State != StateSuperseded {
		t.Errorf("v3 is %s", v3.State)
	}
	// An approval by the author alone, from before the rule, runs nothing.
	self := Script{Name: "old", Versions: []Version{{N: 1, State: StateApproved, Author: ann, Approvals: []Approval{{By: ann}}}}}
	if _, err := Runnable(self); err == nil {
		t.Error("a version only its author approved is runnable")
	}
	// Off production the latest version runs, approved or not; on
	// production only an approved one does.
	draft := Script{Name: "new", Versions: []Version{{N: 1, State: StateDraft, Author: ann}, {N: 2, State: StateDraft, Author: ann}}}
	if v, err := RunVersion(draft, false, 0); err != nil || v.N != 2 {
		t.Errorf("off production: %v %v", v.N, err)
	}
	if v, err := RunVersion(draft, false, 1); err != nil || v.N != 1 {
		t.Errorf("off production, the version seen: %v %v", v.N, err)
	}
	if _, err := RunVersion(draft, false, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such version: %v", err)
	}
	if _, err := RunVersion(Script{Name: "empty"}, false, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("no version at all: %v", err)
	}
	if _, err := RunVersion(draft, true, 2); err == nil {
		t.Error("a draft ran on production")
	}
	if _, err := RunVersion(self, true, 0); err == nil {
		t.Error("a version only its author approved ran on production")
	}
	// A script that does not declare a dry run has none.
	if _, err := DryRunnable(Script{Name: "x", Versions: []Version{{N: 1}}}, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("no dry run declared: %v", err)
	}
	if v, err := DryRunnable(Script{Versions: []Version{{N: 1, Header: Header{DryRun: true}}}}, 1); err != nil || v.N != 1 {
		t.Errorf("a dry run: %v %v", v.N, err)
	}
	if _, err := DryRunnable(Script{Versions: []Version{{N: 1}}}, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such version: %v", err)
	}
	// A version with errors cannot be submitted.
	s, _ = st.Save(ctx, "disk-report", "", "echo no header\n", bob, false)
	if _, err := st.Submit(ctx, "disk-report", s.Latest().N, bob); err == nil {
		t.Error("a version with errors was submitted")
	}
}

func TestPrune(t *testing.T) {
	rec := &scriptsRecord{Scripts: map[string]Script{}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Script{Name: "big"}
	for i := 1; i <= 30; i++ {
		state := StateSuperseded
		if i == 30 {
			state = StateApproved
		}
		s.Versions = append(s.Versions, Version{N: i, Text: strings.Repeat("y", 30<<10), State: state, Created: base.Add(time.Duration(i) * time.Hour)})
	}
	rec.Scripts["big"] = s
	prune(rec, 200<<10, nil)
	got := rec.Scripts["big"].Versions
	if got[len(got)-1].N != 30 || len(got) > 6 || got[0].N < 25 {
		t.Errorf("kept %d versions from v%d", len(got), got[0].N)
	}
}

func TestParams(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := newStore(&now)
	fp := FleetVariables{Values: map[string]string{"MOUNT": "/var", "THRESHOLD": "85"}, Nodes: map[string]map[string]string{"gpu-07": {"MOUNT": "/data"}}}
	if err := st.SetVariables(ctx, "local", "gpu", fp); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVariables(ctx, "local", "gpu", FleetVariables{Values: map[string]string{"lower": "x"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad name: %v", err)
	}
	if err := st.SetVariables(ctx, "local", "gpu", FleetVariables{Values: map[string]string{"NODE": "x"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a reserved name: %v", err)
	}
	if err := st.SetVariables(ctx, "local", "gpu", FleetVariables{Ceiling: "limited"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad ceiling: %v", err)
	}
	got, _ := st.Variables(ctx, "local")
	h, _ := ParseHeader(goodScript)
	r := Resolve(h, got["gpu"], "gpu-07", map[string]string{"THRESHOLD": "90"})
	if r.Values["MOUNT"] != "/data" || r.Sources["MOUNT"] != SourceNode || r.Values["THRESHOLD"] != "90" || r.Sources["THRESHOLD"] != SourceRun {
		t.Errorf("gpu-07: %+v", r)
	}
	r = Resolve(h, got["gpu"], "gpu-01", nil)
	if r.Values["MOUNT"] != "/var" || r.Sources["MOUNT"] != SourceFleet {
		t.Errorf("gpu-01: %+v", r)
	}
	r = Resolve(h, FleetVariables{}, "x", nil)
	if len(r.Missing) != 1 || r.Missing[0] != "MOUNT" {
		t.Errorf("missing: %+v", r)
	}
	if (FleetVariables{Ceiling: PrivilegeNobody}).Allows(PrivilegeElevated) || (FleetVariables{}).Allows(PrivilegeElevated) || !(FleetVariables{Ceiling: PrivilegeElevated}).Allows(PrivilegeElevated) {
		t.Error("the ceiling")
	}
}

func TestBatches(t *testing.T) {
	ns := []string{"a", "b", "c", "d", "e"}
	str := func(b [][]string) string { return fmt.Sprint(b) }
	if got := str(Batches(ns, Rollout{Mode: RolloutAll})); got != "[[a b c d e]]" {
		t.Errorf("all: %s", got)
	}
	if got := str(Batches(ns, Rollout{Mode: RolloutSize, Size: 2})); got != "[[a b] [c d] [e]]" {
		t.Errorf("size: %s", got)
	}
	if got := str(Batches(ns, Rollout{Mode: RolloutCanary, Size: 3})); got != "[[a] [b c d] [e]]" {
		t.Errorf("canary: %s", got)
	}
	if got := str(Batches(ns, Rollout{Mode: RolloutCanary})); got != "[[a] [b c d e]]" {
		t.Errorf("canary, the rest: %s", got)
	}
}

func TestParseResult(t *testing.T) {
	if r, ok := ParseResult("doing\n{\"changed\": true, \"msg\": \"done\", \"reboot\": true}\n\n"); !ok || !r.Changed || !r.Reboot || r.Msg != "done" {
		t.Errorf("%+v %v", r, ok)
	}
	if _, ok := ParseResult("no json here\n"); ok {
		t.Error("plain text read as a result")
	}
}

// fakeExec plays the nodes: each node's script answers by its name.
type fakeExec struct {
	mu       sync.Mutex
	commands []string
	nobody   map[string]bool
	reboots  int
	down     map[string]bool
	block    chan struct{}
	// boots counts each node's boots; stuck nodes never reboot, and
	// rebootExit is what the reboot command exits with.
	boots      map[string]int
	stuck      map[string]bool
	rebootExit int
	// running and peak count the scripts running at once.
	running, peak int
}

var errDown = errors.New("unreachable: connection refused")

func (f *fakeExec) Exec(ctx context.Context, cluster, node, keyID, command string, asNobody bool, stdin io.Reader, out io.Writer) (int, error) {
	f.mu.Lock()
	f.commands = append(f.commands, node+": "+command)
	if f.nobody == nil {
		f.nobody = map[string]bool{}
	}
	f.nobody[node] = asNobody
	down := f.down[node]
	block := f.block
	if strings.Contains(command, "reboot") {
		f.reboots++
		if f.boots == nil {
			f.boots = map[string]int{}
		}
		if !f.stuck[node] {
			f.boots[node]++
		}
	}
	boot := f.boots[node]
	f.mu.Unlock()
	if !strings.Contains(keyID, "via 221B automation run") {
		return -1, fmt.Errorf("key ID %q", keyID)
	}
	if down {
		return -1, errDown
	}
	if command == BootIDCommand {
		fmt.Fprintf(out, "boot-%d\n", boot)
		return 0, nil
	}
	if strings.Contains(command, "reboot") {
		return f.rebootExit, nil
	}
	if command == "true" {
		return 0, nil
	}
	f.mu.Lock()
	f.running++
	f.peak = max(f.peak, f.running)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	script, _ := io.ReadAll(stdin)
	fmt.Fprintf(out, "ran %d bytes\n", len(script))
	switch {
	case strings.HasPrefix(node, "fail"):
		fmt.Fprintln(out, "it broke")
		return 3, nil
	case strings.HasPrefix(node, "boot"):
		fmt.Fprintln(out, `{"changed": true, "msg": "updated", "reboot": true}`)
	case strings.HasPrefix(node, "cp"):
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintln(out, `{"changed": true, "msg": "updated"}`)
	case strings.HasPrefix(node, "chg"):
		fmt.Fprintln(out, `{"changed": true, "msg": "updated"}`)
	default:
		fmt.Fprintln(out, `{"changed": false, "msg": "fine"}`)
	}
	return 0, nil
}

func testEngine(t *testing.T, f *fakeExec) (*Engine, chan Run) {
	t.Helper()
	ended := make(chan Run, 4)
	e := NewEngine(Options{Store: NewStore(&kv.Memory{}, nil), Exec: f, Unreachable: func(err error) bool { return errors.Is(err, errDown) },
		OnEnd: func(r Run) { ended <- r }, RebootPoll: time.Millisecond, RebootWait: time.Second, BeatEvery: time.Hour})
	return e, ended
}

func wait(t *testing.T, ended chan Run) Run {
	t.Helper()
	select {
	case r := <-ended:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end")
	}
	return Run{}
}

func approved(privilege string) Version {
	text := strings.Replace(goodScript, "privilege none", "privilege "+privilege, 1)
	h, fs := Check(text)
	return Version{N: 3, Text: text, Hash: Hash(text), Header: h, Findings: fs, State: StateApproved}
}

func TestRun(t *testing.T) {
	f := &fakeExec{}
	e, ended := testEngine(t, f)
	sp := Spec{Script: "disk-report", Version: approved(PrivilegeElevated), Cluster: "local", Fleet: "gpu", Nodes: []string{"ok-1", "chg-2", "boot-3"},
		Values: map[string]map[string]string{"ok-1": {"MOUNT": "/var"}}, Rollout: Rollout{Mode: RolloutAll}, Who: Who{Name: "Ann", User: "ann"}}
	r, err := e.Start(context.Background(), sp)
	if err != nil || r.ID != 1 {
		t.Fatalf("start: %+v %v", r, err)
	}
	r = wait(t, ended)
	if r.State != RunSucceeded {
		t.Fatalf("state %s: %+v", r.State, r.Nodes)
	}
	states := map[string]string{}
	for _, n := range r.Nodes {
		states[n.Node] = n.State
	}
	if states["ok-1"] != NodeNoChange || states["chg-2"] != NodeChanged || states["boot-3"] != NodeChanged {
		t.Errorf("states %v", states)
	}
	if f.reboots != 1 {
		t.Errorf("%d reboots", f.reboots)
	}
	f.mu.Lock()
	var cmd string
	for _, c := range f.commands {
		if strings.HasPrefix(c, "ok-1: env") {
			cmd = c
		}
	}
	nobody := f.nobody["ok-1"]
	f.mu.Unlock()
	for _, want := range []string{"DRY_RUN='0'", "MOUNT='/var'", "NODE='ok-1'", "RUN_ID='1'", "CLUSTER='local'", "FLEET='gpu'", "sh -s"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command %q lacks %s", cmd, want)
		}
	}
	if nobody {
		t.Error("a full-privilege run dropped to nobody")
	}
	got, outs, ok, _ := e.Get(context.Background(), r.ID)
	if !ok || got.State != RunSucceeded || !strings.Contains(outs["chg-2"], "ran ") {
		t.Errorf("get: %v %+v %q", ok, got.State, outs["chg-2"])
	}
	list, _ := e.Runs(context.Background())
	if len(list) != 1 || list[0].ID != 1 {
		t.Errorf("runs: %+v", list)
	}
}

func TestDryRunAndPrivilegeNone(t *testing.T) {
	f := &fakeExec{}
	e, ended := testEngine(t, f)
	_, err := e.Start(context.Background(), Spec{Version: approved(PrivilegeElevated), DryRun: true, Nodes: []string{"boot-1"}, Rollout: Rollout{}})
	if err != nil {
		t.Fatal(err)
	}
	r := wait(t, ended)
	if !f.nobody["boot-1"] || f.reboots != 0 {
		t.Errorf("dry run: nobody %v, reboots %d", f.nobody["boot-1"], f.reboots)
	}
	if r.Nodes[0].State != NodeChanged {
		t.Errorf("a dry run's would-change: %s", r.Nodes[0].State)
	}
	if _, err := e.Start(context.Background(), Spec{Version: approved(PrivilegeNobody), Nodes: []string{"ok-2"}}); err != nil {
		t.Fatal(err)
	}
	wait(t, ended)
	if !f.nobody["ok-2"] {
		t.Error("privilege none ran as the login user")
	}
}

func TestStopAndFailures(t *testing.T) {
	f := &fakeExec{down: map[string]bool{"down-2": true}}
	e, ended := testEngine(t, f)
	// The first batch stops the run on any failure.
	e.Start(context.Background(), Spec{Version: approved(PrivilegeElevated), Nodes: []string{"fail-1", "ok-2", "ok-3"}, Rollout: Rollout{Mode: RolloutCanary}})
	r := wait(t, ended)
	if r.State != RunStopped || r.Nodes[0].State != NodeFailed || r.Nodes[1].State != NodeSkipped || *r.Nodes[0].Exit != 3 {
		t.Errorf("canary: %s %+v", r.State, r.Nodes)
	}
	// Unreachable counts apart from failed; with no limit the run goes on
	// and ends failed.
	e.Start(context.Background(), Spec{Version: approved(PrivilegeElevated), Nodes: []string{"ok-1", "down-2", "ok-3"}, Rollout: Rollout{Mode: RolloutSize, Size: 1}})
	r = wait(t, ended)
	if r.State != RunFailed || r.Nodes[1].State != NodeUnreachable || r.Nodes[2].State != NodeNoChange {
		t.Errorf("unreachable: %s %+v", r.State, r.Nodes)
	}
	// A limit of 1 stops after the first failure.
	e.Start(context.Background(), Spec{Version: approved(PrivilegeElevated), Nodes: []string{"ok-1", "fail-2", "ok-3", "ok-4"}, Rollout: Rollout{Mode: RolloutSize, Size: 1, StopAfter: 1}})
	r = wait(t, ended)
	if r.State != RunStopped || r.Nodes[2].State != NodeSkipped {
		t.Errorf("limit: %s %+v", r.State, r.Nodes)
	}
}

func TestCancel(t *testing.T) {
	f := &fakeExec{block: make(chan struct{})}
	e, ended := testEngine(t, f)
	r, _ := e.Start(context.Background(), Spec{Version: approved(PrivilegeElevated), Nodes: []string{"ok-1", "ok-2"}, Rollout: Rollout{Mode: RolloutSize, Size: 1}})
	time.Sleep(50 * time.Millisecond)
	if !e.Running(r.ID) || !e.Cancel(r.ID) {
		t.Fatal("not running")
	}
	r = wait(t, ended)
	if r.State != RunCancelled || r.Nodes[0].State != NodeCancelled || r.Nodes[1].State != NodeCancelled {
		t.Errorf("cancel: %s %+v", r.State, r.Nodes)
	}
}

func TestInterruptedAndPrune(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	st := newStore(&now)
	r, _ := st.NewRun(ctx, Run{State: RunRunning, Beat: now, Nodes: []NodeResult{{Node: "a", State: NodeRunning}}})
	now = now.Add(5 * time.Minute)
	list, _ := st.Runs(ctx)
	if list[0].ID != r.ID || list[0].State != RunInterrupted || list[0].Nodes[0].State != NodeCancelled {
		t.Errorf("interrupted: %+v", list[0])
	}
	for i := 0; i < MaxRuns+5; i++ {
		st.NewRun(ctx, Run{State: RunSucceeded, Nodes: []NodeResult{{Node: "n", State: NodeNoChange, Tail: strings.Repeat("z", 1500)}}})
	}
	list, _ = st.Runs(ctx)
	if len(list) > MaxRuns || list[0].ID != MaxRuns+6 {
		t.Errorf("kept %d, newest %d", len(list), list[0].ID)
	}
}

func TestCommandQuotes(t *testing.T) {
	c := Command(map[string]string{"A": "it's", "B": "$(rm -rf /)"})
	if c != `env A='it'\''s' B='$(rm -rf /)' sh -s` {
		t.Errorf("%s", c)
	}
}

// A new version does not take the approved one away: it runs until another
// is approved, on production and, when asked for, anywhere else.
func TestAnApprovedVersionRunsUntilAnotherIsApproved(t *testing.T) {
	approved := Version{N: 2, State: StateApproved, Author: Who{Name: "Ann"}, Approvals: []Approval{{By: Who{Name: "Sam"}}}}
	for _, state := range []string{StateDraft, StateReview} {
		s := Script{Name: "s", Versions: []Version{approved, {N: 3, State: state, Author: Who{Name: "Ann"}}}}
		if v, err := RunVersion(s, true, 0); err != nil || v.N != 2 {
			t.Fatalf("%s on production: v%d, %v", state, v.N, err)
		}
		if v, err := RunVersion(s, false, 2); err != nil || v.N != 2 {
			t.Fatalf("%s elsewhere, v2 asked for: v%d, %v", state, v.N, err)
		}
		if v, err := RunVersion(s, false, 3); err != nil || v.N != 3 {
			t.Fatalf("%s elsewhere, v3 asked for: v%d, %v", state, v.N, err)
		}
	}
}

func TestApprovedVersionsStayApprovedAndAnyCanBeChosenOnProduction(t *testing.T) {
	ann, sam := Who{Name: "Ann"}, Who{Name: "Sam"}
	ok := []Approval{{By: sam}}
	s := Script{Name: "s", Versions: []Version{
		{N: 1, State: StateApproved, Author: ann, Approvals: ok},
		{N: 2, State: StateSuperseded, Author: ann, Approvals: ok},
		{N: 3, State: StateApproved, Author: ann, Approvals: []Approval{{By: ann}}},
		{N: 4, State: StateApproved, Author: ann, Approvals: ok},
		{N: 5, State: StateDraft, Author: ann},
	}}
	if v, err := RunVersion(s, true, 1); err != nil || v.N != 1 {
		t.Fatalf("v1: v%d, %v", v.N, err)
	}
	if v, err := RunVersion(s, true, 0); err != nil || v.N != 4 {
		t.Fatalf("no version asked for: v%d, %v", v.N, err)
	}
	for _, n := range []int{2, 3, 5, 9} {
		if _, err := RunVersion(s, true, n); err == nil {
			t.Errorf("v%d ran on production", n)
		}
	}
}

func TestAnySuperAdminRevokesAnApprovalAndItStopsRunning(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	ann, sam, pat := Who{Name: "Ann"}, Who{Name: "Sam"}, Who{Name: "Pat"}
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho one\n"
	if _, err := st.Save(ctx, "s", "", text, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Submit(ctx, "s", 1, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Approve(ctx, "s", 1, sam); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(ctx, "s", "", text+"echo two\n", ann, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Submit(ctx, "s", 2, ann); err != nil {
		t.Fatal(err)
	}
	s, err := st.Approve(ctx, "s", 2, sam)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Version(1); v.State != StateApproved {
		t.Fatalf("approving v2 left v1 %s", v.State)
	}
	if s, err = st.Revoke(ctx, "s", 2, pat); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Version(2)
	if v.State != StateReview || len(v.Approvals) != 0 || len(v.Revoked) != 1 || v.Revoked[0].By.Name != "Pat" {
		t.Fatalf("after the revoke: %+v", v)
	}
	if _, err := RunVersion(s, true, 2); err == nil {
		t.Fatal("a revoked version ran on production")
	}
	if got, err := RunVersion(s, true, 0); err != nil || got.N != 1 {
		t.Fatalf("the newest still approved: v%d, %v", got.N, err)
	}
	if _, err := st.Revoke(ctx, "s", 2, pat); err == nil {
		t.Fatal("revoked a version with nothing to revoke")
	}
	if s, err = st.Approve(ctx, "s", 2, sam); err != nil || s.Versions[1].State != StateApproved {
		t.Fatalf("approving again: %v", err)
	}
}

// An older version a newer one replaced can be approved again, by someone
// who did not write it; a draft cannot.
func TestAnOlderVersionCanBeApproved(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	ann, sam := Who{Name: "Ann"}, Who{Name: "Sam"}
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho one\n"
	st.Save(ctx, "s", "", text, ann, true)
	st.Submit(ctx, "s", 1, ann)
	st.Save(ctx, "s", "", text+"echo two\n", ann, false)
	s, _ := st.Submit(ctx, "s", 2, ann)
	if s.Versions[0].State != StateSuperseded {
		t.Fatalf("v1 is %s", s.Versions[0].State)
	}
	if _, err := st.Approve(ctx, "s", 1, ann); err == nil {
		t.Fatal("the author approved an older version")
	}
	s, err := st.Approve(ctx, "s", 1, sam)
	if err != nil || s.Versions[0].State != StateApproved {
		t.Fatalf("%v %+v", err, s.Versions[0])
	}
	st.Save(ctx, "s", "", text+"echo three\n", ann, false)
	if _, err := st.Approve(ctx, "s", 3, sam); err == nil {
		t.Fatal("a draft was approved")
	}
}

// A version in review is rejected with a reason, by someone who did not
// write it; it never runs or comes back, and the fix is the next version.
func TestARejectionSaysWhyAndIsFinal(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	ann, sam := Who{Name: "Ann"}, Who{Name: "Sam"}
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho one\n"
	if _, err := st.Save(ctx, "s", "", text, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reject(ctx, "s", 1, sam, "too soon"); err == nil {
		t.Fatal("rejected a draft")
	}
	if _, err := st.Submit(ctx, "s", 1, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reject(ctx, "s", 1, ann, "mine"); !errors.Is(err, ErrOwnWork) {
		t.Fatalf("the author rejected it: %v", err)
	}
	if _, err := st.Reject(ctx, "s", 1, sam, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected without a reason: %v", err)
	}
	if _, err := st.Reject(ctx, "s", 1, sam, strings.Repeat("x", MaxComment+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a reason over the limit: %v", err)
	}
	s, err := st.Reject(ctx, "s", 1, sam, " restarts sshd on every node ")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Version(1)
	if v.State != StateRejected || v.Rejected == nil || v.Rejected.By.Name != "Sam" || v.Rejected.Comment != "restarts sshd on every node" {
		t.Fatalf("after the rejection: %+v", v)
	}
	if _, err := st.Approve(ctx, "s", 1, sam); err == nil {
		t.Fatal("a rejected version was approved")
	}
	if _, err := RunVersion(s, true, 1); err == nil {
		t.Fatal("a rejected version ran on production")
	}
	if _, err := RunVersion(s, false, 1); err == nil {
		t.Fatal("a rejected version ran off production")
	}
	if _, err := RunVersion(s, false, 0); err == nil {
		t.Fatal("a rejected latest version ran off production")
	}
	if s, err = st.Save(ctx, "s", "", text+"echo two\n", ann, false); err != nil || s.Latest().N != 2 || s.Latest().State != StateDraft {
		t.Fatalf("the fix: %+v, %v", s.Latest(), err)
	}
	if waiting, _ := st.ToApprove(ctx, sam); len(waiting) != 0 {
		t.Fatalf("a rejected version waits: %+v", waiting)
	}
}

// Rejecting a change says why; approving one needs no reason.
func TestARejectedChangeKeepsItsReason(t *testing.T) {
	ctx := context.Background()
	st := NewStore(&kv.Memory{}, time.Now)
	c, err := st.NewChange(ctx, VariableChange{Cluster: "prod", Fleet: "web", By: Who{Name: "Ann"}, State: ChangeWaiting})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideChange(ctx, "prod", c.ID, false, "", Who{Name: "Sam"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected without a reason: %v", err)
	}
	got, err := st.DecideChange(ctx, "prod", c.ID, false, "wrong mirror", Who{Name: "Sam"})
	if err != nil || got.State != ChangeRejected || got.Comment != "wrong mirror" || got.DecidedBy == nil || got.DecidedBy.Name != "Sam" {
		t.Fatalf("%+v, %v", got, err)
	}
}

// A lower-case parameter name says what to write instead.
func TestAParameterNameInLowerCaseSaysWhatToWrite(t *testing.T) {
	_, fs := Check("#!/bin/sh\n# 221b:describe x\n# 221b:param myname secret who\n# 221b:privilege none\necho \"$MYNAME\"\n")
	found := false
	for _, f := range fs {
		if f.Code == "param-name" {
			found = strings.Contains(f.Msg, "write MYNAME, not myname")
		}
	}
	if !found {
		t.Fatalf("%+v", fs)
	}
	for in, want := range map[string]string{"my-name": "MY_NAME", "9lives": "_9LIVES", "db.pass": "DB_PASS"} {
		if got := suggestName(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
