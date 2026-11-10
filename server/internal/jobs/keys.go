package jobs

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// --- One flow for findings and cases (Phase 12, wave 6, CF-05 to CF-18) ---
//
// Every finding of a run becomes a case or joins one, by its key: the
// cluster, the object and the check. So that the key stays the same from
// one run to the next, each kind has a fixed catalogue of check ids its
// agent names (or other.<slug>, which the console keeps as it is); the
// console writes the object one way (namespace/Kind/name or Kind/name,
// aliases resolved) and checks it is on the cluster when the cluster
// answers; and the agent's task lists the keys in use on its cluster, to
// reuse a check id for the same problem. A finding whose check or object
// is not valid is kept on the run as invalid, and not routed.

// Check is one id of a kind's catalogue, with what it means and the type
// of the case a finding of it opens (CF-37).
type Check struct {
	ID   string `json:"id"`
	Says string `json:"says"`
	Type string `json:"type"`
}

// AuditChecks are the nightly audit's.
var AuditChecks = []Check{
	{"rbac.cluster-admin-binding", "a binding gives cluster-admin to a user, group or ServiceAccount", "security"},
	{"rbac.wildcard-rule", "a role grants * on resources or verbs", "security"},
	{"rbac.secrets-read", "a subject can read Secrets it should not", "security"},
	{"rbac.pod-exec", "a subject can exec into pods it should not", "security"},
	{"rbac.sa-overprivileged", "a ServiceAccount holds more than it uses", "security"},
	{"pod.privileged", "a container runs privileged or with host namespaces", "security"},
	{"pod.root-user", "a container runs as root", "security"},
	{"pod.no-limits", "a container has no CPU or memory limits", "capacity"},
	{"pod.no-probes", "a container has no readiness or liveness probe", "availability"},
	{"image.latest-tag", "an image uses the latest tag or none", "security"},
	{"psa.level-missing", "a namespace has no Pod Security level", "security"},
	{"netpol.missing", "a namespace has no network policy", "security"},
	{"quota.missing", "a shared namespace has no resource quota", "capacity"},
	{"pvc.nearly-full", "a volume is 85% full or more", "capacity"},
	{"pvc.unbound", "a claim cannot bind", "availability"},
	{"cert.expiring", "a certificate expires within 30 days", "availability"},
	{"node.disk-pressure", "a node reports disk pressure", "capacity"},
	{"node.memory-pressure", "a node reports memory pressure", "capacity"},
	{"node.not-ready", "a node is not ready", "availability"},
}

// AlertChecks are the alert diagnosis's.
var AlertChecks = []Check{
	{"pod.crashloop", "a container restarts over and over", "availability"},
	{"pod.oomkilled", "a container was killed for memory", "capacity"},
	{"pod.image-pull", "an image cannot be pulled", "availability"},
	{"pod.pending", "a pod cannot be scheduled", "capacity"},
	{"pod.probe-failing", "a readiness or liveness probe fails", "availability"},
	{"pod.config-missing", "a ConfigMap, Secret or volume a pod needs is missing", "configuration"},
	{"workload.unavailable", "a Deployment, StatefulSet or DaemonSet has replicas unavailable", "outage"},
	{"job.failed", "a Job or CronJob run failed", "availability"},
	{"service.no-endpoints", "a Service has no ready endpoints", "outage"},
	{"pvc.nearly-full", "a volume is 85% full or more", "capacity"},
	{"node.not-ready", "a node is not ready", "availability"},
	{"node.disk-pressure", "a node reports disk pressure", "capacity"},
	{"node.memory-pressure", "a node reports memory pressure", "capacity"},
	{"cert.expiring", "a certificate expires within 30 days", "availability"},
}

// DigestChecks are the daily digest's.
var DigestChecks = []Check{
	{"digest.failed-runs", "jobs whose runs failed", "other"},
	{"digest.denied-access", "sign-ins or actions refused", "security"},
	{"digest.spend", "AI agents' spend out of the ordinary", "compliance"},
	{"digest.change-failed", "a change to a cluster or an application failed", "configuration"},
	{"digest.approval-waiting", "approvals or proposed changes waiting long", "other"},
}

// KindDrift is Mycroft's drift check (package knowledge's JobDrift,
// KN-19): it has no agent to offer rules to, but its differences are
// keyed by the rules below, so an exception or a known issue stops a
// proposal as it stops a finding (KN-31).
const KindDrift = "driftCheck"

