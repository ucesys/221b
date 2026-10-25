package views_test

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/views"
)

func sc(name, provisioner string, mode storagev1.VolumeBindingMode, def bool) *storagev1.StorageClass {
	reclaim := corev1.PersistentVolumeReclaimDelete
	c := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: ts(180 * 24 * time.Hour)}, Provisioner: provisioner, ReclaimPolicy: &reclaim, VolumeBindingMode: &mode, AllowVolumeExpansion: ptr(true)}
	if def {
		c.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return c
}

func pvc(ns, name string, class *string, size string, phase corev1.PersistentVolumeClaimPhase, volume string) *corev1.PersistentVolumeClaim {
	c := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + ns + "-" + name), CreationTimestamp: ts(2 * time.Hour)}}
	c.Spec = corev1.PersistentVolumeClaimSpec{StorageClassName: class, VolumeName: volume, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}}}
	c.Status.Phase = phase
	if phase == corev1.ClaimBound {
		c.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}
	}
	return c
}

func pv(name, class, size string, phase corev1.PersistentVolumePhase, claimNS, claim string, reclaim corev1.PersistentVolumeReclaimPolicy) *corev1.PersistentVolume {
	v := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: ts(40 * 24 * time.Hour)}}
	v.Spec = corev1.PersistentVolumeSpec{StorageClassName: class, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}, PersistentVolumeReclaimPolicy: reclaim, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}
	v.Spec.CSI = &corev1.CSIPersistentVolumeSource{Driver: "driver.longhorn.io", VolumeHandle: name, FSType: "ext4"}
	if claim != "" {
		v.Spec.ClaimRef = &corev1.ObjectReference{Namespace: claimNS, Name: claim}
	}
	v.Status.Phase = phase
	return v
}

func mounting(ns, name, node, claim string) *corev1.Pod {
	p := pod(ns, name, node)
	p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}}
	p.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/data"}}
	return p
}

func pendingPod(ns, name, claim, msg string) *corev1.Pod {
	p := mounting(ns, name, "", claim)
	p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: msg, LastTransitionTime: ts(19 * 24 * time.Hour)}}}
	return p
}

func storageSource() *fake.Source {
	src := fake.New("worker-1")
	src.Put(sc("longhorn", "driver.longhorn.io", storagev1.VolumeBindingImmediate, true))
	src.Put(sc("local-nvme", "kubernetes.io/no-provisioner", storagev1.VolumeBindingWaitForFirstConsumer, false))
	return src
}

func pairOf(t *testing.T, rows map[string]views.StorageRow, key string) views.StoragePair {
	t.Helper()
	r, ok := rows[key]
	if !ok || r.Pair == nil {
		t.Fatalf("no pair %s among %d rows", key, len(rows))
	}
	return *r.Pair
}

func reason(p views.StoragePair) string {
	if p.Reason == nil {
		return ""
	}
	return p.Reason.Tone + " " + p.Reason.Text
}

