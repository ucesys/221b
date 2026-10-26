package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/views"
)

// The Storage tab's endpoints (ST-13, ST-20). Its rows come through the
// views like every tab's; these serve what a person may read beyond them,
// and the drawers. Every answer is cut to what Kubernetes lets the person
// list: the drawer of a claim needs the claims of its namespace, a
// volume's needs persistentvolumes, and what the claim's drawer shows of
// its volume, class, attachment and pods depends on each.

// storageScope is what the person may read of the cluster-scoped kinds:
// By class and the volume and class drawers need classes and volumes
// (ST-02, ST-11).
type storageScope struct {
	Classes     bool `json:"classes"`
	Volumes     bool `json:"volumes"`
	Attachments bool `json:"attachments"`
}

// storageCan asks Kubernetes, as the person, what they may list for the
// Storage tab: the cluster-scoped kinds and, with a namespace, its pods.
func (s *Server) storageCan(ctx context.Context, r *http.Request, cl Cluster, namespace string) (views.StorageCan, error) {
	a := s.access(r, cl)
	if a == nil {
		return views.StorageCan{Volumes: true, Classes: true, Attachments: true, Pods: true}, nil
	}
	pairs := []access.Pair{
		{Group: "storage.k8s.io", Resource: "storageclasses"},
		{Group: "", Resource: "persistentvolumes"},
		{Group: "storage.k8s.io", Resource: "volumeattachments"},
	}
	if namespace != "" {
		pairs = append(pairs, access.Pair{Group: "", Resource: "pods", Namespace: namespace})
	}
	if a.Stale(pairs) {
		ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
		defer cancel()
		if _, err := a.Prepare(ctx, pairs, 0); err != nil {
			return views.StorageCan{}, err
		}
	}
	return views.StorageCan{
		Classes:     a.Cluster("storageclasses"),
		Volumes:     a.Cluster("persistentvolumes"),
		Attachments: a.Cluster("volumeattachments"),
		Pods:        namespace == "" || a.Namespaced("pods", namespace),
	}, nil
}

func (s *Server) storageScope(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	can, err := s.storageCan(r.Context(), r, cl, "")
	if err != nil {
		jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
		return
	}
	b, _ := json.Marshal(storageScope{Classes: can.Classes, Volumes: can.Volumes, Attachments: can.Attachments})
	writeJSON(w, r, b)
}

// storageClaim is the drawer of a claim (ST-13 to ST-16).
func (s *Server) storageClaim(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !s.mayList(w, r, cl, "persistentvolumeclaims", ns) {
		return
	}
	can, err := s.storageCan(r.Context(), r, cl, ns)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
		return
	}
	d, ok := views.ClaimDetails(cl, ns, name, can, s.opts.Now().UTC())
	if !ok {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no volume claim %s/%s: it may have been deleted", ns, name))
		return
	}
	s.writeStorage(w, r, d)
}

// storageVolume is the drawer of a volume; only someone who may list
// volumes gets it (ST-20). A volume a claim holds shows the claim's chain
// when the person may read the claims of its namespace.
func (s *Server) storageVolume(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if !s.mayListScope(w, r, cl, "persistentvolumes", "", true) {
		return
	}
	can, err := s.storageCan(r.Context(), r, cl, "")
	if err != nil {
		jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
		return
	}
	name := r.PathValue("name")
	if ns, claim, ok := views.HeldBy(cl, name); ok && s.mayReadClaims(r, cl, ns) {
		can, err := s.storageCan(r.Context(), r, cl, ns)
		if err != nil {
			jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
			return
		}
		if d, ok := views.HeldVolumeDetails(cl, name, ns, claim, can, s.opts.Now().UTC()); ok {
			s.writeStorage(w, r, d)
			return
		}
	}
	d, ok := views.VolumeDetails(cl, name, can, s.opts.Now().UTC())
	if !ok {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no volume %s: it may have been deleted", name))
		return
	}
	s.writeStorage(w, r, d)
}

// mayReadClaims says, without answering the request, whether the person
// may list the claims of a namespace.
func (s *Server) mayReadClaims(r *http.Request, cl Cluster, namespace string) bool {
	a := s.access(r, cl)
	if a == nil {
		return true
	}
	pairs := []access.Pair{{Resource: "persistentvolumeclaims", Namespace: namespace}}
	if a.Stale(pairs) {
		ctx, cancel := context.WithTimeout(r.Context(), prepareTimeout)
		defer cancel()
		if _, err := a.Prepare(ctx, pairs, 0); err != nil {
			return false
		}
	}
	return a.Namespaced("persistentvolumeclaims", namespace)
}

func (s *Server) writeStorage(w http.ResponseWriter, r *http.Request, d views.StorageDetails) {
	b, err := json.Marshal(d)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "encoding the storage details: "+err.Error())
		return
	}
	writeJSON(w, r, b)
}
