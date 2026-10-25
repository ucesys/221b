package sim

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// The Storage tab's scenario (ST-23), as the mockups in docs/design/storage
// draw it: a default replicated class (longhorn) with a claim it cannot
// provision, a local class that binds on first use with a claim waiting
// for its pod, an NFS class with shared volumes and a Released one,
// volumes made by hand, a claim naming a class that does not exist, and
// one volume at 86%. It replaces the generic claims of the generated
// StatefulSets, so the tab shows the mockup row for row.

const gi = int64(1) << 30

// stVolume is one volume of the scenario and the claim it is bound to.
type stVolume struct {
	name, class, ns, claim string
	size                   string
	modes                  []corev1.PersistentVolumeAccessMode
	reclaim                corev1.PersistentVolumeReclaimPolicy
	age                    time.Duration
	// pods mount the claim: name, node (a worker number, 0 for none),
	// container, mount path, read-only.
	pods []stPod
	// usedGi of capacityGi, as the kubelet reports it.
	usedGi, fsGi float64
	release      string
	// source: csi (the class's driver), local, or nfs server:path.
	source string
	// phase: "" for Bound, or Released / Available.
	phase corev1.PersistentVolumePhase
}

type stPod struct {
	name      string
	worker    int
	container string
	path      string
	readOnly  bool
	// age is in days.
	age time.Duration
}

