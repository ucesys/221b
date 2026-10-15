package views

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
)

// The Alerts tab (P4-07): pods failing to start, pending and recently
// recovered, node problems and warning events, grouped by the object they
// concern. One row per object; a row exists only while the object has
// something to show.

// Alert kinds, the first part of a row's key: "pod:web/api-0".
const (
	AlertPod  = "pod"
	AlertNode = "node"
	AlertPVC  = "pvc"
)

// Severities.
const (
	SevFailed    = "failed"    // a pod that fails to start now
	SevPending   = "pending"   // a pod not running yet
	SevRecovered = "recovered" // a pod that runs now, after warnings
	SevError     = "error"     // a node or volume with errors
	SevWarning   = "warning"   // a node or volume with warnings
)

// RecoveredWindow is how long a pod that ran out of memory and runs again
// is listed as recovered. Warning events need no window of their own:
// Kubernetes keeps them for an hour.
const RecoveredWindow = time.Hour

// AlertEntry is one row of an alert's table: an event, or a condition or
// state of a node.
type AlertEntry struct {
	// Source is "event", "condition" or "state".
	Source  string     `json:"source"`
	Reason  string     `json:"reason"`
	Message string     `json:"message"`
	Tone    string     `json:"tone"`
	Count   int32      `json:"count"`
	First   *time.Time `json:"first,omitempty"`
	Last    *time.Time `json:"last,omitempty"`
	// ReportedBy is the component that reported it: kubelet,
	// default-scheduler, "Node status".
	ReportedBy string `json:"reportedBy,omitempty"`
	// Pod is a related pod, "namespace/name": the pod a volume's event
	// was reported for, or the pod a node's event names.
	Pod string `json:"pod,omitempty"`
}

// AlertProblem is one problem of a node, with its title and summary.
type AlertProblem struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

// AlertUser is a pod that mounts a volume.
type AlertUser struct {
	Pod    string `json:"pod"`
	Status string `json:"status"`
	Node   string `json:"node,omitempty"`
}

// AlertRow is one object with alerts.
type AlertRow struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	Status     string `json:"status"`
	StatusTone string `json:"statusTone"`

	// A pod's.
	Node     string     `json:"node,omitempty"`
	Release  string     `json:"release,omitempty"`
	Ready    int        `json:"ready,omitempty"`
	Total    int        `json:"total,omitempty"`
	Restarts int32      `json:"restarts,omitempty"`
	Started  *time.Time `json:"started,omitempty"`
	// LastReason, LastExit and LastMessage describe how a container last
	// stopped; LastAt when.
	LastReason  string     `json:"lastReason,omitempty"`
	LastExit    *int32     `json:"lastExit,omitempty"`
	LastMessage string     `json:"lastMessage,omitempty"`
	LastAt      *time.Time `json:"lastAt,omitempty"`
	// Waiting is why a container does not start (ImagePullBackOff) and
	// WaitingMessage what the kubelet says about it.
	Waiting        string `json:"waiting,omitempty"`
	WaitingMessage string `json:"waitingMessage,omitempty"`
	// Why says why a pending pod waits: the scheduler's message, or the
	// latest warning about it.
	Why string `json:"why,omitempty"`
	// OOM is true when a container was killed for running out of memory
	// in the last hour.
	OOM bool `json:"oom,omitempty"`

	// A node's.
	Role     string         `json:"role,omitempty"`
	IP       string         `json:"ip,omitempty"`
	Zone     string         `json:"zone,omitempty"`
	Pool     string         `json:"pool,omitempty"`
	Problems []AlertProblem `json:"problems,omitempty"`

	// A volume claim's.
	Volume       string      `json:"volume,omitempty"`
	StorageClass string      `json:"storageClass,omitempty"`
	Capacity     int64       `json:"capacity,omitempty"`
	Requested    int64       `json:"requested,omitempty"`
	Resizing     bool        `json:"resizing,omitempty"`
	AccessModes  []string    `json:"accessModes,omitempty"`
	Users        []AlertUser `json:"users,omitempty"`

	Events []AlertEntry `json:"events"`

	// Ack says who acknowledged the alert as it is now, if anyone did.
	Ack *AlertAck `json:"ack,omitempty"`
}

