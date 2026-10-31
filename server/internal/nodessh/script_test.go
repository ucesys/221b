package nodessh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

const testCA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe 221b"

// A pairing puts the Include first, so a Match block at the end of
// sshd_config does not swallow it, and it reports success on a node with
// no sshd running: a socket-activated one starts on the next connection,
// and its first check needs /run/sshd, which only pairing has made.
func TestAPairingOnASocketActivatedNodeWithAMatchBlock(t *testing.T) {
	h := newHost(t)
	out, ok := h.run(PairScript(testCA, user(t), 22))
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
	if out, ok := h.run(UnpairScript()); !ok || !strings.Contains(out, markUnpaired) || h.exists("etc/ssh/221b") {
		t.Fatalf("the unpairing failed:\n%s", out)
	}
}

// A second pairing as another user removes the first user's file, or the
// old account stays admissible to a holder of the authority's key.
func TestAPairingLeavesOnlyItsOwnLoginUser(t *testing.T) {
	h := newHost(t)
	if err := os.MkdirAll(filepath.Join(h.root, "etc/ssh/221b"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write("etc/ssh/221b/principals-olduser", Principal+"\n")
	if out, ok := h.run(PairScript(testCA, user(t), 22)); !ok {
		t.Fatalf("the pairing failed:\n%s", out)
	}
	if h.exists("etc/ssh/221b/principals-olduser") || !h.exists("etc/ssh/221b/principals-"+user(t)) {
		t.Fatal("the principals files are wrong")
	}
}

// An sshd too old for Include refuses the file: everything goes back as
// it was, or its next restart would leave the node with no SSH at all.
func TestAFailedPairingPutsTheNodeBack(t *testing.T) {
	h := newHost(t)
	before := h.read("etc/ssh/sshd_config")
	out, ok := h.run(PairScript(testCA, user(t), 22), "NO_INCLUDE=1")
	if ok || !strings.Contains(out, "is as it was") {
		t.Fatalf("the pairing did not fail cleanly:\n%s", out)
	}
	if h.read("etc/ssh/sshd_config") != before || h.exists("etc/ssh/sshd_config.d/221b.conf") || h.exists("etc/ssh/221b") {
		t.Fatal("the node was left changed")
	}
}

// A pairing that fails on a paired node leaves the earlier pairing whole.
func TestAFailedPairingKeepsTheEarlierOne(t *testing.T) {
	h := newHost(t)
	if out, ok := h.run(PairScript(testCA, user(t), 22)); !ok {
		t.Fatalf("the first pairing failed:\n%s", out)
	}
	conf, ca := h.read("etc/ssh/sshd_config.d/221b.conf"), h.read("etc/ssh/221b/ca.pub")
	if out, ok := h.run(PairScript("ssh-ed25519 AAAAother 221b", user(t), 2222)); ok || !strings.Contains(out, "listens on port 22") {
		t.Fatalf("a pairing on the wrong port:\n%s", out)
	}
	if h.read("etc/ssh/sshd_config.d/221b.conf") != conf || h.read("etc/ssh/221b/ca.pub") != ca {
		t.Fatal("the earlier pairing was lost")
	}
}

// A node that trusts another authority keeps it: sshd takes one value, so
// pairing would cut off the certificate logins it serves.
func TestAPairingLeavesAnotherAuthorityAlone(t *testing.T) {
	h := newHost(t)
	before := h.read("etc/ssh/sshd_config")
	out, ok := h.run(PairScript(testCA, user(t), 22), "OTHER_CA=/etc/ssh/corp_ca.pub")
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
	out, ok := h.run(PairScript(testCA, user(t), 22))
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
	out, ok := h.run(PairScript(testCA, user(t), 22))
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
	if out, ok := h.run(script(PairScript(testCA, user(t), 22))); ok || !strings.Contains(out, "no sshd") {
		t.Fatalf("the pairing went on:\n%s", out)
	}
	if out, ok := h.run(script(UnpairScript())); !ok || !strings.Contains(out, markUnpaired) {
		t.Fatalf("the unpairing failed:\n%s", out)
	}
}
