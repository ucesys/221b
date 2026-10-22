package platform

import (
	"embed"
	"fmt"
	"strings"
)

// The guidance for each distribution, operating system and add-on, one
// file each. A prompt holds only those of its cluster.
//
//go:embed sections/*.md
var sectionFiles embed.FS

func section(name string) string {
	b, err := sectionFiles.ReadFile("sections/" + name + ".md")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// addOnSections maps an add-on's name to its section.
var addOnSections = map[string]string{
	"Canal": "net-canal", "Calico": "net-calico", "Cilium": "net-cilium", "Flannel": "net-flannel",
	"Flannel (built into K3s)": "net-flannel", "Amazon VPC CNI": "net-aws-vpc-cni",
	"MetalLB": "lb-metallb", "kube-vip": "lb-kube-vip", "K3s ServiceLB": "lb-servicelb",
	"local-path": "storage-local-path", "Longhorn": "storage-longhorn", "Rook Ceph": "storage-ceph",
	"Amazon EBS CSI": "storage-ebs", "GCE PD CSI": "storage-pd", "Azure Disk CSI": "storage-azure-disk",
}

// Sections are the guidance for this cluster's distribution, add-ons and
// operating systems, in that order.
func (p Profile) Sections() []string {
	var names []string
	if p.Distribution != "" {
		names = append(names, "dist-"+p.Distribution)
	}
	if p.AddOns {
		for _, a := range append([]string{p.Network, p.LoadBalancer}, p.Storage...) {
			if s := addOnSections[a]; s != "" {
				names = append(names, s)
			}
		}
		if p.GPU != "" {
			names = append(names, "gpu-nvidia")
		}
		if p.NPD {
			names = append(names, "npd")
		} else {
			names = append(names, "no-npd")
		}
		if p.NodeExporter {
			names = append(names, "node-exporter")
		}
	}
	// Nodes of kind are containers: their OS is the container image's,
	// and the hardware is the host's.
	if p.Nodes && p.Distribution != Kind {
		seen := map[string]bool{}
		for _, g := range p.OS {
			if g.Family != "" && !seen[g.Family] {
				seen[g.Family] = true
				names = append(names, "os-"+g.Family)
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if s := section(n); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// maxNames bounds the node names listed for one OS image and kernel.
const maxNames = 12

func nameList(names []string) string {
	if len(names) <= maxNames {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxNames], ", "), len(names)-maxNames)
}

// Facts describe the cluster as detected. They hold nothing that changes
// from minute to minute, such as which nodes are ready, so the prompt
// stays the same between questions and is read from the cache.
func (p Profile) Facts(cluster string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This cluster: %q", cluster)
	dist := distNames[p.Distribution]
	switch {
	case dist != "" && p.Version != "":
		fmt.Fprintf(&b, ", %s, Kubernetes %s.", dist, p.Version)
	case dist != "":
		fmt.Fprintf(&b, ", %s.", dist)
	case p.Version != "":
		fmt.Fprintf(&b, ", Kubernetes %s; its distribution was not recognised.", p.Version)
	default:
		b.WriteString(".")
	}
	b.WriteString(" Detected from its nodes and workloads; trust what the tools read now over this.")
	if !p.Nodes {
		b.WriteString("\n- The person may not list nodes, so nothing more is known of them here.")
		return b.String()
	}
	if len(p.ControlPlane) > 0 {
		fmt.Fprintf(&b, "\n- Control plane: %s.", nameList(p.ControlPlane))
	}
	if len(p.OS) > 0 {
		b.WriteString("\n- Nodes by operating system and kernel:")
		for _, g := range p.OS {
			image := g.Image
			if image == "" {
				image = "unknown OS"
			}
			fmt.Fprintf(&b, "\n  - %s, kernel %s: %s", image, g.Kernel, nameList(g.Nodes))
		}
	}
	if len(p.Runtimes) > 0 {
		fmt.Fprintf(&b, "\n- Container runtime: %s. Kubelet: %s.", strings.Join(p.Runtimes, ", "), strings.Join(p.Kubelets, ", "))
	}
	if !p.AddOns {
		return b.String()
	}
	if p.Network != "" {
		fmt.Fprintf(&b, "\n- Network plugin: %s.", p.Network)
	} else {
		b.WriteString("\n- Network plugin: not recognised.")
	}
	if len(p.Ingress) > 0 {
		fmt.Fprintf(&b, "\n- Ingress classes: %s.", strings.Join(p.Ingress, ", "))
	}
	if p.LoadBalancer != "" {
		fmt.Fprintf(&b, "\n- LoadBalancer addresses: %s.", p.LoadBalancer)
	}
	if len(p.Storage) > 0 || len(p.StorageClasses) > 0 {
		b.WriteString("\n- Storage:")
		if len(p.Storage) > 0 {
			fmt.Fprintf(&b, " %s.", strings.Join(p.Storage, ", "))
		}
		if len(p.StorageClasses) > 0 {
			fmt.Fprintf(&b, " Classes in use: %s.", strings.Join(p.StorageClasses, ", "))
		}
	}
	if p.GPU != "" {
		if len(p.GPUNodes) > 0 {
			fmt.Fprintf(&b, "\n- GPUs: %s; nodes with nvidia.com/gpu: %s.", p.GPU, nameList(p.GPUNodes))
		} else {
			fmt.Fprintf(&b, "\n- GPUs: %s, but no node reports nvidia.com/gpu.", p.GPU)
		}
	}
	var also []string
	if p.NodeExporter {
		also = append(also, "Prometheus node-exporter")
	}
	if p.NPD {
		also = append(also, "node-problem-detector")
	}
	if len(also) > 0 {
		fmt.Fprintf(&b, "\n- Also: %s.", strings.Join(also, ", "))
	}
	if !p.NPD {
		b.WriteString("\n- No node-problem-detector.")
	}
	return b.String()
}

// Prompt is the part of Sherlock's prompt about this cluster: its facts,
// then the guidance for its platform.
func (p Profile) Prompt(cluster string) string {
	parts := append([]string{p.Facts(cluster)}, p.Sections()...)
	return strings.Join(parts, "\n\n")
}