func (s *Sim) storageSeed(now time.Time) {
	var workers []string
	for name, n := range s.nodes {
		if _, cp := n.Labels["node-role.kubernetes.io/control-plane"]; !cp {
			workers = append(workers, name)
		}
	}
	sort.Strings(workers)
	if len(workers) == 0 {
		return
	}
	worker := func(i int) string {
		if i <= 0 {
			return ""
		}
		return workers[(i-1)%len(workers)]
	}
	day := func(d float64) time.Time {
		return now.Add(-time.Duration(d * float64(24*time.Hour))).Truncate(time.Second)
	}

	// The generated StatefulSets' claims make way for the scenario's.
	for key := range s.objects["persistentvolumeclaims"] {
		delete(s.objects["persistentvolumeclaims"], key)
	}
	for _, p := range s.pods {
		var vols []corev1.Volume
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				vols = append(vols, v)
			}
		}
		p.Spec.Volumes = vols
		for i := range p.Spec.Containers {
			var ms []corev1.VolumeMount
			for _, m := range p.Spec.Containers[i].VolumeMounts {
				if m.Name != "data" {
					ms = append(ms, m)
				}
			}
			p.Spec.Containers[i].VolumeMounts = ms
		}
	}
	for _, o := range s.objects["statefulsets"] {
		o.(*appsv1.StatefulSet).Spec.VolumeClaimTemplates = nil
	}

	// The classes.
	del, ret := corev1.PersistentVolumeReclaimDelete, corev1.PersistentVolumeReclaimRetain
	imm, wffc := storagev1.VolumeBindingImmediate, storagev1.VolumeBindingWaitForFirstConsumer
	class := func(name, provisioner string, reclaim corev1.PersistentVolumeReclaimPolicy, mode storagev1.VolumeBindingMode, expand, def bool, params map[string]string, mountOptions []string) {
		c := &storagev1.StorageClass{TypeMeta: typeMeta("StorageClass"), ObjectMeta: objectMeta("", name, day(180), nil), Provisioner: provisioner, ReclaimPolicy: &reclaim, VolumeBindingMode: &mode, AllowVolumeExpansion: ptr.To(expand), Parameters: params, MountOptions: mountOptions}
		if def {
			c.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
		}
		s.addLocked(c)
	}
	class("longhorn", "driver.longhorn.io", del, imm, true, true, map[string]string{"numberOfReplicas": "3", "staleReplicaTimeout": "2880", "fsType": "ext4", "dataLocality": "best-effort"}, nil)
	class("local-nvme", "kubernetes.io/no-provisioner", ret, wffc, false, false, nil, nil)
	class("nfs-shared", "nfs.csi.k8s.io", ret, imm, false, false, map[string]string{"server": "10.0.3.5", "share": "/exports/shared"}, []string{"nfsvers=4.1", "hard"})

	rwo := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	rwx := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	rox := []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}
	vols := []stVolume{
		{name: "pvc-8f2c41d6-5a1e-4c2b-9f0a-3d7e1b6c8a21", class: "longhorn", ns: "storage", claim: "data-postgres-0", size: "100Gi", modes: rwo, reclaim: del, age: 41, usedGi: 61.2, fsGi: 98.3, release: "pg-primary", source: "csi",
			pods: []stPod{{name: "postgres-0", worker: 1, container: "postgres", path: "/var/lib/postgresql/data", age: 41}}},
		{name: "pvc-2b7d9e03-61c4-47a8-b3f5-8e2a0c9d4f17", class: "longhorn", ns: "storage", claim: "data-postgres-1", size: "100Gi", modes: rwo, reclaim: del, age: 41, usedGi: 60.4, fsGi: 98.3, release: "pg-primary", source: "csi",
			pods: []stPod{{name: "postgres-1", worker: 4, container: "postgres", path: "/var/lib/postgresql/data", age: 41}}},
		{name: "pvc-c41a7f58-0d2e-4b96-a7c3-5f1e8d2b6a09", class: "longhorn", ns: "monitoring", claim: "prometheus-db-prometheus-0", size: "200Gi", modes: rwo, reclaim: del, age: 67, usedGi: 171.6, fsGi: 199.5, source: "csi",
			pods: []stPod{{name: "prometheus-0", worker: 2, container: "prometheus", path: "/prometheus", age: 12}}},
		{name: "pvc-5e19d0b2-3a8f-4e61-9c27-d04b7a1e5c33", class: "longhorn", ns: "ingest", claim: "data-kafka-0", size: "100Gi", modes: rwo, reclaim: del, age: 33, usedGi: 43.8, fsGi: 98.3, source: "csi",
			pods: []stPod{{name: "kafka-0", worker: 3, container: "kafka", path: "/var/lib/kafka", age: 33}}},
		{name: "pvc-9a0c3e77-b5d1-4f20-8e6a-2c7f9b3d1e48", class: "longhorn", ns: "ingest", claim: "data-kafka-1", size: "100Gi", modes: rwo, reclaim: del, age: 33, usedGi: 45.1, fsGi: 98.3, source: "csi",
			pods: []stPod{{name: "kafka-1", worker: 5, container: "kafka", path: "/var/lib/kafka", age: 33}}},
		{name: "pvc-d2f84b19-7c6e-4a35-b1d8-9e0a4c6f2b71", class: "longhorn", ns: "ingest", claim: "data-kafka-2", size: "100Gi", modes: rwo, reclaim: del, age: 33, usedGi: 42.7, fsGi: 98.3, source: "csi",
			pods: []stPod{{name: "kafka-2", worker: 7, container: "kafka", path: "/var/lib/kafka", age: 33}}},
		{name: "pvc-71e3a6c0-4d9b-4e82-a5f1-6b8c2d0e9a54", class: "longhorn", ns: "analytics", claim: "data-cache-0", size: "20Gi", modes: rwo, reclaim: del, age: 8, usedGi: 14.2, fsGi: 19.6, release: "analytics-cache", source: "csi",
			pods: []stPod{{name: "cache-0", worker: 2, container: "main", path: "/var/lib/cache", age: 8}}},
		{name: "pvc-b8d05f42-e2a7-4c19-9d3b-1f6e8a4c7d26", class: "longhorn", ns: "analytics", claim: "data-cache-1", size: "20Gi", modes: rwo, reclaim: del, age: 8, usedGi: 13.9, fsGi: 19.6, release: "analytics-cache", source: "csi",
			pods: []stPod{{name: "cache-1", worker: 6, container: "main", path: "/var/lib/cache", age: 8}}},

		{name: "local-pv-w001-nvme0", class: "local-nvme", ns: "ingest", claim: "buffer-nifi-0", size: "500Gi", modes: rwo, reclaim: ret, age: 52, usedGi: 104, fsGi: 491, source: "local",
			pods: []stPod{{name: "nifi-0", worker: 1, container: "nifi", path: "/opt/nifi/buffer", age: 52}}},
		{name: "local-pv-w002-nvme0", class: "local-nvme", ns: "ingest", claim: "buffer-nifi-1", size: "500Gi", modes: rwo, reclaim: ret, age: 52, usedGi: 98, fsGi: 491, source: "local",
			pods: []stPod{{name: "nifi-1", worker: 2, container: "nifi", path: "/opt/nifi/buffer", age: 52}}},
		{name: "local-pv-w003-nvme0", class: "local-nvme", ns: "ingest", claim: "buffer-nifi-2", size: "500Gi", modes: rwo, reclaim: ret, age: 52, usedGi: 101, fsGi: 491, source: "local",
			pods: []stPod{{name: "nifi-2", worker: 3, container: "nifi", path: "/opt/nifi/buffer", age: 52}}},
		{name: "local-pv-w004-nvme0", class: "local-nvme", ns: "ingest", claim: "buffer-nifi-3", size: "500Gi", modes: rwo, reclaim: ret, age: 52, usedGi: 107, fsGi: 491, source: "local",
			pods: []stPod{{name: "nifi-3", worker: 4, container: "nifi", path: "/opt/nifi/buffer", age: 52}}},
		{name: "local-pv-w005-nvme0", class: "local-nvme", ns: "analytics", claim: "spill-dremio-executor-0", size: "500Gi", modes: rwo, reclaim: ret, age: 19, usedGi: 205, fsGi: 491, release: "dremio", source: "local",
			pods: []stPod{{name: "dremio-executor-0", worker: 5, container: "executor", path: "/opt/dremio/spill", age: 19}}},
		{name: "local-pv-w006-nvme0", class: "local-nvme", ns: "analytics", claim: "spill-dremio-executor-1", size: "500Gi", modes: rwo, reclaim: ret, age: 19, usedGi: 190, fsGi: 491, release: "dremio", source: "local",
			pods: []stPod{{name: "dremio-executor-1", worker: 6, container: "executor", path: "/opt/dremio/spill", age: 19}}},
		{name: "local-pv-w007-nvme0", class: "local-nvme", ns: "analytics", claim: "spill-dremio-executor-2", size: "500Gi", modes: rwo, reclaim: ret, age: 19, usedGi: 220, fsGi: 491, release: "dremio", source: "local",
			pods: []stPod{{name: "dremio-executor-2", worker: 7, container: "executor", path: "/opt/dremio/spill", age: 19}}},
		{name: "local-pv-w003-nvme1", class: "local-nvme", size: "500Gi", modes: rwo, reclaim: ret, age: 30, source: "local", phase: corev1.VolumeAvailable},

		{name: "pvc-3c9e2a84-f6b1-4d07-8a52-c1e9d7b3f0a6", class: "nfs-shared", ns: "analytics", claim: "shared-datasets", size: "2Ti", modes: rwx, reclaim: ret, age: 120, usedGi: 1310.7, fsGi: 2048, release: "datasets", source: "csi",
			pods: []stPod{
				{name: "dremio-executor-0", worker: 5, container: "executor", path: "/data/datasets", readOnly: true, age: 19},
				{name: "dremio-executor-1", worker: 6, container: "executor", path: "/data/datasets", readOnly: true, age: 19},
				{name: "dremio-executor-2", worker: 7, container: "executor", path: "/data/datasets", readOnly: true, age: 19},
				{name: "dremio-coordinator-0", worker: 5, container: "coordinator", path: "/data/datasets", readOnly: true, age: 19},
				{name: "datasets-sync-7c9d4", worker: 6, container: "sync", path: "/data", age: 6},
				{name: "notebook-0", worker: 7, container: "notebook", path: "/home/jovyan/datasets", age: 3},
			}},
		{name: "pvc-e07b4d61-29a3-4c58-b6e0-7d2f1a8c5b94", class: "nfs-shared", ns: "batch", claim: "reports-out", size: "200Gi", modes: rwx, reclaim: ret, age: 60, usedGi: 61.5, fsGi: 200, release: "reports", source: "csi",
			pods: []stPod{
				{name: "report-runner-0", worker: 3, container: "runner", path: "/out", age: 2},
				{name: "report-runner-1", worker: 5, container: "runner", path: "/out", age: 2},
				{name: "report-29pzq", container: "runner", path: "/out", age: 19},
			}},
		{name: "pvc-a64f1c28-5e7d-4b93-9f16-0c3a8e2d7b65", class: "nfs-shared", ns: "web", claim: "media", size: "200Gi", modes: rwx, reclaim: ret, age: 90, usedGi: 35.9, fsGi: 200, source: "csi",
			pods: []stPod{
				{name: "media-6f9c7-2x4kq", worker: 1, container: "media", path: "/srv/media", age: 9},
				{name: "media-6f9c7-8bn2w", worker: 2, container: "media", path: "/srv/media", age: 9},
				{name: "media-6f9c7-p7rt5", worker: 4, container: "media", path: "/srv/media", age: 9},
				{name: "media-6f9c7-w9zl3", worker: 4, container: "media", path: "/srv/media", age: 9},
			}},
		{name: "pvc-6d2b8e95-1f4a-4c70-a8d3-e5b9c2f6a187", class: "nfs-shared", ns: "ingest", claim: "landing-zone", size: "100Gi", modes: rwx, reclaim: ret, age: 150, source: "csi", phase: corev1.VolumeReleased},

		{name: "legacy-hdfs-export", class: "", ns: "ingest", claim: "hdfs-export", size: "1Ti", modes: rox, reclaim: ret, age: 210, usedGi: 788, fsGi: 1024, source: "nfs 10.0.3.5:/hdfs",
			pods: []stPod{
				{name: "hdfs-export-5d8b9-k2m4p", worker: 2, container: "export", path: "/mnt/hdfs", readOnly: true, age: 14},
				{name: "hdfs-export-5d8b9-q8v7n", worker: 3, container: "export", path: "/mnt/hdfs", readOnly: true, age: 14},
				{name: "hdfs-export-5d8b9-t4c6h", worker: 3, container: "export", path: "/mnt/hdfs", readOnly: true, age: 14},
			}},
		{name: "archive-tapes-2024", class: "", size: "500Gi", modes: rox, reclaim: ret, age: 30, source: "nfs 10.0.3.5:/archive-2024", phase: corev1.VolumeAvailable},
	}

	drivers := map[string]string{"longhorn": "driver.longhorn.io", "nfs-shared": "nfs.csi.k8s.io"}
	for _, v := range vols {
		created := day(float64(v.age))
		pv := &corev1.PersistentVolume{TypeMeta: typeMeta("PersistentVolume"), ObjectMeta: objectMeta("", v.name, created, nil)}
		pv.Spec = corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.size)}, AccessModes: v.modes, PersistentVolumeReclaimPolicy: v.reclaim, StorageClassName: v.class, VolumeMode: ptr.To(corev1.PersistentVolumeFilesystem)}
		switch {
		case v.source == "csi":
			pv.Spec.CSI = &corev1.CSIPersistentVolumeSource{Driver: drivers[v.class], VolumeHandle: v.name, FSType: "ext4"}
			if v.class == "nfs-shared" {
				pv.Spec.CSI.VolumeHandle = "10.0.3.5#exports/shared#" + v.name
				pv.Spec.CSI.FSType = ""
			}
			pv.Annotations = map[string]string{"pv.kubernetes.io/provisioned-by": drivers[v.class]}
		case v.source == "local":
			node := worker(int(v.name[len("local-pv-w00")] - '0'))
			pv.Spec.Local = &corev1.LocalVolumeSource{Path: "/mnt/nvme" + v.name[len(v.name)-1:], FSType: ptr.To("xfs")}
			pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{node}}}}}}}
		default:
			server, path, _ := strings.Cut(strings.TrimPrefix(v.source, "nfs "), ":")
			pv.Spec.NFS = &corev1.NFSVolumeSource{Server: server, Path: path, ReadOnly: true}
		}
		switch v.phase {
		case corev1.VolumeReleased:
			ns, claim := v.ns, v.claim
			pv.Spec.ClaimRef = &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: ns, Name: claim, UID: uid("gone/" + ns + "/" + claim)}
			pv.Status = corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased, LastPhaseTransitionTime: ptr.To(metav1.NewTime(day(12)))}
			s.addLocked(pv)
			continue
		case corev1.VolumeAvailable:
			pv.Status = corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable, LastPhaseTransitionTime: ptr.To(metav1.NewTime(created))}
			s.addLocked(pv)
			continue
		}
		pv.Spec.ClaimRef = &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: v.ns, Name: v.claim, UID: uid(v.ns + "/" + v.claim)}
		pv.Status = corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound, LastPhaseTransitionTime: ptr.To(metav1.NewTime(created))}
		s.addLocked(pv)

		c := s.storageClaim(v.ns, v.claim, &v.class, v.size, v.modes, created, v.release)
		c.Spec.VolumeName = v.name
		c.Status = corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, AccessModes: v.modes, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.size)}}
		if v.class == "longhorn" {
			pv.Annotations["pv.kubernetes.io/provisioned-by"] = "driver.longhorn.io"
			s.event(v.ns, "PersistentVolumeClaim", v.claim, c.UID, corev1.EventTypeNormal, "ProvisioningSucceeded", "Successfully provisioned volume "+v.name, 1, created, created, "driver.longhorn.io_csi-provisioner", "")
		}
		s.addLocked(c)
		for i, sp := range v.pods {
			p := s.storagePod(v.ns, sp, worker(sp.worker), day(float64(sp.age)), v.claim, now)
			if v.class == "longhorn" && i == 0 && p.Spec.NodeName != "" {
				// The CSI driver attaches a block device to the pod's node.
				att := &storagev1.VolumeAttachment{TypeMeta: typeMeta("VolumeAttachment"), ObjectMeta: objectMeta("", fmt.Sprintf("csi-%x", uid("attach/"+v.name))[:16], created, nil)}
				att.Spec = storagev1.VolumeAttachmentSpec{Attacher: "driver.longhorn.io", NodeName: p.Spec.NodeName, Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: ptr.To(v.name)}}
				att.Status = storagev1.VolumeAttachmentStatus{Attached: true}
				s.addLocked(att)
				s.event(v.ns, "Pod", p.Name, p.UID, corev1.EventTypeNormal, "SuccessfulAttachVolume", `AttachVolume.Attach succeeded for volume "`+v.name+`"`, 1, created, created, "attachdetach-controller", "")
			}
		}
		if v.usedGi > 0 {
			s.volumeUsage[v.ns+"/"+v.claim] = [4]int64{int64(v.usedGi * float64(gi)), int64(v.fsGi * float64(gi)), int64(v.usedGi * float64(gi) / 55_000), int64(v.fsGi * float64(gi) / 16_000)}
		}
	}

	// A claim longhorn cannot provision: not enough disk space, and its
	// StatefulSet's pod waits for it.
	cacheClaim := s.storageClaim("analytics", "data-cache-2", ptr.To("longhorn"), "20Gi", rwo, now.Add(-(2*time.Hour + 14*time.Minute)), "analytics-cache")
	cacheClaim.Status = corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}
	s.addLocked(cacheClaim)
	at := cacheClaim.CreationTimestamp.Time
	s.event("analytics", "PersistentVolumeClaim", "data-cache-2", cacheClaim.UID, corev1.EventTypeWarning, "ProvisioningFailed", `failed to provision volume with StorageClass "longhorn": rpc error: code = ResourceExhausted desc = not enough disk space on any node for a 20Gi volume with 3 replicas`, 14, at, now.Add(-2*time.Minute), "driver.longhorn.io_csi-provisioner", "")
	s.event("analytics", "PersistentVolumeClaim", "data-cache-2", cacheClaim.UID, corev1.EventTypeNormal, "ExternalProvisioning", `Waiting for a volume to be created either by the external provisioner "driver.longhorn.io" or manually by the system administrator. If volume creation is delayed, please verify that the provisioner is running and correctly registered.`, 27, at, now.Add(-time.Minute), "persistentvolume-controller", "")
	cache2 := s.storagePod("analytics", stPod{name: "cache-2", container: "main", path: "/var/lib/cache"}, "", at, "data-cache-2", now)
	s.waiting(cache2, "0/"+fmt.Sprint(len(s.nodes))+" nodes are available: pod has unbound immediate PersistentVolumeClaims. preemption: 0/"+fmt.Sprint(len(s.nodes))+" nodes are available: "+fmt.Sprint(len(s.nodes))+" Preemption is not helpful for scheduling.", 9, at, now.Add(-4*time.Minute))

	// A claim of the local class waits for its pod to be scheduled.
	spill := s.storageClaim("analytics", "spill-dremio-executor-3", ptr.To("local-nvme"), "500Gi", rwo, now.Add(-3*time.Hour), "dremio")
	spill.Status = corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}
	s.addLocked(spill)
	s.event("analytics", "PersistentVolumeClaim", spill.Name, spill.UID, corev1.EventTypeNormal, "WaitForFirstConsumer", "waiting for first consumer to be created before binding", 12, now.Add(-3*time.Hour), now.Add(-90*time.Second), "persistentvolume-controller", "")
	exec3 := s.storagePod("analytics", stPod{name: "dremio-executor-3", container: "executor", path: "/opt/dremio/spill"}, "", now.Add(-3*time.Hour), spill.Name, now)
	s.waiting(exec3, "0/"+fmt.Sprint(len(s.nodes))+" nodes are available: 3 Insufficient memory, 3 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }.", 31, now.Add(-3*time.Hour), now.Add(-3*time.Minute))

	// A claim naming a class that does not exist.
	scratch := s.storageClaim("batch", "scratch-report", ptr.To("fast-ssd"), "100Gi", rwo, day(19), "")
	scratch.Status = corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}
	s.addLocked(scratch)
	s.event("batch", "PersistentVolumeClaim", "scratch-report", scratch.UID, corev1.EventTypeWarning, "ProvisioningFailed", `storageclass.storage.k8s.io "fast-ssd" not found`, 612, day(19), now.Add(-5*time.Minute), "persistentvolume-controller", "")
	sr := s.storagePod("batch", stPod{name: "scratch-report-5mv8t", container: "report", path: "/scratch"}, "", day(19), "scratch-report", now)
	s.waiting(sr, "0/"+fmt.Sprint(len(s.nodes))+" nodes are available: pod has unbound immediate PersistentVolumeClaims.", 870, day(19), now.Add(-6*time.Minute))

	// The report that waits for room, mounting a bound claim.
	if p := s.pods["batch/report-29pzq"]; p != nil {
		s.waiting(p, "0/"+fmt.Sprint(len(s.nodes))+" nodes are available: "+fmt.Sprint(len(s.nodes))+" Insufficient cpu.", 1640, day(19), now.Add(-8*time.Minute))
	}

	// The StatefulSets the claims come from.
	s.storageSet("storage", "postgres", 2, "data", day(41))
	s.storageSet("monitoring", "prometheus", 1, "prometheus-db", day(67))
	s.storageSet("ingest", "kafka", 3, "data", day(33))
	s.storageSet("ingest", "nifi", 4, "buffer", day(52))
	s.storageSet("analytics", "dremio-executor", 4, "spill", day(19))
	s.storageSet("analytics", "dremio-coordinator", 1, "", day(19))
	s.storageSet("analytics", "cache", 4, "data", day(8))
	s.storageSet("batch", "report-runner", 2, "", day(2))
	// The cache set stops at cache-2, which waits for its claim.
	for key, p := range s.pods {
		if n, err := strconv.Atoi(strings.TrimPrefix(p.Name, "cache-")); p.Namespace == "analytics" && strings.HasPrefix(p.Name, "cache-") && err == nil && n > 2 {
			delete(s.pods, key)
		}
	}

	// analytics has a storage quota; batch has none.
	q := &corev1.ResourceQuota{TypeMeta: typeMeta("ResourceQuota"), ObjectMeta: objectMeta("analytics", "storage", day(90), nil)}
	q.Spec.Hard = corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("8Ti"), "persistentvolumeclaims": resource.MustParse("40")}
	var used int64
	n := int64(0)
	for _, o := range s.objects["persistentvolumeclaims"] {
		if c := o.(*corev1.PersistentVolumeClaim); c.Namespace == "analytics" {
			used += c.Spec.Resources.Requests.Storage().Value()
			n++
		}
	}
	q.Status = corev1.ResourceQuotaStatus{Hard: q.Spec.Hard, Used: corev1.ResourceList{corev1.ResourceRequestsStorage: *resource.NewQuantity(used, resource.BinarySI), "persistentvolumeclaims": *resource.NewQuantity(n, resource.DecimalSI)}}
	s.addLocked(q)
}

