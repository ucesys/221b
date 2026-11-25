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
// memory for the run, and a run that waits for approval keeps it (its
// JSON names it "user" and "groups"; the field names of before, "User"
// and "Groups", read the same, as Go reads a name in any case).
type Person struct {
	User   string   `json:"user"`
	Groups []string `json:"groups,omitempty"`
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

// kubeValueFlags are kubectl's flags that take a value, which may come
// as the next word (-n web); kubeBoolFlags those that stand alone. A flag
// of neither is refused unless its value is joined to it (--flag=value):
// otherwise a word after it could be its value or a resource, and the
// line could act on a resource its header never declared.
var (
	kubeValueFlags = map[string]bool{"n": true, "namespace": true, "l": true, "selector": true, "o": true, "output": true, "p": true, "patch": true,
		"type": true, "L": true, "label-columns": true, "f": true, "filename": true, "k": true, "kustomize": true, "c": true, "container": true,
		"field-selector": true, "sort-by": true, "template": true, "timeout": true, "grace-period": true, "replicas": true, "since": true,
		"since-time": true, "tail": true, "limit-bytes": true, "chunk-size": true, "field-manager": true, "context": true, "kubeconfig": true,
		"cluster": true, "user": true, "s": true, "server": true, "token": true, "as": true, "as-group": true, "as-uid": true,
		"request-timeout": true, "pod-selector": true, "to-revision": true, "revision": true, "subresource": true, "current-replicas": true,
		"resource-version": true, "for": true, "image": true, "max-log-requests": true, "pod-running-timeout": true, "skip-wait-for-delete-timeout": true,
		"min": true, "max": true, "cpu-percent": true}
	kubeBoolFlags = map[string]bool{"A": true, "all-namespaces": true, "all": true, "overwrite": true, "force": true, "w": true, "watch": true,
		"watch-only": true, "ignore-daemonsets": true, "delete-emptydir-data": true, "delete-local-data": true, "disable-eviction": true,
		"ignore-not-found": true, "show-labels": true, "no-headers": true, "R": true, "recursive": true, "record": true, "now": true,
		"local": true, "wait": true, "show-kind": true, "previous": true, "timestamps": true, "prefix": true, "server-side": true,
		"show-managed-fields": true, "list": true, "follow": true, "insecure-skip-tls-verify": true,
		// These two take a value only when joined: --dry-run=server.
		"dry-run": true, "cascade": true}
)

// KubeActionOf reads the verb and the first resource of a kubectl line.
func KubeActionOf(line string) (KubeAction, error) {
	all, err := KubeActionsOf(line)
	if err != nil {
		return KubeAction{}, err
	}
	return all[0], nil
}

// KubeActionsOf reads the verb of a kubectl line and every resource it
// acts on: each type of TYPE[,TYPE...], and each TYPE/NAME it names, so a
// line is allowed only when its header declares the verb on all of them.
// A flag's value is skipped, whether joined to it or the next word; a
// flag the console does not know must have its value joined to it.
func KubeActionsOf(line string) ([]KubeAction, error) {
	words := strings.Fields(line)
	if len(words) < 2 || words[0] != "kubectl" {
		return nil, fmt.Errorf("a line of a Kubernetes script is one kubectl command")
	}
	var pos []string
	for i := 1; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			pos = append(pos, w)
			continue
		}
		if w == "--" {
			pos = append(pos, words[i+1:]...)
			break
		}
		name := strings.TrimLeft(w, "-")
		if strings.Contains(name, "=") {
			continue
		}
		switch {
		case kubeValueFlags[name]:
			i++
		case kubeBoolFlags[name]:
		default:
			return nil, fmt.Errorf("the console does not know whether %s takes a value: write it as %s=VALUE", w, w)
		}
	}
	if len(pos) == 0 {
		return nil, fmt.Errorf("the line names no verb")
	}
	verb := pos[0]
	rest := pos[1:]
	switch verb {
	case "cordon", "uncordon", "drain":
		return []KubeAction{{Verb: verb, Resource: "nodes"}}, nil
	case "rollout":
		if len(rest) < 2 {
			return nil, fmt.Errorf("the line names no resource")
		}
		verb, rest = "rollout "+rest[0], rest[1:]
	case "logs":
		return []KubeAction{{Verb: verb, Resource: "pods"}}, nil
	}
	if len(rest) == 0 {
		return nil, fmt.Errorf("the line names no resource")
	}
	keys := verb == "label" || verb == "annotate"
	var out []KubeAction
	seen := map[string]bool{}
	add := func(r string) {
		if r = normalResource(r); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, KubeAction{Verb: verb, Resource: r})
		}
	}
	// The first word is TYPE, TYPE,TYPE or TYPE/NAME; after it a word with
	// a slash is another TYPE/NAME, and the rest are names, or a label's
	// or an annotation's KEY=VALUE and KEY-.
	for _, r := range strings.Split(rest[0], ",") {
		add(r)
	}
	for _, w := range rest[1:] {
		switch {
		case strings.Contains(w, "="), keys && strings.HasSuffix(w, "-"):
		case strings.Contains(w, "/"):
			add(w)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the line names no resource")
	}
	return out, nil
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
		all, err := KubeActionsOf(l.Text)
		if err != nil {
			fs = append(fs, Finding{Level: LevelError, Line: l.N, Code: "k8s-line", Msg: err.Error()})
			continue
		}
		for _, a := range all {
			seen[a] = true
			if !h.Allowed(a) {
				fs = append(fs, Finding{Level: LevelError, Line: l.N, Code: "k8s-undeclared", Msg: fmt.Sprintf("this line needs `# 221b:k8s %s %s` in the header: Watson refuses what is not declared", a.Verb, a.Resource)})
			}
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
	// Script, Version and Hash name the approved version a script hook
	// runs, which a production run checks again before each batch: "" for
	// a built-in hook.
	Script  string
	Version int
	Hash    string
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
	h := Hook{Label: fmt.Sprintf("%s v%d", name, v.N), Header: v.Header, Script: name, Version: v.N, Hash: v.Hash}
	for _, l := range KubeLines(v.Text) {
		if !strings.HasPrefix(l.Text, "#!") {
			h.Lines = append(h.Lines, l.Text)
		}
	}
	return h
}