// ST-14: each diagnosis rule, with the reason line and whether the row
// needs attention (ST-09).
func TestStorageDiagnosisRules(t *testing.T) {
	src := storageSource()
	longhorn, local, missing, none := ptr("longhorn"), ptr("local-nvme"), ptr("fast-ssd"), ptr("")

	// A claim naming a class that does not exist.
	src.Put(pvc("batch", "scratch-report", missing, "100Gi", corev1.ClaimPending, ""))
	// A ProvisioningFailed warning.
	src.Put(pvc("analytics", "data-cache-2", longhorn, "20Gi", corev1.ClaimPending, ""))
	src.Put(warning("uid-analytics-data-cache-2", "PersistentVolumeClaim", "analytics", "data-cache-2", "ProvisioningFailed", `failed to provision volume with StorageClass "longhorn": rpc error: code = ResourceExhausted desc = not enough disk space on any node for a 20Gi volume with 3 replicas`, 14, 2*time.Hour, 2*time.Minute))
	// The class binds on first use and no pod is scheduled.
	src.Put(pvc("analytics", "spill-3", local, "500Gi", corev1.ClaimPending, ""))
	src.Put(pendingPod("analytics", "dremio-executor-3", "spill-3", "0/10 nodes are available: 3 Insufficient memory."))
	// No class, and no volume matches.
	src.Put(pvc("ingest", "by-hand", none, "1Ti", corev1.ClaimPending, ""))
	// Otherwise Pending: waiting for the provisioner, with no warning yet.
	src.Put(pvc("web", "fresh", longhorn, "10Gi", corev1.ClaimPending, ""))
	// Lost.
	src.Put(pvc("web", "orphan", longhorn, "10Gi", corev1.ClaimLost, "pvc-gone"))
	// Released, Retain and Delete; Failed.
	src.Put(pv("pvc-kept", "longhorn", "100Gi", corev1.VolumeReleased, "ingest", "landing-zone", corev1.PersistentVolumeReclaimRetain))
	deleting := pv("pvc-deleting", "longhorn", "50Gi", corev1.VolumeReleased, "ingest", "old", corev1.PersistentVolumeReclaimDelete)
	deleting.Status.LastPhaseTransitionTime = ptr(ts(12 * 24 * time.Hour))
	src.Put(deleting)
	failed := pv("pvc-failed", "longhorn", "50Gi", corev1.VolumeFailed, "", "", corev1.PersistentVolumeReclaimDelete)
	failed.Status.Message = "Error deleting EBS volume: VolumeInUse: vol-0abc is attached to i-0def"
	src.Put(failed)
	// Available.
	src.Put(pv("archive", "", "500Gi", corev1.VolumeAvailable, "", "", corev1.PersistentVolumeReclaimRetain))
	// Resizing: the request is above what the volume has.
	big := pvc("storage", "data-postgres-0", longhorn, "150Gi", corev1.ClaimBound, "pvc-pg0")
	big.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")}
	src.Put(big)
	src.Put(pv("pvc-pg0", "longhorn", "100Gi", corev1.VolumeBound, "storage", "data-postgres-0", corev1.PersistentVolumeReclaimDelete))

	rows := views.BuildStorage(src, now)
	for _, c := range []struct {
		key, reason string
		attention   bool
	}{
		{"pvc:batch/scratch-report", "err no class named fast-ssd · nothing will provision it", true},
		{"pvc:analytics/data-cache-2", "err provisioning failed · not enough disk space", true},
		{"pvc:analytics/spill-3", "info waits for a pod · the class binds on first use", false},
		{"pvc:ingest/by-hand", "warn no volume matches (size, access mode or selector)", false},
		{"pvc:web/fresh", "warn waiting for the provisioner", false},
		{"pvc:web/orphan", "err volume pvc-gone is gone · the data cannot be reached", true},
		{"pv:pvc-kept", "warn claim deleted 40d ago · data kept (Retain)", true},
		{"pv:pvc-deleting", "warn claim deleted 12d ago · being deleted", true},
		{"pv:pvc-failed", "err vol-0abc is attached to i-0def", true},
		{"pv:archive", "muted waiting for a claim", false},
		{"pvc:storage/data-postgres-0", "warn resizing to 150Gi", false},
	} {
		p := pairOf(t, rows, c.key)
		if reason(p) != c.reason || p.Attention != c.attention {
			t.Errorf("%s: %q attention=%v, want %q %v", c.key, reason(p), p.Attention, c.reason, c.attention)
		}
	}
	// The class that binds on first use makes the pill blue, not amber.
	if p := pairOf(t, rows, "pvc:analytics/spill-3"); p.Tone != "info" || p.State != views.StatePending {
		t.Errorf("spill-3: %s %s", p.State, p.Tone)
	}
	if p := pairOf(t, rows, "pvc:batch/scratch-report"); !p.ClassMissing || p.Class != "fast-ssd" {
		t.Errorf("scratch-report: class %q missing=%v", p.Class, p.ClassMissing)
	}
	// A warning on a claim waiting for the provisioner makes it need attention.
	src.Put(warning("uid-web-fresh", "PersistentVolumeClaim", "web", "fresh", "ExternalProvisioning", "Waiting for a volume to be created", 3, time.Hour, time.Minute))
	if p := pairOf(t, views.BuildStorage(src, now), "pvc:web/fresh"); !p.Attention {
		t.Error("a pending claim with a warning needs attention")
	}
}

