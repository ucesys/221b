// Package automation runs scripts on a fleet's nodes (Phase 11 part 2,
// docs/automation.md, ADR-73): scripts with a header the console reads,
// static checks, versions approved once, fleet and node parameters, and
// runs over Phase 11's SSH in batches, with a failure limit, reboot and
// wait, and cancel. It knows nothing of SSH itself: the console gives it an
// Executor.
package automation

import (
	"regexp"
	"strings"
)

// Privileges a script may declare (AU-03). Limited sudo needs rules on the
// node and comes later.
const (
	PrivilegeNobody   = "none"
	PrivilegeElevated = "full"
)

// PrivilegeName is how a privilege reads to a person, and in a header:
// nobody, read-only, or elevated, the ssh-paired account's rights. The
// stored values stay none and full, so earlier versions keep their hashes.
func PrivilegeName(p string) string {
	if p == PrivilegeElevated {
		return "elevated"
	}
	return "nobody"
}

// Param is one parameter a script declares.
type Param struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Desc     string `json:"desc,omitempty"`
	// Secret says the value is a reference to a Kubernetes Secret, read
	// when the run starts and kept out of every record.
	Secret bool `json:"secret,omitempty"`
}

// Header is what a script's `# 221b:` lines declare.
type Header struct {
	Describe  string   `json:"describe"`
	Params    []Param  `json:"params"`
	Privilege string   `json:"privilege"`
	DryRun    bool     `json:"dryRun"`
	Facts     []string `json:"facts,omitempty"`
	// Target is "" for nodes, or kubernetes for the cluster.
	Target string `json:"target,omitempty"`
	// K8s are the actions a Kubernetes-target script may take; RunsAs is
	// its default identity, namespace/name of a service account.
	K8s    []KubeAction `json:"k8s,omitempty"`
	RunsAs string       `json:"runsAs,omitempty"`
}

