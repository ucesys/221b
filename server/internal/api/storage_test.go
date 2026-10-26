package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/views"
)

// putStorage gives the test cluster a class, a claim in web and one in
// kube-system with their volumes, and a Released volume.
func putStorage(s *source, st *live.Store) {
	reclaim := corev1.PersistentVolumeReclaimDelete
	mode := storagev1.VolumeBindingImmediate
	s.Put(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "longhorn", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}, Provisioner: "driver.longhorn.io", ReclaimPolicy: &reclaim, VolumeBindingMode: &mode})
	class := "longhorn"
	for _, c := range []struct{ ns, name, pv string }{{"web", "data", "pvc-web-data"}, {"kube-system", "etcd-backup", "pvc-ks-backup"}} {
		s.Put(&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: c.name, Namespace: c.ns, UID: types.UID("uid-" + c.ns + c.name)},
			Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &class, VolumeName: c.pv, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		})
		s.Put(&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: c.pv},
			Spec:       corev1.PersistentVolumeSpec{StorageClassName: class, PersistentVolumeReclaimPolicy: reclaim, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}, ClaimRef: &corev1.ObjectReference{Namespace: c.ns, Name: c.name}, PersistentVolumeSource: corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "10.0.3.5", Path: "/" + c.ns}}},
			Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		})
	}
	s.Put(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-released"},
		Spec:       corev1.PersistentVolumeSpec{StorageClassName: class, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: "kube-system", Name: "old"}},
		Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	})
	ch := kube.Change{}
	ch.Mark(live.ViewStorage, kube.AllRows)
	st.Notify(ch)
	st.Process()
}

// ST-20: a person bound to one namespace sees its claims and nothing else:
// no claim of another namespace in the view or the drawers, no volume or
// class rows, and of their own claim only what the claim says.
func TestStorageIsCutToWhatThePersonMayList(t *testing.T) {
	deny := map[string]bool{"persistentvolumes/": true, "storageclasses/": true, "volumeattachments/": true, "persistentvolumeclaims/kube-system": true, "pods/kube-system": true, "resourcequotas/kube-system": true}
	srv, viewer, _, _, local, store := multiSetupWith(t, "viewer@corp.example", deny)
	putStorage(local, store)

	r, body := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/storage", "")
	if r.StatusCode != 200 || body != `{"classes":false,"volumes":false,"attachments":false}` {
		t.Fatalf("scope %d %s", r.StatusCode, body)
	}
	r, body = send(t, viewer, http.MethodGet, srv.URL+"/api/v1/snapshot?views=storage", "")
	if r.StatusCode != 200 || !strings.Contains(body, `"pvc:web/data"`) {
		t.Fatalf("snapshot %d %s", r.StatusCode, body)
	}
	for _, hidden := range []string{"kube-system", "etcd-backup", "pvc-released", `"sc:`, "nfs", "10.0.3.5", `"reclaim"`} {
		if strings.Contains(body, hidden) {
			t.Errorf("the snapshot names %q: %s", hidden, body)
		}
	}
	r, body = send(t, viewer, http.MethodGet, srv.URL+"/api/v1/storage/claims/web/data", "")
	if r.StatusCode != 200 {
		t.Fatalf("own claim %d %s", r.StatusCode, body)
	}
	var d views.StorageDetails
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.Pair.Volume != "pvc-web-data" || d.Pair.Reclaim != "" || d.Pair.Source != nil || d.Class != nil || strings.Contains(body, "10.0.3.5") {
		t.Errorf("the claim's drawer shows more than the claim says: %s", body)
	}
	for _, path := range []string{"/api/v1/storage/claims/kube-system/etcd-backup", "/api/v1/storage/volumes/pvc-released", "/api/v1/clusters/local/storage/claims/kube-system/etcd-backup"} {
		if r, body := send(t, viewer, http.MethodGet, srv.URL+path, ""); r.StatusCode != http.StatusForbidden || strings.Contains(body, "pvc-ks-backup") {
			t.Errorf("%s: %d %s", path, r.StatusCode, body)
		}
	}
}

// Someone who may read everything gets every row and the volume's and
// class's facts in the drawer, under both paths.
func TestStorageDrawersForSomeoneWhoMayReadEverything(t *testing.T) {
	srv, ops, _, _, local, store := multiSetupWith(t, "ops@corp.example", nil)
	putStorage(local, store)
	_, body := send(t, ops, http.MethodGet, srv.URL+"/api/v1/snapshot?views=storage", "")
	for _, key := range []string{`"sc:longhorn"`, `"pvc:web/data"`, `"pvc:kube-system/etcd-backup"`, `"pv:pvc-released"`} {
		if !strings.Contains(body, key) {
			t.Errorf("no %s in %s", key, body)
		}
	}
	for _, base := range []string{"/api/v1/", "/api/v1/clusters/local/"} {
		r, body := send(t, ops, http.MethodGet, srv.URL+base+"storage/claims/web/data", "")
		var d views.StorageDetails
		if r.StatusCode != 200 || json.Unmarshal([]byte(body), &d) != nil || d.Pair.Reclaim != "Delete" || d.Class == nil || !d.Class.Default || len(d.Hops) != 6 {
			t.Fatalf("%s: %d %s", base, r.StatusCode, body)
		}
		// The volume a claim holds shows the claim, not "none".
		r, body = send(t, ops, http.MethodGet, srv.URL+base+"storage/volumes/pvc-web-data", "")
		if r.StatusCode != 200 || json.Unmarshal([]byte(body), &d) != nil || d.Pair.Claim != "data" || d.Hops[2].Value[0].L == nil || d.Hops[2].Value[0].L.Kind != "claim" {
			t.Fatalf("%s held volume: %d %s", base, r.StatusCode, body)
		}
		r, body = send(t, ops, http.MethodGet, srv.URL+base+"storage/volumes/pvc-released", "")
		if r.StatusCode != 200 || json.Unmarshal([]byte(body), &d) != nil || d.Pair.State != views.StateReleased || d.Pair.WasClaim != "kube-system/old" {
			t.Fatalf("%s volume: %d %s", base, r.StatusCode, body)
		}
	}
	if r, _ := send(t, ops, http.MethodGet, srv.URL+"/api/v1/storage/claims/web/nope", ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("a missing claim: %d", r.StatusCode)
	}
}
