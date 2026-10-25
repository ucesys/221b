package views

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/ucesys/221b/server/internal/kube"
)

// The Storage tab (ST-01 to ST-25): storage classes, volumes, the claims
// that hold them and the pods that mount those, as one tree. The view's
// rows are the parts the browser groups: a class, a claim with its volume
// (or none yet), a volume no claim holds, and a namespace's storage quota.
// Each row stands for one Kubernetes resource, so the person's filter
// decides it alone (ST-20): a claim row is persistentvolumeclaims in its
// namespace, a volume row persistentvolumes and a class row storageclasses,
// both cluster-wide.

// Storage row kinds, the first part of a row's key.
const (
	StClass  = "sc"    // "sc:longhorn"
	StClaim  = "pvc"   // "pvc:analytics/data-cache-2"
	StVolume = "pv"    // "pv:archive-tapes-2024"
	StQuota  = "quota" // "quota:analytics"
)

// A pair's state (ST-05).
const (
	StateBound     = "bound"
	StatePending   = "pending"
	StateLost      = "lost"
	StateReleased  = "released"
	StateAvailable = "available"
	StateFailed    = "failed"
)

// Usage thresholds (ST-08): amber from NearlyFull, red from Full. Constants
// in the first version, as the requirements' open question answers.
const (
	NearlyFull = 85.0
	Full       = 95.0
)

// The default-class annotations, GA and beta.
const (
	defaultClassAnnotation     = "storageclass.kubernetes.io/is-default-class"
	betaDefaultClassAnnotation = "storageclass.beta.kubernetes.io/is-default-class"
)

// StorageRow is one row of the storage view; exactly one of Class, Pair and
// Quota is set, as Kind says.
type StorageRow struct {
	Key   string        `json:"key"`
	Kind  string        `json:"kind"`
	Class *StorageClass `json:"class,omitempty"`
	Pair  *StoragePair  `json:"pair,omitempty"`
	Quota *StorageQuota `json:"quota,omitempty"`
}

// StorageClass is a class as its group header and drawer show it (ST-03,
// ST-17).
type StorageClass struct {
	Name        string `json:"name"`
	Provisioner string `json:"provisioner"`
	// Reclaim is Delete or Retain; Binding Immediate or WaitForFirstConsumer.
	Reclaim      string            `json:"reclaim"`
	Binding      string            `json:"binding"`
	Expansion    bool              `json:"expansion"`
	Default      bool              `json:"default"`
	Parameters   map[string]string `json:"parameters,omitempty"`
	MountOptions []string          `json:"mountOptions,omitempty"`
	// Topologies is each allowed topology term as "key in (a, b)".
	Topologies []string          `json:"topologies,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Created    time.Time         `json:"created"`
}

// StoragePair is a volume and its claim, one row of the tree (ST-05): a
// bound pair, a claim with no volume yet, a volume no claim holds, or a
// claim whose volume is gone.
type StoragePair struct {
	State     string `json:"state"`
	Namespace string `json:"namespace,omitempty"`
	Claim     string `json:"claim,omitempty"`
	Volume    string `json:"volume,omitempty"`
	// Class is the claim's spec.storageClassName, or the volume's when no
	// claim names one; "" for none. ClassMissing is a class name no class
	// in the cluster has.
	Class        string `json:"class"`
	ClassMissing bool   `json:"classMissing,omitempty"`
	// Phase is what the Status pill says; Tone its colour.
	Phase string     `json:"phase"`
	Tone  string     `json:"tone"`
	Since *time.Time `json:"since,omitempty"`
	// Reason is the one-line diagnosis under the pill (ST-14); nil while
	// the row is healthy. Attention puts the row under Needs attention
	// (ST-09).
	Reason    *Diagnosis `json:"reason,omitempty"`
	Attention bool       `json:"attention,omitempty"`
	// Capacity is the volume's spec.capacity, or the claim's
	// status.capacity; Requested the claim's request.
	Capacity    int64    `json:"capacity,omitempty"`
	Requested   int64    `json:"requested,omitempty"`
	AccessModes []string `json:"accessModes,omitempty"`
	VolumeMode  string   `json:"volumeMode,omitempty"`
	// What only the volume says; empty for someone who may not read
	// volumes (ST-11).
	Reclaim      string            `json:"reclaim,omitempty"`
	Source       *VolumeSource     `json:"source,omitempty"`
	WasClaim     string            `json:"wasClaim,omitempty"`
	VolumeLabels map[string]string `json:"volumeLabels,omitempty"`
	Attached     []string          `json:"attached,omitempty"`
	// What the claim says.
	ClaimLabels map[string]string `json:"claimLabels,omitempty"`
	Release     string            `json:"release,omitempty"`
	Owner       *ClaimOwner       `json:"owner,omitempty"`
	Resizing    bool              `json:"resizing,omitempty"`
	Pods        []StoragePod      `json:"pods,omitempty"`
	// Nodes are the nodes its pods run on, sorted.
	Nodes   []string     `json:"nodes,omitempty"`
	Usage   *VolumeUsage `json:"usage,omitempty"`
	Created time.Time    `json:"created"`
}

// Diagnosis is the reason line under a row's status, and the cause of the
// first broken hop of its chain (ST-14). Tone is err, warn or info.
type Diagnosis struct {
	Text string `json:"text"`
	Tone string `json:"tone"`
}

// VolumeSource says where a volume's data lives.
type VolumeSource struct {
	// Kind is csi, nfs, local, hostPath or other.
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Driver string `json:"driver,omitempty"`
	Handle string `json:"handle,omitempty"`
	// FSType is the filesystem the volume says it carries.
	FSType string `json:"fsType,omitempty"`
	// Node is the one node a local volume lives on (its nodeAffinity).
	Node string `json:"node,omitempty"`
}

// ClaimOwner is the workload a claim comes from: its controller, or the
// StatefulSet whose volumeClaimTemplate names it.
type ClaimOwner struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Template string `json:"template,omitempty"`
	// Ordinal is the pod number a StatefulSet's claim belongs to; -1 for
	// none.
	Ordinal  int   `json:"ordinal"`
	Replicas int32 `json:"replicas,omitempty"`
}

// StoragePod is a pod that mounts a claim (ST-07).
type StoragePod struct {
	Name      string     `json:"name"`
	Node      string     `json:"node,omitempty"`
	Status    string     `json:"status"`
	Tone      string     `json:"tone"`
	MountPath string     `json:"mountPath,omitempty"`
	ReadOnly  bool       `json:"readOnly,omitempty"`
	SubPath   string     `json:"subPath,omitempty"`
	Container string     `json:"container,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	// Note says why a pod that is not running waits: "Pending, waits for
	// the volume", "Unschedulable for 19d".
	Note string `json:"note,omitempty"`
	// Why is the scheduler's message for a pod with no node.
	Why string `json:"why,omitempty"`
}