// Param returns a declared parameter by name.
func (h Header) Param(name string) (Param, bool) {
	for _, p := range h.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Finding levels.
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

// Finding is one thing the checks found in a script, on a line (1-based;
// 0 is the script as a whole).
type Finding struct {
	Level string `json:"level"`
	Line  int    `json:"line"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

// HasErrors says whether any finding is an error.
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == LevelError {
			return true
		}
	}
	return false
}

var (
	// ParamNameRE is a parameter's name: an environment variable's.
	ParamNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	headerRE    = regexp.MustCompile(`^#\s*221b:\s*([a-z0-9-]+):?\s*(.*)$`)
	contRE      = regexp.MustCompile(`^#\s{2,}(\S.*)$`)
	runsAsRE    = regexp.MustCompile(`^[a-z0-9][-a-z0-9.]*/[a-z0-9][-a-z0-9.]*$`)
)

// Reserved are the variables the engine sets on every node; a script may
// not declare them as parameters.
var Reserved = map[string]bool{"DRY_RUN": true, "NODE": true, "CLUSTER": true, "FLEET": true, "RUN_ID": true, "PATH": true, "HOME": true, "USER": true, "SHELL": true}

// privilegeHelp says what a privilege means, for the findings about it.
const privilegeHelp = "nobody (read-only: cannot change the node) or elevated (runs with all the rights of the ssh-paired account)"

// ParseHeader reads the `# 221b:` lines of the comment block at the top of
// a script, before its first command (AU-03). `# 221b:privilege nobody` and
// `# 221b: privilege: nobody` are the same line; none and full, the words
// before nobody and elevated, still read.
func ParseHeader(text string) (Header, []Finding) {
	var h Header
	var fs []Finding
	add := func(level string, line int, code, msg string) {
		fs = append(fs, Finding{Level: level, Line: line, Code: code, Msg: msg})
	}
	seenPriv := false
	last := ""
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if n == 1 && strings.HasPrefix(trimmed, "#!") {
			continue
		}
		if trimmed == "" {
			last = ""
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			break
		}
		m := headerRE.FindStringSubmatch(trimmed)
		if m == nil {
			if c := contRE.FindStringSubmatch(line); c != nil && last == "describe" {
				h.Describe += " " + strings.TrimSpace(c[1])
				continue
			}
			last = ""
			continue
		}
		key, rest := m[1], strings.TrimSpace(m[2])
		last = key
		switch key {
		case "describe":
			if h.Describe == "" {
				h.Describe = rest
			} else if rest != "" {
				h.Describe += " " + rest
			}
		case "param":
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				add(LevelError, n, "param", "a param line names the parameter: # 221b:param NAME required|optional description")
				continue
			}
			p := Param{Name: fields[0], Required: true}
			desc := fields[1:]
			if len(desc) > 0 {
				switch desc[0] {
				case "required":
					desc = desc[1:]
				case "optional":
					p.Required = false
					desc = desc[1:]
				case "secret":
					p.Secret = true
					desc = desc[1:]
				}
			}
			p.Desc = strings.Join(desc, " ")
			switch {
			case !ParamNameRE.MatchString(p.Name):
				add(LevelError, n, "param-name", "parameter names are capitals, digits and underscores, as the script reads them from its environment: write "+suggestName(p.Name)+", not "+p.Name)
			case Reserved[p.Name]:
				add(LevelError, n, "param-reserved", p.Name+" is set by the console on every node; pick another name")
			default:
				if _, dup := h.Param(p.Name); dup {
					add(LevelError, n, "param-twice", "parameter "+p.Name+" is declared twice")
					continue
				}
				h.Params = append(h.Params, p)
			}
		case "privilege":
			seenPriv = true
			switch rest {
			case PrivilegeNobody, "nobody":
				h.Privilege = PrivilegeNobody
			case PrivilegeElevated, "elevated":
				h.Privilege = PrivilegeElevated
			case "limited":
				add(LevelError, n, "privilege-limited", "limited sudo is not built yet: declare "+privilegeHelp)
			default:
				add(LevelError, n, "privilege", "privilege is "+privilegeHelp)
			}
		case "dry-run":
			switch rest {
			case "yes":
				h.DryRun = true
			case "no":
			default:
				add(LevelError, n, "dry-run", "dry-run is yes or no")
			}
		case "facts":
			for _, f := range strings.Fields(rest) {
				if !KnownFact(f) {
					add(LevelWarning, n, "fact", "the console collects no fact "+f)
					continue
				}
				h.Facts = append(h.Facts, f)
			}
		case "target":
			switch rest {
			case "nodes":
			case TargetKubernetes:
				h.Target = TargetKubernetes
			default:
				add(LevelError, n, "target", "target is nodes or kubernetes")
			}
		case "k8s":
			fields := strings.Fields(rest)
			switch {
			case len(fields) == 3 && fields[0] == "rollout":
				h.K8s = append(h.K8s, KubeAction{Verb: fields[0] + " " + fields[1], Resource: fields[2]})
			case len(fields) == 2:
				h.K8s = append(h.K8s, KubeAction{Verb: fields[0], Resource: fields[1]})
			default:
				add(LevelError, n, "k8s", "a k8s line is # 221b:k8s verb resource, one action a line")
			}
		case "runs-as":
			if !runsAsRE.MatchString(rest) {
				add(LevelError, n, "runs-as", "runs-as is namespace/name of a service account")
				continue
			}
			h.RunsAs = rest
		default:
			add(LevelWarning, n, "unknown", "# 221b:"+key+" is not a header key the console knows; it is ignored")
		}
	}
	if h.Describe == "" {
		add(LevelError, 0, "describe", "the header has no # 221b:describe line saying what the script does")
	}
	if !seenPriv {
		add(LevelError, 0, "privilege-missing", "the header has no # 221b:privilege line: "+privilegeHelp)
	}
	if h.Params == nil {
		h.Params = []Param{}
	}
	return h, fs
}

// suggestName is the parameter name a person probably meant: capitals,
// with anything else an underscore, not starting with a digit.
func suggestName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "_" + out
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
