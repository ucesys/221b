package nodessh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeHost is a directory that stands for a node's /etc/ssh and /run, and
// an sshd that answers from it, so the scripts run for real.
type fakeHost struct {
	t    *testing.T
	root string
	bin  string
	env  []string
}

const fakeSSHD = `#!/bin/sh
cfg="$ROOT/etc/ssh/sshd_config"
if [ "$1" = -t ]; then
  if [ -n "$NO_INCLUDE" ] && grep -q '^Include' "$cfg"; then echo "Bad configuration option: Include"; exit 255; fi
  [ -d "$ROOT/run/sshd" ] || { echo "Missing privilege separation directory"; exit 255; }
  exit 0
fi
echo "port ${SSHD_PORT:-22}"
if [ -n "$OTHER_CA" ]; then echo "trustedusercakeys $OTHER_CA"; echo "authorizedprincipalsfile none"; exit 0; fi
if [ -f "$ROOT/etc/ssh/sshd_config.d/221b.conf" ] && { [ ! -f "$cfg" ] || head -1 "$cfg" | grep -q '^Include'; }; then
  echo "trustedusercakeys $ROOT/etc/ssh/221b/ca.pub"; echo "authorizedprincipalsfile $ROOT/etc/ssh/221b/principals-%u"
else
  echo "trustedusercakeys none"; echo "authorizedprincipalsfile none"
fi
`

func newHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{t: t, root: t.TempDir(), bin: t.TempDir()}
	for _, d := range []string{"etc/ssh", "run"} {
		if err := os.MkdirAll(filepath.Join(h.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.write("etc/ssh/sshd_config", "PermitRootLogin no\nMatch Group sftponly\n  ForceCommand internal-sftp\n")
	h.write("etc/ssh/ssh_host_ed25519_key.pub", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe root@node\n")
	if err := os.WriteFile(filepath.Join(h.bin, "sshd"), []byte(fakeSSHD), 0o755); err != nil {
		t.Fatal(err)
	}
	h.env = []string{"ROOT=" + h.root, "PATH=" + h.bin + ":/usr/bin:/bin:/usr/sbin:/sbin"}
	return h
}

func (h *fakeHost) write(name, text string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.root, name), []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fakeHost) read(name string) string {
	b, err := os.ReadFile(filepath.Join(h.root, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func (h *fakeHost) exists(name string) bool {
	_, err := os.Stat(filepath.Join(h.root, name))
	return err == nil
}

// run runs a script with the node's paths moved under the fake root.
func (h *fakeHost) run(script string, env ...string) (string, bool) {
	script = strings.ReplaceAll(script, "/etc/ssh", h.root+"/etc/ssh")
	script = strings.ReplaceAll(script, "/run/sshd", h.root+"/run/sshd")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(append([]string{}, h.env...), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func user(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Skip("no id command")
	}
	return strings.TrimSpace(string(out))
}

const (
	testCA      = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe 221b"
	testConsole = "221b.k8s-console-221b"
)

// console is one console that pairs nodes, with an authority of its own.
type console struct {
	t    *testing.T
	name string
	ca   *CA
}

func newConsole(t *testing.T, name string) console {
	t.Helper()
	ca := NewCA(nil)
	if err := ca.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return console{t: t, name: name, ca: ca}
}

func (c console) pair(login string, port int) string {
	return PairScript(c.ca.PublicKey(), c.name, login, port)
}

func (c console) unpair() string { return UnpairScript(c.ca.PublicKey(), c.name) }

// cert is a session's certificate, signed for the principal.
func (c console) cert(principal string) *ssh.Certificate {
	c.t.Helper()
	signer, err := c.ca.Sign("pat via 221B session s1", []string{principal}, time.Minute, time.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	return signer.PublicKey().(*ssh.Certificate)
}

// own is a certificate of the kind this console's new pairings use.
func (c console) own() *ssh.Certificate { return c.cert(PrincipalOf(c.name)) }

// admits decides as sshd does from the files the scripts left: the signer
// is a line of ca.pub, and a principal a line of the login user's file.
func (h *fakeHost) admits(cert *ssh.Certificate, login string) bool {
	h.t.Helper()
	if !h.exists("etc/ssh/sshd_config.d/221b.conf") {
		return false
	}
	trusted := false
	for _, line := range strings.Split(h.read("etc/ssh/221b/ca.pub"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			h.t.Fatalf("ca.pub holds %q: %v", line, err)
		}
		trusted = trusted || bytes.Equal(k.Marshal(), cert.SignatureKey.Marshal())
	}
	if !trusted {
		return false
	}
	for _, line := range strings.Split(h.read("etc/ssh/221b/principals-"+login), "\n") {
		for _, p := range cert.ValidPrincipals {
			if line == p {
				return true
			}
		}
	}
	return false
}

func (h *fakeHost) mustRun(script string) string {
	h.t.Helper()
	out, ok := h.run(script)
	if !ok {
		h.t.Fatalf("the script failed:\n%s", out)
	}
	return out
}

// A pairing puts the Include first, so a Match block at the end of
// sshd_config does not swallow it, and it reports success on a node with
// no sshd running: a socket-activated one starts on the next connection,
// and its first check needs /run/sshd, which only pairing has made.
func TestAPairingOnASocketActivatedNodeWithAMatchBlock(t *testing.T) {
	h := newHost(t)
	out, ok := h.run(PairScript(testCA, testConsole, user(t), 22))
	if !ok || !strings.Contains(out, markPaired) || !strings.Contains(out, markHostKey) {
		t.Fatalf("the pairing failed:\n%s", out)
	}
	if first := strings.SplitN(h.read("etc/ssh/sshd_config"), "\n", 2)[0]; !strings.HasPrefix(first, "Include ") {
		t.Fatalf("sshd_config starts with %q", first)
	}
	if !strings.Contains(h.read("etc/ssh/sshd_config"), "Match Group sftponly") {
		t.Fatal("the node's own settings went")
	}
	// Unpairing there works too, and a second pairing as another user
	// leaves only that user's principals.
	if out, ok := h.run(UnpairScript(testCA, testConsole)); !ok || !strings.Contains(out, markUnpaired) || h.exists("etc/ssh/221b") {
		t.Fatalf("the unpairing failed:\n%s", out)
	}
}

// A second pairing as another user drops the first user's file, or the
// old account stays admissible to a holder of the authority's key.
func TestAPairingLeavesOnlyItsOwnLoginUser(t *testing.T) {
	h := newHost(t)
	a := newConsole(t, "ns.a")
	h.mustRun(a.pair("root", 22))
	h.mustRun(a.pair("nobody", 22))
	if h.exists("etc/ssh/221b/principals-root") || !h.exists("etc/ssh/221b/principals-nobody") {
		t.Fatal("the principals files are wrong")
	}
	if h.admits(a.own(), "root") || !h.admits(a.own(), "nobody") {
		t.Fatal("the old account stayed, or the new one is not admitted")
	}
	if got := strings.Count(h.read("etc/ssh/221b/ca.pub"), "\n"); got != 1 {
		t.Fatalf("ca.pub holds %d lines:\n%s", got, h.read("etc/ssh/221b/ca.pub"))
	}
}

// Two consoles pair one node with different users: each keeps working,
// neither's certificates admit the other's user, and when one unpairs the
// other stays, with the drop-in; the last one takes everything away.
func TestTwoConsolesPairOneNode(t *testing.T) {
	h := newHost(t)
	a, b := newConsole(t, "ns.a"), newConsole(t, "ns.b")
	h.mustRun(a.pair("root", 22))
	h.mustRun(b.pair("nobody", 22))
	check := func(when string, aRoot, bNobody bool) {
		t.Helper()
		if h.admits(a.own(), "root") != aRoot || h.admits(b.own(), "nobody") != bNobody {
			t.Fatalf("%s: a as root %v, b as nobody %v", when, h.admits(a.own(), "root"), h.admits(b.own(), "nobody"))
		}
		if h.admits(a.own(), "nobody") || h.admits(b.own(), "root") {
			t.Fatalf("%s: a console's certificate admits the other's user", when)
		}
	}
	check("both paired", true, true)
	// Pairing again changes nothing for the other.
	h.mustRun(a.pair("root", 22))
	check("paired again", true, true)
	if got := h.read("etc/ssh/221b/principals-root"); got != PrincipalOf("ns.a")+"\n" {
		t.Fatalf("a's file holds %q", got)
	}
	out := h.mustRun(b.unpair())
	if !strings.Contains(out, markUnpaired) || !strings.Contains(out, "another console's key stays") {
		t.Fatalf("the unpairing said:\n%s", out)
	}
	check("b unpaired", true, false)
	if h.exists("etc/ssh/221b/principals-nobody") || strings.Contains(h.read("etc/ssh/221b/ca.pub"), "ns.b") {
		t.Fatal("b left its lines behind")
	}
	h.mustRun(a.unpair())
	check("a unpaired", false, false)
	if h.exists("etc/ssh/221b") || h.exists("etc/ssh/sshd_config.d/221b.conf") {
		t.Fatal("the last console left the directory or the drop-in")
	}
}

// Two consoles may log in as one user: the file holds both, and a console
// that moves to another user or unpairs takes only its own line.
func TestTwoConsolesShareALoginUser(t *testing.T) {
	h := newHost(t)
	a, b := newConsole(t, "ns.a"), newConsole(t, "ns.b")
	h.mustRun(a.pair("root", 22))
	h.mustRun(b.pair("root", 22))
	if !h.admits(a.own(), "root") || !h.admits(b.own(), "root") {
		t.Fatal("one of the two is not admitted")
	}
	h.mustRun(a.pair("nobody", 22))
	if h.admits(a.own(), "root") || !h.admits(a.own(), "nobody") || !h.admits(b.own(), "root") || h.admits(b.own(), "nobody") {
		t.Fatal("moving a to another user touched b, or left a's old account")
	}
	h.mustRun(a.pair("root", 22))
	h.mustRun(a.unpair())
	if !h.admits(b.own(), "root") || h.admits(a.own(), "root") {
		t.Fatal("unpairing a touched b, or left a")
	}
}

// A console that lost its key and pairs again replaces its line: the old
// authority is trusted no longer, the other console's stays.
func TestPairingAgainWithANewKeyReplacesTheConsolesLine(t *testing.T) {
	h := newHost(t)
	old, b := newConsole(t, "ns.a"), newConsole(t, "ns.b")
	h.mustRun(old.pair("root", 22))
	h.mustRun(b.pair("root", 22))
	renewed := newConsole(t, "ns.a")
	h.mustRun(renewed.pair("root", 22))
	if h.admits(old.own(), "root") || !h.admits(renewed.own(), "root") || !h.admits(b.own(), "root") {
		t.Fatal("the lines are wrong:\n" + h.read("etc/ssh/221b/ca.pub"))
	}
	if got := strings.Count(h.read("etc/ssh/221b/principals-root"), "\n"); got != 2 {
		t.Fatalf("the principals file:\n%s", h.read("etc/ssh/221b/principals-root"))
	}
}

// A console that lost its key can still unpair: its lines go by its name.
func TestUnpairingWithNoKeyGoesByTheName(t *testing.T) {
	h := newHost(t)
	a, b := newConsole(t, "ns.a"), newConsole(t, "ns.b")
	h.mustRun(a.pair("root", 22))
	h.mustRun(b.pair("root", 22))
	h.mustRun(UnpairScript("", a.name))
	if h.admits(a.own(), "root") || !h.admits(b.own(), "root") || strings.Count(h.read("etc/ssh/221b/ca.pub"), "\n") != 1 {
		t.Fatal("the lines are wrong:\n" + h.read("etc/ssh/221b/ca.pub"))
	}
}

// Lines go by their whole name, never by a part of it or a pattern.
func TestNamesThatLookAlikeAreNotTouched(t *testing.T) {
	h := newHost(t)
	a, b, c := newConsole(t, "ns.a"), newConsole(t, "ns.a-2"), newConsole(t, "nsxa")
	for _, x := range []console{a, b, c} {
		h.mustRun(x.pair("root", 22))
	}
	h.mustRun(a.unpair())
	if h.admits(a.own(), "root") || !h.admits(b.own(), "root") || !h.admits(c.own(), "root") {
		t.Fatal("a look-alike went:\n" + h.read("etc/ssh/221b/principals-root"))
	}
}

// A node paired by an older build has one untagged line and the shared
// principal. Another console pairing beside it leaves both, so that
// pairing keeps working; its own console pairing again takes both away.
func TestAnOlderPairingStaysUntilItsOwnConsolePairsAgain(t *testing.T) {
	h := newHost(t)
	a, b := newConsole(t, "ns.a"), newConsole(t, "ns.b")
	h.mustRun(a.pair("root", 22))
	h.write("etc/ssh/221b/ca.pub", a.ca.PublicKey()+"\n")
	h.write("etc/ssh/221b/principals-root", oldPrincipal+"\n")
	h.mustRun(b.pair("root", 22))
	h.mustRun(b.pair("nobody", 22))
	if !h.admits(a.cert(oldPrincipal), "root") || !h.admits(b.own(), "nobody") {
		t.Fatal("the older pairing went:\n" + h.read("etc/ssh/221b/ca.pub"))
	}
	h.mustRun(a.pair("root", 22))
	if h.admits(a.cert(oldPrincipal), "root") || !h.admits(a.own(), "root") || !h.admits(b.own(), "nobody") {
		t.Fatal("pairing again:\n" + h.read("etc/ssh/221b/ca.pub"))
	}
	if strings.Contains(h.read("etc/ssh/221b/ca.pub"), a.ca.PublicKey()) {
		t.Fatal("the untagged line stayed")
	}
}

// Unpairing a console from its older pairing takes its untagged line and
// the shared principal; an untagged line of another key stays.
func TestUnpairingAnOlderPairing(t *testing.T) {
	h := newHost(t)
	a, other := newConsole(t, "ns.a"), newConsole(t, "ns.other")
	h.mustRun(a.pair("root", 22))
	h.write("etc/ssh/221b/ca.pub", other.ca.PublicKey()+"\n")
	h.write("etc/ssh/221b/principals-root", oldPrincipal+"\n")
	h.mustRun(a.unpair())
	if !h.admits(other.cert(oldPrincipal), "root") {
		t.Fatal("another console's older pairing went")
	}
	h.write("etc/ssh/221b/ca.pub", a.ca.PublicKey()+"\n")
	h.mustRun(a.unpair())
	if h.exists("etc/ssh/221b") || h.exists("etc/ssh/sshd_config.d/221b.conf") {
		t.Fatal("the older pairing stayed")
	}
}

// A second console's pairing that fails leaves the first one whole, and
// the failed console's lines are not on the node.
func TestAFailedPairingOfAnotherConsoleLeavesTheFirst(t *testing.T) {
	h := newHost(t)
	a, b := newConsole(t, "ns.a"), newConsole(t, "ns.b")
	h.mustRun(a.pair("root", 22))
	keys, principals, conf := h.read("etc/ssh/221b/ca.pub"), h.read("etc/ssh/221b/principals-root"), h.read("etc/ssh/sshd_config.d/221b.conf")
	if out, ok := h.run(b.pair("root", 2222)); ok || !strings.Contains(out, "is as it was") {
		t.Fatalf("a pairing on the wrong port:\n%s", out)
	}
	if h.read("etc/ssh/221b/ca.pub") != keys || h.read("etc/ssh/221b/principals-root") != principals || h.read("etc/ssh/sshd_config.d/221b.conf") != conf {
		t.Fatal("the first pairing changed")
	}
	if !h.admits(a.own(), "root") || h.admits(b.own(), "root") || h.exists("etc/ssh/221b.lock") {
		t.Fatal("the rollback is not whole")
	}
	if entries, _ := os.ReadDir(filepath.Join(h.root, "etc/ssh/221b")); len(entries) != 2 {
		t.Fatalf("the directory holds %v", entries)
	}
}

// Consoles that pair one node at once wait for each other: every line
// lands, or a console would look paired and be cut off.
func TestConsolesPairingAtOnceAllLand(t *testing.T) {
	h := newHost(t)
	var wg sync.WaitGroup
	cs := make([]console, 6)
	outs := make([]string, len(cs))
	oks := make([]bool, len(cs))
	for i := range cs {
		cs[i] = newConsole(t, fmt.Sprintf("ns.c%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i], oks[i] = h.run(cs[i].pair("root", 22))
		}()
	}
	wg.Wait()
	for i, c := range cs {
		if !oks[i] {
			t.Fatalf("console %d failed:\n%s", i, outs[i])
		}
		if !h.admits(c.own(), "root") {
			t.Fatalf("console %d lost its line:\n%s", i, h.read("etc/ssh/221b/ca.pub"))
		}
	}
	if h.exists("etc/ssh/221b.lock") {
		t.Fatal("the lock stayed")
	}
}

// A live lock turns a pairing away, changing nothing; one left by a killed
// script is broken once it is old.
func TestALockIsWaitedForAndAnOldOneBroken(t *testing.T) {
	h := newHost(t)
	if err := os.WriteFile(filepath.Join(h.bin, "sleep"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newConsole(t, "ns.a")
	lock := filepath.Join(h.root, "etc/ssh/221b.lock")
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, ok := h.run(a.pair("root", 22)); ok || !strings.Contains(out, "another pairing is running") {
		t.Fatalf("the pairing went on:\n%s", out)
	}
	if h.exists("etc/ssh/221b") {
		t.Fatal("the node was changed")
	}
	if !h.exists("etc/ssh/221b.lock") {
		t.Fatal("the other's lock was released")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	h.mustRun(a.pair("root", 22))
	if h.exists("etc/ssh/221b.lock") {
		t.Fatal("the lock stayed")
	}
}

// An sshd too old for Include refuses the file: everything goes back as
// it was, or its next restart would leave the node with no SSH at all.
func TestAFailedPairingPutsTheNodeBack(t *testing.T) {
	h := newHost(t)
	before := h.read("etc/ssh/sshd_config")
	out, ok := h.run(PairScript(testCA, testConsole, user(t), 22), "NO_INCLUDE=1")
	if ok || !strings.Contains(out, "is as it was") {
		t.Fatalf("the pairing did not fail cleanly:\n%s", out)
	}
	if h.read("etc/ssh/sshd_config") != before || h.exists("etc/ssh/sshd_config.d/221b.conf") || h.exists("etc/ssh/221b") || h.exists("etc/ssh/221b.lock") {
		t.Fatal("the node was left changed")
	}
}

// A pairing that fails on a paired node leaves the earlier pairing whole.
func TestAFailedPairingKeepsTheEarlierOne(t *testing.T) {
	h := newHost(t)
	if out, ok := h.run(PairScript(testCA, testConsole, user(t), 22)); !ok {
		t.Fatalf("the first pairing failed:\n%s", out)
	}
	conf, ca := h.read("etc/ssh/sshd_config.d/221b.conf"), h.read("etc/ssh/221b/ca.pub")
	if out, ok := h.run(PairScript("ssh-ed25519 AAAAother 221b", testConsole, user(t), 2222)); ok || !strings.Contains(out, "listens on port 22") {
		t.Fatalf("a pairing on the wrong port:\n%s", out)
	}
	if h.read("etc/ssh/sshd_config.d/221b.conf") != conf || h.read("etc/ssh/221b/ca.pub") != ca {
		t.Fatal("the earlier pairing was lost")
	}
}

// A node that trusts an authority outside our file keeps it: sshd takes
// one value, so pairing would cut off the certificate logins it serves.
func TestAPairingLeavesAnotherAuthorityAlone(t *testing.T) {
	h := newHost(t)
	before := h.read("etc/ssh/sshd_config")
	out, ok := h.run(PairScript(testCA, testConsole, user(t), 22), "OTHER_CA=/etc/ssh/corp_ca.pub")
	if ok || !strings.Contains(out, "already trusts another SSH authority") {
		t.Fatalf("the pairing went on:\n%s", out)
	}
	if h.read("etc/ssh/sshd_config") != before || h.exists("etc/ssh/221b") {
		t.Fatal("the node was left changed")
	}
}

// sshd accepts a config whose drop-in it does not use (a setting that
// comes first): the pairing says so rather than recording a pairing no
// session can use.
func TestAPairingChecksThatSshdUsesIt(t *testing.T) {
	h := newHost(t)
	h.write("etc/ssh/sshd_config", "PermitRootLogin no\nInclude "+h.root+"/etc/ssh/sshd_config.d/*.conf\n")
	out, ok := h.run(PairScript(testCA, testConsole, user(t), 22))
	if ok || !strings.Contains(out, "does not use the drop-in") || h.exists("etc/ssh/221b") {
		t.Fatalf("the pairing went on:\n%s", out)
	}
}

// With the vendor file under /usr/etc no /etc/ssh/sshd_config exists, and
// writing one would replace the vendor's settings.
func TestAPairingWritesNoConfigWhereThereIsNone(t *testing.T) {
	h := newHost(t)
	if err := os.Remove(filepath.Join(h.root, "etc/ssh/sshd_config")); err != nil {
		t.Fatal(err)
	}
	out, ok := h.run(PairScript(testCA, testConsole, user(t), 22))
	if !ok || h.exists("etc/ssh/sshd_config") {
		t.Fatalf("the pairing:\n%s", out)
	}
}

// A node with no sshd cannot be paired, and can still be unpaired.
func TestNoSshdOnTheNode(t *testing.T) {
	h := newHost(t)
	if err := os.Remove(filepath.Join(h.bin, "sshd")); err != nil {
		t.Fatal(err)
	}
	script := func(s string) string { return strings.ReplaceAll(s, "/usr/sbin/sshd", h.bin+"/sshd") }
	// No sbin on the path either: the machine running the test may have an
	// sshd of its own, which this node has not.
	noSbin := "PATH=" + h.bin + ":/usr/bin:/bin"
	if out, ok := h.run(script(PairScript(testCA, testConsole, user(t), 22)), noSbin); ok || !strings.Contains(out, "no sshd") {
		t.Fatalf("the pairing went on:\n%s", out)
	}
	if out, ok := h.run(script(UnpairScript(testCA, testConsole)), noSbin); !ok || !strings.Contains(out, markUnpaired) {
		t.Fatalf("the unpairing failed:\n%s", out)
	}
}