// InventoryChecks are the inventory's and the drift check's: what a
// difference between the documents and the console is (KN-31). No audit
// job is offered them.
var InventoryChecks = []Check{
	{"inventory.host-missing", "a document lists a host the console does not know", "configuration"},
	{"inventory.host-unreachable", "a host a document lists is not answering", "availability"},
	{"inventory.address-differs", "a document gives a host another address than the console reaches it at", "configuration"},
	{"inventory.host-unlisted", "the console knows a host no document names", "configuration"},
	{"inventory.fleet-missing", "a document names a group of hosts the console has no fleet for", "configuration"},
}

// KindRules is a kind with a catalogue: its id, its label and its
// built-in rules.
type KindRules struct {
	Kind   string
	Label  string
	Checks []Check
}

// BuiltinRules are the kinds with a catalogue and the rules they ship
// with (ADR-138): what the merged list of validation rules starts from.
var BuiltinRules = []KindRules{{KindAudit, "Nightly audit", AuditChecks}, {KindAlert, "Alert diagnosis", AlertChecks}, {KindDigest, "Daily digest", DigestChecks}, {KindDrift, "Drift check", InventoryChecks}}

// Rules is the list of validation rules (check ids) as it stands
// (ADR-138): the built-in catalogues, with what a SuperAdmin added,
// changed, retired or replaced in AIOps › Mycroft › Validation rules
// (package casework keeps them). A kind's agent is told the rules it is
// offered; a finding is valid with any rule of its kind, retired or
// replaced ones too, so the cases and exceptions keyed by them still
// match; and routes by the id its rule is replaced by.
type Rules interface {
	// Offered are the active rules of a kind: what its agent is told.
	Offered(k Kind) []Check
	// Known says whether a check id is one of a kind's rules, whatever
	// its state.
	Known(k Kind, id string) bool
	// Canon is the id a check id routes by: the rule that replaced it,
	// at the end of the chain; itself otherwise.
	Canon(id string) string
}

// RuleBook gives the rules as they stand, read once for a run or a task.
type RuleBook func(ctx context.Context) Rules

// builtinRules are the kinds' own catalogues, while nobody keeps others.
type builtinRules struct{}

func (builtinRules) Offered(k Kind) []Check { return k.Checks }
func (builtinRules) Known(k Kind, id string) bool {
	return slices.ContainsFunc(k.Checks, func(c Check) bool { return c.ID == id })
}
func (builtinRules) Canon(id string) string { return id }

// SetRules gives the service the validation rules as they stand
// (ADR-138); nil keeps the kinds' built-in catalogues.
func (s *Service) SetRules(f RuleBook) { s.o.Rules = f }

// rules are the validation rules for a run or a task.
func (s *Service) rules(ctx context.Context) Rules {
	if s.o.Rules != nil {
		if r := s.o.Rules(ctx); r != nil {
			return r
		}
	}
	return builtinRules{}
}

// CheckType is the type of the case a finding of a check opens (CF-37):
// the catalogues', other for other.<slug> and any check they do not hold.
func CheckType(check string) string {
	for _, list := range [][]Check{AuditChecks, AlertChecks, DigestChecks, InventoryChecks} {
		for _, c := range list {
			if c.ID == check && c.Type != "" {
				return c.Type
			}
		}
	}
	return "other"
}

// slugPattern is other.<slug>: lower case, digits and dashes, at most 48.
var slugPattern = regexp.MustCompile(`^other\.[a-z0-9]([a-z0-9-]{0,46}[a-z0-9])?$`)

// ValidCheck says why a check id is not one of a kind's built-in
// catalogue, if it is not.
func ValidCheck(k Kind, check string) string {
	return validCheck(k, builtinRules{}, check)
}

// validCheck says why a check id is not one of a kind's rules as they
// stand, nor other.<slug>, if it is not (ADR-138).
func validCheck(k Kind, rules Rules, check string) string {
	if check == "" {
		return "the finding names no check"
	}
	if rules.Known(k, check) || slugPattern.MatchString(check) {
		return ""
	}
	return fmt.Sprintf("the check %q is not one of the %s's, nor other.<slug> (lower case, digits, dashes, at most 48)", clip(check, 80), strings.ToLower(k.Label))
}

// kindAliases are the short names and plurals of the kinds, lower case,
// to their canonical names.
var kindAliases = map[string]string{}