// AlertAck is an acknowledgement of an alert.
type AlertAck struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// AlertFingerprint is what an alert is, for acknowledgements: an
// acknowledgement lapses when it changes. It is the severity, the status
// and, for a node, its problems; not counts or times, which move while the
// alert stays the same.
func AlertFingerprint(r AlertRow) string {
	parts := []string{r.Severity, r.Status}
	titles := make([]string, 0, len(r.Problems))
	for _, p := range r.Problems {
		titles = append(titles, p.Severity+" "+p.Title)
	}
	sort.Strings(titles)
	return strings.Join(append(parts, titles...), "|")
}

// AlertKey is a row's key.
func AlertKey(kind, namespace, name string) string {
	if namespace == "" {
		return kind + ":" + name
	}
	return kind + ":" + namespace + "/" + name
}

// SplitAlertKey reads a row's key back: kind, namespace and name.
func SplitAlertKey(key string) (kind, namespace, name string, ok bool) {
	kind, rest, ok := strings.Cut(key, ":")
	if !ok || rest == "" {
		return "", "", "", false
	}
	if kind == AlertNode {
		return kind, "", rest, true
	}
	namespace, name, ok = strings.Cut(rest, "/")
	return kind, namespace, name, ok && namespace != "" && name != ""
}

// AlertKeys lists every object that could have alerts: each pod, node and
// volume claim.
func AlertKeys(src Source) []string {
	var out []string
	for _, o := range src.Objects("pods", "") {
		if p, ok := o.(*corev1.Pod); ok {
			out = append(out, AlertKey(AlertPod, p.Namespace, p.Name))
		}
	}
	for _, o := range src.Objects("nodes", "") {
		if n, ok := o.(*corev1.Node); ok {
			out = append(out, AlertKey(AlertNode, "", n.Name))
		}
	}
	for _, o := range src.Objects("persistentvolumeclaims", "") {
		if c, ok := o.(*corev1.PersistentVolumeClaim); ok {
			out = append(out, AlertKey(AlertPVC, c.Namespace, c.Name))
		}
	}
	return out
}

// BuildAlert computes an object's alert; ok is false when it has none.
func BuildAlert(src Source, key string, now time.Time) (AlertRow, bool) {
	kind, ns, name, ok := SplitAlertKey(key)
	if !ok {
		return AlertRow{}, false
	}
	switch kind {
	case AlertPod:
		p, _ := src.Object("pods", ns, name).(*corev1.Pod)
		if p == nil {
			return AlertRow{}, false
		}
		return podAlert(src, p, now)
	case AlertNode:
		n, _ := src.Object("nodes", "", name).(*corev1.Node)
		if n == nil {
			return AlertRow{}, false
		}
		return nodeAlert(src, n, now)
	case AlertPVC:
		c, _ := src.Object("persistentvolumeclaims", ns, name).(*corev1.PersistentVolumeClaim)
		if c == nil {
			return AlertRow{}, false
		}
		return claimAlert(src, c, now)
	}
	return AlertRow{}, false
}

// warnings lists the warning events about an object, newest first. Events
// about nodes may name the node by its name rather than its UID, as the
// kubelet does.
func warnings(src Source, uids ...string) []AlertEntry {
	out := []AlertEntry{}
	seen := map[string]bool{}
	for _, uid := range uids {
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		for _, o := range src.ByIndex("events", kube.IndexEventObject, uid) {
			e, ok := o.(*corev1.Event)
			if !ok || e.Type != corev1.EventTypeWarning {
				continue
			}
			out = append(out, eventEntry(e))
		}
	}
	sortEntries(out)
	return out
}