// VolumeUsage is what the kubelet reports about a mounted volume (ST-08).
type VolumeUsage struct {
	Used       int64   `json:"used"`
	Capacity   int64   `json:"capacity"`
	Available  int64   `json:"available"`
	InodesUsed int64   `json:"inodesUsed,omitempty"`
	Inodes     int64   `json:"inodes,omitempty"`
	Pct        float64 `json:"pct"`
	Node       string  `json:"node,omitempty"`
}

// StorageQuota is a namespace's ResourceQuota on storage (ST-11).
type StorageQuota struct {
	Namespace string `json:"namespace"`
	// Name is the quota object's name; with several, the first that
	// limits requests.storage.
	Name string `json:"name"`
	Hard int64  `json:"hard,omitempty"`
	Used int64  `json:"used,omitempty"`
	// Classes are the per-class limits,
	// <class>.storageclass.storage.k8s.io/requests.storage.
	Classes map[string]QuotaUse `json:"classes,omitempty"`
}

// QuotaUse is a quota's hard limit and what is used of it.
type QuotaUse struct {
	Hard int64 `json:"hard"`
	Used int64 `json:"used"`
}

// StorageKey is a row's key.
func StorageKey(kind, namespace, name string) string {
	switch {
	case namespace == "":
		return kind + ":" + name
	case name == "":
		return kind + ":" + namespace
	}
	return kind + ":" + namespace + "/" + name
}

// SplitStorageKey reads a row's key back.
func SplitStorageKey(key string) (kind, namespace, name string, ok bool) {
	kind, rest, ok := strings.Cut(key, ":")
	if !ok || rest == "" {
		return "", "", "", false
	}
	switch kind {
	case StClass, StVolume:
		return kind, "", rest, true
	case StQuota:
		return kind, rest, "", true
	case StClaim:
		namespace, name, ok = strings.Cut(rest, "/")
		return kind, namespace, name, ok && namespace != "" && name != ""
	}
	return "", "", "", false
}