func init() {
	for canon, aliases := range map[string][]string{
		"Pod": {"po", "pods"}, "Deployment": {"deploy", "deployments"}, "DaemonSet": {"ds", "daemonsets"}, "StatefulSet": {"sts", "statefulsets"},
		"ReplicaSet": {"rs", "replicasets"}, "Job": {"jobs"}, "CronJob": {"cj", "cronjobs"}, "Service": {"svc", "services"},
		"ConfigMap": {"cm", "configmaps"}, "Secret": {"secrets"}, "ServiceAccount": {"sa", "serviceaccounts"}, "Namespace": {"ns", "namespaces"},
		"Node": {"no", "nodes"}, "PersistentVolumeClaim": {"pvc", "persistentvolumeclaims"}, "PersistentVolume": {"pv", "persistentvolumes"},
		"Ingress": {"ing", "ingresses"}, "NetworkPolicy": {"netpol", "networkpolicies"}, "Role": {"roles"}, "RoleBinding": {"rb", "rolebindings"},
		"ClusterRole": {"cr", "clusterroles"}, "ClusterRoleBinding": {"crb", "clusterrolebindings"}, "StorageClass": {"sc", "storageclasses"},
		"HorizontalPodAutoscaler": {"hpa", "horizontalpodautoscalers"}, "PodDisruptionBudget": {"pdb", "poddisruptionbudgets"},
		"ResourceQuota": {"quota", "resourcequotas"}, "LimitRange": {"limits", "limitranges"}, "Endpoints": {"ep"}, "Certificate": {"cert", "certificates"},
		"CustomResourceDefinition": {"crd", "crds", "customresourcedefinitions"},
	} {
		kindAliases[strings.ToLower(canon)] = canon
		for _, a := range aliases {
			kindAliases[a] = canon
		}
	}
}

// clusterScoped are the kinds without a namespace.
var clusterScoped = map[string]bool{"Namespace": true, "Node": true, "PersistentVolume": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"StorageClass": true, "CustomResourceDefinition": true}