// ST-05: the kinds of row, with a volume and its claim sharing one; ST-07:
// the pods that mount a claim and why one waits; ST-08: usage, and 85%
// and over needing attention.
func TestStorageRowsPairVolumesWithClaims(t *testing.T) {
	src := storageSource()
	longhorn := ptr("longhorn")
	src.Put(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "storage"}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(2)), VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}})
	c := pvc("storage", "data-postgres-0", longhorn, "100Gi", corev1.ClaimBound, "pvc-pg0")
	c.Annotations = map[string]string{"meta.helm.sh/release-name": "pg-primary"}
	src.Put(c)
	src.Put(pv("pvc-pg0", "longhorn", "100Gi", corev1.VolumeBound, "storage", "data-postgres-0", corev1.PersistentVolumeReclaimDelete))
	src.Put(mounting("storage", "postgres-0", "worker-1", "data-postgres-0"))
	src.Put(&storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "csi-3a9f1e7c0b2d"}, Spec: storagev1.VolumeAttachmentSpec{Attacher: "driver.longhorn.io", NodeName: "worker-1", Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: ptr("pvc-pg0")}}, Status: storagev1.VolumeAttachmentStatus{Attached: true}})
	src.SetVolumeStats("storage", "data-postgres-0", kube.VolumeStats{UsedBytes: 86 * (1 << 30), CapacityBytes: 100 * (1 << 30), AvailableBytes: 14 * (1 << 30), Node: "worker-1"})
	// A claim mounted by a running pod and one that waits for room.
	src.Put(pvc("batch", "reports-out", longhorn, "200Gi", corev1.ClaimBound, "pvc-rep"))
	src.Put(pv("pvc-rep", "longhorn", "200Gi", corev1.VolumeBound, "batch", "reports-out", corev1.PersistentVolumeReclaimRetain))
	src.Put(mounting("batch", "report-runner-0", "worker-1", "reports-out"))
	src.Put(pendingPod("batch", "report-29pzq", "reports-out", "0/10 nodes are available: 10 Insufficient cpu."))
	// A claim no pod mounts has no usage, whatever the kubelet once said.
	src.Put(pvc("batch", "idle", longhorn, "1Gi", corev1.ClaimBound, "pvc-idle"))
	src.Put(pv("pvc-idle", "longhorn", "1Gi", corev1.VolumeBound, "batch", "idle", corev1.PersistentVolumeReclaimDelete))
	src.SetVolumeStats("batch", "idle", kube.VolumeStats{UsedBytes: 1, CapacityBytes: 2})

	rows := views.BuildStorage(src, now)
	p := pairOf(t, rows, "pvc:storage/data-postgres-0")
	if p.State != views.StateBound || p.Volume != "pvc-pg0" || p.Class != "longhorn" || p.Reclaim != "Delete" || p.Release != "pg-primary" || p.Capacity != 100<<30 {
		t.Fatalf("pair %+v", p)
	}
	if p.Owner == nil || p.Owner.Kind != "StatefulSet" || p.Owner.Name != "postgres" || p.Owner.Template != "data" || p.Owner.Ordinal != 0 {
		t.Errorf("owner %+v", p.Owner)
	}
	if len(p.Attached) != 1 || p.Attached[0] != "worker-1" || p.Source == nil || p.Source.Kind != "csi" || p.Source.Driver != "driver.longhorn.io" {
		t.Errorf("attached %v source %+v", p.Attached, p.Source)
	}
	if len(p.Pods) != 1 || p.Pods[0].MountPath != "/var/lib/data" || p.Pods[0].Container != "main" || p.Pods[0].Status != "Running" {
		t.Errorf("pods %+v", p.Pods)
	}
	if p.Usage == nil || p.Usage.Pct != 86 || !p.Attention {
		t.Errorf("86%% used needs attention: %+v %v", p.Usage, p.Attention)
	}
	r := pairOf(t, rows, "pvc:batch/reports-out")
	if len(r.Pods) != 2 || r.Pods[1].Name != "report-29pzq" || r.Pods[1].Note != "Unschedulable for 19d" || r.Pods[1].Node != "" {
		t.Errorf("reports-out pods %+v", r.Pods)
	}
	if len(r.Nodes) != 1 || r.Nodes[0] != "worker-1" {
		t.Errorf("nodes %v", r.Nodes)
	}
	if u := pairOf(t, rows, "pvc:batch/idle").Usage; u != nil {
		t.Errorf("idle has usage %+v", u)
	}
	// The class rows carry the header's facts (ST-03).
	cls := rows["sc:longhorn"].Class
	if cls == nil || !cls.Default || cls.Reclaim != "Delete" || cls.Binding != "Immediate" || !cls.Expansion || cls.Provisioner != "driver.longhorn.io" {
		t.Errorf("class %+v", cls)
	}
	if local := rows["sc:local-nvme"].Class; local == nil || local.Default || local.Binding != "WaitForFirstConsumer" {
		t.Errorf("local class %+v", local)
	}
}

