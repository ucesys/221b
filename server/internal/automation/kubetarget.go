package automation

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// TargetKubernetes is a script that runs on a cluster instead of on nodes
// (AT30). Its body is kubectl lines, which Watson runs inside the console:
// no shell, each line checked against the actions the header declares, and
// each one audited. The header's list is what an approver approves.
const TargetKubernetes = "kubernetes"

// KubeAction is one action a script declares: `# 221b:k8s patch deployments`.
type KubeAction struct {
	Verb     string `json:"verb"`
	Resource string `json:"resource"`
}

// KubeRunner runs one kubectl line on a cluster; main wires it to Watson's
// kubectl. Identity is namespace/name of a service account, or "" for the
// person; a dry run adds server-side dry run to what changes anything.
type KubeRunner interface {
	RunLine(ctx context.Context, cluster string, person Person, identity, line string, dryRun bool, out io.Writer) (int, error)
}

// Person is who started a run, as Kubernetes knows them; it stays in
// memory for the run.
type Person struct {
	User   string
	Groups []string
}

// KubeLine is a command of a Kubernetes-target script.
type KubeLine struct {
	N    int
	Text string
}

var shellRE = regexp.MustCompile("[|;&<>`\\\\]|\\$\\(")

// KubeLines are the commands of a Kubernetes-target script: every line that
// is not blank, a comment or the shebang.
func KubeLines(text string) []KubeLine {
	var out []KubeLine
	for i, raw := range strings.Split(text, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") || t == "set -eu" || t == "set -e" {
			continue
		}
		out = append(out, KubeLine{N: i + 1, Text: t})
	}
	return out
}

var resourceAliases = map[string]string{
	"po": "pods", "pod": "pods", "deploy": "deployments", "deployment": "deployments", "svc": "services", "service": "services",
	"no": "nodes", "node": "nodes", "ns": "namespaces", "namespace": "namespaces", "sts": "statefulsets", "statefulset": "statefulsets",
	"ds": "daemonsets", "daemonset": "daemonsets", "cm": "configmaps", "configmap": "configmaps", "rs": "replicasets", "replicaset": "replicasets",
	"pvc": "persistentvolumeclaims", "pv": "persistentvolumes", "job": "jobs", "cronjob": "cronjobs", "cj": "cronjobs", "ing": "ingresses",
}

func normalResource(r string) string {
	r = strings.ToLower(r)
	if i := strings.IndexByte(r, '/'); i >= 0 {
		r = r[:i]
	}
	if i := strings.IndexByte(r, '.'); i >= 0 && !strings.HasSuffix(r, ".") {
		// clusters.postgresql.cnpg.io keeps its group.
		return r
	}
	if a, ok := resourceAliases[r]; ok {
		return a
	}
	return r
}

// KubeActionOf reads the verb and resource of a kubectl line.
func KubeActionOf(line string) (KubeAction, error) {
	words := strings.Fields(line)
	if len(words) < 2 || words[0] != "kubectl" {
		return KubeAction{}, fmt.Errorf("a line of a Kubernetes script is one kubectl command")
	}
	var pos []string
	for i := 1; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			if (w == "-n" || w == "--namespace" || w == "-l" || w == "--selector" || w == "-o" || w == "--output" || w == "-p" || w == "--patch" || w == "--type") && i+1 < len(words) {
				i++
			}
			continue
		}
		pos = append(pos, w)
	}
	if len(pos) == 0 {
		return KubeAction{}, fmt.Errorf("the line names no verb")
	}
	verb := pos[0]
	rest := pos[1:]
	switch verb {
	case "cordon", "uncordon", "drain":
		return KubeAction{Verb: verb, Resource: "nodes"}, nil
	case "rollout":
		if len(rest) < 2 {
			return KubeAction{}, fmt.Errorf("the line names no resource")
		}
		return KubeAction{Verb: "rollout " + rest[0], Resource: normalResource(rest[1])}, nil
	case "logs":
		return KubeAction{Verb: verb, Resource: "pods"}, nil
	}
	if len(rest) == 0 {
		return KubeAction{}, fmt.Errorf("the line names no resource")
	}
	return KubeAction{Verb: verb, Resource: normalResource(rest[0])}, nil
}

// Allowed says whether a script's header declares an action.
func (h Header) Allowed(a KubeAction) bool {
	for _, x := range h.K8s {
		if x.Verb == a.Verb && normalResource(x.Resource) == a.Resource {
			return true
		}
	}
	return false
}

// checkKube is the static check of a Kubernetes-target script.
func checkKube(text string, h Header) []Finding {
	var fs []Finding
	lines := KubeLines(text)
	if len(lines) == 0 {
		fs = append(fs, Finding{Level: LevelError, Code: "k8s-empty", Msg: "the script has no kubectl line"})
	}
	seen := map[KubeAction]bool{}
	for _, l := range lines {
		if strings.HasPrefix(l.Text, "#!") {
			continue
		}
		if shellRE.MatchString(l.Text) {
			fs = append(fs, Finding{Level: LevelError, Line: l.N, Code: "k8s-shell", Msg: "a Kubernetes script has no shell: one kubectl command per line, with no pipes, redirects or substitutions"})
			continue
		}
		a, err := KubeActionOf(l.Text)
		if err != nil {
			fs = append(fs, Finding{Level: LevelError, Line: l.N, Code: "k8s-line", Msg: err.Error()})
			continue
		}
		seen[a] = true
		if !h.Allowed(a) {
			fs = append(fs, Finding{Level: LevelError, Line: l.N, Code: "k8s-undeclared", Msg: fmt.Sprintf("this line needs `# 221b:k8s %s %s` in the header: Watson refuses what is not declared", a.Verb, a.Resource)})
		}
	}
	for _, a := range h.K8s {
		if !seen[KubeAction{Verb: a.Verb, Resource: normalResource(a.Resource)}] {
			fs = append(fs, Finding{Level: LevelWarning, Code: "k8s-unused", Msg: fmt.Sprintf("the header declares %s %s, and no line does it: declare only what it needs", a.Verb, a.Resource)})
		}
	}
	return fs
}

var (
	kubeVarRE   = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	kubeValueRE = regexp.MustCompile(`^[A-Za-z0-9._:/=@%+,-]*$`)
)

// ExpandKube puts a run's values into a line. There is no shell to quote
// for, so a value that would split the line is refused.
func ExpandKube(line string, env map[string]string) (string, error) {
	var bad error
	out := kubeVarRE.ReplaceAllStringFunc(line, func(m string) string {
		name := kubeVarRE.FindStringSubmatch(m)[1]
		v, ok := env[name]
		switch {
		case !ok:
			bad = fmt.Errorf("the line reads %s, which has no value", name)
		case !kubeValueRE.MatchString(v) || strings.HasPrefix(v, "-"):
			bad = fmt.Errorf("the value of %s cannot go into a kubectl line", name)
		}
		return v
	})
	return out, bad
}

// Hook runs around each node (AT33).
type Hook struct {
	// Label says what it is, for the record: "cordon and drain", or a
	// script and its version.
	Label string
	Lines []string
	// Header says what it may do.
	Header Header
	// Identity is its service account, or "" for the person.
	Identity string
}

// DrainHook and UncordonHook are the built-in hooks.
func DrainHook() Hook {
	return Hook{Label: "cordon and drain", Lines: []string{"kubectl cordon $NODE", "kubectl drain $NODE --ignore-daemonsets --delete-emptydir-data --timeout=300s"},
		Header: Header{K8s: []KubeAction{{Verb: "cordon", Resource: "nodes"}, {Verb: "drain", Resource: "nodes"}}}}
}

func UncordonHook() Hook {
	return Hook{Label: "uncordon", Lines: []string{"kubectl uncordon $NODE"}, Header: Header{K8s: []KubeAction{{Verb: "uncordon", Resource: "nodes"}}}}
}

// HookOf makes a hook of an approved Kubernetes-target version.
func HookOf(name string, v Version) Hook {
	h := Hook{Label: fmt.Sprintf("%s v%d", name, v.N), Header: v.Header}
	for _, l := range KubeLines(v.Text) {
		if !strings.HasPrefix(l.Text, "#!") {
			h.Lines = append(h.Lines, l.Text)
		}
	}
	return h
}
