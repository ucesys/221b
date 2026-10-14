package views

import (
	"encoding/json"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/nodes"
)

// ReleaseRow is one Helm release as the Helm tab lists it: its latest
// revision, with the pods it runs.
type ReleaseRow struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Chart     string `json:"chart"`
	// ChartVersion and AppVersion come from the chart's metadata.
	ChartVersion string     `json:"chartVersion"`
	AppVersion   string     `json:"appVersion"`
	Revision     int        `json:"revision"`
	Status       string     `json:"status"`
	Tone         string     `json:"tone"`
	Description  string     `json:"description"`
	Updated      *time.Time `json:"updated,omitempty"`
	First        *time.Time `json:"firstDeployed,omitempty"`
	Revisions    int        `json:"revisions"`
	// Pods the release runs: ready of total, and how many fail.
	Pods       int    `json:"pods"`
	PodsReady  int    `json:"podsReady"`
	PodsFailed int    `json:"podsFailed"`
	Group      string `json:"group"`
}

// addOnCharts are charts that run cluster add-ons rather than
// applications; the Helm tab lists them apart.
var addOnCharts = map[string]bool{
	"cert-manager": true, "ingress-nginx": true, "metrics-server": true, "longhorn": true, "kube-prometheus-stack": true,
	"metallb": true, "cilium": true, "calico": true, "coredns": true, "external-dns": true, "traefik": true, "cluster-autoscaler": true,
	"aws-load-balancer-controller": true, "nfs-subdir-external-provisioner": true, "rook-ceph": true, "velero": true, "kube-vip": true,
}