// volumeStatser is a source that knows what the kubelets report about
// volumes (ST-08). The cluster cache is one; a test source may be.
type volumeStatser interface {
	VolumeStats(namespace, claim string) (kube.VolumeStats, bool)
}

// storageIndex is everything the rows are made from, read once per build.
type storageIndex struct {
	classes      map[string]*storagev1.StorageClass
	classesKnown bool
	volumes      map[string]*corev1.PersistentVolume
	// held names the volumes a listed claim holds.
	held     map[string]bool
	attached map[string][]string // volume -> nodes it is attached to
	// mounts lists the pods that mount each claim, "ns/claim".
	mounts map[string][]*corev1.Pod
	stats  volumeStatser
	now    time.Time
	src    Source
}

func newStorageIndex(src Source, now time.Time) *storageIndex {
	ix := &storageIndex{classes: map[string]*storagev1.StorageClass{}, volumes: map[string]*corev1.PersistentVolume{}, held: map[string]bool{}, attached: map[string][]string{}, mounts: map[string][]*corev1.Pod{}, now: now, src: src}
	for _, k := range src.Kinds() {
		if k.Name == "storageclasses" {
			ix.classesKnown = true
		}
	}
	for _, o := range src.Objects("storageclasses", "") {
		if c, ok := o.(*storagev1.StorageClass); ok {
			ix.classes[c.Name] = c
		}
	}
	// With no class at all, the classes may be unreadable rather than
	// absent: no claim is said to name a missing one.
	if len(ix.classes) == 0 {
		ix.classesKnown = false
	}
	for _, o := range src.Objects("persistentvolumes", "") {
		if v, ok := o.(*corev1.PersistentVolume); ok {
			ix.volumes[v.Name] = v
		}
	}
	for _, o := range src.Objects("volumeattachments", "") {
		a, ok := o.(*storagev1.VolumeAttachment)
		if !ok || a.Spec.Source.PersistentVolumeName == nil || !a.Status.Attached {
			continue
		}
		pv := *a.Spec.Source.PersistentVolumeName
		ix.attached[pv] = append(ix.attached[pv], a.Spec.NodeName)
	}
	for _, o := range src.Objects("pods", "") {
		p, ok := o.(*corev1.Pod)
		if !ok || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, claim := range ClaimsOf(p) {
			k := p.Namespace + "/" + claim
			ix.mounts[k] = append(ix.mounts[k], p)
		}
	}
	// Running pods first, then those that wait, each by name (ST-07).
	for k := range ix.mounts {
		ps := ix.mounts[k]
		sort.Slice(ps, func(i, j int) bool {
			ri, rj := ps[i].Status.Phase == corev1.PodRunning, ps[j].Status.Phase == corev1.PodRunning
			if ri != rj {
				return ri
			}
			return ps[i].Name < ps[j].Name
		})
	}
	ix.stats, _ = src.(volumeStatser)
	return ix
}

// BuildStorage builds every row of the storage view.
func BuildStorage(src Source, now time.Time) map[string]StorageRow {
	ix := newStorageIndex(src, now)
	out := map[string]StorageRow{}
	for _, c := range ix.classes {
		info := classInfo(c)
		out[StorageKey(StClass, "", c.Name)] = StorageRow{Key: StorageKey(StClass, "", c.Name), Kind: StClass, Class: &info}
	}
	for _, o := range src.Objects("persistentvolumeclaims", "") {
		c, ok := o.(*corev1.PersistentVolumeClaim)
		if !ok {
			continue
		}
		p := ix.claimPair(c)
		if p.Volume != "" && ix.volumes[p.Volume] != nil {
			ix.held[p.Volume] = true
		}
		k := StorageKey(StClaim, c.Namespace, c.Name)
		out[k] = StorageRow{Key: k, Kind: StClaim, Pair: &p}
	}
	for name, v := range ix.volumes {
		if ix.held[name] {
			continue
		}
		p := ix.volumePair(v)
		k := StorageKey(StVolume, "", name)
		out[k] = StorageRow{Key: k, Kind: StVolume, Pair: &p}
	}
	for ns, q := range storageQuotas(src) {
		k := StorageKey(StQuota, ns, "")
		out[k] = StorageRow{Key: k, Kind: StQuota, Quota: q}
	}
	return out
}

// StorageClaim builds one claim's pair, for the drawer and Sherlock.
func StorageClaim(src Source, namespace, name string, now time.Time) (StoragePair, bool) {
	c, _ := src.Object("persistentvolumeclaims", namespace, name).(*corev1.PersistentVolumeClaim)
	if c == nil {
		return StoragePair{}, false
	}
	return newStorageIndex(src, now).claimPair(c), true
}