// A pending claim's pod says it waits for the volume; with a class that
// binds on first use, that it is not yet scheduled.
func TestStoragePodsSayWhyTheyWait(t *testing.T) {
	src := storageSource()
	src.Put(pvc("analytics", "data-cache-2", ptr("longhorn"), "20Gi", corev1.ClaimPending, ""))
	src.Put(pendingPod("analytics", "cache-2", "data-cache-2", "pod has unbound immediate PersistentVolumeClaims"))
	src.Put(pvc("analytics", "spill-3", ptr("local-nvme"), "500Gi", corev1.ClaimPending, ""))
	src.Put(pendingPod("analytics", "dremio-executor-3", "spill-3", "0/10 nodes are available"))
	rows := views.BuildStorage(src, now)
	if n := pairOf(t, rows, "pvc:analytics/data-cache-2").Pods[0].Note; n != "Pending, waits for the volume" {
		t.Errorf("cache-2: %q", n)
	}
	if n := pairOf(t, rows, "pvc:analytics/spill-3").Pods[0].Note; n != "Pending, not yet scheduled" {
		t.Errorf("dremio-executor-3: %q", n)
	}
}

// With no class readable at all, no claim is said to name a missing one.
func TestStorageNoClassesReadMeansNoMissingClass(t *testing.T) {
	src := fake.New("worker-1")
	src.Put(pvc("batch", "scratch-report", ptr("fast-ssd"), "100Gi", corev1.ClaimPending, ""))
	p := pairOf(t, views.BuildStorage(src, now), "pvc:batch/scratch-report")
	if p.ClassMissing || reason(p) != "warn waiting for the provisioner" {
		t.Errorf("%v %q", p.ClassMissing, reason(p))
	}
}

// ST-11: the namespace's quota on requests.storage, and per class.
func TestStorageQuotas(t *testing.T) {
	src := storageSource()
	src.Put(&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: "analytics"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("40")}}})
	src.Put(&corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "storage", Namespace: "analytics"},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("8Ti"), "longhorn.storageclass.storage.k8s.io/requests.storage": resource.MustParse("500Gi")}},
		Status:     corev1.ResourceQuotaStatus{Used: corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("4Ti"), "longhorn.storageclass.storage.k8s.io/requests.storage": resource.MustParse("60Gi")}},
	})
	rows := views.BuildStorage(src, now)
	q := rows["quota:analytics"].Quota
	if q == nil || q.Name != "storage" || q.Hard != 8<<40 || q.Used != 4<<40 || q.Classes["longhorn"].Hard != 500<<30 || q.Classes["longhorn"].Used != 60<<30 {
		t.Fatalf("quota %+v", q)
	}
	if _, ok := rows["quota:batch"]; ok {
		t.Error("batch has no storage quota")
	}
}

