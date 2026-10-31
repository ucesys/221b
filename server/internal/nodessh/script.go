package nodessh

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Where pairing puts things on a node.
const (
	nodeDir  = "/etc/ssh/221b"
	nodeConf = "/etc/ssh/sshd_config.d/221b.conf"
)

// Markers the scripts print, for Done to read.
const (
	markHostKey  = "221B-HOSTKEY "
	markPaired   = "221B-PAIRED "
	markUnpaired = "221B-UNPAIRED"
)

// PairScript is the shell script that pairs a node. It refuses, changing
// nothing, when the login user or sshd is not there, or when sshd already
// trusts another authority. It then writes the authority's public key,
// the principals file for that user alone and a drop-in, checks that sshd
// accepts them and really uses them on that port, reloads sshd, and prints
// the node's host keys. Any failure puts the files back as they were.
// It runs as root with the node's root at / (chroot /host, TB-31).
func PairScript(caPublicKey, loginUser string, port int) string {
	return strings.Join([]string{
		"set -e",
		"u=" + shellQuote(loginUser),
		"d=" + nodeDir,
		`if ! id -u "$u" >/dev/null 2>&1; then echo "221B: the login user $u does not exist on this node; create it first"; exit 3; fi`,
		sshdBin,
		`if [ ! -x "$sshd_bin" ]; then echo "221B: this node has no sshd; install the OpenSSH server first"; exit 5; fi`,
		`set -- /etc/ssh/ssh_host_*_key.pub`,
		`if [ ! -r "$1" ]; then echo "221B: this node has no /etc/ssh/ssh_host_*_key.pub to pin"; exit 5; fi`,
		runDir,
		`value() { "$sshd_bin" -T 2>/dev/null | awk -v k="$1" '$1==k{print $2}'; }`,
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
		`  exit "$code"`,
		`}`,
		`trap undo EXIT`,
		`mkdir -p "$d" /etc/ssh/sshd_config.d`,
		`rm -f "$d"/principals-*`,
		`printf '%s\n' ` + shellQuote(caPublicKey) + ` > "$d/ca.pub"`,
		`printf '%s\n' ` + shellQuote(Principal) + ` > "$d/principals-$u"`,
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

// UnpairScript removes what PairScript put on the node and reloads sshd,
// when the node has one: a node whose sshd has gone can still be unpaired.
func UnpairScript() string {
	return strings.Join([]string{
		"set -e",
		`rm -f ` + nodeConf,
		`rm -rf ` + nodeDir,
		sshdBin,
		runDir,
		reloadSSHD,
		`if [ -x "$sshd_bin" ]; then "$sshd_bin" -t; reload_sshd; fi`,
		`echo "` + markUnpaired + `"`,
	}, "\n")
}

const (
	sshdBin = `sshd_bin=$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)`
	// runDir is what sshd -t asks for: under socket activation nothing
	// has made it before the first connection since boot.
	runDir = `mkdir -p /run/sshd`
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
