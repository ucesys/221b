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
	PrivilegeNone = "none"
	PrivilegeFull = "full"
)

// Param is one parameter a script declares.
type Param struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Desc     string `json:"desc,omitempty"`
}

// Header is what a script's `# 221b:` lines declare.
type Header struct {
	Describe  string   `json:"describe"`
	Params    []Param  `json:"params"`
	Privilege string   `json:"privilege"`
	DryRun    bool     `json:"dryRun"`
	Facts     []string `json:"facts,omitempty"`
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
	headerRE    = regexp.MustCompile(`^#\s*221b:([a-z-]+)\s*(.*)$`)
	contRE      = regexp.MustCompile(`^#\s{2,}(\S.*)$`)
)

// Reserved are the variables the engine sets on every node; a script may
// not declare them as parameters.
var Reserved = map[string]bool{"DRY_RUN": true, "NODE": true, "CLUSTER": true, "FLEET": true, "RUN_ID": true, "PATH": true, "HOME": true, "USER": true, "SHELL": true}

// ParseHeader reads the `# 221b:` lines of the comment block at the top of
// a script, before its first command (AU-03).
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
					add(LevelError, n, "param-secret", "secret parameters come in a later step; pass the value another way for now")
					desc = desc[1:]
				}
			}
			p.Desc = strings.Join(desc, " ")
			switch {
			case !ParamNameRE.MatchString(p.Name):
				add(LevelError, n, "param-name", "parameter "+p.Name+" is not an environment variable's name: capitals, digits and underscores")
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
			case PrivilegeNone, PrivilegeFull:
				h.Privilege = rest
			case "limited":
				add(LevelError, n, "privilege-limited", "limited sudo is not built yet: declare none or full")
			default:
				add(LevelError, n, "privilege", "privilege is none or full")
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
			h.Facts = append(h.Facts, strings.Fields(rest)...)
		case "runs-as", "k8s":
			add(LevelWarning, n, "later", "# 221b:"+key+" is for Kubernetes targets, which come in a later step; it is ignored for now")
		default:
			add(LevelWarning, n, "unknown", "# 221b:"+key+" is not a header key the console knows; it is ignored")
		}
	}
	if h.Describe == "" {
		add(LevelError, 0, "describe", "the header has no # 221b:describe line saying what the script does")
	}
	if !seenPriv {
		add(LevelError, 0, "privilege-missing", "the header has no # 221b:privilege line: none or full")
	}
	if h.Params == nil {
		h.Params = []Param{}
	}
	return h, fs
}