// StorageVolume builds the pair of a volume no claim holds.
func StorageVolume(src Source, name string, now time.Time) (StoragePair, bool) {
	v, _ := src.Object("persistentvolumes", "", name).(*corev1.PersistentVolume)
	if v == nil {
		return StoragePair{}, false
	}
	return newStorageIndex(src, now).volumePair(v), true
}

// IsDefaultClass says whether a class carries the default annotation, GA
// or beta (ST-03).
func IsDefaultClass(c *storagev1.StorageClass) bool {
	return c.Annotations[defaultClassAnnotation] == "true" || c.Annotations[betaDefaultClassAnnotation] == "true"
}

func classInfo(c *storagev1.StorageClass) StorageClass {
	info := StorageClass{Name: c.Name, Provisioner: c.Provisioner, Reclaim: "Delete", Binding: string(storagev1.VolumeBindingImmediate), Default: IsDefaultClass(c), Parameters: c.Parameters, MountOptions: c.MountOptions, Labels: c.Labels, Created: c.CreationTimestamp.Time.UTC()}
	if c.ReclaimPolicy != nil {
		info.Reclaim = string(*c.ReclaimPolicy)
	}
	if c.VolumeBindingMode != nil {
		info.Binding = string(*c.VolumeBindingMode)
	}
	if c.AllowVolumeExpansion != nil {
		info.Expansion = *c.AllowVolumeExpansion
	}
	for _, t := range c.AllowedTopologies {
		var parts []string
		for _, e := range t.MatchLabelExpressions {
			parts = append(parts, e.Key+" in ("+strings.Join(e.Values, ", ")+")")
		}
		info.Topologies = append(info.Topologies, strings.Join(parts, " and "))
	}
	return info
}

func quantity(l corev1.ResourceList) int64 {
	if q, ok := l[corev1.ResourceStorage]; ok {
		return q.Value()
	}
	return 0
}

func accessModes(ms []corev1.PersistentVolumeAccessMode) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, string(m))
	}
	return out
}

// claimPair builds the row of a claim, with its volume when it has one.
func (ix *storageIndex) claimPair(c *corev1.PersistentVolumeClaim) StoragePair {
	p := StoragePair{Namespace: c.Namespace, Claim: c.Name, Volume: c.Spec.VolumeName, ClaimLabels: c.Labels, Release: objectRelease(c), Created: c.CreationTimestamp.Time.UTC()}
	if c.Spec.StorageClassName != nil {
		p.Class = *c.Spec.StorageClassName
	}
	p.Requested = quantity(c.Spec.Resources.Requests)
	p.Capacity = quantity(c.Status.Capacity)
	p.AccessModes = accessModes(c.Spec.AccessModes)
	if len(c.Status.AccessModes) > 0 {
		p.AccessModes = accessModes(c.Status.AccessModes)
	}
	if c.Spec.VolumeMode != nil {
		p.VolumeMode = string(*c.Spec.VolumeMode)
	}
	pv := ix.volumes[c.Spec.VolumeName]
	if pv != nil {
		ix.addVolume(&p, pv)
		if p.Class == "" {
			p.Class = pv.Spec.StorageClassName
		}
	}
	cls := ix.classes[p.Class]
	if p.Class != "" && cls == nil && ix.classesKnown {
		p.ClassMissing = true
	}
	p.Owner = claimOwner(ix.src, c)
	for _, cond := range c.Status.Conditions {
		if (cond.Type == corev1.PersistentVolumeClaimResizing || cond.Type == corev1.PersistentVolumeClaimFileSystemResizePending) && cond.Status == corev1.ConditionTrue {
			p.Resizing = true
		}
	}
	if p.Capacity > 0 && p.Requested > p.Capacity && c.Status.Phase == corev1.ClaimBound {
		p.Resizing = true
	}
	pods := ix.mounts[c.Namespace+"/"+c.Name]
	for _, pod := range pods {
		p.Pods = append(p.Pods, ix.storagePod(pod, c, cls))
	}
	p.Nodes = podNodes(p.Pods)
	switch c.Status.Phase {
	case corev1.ClaimBound:
		p.State, p.Phase, p.Tone = StateBound, "Bound", "ok"
	case corev1.ClaimLost:
		p.State, p.Phase, p.Tone = StateLost, "Lost", "err"
	default:
		p.State, p.Phase, p.Tone = StatePending, "Pending", "warn"
		since := c.CreationTimestamp.Time.UTC()
		p.Since = &since
	}
	if p.Resizing {
		p.Phase, p.Tone = "Resizing", "warn"
	}
	events := eventsFor(ix.src, string(c.UID))
	p.Reason, p.Attention = diagnoseClaim(c, pv, cls, p.ClassMissing, events, pods, ix.now)
	// ADR-64: a claim the Alerts tab raises needs attention here too. Its
	// rule also counts the attach and mount warnings of the pods that mount
	// the claim; a Bound claim with no reason line and whose pods are well
	// is never raised, so only the others are asked about.
	if !p.Attention && (p.State != StateBound || p.Resizing || p.Reason != nil || anyUnwell(pods, ix.now)) {
		if a, ok := claimAlert(ix.src, c, ix.now); ok && a.Severity != SevRecovered {
			p.Attention = true
			if p.Reason == nil && len(a.Events) > 0 {
				e := a.Events[0]
				text := e.Reason
				if e.Pod != "" {
					text = e.Pod[strings.Index(e.Pod, "/")+1:] + " · " + e.Reason
				}
				p.Reason = &Diagnosis{Text: text + " · " + shortCause(e.Message), Tone: "warn"}
			}
		}
	}
	if p.Reason != nil && p.Reason.Tone == "info" && p.State == StatePending {
		p.Tone = "info"
	}
	// Usage, while a running pod mounts it (ST-08).
	if p.State == StateBound && p.VolumeMode != string(corev1.PersistentVolumeBlock) && ix.stats != nil && anyRunning(pods) {
		if st, ok := ix.stats.VolumeStats(c.Namespace, c.Name); ok && st.CapacityBytes > 0 {
			u := VolumeUsage{Used: st.UsedBytes, Capacity: st.CapacityBytes, Available: st.AvailableBytes, InodesUsed: st.InodesUsed, Inodes: st.Inodes, Node: st.Node}
			u.Pct = float64(st.UsedBytes) * 100 / float64(st.CapacityBytes)
			p.Usage = &u
			if u.Pct >= NearlyFull {
				p.Attention = true
			}
		}
	}
	return p
}

