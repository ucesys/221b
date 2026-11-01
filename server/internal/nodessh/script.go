package nodessh

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/sessions"
)

// Where pairing puts things on a node.
const (
	nodeDir  = "/etc/ssh/221b"
	nodeConf = "/etc/ssh/sshd_config.d/221b.conf"
	nodeLock = "/etc/ssh/221b.lock"
)

// consoleTag starts the comment that names a console's line in ca.pub.
const consoleTag = "221b-console="

// Markers the scripts print, for Done to read.
const (
	markHostKey  = sessions.HostKeyMark
	markPaired   = "221B-PAIRED "
	markUnpaired = "221B-UNPAIRED"
)

// PairScript is the shell script that pairs a node. It refuses, changing
// nothing, when the login user or sshd is not there, or when sshd already
// trusts an authority outside our file. It then replaces this console's
// lines: its key in ca.pub, and its principal in the principals file of
// that user alone; other consoles' lines stay. It writes the drop-in,
// checks that sshd accepts all that and really uses it on that port,
// reloads sshd, and prints the node's host keys. Any failure puts the
// files back as they were.
// It runs as root with the node's root at / (chroot /host, TB-31).
func PairScript(caPublicKey, console, loginUser string, port int) string {
	return strings.Join([]string{
		"set -e",
		"u=" + shellQuote(loginUser),
		consoleVars(caPublicKey, console),
		`if ! id -u "$u" >/dev/null 2>&1; then echo "221B: the login user $u does not exist on this node; create it first"; exit 3; fi`,
		sshdBin,
		`if [ ! -x "$sshd_bin" ]; then echo "221B: this node has no sshd; install the OpenSSH server first"; exit 5; fi`,
		`set -- /etc/ssh/ssh_host_*_key.pub`,
		`if [ ! -r "$1" ]; then echo "221B: this node has no /etc/ssh/ssh_host_*_key.pub to pin"; exit 5; fi`,
		runDir,
		`value() { "$sshd_bin" -T 2>/dev/null | awk -v k="$1" '$1==k{print $2}'; }`,
		lockNode,
		`cur=$(value trustedusercakeys)`,
		`case "$cur" in ""|none|"$d/ca.pub") ;; *) echo "221B: sshd on this node already trusts another SSH authority ($cur); pairing would replace it, so nothing was changed"; exit 5;; esac`,
		`b=$(mktemp -d)`,
		`cp -p /etc/ssh/sshd_config "$b/sshd_config" 2>/dev/null || true`,
		`cp -p ` + nodeConf + ` "$b/221b.conf" 2>/dev/null || true`,
		`cp -Rp "$d" "$b/dir" 2>/dev/null || true`,
		`undo() {`,
		`  code=$?`,
		`  trap - EXIT`,
		`  set +e`,
		`  if [ "$code" -ne 0 ]; then`,
		`    if [ -f "$b/sshd_config" ]; then cp -p "$b/sshd_config" /etc/ssh/sshd_config; fi`,
		`    rm -f ` + nodeConf + `; if [ -f "$b/221b.conf" ]; then cp -p "$b/221b.conf" ` + nodeConf + `; fi`,
		`    rm -rf "$d"; if [ -d "$b/dir" ]; then cp -Rp "$b/dir" "$d"; fi`,
		`    echo "221B: the pairing did not finish; the node's SSH configuration is as it was"`,
		`  fi`,
		`  rm -rf "$b"`,
		`  rmdir "$lock"`,
		`  exit "$code"`,
		`}`,
		`trap undo EXIT`,
		`mkdir -p "$d" /etc/ssh/sshd_config.d`,
		dropConsole,
		`drop_console`,
		`printf '%s\n' "$k $t" >> "$d/ca.pub"`,
		`printf '%s\n' "$p" >> "$d/principals-$u"`,
		`chmod 644 "$d/ca.pub" "$d/principals-$u"`,
		`printf '%s\n' '# Written by 221B: SSH sessions from the console (ADR-72). Unpair from the console to remove.' 'TrustedUserCAKeys ` + nodeDir + `/ca.pub' 'AuthorizedPrincipalsFile ` + nodeDir + `/principals-%u' > ` + nodeConf,
		`chmod 644 ` + nodeConf,
		// At the top: sshd takes a setting's first value, and an Include
		// at the end could land inside a Match block. A system with no
		// /etc/ssh/sshd_config keeps a vendor file that reads the
		// drop-ins by itself, and an /etc file would replace it.
		`if [ -f /etc/ssh/sshd_config ] && ! grep -qs '^Include /etc/ssh/sshd_config.d/' /etc/ssh/sshd_config; then { printf 'Include /etc/ssh/sshd_config.d/*.conf\n'; cat "$b/sshd_config"; } > /etc/ssh/sshd_config; fi`,
		`"$sshd_bin" -t`,
		`if [ "$(value trustedusercakeys)" != "$d/ca.pub" ] || [ "$(value authorizedprincipalsfile)" != "$d/principals-%u" ]; then echo "221B: sshd on this node does not use the drop-in: another setting comes first, or the Include sits under a Match block"; exit 6; fi`,
		fmt.Sprintf(`if ! value port | grep -qx %d; then echo "221B: sshd on this node listens on port $(value port | tr '\n' ' ')and not on %d"; exit 6; fi`, port, port),
		reloadSSHD,
		`reload_sshd`,
		`for f in /etc/ssh/ssh_host_*_key.pub; do [ -r "$f" ] && echo "` + markHostKey + `$(cut -d' ' -f1,2 "$f")"; done`,
		`echo "` + markPaired + `$u"`,
	}, "\n")
}