func eventEntry(e *corev1.Event) AlertEntry {
	ev := eventOf(e)
	return AlertEntry{Source: "event", Reason: ev.Reason, Message: ev.Message, Tone: "warn", Count: ev.Count, First: ev.First, Last: ev.Last, ReportedBy: ev.Source}
}

// sortEntries orders entries newest first.
func sortEntries(es []AlertEntry) {
	at := func(e AlertEntry) time.Time {
		if e.Last != nil {
			return *e.Last
		}
		if e.First != nil {
			return *e.First
		}
		return time.Time{}
	}
	sort.SliceStable(es, func(i, j int) bool { return at(es[i]).After(at(es[j])) })
}

func podAlert(src Source, p *corev1.Pod, now time.Time) (AlertRow, bool) {
	row := BuildPodRow(src, p, now)
	if row.Done || p.DeletionTimestamp != nil {
		return AlertRow{}, false
	}
	events := warnings(src, string(p.UID))
	sev := ""
	switch {
	case row.Tone == "err":
		sev = SevFailed
	case p.Status.Phase == corev1.PodPending:
		sev = SevPending
	}
	a := AlertRow{
		Key: AlertKey(AlertPod, p.Namespace, p.Name), Kind: AlertPod, Name: p.Name, Namespace: p.Namespace,
		Status: row.Status, StatusTone: row.Tone, Node: p.Spec.NodeName, Release: row.Release,
		Ready: row.Ready, Total: row.Total, Restarts: row.Restarts, Started: row.Started, Events: events,
	}
	if a.Started == nil {
		t := p.CreationTimestamp.Time.UTC()
		a.Started = &t
	}
	lastTermination(p, &a)
	if a.LastReason == "OOMKilled" && a.LastAt != nil && now.Sub(*a.LastAt) < RecoveredWindow {
		a.OOM = true
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" && w.Reason != "PodInitializing" {
			a.Waiting, a.WaitingMessage = w.Reason, strings.TrimSpace(w.Message)
			break
		}
	}
	if sev == "" && (len(events) > 0 || a.OOM) {
		sev = SevRecovered
	}
	if sev == "" {
		return AlertRow{}, false
	}
	a.Severity = sev
	if sev == SevPending {
		a.Why = pendingWhy(p, events)
	}
	return a, true
}

// lastTermination finds how a container of the pod last stopped.
func lastTermination(p *corev1.Pod, a *AlertRow) {
	var best *corev1.ContainerStateTerminated
	for _, cs := range p.Status.ContainerStatuses {
		for _, t := range []*corev1.ContainerStateTerminated{cs.State.Terminated, cs.LastTerminationState.Terminated} {
			if t == nil {
				continue
			}
			if best == nil || t.FinishedAt.After(best.FinishedAt.Time) {
				best = t
			}
		}
	}
	if best == nil {
		return
	}
	a.LastReason = best.Reason
	if a.LastReason == "" {
		a.LastReason = "Error"
		if best.ExitCode == 0 {
			a.LastReason = "Completed"
		}
	}
	code := best.ExitCode
	a.LastExit = &code
	a.LastMessage = strings.TrimSpace(best.Message)
	if len(a.LastMessage) > 2048 {
		a.LastMessage = a.LastMessage[len(a.LastMessage)-2048:]
	}
	if !best.FinishedAt.IsZero() {
		t := best.FinishedAt.Time.UTC()
		a.LastAt = &t
	}
}

// pendingWhy says why a pending pod waits: the scheduler's verdict for one
// with no node, otherwise the latest warning, otherwise what its
// containers wait for.
func pendingWhy(p *corev1.Pod, events []AlertEntry) string {
	if p.Spec.NodeName == "" {
		if c := podCondition(p, corev1.PodScheduled); c != nil && c.Status != corev1.ConditionTrue && c.Message != "" {
			return c.Message
		}
	}
	if len(events) > 0 {
		return events[0].Message
	}
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && strings.TrimSpace(w.Message) != "" {
			return strings.TrimSpace(w.Message)
		}
	}
	if p.Spec.NodeName == "" {
		return "The scheduler has not placed it yet."
	}
	return "Its containers are being created on " + p.Spec.NodeName + "."
}