// volumePair builds the row of a volume no claim holds: Released,
// Available or Failed.
func (ix *storageIndex) volumePair(v *corev1.PersistentVolume) StoragePair {
	p := StoragePair{Volume: v.Name, Class: v.Spec.StorageClassName, AccessModes: accessModes(v.Spec.AccessModes), Created: v.CreationTimestamp.Time.UTC()}
	if v.Spec.VolumeMode != nil {
		p.VolumeMode = string(*v.Spec.VolumeMode)
	}
	ix.addVolume(&p, v)
	if p.Class != "" && ix.classes[p.Class] == nil && ix.classesKnown {
		p.ClassMissing = true
	}
	since := v.CreationTimestamp.Time.UTC()
	if v.Status.LastPhaseTransitionTime != nil {
		since = v.Status.LastPhaseTransitionTime.Time.UTC()
	}
	switch v.Status.Phase {
	case corev1.VolumeReleased:
		p.State, p.Phase, p.Tone, p.Since, p.Attention = StateReleased, "Released", "warn", &since, true
		text := "claim deleted " + dur(ix.now.Sub(since)) + " ago · data kept (Retain)"
		if p.Reclaim != string(corev1.PersistentVolumeReclaimRetain) {
			text = "claim deleted " + dur(ix.now.Sub(since)) + " ago · being deleted"
		}
		p.Reason = &Diagnosis{Text: text, Tone: "warn"}
	case corev1.VolumeFailed:
		p.State, p.Phase, p.Tone, p.Since, p.Attention = StateFailed, "Failed", "err", &since, true
		msg := strings.TrimSpace(v.Status.Message)
		if msg == "" {
			msg = "the volume failed"
		}
		p.Reason = &Diagnosis{Text: shortCause(msg), Tone: "err"}
	case corev1.VolumeBound:
		// Bound to a claim the cache does not have: deleted a moment ago.
		p.State, p.Phase, p.Tone = StateBound, "Bound", "ok"
	default:
		p.State, p.Phase, p.Tone = StateAvailable, "Available", "info"
		p.Reason = &Diagnosis{Text: "waiting for a claim", Tone: "muted"}
	}
	return p
}

