package platform

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type fake map[string][]runtime.Object

func (f fake) Objects(kind, namespace string) []runtime.Object {
	var out []runtime.Object
	for _, o := range f[kind] {
		if m, ok := o.(metav1.Object); ok && namespace != "" && m.GetNamespace() != namespace {
			continue
		}
		out = append(out, o)
	}
	return out
}

func node(name, provider, image, kernel, runtime, kubelet string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: provider},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{
			OperatingSystem: "linux", OSImage: image, KernelVersion: kernel, ContainerRuntimeVersion: runtime, KubeletVersion: kubelet,
		}},
	}
}

func ds(ns, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func deploy(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func pvc(ns, name, class string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class}}
}

// rke2 is 221B's own cluster: RKE2 on Debian and RHEL nodes.
func rke2() fake {
	const v, rt = "v1.31.14+rke2r1", "containerd://2.1.5-k3s1"
	cp := map[string]string{"node-role.kubernetes.io/control-plane": "true", "node-role.kubernetes.io/etcd": "true"}
	deb, rhel := "Debian GNU/Linux 12 (bookworm)", "Red Hat Enterprise Linux 8.10 (Ootpa)"
	gpu := node("kloud", "rke2://kloud", rhel, "4.18.0-553.el8_10.x86_64", rt, v, nil)
	gpu.Status.Capacity = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("0")}
	return fake{
		"nodes": {
			node("rke2cp1", "rke2://rke2cp1", deb, "6.1.0-53-cloud-amd64", rt, v, cp),
			node("rke2cp2", "rke2://rke2cp2", deb, "6.1.0-54-cloud-amd64", rt, v, cp),
			node("rke2cp3", "rke2://rke2cp3", deb, "6.1.0-54-cloud-amd64", rt, v, cp),
			node("r620-worker", "rke2://r620-worker", deb, "6.1.0-53-cloud-amd64", rt, v, nil),
			gpu,
			node("r820", "rke2://r820", rhel, "4.18.0-553.el8_10.x86_64", rt, v, nil),
			node("r820b", "rke2://r820b", rhel, "4.18.0-553.151.1.el8_10.x86_64", rt, v, nil),
		},
		"daemonsets": {
			ds("kube-system", "rke2-canal"), ds("kube-system", "rke2-ingress-nginx-controller"), ds("kube-system", "nvidia-device-plugin-daemonset"),
			ds("metallb-system", "speaker"), ds("monitoring", "kps-prometheus-node-exporter"),
		},
		"deployments":            {deploy("kube-system", "rke2-coredns-rke2-coredns"), deploy("local-path-storage", "local-path-provisioner")},
		"ingressclasses":         {&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}}},
		"persistentvolumeclaims": {pvc("db", "data-0", "local-path"), pvc("db", "data-1", "c3-disk")},
	}
}

