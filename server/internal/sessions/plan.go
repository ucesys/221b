package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// Plan is what a session's Watson card shows (TB-36): the objects, what
// the session does, the server's dry run where Kubernetes has one, and
// the object it creates.
type Plan struct {
	Targets []string
	Impact  []string
	// DryRun lines, and its exit: 0 when the server accepts it.
	DryRun     []string
	DryRunExit int
	DryRunNote string
	// Object is the YAML of what the session creates or patches.
	Object string
}

// Asker is the backend's access check, as the person.
type Asker interface {
	Can(ctx context.Context, verb, group, resource, subresource, namespace, name string) (bool, string, error)
	Node(ctx context.Context, name string) (*corev1.Node, error)
}

// Can asks the API server whether the person may do something, with a
// SelfSubjectAccessReview made as the person.
func (b *KubeBackend) Can(ctx context.Context, verb, group, resource, subresource, namespace, name string) (bool, string, error) {
	r, err := b.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authzv1.ResourceAttributes{Verb: verb, Group: group, Resource: resource, Subresource: subresource, Namespace: namespace, Name: name},
	}}, metav1.CreateOptions{})
	if err != nil {
		return false, "", err
	}
	return r.Status.Allowed, r.Status.Reason, nil
}

// Node reads a node.
func (b *KubeBackend) Node(ctx context.Context, name string) (*corev1.Node, error) {
	return b.cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
}