// addVolume fills in what only the volume says.
func (ix *storageIndex) addVolume(p *StoragePair, v *corev1.PersistentVolume) {
	p.Volume = v.Name
	p.Reclaim = string(v.Spec.PersistentVolumeReclaimPolicy)
	if c := quantity(v.Spec.Capacity); c > 0 {
		p.Capacity = c
	}
	p.VolumeLabels = v.Labels
	p.Source = pvSource(v)
	if ref := v.Spec.ClaimRef; ref != nil && p.Claim == "" {
		p.WasClaim = ref.Namespace + "/" + ref.Name
	}
	if p.VolumeMode == "" && v.Spec.VolumeMode != nil {
		p.VolumeMode = string(*v.Spec.VolumeMode)
	}
	p.Attached = append([]string(nil), ix.attached[v.Name]...)
	sort.Strings(p.Attached)
}

// pvSource says where a volume's data lives, as the Volume column's
// second line and the Source column show it (ST-06).
func pvSource(v *corev1.PersistentVolume) *VolumeSource {
	s := v.Spec.PersistentVolumeSource
	switch {
	case s.CSI != nil:
		return &VolumeSource{Kind: "csi", Driver: s.CSI.Driver, Handle: s.CSI.VolumeHandle, FSType: s.CSI.FSType, Text: "csi " + s.CSI.Driver + " " + s.CSI.VolumeHandle}
	case s.NFS != nil:
		return &VolumeSource{Kind: "nfs", Text: "nfs " + s.NFS.Server + ":" + s.NFS.Path}
	case s.Local != nil:
		vs := &VolumeSource{Kind: "local", Text: "local " + s.Local.Path, Node: affinityNode(v)}
		if s.Local.FSType != nil {
			vs.FSType = *s.Local.FSType
		}
		return vs
	case s.HostPath != nil:
		return &VolumeSource{Kind: "hostPath", Text: "hostPath " + s.HostPath.Path, Node: affinityNode(v)}
	}
	return &VolumeSource{Kind: "other", Text: "other"}
}

// affinityNode is the one node a volume's nodeAffinity allows, if it
// names exactly one by hostname.
func affinityNode(v *corev1.PersistentVolume) string {
	if v.Spec.NodeAffinity == nil || v.Spec.NodeAffinity.Required == nil {
		return ""
	}
	for _, t := range v.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, e := range t.MatchExpressions {
			if e.Key == corev1.LabelHostname && len(e.Values) == 1 {
				return e.Values[0]
			}
		}
	}
	return ""
}

// claimOwner finds the workload a claim belongs to: its controlling owner,
// or the StatefulSet whose volumeClaimTemplate makes claims of its name
// (<template>-<set>-<ordinal>), which owns them only with a retention
// policy.
func claimOwner(src Source, c *corev1.PersistentVolumeClaim) *ClaimOwner {
	for _, o := range src.Objects("statefulsets", c.Namespace) {
		set, ok := o.(*appsv1.StatefulSet)
		if !ok {
			continue
		}
		replicas := int32(1)
		if set.Spec.Replicas != nil {
			replicas = *set.Spec.Replicas
		}
		for _, t := range set.Spec.VolumeClaimTemplates {
			prefix := t.Name + "-" + set.Name + "-"
			if rest, ok := strings.CutPrefix(c.Name, prefix); ok {
				if n, err := strconv.Atoi(rest); err == nil {
					return &ClaimOwner{Kind: "StatefulSet", Name: set.Name, Template: t.Name, Ordinal: n, Replicas: replicas}
				}
			}
		}
	}
	for _, r := range c.OwnerReferences {
		if r.Controller != nil && *r.Controller {
			return &ClaimOwner{Kind: r.Kind, Name: r.Name, Ordinal: -1}
		}
	}
	return nil
}

func podNodes(pods []StoragePod) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pods {
		if p.Node != "" && !seen[p.Node] {
			seen[p.Node] = true
			out = append(out, p.Node)
		}
	}
	sort.Strings(out)
	return out
}

func anyRunning(pods []*corev1.Pod) bool {
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning {
			return true
		}
	}
	return false
}

