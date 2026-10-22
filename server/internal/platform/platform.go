// Package platform tells which Kubernetes distribution a cluster runs, on
// which operating systems, with which add-ons, so that Sherlock's prompt
// speaks of that cluster's own services, files and commands. One console
// manages clusters of different kinds, so each question gets the profile
// of the cluster it is asked about.
package platform

import (
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Source is the cluster's cache, as the views read it.
type Source interface {
	Objects(kind, namespace string) []runtime.Object
}

// Distributions.
const (
	RKE2      = "rke2"
	K3s       = "k3s"
	EKS       = "eks"
	GKE       = "gke"
	AKS       = "aks"
	OpenShift = "openshift"
	Talos     = "talos"
	Kind      = "kind"
	Kubeadm   = "kubeadm"
)

var distNames = map[string]string{
	RKE2: "RKE2", K3s: "K3s", EKS: "Amazon EKS", GKE: "Google GKE", AKS: "Azure AKS",
	OpenShift: "OpenShift", Talos: "Talos Linux", Kind: "kind", Kubeadm: "kubeadm",
}

// Operating system families.
const (
	Debian       = "debian"
	Ubuntu       = "ubuntu"
	RHEL         = "rhel"
	RHCOS        = "rhcos"
	SUSE         = "suse"
	AL2023       = "al2023"
	AL2          = "al2"
	Bottlerocket = "bottlerocket"
	COS          = "cos"
	Flatcar      = "flatcar"
	TalosOS      = "talos"
	Windows      = "windows"
)

// Profile is what was detected of one cluster.
type Profile struct {
	// Distribution is one of the constants above, or "" when unknown.
	Distribution string
	Version      string
	// Nodes says the node facts below were read: the person may list
	// nodes. Without it only what the version tells is known.
	Nodes        bool
	ControlPlane []string
	OS           []OSGroup
	Runtimes     []string
	Kubelets     []string
	// AddOns says the add-ons below were read.
	AddOns         bool
	Network        string
	Ingress        []string
	LoadBalancer   string
	Storage        []string
	StorageClasses []string
	GPU            string
	GPUNodes       []string
	NPD            bool
	NodeExporter   bool
}

// OSGroup is the nodes that run one OS image and kernel.
type OSGroup struct {
	Family string
	Image  string
	Kernel string
	Nodes  []string
}

// What may be read for the person asking.
type Can struct {
	Nodes  bool
	AddOns bool
}

// FromVersion is the distribution as the version string tells it.
func FromVersion(version string) string {
	v := strings.ToLower(version)
	switch {
	case strings.Contains(v, "+rke2"):
		return RKE2
	case strings.Contains(v, "+k3s"):
		return K3s
	case strings.Contains(v, "-eks-"):
		return EKS
	case strings.Contains(v, "-gke."):
		return GKE
	}
	return ""
}

// Detect reads a cluster's profile from its cache.
func Detect(src Source, version string, can Can) Profile {
	p := Profile{Version: version, Distribution: FromVersion(version)}
	if can.Nodes {
		p.Nodes = true
		p.nodes(src)
	}
	if can.AddOns {
		p.AddOns = true
		p.addOns(src)
	}
	return p
}

func (p *Profile) nodes(src Source) {
	groups := map[[3]string]*OSGroup{}
	runtimes, kubelets := map[string]bool{}, map[string]bool{}
	var providers, labels []string
	for _, o := range src.Objects("nodes", "") {
		n, ok := o.(*corev1.Node)
		if !ok {
			continue
		}
		info := n.Status.NodeInfo
		fam := osFamily(info.OperatingSystem, info.OSImage, n.Labels)
		k := [3]string{fam, info.OSImage, info.KernelVersion}
		g := groups[k]
		if g == nil {
			g = &OSGroup{Family: fam, Image: info.OSImage, Kernel: info.KernelVersion}
			groups[k] = g
		}
		g.Nodes = append(g.Nodes, n.Name)
		if info.ContainerRuntimeVersion != "" {
			runtimes[info.ContainerRuntimeVersion] = true
		}
		if info.KubeletVersion != "" {
			kubelets[info.KubeletVersion] = true
		}
		_, cp := n.Labels["node-role.kubernetes.io/control-plane"]
		_, master := n.Labels["node-role.kubernetes.io/master"]
		if cp || master {
			p.ControlPlane = append(p.ControlPlane, n.Name)
		}
		if q, ok := n.Status.Capacity["nvidia.com/gpu"]; ok && !q.IsZero() {
			p.GPUNodes = append(p.GPUNodes, n.Name)
		}
		providers = append(providers, n.Spec.ProviderID)
		for l := range n.Labels {
			labels = append(labels, l)
		}
		if p.Distribution == "" && strings.HasPrefix(info.OSImage, "Talos") {
			p.Distribution = Talos
		}
	}
	if p.Distribution == "" {
		p.Distribution = fromNodes(providers, labels)
	}
	if p.Distribution == "" || p.Distribution == Kind {
		// kubeadm runs the control plane as static pods named after
		// their node; kind is kubeadm in containers.
		for _, o := range src.Objects("pods", "kube-system") {
			if pod, ok := o.(*corev1.Pod); ok && pod.Labels["component"] == "kube-apiserver" && pod.Labels["tier"] == "control-plane" {
				if p.Distribution == "" {
					p.Distribution = Kubeadm
				}
				break
			}
		}
	}
	for _, g := range groups {
		sort.Strings(g.Nodes)
		p.OS = append(p.OS, *g)
	}
	sort.Slice(p.OS, func(i, j int) bool {
		if len(p.OS[i].Nodes) != len(p.OS[j].Nodes) {
			return len(p.OS[i].Nodes) > len(p.OS[j].Nodes)
		}
		return p.OS[i].Image+p.OS[i].Kernel < p.OS[j].Image+p.OS[j].Kernel
	})
	sort.Strings(p.ControlPlane)
	sort.Strings(p.GPUNodes)
	p.Runtimes, p.Kubelets = keys(runtimes), keys(kubelets)
}

func fromNodes(providers, labels []string) string {
	has := func(prefix string, list []string) bool {
		for _, s := range list {
			if strings.HasPrefix(s, prefix) {
				return true
			}
		}
		return false
	}
	switch {
	case has("rke2://", providers):
		return RKE2
	case has("k3s://", providers):
		return K3s
	case has("kind://", providers):
		return Kind
	case has("node.openshift.io/", labels):
		return OpenShift
	case has("eks.amazonaws.com/", labels):
		return EKS
	case has("cloud.google.com/gke-", labels):
		return GKE
	case has("kubernetes.azure.com/", labels):
		return AKS
	}
	return ""
}

func osFamily(system, image string, labels map[string]string) string {
	if system == "windows" {
		return Windows
	}
	im := strings.ToLower(image)
	switch {
	case strings.Contains(im, "coreos") || labels["node.openshift.io/os_id"] == "rhcos":
		return RHCOS
	case strings.HasPrefix(im, "debian"):
		return Debian
	case strings.HasPrefix(im, "ubuntu"):
		return Ubuntu
	case strings.HasPrefix(im, "red hat enterprise linux"), strings.HasPrefix(im, "rocky"), strings.HasPrefix(im, "almalinux"),
		strings.HasPrefix(im, "centos"), strings.HasPrefix(im, "oracle linux"):
		return RHEL
	case strings.Contains(im, "suse"), strings.HasPrefix(im, "sle "), strings.HasPrefix(im, "sle micro"):
		return SUSE
	case strings.HasPrefix(im, "amazon linux 2023"):
		return AL2023
	case strings.HasPrefix(im, "amazon linux 2"):
		return AL2
	case strings.HasPrefix(im, "bottlerocket"):
		return Bottlerocket
	case strings.HasPrefix(im, "container-optimized os"):
		return COS
	case strings.HasPrefix(im, "flatcar"):
		return Flatcar
	case strings.HasPrefix(im, "talos"):
		return TalosOS
	}
	return ""
}

// The add-ons, matched on the names of DaemonSets and Deployments.
var networks = []struct{ match, name string }{
	{"cilium", "Cilium"}, {"canal", "Canal"}, {"calico", "Calico"}, {"aws-node", "Amazon VPC CNI"},
	{"azure-cns", "Azure CNI"}, {"anetd", "GKE Dataplane V2"}, {"antrea", "Antrea"}, {"kube-router", "kube-router"},
	{"kindnet", "kindnet"}, {"kube-ovn", "Kube-OVN"}, {"ovnkube", "OVN-Kubernetes"}, {"weave", "Weave Net"}, {"flannel", "Flannel"},
}

var storages = []struct{ match, name string }{
	{"local-path-provisioner", "local-path"}, {"longhorn-manager", "Longhorn"}, {"csi-rbdplugin", "Rook Ceph"},
	{"ebs-csi-node", "Amazon EBS CSI"}, {"efs-csi-node", "Amazon EFS CSI"}, {"pdcsi-node", "GCE PD CSI"},
	{"csi-azuredisk-node", "Azure Disk CSI"}, {"csi-azurefile-node", "Azure Files CSI"}, {"openebs", "OpenEBS"},
	{"nfs-subdir-external-provisioner", "NFS subdir provisioner"}, {"vsphere-csi-node", "vSphere CSI"},
}

func (p *Profile) addOns(src Source) {
	var names []string
	for _, kind := range []string{"daemonsets", "deployments"} {
		for _, o := range src.Objects(kind, "") {
			switch w := o.(type) {
			case *appsv1.DaemonSet:
				names = append(names, w.Namespace+"/"+w.Name)
			case *appsv1.Deployment:
				names = append(names, w.Namespace+"/"+w.Name)
			}
		}
	}
	sort.Strings(names)
	find := func(match string) bool {
		for _, n := range names {
			if strings.Contains(n, match) {
				return true
			}
		}
		return false
	}
	for _, n := range networks {
		if find(n.match) {
			p.Network = n.name
			break
		}
	}
	if p.Network == "" && (p.Distribution == K3s) {
		p.Network = "Flannel (built into K3s)"
	}
	for _, s := range storages {
		if find(s.match) {
			p.Storage = append(p.Storage, s.name)
		}
	}
	switch {
	case find("metallb"):
		p.LoadBalancer = "MetalLB"
	case find("kube-vip"):
		p.LoadBalancer = "kube-vip"
	case find("/svclb-"):
		p.LoadBalancer = "K3s ServiceLB"
	}
	switch {
	case find("gpu-operator"):
		p.GPU = "NVIDIA GPU Operator"
	case find("nvidia-device-plugin"):
		p.GPU = "NVIDIA device plugin"
	}
	p.NPD = find("node-problem-detector")
	p.NodeExporter = find("node-exporter")
	for _, o := range src.Objects("ingressclasses", "") {
		if ic, ok := o.(*networkingv1.IngressClass); ok {
			p.Ingress = append(p.Ingress, fmt.Sprintf("%s (%s)", ic.Name, ic.Spec.Controller))
		}
	}
	sort.Strings(p.Ingress)
	classes := map[string]bool{}
	for _, o := range src.Objects("persistentvolumeclaims", "") {
		if c, ok := o.(*corev1.PersistentVolumeClaim); ok && c.Spec.StorageClassName != nil && *c.Spec.StorageClassName != "" {
			classes[*c.Spec.StorageClassName] = true
		}
	}
	p.StorageClasses = keys(classes)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