func TestStorageKeysAndSizes(t *testing.T) {
	for _, k := range []string{"sc:longhorn", "pv:archive", "pvc:batch/scratch-report", "quota:analytics"} {
		kind, ns, name, ok := views.SplitStorageKey(k)
		if !ok || views.StorageKey(kind, ns, name) != k {
			t.Errorf("%s: %s %s %s %v", k, kind, ns, name, ok)
		}
	}
	for n, want := range map[int64]string{740 << 30: "740Gi", 61*(1<<30) + (1<<30)/5: "61.2Gi", 1310 << 30: "1.28Ti", 2 << 40: "2Ti", 500 << 30: "500Gi"} {
		if got := views.Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %s, want %s", n, got, want)
		}
	}
}

func segText(ss []views.Seg) string {
	out := ""
	for _, s := range ss {
		out += s.T
	}
	return out
}

func tones(hs []views.StorageHop) string {
	out := []string{}
	for _, h := range hs {
		out = append(out, h.Key+"="+h.Tone)
	}
	return strings.Join(out, " ")
}

var all = views.StorageCan{Volumes: true, Classes: true, Attachments: true, Pods: true}

// ST-14: the chain of a claim the provisioner cannot satisfy breaks at the
// volume, the claim and its pod wait, the node cannot be reached yet; the
// intro names the cause. ST-15: the claim's events and its pod's
// FailedScheduling, warnings first.
func TestStorageChainBreaksAtTheFirstHopThatDoesNotHold(t *testing.T) {
	src := storageSource()
	src.Put(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "analytics"}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(4)), VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}})
	src.Put(pvc("analytics", "data-cache-2", ptr("longhorn"), "20Gi", corev1.ClaimPending, ""))
	src.Put(warning("uid-analytics-data-cache-2", "PersistentVolumeClaim", "analytics", "data-cache-2", "ProvisioningFailed", `failed to provision volume with StorageClass "longhorn": rpc error: code = ResourceExhausted desc = not enough disk space on any node for a 20Gi volume with 3 replicas`, 14, 2*time.Hour, 2*time.Minute))
	cache2 := pendingPod("analytics", "cache-2", "data-cache-2", "0/10 nodes are available: pod has unbound immediate PersistentVolumeClaims. preemption: 0/10 nodes are available: 10 Preemption is not helpful for scheduling.")
	src.Put(cache2)
	src.Put(warning("uid-cache-2", "Pod", "analytics", "cache-2", "FailedScheduling", "0/10 nodes are available: pod has unbound immediate PersistentVolumeClaims.", 9, 2*time.Hour, 4*time.Minute))
	lh, _ := src.Object("storageclasses", "", "longhorn").(*storagev1.StorageClass)
	lh.Parameters = map[string]string{"numberOfReplicas": "3"}

	d, ok := views.ClaimDetails(src, "analytics", "data-cache-2", all, now)
	if !ok {
		t.Fatal("no details")
	}
	if got := tones(d.Hops); got != "class=ok volume=err claim=warn pods=warn node=muted" {
		t.Errorf("tones %s", got)
	}
	if got := segText(d.Hops[1].Desc); got != "longhorn could not create a 20Gi volume: not enough disk space on any node. 14 attempts in 2h, the last 2m ago; it retries with backoff. With 3 replicas it needs 60Gi free across three nodes." {
		t.Errorf("volume hop: %s", got)
	}
	if got := segText(d.Intro); got != "The chain breaks at the volume: longhorn has tried 14 times to create one and keeps retrying. Nothing below it can move until it does." {
		t.Errorf("intro: %s", got)
	}
	if got := segText(d.Hops[3].Desc); got != "waits for the claim: pod has unbound immediate PersistentVolumeClaims · the StatefulSet will not start cache-3 before cache-2 is ready" {
		t.Errorf("pod hop: %s", got)
	}
	if len(d.Events) != 2 || d.Events[0].Reason != "ProvisioningFailed" || d.Events[0].Tone != "err" || d.Events[1].Pod != "cache-2" || d.NoWarningDays != -1 || !d.Broken {
		t.Errorf("events %+v", d.Events)
	}
	// A class that does not exist breaks the chain at the class.
	src.Put(pvc("batch", "scratch-report", ptr("fast-ssd"), "100Gi", corev1.ClaimPending, ""))
	d, _ = views.ClaimDetails(src, "batch", "scratch-report", all, now)
	if got := tones(d.Hops); got != "class=err volume=muted claim=warn pods=muted node=muted" || !strings.HasPrefix(segText(d.Intro), "The chain breaks at the class: no class named fast-ssd") {
		t.Errorf("missing class: %s / %s", got, segText(d.Intro))
	}
}