// storagePod is a pod as a claim's sub-row shows it: where it mounts the
// claim, and why it waits when it does not run.
func (ix *storageIndex) storagePod(p *corev1.Pod, c *corev1.PersistentVolumeClaim, cls *storagev1.StorageClass) StoragePod {
	status, tone, _ := podStatus(p, ix.now)
	sp := StoragePod{Name: p.Name, Node: p.Spec.NodeName, Status: status, Tone: tone}
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != c.Name {
			continue
		}
		sp.ReadOnly = v.PersistentVolumeClaim.ReadOnly
	mounts:
		for _, ct := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
			for _, m := range ct.VolumeMounts {
				if m.Name == v.Name {
					sp.MountPath, sp.SubPath, sp.Container = m.MountPath, m.SubPath, ct.Name
					sp.ReadOnly = sp.ReadOnly || m.ReadOnly
					break mounts
				}
			}
		}
		break
	}
	if p.Status.Phase == corev1.PodRunning {
		if s := timePtr(p.Status.StartTime); s != nil {
			sp.Since = s
		}
		return sp
	}
	since := p.CreationTimestamp.Time.UTC()
	sched := podCondition(p, corev1.PodScheduled)
	if sched != nil && !sched.LastTransitionTime.IsZero() {
		since = sched.LastTransitionTime.Time.UTC()
	}
	sp.Since = &since
	if p.Spec.NodeName == "" {
		if sched != nil && sched.Message != "" {
			sp.Why = sched.Message
		}
		switch {
		case c.Status.Phase != corev1.ClaimBound && cls != nil && cls.VolumeBindingMode != nil && *cls.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer:
			sp.Note = "Pending, not yet scheduled"
		case c.Status.Phase != corev1.ClaimBound:
			sp.Note = "Pending, waits for the volume"
		case sched != nil && sched.Reason != "":
			sp.Note = sched.Reason + " for " + dur(ix.now.Sub(since))
		default:
			sp.Note = "Pending for " + dur(ix.now.Sub(since))
		}
		return sp
	}
	sp.Note = status + " for " + dur(ix.now.Sub(since))
	return sp
}

// The ST-14 rules, in order. Each returns the reason line and whether the
// row needs attention (ST-09).

// provisioningFailed matches the event the external provisioner or the
// volume controller reports when it cannot make a volume.
const provisioningFailed = "ProvisioningFailed"

func diagnoseClaim(c *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, cls *storagev1.StorageClass, classMissing bool, events []Event, pods []*corev1.Pod, now time.Time) (*Diagnosis, bool) {
	warned := false
	var failed *Event
	for i, e := range events {
		if e.Type == corev1.EventTypeWarning {
			warned = true
			if e.Reason == provisioningFailed && failed == nil {
				failed = &events[i]
			}
		}
	}
	switch c.Status.Phase {
	case corev1.ClaimLost:
		text := "volume " + c.Spec.VolumeName + " is gone · the data cannot be reached"
		if c.Spec.VolumeName == "" {
			text = "its volume is gone · the data cannot be reached"
		}
		return &Diagnosis{Text: text, Tone: "err"}, true
	case corev1.ClaimBound:
		if pv != nil && pv.Status.Phase == corev1.VolumeFailed {
			return &Diagnosis{Text: "volume failed · " + shortCause(pv.Status.Message), Tone: "err"}, true
		}
		req, cap := quantity(c.Spec.Resources.Requests), quantity(c.Status.Capacity)
		for _, cond := range c.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}
			switch cond.Type {
			case corev1.PersistentVolumeClaimFileSystemResizePending:
				return &Diagnosis{Text: "resizing · the filesystem grows when a pod mounts it again", Tone: "warn"}, false
			case corev1.PersistentVolumeClaimResizing:
				return &Diagnosis{Text: "resizing to " + Bytes(req), Tone: "warn"}, false
			}
		}
		if cap > 0 && req > cap {
			return &Diagnosis{Text: "resizing to " + Bytes(req), Tone: "warn"}, false
		}
		return nil, false
	}
	// Pending.
	class := ""
	if c.Spec.StorageClassName != nil {
		class = *c.Spec.StorageClassName
	}
	switch {
	case classMissing:
		return &Diagnosis{Text: "no class named " + class + " · nothing will provision it", Tone: "err"}, true
	case failed != nil:
		return &Diagnosis{Text: "provisioning failed · " + shortCause(failed.Message), Tone: "err"}, true
	case cls != nil && cls.VolumeBindingMode != nil && *cls.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer && !anyScheduled(pods):
		return &Diagnosis{Text: "waits for a pod · the class binds on first use", Tone: "info"}, warned
	case c.Spec.StorageClassName != nil && class == "":
		return &Diagnosis{Text: "no volume matches (size, access mode or selector)", Tone: "warn"}, warned
	}
	return &Diagnosis{Text: "waiting for the provisioner", Tone: "warn"}, warned
}