// PlanFor works out a request's card as the person who asked: Kubernetes
// answers the dry run, or the access review where a session has no dry
// run (a shell, a command, a forward).
func (m *Manager) PlanFor(ctx context.Context, cl Cluster, r Request, owner kube.Person, ownerName string) Plan {
	var p Plan
	b, err := m.opts.Backend(cl, owner)
	if err != nil {
		p.DryRun, p.DryRunExit = []string{err.Error()}, 1
		return p
	}
	asker, _ := b.(Asker)
	who := ownerName
	if who == "" {
		who = "the console"
	}
	limit := m.opts.Config.Limit(minutes(r.Minutes))
	terms := fmt.Sprintf("Ends after %s without activity, or at %s.", short(m.opts.Config.IdleTimeout.D()), short(limit))
	if m.opts.Config.Transcripts == toolbox.TranscriptsPVC && Interactive(r.Tool) {
		terms += " Recorded."
	}
	review := func(verb, group, resource, sub, ns, name, what string) {
		if asker == nil {
			p.DryRunNote = "No dry run for " + what + "; Kubernetes decides when it opens."
			return
		}
		ok, why, err := asker.Can(ctx, verb, group, resource, sub, ns, name)
		res := resource
		if sub != "" {
			res += "/" + sub
		}
		switch {
		case err != nil:
			p.DryRun, p.DryRunExit = []string{"The access review failed: " + statusMessage(err)}, 1
		case ok:
			p.DryRun = []string{fmt.Sprintf("Kubernetes allows %s to %s %s in %s.", who, verb, res, nsName(ns))}
		default:
			msg := fmt.Sprintf("Kubernetes does not allow %s to %s %s in %s.", who, verb, res, nsName(ns))
			if why != "" {
				msg += " " + why
			}
			p.DryRun, p.DryRunExit = []string{msg}, 1
		}
		p.DryRunNote = "No dry run for " + what + ": what happens inside cannot be predicted. This is the access review, as " + who + "."
	}
	podCheck := func() *corev1.Pod {
		pod, err := b.Pod(ctx, r.Namespace, r.Pod)
		if err != nil {
			p.DryRun, p.DryRunExit = []string{statusMessage(err)}, 1
			return nil
		}
		return pod
	}
	switch r.Tool {
	case toolbox.ToolShell, toolbox.ToolCommand:
		p.Targets = []string{"pod/" + r.Pod + " -n " + r.Namespace}
		pod := podCheck()
		if pod == nil {
			return p
		}
		c := r.Container
		if c == "" && len(pod.Spec.Containers) > 0 {
			c = pod.Spec.Containers[0].Name
		}
		if r.Tool == toolbox.ToolShell {
			p.Impact = []string{fmt.Sprintf("Opens a terminal in container %s of pod %s, as %s.", c, r.Pod, who), terms}
		} else {
			p.Impact = []string{fmt.Sprintf("Runs in container %s of pod %s, as %s:", c, r.Pod, who), "  " + r.Command, "The command and its output go to the audit log."}
		}
		if ok, why := containerRunning(pod, c); !ok {
			if why == "" {
				why = "container " + c + " is not running"
			}
			p.DryRun, p.DryRunExit = []string{why}, 1
			return p
		}
		review("create", "", "pods", "exec", r.Namespace, r.Pod, map[bool]string{true: "a shell", false: "a command"}[r.Tool == toolbox.ToolShell])
	case toolbox.ToolForward:
		p.Targets = []string{"pod/" + r.Pod + " -n " + r.Namespace}
		if podCheck() == nil {
			return p
		}
		p.Impact = []string{fmt.Sprintf("Forwards port %d of pod %s to your computer through the 221b CLI, as %s.", r.Port, r.Pod, who), "Connections and bytes are recorded, never the data.", terms}
		review("create", "", "pods", "portforward", r.Namespace, r.Pod, "a port forward")
	case toolbox.ToolDebugContainer:
		p.Targets = []string{"pod/" + r.Pod + " -n " + r.Namespace}
		pod := podCheck()
		if pod == nil {
			return p
		}
		level := LevelPrivileged
		if ns, err := b.Namespace(ctx, r.Namespace); err == nil {
			level = PodSecurity(ns)
		}
		ec, err := EphemeralContainer(m.opts.Config, "plan0000", r, pod, level)
		if err != nil {
			p.DryRun, p.DryRunExit = []string{err.Error()}, 1
			return p
		}
		ec.Name = strings.TrimSuffix(ec.Name, "plan") + "…"
		p.Impact = []string{fmt.Sprintf("Adds debug container %s (%s) to pod %s; it sees the processes of %s.", ec.Name, ec.Image, r.Pod, ec.TargetContainerName), "It stays until the pod restarts and shares the pod's memory limit.", "Then opens a terminal in it. " + terms}
		if len(ec.VolumeMounts) > 0 {
			var names []string
			for _, v := range ec.VolumeMounts {
				names = append(names, v.Name+" at "+v.MountPath+map[bool]string{true: " (read-only)", false: ""}[v.ReadOnly])
			}
			p.Impact = append(p.Impact, "Mounts "+strings.Join(names, ", ")+".")
		}
		p.Object = toYAML(ec)
		probe, _ := EphemeralContainer(m.opts.Config, "plan0000", r, pod, level)
		err = b.AddEphemeral(ctx, pod, probe, true)
		p.dry(err, "pod/"+r.Pod+" ephemeral container added (server dry run)")
	case toolbox.ToolDebugPod:
		p.Targets = []string{"namespace/" + r.Namespace}
		level := LevelPrivileged
		if ns, err := b.Namespace(ctx, r.Namespace); err == nil {
			level = PodSecurity(ns)
		}
		var copied map[string]string
		if r.LabelsFrom != "" {
			src, err := b.Pod(ctx, r.Namespace, r.LabelsFrom)
			if err != nil {
				p.DryRun, p.DryRunExit = []string{statusMessage(err)}, 1
				return p
			}
			copied = src.Labels
		}
		job, notes, err := DebugJob(m.opts.Config, "plan0000plan0000", ownerName, r, level, copied, limit)
		if err != nil {
			p.DryRun, p.DryRunExit = []string{err.Error()}, 1
			return p
		}
		where := "any node"
		if r.Node != "" {
			where = "node " + r.Node
		}
		p.Impact = append([]string{fmt.Sprintf("Creates Job %s in %s with one pod on %s, image %s.", strings.Replace(job.Name, "plan0000", "…", 1), r.Namespace, where, job.Spec.Template.Spec.Containers[0].Image),
			fmt.Sprintf("It never becomes Ready, so no Service sends it traffic; it is deleted when the session ends, at %s at the latest.", short(limit))}, notes...)
		p.Impact = append(p.Impact, "Then opens a terminal in it. "+terms)
		p.Object = toYAML(job)
		err = b.CreateJob(ctx, job, true)
		p.dry(err, "job.batch/"+job.Name+" created (server dry run)")
	case toolbox.ToolNodeShell, toolbox.ToolNodeCommand:
		p.Targets = []string{"node/" + r.Node}
		pod, err := NodePod(m.opts.Config, "plan0000plan0000", ownerName, r, limit)
		if err != nil {
			p.DryRun, p.DryRunExit = []string{err.Error()}, 1
			return p
		}
		if asker != nil {
			if n, err := asker.Node(ctx, r.Node); err == nil && !nodeReady(n) {
				p.DryRun, p.DryRunExit = []string{fmt.Sprintf("Node %s is not Ready: the pod cannot start without a working kubelet.", r.Node)}, 1
				return p
			}
		}
		if r.Tool == toolbox.ToolNodeShell {
			p.Impact = []string{fmt.Sprintf("Creates pod %s in %s on node %s with the %s profile: %s.", strings.Replace(pod.Name, "plan0000", "…", 1), pod.Namespace, r.Node, r.Profile, profileWords(r.Profile)),
				"Root on the node is root over every pod that runs there, their Secrets included.", "Then opens a terminal in it; the pod is deleted when the session ends. " + terms}
		} else {
			p.Impact = []string{fmt.Sprintf("Runs on node %s, in a short-lived pod in %s:", r.Node, pod.Namespace), "  " + r.Command, "The command and its output go to the audit log; the pod is deleted after."}
		}
		p.Object = toYAML(pod)
		err = b.CreatePod(ctx, pod, true)
		p.dry(err, "pod/"+pod.Name+" created (server dry run)")
	}
	return p
}

func (p *Plan) dry(err error, ok string) {
	if err != nil {
		p.DryRun, p.DryRunExit = []string{statusMessage(err)}, 1
		return
	}
	p.DryRun, p.DryRunExit = []string{strings.ReplaceAll(ok, "plan0000", "…")}, 0
}

func nsName(ns string) string {
	if ns == "" {
		return "the cluster"
	}
	return "namespace " + ns
}

func profileWords(p string) string {
	switch p {
	case toolbox.ProfileNetAdmin:
		return "the host's network with NET_ADMIN and NET_RAW"
	case toolbox.ProfileSysAdmin:
		return "privileged, the host's PID, network and IPC namespaces and its filesystem at /host"
	}
	return "the host's PID, network and IPC namespaces and its filesystem at /host, not privileged"
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func toYAML(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	s = strings.ReplaceAll(s, "plan0000plan0000", "…")
	s = strings.ReplaceAll(s, "plan0000", "…")
	return strings.ReplaceAll(s, "  creationTimestamp: null\n", "")
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }
