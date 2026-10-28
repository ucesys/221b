// Package watson runs kubectl and helm commands for the console (Phase 6):
// reads at once, changes only after an approval card, always as the person
// who approved them, and never through a shell. Commands are parsed here
// and checked against a fixed list of what Watson supports; the kubectl
// ones then run through kubectl's own packages and the Helm ones through
// the Helm SDK, inside the server.
package watson

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MaxLine bounds a command's length.
const MaxLine = 4000

// Refusal is a command Watson will not run, with the rule that refused it.
type Refusal struct {
	// Rule names what refused it, for the card and the audit record:
	// "not supported", "shell", "read-only cluster", "two people"...
	Rule    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(rule, format string, a ...any) *Refusal {
	return &Refusal{Rule: rule, Message: fmt.Sprintf(format, a...)}
}

// Command is a parsed command.
type Command struct {
	// Line is the command as typed, trimmed.
	Line string
	// Tool is kubectl or helm.
	Tool string
	// Verb is the subcommand: ["get"], ["rollout", "restart"], ["get", "values"].
	Verb []string
	// Namespace is -n's value; empty for the default.
	Namespace string
	// Args are what follows the verb, without -n: what the tool's own
	// command parses.
	Args []string
	// Positionals are the arguments that are not flags.
	Positionals []string
	// Flags are the flags given, by long name, with their values ("true"
	// for a bare boolean).
	Flags map[string][]string
	// Change says whether the command changes the cluster.
	Change bool
	spec   *spec
}

// Name is the command's name: "kubectl rollout restart", "helm list".
func (c *Command) Name() string { return c.Tool + " " + strings.Join(c.Verb, " ") }

// Has says whether a flag was given.
func (c *Command) Has(flag string) bool { _, ok := c.Flags[flag]; return ok }

// Flag is a flag's last value, or "".
func (c *Command) Flag(flag string) string {
	v := c.Flags[flag]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// DryRun says whether the command asks for a dry run, which changes
// nothing.
func (c *Command) DryRun() bool {
	if c.Tool == "helm" {
		return c.Has("dry-run") && c.Flag("dry-run") != "false" && c.Flag("dry-run") != "none"
	}
	v := c.Flag("dry-run")
	return c.Has("dry-run") && v != "none" && v != "false"
}

// flag describes one flag a command takes.
type flag struct {
	short string
	// value is true for a flag that takes a value.
	value bool
	// values, when set, are the only values allowed.
	values []string
	// optional is a boolean that may also take =value (--dry-run).
	optional bool
}

// spec is one supported command.
type spec struct {
	tool   string
	verb   []string
	change bool
	flags  map[string]flag
	// min and max bound the positional arguments (max -1: any).
	min, max int
	// check refuses what the flags alone cannot express.
	check func(c *Command) *Refusal
	// usage is shown when the arguments are wrong.
	usage string
}

var (
	fNamespace = flag{short: "n", value: true}
	fAll       = flag{short: "A"}
	fSelector  = flag{short: "l", value: true}
	fDryRun    = flag{value: true, optional: true, values: []string{"none", "client", "server"}}
	fOutput    = flag{short: "o", value: true}
)

func flags(m map[string]flag) map[string]flag {
	m["namespace"] = fNamespace
	return m
}

// specs lists every command Watson runs (P6-02).
var specs = []*spec{
	// ---------- kubectl reads ----------
	{tool: "kubectl", verb: []string{"get"}, min: 1, max: -1, usage: "kubectl get TYPE [NAME] [-n NAMESPACE | -A] [-o wide|yaml|json|name] [-l SELECTOR]",
		flags: flags(map[string]flag{"all-namespaces": fAll, "output": fOutput, "selector": fSelector, "field-selector": {value: true}, "show-labels": {}, "sort-by": {value: true},
			"no-headers": {}, "label-columns": {short: "L", value: true}, "show-kind": {}, "ignore-not-found": {}, "chunk-size": {value: true}, "subresource": {value: true, values: []string{"status", "scale"}}}),
		check: checkGet},
	{tool: "kubectl", verb: []string{"describe"}, min: 1, max: -1, usage: "kubectl describe TYPE [NAME] [-n NAMESPACE | -A] [-l SELECTOR]",
		flags: flags(map[string]flag{"all-namespaces": fAll, "selector": fSelector, "show-events": {optional: true, value: true, values: []string{"true", "false"}}, "chunk-size": {value: true}})},
	{tool: "kubectl", verb: []string{"logs"}, min: 0, max: 2, usage: "kubectl logs POD [-c CONTAINER] [--tail N] [--previous]",
		flags: flags(map[string]flag{"container": {short: "c", value: true}, "tail": {value: true}, "since": {value: true}, "since-time": {value: true}, "timestamps": {}, "previous": {short: "p"},
			"all-containers": {}, "selector": fSelector, "prefix": {}, "max-log-requests": {value: true}, "limit-bytes": {value: true}, "ignore-errors": {}}),
		check: checkLogs},
	{tool: "kubectl", verb: []string{"top"}, min: 1, max: 2, usage: "kubectl top node|pod [NAME] [-n NAMESPACE | -A]",
		flags: flags(map[string]flag{"all-namespaces": fAll, "selector": fSelector, "containers": {}, "sort-by": {value: true, values: []string{"cpu", "memory"}}, "no-headers": {}, "sum": {}, "show-capacity": {}}),
		check: checkTop},
	{tool: "kubectl", verb: []string{"events"}, min: 0, max: 0, usage: "kubectl events [-n NAMESPACE | -A] [--for TYPE/NAME] [--types Warning]",
		flags: flags(map[string]flag{"all-namespaces": fAll, "for": {value: true}, "types": {value: true}, "no-headers": {}, "output": fOutput, "chunk-size": {value: true}})},
	{tool: "kubectl", verb: []string{"auth", "can-i"}, min: 0, max: 3, usage: "kubectl auth can-i VERB TYPE [NAME] [-n NAMESPACE | -A] | --list",
		flags: flags(map[string]flag{"all-namespaces": fAll, "list": {}, "subresource": {value: true}, "no-headers": {}, "quiet": {short: "q"}}),
		check: checkCanI},

	// ---------- kubectl changes ----------
	{tool: "kubectl", verb: []string{"cordon"}, change: true, min: 0, max: -1, usage: "kubectl cordon NODE [NODE...]",
		flags: flags(map[string]flag{"selector": fSelector, "dry-run": fDryRun}), check: needTarget},
	{tool: "kubectl", verb: []string{"uncordon"}, change: true, min: 0, max: -1, usage: "kubectl uncordon NODE [NODE...]",
		flags: flags(map[string]flag{"selector": fSelector, "dry-run": fDryRun}), check: needTarget},
	{tool: "kubectl", verb: []string{"drain"}, change: true, min: 0, max: -1, usage: "kubectl drain NODE --ignore-daemonsets [--delete-emptydir-data]",
		flags: flags(map[string]flag{"selector": fSelector, "dry-run": fDryRun, "ignore-daemonsets": {optional: true, value: true, values: []string{"true", "false"}}, "delete-emptydir-data": {optional: true, value: true, values: []string{"true", "false"}},
			"force": {}, "grace-period": {value: true}, "timeout": {value: true}, "pod-selector": {value: true}, "disable-eviction": {}, "skip-wait-for-delete-timeout": {value: true}, "chunk-size": {value: true}}),
		check: needTarget},
	{tool: "kubectl", verb: []string{"scale"}, change: true, min: 1, max: 2, usage: "kubectl scale deployment/NAME --replicas=N",
		flags: flags(map[string]flag{"replicas": {value: true}, "current-replicas": {value: true}, "resource-version": {value: true}, "timeout": {value: true}, "dry-run": fDryRun}),
		check: checkScale},
	{tool: "kubectl", verb: []string{"rollout", "restart"}, change: true, min: 0, max: 2, usage: "kubectl rollout restart deployment/NAME",
		flags: flags(map[string]flag{"selector": fSelector, "dry-run": fDryRun}), check: checkRollout},
	{tool: "kubectl", verb: []string{"rollout", "undo"}, change: true, min: 1, max: 2, usage: "kubectl rollout undo deployment/NAME [--to-revision=N]",
		flags: flags(map[string]flag{"to-revision": {value: true}, "dry-run": fDryRun}), check: checkRollout},
	{tool: "kubectl", verb: []string{"delete"}, change: true, min: 1, max: -1, usage: "kubectl delete pod NAME [NAME...] | kubectl delete pvc NAME | kubectl delete namespace NAME",
		flags: flags(map[string]flag{"selector": fSelector, "grace-period": {value: true}, "now": {}, "force": {}, "wait": {optional: true, value: true, values: []string{"true", "false"}}, "timeout": {value: true}, "dry-run": fDryRun, "ignore-not-found": {}}),
		check: checkDelete},
	{tool: "kubectl", verb: []string{"patch"}, change: true, min: 1, max: 2, usage: `kubectl patch pvc NAME -n NAMESPACE --type merge -p '{"spec":{"resources":{"requests":{"storage":"150Gi"}}}}'`,
		flags: flags(map[string]flag{"type": {value: true, values: []string{"merge", "strategic"}}, "patch": {short: "p", value: true}, "dry-run": fDryRun}), check: checkPatch},
	{tool: "kubectl", verb: []string{"label"}, change: true, min: 2, max: -1, usage: "kubectl label TYPE NAME KEY=VALUE|KEY- [--overwrite]",
		flags: flags(map[string]flag{"overwrite": {}, "selector": fSelector, "resource-version": {value: true}, "dry-run": fDryRun, "list": {}}), check: checkLabel},
	{tool: "kubectl", verb: []string{"taint"}, change: true, min: 2, max: -1, usage: "kubectl taint nodes NODE [NODE...] KEY[=VALUE]:EFFECT|KEY[=VALUE][:EFFECT]- [--overwrite]",
		flags: flags(map[string]flag{"overwrite": {}, "selector": fSelector, "dry-run": fDryRun}), check: checkTaint},
	{tool: "kubectl", verb: []string{"annotate"}, change: true, min: 2, max: -1, usage: "kubectl annotate TYPE NAME KEY=VALUE|KEY- [--overwrite]",
		flags: flags(map[string]flag{"overwrite": {}, "selector": fSelector, "resource-version": {value: true}, "dry-run": fDryRun, "list": {}}), check: checkLabel},

	// ---------- helm ----------
	{tool: "helm", verb: []string{"list"}, min: 0, max: 0, usage: "helm list [-n NAMESPACE | -A] [-a]",
		flags: flags(map[string]flag{"all-namespaces": fAll, "all": {short: "a"}, "deployed": {}, "failed": {}, "pending": {}, "superseded": {}, "uninstalled": {}, "uninstalling": {},
			"filter": {short: "f", value: true}, "output": {short: "o", value: true, values: []string{"table", "json", "yaml"}}, "short": {short: "q"}, "date": {short: "d"}, "reverse": {short: "r"},
			"max": {short: "m", value: true}, "offset": {value: true}, "no-headers": {}, "time-format": {value: true}, "selector": fSelector})},
	{tool: "helm", verb: []string{"status"}, min: 1, max: 1, usage: "helm status RELEASE [-n NAMESPACE] [--revision N]",
		flags: flags(map[string]flag{"revision": {value: true}, "output": {short: "o", value: true, values: []string{"table", "json", "yaml"}}})},
	{tool: "helm", verb: []string{"history"}, min: 1, max: 1, usage: "helm history RELEASE [-n NAMESPACE] [--max N]",
		flags: flags(map[string]flag{"max": {value: true}, "output": {short: "o", value: true, values: []string{"table", "json", "yaml"}}})},
	{tool: "helm", verb: []string{"get", "values"}, min: 1, max: 1, usage: "helm get values RELEASE [-n NAMESPACE] [--revision N] [-a]",
		flags: flags(map[string]flag{"revision": {value: true}, "all": {short: "a"}, "output": {short: "o", value: true, values: []string{"table", "json", "yaml"}}})},
	{tool: "helm", verb: []string{"rollback"}, change: true, min: 1, max: 2, usage: "helm rollback RELEASE [REVISION] [-n NAMESPACE]",
		flags: flags(map[string]flag{"dry-run": {optional: true, value: true, values: []string{"true", "false", "none", "client", "server"}}, "wait": {optional: true, value: true, values: []string{"true", "false", "watcher", "hookOnly", "legacy"}}, "timeout": {value: true},
			"no-hooks": {}, "cleanup-on-fail": {}, "history-max": {value: true}, "wait-for-jobs": {}})},
	// Helm applications (Phase 10): a chart from a git repository, at a
	// branch or a tag; --app takes the console's saved values for it.
	{tool: "helm", verb: []string{"install"}, change: true, min: 1, max: 1, usage: "helm install RELEASE --git URL [--path DIR] --ref BRANCH|TAG [--tag] -n NAMESPACE [--app] [--create-namespace]",
		flags: flags(map[string]flag{"git": {value: true}, "path": {value: true}, "ref": {value: true}, "tag": {}, "app": {}, "create-namespace": {}, "set": {value: true},
			"dry-run": {optional: true, value: true, values: []string{"true", "false", "none", "client", "server"}}, "wait": {optional: true, value: true, values: []string{"true", "false", "watcher", "hookOnly", "legacy"}}, "timeout": {value: true}, "no-hooks": {}})},
	{tool: "helm", verb: []string{"upgrade"}, change: true, min: 1, max: 1, usage: "helm upgrade RELEASE [--git URL] [--path DIR] [--ref BRANCH|TAG] [--tag] -n NAMESPACE [--app] [--install] [--create-namespace]",
		flags: flags(map[string]flag{"git": {value: true}, "path": {value: true}, "ref": {value: true}, "tag": {}, "app": {}, "install": {short: "i"}, "create-namespace": {}, "set": {value: true},
			"dry-run": {optional: true, value: true, values: []string{"true", "false", "none", "client", "server"}}, "wait": {optional: true, value: true, values: []string{"true", "false", "watcher", "hookOnly", "legacy"}}, "timeout": {value: true}, "no-hooks": {}})},
	{tool: "helm", verb: []string{"uninstall"}, change: true, min: 1, max: 1, usage: "helm uninstall RELEASE -n NAMESPACE [--keep-history]",
		flags: flags(map[string]flag{"keep-history": {}, "dry-run": {optional: true, value: true, values: []string{"true", "false", "none", "client", "server"}}, "wait": {optional: true, value: true, values: []string{"true", "false", "watcher", "hookOnly", "legacy"}}, "timeout": {value: true}, "no-hooks": {}})},
}

// aliases maps what people type to the commands above.
var aliases = map[string]string{"helm ls": "helm list", "helm hist": "helm history"}

// Supported lists the supported commands by name, for messages and docs.
func Supported(change bool) []string {
	var out []string
	for _, s := range specs {
		if s.change == change {
			out = append(out, s.tool+" "+strings.Join(s.verb, " "))
		}
	}
	return out
}

// Parse reads a command line. It never involves a shell: quotes group
// words, and anything a shell would act on is refused rather than
// passed along.
func Parse(line string) (*Command, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, refuse("empty", "Type a kubectl or helm command.")
	}
	if len(line) > MaxLine {
		return nil, refuse("too long", "The command is longer than %d characters.", MaxLine)
	}
	words, err := split(line)
	if err != nil {
		return nil, err
	}
	if len(words) > 128 {
		return nil, refuse("too long", "The command has more than 128 words.")
	}
	tool := words[0]
	if tool != "kubectl" && tool != "helm" {
		return nil, refuse("not supported", "Watson runs kubectl and helm commands only; %q is not one.", tool)
	}
	// Flags may come before the verb, as in kubectl -n web get pods: they
	// are taken out and parsed with the rest.
	args, lead := leadingFlags(words[1:])
	sp, rest := match(tool, args)
	if sp == nil {
		sub := ""
		if len(args) > 0 {
			sub = " " + args[0]
		}
		return nil, refuse("not supported", "Watson does not run %s%s. It runs %s, and changes with %s.", tool, sub, strings.Join(Supported(false), ", "), strings.Join(Supported(true), ", "))
	}
	c := &Command{Line: line, Tool: tool, Verb: sp.verb, Change: sp.change, Flags: map[string][]string{}, spec: sp}
	if err := c.parseArgs(append(append([]string(nil), lead...), rest...)); err != nil {
		return nil, err
	}
	n := len(c.Positionals)
	if n < sp.min || (sp.max >= 0 && n > sp.max) {
		return nil, refuse("usage", "Usage: %s", sp.usage)
	}
	if sp.check != nil {
		if r := sp.check(c); r != nil {
			return nil, r
		}
	}
	return c, nil
}

// valueFlags are the flags that take a value when they come before the
// verb, written apart from it (-n web); the rest are taken to stand alone.
var valueFlags = map[string]bool{"n": true, "namespace": true, "context": true, "kubeconfig": true, "cluster": true, "user": true, "server": true, "s": true,
	"token": true, "as": true, "as-group": true, "as-uid": true, "kube-context": true, "kube-apiserver": true, "kube-token": true, "kube-as-user": true,
	"kube-as-group": true, "kube-ca-file": true, "request-timeout": true, "username": true, "password": true}

// leadingFlags splits the flags before the verb from the words after them.
func leadingFlags(words []string) (rest, lead []string) {
	i := 0
	for i < len(words) && strings.HasPrefix(words[i], "-") && words[i] != "-" && words[i] != "--" {
		w := words[i]
		lead = append(lead, w)
		i++
		name := strings.TrimLeft(w, "-")
		if !strings.Contains(name, "=") && valueFlags[name] && i < len(words) {
			lead = append(lead, words[i])
			i++
		}
	}
	return words[i:], lead
}

// match finds the spec for a tool's words, longest verb first.
func match(tool string, words []string) (*spec, []string) {
	var best *spec
	for _, s := range specs {
		if s.tool != tool || len(s.verb) > len(words) {
			continue
		}
		ok := true
		for i, v := range s.verb {
			w := words[i]
			if a, found := aliases[tool+" "+w]; found && i == 0 {
				w = strings.TrimPrefix(a, tool+" ")
			}
			if w != v {
				ok = false
				break
			}
		}
		if ok && (best == nil || len(s.verb) > len(best.verb)) {
			best = s
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, words[len(best.verb):]
}

// blocked are flags no command may take: they would make kubectl or helm
// act as someone else, against another cluster, or outside the console.
var blocked = map[string]string{
	"kubeconfig": "Watson uses the console's connection to the cluster.", "context": "Watson uses the console's connection to the cluster.",
	"cluster": "Watson uses the console's connection to the cluster.", "server": "Watson uses the console's connection to the cluster.", "s": "Watson uses the console's connection to the cluster.",
	"user": "Watson runs commands as you.", "token": "Watson runs commands as you.", "as": "Watson runs commands as you; --as is not supported.",
	"as-group": "Watson runs commands as you; --as-group is not supported.", "as-uid": "Watson runs commands as you; --as-uid is not supported.",
	"username": "Watson runs commands as you.", "password": "Watson runs commands as you.",
	"client-certificate": "Watson uses the console's connection to the cluster.", "client-key": "Watson uses the console's connection to the cluster.",
	"certificate-authority": "Watson uses the console's connection to the cluster.", "insecure-skip-tls-verify": "Watson uses the console's connection to the cluster.",
	"tls-server-name": "Watson uses the console's connection to the cluster.", "kube-context": "Watson uses the console's connection to the cluster.",
	"kube-apiserver": "Watson uses the console's connection to the cluster.", "kube-token": "Watson runs commands as you.", "kube-as-user": "Watson runs commands as you.",
	"kube-as-group": "Watson runs commands as you.", "kube-ca-file": "Watson uses the console's connection to the cluster.", "kube-insecure-skip-tls-verify": "Watson uses the console's connection to the cluster.",
	"registry-config": "Watson does not reach registries.", "repository-config": "Watson does not reach repositories.", "repository-cache": "Watson does not reach repositories.",
	"watch": "Watson prints once and does not follow changes; run the command again for the latest.", "w": "Watson prints once and does not follow changes; run the command again for the latest.",
	"follow": "Watson prints the logs once; the pod's Logs tab follows them live.", "f": "Watson prints the logs once; the pod's Logs tab follows them live.",
	"raw": "Watson does not make raw API requests.", "filename": "Watson does not read files.", "kustomize": "Watson does not read files.", "recursive": "Watson does not read files.",
	"v": "Watson does not change kubectl's logging.", "vmodule": "Watson does not change kubectl's logging.", "debug": "Watson does not change helm's logging.",
	"profile": "Watson does not profile commands.", "cache-dir": "Watson does not use a cache directory.", "template": "Watson does not run templates.",
}

// parseArgs reads flags and positional arguments the way kubectl and helm
// (pflag) do, refusing any flag the command does not take.
func (c *Command) parseArgs(words []string) error {
	sp := c.spec
	byShort := map[string]string{}
	for name, f := range sp.flags {
		if f.short != "" {
			byShort[f.short] = name
		}
	}
	add := func(name, value, typed string) error {
		f, ok := sp.flags[name]
		if !ok {
			if why, b := blocked[name]; b {
				return refuse("flag", "%s does not take %s here: %s", c.Name(), typed, why)
			}
			return refuse("flag", "%s does not take %s here. Usage: %s", c.Name(), typed, sp.usage)
		}
		if len(f.values) > 0 && value != "" {
			ok := false
			for _, v := range f.values {
				if v == value {
					ok = true
				}
			}
			if !ok {
				return refuse("flag", "%s takes one of %s, not %q.", typed, strings.Join(f.values, ", "), value)
			}
		}
		if name == "namespace" {
			if value == "" {
				return refuse("usage", "-n needs a namespace.")
			}
			c.Namespace = value
			return nil
		}
		if name == "dry-run" && value == "" {
			// A bare --dry-run means a dry run on the server.
			value = map[string]string{"helm": "true", "kubectl": "server"}[c.Tool]
		}
		c.Flags[name] = append(c.Flags[name], value)
		arg := "--" + name
		if f.value || f.optional {
			if value != "" || !f.optional {
				arg += "=" + value
			}
		}
		c.Args = append(c.Args, arg)
		return nil
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--":
			return refuse("flag", "Watson does not pass arguments after --.")
		case strings.HasPrefix(w, "--"):
			name, value, hasValue := strings.Cut(w[2:], "=")
			f, known := c.spec.flags[name]
			if known && f.value && !f.optional && !hasValue {
				if i+1 >= len(words) {
					return refuse("usage", "%s needs a value.", "--"+name)
				}
				i++
				value, hasValue = words[i], true
			}
			if known && !f.value && !f.optional && hasValue && value != "true" {
				if value == "false" {
					continue
				}
				return refuse("flag", "--%s takes no value.", name)
			}
			if err := add(name, value, "--"+name); err != nil {
				return err
			}
		case strings.HasPrefix(w, "-") && len(w) > 1 && !isNumber(w):
			// Shorthands: -n NS, -nNS, -n=NS, and booleans run together (-qA).
			body := w[1:]
			for j := 0; j < len(body); j++ {
				sh := string(body[j])
				name, known := byShort[sh]
				if !known {
					if why, b := blocked[sh]; b {
						return refuse("flag", "%s does not take -%s here: %s", c.Name(), sh, why)
					}
					return refuse("flag", "%s does not take -%s here. Usage: %s", c.Name(), sh, sp.usage)
				}
				f := c.spec.flags[name]
				if f.value && !f.optional {
					value := strings.TrimPrefix(body[j+1:], "=")
					if value == "" {
						if i+1 >= len(words) {
							return refuse("usage", "-%s needs a value.", sh)
						}
						i++
						value = words[i]
					}
					if err := add(name, value, "-"+sh); err != nil {
						return err
					}
					break
				}
				if err := add(name, "", "-"+sh); err != nil {
					return err
				}
			}
		default:
			c.Positionals = append(c.Positionals, w)
			c.Args = append(c.Args, w)
		}
	}
	return nil
}

func isNumber(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// shellChars are what a shell would act on. Watson has no shell, so they
// would do nothing; refusing them says so instead of passing them to
// kubectl as part of a name.
var shellChars = []struct{ s, what string }{
	{"&&", "&&"}, {"||", "||"}, {"|", "a pipe (|)"}, {";", "a semicolon"}, {"&", "&"}, {">", "a redirection (>)"}, {"<", "a redirection (<)"},
	{"`", "a backquote"}, {"$(", "$( )"}, {"${", "${ }"}, {"$", "a variable ($)"}, {"\n", "a second line"},
}

// split breaks a line into words: whitespace separates them, single
// quotes keep everything, double quotes keep everything but \" and \\,
// and a backslash outside quotes keeps the next character. Characters a
// shell would act on are refused when unquoted.
func split(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	has := false
	flush := func() {
		if has {
			words = append(words, cur.String())
		}
		cur.Reset()
		has = false
	}
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case ch == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, refuse("usage", "A single quote is not closed.")
			}
			cur.WriteString(line[i+1 : i+1+end])
			has = true
			i += end + 1
		case ch == '"':
			j := i + 1
			for ; j < len(line) && line[j] != '"'; j++ {
				if line[j] == '\\' && j+1 < len(line) && (line[j+1] == '"' || line[j+1] == '\\') {
					j++
				} else if line[j] == '$' || line[j] == '`' {
					return nil, refuse("shell", "Watson has no shell, so %s inside double quotes would not be expanded. Use single quotes for a literal value.", map[byte]string{'$': "$", '`': "a backquote"}[line[j]])
				}
				cur.WriteByte(line[j])
			}
			if j >= len(line) {
				return nil, refuse("usage", "A double quote is not closed.")
			}
			cur2 := strings.ReplaceAll(strings.ReplaceAll(cur.String(), `\"`, `"`), `\\`, `\`)
			cur.Reset()
			cur.WriteString(cur2)
			has = true
			i = j
		case ch == '\\':
			if i+1 < len(line) && line[i+1] == '\n' {
				i++ // a continued line
				continue
			}
			if i+1 < len(line) {
				cur.WriteByte(line[i+1])
				has = true
				i++
			}
		case ch == ' ' || ch == '\t' || ch == '\r':
			flush()
		case ch == '#' && !has:
			if strings.TrimSpace(line[i:]) != "" && i == 0 {
				return nil, refuse("usage", "That is a comment, not a command.")
			}
			flush()
			return finish(words)
		default:
			for _, sc := range shellChars {
				if strings.HasPrefix(line[i:], sc.s) {
					return nil, refuse("shell", "Watson runs one kubectl or helm command at a time, without a shell, so %s would do nothing. Send each command on its own.", sc.what)
				}
			}
			cur.WriteByte(ch)
			has = true
		}
	}
	flush()
	return finish(words)
}

func finish(words []string) ([]string, error) {
	if len(words) == 0 {
		return nil, refuse("empty", "Type a kubectl or helm command.")
	}
	return words, nil
}

// SplitCommands breaks a block of commands, as Sherlock writes them, into
// one per line: continued lines joined, comments and blank lines dropped.
func SplitCommands(text string) []string {
	var out []string
	cur := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if cur == "" && (strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			cur += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		cur += line
		if c := strings.TrimSpace(stripComment(cur)); c != "" {
			out = append(out, c)
		}
		cur = ""
	}
	if c := strings.TrimSpace(stripComment(cur)); c != "" {
		out = append(out, c)
	}
	return out
}

func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case q != 0:
			if ch == q {
				q = 0
			}
		case ch == '\'' || ch == '"':
			q = ch
		case ch == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

// ---------- per-command checks ----------

// resourceNames maps what kubectl accepts for a type to its resource.
var resourceNames = map[string]string{}

func init() {
	for res, names := range map[string][]string{
		"pods": {"po", "pod"}, "nodes": {"no", "node"}, "deployments": {"deploy", "deployment", "deployments.apps", "deployment.apps"},
		"statefulsets": {"sts", "statefulset", "statefulsets.apps", "statefulset.apps"}, "daemonsets": {"ds", "daemonset", "daemonsets.apps", "daemonset.apps"},
		"replicasets": {"rs", "replicaset", "replicasets.apps", "replicaset.apps"}, "secrets": {"secret"}, "namespaces": {"ns", "namespace"},
		"persistentvolumeclaims": {"pvc", "persistentvolumeclaim"}, "persistentvolumes": {"pv", "persistentvolume"},
		"storageclasses": {"sc", "storageclass", "storageclasses.storage.k8s.io", "storageclass.storage.k8s.io"},
	} {
		resourceNames[res] = res
		for _, n := range names {
			resourceNames[n] = res
		}
	}
}

// groupOf is the API group of each resource in resourceNames.
var groupOf = map[string]string{"deployments": "apps", "statefulsets": "apps", "daemonsets": "apps", "replicasets": "apps", "storageclasses": "storage.k8s.io"}

// resourceOf names the resource of a TYPE argument, as far as Watson
// needs to know it; other types come back as typed. kubectl also takes a
// type with its group, or with its version and group, the core group
// being empty: secrets. and secret.v1. name Secrets as secret does.
func resourceOf(t string) string {
	t = strings.ToLower(t)
	if r, ok := resourceNames[t]; ok {
		return r
	}
	gvr, gr := schema.ParseResourceArg(t)
	if gvr != nil {
		if r, ok := resourceNames[gvr.Resource]; ok && gvr.Group == groupOf[r] {
			return r
		}
	}
	if r, ok := resourceNames[gr.Resource]; ok && gr.Group == groupOf[r] {
		return r
	}
	return t
}

// isChange says whether an argument of kubectl label or annotate is a
// change rather than an object, as kubectl tells them apart: KEY=VALUE
// sets, and KEY- removes, prefixed keys such as a/b- included.
func isChange(p string) bool {
	return (strings.Contains(p, "=") && p[0] != '=') || (strings.HasSuffix(p, "-") && p != "-")
}

// targets reads TYPE NAME... or TYPE/NAME... into the types they name.
func targets(pos []string) (types []string, names []string) {
	if len(pos) == 0 {
		return nil, nil
	}
	if strings.Contains(pos[0], "/") {
		for _, p := range pos {
			t, n, _ := strings.Cut(p, "/")
			types = append(types, resourceOf(t))
			names = append(names, n)
		}
		return types, names
	}
	for _, t := range strings.Split(pos[0], ",") {
		types = append(types, resourceOf(t))
	}
	return types, pos[1:]
}

func checkGet(c *Command) *Refusal {
	types, _ := targets(c.Positionals)
	out := c.Flag("output")
	if out != "" {
		ok := out == "wide" || out == "yaml" || out == "json" || out == "name" || strings.HasPrefix(out, "jsonpath=") || strings.HasPrefix(out, "jsonpath-as-json=") || strings.HasPrefix(out, "custom-columns=")
		if !ok {
			return refuse("flag", "Watson prints -o wide, yaml, json, name, jsonpath=… and custom-columns=…, not %q.", out)
		}
	}
	for _, t := range types {
		if t == "secrets" && out != "" && out != "wide" && out != "name" {
			return refuse("secret values", "Watson never prints Secret values. kubectl describe secret shows their keys and sizes.")
		}
	}
	return nil
}

func checkLogs(c *Command) *Refusal {
	if len(c.Positionals) == 0 && !c.Has("selector") {
		return refuse("usage", "Name a pod, or a selector with -l. Usage: %s", c.spec.usage)
	}
	return nil
}

func checkTop(c *Command) *Refusal {
	switch c.Positionals[0] {
	case "node", "nodes", "no", "pod", "pods", "po":
		return nil
	}
	return refuse("usage", "kubectl top shows nodes or pods. Usage: %s", c.spec.usage)
}

func checkCanI(c *Command) *Refusal {
	if c.Has("list") {
		if len(c.Positionals) > 0 {
			return refuse("usage", "kubectl auth can-i --list takes no other arguments.")
		}
		return nil
	}
	if len(c.Positionals) < 2 {
		return refuse("usage", "Usage: %s", c.spec.usage)
	}
	return nil
}

func needTarget(c *Command) *Refusal {
	if len(c.Positionals) == 0 && !c.Has("selector") {
		return refuse("usage", "Name a node, or select nodes with -l. Usage: %s", c.spec.usage)
	}
	return nil
}

func checkScale(c *Command) *Refusal {
	if !c.Has("replicas") {
		return refuse("usage", "Say how many replicas with --replicas=N.")
	}
	types, names := targets(c.Positionals)
	if len(names) == 0 {
		return refuse("usage", "Usage: %s", c.spec.usage)
	}
	for _, t := range types {
		if t != "deployments" && t != "statefulsets" && t != "replicasets" {
			return refuse("not supported", "Watson scales Deployments, StatefulSets and ReplicaSets, not %s.", t)
		}
	}
	return nil
}

func checkRollout(c *Command) *Refusal {
	types, names := targets(c.Positionals)
	if len(names) == 0 && !c.Has("selector") {
		return refuse("usage", "Usage: %s", c.spec.usage)
	}
	for _, t := range types {
		if t != "deployments" && t != "statefulsets" && t != "daemonsets" {
			return refuse("not supported", "kubectl %s works on Deployments, StatefulSets and DaemonSets, not %s.", strings.Join(c.Verb, " "), t)
		}
	}
	return nil
}

func checkDelete(c *Command) *Refusal {
	types, names := targets(c.Positionals)
	claims, namespaces := false, false
	for _, t := range types {
		switch t {
		case "pods":
		case "persistentvolumeclaims":
			// The Storage tab's Delete claim (ST-18), one claim by name.
			claims = true
		case "namespaces":
			// The Namespaces tab's Delete namespace (NS-02), one by name.
			namespaces = true
		default:
			return refuse("not supported", "Watson deletes only pods, volume claims and namespaces; deleting %s is not supported.", t)
		}
	}
	if claims && (len(types) > 1 || c.Has("selector") || len(names) != 1) {
		return refuse("usage", "Name the one claim to delete: kubectl delete pvc NAME -n NAMESPACE.")
	}
	if namespaces && (len(types) > 1 || c.Has("selector") || len(names) != 1) {
		return refuse("usage", "Name the one namespace to delete: kubectl delete namespace NAME.")
	}
	if len(names) == 0 && !c.Has("selector") {
		return refuse("usage", "Name the pods to delete, or select them with -l. Watson does not delete every pod at once.")
	}
	return nil
}

// checkPatch holds kubectl patch to the one patch the Storage tab makes
// (ST-18): a claim's requested size, nothing else.
func checkPatch(c *Command) *Refusal {
	types, names := targets(c.Positionals)
	if len(types) != 1 || types[0] != "persistentvolumeclaims" || len(names) != 1 {
		return refuse("not supported", "Watson patches only a volume claim's size: %s", c.spec.usage)
	}
	if t := c.Flag("type"); t != "" && t != "merge" && t != "strategic" {
		return refuse("flag", "Patch the size with --type merge, not %q.", t)
	}
	if _, err := ClaimSize(c.Flag("patch")); err != nil {
		return refuse("not supported", "%v Usage: %s", err, c.spec.usage)
	}
	return nil
}

// ClaimSize reads the size a claim patch asks for; the patch must set
// spec.resources.requests.storage and nothing else.
func ClaimSize(patch string) (resource.Quantity, error) {
	var p map[string]any
	if err := json.Unmarshal([]byte(patch), &p); err != nil {
		return resource.Quantity{}, errors.New("The patch is not JSON.")
	}
	size, ok := only(p, "spec", "resources", "requests", "storage").(string)
	if !ok {
		return resource.Quantity{}, errors.New(`The patch may set only the requested size: {"spec":{"resources":{"requests":{"storage":"150Gi"}}}}.`)
	}
	q, err := resource.ParseQuantity(size)
	if err != nil || q.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("%q is not a size such as 150Gi.", size)
	}
	return q, nil
}

// only walks a JSON object along keys that must each be the only key.
func only(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok || len(m) != 1 {
			return nil
		}
		v = m[k]
	}
	return v
}

func checkLabel(c *Command) *Refusal {
	if c.Has("list") {
		return nil
	}
	// The access rules are written by the chart, and by a person who holds
	// that right; Watson never changes them, whatever the person's role
	// would let (RB-52).
	types, _ := targets(c.Positionals)
	for _, t := range types {
		if isAccessRule(t) {
			return refuse("access rules", "Watson does not change roles or bindings: they are set by the chart's values, and 221B never writes them.")
		}
	}
	var changes int
	for _, p := range c.Positionals {
		if isChange(p) {
			changes++
		}
	}
	if changes == 0 {
		return refuse("usage", "Say what to change: KEY=VALUE to set, KEY- to remove. Usage: %s", c.spec.usage)
	}
	return nil
}

// checkTaint holds kubectl taint to nodes, the only kind with taints, and
// to at least one taint to add or remove.
func checkTaint(c *Command) *Refusal {
	nodes, changes := splitTaints(c.Positionals)
	types, _ := targets(nodes)
	for _, t := range types {
		if t != "nodes" {
			return refuse("usage", "Only nodes have taints. Usage: %s", c.spec.usage)
		}
	}
	if len(changes) == 0 {
		return refuse("usage", "Say which taint: KEY=VALUE:EFFECT to add, KEY:EFFECT- or KEY- to remove. Usage: %s", c.spec.usage)
	}
	return nil
}

// splitTaints parts kubectl taint's arguments into the nodes and the
// taints: after the first, an argument that reads as a taint is one.
func splitTaints(pos []string) (nodes, taints []string) {
	for i, p := range pos {
		if i > 0 && isTaint(p) {
			taints = append(taints, p)
		} else {
			nodes = append(nodes, p)
		}
	}
	return nodes, taints
}

// isTaint says whether an argument of kubectl taint is a taint rather
// than a node: KEY[=VALUE]:EFFECT, or a removal ending in "-".
func isTaint(p string) bool {
	return strings.Contains(p, ":") || strings.Contains(p, "=") || (strings.HasSuffix(p, "-") && p != "-")
}

// isAccessRule says whether a TYPE names Kubernetes' own access rules.
func isAccessRule(t string) bool {
	t, _, _ = strings.Cut(strings.ToLower(t), ".")
	switch t {
	case "role", "roles", "rolebinding", "rolebindings", "clusterrole", "clusterroles", "clusterrolebinding", "clusterrolebindings":
		return true
	}
	return false
}

// Help is what Watson prints for "help".
func Help() []string {
	reads, changes := Supported(false), Supported(true)
	sort.Strings(reads)
	sort.Strings(changes)
	return []string{
		"Watson runs these at once: " + strings.Join(reads, ", ") + ".",
		"These change the cluster, so each waits for an approval card: " + strings.Join(changes, ", ") + ".",
		"Commands run as you, without a shell, against this cluster only.",
	}
}