// UnpairScript takes this console's lines off the node and reloads sshd,
// when the node has one: a node whose sshd has gone can still be unpaired.
// The drop-in and the directory go with the last console's key. caPublicKey
// is empty when the console has no key any more: its lines then go by name.
func UnpairScript(caPublicKey, console string) string {
	return strings.Join([]string{
		"set -e",
		consoleVars(caPublicKey, console),
		lockNode,
		dropConsole,
		`drop_console`,
		`if grep -qs '^[^#[:space:]]' "$d/ca.pub"; then echo "221B: another console's key stays on this node, with the drop-in"; else rm -f ` + nodeConf + `; rm -rf "$d"; fi`,
		sshdBin,
		runDir,
		reloadSSHD,
		`if [ -x "$sshd_bin" ]; then "$sshd_bin" -t; reload_sshd; fi`,
		`echo "` + markUnpaired + `"`,
	}, "\n")
}

// consoleVars sets what the scripts below go by: the directory, the
// console's key (type and base64), its tag and its principal.
func consoleVars(caPublicKey, console string) string {
	key := ""
	if f := strings.Fields(caPublicKey); len(f) >= 2 {
		key = f[0] + " " + f[1]
	}
	return strings.Join([]string{
		"d=" + nodeDir,
		"k=" + shellQuote(key),
		"t=" + shellQuote(consoleTag+console),
		"p=" + shellQuote(PrincipalOf(console)),
	}, "\n")
}

const (
	sshdBin = `sshd_bin=$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)`
	// runDir is what sshd -t asks for: under socket activation nothing
	// has made it before the first connection since boot.
	runDir = `mkdir -p /run/sshd`
	// lockNode lets one script at a time near the files the consoles
	// share: each rewrites them, and a rollback puts back what was there
	// when it began, so two at once could lose the other's line. The lock
	// is released on exit; one left by a killed script is broken once old.
	lockNode = `lock=` + nodeLock + `
tries=0
until mkdir "$lock" 2>/dev/null; do
  if [ -n "$(find "$lock" -maxdepth 0 -mmin +5 2>/dev/null)" ]; then rmdir "$lock" 2>/dev/null || true; fi
  tries=$((tries+1))
  if [ "$tries" -ge 60 ]; then echo "221B: no lock at $lock: another pairing is running on this node, or /etc/ssh cannot be written; nothing was changed"; exit 5; fi
  sleep 1
done
trap 'rmdir "$lock"' EXIT`
	// dropConsole takes this console's lines out, each file swapped whole
	// so sshd never reads a part of it while another console logs in: the
	// key line by its tag, or by the key for a line from before tags; the
	// principal from every principals file, and a file left empty with
	// it. An untagged line of this very key is this console's own older
	// pairing, and the old principal goes with it; an untagged line of
	// another key stays, with its principal.
	dropConsole = `put() { chmod 644 "$d/.new"; mv "$d/.new" "$1"; }
drop_principal() {
  for f in "$d"/principals-*; do
    [ -f "$f" ] || continue
    grep -vxF "$1" "$f" > "$d/.new" || [ $? -eq 1 ]
    if [ -s "$d/.new" ]; then put "$f"; else rm -f "$f" "$d/.new"; fi
  done
}
drop_console() {
  if [ -f "$d/ca.pub" ]; then
    if [ -n "$k" ] && grep -qxF "$k ` + caComment + `" "$d/ca.pub"; then drop_principal ` + oldPrincipal + `; fi
    awk -v t="$t" -v k="$k" '$3 != t && ($1 " " $2) != k' "$d/ca.pub" > "$d/.new"
    put "$d/ca.pub"
  fi
  drop_principal "$p"
}`
	// reloadSSHD reloads sshd: through systemd where it runs (the host's,
	// reached through the host's /run), else by a HUP to sshd's pid. With
	// none running there is nothing to reload: the next one to start,
	// socket-activated ones included, reads the new configuration.
	reloadSSHD = `reload_sshd() {
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active sshd >/dev/null 2>&1; then systemctl reload sshd
  elif command -v systemctl >/dev/null 2>&1 && systemctl is-active ssh >/dev/null 2>&1; then systemctl reload ssh
  elif [ -r /run/sshd.pid ]; then kill -HUP "$(cat /run/sshd.pid)"
  else echo "221B: no sshd is running now; the next one to start reads this configuration"
  fi
}`
)

// shellQuote quotes a value for sh, in single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ParsePairOutput reads what PairScript printed: the node's host keys
// (every type sshd may present) and the login user it paired.
func ParsePairOutput(out string) (hostKeys []string, loginUser string, err error) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, markHostKey):
			k := strings.TrimSpace(strings.TrimPrefix(line, markHostKey))
			if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err != nil {
				return nil, "", fmt.Errorf("a host key of the node is not readable (%q): %w", k, err)
			}
			hostKeys = append(hostKeys, k)
		case strings.HasPrefix(line, markPaired):
			loginUser = strings.TrimSpace(strings.TrimPrefix(line, markPaired))
		}
	}
	if loginUser == "" {
		return nil, "", errors.New("the pairing did not finish on the node (no 221B-PAIRED line in its output)")
	}
	if len(hostKeys) == 0 {
		return nil, "", errors.New("the node printed no host key (no /etc/ssh/ssh_host_*_key.pub)")
	}
	return hostKeys, loginUser, nil
}