// storageClaim makes a claim with the labels and annotations Helm puts on
// what it installs.
func (s *Sim) storageClaim(ns, name string, class *string, size string, modes []corev1.PersistentVolumeAccessMode, created time.Time, release string) *corev1.PersistentVolumeClaim {
	c := &corev1.PersistentVolumeClaim{TypeMeta: typeMeta("PersistentVolumeClaim"), ObjectMeta: objectMeta(ns, name, created, map[string]string{"app": strings.SplitN(name, "-", 2)[0]})}
	if release != "" {
		c.Labels["app.kubernetes.io/managed-by"] = "Helm"
		c.Labels["app.kubernetes.io/instance"] = release
		c.Annotations = map[string]string{"meta.helm.sh/release-name": release, "meta.helm.sh/release-namespace": ns}
	}
	c.Spec = corev1.PersistentVolumeClaimSpec{AccessModes: modes, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}}, StorageClassName: class, VolumeMode: ptr.To(corev1.PersistentVolumeFilesystem)}
	return c
}

// storagePod makes, or takes over, a pod that mounts a claim. A pod with
// no node is pending.
func (s *Sim) storagePod(ns string, sp stPod, node string, started time.Time, claim string, now time.Time) *corev1.Pod {
	key := ns + "/" + sp.name
	p := s.pods[key]
	if p == nil {
		p = s.newPod(ns, sp.name, node, 250, 512, now)
		p.Labels = map[string]string{"app": appOf(sp.name)}
		// What every generated pod has: a port, probes and the
		// ServiceAccount token.
		c := &p.Spec.Containers[0]
		c.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}
		c.LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8080)}}, InitialDelaySeconds: 10, PeriodSeconds: 10}
		c.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 5}
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "kube-api-access", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true})
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "kube-api-access", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr.To[int64](3607)}}}}}})
		s.pods[key] = p
	}
	p.CreationTimestamp = metav1.NewTime(started)
	p.Spec.NodeName = node
	if len(p.Spec.Containers) > 0 && p.Spec.Containers[0].Name == "main" && sp.container != "" {
		p.Spec.Containers[0].Name = sp.container
	}
	volName := claim
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: volName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}})
	p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: volName, MountPath: sp.path, ReadOnly: sp.readOnly})
	if node == "" {
		p.Status = corev1.PodStatus{Phase: corev1.PodPending}
		return p
	}
	if p.Status.Phase != corev1.PodRunning {
		st := metav1.NewTime(started)
		p.Status = corev1.PodStatus{StartTime: &st}
		runReady(p, started)
		if len(p.Status.ContainerStatuses) > 0 {
			p.Status.ContainerStatuses[0].Name = p.Spec.Containers[0].Name
		}
	}
	if n := s.nodes[node]; n != nil {
		p.Status.HostIP = address(n, corev1.NodeInternalIP)
		p.Status.PodIP = fmt.Sprintf("%s.%d", strings.TrimSuffix(n.Spec.PodCIDR, ".0/24"), 200+len(p.Spec.Volumes)%50)
		p.Status.PodIPs = []corev1.PodIP{{IP: p.Status.PodIP}}
		p.Status.QOSClass = corev1.PodQOSBurstable
	}
	return p
}

