package views

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	psaapi "github.com/ucesys/221b/server/internal/psa/api"
	"github.com/ucesys/221b/server/internal/psa/policy"
)

func metaOf(o runtime.Object) (metav1.Object, bool) {
	m, err := meta.Accessor(o)
	return m, err == nil
}

const psaLabel = "pod-security.kubernetes.io/"

var (
	psaOnce      sync.Once
	psaEvaluator policy.Evaluator
)

func evaluator() policy.Evaluator {
	psaOnce.Do(func() {
		e, err := policy.NewEvaluator(policy.DefaultChecks(), nil)
		if err != nil {
			panic(err)
		}
		psaEvaluator = e
	})
	return psaEvaluator
}

// PSAPod is a pod a Pod Security level rejects, and why.
type PSAPod struct {
	Name string `json:"name"`
	// Reasons are the checks it fails ("host namespaces"); Detail adds
	// what in the pod fails them, as the API server says when it rejects
	// a new pod.
	Reasons []string `json:"reasons"`
	Detail  string   `json:"detail"`
}

// PSAResult is what a level says of a namespace's pods (P4-06).
type PSAResult struct {
	// Level and Version are what was evaluated: "baseline", "latest".
	Level   string   `json:"level"`
	Version string   `json:"version"`
	Fail    []PSAPod `json:"fail"`
	// Warnings are what the API server answers to a dry-run label change
	// that enforces the level: the header and one line per group of pods
	// that fail for the same reasons.
	Warnings []string `json:"warnings"`
}

// evaluatePSA runs the API server's own Pod Security checks against the
// pods, at a level and version ("" for latest), and words the result as
// the API server does when the enforce label changes.
func evaluatePSA(pods []*corev1.Pod, namespace, level, version string) PSAResult {
	lv := psaapi.LevelVersion{Level: psaapi.Level(level), Version: psaapi.LatestVersion()}
	if version != "" && version != "latest" {
		if v, err := psaapi.ParseVersion(version); err == nil {
			lv.Version = v
		}
	}
	r := PSAResult{Level: level, Version: lv.Version.String(), Fail: []PSAPod{}, Warnings: []string{}}
	groups := map[string][]string{}
	for _, p := range pods {
		res := policy.AggregateCheckResults(evaluator().EvaluatePod(lv, &p.ObjectMeta, &p.Spec))
		if res.Allowed {
			continue
		}
		r.Fail = append(r.Fail, PSAPod{Name: p.Name, Reasons: res.ForbiddenReasons, Detail: res.ForbiddenDetail()})
		reason := res.ForbiddenReason()
		groups[reason] = append(groups[reason], p.Name)
	}
	if len(groups) == 0 {
		return r
	}
	// As the API server's EvaluatePodsInNamespace words it: the
	// lexicographically first pod of each group, the lines sorted.
	var lines []string
	for reason, names := range groups {
		sort.Strings(names)
		switch len(names) {
		case 1:
			lines = append(lines, fmt.Sprintf("%s: %s", names[0], reason))
		case 2:
			lines = append(lines, fmt.Sprintf("%s (and 1 other pod): %s", names[0], reason))
		default:
			lines = append(lines, fmt.Sprintf("%s (and %d other pods): %s", names[0], len(names)-1, reason))
		}
	}
	sort.Strings(lines)
	r.Warnings = append([]string{fmt.Sprintf("existing pods in namespace %q violate the new PodSecurity enforce level %q", namespace, lv.String())}, lines...)
	return r
}

// ShortReasons is the union of the reasons, in first-seen order.
func ShortReasons(pods []PSAPod) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pods {
		for _, r := range p.Reasons {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return strings.Join(out, ", ")
}