var namePart = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.:]*[a-z0-9])?$`)

// Object is a finding's object, normalised.
type Object struct {
	Namespace string
	Kind      string
	Name      string
}

// String is the object as a key holds it: namespace/Kind/name or
// Kind/name; "" for none.
func (o Object) String() string {
	switch {
	case o.Kind == "":
		return ""
	case o.Namespace == "":
		return o.Kind + "/" + o.Name
	}
	return o.Namespace + "/" + o.Kind + "/" + o.Name
}

// NormalizeObject writes an object one way (CF-05): namespace/Kind/name or
// Kind/name, the kind's alias or case resolved to its canonical name
// (deploy → Deployment, crb → ClusterRoleBinding); a namespaced kind
// written as kind/name in namespace/name order is read either way
// ("Deployment/shop/web"). "" is the cluster as a whole. A kind it does
// not know keeps its words, its first letter upper case.
func NormalizeObject(text string) (Object, error) {
	t := strings.TrimSpace(text)
	if t == "" || t == "-" || strings.EqualFold(t, "cluster") {
		return Object{}, nil
	}
	parts := strings.Split(t, "/")
	if slices.Contains(parts, "") || len(parts) < 2 || len(parts) > 3 || strings.ContainsAny(t, " \t") {
		return Object{}, fmt.Errorf("name the object as namespace/Kind/name or Kind/name, not %q", clip(t, 80))
	}
	kindOf := func(s string) (string, bool) {
		if c, ok := kindAliases[strings.ToLower(s)]; ok {
			return c, true
		}
		return "", false
	}
	var o Object
	switch len(parts) {
	case 2:
		k, ok := kindOf(parts[0])
		if !ok {
			k = strings.ToUpper(parts[0][:1]) + parts[0][1:]
		}
		o = Object{Kind: k, Name: parts[1]}
	case 3:
		if k, ok := kindOf(parts[1]); ok {
			o = Object{Namespace: parts[0], Kind: k, Name: parts[2]}
		} else if k, ok := kindOf(parts[0]); ok {
			o = Object{Namespace: parts[1], Kind: k, Name: parts[2]}
		} else {
			o = Object{Namespace: parts[0], Kind: strings.ToUpper(parts[1][:1]) + parts[1][1:], Name: parts[2]}
		}
	}
	// A cluster-scoped object named with a namespace ("shop/Namespace/shop")
	// is the same object: the namespace is dropped, so the key stays one.
	if clusterScoped[o.Kind] {
		o.Namespace = ""
	}
	if !namePart.MatchString(strings.ToLower(o.Name)) || o.Namespace != "" && !namePart.MatchString(o.Namespace) {
		return Object{}, fmt.Errorf("%q is not an object's name", clip(t, 80))
	}
	return o, nil
}

// ObjectCheck says whether an object is on a cluster: answered false when
// the cluster did not answer (the finding is kept, unverified).
type ObjectCheck func(ctx context.Context, cluster string, o Object) (exists, answered bool)

// keyOf gives a finding its key, or says why it has none (CF-05). The
// key holds the id its rule routes by: a replaced rule's finding routes
// by the rule that replaced it (ADR-138); the finding keeps its own.
func (s *Service) keyOf(ctx context.Context, k Kind, rules Rules, f *Finding) {
	if why := validCheck(k, rules, f.Check); why != "" {
		f.Routing, f.InvalidReason = RoutedInvalid, why
		return
	}
	o, err := NormalizeObject(f.Resource)
	if err != nil {
		f.Routing, f.InvalidReason = RoutedInvalid, err.Error()
		return
	}
	if f.Cluster == "" {
		f.Routing, f.InvalidReason = RoutedInvalid, "the finding names no cluster"
		return
	}
	if o.Kind != "" && s.o.Objects != nil {
		exists, answered := s.o.Objects(ctx, f.Cluster, o)
		switch {
		case !answered:
			f.Unverified = true
		case !exists:
			f.Routing, f.InvalidReason = RoutedInvalid, fmt.Sprintf("there is no %s on the cluster %s", o, f.Cluster)
			return
		}
	}
	f.Key = &Key{Cluster: f.Cluster, Object: o.String(), Check: rules.Canon(f.Check)}
}

// Router routes a run's findings into cases (package casework, CF-01),
// and says the keys in use on a cluster, for the agents' tasks.
type Router interface {
	Route(ctx context.Context, in RouteInput) RouteResult
	KeysInUse(ctx context.Context, cluster string) []KeyInUse
}

// RouteInput is a run's findings to route, with the run.
type RouteInput struct {
	Run      string
	Job      string
	Kind     string
	Label    string
	Clusters []string
	// OK says the run ended well: only then does what it did not find
	// verify a fix.
	OK       bool
	Findings []Finding
	// Agent is the name of the job's agent, who the cases it opens name
	// as their creator; MinPriority the job's lowest priority, below which
	// a finding opens no case (CF-33, CF-35); nil routes every priority.
	Agent       string
	MinPriority *int
}

// RouteResult is what became of them.
type RouteResult struct {
	Findings  []Finding
	Routed    Routed
	Verified  []string
	NewChecks []string
}

// KeyInUse is a key the cluster's cases, known issues and exceptions
// hold, for the agent's task.
type KeyInUse struct {
	Key
	// What is "case C-12", "known issue KI-3" or "exception EX-4", with
	// its title.
	What  string
	Title string
}

// SetRouter gives the service its router.
func (s *Service) SetRouter(r Router) { s.o.Router = r }

// Keys is what a kind's task says about check ids on a cluster (CF-05,
// CF-10): the rules it is offered as they stand (ADR-138), other.<slug>,
// and the keys in use there to reuse for the same problem. Quoted, as
// data.
func (e *RunEnv) Keys(ctx context.Context, cluster string) string {
	if len(e.Kind.Checks) == 0 {
		return ""
	}
	offered := e.s.rules(ctx).Offered(e.Kind)
	var b strings.Builder
	b.WriteString("The validation rules: name each finding's check with one of these ids or, when none fits, other.<slug> (lower case, digits and dashes, at most 48 characters). A finding with any other check is refused.\n\n")
	var cat strings.Builder
	for _, c := range offered {
		fmt.Fprintf(&cat, "- %s: %s\n", c.ID, c.Says)
	}
	b.WriteString(Quote("check-ids", cat.String()))
	if e.s.o.Router != nil {
		if keys := e.s.o.Router.KeysInUse(ctx, cluster); len(keys) > 0 {
			var in strings.Builder
			for _, k := range keys {
				fmt.Fprintf(&in, "- %s on %s: %s (%s)\n", k.Check, orCluster(k.Object), clip(k.Title, 160), k.What)
			}
			b.WriteString("\n\nThe keys in use on this cluster: for the same problem again, use the same check on the same resource, so it joins what people already know. The console drops findings an exception covers.\n\n")
			b.WriteString(Quote("keys-in-use", in.String()))
		}
	}
	return b.String()
}

func orCluster(object string) string {
	if object == "" {
		return "the cluster as a whole"
	}
	return object
}
