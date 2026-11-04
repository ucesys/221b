package automation

import (
	"regexp"
	"sort"
	"strings"
)

// rule is one pattern the static checks look for in a script's commands.
type rule struct {
	re    *regexp.Regexp
	level string
	code  string
	msg   string
}

// The denylist and the warnings (AU-04). They catch the obvious, not a
// determined author: the limits that hold are runuser and the fleet's
// ceiling.
var rules = []rule{
	{regexp.MustCompile(`\brm\s+(-\S+\s+)*/\*?(\s|;|&|\||$)`), LevelError, "deny-rm-root", "removes the root filesystem"},
	{regexp.MustCompile(`\bmkfs(\.\w+)?\b`), LevelError, "deny-mkfs", "formats a disk; do it by hand, not from a script run on many nodes"},
	{regexp.MustCompile(`\bdd\b[^#\n]*\bof=/dev/`), LevelError, "deny-dd", "writes over a device"},
	{regexp.MustCompile(`>\s*/dev/(sd[a-z]|nvme\d|vd[a-z]|xvd[a-z]|hd[a-z])`), LevelError, "deny-dev", "writes over a disk"},
	{regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), LevelError, "deny-forkbomb", "is a fork bomb"},
	{regexp.MustCompile(`\biptables\s+(-\S+\s+)*(-F|--flush)\b`), LevelError, "deny-iptables", "flushes the firewall"},
	{regexp.MustCompile(`\b(curl|wget)\b[^|#\n]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`), LevelError, "deny-pipe-shell", "pipes a download into a shell; download, check its SHA-256, then run it"},
	{regexp.MustCompile(`\bhelm\s+(install|upgrade)\b|\bkubectl\s+apply\b`), LevelWarning, "apps", "deploys an application; deployments belong in Apps, with its diff and history"},
	{regexp.MustCompile(`^\s*(sudo\s+)?(reboot|shutdown|poweroff|halt)\b|\bsystemctl\s+(reboot|poweroff|halt)\b`), LevelWarning, "reboot", "reboots or stops the node itself; print {\"reboot\": true} as the last line and the console reboots it and waits"},
}

var (
	sudoRE     = regexp.MustCompile(`(^|[;&|(\s` + "`" + `])sudo(\s|$)`)
	downloadRE = regexp.MustCompile(`\bwget\b|\bcurl\b[^#\n]*(\s-[A-Za-z]*[oO]\b|\s--output\b|\s--remote-name\b)`)
	checksumRE = regexp.MustCompile(`\b(sha256sum|sha512sum|shasum)\b[^#\n]*(-c\b|--check\b)`)
)

// Check reads a script's header and checks its commands (AU-03, AU-04).
// A version with an error cannot be submitted.
func Check(text string) (Header, []Finding) {
	h, fs := ParseHeader(text)
	add := func(level string, line int, code, msg string) {
		fs = append(fs, Finding{Level: level, Line: line, Code: code, Msg: msg})
	}
	if h.Target == TargetKubernetes {
		for _, p := range h.Params {
			if p.Secret {
				add(LevelError, 0, "k8s-secret", "a kubectl line cannot carry a secret: "+p.Name+" would be in the audit log")
			}
		}
		return h, append(fs, checkKube(text, h)...)
	}
	lines := strings.Split(text, "\n")
	firstDownload, usesDryRun, checksums := 0, false, false
	for i, raw := range lines {
		n := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "DRY_RUN") {
			usesDryRun = true
		}
		if checksumRE.MatchString(line) {
			checksums = true
		}
		if firstDownload == 0 && downloadRE.MatchString(line) {
			firstDownload = n
		}
		if h.Privilege == PrivilegeNobody && sudoRE.MatchString(line) {
			add(LevelError, n, "sudo-undeclared", "uses sudo, but the header declares privilege nobody: the script runs as nobody and cannot change the node")
		}
		for _, r := range rules {
			if r.re.MatchString(line) {
				add(r.level, n, r.code, "this line "+r.msg)
			}
		}
	}
	if h.DryRun && !usesDryRun {
		add(LevelError, 0, "dry-run-unused", "the header says dry-run yes, but the script never reads DRY_RUN")
	}
	if firstDownload > 0 && !checksums {
		add(LevelError, firstDownload, "download-unchecked", "downloads a file but never checks it: verify a pinned SHA-256 with sha256sum -c before using it")
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Line < fs[j].Line })
	if fs == nil {
		fs = []Finding{}
	}
	return h, fs
}