// poolKeys are labels that name a node's pool, in order of preference.
var poolKeys = []string{
	"eks.amazonaws.com/nodegroup", "alpha.eksctl.io/nodegroup-name", "cloud.google.com/gke-nodepool",
	"kubernetes.azure.com/agentpool", "agentpool", "karpenter.sh/nodepool", "node.kubernetes.io/pool",
	"nodepool", "node-pool", "pool", "workload",
}

// NodePool names a node's pool from its labels, or "".
func NodePool(labels map[string]string) string {
	for _, k := range poolKeys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

// IsControlPlane says whether a node is a control-plane node.
func IsControlPlane(labels map[string]string) bool {
	_, cp := labels["node-role.kubernetes.io/control-plane"]
	_, master := labels["node-role.kubernetes.io/master"]
	return cp || master
}

func nodeAlert(src Source, n *corev1.Node, now time.Time) (AlertRow, bool) {
	var pods []*corev1.Pod
	for _, o := range src.ByIndex("pods", kube.IndexPodNode, n.Name) {
		if p, ok := o.(*corev1.Pod); ok {
			pods = append(pods, p)
		}
	}
	nr := nodes.BuildRow(n, pods, nil, nil, now)
	events := warnings(src, string(n.UID), n.Name)
	a := AlertRow{
		Key: AlertKey(AlertNode, "", n.Name), Kind: AlertNode, Name: n.Name,
		Status: nr.Status, StatusTone: nr.Tone, IP: nr.InternalIP, Zone: nr.Zone, Pool: NodePool(n.Labels), Role: "worker",
	}
	if IsControlPlane(n.Labels) {
		a.Role = "control-plane"
	}
	sev := ""
	for _, p := range nr.Problems {
		if p.Pods {
			continue
		}
		a.Problems = append(a.Problems, AlertProblem{Title: p.Title, Severity: p.Severity, Summary: p.Summary})
		if p.Severity == nodes.SeverityError {
			sev = SevError
		} else if sev == "" {
			sev = SevWarning
		}
		tone := "warn"
		if p.Severity == nodes.SeverityError {
			tone = "err"
		}
		for _, e := range p.Entries {
			a.Events = append(a.Events, AlertEntry{Source: "condition", Reason: e.Reason, Message: e.Message, Tone: tone, Count: 1, First: e.Since, Last: e.Since, ReportedBy: "Node status"})
		}
		if p.Title == "Cordoned" {
			a.Events = append(a.Events, AlertEntry{Source: "state", Reason: "Unschedulable", Message: "The node is cordoned: new pods are not scheduled here.", Tone: tone, Count: 1, ReportedBy: "Node spec", First: cordonedAt(n), Last: cordonedAt(n)})
		}
	}
	if len(events) > 0 && sev == "" {
		sev = SevWarning
		a.Problems = append(a.Problems, AlertProblem{Title: countText(len(events), "warning event", "warning events"), Severity: nodes.SeverityWarning, Summary: "Kubernetes reported " + strings.ToLower(events[0].Reason) + " and other warnings about this node in the last hour."})
	}
	if sev == "" {
		return AlertRow{}, false
	}
	// A kernel or kubelet event that names a pod links to it.
	for i := range events {
		events[i].Pod = podNamed(src, events[i].Message)
	}
	a.Events = append(a.Events, events...)
	sortEntries(a.Events)
	a.Severity = sev
	return a, true
}

// cordonedAt is when a node was cordoned, if its taint says.
func cordonedAt(n *corev1.Node) *time.Time {
	for _, t := range n.Spec.Taints {
		if t.Key == corev1.TaintNodeUnschedulable && t.TimeAdded != nil {
			at := t.TimeAdded.Time.UTC()
			return &at
		}
	}
	return nil
}

var podRef = regexp.MustCompile(`(?:pod|Pod) "?([a-z0-9][a-z0-9.-]*)/([a-z0-9][a-z0-9.-]*)"?`)

// podNamed finds "pod ns/name" in a message and returns it when the pod
// exists.
func podNamed(src Source, msg string) string {
	m := podRef.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	if src.Object("pods", m[1], m[2]) == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

func countText(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// attachMount matches the reasons of events about attaching and mounting
// volumes, which the kubelet and the attach-detach controller report for
// the pod rather than the claim.
var attachMount = regexp.MustCompile(`Attach|Mount`)

// ClaimsOf names the volume claims a pod mounts.
func ClaimsOf(p *corev1.Pod) []string {
	var out []string
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			out = append(out, v.PersistentVolumeClaim.ClaimName)
		}
	}
	return out
}

func claimAlert(src Source, c *corev1.PersistentVolumeClaim, now time.Time) (AlertRow, bool) {
	events := warnings(src, string(c.UID))
	pv := c.Spec.VolumeName
	a := AlertRow{Key: AlertKey(AlertPVC, c.Namespace, c.Name), Kind: AlertPVC, Name: c.Name, Namespace: c.Namespace, Volume: pv, Release: objectRelease(c)}
	if c.Spec.StorageClassName != nil {
		a.StorageClass = *c.Spec.StorageClassName
	}
	if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
		a.Capacity = q.Value()
	}
	if q, ok := c.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		a.Requested = q.Value()
	}
	for _, m := range c.Spec.AccessModes {
		a.AccessModes = append(a.AccessModes, string(m))
	}
	for _, cond := range c.Status.Conditions {
		if (cond.Type == corev1.PersistentVolumeClaimResizing || cond.Type == corev1.PersistentVolumeClaimFileSystemResizePending) && cond.Status == corev1.ConditionTrue {
			a.Resizing = true
		}
	}
	if a.Capacity > 0 && a.Requested > a.Capacity && c.Status.Phase == corev1.ClaimBound {
		a.Resizing = true
	}
	a.Status, a.StatusTone = string(c.Status.Phase), "ok"
	switch {
	case a.Resizing:
		a.Status, a.StatusTone = "Resizing", "warn"
	case c.Status.Phase == corev1.ClaimPending:
		a.StatusTone = "warn"
	case c.Status.Phase == corev1.ClaimLost:
		a.StatusTone = "err"
	}
	// The pods that mount it, and what was reported for them about
	// attaching or mounting this volume.
	for _, o := range src.Objects("pods", c.Namespace) {
		p, ok := o.(*corev1.Pod)
		if !ok {
			continue
		}
		uses := false
		for _, name := range ClaimsOf(p) {
			if name == c.Name {
				uses = true
			}
		}
		if !uses {
			continue
		}
		status, _, _ := podStatus(p, now)
		a.Users = append(a.Users, AlertUser{Pod: p.Name, Status: status, Node: p.Spec.NodeName})
		for _, e := range warnings(src, string(p.UID)) {
			if !attachMount.MatchString(e.Reason) {
				continue
			}
			if (pv != "" && strings.Contains(e.Message, pv)) || strings.Contains(e.Message, "["+c.Name+"]") || strings.Contains(e.Message, `"`+c.Name+`"`) || strings.Contains(e.Message, c.Name) {
				e.Pod = p.Namespace + "/" + p.Name
				events = append(events, e)
			}
		}
	}
	sort.Slice(a.Users, func(i, j int) bool { return a.Users[i].Pod < a.Users[j].Pod })
	if len(events) == 0 && c.Status.Phase != corev1.ClaimLost {
		return AlertRow{}, false
	}
	sortEntries(events)
	a.Events = events
	a.Severity = SevWarning
	if c.Status.Phase == corev1.ClaimLost {
		a.Severity = SevError
	}
	return a, true
}
