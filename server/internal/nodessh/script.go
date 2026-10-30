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

// PairScript is the shell script that pairs a node: it refuses when the
// login user is not there, writes the authority's public key and the
// principals file for that user, points sshd at them in a drop-in,
// checks the configuration, reloads sshd, and prints the node's host key.
// It runs as root with the node's root at / (chroot /host, TB-31).
func PairScript(caPublicKey, loginUser string) string {
	return strings.Join([]string{
		"set -e",
		"u=" + shellQuote(loginUser),
		`if ! id -u "$u" >/dev/null 2>&1; then echo "221B: the login user $u does not exist on this node; create it first"; exit 3; fi`,
		"d=" + nodeDir,
		`mkdir -p "$d" /etc/ssh/sshd_config.d`,
		`printf '%s\n' ` + shellQuote(caPublicKey) + ` > "$d/ca.pub"`,
		`printf '%s\n' ` + shellQuote(Principal) + ` > "$d/principals-$u"`,
		`chmod 644 "$d/ca.pub" "$d/principals-$u"`,
		`printf '%s\n' '# Written by 221B: SSH sessions from the console (ADR-72). Unpair from the console to remove.' 'TrustedUserCAKeys ` + nodeDir + `/ca.pub' 'AuthorizedPrincipalsFile ` + nodeDir + `/principals-%u' > ` + nodeConf,
		`chmod 644 ` + nodeConf,
		`grep -qs '^Include /etc/ssh/sshd_config.d/' /etc/ssh/sshd_config || printf '\nInclude /etc/ssh/sshd_config.d/*.conf\n' >> /etc/ssh/sshd_config`,
		`sshd_bin=$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)`,
		`"$sshd_bin" -t`,
		reloadLine,
		`for t in ed25519 ecdsa rsa; do f="/etc/ssh/ssh_host_${t}_key.pub"; if [ -r "$f" ]; then echo "` + markHostKey + `$(cut -d' ' -f1,2 "$f")"; break; fi; done`,
		`echo "` + markPaired + `$u"`,
	}, "\n")
}

// UnpairScript removes what PairScript put on the node and reloads sshd.
func UnpairScript() string {
	return strings.Join([]string{
		"set -e",
		`rm -f ` + nodeConf,
		`rm -rf ` + nodeDir,
		`sshd_bin=$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)`,
		`"$sshd_bin" -t`,
		reloadLine,
		`echo "` + markUnpaired + `"`,
	}, "\n")
}

// reloadLine reloads sshd: through systemd where it runs (the host's,
// reached through the host's /run), else by a HUP to sshd's pid.
const reloadLine = `if command -v systemctl >/dev/null 2>&1 && systemctl is-active sshd >/dev/null 2>&1; then systemctl reload sshd; elif command -v systemctl >/dev/null 2>&1 && systemctl is-active ssh >/dev/null 2>&1; then systemctl reload ssh; elif [ -r /run/sshd.pid ]; then kill -HUP "$(cat /run/sshd.pid)"; else echo "221B: sshd is not running under systemd and has no pid file; it was not reloaded"; exit 4; fi`

// shellQuote quotes a value for sh, in single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ParsePairOutput reads what PairScript printed: the host key and the
// login user it paired.
func ParsePairOutput(out string) (hostKey, loginUser string, err error) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, markHostKey):
			hostKey = strings.TrimSpace(strings.TrimPrefix(line, markHostKey))
		case strings.HasPrefix(line, markPaired):
			loginUser = strings.TrimSpace(strings.TrimPrefix(line, markPaired))
		}
	}
	if loginUser == "" {
		return "", "", errors.New("the pairing did not finish on the node (no 221B-PAIRED line in its output)")
	}
	if hostKey == "" {
		return "", "", errors.New("the node printed no host key (no /etc/ssh/ssh_host_*_key.pub)")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey)); err != nil {
		return "", "", fmt.Errorf("the node's host key is not readable: %w", err)
	}
	return hostKey, loginUser, nil
}