func TestRKE2OnDebianAndRHEL(t *testing.T) {
	p := Detect(rke2(), "v1.31.14+rke2r1", Can{Nodes: true, AddOns: true})
	if p.Distribution != RKE2 || p.Network != "Canal" || p.LoadBalancer != "MetalLB" || p.GPU != "NVIDIA device plugin" || len(p.GPUNodes) != 0 || p.NPD || !p.NodeExporter {
		t.Fatalf("profile: %+v", p)
	}
	if strings.Join(p.ControlPlane, ",") != "rke2cp1,rke2cp2,rke2cp3" {
		t.Errorf("control plane: %v", p.ControlPlane)
	}
	prompt := p.Prompt("local")
	for _, want := range []string{
		`This cluster: "local", RKE2, Kubernetes v1.31.14+rke2r1.`,
		"Debian GNU/Linux 12 (bookworm), kernel 6.1.0-54-cloud-amd64: rke2cp2, rke2cp3",
		"Red Hat Enterprise Linux 8.10 (Ootpa), kernel 4.18.0-553.151.1.el8_10.x86_64: r820b",
		"Network plugin: Canal.", "nginx (k8s.io/ingress-nginx)", "LoadBalancer addresses: MetalLB.",
		"Storage: local-path. Classes in use: c3-disk, local-path.",
		"no node reports nvidia.com/gpu", "No node-problem-detector.",
		// The sections of this cluster, and none of another's.
		"rke2-server", "Canal (Calico policy, Flannel VXLAN)", "MetalLB:", "local-path:", "NVIDIA GPUs:",
		"Without node-problem-detector", "node-exporter:", "Debian nodes:", "Red Hat-like nodes",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	for _, not := range []string{"K3s:", "Amazon EKS:", "Bottlerocket", "Ubuntu nodes:", "kubeadm:"} {
		if strings.Contains(prompt, not) {
			t.Errorf("the prompt has %q", not)
		}
	}
	// The same cluster gives the same prompt, so it is read from the cache.
	if again := Detect(rke2(), "v1.31.14+rke2r1", Can{Nodes: true, AddOns: true}).Prompt("local"); again != prompt {
		t.Error("the prompt changed between two detections of the same cluster")
	}
}

func TestEKSOnBottlerocketAndAL2023(t *testing.T) {
	const v = "v1.33.4-eks-1a2b3c4"
	ng := map[string]string{"eks.amazonaws.com/nodegroup": "general"}
	src := fake{
		"nodes": {
			node("ip-10-0-1-5.ec2.internal", "aws:///us-east-1a/i-1", "Bottlerocket OS 1.42.0 (aws-k8s-1.33)", "6.1.141", "containerd://1.7.27+bottlerocket", "v1.33.4-eks-1a2b3c4", ng),
			node("ip-10-0-2-9.ec2.internal", "aws:///us-east-1b/i-2", "Amazon Linux 2023.8.20250818", "6.1.147-172.266.amzn2023.x86_64", "containerd://1.7.27", "v1.33.4-eks-1a2b3c4", ng),
		},
		"daemonsets": {ds("kube-system", "aws-node"), ds("kube-system", "ebs-csi-node"), ds("kube-system", "kube-proxy")},
	}
	prompt := Detect(src, v, Can{Nodes: true, AddOns: true}).Prompt("prod")
	for _, want := range []string{"Amazon EKS, Kubernetes v1.33.4-eks-1a2b3c4", "Amazon VPC CNI", "Amazon EBS CSI", "Bottlerocket nodes:", "Amazon Linux 2023 nodes:", "SSM Session Manager"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	for _, not := range []string{"RKE2:", "rke2-server", "Canal", "Debian nodes:"} {
		if strings.Contains(prompt, not) {
			t.Errorf("the prompt has %q", not)
		}
	}
}

func TestKindNodesAreContainers(t *testing.T) {
	src := fake{
		"nodes":      {node("kind-control-plane", "kind://docker/kind/kind-control-plane", "Debian GNU/Linux 12 (bookworm)", "6.1.0-53-cloud-amd64", "containerd://2.1.3", "v1.33.1", map[string]string{"node-role.kubernetes.io/control-plane": ""})},
		"daemonsets": {ds("kube-system", "kindnet")},
		"pods":       {&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kube-apiserver-kind-control-plane", Labels: map[string]string{"component": "kube-apiserver", "tier": "control-plane"}}}},
	}
	prompt := Detect(src, "v1.33.1", Can{Nodes: true, AddOns: true}).Prompt("kind")
	if !strings.Contains(prompt, "kind, Kubernetes v1.33.1") || !strings.Contains(prompt, "The nodes are containers on one host") {
		t.Errorf("not kind:\n%s", prompt)
	}
	// The node image's OS is not the machine's.
	if strings.Contains(prompt, "Debian nodes:") {
		t.Error("kind's nodes got the Debian section")
	}
}

func TestKubeadmFromItsStaticPods(t *testing.T) {
	src := fake{
		"nodes": {node("cp-1", "", "Ubuntu 24.04.3 LTS", "6.8.0-79-generic", "containerd://2.1.4", "v1.34.1", nil)},
		"pods":  {&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kube-apiserver-cp-1", Labels: map[string]string{"component": "kube-apiserver", "tier": "control-plane"}}}},
	}
	prompt := Detect(src, "v1.34.1", Can{Nodes: true, AddOns: true}).Prompt("lab")
	for _, want := range []string{"kubeadm, Kubernetes v1.34.1", "/etc/kubernetes/manifests", "Ubuntu nodes:", "Network plugin: not recognised."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
}

// A person who may not list nodes learns nothing of them from the prompt:
// only what the version, which everyone sees, already tells.
func TestWithoutNodesOnlyTheVersionTells(t *testing.T) {
	prompt := Detect(rke2(), "v1.31.14+rke2r1", Can{}).Prompt("local")
	if !strings.Contains(prompt, "RKE2, Kubernetes v1.31.14+rke2r1") || !strings.Contains(prompt, "rke2-server") {
		t.Errorf("the distribution is missing:\n%s", prompt)
	}
	for _, not := range []string{"rke2cp1", "Debian", "Red Hat", "Canal", "MetalLB", "node-problem-detector"} {
		if strings.Contains(prompt, not) {
			t.Errorf("the prompt has %q", not)
		}
	}
	if !strings.Contains(prompt, "may not list nodes") {
		t.Error("the prompt does not say why it knows no more")
	}
}

func TestUnknownClusterGetsOnlyFacts(t *testing.T) {
	src := fake{"nodes": {node("n1", "", "Some Linux 1.0", "5.15.0", "containerd://1.7.0", "v1.30.2", nil)}}
	p := Detect(src, "v1.30.2", Can{Nodes: true})
	if p.Distribution != "" || len(p.Sections()) != 0 {
		t.Fatalf("profile %+v, sections %v", p, p.Sections())
	}
	if !strings.Contains(p.Prompt("x"), "its distribution was not recognised") {
		t.Error(p.Prompt("x"))
	}
}

// Every section a profile can name exists.
func TestEverySectionExists(t *testing.T) {
	var names []string
	for d := range distNames {
		names = append(names, "dist-"+d)
	}
	for _, s := range addOnSections {
		names = append(names, s)
	}
	for _, f := range []string{Debian, Ubuntu, RHEL, RHCOS, SUSE, AL2023, AL2, Bottlerocket, COS, Flatcar, Windows} {
		names = append(names, "os-"+f)
	}
	names = append(names, "gpu-nvidia", "npd", "no-npd", "node-exporter")
	for _, n := range names {
		if section(n) == "" {
			t.Errorf("no section %s", n)
		}
	}
}

func TestLongNodeListsAreCut(t *testing.T) {
	var nodes []runtime.Object
	for i := 0; i < 500; i++ {
		nodes = append(nodes, node(fmt.Sprintf("n%03d", i), "", "Ubuntu 24.04.3 LTS", "6.8.0", "containerd://2", "v1.34.1", nil))
	}
	f := Detect(fake{"nodes": nodes}, "v1.34.1", Can{Nodes: true}).Facts("big")
	if !strings.Contains(f, "and 488 more") || len(f) > 1000 {
		t.Errorf("facts of %d characters:\n%s", len(f), f)
	}
}