// anyUnwell says whether a pod that mounts a claim is not well, as the
// Pods tab shows it.
func anyUnwell(pods []*corev1.Pod, now time.Time) bool {
	for _, p := range pods {
		if _, tone, _ := podStatus(p, now); tone != "ok" {
			return true
		}
	}
	return false
}

func anyScheduled(pods []*corev1.Pod) bool {
	for _, p := range pods {
		if p.Spec.NodeName != "" {
			return true
		}
	}
	return false
}

// causeAfter finds the human part of a provisioner's message: what
// follows "desc = " in a gRPC error.
var causeAfter = regexp.MustCompile(`desc = (.*)$`)

// shortCause shortens a provisioner's or the volume controller's message
// to its cause, as the reason line shows it: "rpc error: code =
// ResourceExhausted desc = not enough disk space on any node for a 20Gi
// volume" becomes "not enough disk space".
func shortCause(msg string) string {
	msg = strings.TrimSpace(msg)
	if m := causeAfter.FindStringSubmatch(msg); m != nil {
		msg = m[1]
	} else if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	for _, cut := range []string{" on any ", " on node", " for a ", " for volume", "; ", ". "} {
		if i := strings.Index(msg, cut); i > 0 {
			msg = msg[:i]
		}
	}
	msg = strings.TrimRight(msg, ". ")
	if len(msg) > 80 {
		msg = msg[:77] + "…"
	}
	return msg
}

// storageQuotas reads each namespace's storage quota (ST-11): the first
// ResourceQuota that limits requests.storage, and every per-class limit.
func storageQuotas(src Source) map[string]*StorageQuota {
	out := map[string]*StorageQuota{}
	var qs []*corev1.ResourceQuota
	for _, o := range src.Objects("resourcequotas", "") {
		if q, ok := o.(*corev1.ResourceQuota); ok {
			qs = append(qs, q)
		}
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].Namespace+"/"+qs[i].Name < qs[j].Namespace+"/"+qs[j].Name })
	for _, q := range qs {
		for name, hard := range q.Spec.Hard {
			used := q.Status.Used[name]
			n := string(name)
			switch {
			case n == string(corev1.ResourceRequestsStorage):
				sq := quotaOf(out, q)
				if sq.Hard == 0 {
					sq.Name, sq.Hard, sq.Used = q.Name, hard.Value(), used.Value()
				}
			case strings.HasSuffix(n, ".storageclass.storage.k8s.io/requests.storage"):
				class := strings.TrimSuffix(n, ".storageclass.storage.k8s.io/requests.storage")
				sq := quotaOf(out, q)
				if sq.Classes == nil {
					sq.Classes = map[string]QuotaUse{}
				}
				if _, ok := sq.Classes[class]; !ok {
					sq.Classes[class] = QuotaUse{Hard: hard.Value(), Used: used.Value()}
				}
			}
		}
	}
	return out
}

func quotaOf(m map[string]*StorageQuota, q *corev1.ResourceQuota) *StorageQuota {
	sq := m[q.Namespace]
	if sq == nil {
		sq = &StorageQuota{Namespace: q.Namespace, Name: q.Name}
		m[q.Namespace] = sq
	}
	return sq
}

// Bytes formats a size the way the tab does: binary units, three figures
// at most ("61.2Gi", "1.28Ti", "740Gi").
func Bytes(n int64) string {
	if n <= 0 {
		return "0"
	}
	units := []string{"", "Ki", "Mi", "Gi", "Ti", "Pi"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	var s string
	switch {
	case v >= 100 || v == float64(int64(v)):
		s = strconv.FormatFloat(v, 'f', 0, 64)
	case v >= 10:
		s = strconv.FormatFloat(v, 'f', 1, 64)
	default:
		s = strconv.FormatFloat(v, 'f', 2, 64)
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s + units[i]
}

// Quantity formats a byte count as Kubernetes writes sizes, for commands:
// 150Gi.
func Quantity(n int64) string {
	return resource.NewQuantity(n, resource.BinarySI).String()
}

// accessShort is the column's abbreviation of an access mode (ST-06).
func accessShort(m string) string {
	switch m {
	case string(corev1.ReadWriteOnce):
		return "RWO"
	case string(corev1.ReadOnlyMany):
		return "ROX"
	case string(corev1.ReadWriteMany):
		return "RWX"
	case string(corev1.ReadWriteOncePod):
		return "RWOP"
	}
	return m
}

// AccessText is a pair's access modes as the column shows them.
func AccessText(modes []string) string {
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		out = append(out, accessShort(m))
	}
	return strings.Join(out, ",")
}