// waiting makes a pod with no node unschedulable, with the scheduler's
// message and its FailedScheduling events.
func (s *Sim) waiting(p *corev1.Pod, msg string, count int32, first, last time.Time) {
	p.Spec.NodeName = ""
	p.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: msg, LastTransitionTime: metav1.NewTime(first)}}}
	s.event(p.Namespace, "Pod", p.Name, p.UID, corev1.EventTypeWarning, "FailedScheduling", msg, count, first, last, "default-scheduler", "")
}

// storageSet makes, or adjusts, the StatefulSet whose pods are named
// <name>-<n> and whose claim template is template.
func (s *Sim) storageSet(ns, name string, replicas int32, template string, created time.Time) {
	key := ns + "/" + name
	var set *appsv1.StatefulSet
	if o, ok := s.objects["statefulsets"][key]; ok {
		set = o.(*appsv1.StatefulSet)
	} else {
		labels := map[string]string{"app": name}
		set = &appsv1.StatefulSet{TypeMeta: typeMeta("StatefulSet"), ObjectMeta: objectMeta(ns, name, created, labels)}
		set.Spec = appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, ServiceName: name, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "registry.example/" + name + ":1.0"}}}}}
		set.Generation = 1
		s.addLocked(set)
	}
	set.Spec.Replicas = ptr.To(replicas)
	set.Spec.VolumeClaimTemplates = nil
	if template != "" {
		set.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: template}}}
	}
	ctrl := true
	var ready int32
	for i := int32(0); i < replicas; i++ {
		p := s.pods[fmt.Sprintf("%s/%s-%d", ns, name, i)]
		if p == nil {
			continue
		}
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: name, UID: set.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
		p.Labels["app"] = name
		p.Labels["statefulset.kubernetes.io/pod-name"] = p.Name
		if podReady(p) {
			ready++
		}
	}
	set.Status = appsv1.StatefulSetStatus{Replicas: replicas, ReadyReplicas: ready, AvailableReplicas: ready, CurrentReplicas: replicas, UpdatedReplicas: replicas, ObservedGeneration: set.Generation}
}

// appOf is a pod's app from its name: "dremio-executor-0" → dremio-executor.
func appOf(name string) string {
	i := strings.LastIndex(name, "-")
	if i <= 0 {
		return name
	}
	return name[:i]
}
