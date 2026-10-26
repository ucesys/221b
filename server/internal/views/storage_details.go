package views

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

// The Storage drawer (ST-13 to ST-16): the chain from the class down to
// the node, one hop per row, the first that does not hold in red with its
// cause; the events of the claim and of the pods that use it; and what the
// reclaim policy and expansion mean for this claim.

// Seg is a piece of rich text: plain, bold, monospace or a link.
type Seg struct {
	T string `json:"t"`
	B bool   `json:"b,omitempty"`
	M bool   `json:"m,omitempty"`
	L *Link  `json:"l,omitempty"`
}

// Link names an object the drawer can open: a class, volume, claim, pod,
// node, StatefulSet, Helm release or namespace.
type Link struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// StorageHop is one link of the chain (ST-14).
type StorageHop struct {
	N     int    `json:"n"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Value []Seg  `json:"value"`
	// Tag is "default" for the default class.
	Tag  string `json:"tag,omitempty"`
	Desc []Seg  `json:"desc"`
	// Meter is a usage share, 0 to 100, drawn before the description.
	Meter *float64 `json:"meter,omitempty"`
	State string   `json:"state"`
	// Tone is ok, info, warn, err or muted.
	Tone string `json:"tone"`
}

// StorageEvent is one row of the drawer's events (ST-15).
type StorageEvent struct {
	Type       string     `json:"type"`
	Reason     string     `json:"reason"`
	Message    string     `json:"message"`
	Count      int32      `json:"count"`
	First      *time.Time `json:"first,omitempty"`
	Last       *time.Time `json:"last,omitempty"`
	ReportedBy string     `json:"reportedBy,omitempty"`
	// Pod is the pod an event is about, when it is not the claim.
	Pod string `json:"pod,omitempty"`
	// Tone is err for a provisioning failure, warn for another warning,
	// muted for the rest.
	Tone string `json:"tone"`
}

// StorageDetails is what the drawer of a claim or a volume shows.
type StorageDetails struct {
	Pair StoragePair `json:"pair"`
	// Class is the class the pair names, for someone who may read classes.
	Class  *StorageClass  `json:"class,omitempty"`
	Intro  []Seg          `json:"intro"`
	Hops   []StorageHop   `json:"hops"`
	Broken bool           `json:"broken"`
	Events []StorageEvent `json:"events"`
	// Lifecycle is the Lifecycle card's lines (ST-16).
	Lifecycle [][]Seg `json:"lifecycle"`
	// NoWarningDays is how many days the pair has had no warning, for
	// "No warnings in the last N days"; -1 when it has warnings.
	NoWarningDays int `json:"noWarningDays"`
	// Alert says the Alerts tab has an alert for the claim.
	Alert bool `json:"alert,omitempty"`
	// Snapshots says why Snapshot is disabled (ST-18).
	Snapshots string `json:"snapshots"`
	// Attachment is the VolumeAttachment that holds the volume on a node.
	Attachment *Attachment `json:"attachment,omitempty"`
	// Expansion is whether the class lets the claim grow (ST-18).
	Expansion bool `json:"expansion,omitempty"`
}

// Attachment is a volume's VolumeAttachment.
type Attachment struct {
	Name string    `json:"name"`
	Node string    `json:"node"`
	At   time.Time `json:"at"`
}

// StorageCan says what the person may read beyond the claim itself (ST-11,
// ST-20): volumes, classes, VolumeAttachments and the pods of the claim's
// namespace.
type StorageCan struct {
	Volumes, Classes, Attachments, Pods bool
}

// NoSnapshots is why Snapshot is disabled until snapshots are in scope.
const NoSnapshots = "No VolumeSnapshotClass on this cluster"

func segT(s string) Seg { return Seg{T: s} }
func segB(s string) Seg { return Seg{T: s, B: true} }
func segM(s string) Seg { return Seg{T: s, M: true} }
func segL(s string, l Link) Seg {
	return Seg{T: s, L: &l}
}

// ClaimDetails builds the drawer of a claim.
func ClaimDetails(src Source, namespace, name string, can StorageCan, now time.Time) (StorageDetails, bool) {
	c, _ := src.Object("persistentvolumeclaims", namespace, name).(*corev1.PersistentVolumeClaim)
	if c == nil {
		return StorageDetails{}, false
	}
	ix := newStorageIndex(src, now)
	p := ix.claimPair(c)
	var pv *corev1.PersistentVolume
	if can.Volumes {
		pv = ix.volumes[c.Spec.VolumeName]
	} else {
		redactPair(&p)
	}
	if !can.Pods {
		p.Pods, p.Nodes = nil, nil
	}
	var cls *storagev1.StorageClass
	if can.Classes {
		cls = ix.classes[p.Class]
	}
	d := StorageDetails{Pair: p, Snapshots: NoSnapshots, NoWarningDays: -1}
	if cls != nil {
		info := classInfo(cls)
		d.Class = &info
		d.Expansion = info.Expansion
	}
	if a, ok := BuildAlert(src, AlertKey(AlertPVC, namespace, name), now); ok && a.Severity != SevRecovered {
		d.Alert = true
	}
	var pods []*corev1.Pod
	if can.Pods {
		pods = ix.mounts[namespace+"/"+name]
	}
	d.Events = claimEvents(src, c, pv, pods, can, now)
	if can.Attachments && pv != nil {
		d.Attachment = attachmentOf(src, pv.Name)
	}
	d.build(c, pv, cls, pods, ix, can, now)
	d.lifecycle(pv, cls)
	d.recent(c.CreationTimestamp.Time, now)
	return d, true
}

// VolumeDetails builds the drawer of a volume no claim holds.
func VolumeDetails(src Source, name string, can StorageCan, now time.Time) (StorageDetails, bool) {
	v, _ := src.Object("persistentvolumes", "", name).(*corev1.PersistentVolume)
	if v == nil {
		return StorageDetails{}, false
	}
	ix := newStorageIndex(src, now)
	p := ix.volumePair(v)
	var cls *storagev1.StorageClass
	if can.Classes {
		cls = ix.classes[p.Class]
	}
	d := StorageDetails{Pair: p, Snapshots: NoSnapshots, NoWarningDays: -1}
	if cls != nil {
		info := classInfo(cls)
		d.Class = &info
	}
	for _, e := range eventsFor(src, string(v.UID)) {
		d.Events = append(d.Events, storageEvent(e, ""))
	}
	sortStorageEvents(d.Events)
	if d.Events == nil {
		d.Events = []StorageEvent{}
	}
	d.volumeChain(v, cls, ix, now)
	d.lifecycle(v, cls)
	d.recent(v.CreationTimestamp.Time, now)
	return d, true
}

// redactPair drops what only the volume says, for someone who may not
// read volumes.
func redactPair(p *StoragePair) {
	p.Reclaim, p.Source, p.VolumeLabels, p.Attached, p.WasClaim = "", nil, nil, nil, ""
}

func attachmentOf(src Source, pv string) *Attachment {
	for _, o := range src.Objects("volumeattachments", "") {
		a, ok := o.(*storagev1.VolumeAttachment)
		if ok && a.Spec.Source.PersistentVolumeName != nil && *a.Spec.Source.PersistentVolumeName == pv && a.Status.Attached {
			return &Attachment{Name: a.Name, Node: a.Spec.NodeName, At: a.CreationTimestamp.Time.UTC()}
		}
	}
	return nil
}

// classFacts is a class's one line: "driver.longhorn.io · Delete on
// release · binds immediately · expansion allowed" (ST-03).
func classFacts(c StorageClass) string {
	return strings.Join([]string{c.Provisioner, ReclaimText(c.Reclaim), BindingText(c.Binding), ExpansionText(c.Expansion)}, " · ")
}

// ReclaimText is "Delete on release" or "Retain on release".
func ReclaimText(r string) string { return r + " on release" }

// BindingText says when a class binds (ST-03).
func BindingText(mode string) string {
	if mode == string(storagev1.VolumeBindingWaitForFirstConsumer) {
		return "binds when a pod is scheduled"
	}
	return "binds immediately"
}

// ExpansionText says whether a class lets claims grow.
func ExpansionText(on bool) string {
	if on {
		return "expansion allowed"
	}
	return "no expansion"
}

func accessNote(modes []string, pods int) string {
	switch {
	case contains(modes, string(corev1.ReadWriteOncePod)):
		return "only this pod can mount it: ReadWriteOncePod"
	case contains(modes, string(corev1.ReadWriteMany)):
		return "any number of pods, on any node, can mount it: ReadWriteMany"
	case contains(modes, string(corev1.ReadOnlyMany)):
		return "pods on any node mount it read-only: ReadOnlyMany"
	case pods <= 1:
		return "the only pod that can mount it: ReadWriteOnce"
	}
	return "pods on one node at a time can mount it: ReadWriteOnce"
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func modeText(modes []string) string {
	return strings.Join(modes, ", ")
}

// sourceSegs says where a volume's data lives, for the volume hop.
func sourceSegs(p StoragePair, v *corev1.PersistentVolume) []Seg {
	s := p.Source
	if s == nil {
		return nil
	}
	switch s.Kind {
	case "csi":
		return []Seg{segT("CSI "), segM(s.Driver), segT(", handle "), segM(s.Handle)}
	case "nfs":
		return []Seg{segT("NFS "), segM(strings.TrimPrefix(s.Text, "nfs "))}
	case "local":
		out := []Seg{segT("local disk "), segM(strings.TrimPrefix(s.Text, "local "))}
		if s.Node != "" {
			out = append(out, segT(" on "), segL(s.Node, Link{Kind: "node", Name: s.Node}))
		}
		return out
	case "hostPath":
		return []Seg{segT("a folder of the node, "), segM(strings.TrimPrefix(s.Text, "hostPath "))}
	}
	return []Seg{segT("a volume of another kind")}
}

func modeFS(p StoragePair, cls *storagev1.StorageClass) string {
	mode := p.VolumeMode
	if mode == "" {
		mode = string(corev1.PersistentVolumeFilesystem)
	}
	fs := ""
	if p.Source != nil {
		fs = p.Source.FSType
	}
	if fs == "" && cls != nil {
		fs = cls.Parameters["fsType"]
		if fs == "" {
			fs = cls.Parameters["csi.storage.k8s.io/fstype"]
		}
	}
	if fs != "" && mode == string(corev1.PersistentVolumeFilesystem) {
		return mode + ", " + fs
	}
	return mode
}

// provisioningFailure is the latest ProvisioningFailed warning, if any.
func provisioningFailure(events []StorageEvent) *StorageEvent {
	for i, e := range events {
		if e.Reason == provisioningFailed && e.Type == corev1.EventTypeWarning && e.Pod == "" {
			return &events[i]
		}
	}
	return nil
}

// fullCause is a provisioner's message without the rpc wrapping and the
// size: "not enough disk space on any node".
func fullCause(msg string) string {
	if m := causeAfter.FindStringSubmatch(msg); m != nil {
		msg = m[1]
	}
	for _, cut := range []string{" for a ", " for volume"} {
		if i := strings.Index(msg, cut); i > 0 {
			msg = msg[:i]
		}
	}
	return strings.TrimRight(strings.TrimSpace(msg), ".")
}

// build makes the chain of a claim.
func (d *StorageDetails) build(c *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, cls *storagev1.StorageClass, pods []*corev1.Pod, ix *storageIndex, can StorageCan, now time.Time) {
	p := d.Pair
	pending := p.State == StatePending
	wffc := cls != nil && cls.VolumeBindingMode != nil && *cls.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer
	failure := provisioningFailure(d.Events)
	ns, name := c.Namespace, c.Name
	broken := ""

	// 1. The class.
	h1 := StorageHop{N: 1, Key: "class", Label: "Storage class", Tone: "ok", State: "in place"}
	switch {
	case p.ClassMissing:
		h1.Value = []Seg{segT(p.Class)}
		h1.Desc = []Seg{segB("No StorageClass named " + p.Class + " exists, so nothing will provision a volume for this claim."), segT(" Its only sign is a ProvisioningFailed event.")}
		h1.Tone, h1.State, broken = "err", "not found", "class"
	case p.Class == "":
		h1.Value = []Seg{segT("none")}
		h1.Desc = []Seg{segT("no class: the claim binds to a volume made by hand, by name or by size; nothing provisions or deletes it")}
		h1.Tone, h1.State = "muted", "—"
	case d.Class != nil:
		h1.Value = []Seg{segL(p.Class, Link{Kind: "class", Name: p.Class})}
		if d.Class.Default {
			h1.Tag = "default"
		}
		h1.Desc = []Seg{segT(classFacts(*d.Class))}
	default:
		h1.Value = []Seg{segT(p.Class)}
		h1.Desc = []Seg{segT("the class the claim names; your role does not read classes")}
		h1.Tone, h1.State = "muted", "—"
	}

	// 2. The volume.
	h2 := StorageHop{N: 2, Key: "volume", Label: "Volume", Tone: "ok"}
	switch {
	case p.State == StateLost:
		h2.Value = []Seg{segT(orNone(c.Spec.VolumeName))}
		h2.Desc = []Seg{segB("The volume " + orNone(c.Spec.VolumeName) + " is gone, so the claim has lost its data.")}
		h2.Tone, h2.State, broken = "err", "gone", "volume"
	case pending && p.ClassMissing:
		h2.Value = []Seg{segT("none")}
		h2.Desc = []Seg{segT("nothing provisions a volume for a class that does not exist")}
		h2.Tone, h2.State = "muted", "—"
	case pending && failure != nil:
		class := p.Class
		if class == "" {
			class = "the provisioner"
		}
		req := Bytes(p.Requested)
		h2.Value = []Seg{segT("none provisioned")}
		h2.Desc = []Seg{segB(class + " could not create a " + req + " volume: " + fullCause(failure.Message) + ".")}
		attempts := fmt.Sprintf(" %d attempts", failure.Count)
		if failure.Count == 1 {
			attempts = " 1 attempt"
		}
		if failure.First != nil {
			attempts += " in " + dur(now.Sub(*failure.First))
		}
		if failure.Last != nil {
			attempts += ", the last " + dur(now.Sub(*failure.Last)) + " ago"
		}
		h2.Desc = append(h2.Desc, segT(attempts+"; it retries with backoff."))
		if cls != nil {
			if n, err := strconv.Atoi(cls.Parameters["numberOfReplicas"]); err == nil && n > 1 {
				h2.Desc = append(h2.Desc, segT(fmt.Sprintf(" With %d replicas it needs %s free across %s nodes.", n, Bytes(p.Requested*int64(n)), numberWord(n))))
			}
		}
		h2.Tone, h2.State, broken = "err", "failed", "volume"
	case pending && wffc && !anyScheduled(pods):
		h2.Value = []Seg{segT("none yet")}
		h2.Desc = []Seg{segT("the class binds on first use: the volume is chosen when a pod that mounts the claim is scheduled")}
		h2.Tone, h2.State = "info", "waiting"
	case pending && p.Class == "" && c.Spec.StorageClassName != nil:
		h2.Value = []Seg{segT("none")}
		h2.Desc = []Seg{segB("No volume made by hand matches the claim's size, access mode or selector.")}
		h2.Tone, h2.State, broken = "err", "no match", "volume"
	case pending:
		h2.Value = []Seg{segT("none yet")}
		h2.Desc = []Seg{segT("waiting for the provisioner to create one")}
		h2.Tone, h2.State = "warn", "Pending"
	case pv == nil:
		h2.Value = []Seg{segT(p.Volume)}
		h2.Desc = []Seg{segT("the volume the claim is bound to; your role does not read volumes")}
		h2.State = "Bound"
	default:
		h2.Value = []Seg{segL(pv.Name, Link{Kind: "volume", Name: pv.Name})}
		h2.Desc = append(sourceSegs(p, pv), segT(" · "+Bytes(p.Capacity)+" · "+modeFS(p, cls)+" · "+volumeOrigin(pv, now)))
		h2.State = string(pv.Status.Phase)
		if pv.Status.Phase == corev1.VolumeFailed {
			h2.Desc = []Seg{segB("The volume failed: " + strings.TrimSpace(pv.Status.Message) + ".")}
			h2.Tone, broken = "err", "volume"
		}
	}

	// 3. The claim.
	h3 := StorageHop{N: 3, Key: "claim", Label: "Claim", Value: []Seg{segT(ns + "/" + name)}, Tone: "ok", State: p.Phase}
	h3.Desc = claimDesc(p, c)
	switch {
	case p.State == StateLost:
		h3.Tone = "err"
	case pending && h2.Tone == "info":
		h3.Tone = "info"
	case pending:
		h3.Tone = "warn"
	case p.Resizing:
		h3.Tone = "warn"
	}

	// 4. The pods.
	h4 := StorageHop{N: 4, Key: "pods", Label: "Mounted by", Tone: "ok"}
	switch {
	case !can.Pods:
		h4.Value = []Seg{segT("—")}
		h4.Desc = []Seg{segT("your role does not list the pods of " + ns)}
		h4.Tone, h4.State = "muted", "—"
	case len(p.Pods) == 0:
		h4.Value = []Seg{segT("no pod")}
		h4.Desc = []Seg{segT("no pod mounts it now")}
		h4.Tone, h4.State = "muted", "—"
	case len(p.Pods) == 1:
		sp := p.Pods[0]
		h4.Value = []Seg{segL(sp.Name, Link{Kind: "pod", Namespace: ns, Name: sp.Name})}
		h4.Desc, h4.Tone, h4.State = podHop(sp, p, pods, ix, now)
	default:
		running := 0
		for _, sp := range p.Pods {
			if sp.Tone == "ok" {
				running++
			}
		}
		h4.Value = []Seg{segT(fmt.Sprintf("%d pods", len(p.Pods)))}
		h4.Desc = []Seg{segT(fmt.Sprintf("%d running", running))}
		if len(p.Nodes) > 0 {
			h4.Desc = append(h4.Desc, segT(" on "+namesText(p.Nodes, 4)))
		}
		h4.Desc = append(h4.Desc, segT(" · "+accessNote(p.AccessModes, len(p.Pods))))
		h4.State = "Running"
		if running < len(p.Pods) {
			h4.State, h4.Tone = fmt.Sprintf("%d waiting", len(p.Pods)-running), "warn"
		}
	}
	if pending && h4.Tone == "ok" {
		h4.Tone = "warn"
	}

	// 5. The node.
	h5 := StorageHop{N: 5, Key: "node", Label: "Attached to", Tone: "ok"}
	switch {
	case pending && h2.Tone == "info":
		h5.Label, h5.Value = "Node", []Seg{segT("not chosen yet")}
		h5.Desc = []Seg{segT("the scheduler picks a node for the pod first, and the volume follows")}
		h5.Tone, h5.State = "muted", "—"
	case pending || p.State == StateLost:
		h5.Label, h5.Value = "Node", []Seg{segT("not scheduled")}
		h5.Desc = []Seg{segT("the scheduler holds the pod until the claim is bound; no node is involved yet")}
		h5.Tone, h5.State = "muted", "—"
	default:
		h5.Value, h5.Desc, h5.Tone, h5.State = d.nodeHop(p, pv, now)
	}

	d.Hops = []StorageHop{h1, h2, h3, h4, h5}
	// 6. Usage, once there is a volume.
	if p.State == StateBound {
		d.Hops = append(d.Hops, usageHop(p, pods, ix))
	}
	d.markBroken(broken)
	d.intro(broken, p, cls, failure)
}

// volumeOrigin says who made a volume and what happens to it when its
// claim goes.
func volumeOrigin(pv *corev1.PersistentVolume, now time.Time) string {
	made := "made by hand "
	if pv.Annotations["pv.kubernetes.io/provisioned-by"] != "" {
		made = "created by the provisioner "
	}
	end := ", kept when the claim goes"
	if pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
		end = ", deleted with the claim"
	}
	return made + dur(now.Sub(pv.CreationTimestamp.Time)) + " ago" + end
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func numberWord(n int) string {
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if n >= 0 && n < len(words) {
		return words[n]
	}
	return strconv.Itoa(n)
}

func claimDesc(p StoragePair, c *corev1.PersistentVolumeClaim) []Seg {
	req := "requested " + Bytes(p.Requested)
	if p.State == StateBound && p.Capacity > 0 {
		req += ", got " + Bytes(quantity(c.Status.Capacity))
	}
	out := []Seg{segT(req + " · " + modeText(p.AccessModes))}
	if o := p.Owner; o != nil {
		if o.Kind == "StatefulSet" && o.Template != "" {
			out = append(out, segT(" · from the "), segM(o.Template), segT(" volumeClaimTemplate of StatefulSet "), segL(o.Name, Link{Kind: "statefulset", Namespace: c.Namespace, Name: o.Name}))
		} else {
			out = append(out, segT(" · owned by "+o.Kind+" "), segM(o.Name))
		}
	}
	if p.Release != "" {
		out = append(out, segT(" · Helm release "), segL(p.Release, Link{Kind: "release", Namespace: c.Namespace, Name: p.Release}))
	}
	return out
}

// podHop describes the one pod that mounts a claim.
func podHop(sp StoragePod, p StoragePair, pods []*corev1.Pod, ix *storageIndex, now time.Time) ([]Seg, string, string) {
	if sp.Tone == "ok" && sp.Node != "" {
		rw := "read-write"
		if sp.ReadOnly {
			rw = "read-only"
		}
		out := []Seg{segM(sp.MountPath), segT(", " + rw + ", in container "), segM(sp.Container), segT(" · " + accessNote(p.AccessModes, 1))}
		return out, "ok", sp.Status
	}
	if sp.Node == "" && p.State != StateBound {
		why := "it waits for the claim"
		out := []Seg{segT("waits for the claim")}
		if sp.Why != "" {
			why = shortSchedulerMessage(sp.Why)
			out = []Seg{segT("waits for the claim: "), segM(why)}
		}
		if o := p.Owner; o != nil && o.Kind == "StatefulSet" && o.Ordinal >= 0 && int32(o.Ordinal+1) < o.Replicas {
			out = append(out, segT(fmt.Sprintf(" · the StatefulSet will not start %s-%d before %s is ready", o.Name, o.Ordinal+1, sp.Name)))
		}
		return out, "warn", sp.Status
	}
	out := []Seg{segT(sp.Note)}
	if sp.Why != "" {
		out = append(out, segT(": "), segM(shortSchedulerMessage(sp.Why)))
	}
	return out, "warn", sp.Status
}

// shortSchedulerMessage keeps the scheduler's reason and drops the node
// count and the preemption verdict: "0/10 nodes are available: pod has
// unbound immediate PersistentVolumeClaims. preemption: …" becomes "pod
// has unbound immediate PersistentVolumeClaims".
func shortSchedulerMessage(msg string) string {
	if i := strings.Index(msg, "are available: "); i >= 0 {
		msg = msg[i+len("are available: "):]
	}
	if i := strings.Index(msg, " preemption:"); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimRight(strings.TrimSpace(msg), ".")
}

// nodeHop says where a bound volume is attached or mounted.
func (d *StorageDetails) nodeHop(p StoragePair, pv *corev1.PersistentVolume, now time.Time) ([]Seg, []Seg, string, string) {
	if a := d.Attachment; a != nil {
		desc := []Seg{segT("VolumeAttachment "), segM(a.Name), segT(" · attached " + dur(now.Sub(a.At)) + " ago")}
		for _, sp := range p.Pods {
			if sp.Node == a.Node {
				desc = append(desc, segT(", when "+sp.Name+" was scheduled here"))
				break
			}
		}
		if !contains(p.AccessModes, string(corev1.ReadWriteMany)) {
			desc = append(desc, segT(" · one node at a time"))
		}
		return []Seg{segL(a.Node, Link{Kind: "node", Name: a.Node})}, desc, "ok", "attached"
	}
	if p.Source != nil && p.Source.Node != "" {
		return []Seg{segL(p.Source.Node, Link{Kind: "node", Name: p.Source.Node})}, []Seg{segT("a disk of this node: the pods that mount the claim run here only")}, "ok", "on the node"
	}
	switch len(p.Nodes) {
	case 0:
		return []Seg{segT("none")}, []Seg{segT("no running pod mounts it, so it is on no node")}, "muted", "—"
	case 1:
		node := p.Nodes[0]
		desc := []Seg{segT("mounted where its pod runs")}
		if pv != nil && p.Source != nil && p.Source.Kind == "nfs" {
			desc = []Seg{segT("mounted over the network; no attachment is needed")}
		}
		return []Seg{segL(node, Link{Kind: "node", Name: node})}, desc, "ok", "mounted"
	}
	return []Seg{segT(fmt.Sprintf("%d nodes", len(p.Nodes)))}, []Seg{segT("mounted on " + namesText(p.Nodes, 4) + "; a volume shared over the network needs no attachment")}, "ok", "mounted"
}

// usageHop is what the kubelet says about the volume (ST-08).
func usageHop(p StoragePair, pods []*corev1.Pod, ix *storageIndex) StorageHop {
	h := StorageHop{N: 6, Key: "usage", Label: "Usage", Tone: "ok", State: "room to grow"}
	u := p.Usage
	if u == nil {
		h.Value, h.Tone, h.State = []Seg{segT("—")}, "muted", "—"
		switch {
		case p.VolumeMode == string(corev1.PersistentVolumeBlock):
			h.Desc = []Seg{segT("a block device has no filesystem the kubelet can measure")}
		case !anyRunning(pods):
			h.Desc = []Seg{segT("no running pod mounts it, so no kubelet reports it")}
		case ix.stats == nil:
			h.Desc = []Seg{segT("the console does not read the kubelets' stats (kubeletStats in the chart)")}
		default:
			h.Desc = []Seg{segT("the kubelet has not reported it yet, or the console may not read the kubelets' stats (kubeletStats in the chart)")}
		}
		return h
	}
	pct := u.Pct
	h.Meter = &pct
	h.Value = []Seg{segT(Bytes(u.Used)), Seg{T: " of " + Bytes(u.Capacity)}}
	desc := fmt.Sprintf("%.0f%% used", u.Pct)
	if u.Inodes > 0 {
		desc += " · " + countShort(u.InodesUsed) + " of " + countShort(u.Inodes) + " inodes"
	}
	if u.Node != "" {
		desc += " · reported by the kubelet on " + u.Node
	}
	h.Desc = []Seg{segT(desc)}
	switch {
	case u.Pct >= Full:
		h.Tone, h.State = "err", "full"
	case u.Pct >= NearlyFull:
		h.Tone, h.State = "warn", "nearly full"
	}
	return h
}

// countShort writes a count as 1.1M, 640K, 912.
func countShort(n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1_000, 'f', 0, 64) + "K"
	}
	return strconv.FormatInt(n, 10)
}

// markBroken colours the hops after the first that does not hold: those
// that wait on it amber, those that cannot be reached yet grey (ST-14).
func (d *StorageDetails) markBroken(broken string) {
	if broken == "" {
		return
	}
	d.Broken = true
	seen := false
	for i := range d.Hops {
		h := &d.Hops[i]
		if h.Key == broken {
			seen = true
			continue
		}
		if !seen {
			continue
		}
		switch h.Key {
		case "volume", "claim", "pods":
			if h.Tone != "muted" {
				h.Tone = "warn"
			}
		default:
			h.Tone = "muted"
		}
	}
}

// intro is the sentence above the chain.
func (d *StorageDetails) intro(broken string, p StoragePair, cls *storagev1.StorageClass, failure *StorageEvent) {
	pod := ""
	if len(p.Pods) > 0 {
		pod = p.Pods[0].Name
	}
	switch {
	case broken == "class":
		d.Intro = []Seg{segT("The chain breaks at the class: no class named "), segB(p.Class), segT(" exists, so nothing will create a volume. Nothing below it can move until the claim names a class that does.")}
	case broken == "volume" && failure != nil:
		d.Intro = []Seg{segT("The chain breaks at the volume: "), segB(orNone(p.Class)), segT(fmt.Sprintf(" has tried %d times to create one and keeps retrying. Nothing below it can move until it does.", failure.Count))}
	case broken == "volume" && p.State == StateLost:
		d.Intro = []Seg{segT("The chain breaks at the volume: "), segB(orNone(p.Volume)), segT(" is gone, and the claim with it. Nothing below it can work again.")}
	case broken == "volume":
		d.Intro = []Seg{segT("The chain breaks at the volume: "), segB("no volume"), segT(" matches the claim. Nothing below it can move until one does.")}
	case p.State == StatePending && len(d.Hops) > 1 && d.Hops[1].Tone == "info":
		d.Intro = []Seg{segT("Nothing is wrong: the class binds on first use, so the volume is chosen when ")}
		if pod != "" {
			d.Intro = append(d.Intro, segB(pod), segT(" is scheduled."))
		} else {
			d.Intro = append(d.Intro, segT("a pod that mounts the claim is scheduled."))
		}
	case p.State == StatePending:
		d.Intro = []Seg{segT("The claim waits for a volume; the provisioner has not reported a failure.")}
	case pod != "":
		d.Intro = []Seg{segT("Every link in the chain that has to hold for "), segB(pod), segT(" to read and write this volume, from the class down to the node.")}
	default:
		d.Intro = []Seg{segT("Every link in the chain that has to hold for a pod to read and write this volume, from the class down to the node.")}
	}
}

// volumeChain is the chain of a volume no claim holds.
func (d *StorageDetails) volumeChain(v *corev1.PersistentVolume, cls *storagev1.StorageClass, ix *storageIndex, now time.Time) {
	p := d.Pair
	h1 := StorageHop{N: 1, Key: "class", Label: "Storage class", Tone: "ok", State: "in place"}
	switch {
	case p.Class == "":
		h1.Value, h1.Desc, h1.Tone, h1.State = []Seg{segT("none")}, []Seg{segT("made by hand: nothing provisions or deletes it")}, "muted", "—"
	case d.Class != nil:
		h1.Value = []Seg{segL(p.Class, Link{Kind: "class", Name: p.Class})}
		if d.Class.Default {
			h1.Tag = "default"
		}
		h1.Desc = []Seg{segT(classFacts(*d.Class))}
	default:
		h1.Value, h1.Desc, h1.Tone, h1.State = []Seg{segT(p.Class)}, []Seg{segT("no class of this name exists any more")}, "muted", "—"
	}
	h2 := StorageHop{N: 2, Key: "volume", Label: "Volume", Value: []Seg{segT(v.Name)}, State: p.Phase}
	base := append(sourceSegs(p, v), segT(" · "+Bytes(p.Capacity)+" · "+modeFS(p, cls)+" · "+volumeOrigin(v, now)))
	h3 := StorageHop{N: 3, Key: "claim", Label: "Claim", Tone: "muted", State: "—"}
	switch p.State {
	case StateReleased:
		h2.Tone = "warn"
		since := now
		if p.Since != nil {
			since = *p.Since
		}
		keep := " The data is kept: to use it again, remove the volume's claimRef so a new claim can bind it, or copy the data off and delete the volume."
		if p.Reclaim != string(corev1.PersistentVolumeReclaimRetain) {
			keep = " The reclaim policy is " + p.Reclaim + ", so the volume and its data are being deleted."
		}
		h2.Desc = append([]Seg{segB("Its claim was deleted " + dur(now.Sub(since)) + " ago."), segT(keep + " ")}, base...)
		h3.Value, h3.Desc, h3.State = []Seg{segT("was " + p.WasClaim)}, []Seg{segT("deleted " + dur(now.Sub(since)) + " ago")}, "deleted"
		d.Intro = []Seg{segT("This volume's claim is gone: "), segB(p.WasClaim), segT(" was deleted " + dur(now.Sub(since)) + " ago, and the volume is Released. No new claim binds it until someone clears its claimRef.")}
	case StateFailed:
		h2.Tone = "err"
		h2.Desc = append([]Seg{segB("The volume failed: " + strings.TrimSpace(v.Status.Message) + ". ")}, base...)
		h3.Value, h3.Desc = []Seg{segT(orNone(p.WasClaim))}, []Seg{segT("the claim it was for")}
		d.Intro = []Seg{segT("The chain breaks at the volume: "), segB(v.Name), segT(" failed. " + strings.TrimSpace(v.Status.Message))}
		d.Broken = true
	default:
		h2.Tone = "info"
		h2.Desc = append(base, segT(" · waiting for a claim that asks for this size and access mode, or names it"))
		h3.Value, h3.Desc = []Seg{segT("none")}, []Seg{segT("no claim holds it")}
		d.Intro = []Seg{segT("This volume is "), segB("Available"), segT(": no claim holds it. A claim binds it when it asks for no more than " + Bytes(p.Capacity) + " with a matching access mode and class, or names it.")}
	}
	h4 := StorageHop{N: 4, Key: "pods", Label: "Mounted by", Value: []Seg{segT("no pod")}, Desc: []Seg{segT("no pod can mount a volume without a claim")}, Tone: "muted", State: "—"}
	h5 := StorageHop{N: 5, Key: "node", Label: "Attached to", Value: []Seg{segT("none")}, Desc: []Seg{segT("on no node")}, Tone: "muted", State: "—"}
	if p.Source != nil && p.Source.Node != "" {
		h5.Value, h5.Desc = []Seg{segL(p.Source.Node, Link{Kind: "node", Name: p.Source.Node})}, []Seg{segT("a disk of this node")}
	}
	d.Hops = []StorageHop{h1, h2, h3, h4, h5}
}

// claimEvents gathers the claim's events, the volume's, what the kubelet
// and the attach-detach controller reported for its pods about attaching
// and mounting it, and why its waiting pods are not scheduled (ST-15).
func claimEvents(src Source, c *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, pods []*corev1.Pod, can StorageCan, now time.Time) []StorageEvent {
	out := []StorageEvent{}
	for _, e := range eventsFor(src, string(c.UID)) {
		out = append(out, storageEvent(e, ""))
	}
	if pv != nil {
		for _, e := range eventsFor(src, string(pv.UID)) {
			out = append(out, storageEvent(e, ""))
		}
	}
	for _, p := range pods {
		for _, e := range eventsFor(src, string(p.UID)) {
			switch {
			case attachMount.MatchString(e.Reason):
				if (c.Spec.VolumeName != "" && strings.Contains(e.Message, c.Spec.VolumeName)) || strings.Contains(e.Message, c.Name) {
					out = append(out, storageEvent(e, p.Name))
				}
			case e.Reason == "FailedScheduling" && p.Spec.NodeName == "":
				out = append(out, storageEvent(e, p.Name))
			}
		}
	}
	sortStorageEvents(out)
	return out
}

func storageEvent(e Event, pod string) StorageEvent {
	se := StorageEvent{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count, First: e.First, Last: e.Last, ReportedBy: e.Source, Pod: pod, Tone: "muted"}
	if e.Type == corev1.EventTypeWarning {
		se.Tone = "warn"
		if e.Reason == provisioningFailed || strings.HasPrefix(e.Reason, "Failed") && pod == "" {
			se.Tone = "err"
		}
	}
	return se
}

// sortStorageEvents puts warnings first, then the newest first (ST-15).
func sortStorageEvents(es []StorageEvent) {
	at := func(e StorageEvent) time.Time {
		if e.Last != nil {
			return *e.Last
		}
		if e.First != nil {
			return *e.First
		}
		return time.Time{}
	}
	sort.SliceStable(es, func(i, j int) bool {
		wi, wj := es[i].Type == corev1.EventTypeWarning, es[j].Type == corev1.EventTypeWarning
		if wi != wj {
			return wi
		}
		return at(es[i]).After(at(es[j]))
	})
}

// lifecycle is the Lifecycle card (ST-16): what the reclaim policy means
// for this claim, whether it can grow, and snapshots.
func (d *StorageDetails) lifecycle(pv *corev1.PersistentVolume, cls *storagev1.StorageClass) {
	reclaim := d.Pair.Reclaim
	if reclaim == "" && d.Class != nil {
		reclaim = d.Class.Reclaim
	}
	switch reclaim {
	case string(corev1.PersistentVolumeReclaimDelete):
		d.Lifecycle = append(d.Lifecycle, []Seg{segB("Reclaim policy Delete"), segT(" — deleting the claim deletes the volume and its data. To keep the data, set the volume to Retain first.")})
	case string(corev1.PersistentVolumeReclaimRetain):
		d.Lifecycle = append(d.Lifecycle, []Seg{segB("Reclaim policy Retain"), segT(" — deleting the claim keeps the volume and its data; the volume is Released until someone reuses or deletes it.")})
	case "":
	default:
		d.Lifecycle = append(d.Lifecycle, []Seg{segB("Reclaim policy " + reclaim)})
	}
	switch {
	case d.Class != nil && d.Class.Expansion:
		d.Lifecycle = append(d.Lifecycle, []Seg{segB("Expansion allowed"), segT(" — online, while mounted; a volume only grows, never shrinks.")})
	case d.Class != nil:
		d.Lifecycle = append(d.Lifecycle, []Seg{segB("No expansion"), segT(" — the class does not allow it; a bigger volume means a new claim and a copy of the data.")})
	}
	d.Lifecycle = append(d.Lifecycle, []Seg{segB("Snapshots"), segT(" — no VolumeSnapshotClass on this cluster.")})
}

// recent says how long the pair has had no warning.
func (d *StorageDetails) recent(created, now time.Time) {
	for _, e := range d.Events {
		if e.Type == corev1.EventTypeWarning {
			return
		}
	}
	d.NoWarningDays = max(1, int(now.Sub(created).Hours()/24))
}

// namesText lists names as "a, b and c", the first max of them and how
// many more.
func namesText(names []string, max int) string {
	if len(names) > max {
		return strings.Join(names[:max], ", ") + fmt.Sprintf(" and %d more", len(names)-max)
	}
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