// A bound claim's chain holds all the way to its usage; the Lifecycle card
// says what the reclaim policy and expansion mean (ST-16).
func TestStorageChainOfABoundClaim(t *testing.T) {
	src := storageSource()
	c := pvc("storage", "data-postgres-0", ptr("longhorn"), "100Gi", corev1.ClaimBound, "pvc-pg0")
	src.Put(c)
	v := pv("pvc-pg0", "longhorn", "100Gi", corev1.VolumeBound, "storage", "data-postgres-0", corev1.PersistentVolumeReclaimDelete)
	v.Annotations = map[string]string{"pv.kubernetes.io/provisioned-by": "driver.longhorn.io"}
	src.Put(v)
	src.Put(mounting("storage", "postgres-0", "worker-1", "data-postgres-0"))
	att := &storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "csi-3a9f1e7c0b2d", CreationTimestamp: ts(41 * 24 * time.Hour)}, Spec: storagev1.VolumeAttachmentSpec{NodeName: "worker-1", Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: ptr("pvc-pg0")}}, Status: storagev1.VolumeAttachmentStatus{Attached: true}}
	src.Put(att)
	src.SetVolumeStats("storage", "data-postgres-0", kube.VolumeStats{UsedBytes: 62 * (1 << 30), CapacityBytes: 100 * (1 << 30), InodesUsed: 1_100_000, Inodes: 6_400_000, Node: "worker-1"})
	d, _ := views.ClaimDetails(src, "storage", "data-postgres-0", all, now)
	if got := tones(d.Hops); got != "class=ok volume=ok claim=ok pods=ok node=ok usage=ok" || d.Broken {
		t.Errorf("tones %s", got)
	}
	if got := segText(d.Intro); got != "Every link in the chain that has to hold for postgres-0 to read and write this volume, from the class down to the node." {
		t.Errorf("intro %s", got)
	}
	if got := segText(d.Hops[4].Desc); got != "VolumeAttachment csi-3a9f1e7c0b2d · attached 41d ago, when postgres-0 was scheduled here · one node at a time" {
		t.Errorf("node hop %s", got)
	}
	if got := segText(d.Hops[5].Desc); got != "62% used · 1.1M of 6.4M inodes · reported by the kubelet on worker-1" || d.Hops[5].State != "room to grow" {
		t.Errorf("usage hop %s %s", got, d.Hops[5].State)
	}
	if len(d.Lifecycle) != 3 || segText(d.Lifecycle[0]) != "Reclaim policy Delete — deleting the claim deletes the volume and its data. To keep the data, set the volume to Retain first." {
		t.Errorf("lifecycle %v", d.Lifecycle)
	}
	// Without the volume and class reads, the drawer says what the claim says.
	d, _ = views.ClaimDetails(src, "storage", "data-postgres-0", views.StorageCan{Pods: true}, now)
	if d.Pair.Reclaim != "" || d.Pair.Source != nil || d.Class != nil || d.Attachment != nil || d.Hops[1].Value[0].L != nil {
		t.Errorf("redacted drawer %+v", d.Pair)
	}
}
