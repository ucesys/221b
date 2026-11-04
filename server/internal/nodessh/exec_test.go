package nodessh

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// Exec runs one command with no terminal (Automation, AU-08): its stdin
// reaches the node, its output and exit status come back, asNobody wraps
// it to drop to nobody, and an unpaired node is unreachable.
func TestExecRunsACommandAndReturnsItsStatus(t *testing.T) {
	ctx := context.Background()
	ca := NewCA(nil)
	if err := ca.Load(ctx); err != nil {
		t.Fatal(err)
	}
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(ca.PublicKey()))
	node := startNode(t, caPub, "ops")
	svc := New(Options{CA: ca, Pairings: NewPairings(&kv.Memory{}), NodeAddr: func(cluster, n string) (string, error) { return "127.0.0.1", nil }, CertTTL: time.Minute})
	var out safeBuffer
	if _, err := svc.Exec(ctx, "local", "worker-1", "pat via 221B automation run 1", "true", false, strings.NewReader(""), &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("an unpaired node: %v", err)
	}
	req := sessions.Request{Tool: toolbox.ToolSSHPair, Node: "worker-1", LoginUser: "ops", Port: node.ln.Addr().(*net.TCPAddr).Port}
	if err := svc.Done(ctx, "local", req, "221B-HOSTKEY "+node.hostKeyLine()+"\n221B-PAIRED ops\n", "Pat"); err != nil {
		t.Fatal(err)
	}
	code, err := svc.Exec(ctx, "local", "worker-1", "pat via 221B automation run 1", "env A='1' sh -s", false, strings.NewReader("echo script\n"), &out)
	if err != nil || code != 0 {
		t.Fatalf("exec: %d %v", code, err)
	}
	if got := out.String(); !strings.Contains(got, "ran: env A='1' sh -s") || !strings.Contains(got, "stdin: echo script") {
		t.Fatalf("output %q", got)
	}
	var out2 safeBuffer
	code, err = svc.Exec(ctx, "local", "worker-1", "pat via 221B automation run 2", "fail now", true, strings.NewReader(""), &out2)
	if err != nil || code != 7 {
		t.Fatalf("a failing command: %d %v", code, err)
	}
	node.mu.Lock()
	last := node.commands[len(node.commands)-1]
	node.mu.Unlock()
	if last != "cd / && exec sudo -n -u nobody -- sh -c 'fail now'" {
		t.Fatalf("as nobody: %q", last)
	}
	if got, want := NobodyCommand("root", "echo 'x'"), `cd / && exec runuser -u nobody -- sh -c 'echo '\''x'\'''`; got != want {
		t.Fatalf("as root: %s, want %s", got, want)
	}
}