// releaseRevisions returns every revision of a release, newest first.
func releaseRevisions(src Source, namespace, name string) []*kube.HelmRelease {
	var out []*kube.HelmRelease
	for _, o := range src.ByIndex("secrets", kube.IndexRelease, namespace+"/"+name) {
		if s, ok := o.(*kube.SecretSummary); ok && s.Release != nil {
			out = append(out, s.Release)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out
}

// ReleaseKeys lists every release as "namespace/name".
func ReleaseKeys(src Source) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range src.Objects("secrets", "") {
		if s, ok := o.(*kube.SecretSummary); ok && s.Release != nil {
			k := s.Namespace + "/" + s.Release.Name
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// BuildReleaseRow computes a release's row, or ok=false when it has no
// revisions left.
func BuildReleaseRow(src Source, namespace, name string, now time.Time) (ReleaseRow, bool) {
	revs := releaseRevisions(src, namespace, name)
	if len(revs) == 0 {
		return ReleaseRow{}, false
	}
	latest := revs[0]
	r := ReleaseRow{
		Key: namespace + "/" + name, Name: name, Namespace: namespace,
		Chart: latest.Chart, ChartVersion: latest.Version, AppVersion: latest.App,
		Revision: latest.Revision, Status: latest.Status, Description: latest.Desc,
		Updated: latest.Deployed, Revisions: len(revs), Group: "apps",
	}
	if r.Updated == nil {
		r.Updated = latest.First
	}
	r.First = revs[len(revs)-1].First
	r.Tone = releaseTone(latest.Status)
	if addOnCharts[latest.Chart] || systemNamespace(namespace) {
		r.Group = "addons"
	}
	for _, p := range releasePods(src, namespace, name) {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		r.Pods++
		status, tone, _ := podStatus(p, now)
		if status == "Running" && tone == "ok" {
			r.PodsReady++
		}
		if tone == "err" {
			r.PodsFailed++
		}
	}
	return r, true
}

func releaseTone(status string) string {
	switch status {
	case "deployed":
		return "ok"
	case "failed":
		return "err"
	case "pending-install", "pending-upgrade", "pending-rollback", "uninstalling":
		return "warn"
	case "superseded", "uninstalled":
		return "muted"
	}
	return "muted"
}

// releasePods finds the pods of a release: those carrying the labels
// charts set and the annotation Helm adds, and those owned, through
// ReplicaSets and Jobs, by a workload the release's manifest creates.
// Charts that set neither label on their pod templates (and pods never
// carry Helm's own annotations, which go on the workload) are still found
// through the second route.
func releasePods(src Source, namespace, name string) []*corev1.Pod {
	owned := map[string]bool{}
	for _, rev := range releaseRevisions(src, namespace, name) {
		for _, res := range rev.Resources {
			if res.Namespace == "" || res.Namespace == namespace {
				owned[res.Kind+"/"+res.Name] = true
			}
		}
		break // the latest revision's manifest is what runs
	}
	var out []*corev1.Pod
	for _, o := range src.Objects("pods", namespace) {
		p, ok := o.(*corev1.Pod)
		if !ok {
			continue
		}
		mine := podRelease(p) == name
		if !mine && len(owned) > 0 {
			for _, owner := range ownerChain(src, namespace, p.OwnerReferences, 4) {
				if owned[owner.Kind+"/"+owner.Name] {
					mine = true
					break
				}
			}
		}
		if mine {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReleaseDetails is what the Helm tab shows for the selected release.
type ReleaseDetails struct {
	ReleaseRow
	Notes string `json:"notes"`
	// Values are the user-supplied values of the latest revision, with
	// credentials masked, as YAML; MaskedValues names what was masked.
	Values       string            `json:"values"`
	MaskedValues []string          `json:"maskedValues"`
	History      []Revision        `json:"history"`
	Resources    []ReleaseResource `json:"resources"`
	PodRows      []PodRow          `json:"podRows"`
	// Usage is CPU and memory in use by pod key, when metrics-server has
	// readings; the API fills it in.
	Usage map[string]nodes.Usage `json:"usage,omitempty"`
}

// Revision is one line of a release's history.
type Revision struct {
	Revision     int        `json:"revision"`
	Updated      *time.Time `json:"updated,omitempty"`
	Status       string     `json:"status"`
	Tone         string     `json:"tone"`
	Chart        string     `json:"chart"`
	ChartVersion string     `json:"chartVersion"`
	AppVersion   string     `json:"appVersion"`
	Description  string     `json:"description"`
}

// ReleaseResource is one object the release's manifest creates, with its
// state when the console watches its kind.
type ReleaseResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status,omitempty"`
	Tone      string `json:"tone,omitempty"`
	Details   string `json:"details,omitempty"`
	Watched   bool   `json:"watched"`
}

// BuildReleaseDetails computes the Helm tab's data for a release.
func BuildReleaseDetails(src Source, namespace, name string, now time.Time) (ReleaseDetails, bool) {
	row, ok := BuildReleaseRow(src, namespace, name, now)
	if !ok {
		return ReleaseDetails{}, false
	}
	revs := releaseRevisions(src, namespace, name)
	latest := revs[0]
	d := ReleaseDetails{ReleaseRow: row, Notes: latest.Notes, MaskedValues: latest.Masked, History: []Revision{}, Resources: []ReleaseResource{}, PodRows: []PodRow{}}
	if d.MaskedValues == nil {
		d.MaskedValues = []string{}
	}
	d.Values = valuesYAML(latest.Values)
	for _, r := range revs {
		d.History = append(d.History, Revision{
			Revision: r.Revision, Updated: r.Deployed, Status: r.Status, Tone: releaseTone(r.Status),
			Chart: r.Chart, ChartVersion: r.Version, AppVersion: r.App, Description: r.Desc,
		})
	}
	for _, res := range latest.Resources {
		rr := ReleaseResource{Kind: res.Kind, Name: res.Name, Namespace: res.Namespace}
		if rr.Namespace == "" {
			if k, ok := kube.KindOf(res.Kind); ok && k.Namespaced {
				rr.Namespace = namespace
			}
		}
		if k, ok := kube.KindOf(res.Kind); ok {
			ns := rr.Namespace
			if !k.Namespaced {
				ns = ""
			}
			if obj := src.Object(k.Name, ns, res.Name); obj != nil {
				if row, ok := objectRow(src, k, obj, now); ok {
					rr.Status, rr.Tone, rr.Details, rr.Watched = row.Status, row.Tone, row.Details, true
				}
			} else {
				rr.Status, rr.Tone, rr.Watched = "missing", "err", true
			}
		}
		d.Resources = append(d.Resources, rr)
	}
	for _, p := range releasePods(src, namespace, name) {
		d.PodRows = append(d.PodRows, BuildPodRow(src, p, now))
	}
	return d, true
}

// valuesYAML renders values as YAML the way helm get values does.
func valuesYAML(values json.RawMessage) string {
	if len(values) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(values, &v); err != nil {
		return ""
	}
	out, err := kube.ToYAML(v)
	if err != nil {
		return ""
	}
	return out
}
