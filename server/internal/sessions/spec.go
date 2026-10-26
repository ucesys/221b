// Package sessions runs the toolbox's sessions (Phase 9): a shell or a
// command in a pod's container, a port forward to a pod, a debug container,
// a debug pod and a node shell. A session starts only from an approved
// Watson card (TB-36, TB-39), runs as the person who asked for it (TB-51),
// ends after a time without activity or at its limit (TB-13), and every
// step is audited (TB-53).
package sessions

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/ucesys/221b/server/internal/toolbox"
)

// Labels and annotations on everything a session creates.
const (
	LabelManagedBy  = "app.kubernetes.io/managed-by"
	ManagedBy       = "221b"
	LabelSession    = "221b.io/session"
	LabelTool       = "221b.io/tool"
	AnnotationOwner = "221b.io/requested-by"
	// NeverReady is the readiness gate a debug pod carries so that no
	// Service ever lists it among its endpoints (TB-28).
	NeverReady = "221b.io/never-ready"
)

// Request is what a tool's dialog sends: the target and the choices. One
// type serves every tool; each reads the fields it needs.
type Request struct {
	Tool      string `json:"tool"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	Node      string `json:"node,omitempty"`
	// Shell is auto, bash or sh.
	Shell string `json:"shell,omitempty"`
	// Command is the one command of the command tools.
	Command string `json:"command,omitempty"`
	// Port is the pod's port of a forward; LocalPort what the CLI should
	// listen on (0: it chooses).
	Port      int `json:"port,omitempty"`
	LocalPort int `json:"localPort,omitempty"`
	// Image names an allowlisted image (debug container, debug pod).
	Image string `json:"image,omitempty"`
	// Target is the container whose processes a debug container sees.
	Target string `json:"target,omitempty"`
	// MountVolumes mounts the pod's volumes in a debug container; with
	// ReadOnly, claims are mounted read-only.
	MountVolumes bool `json:"mountVolumes,omitempty"`
	ReadOnly     bool `json:"readOnlyVolumes,omitempty"`
	// Profile is the capability profile of a debug container (general,
	// baseline, restricted, netadmin, sysadmin) or of a node shell
	// (general, netadmin, sysadmin).
	Profile string `json:"profile,omitempty"`
	// ServiceAccount, LabelsFrom and Labels shape a debug pod.
	ServiceAccount string            `json:"serviceAccount,omitempty"`
	LabelsFrom     string            `json:"labelsFrom,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	// Minutes is the duration asked for; 0 takes the chart's limit.
	Minutes int `json:"minutes,omitempty"`
	// Reason is shown to the approver and kept in the audit log.
	Reason string `json:"reason,omitempty"`
}

// Limits on what a request may carry.
const (
	maxCommand = 4096
	maxReason  = 500
	minReason  = 10
)

var shellRE = regexp.MustCompile(`^(auto|bash|sh|ash|zsh)?$`)

// Validate checks a request against the chart's values (TB-05, TB-48) and
// fills in the defaults. Production is whether a reason is required
// (TB-07).
func (r *Request) Validate(c toolbox.Config, production bool) error {
	if !c.On(r.Tool) {
		if !c.Enabled {
			return errors.New("the toolbox is turned off on this console (toolbox.enabled)")
		}
		return fmt.Errorf("%s is turned off on this console", ToolName(r.Tool))
	}
	name := func(what, v string, optional bool) error {
		if v == "" {
			if optional {
				return nil
			}
			return fmt.Errorf("choose the %s", what)
		}
		if errs := validation.IsDNS1123Subdomain(v); len(errs) > 0 {
			return fmt.Errorf("%q is not a valid %s name", v, what)
		}
		return nil
	}
	label := func(what, v string, optional bool) error {
		if v == "" {
			if optional {
				return nil
			}
			return fmt.Errorf("choose the %s", what)
		}
		if errs := validation.IsDNS1123Label(v); len(errs) > 0 {
			return fmt.Errorf("%q is not a valid %s name", v, what)
		}
		return nil
	}
	var errs []error
	switch r.Tool {
	case toolbox.ToolShell, toolbox.ToolCommand, toolbox.ToolForward, toolbox.ToolDebugContainer:
		errs = append(errs, label("namespace", r.Namespace, false), name("pod", r.Pod, false))
	case toolbox.ToolDebugPod:
		errs = append(errs, label("namespace", r.Namespace, false), name("node", r.Node, true), name("service account", r.ServiceAccount, true), name("pod to copy the labels of", r.LabelsFrom, true))
	case toolbox.ToolNodeShell, toolbox.ToolNodeCommand:
		errs = append(errs, name("node", r.Node, false))
		r.Namespace = ""
	}
	switch r.Tool {
	case toolbox.ToolShell, toolbox.ToolCommand:
		errs = append(errs, label("container", r.Container, true))
		if !shellRE.MatchString(r.Shell) {
			errs = append(errs, fmt.Errorf("the shell is auto, bash or sh, not %q", r.Shell))
		}
	case toolbox.ToolForward:
		if r.Port < 1 || r.Port > 65535 {
			errs = append(errs, errors.New("choose the pod's port, 1 to 65535"))
		}
		if r.LocalPort < 0 || r.LocalPort > 65535 {
			errs = append(errs, errors.New("the local port is 1 to 65535, or 0 to let the CLI choose"))
		}
	case toolbox.ToolDebugContainer:
		errs = append(errs, label("container", r.Target, true))
		if !contains(c.DebugContainer.Images, r.Image) {
			errs = append(errs, fmt.Errorf("choose one of the allowed images: %s", strings.Join(c.DebugContainer.Images, ", ")))
		}
		if r.Profile == "" {
			r.Profile = toolbox.ProfileRestricted
		}
		if !contains([]string{toolbox.ProfileGeneral, toolbox.ProfileRestricted, toolbox.ProfileNetAdmin, toolbox.ProfileSysAdmin}, r.Profile) {
			errs = append(errs, fmt.Errorf("%q is not a capability profile", r.Profile))
		}
		if r.MountVolumes && !c.DebugContainer.MountVolumes {
			errs = append(errs, errors.New("mounting the pod's volumes is turned off on this console"))
		}
	case toolbox.ToolDebugPod:
		if !contains(c.DebugPod.Profiles, r.Image) {
			errs = append(errs, fmt.Errorf("choose one of the allowed profiles: %s", strings.Join(c.DebugPod.Profiles, ", ")))
		}
		for k, v := range r.Labels {
			if e := validation.IsQualifiedName(k); len(e) > 0 {
				errs = append(errs, fmt.Errorf("%q is not a valid label key", k))
			}
			if e := validation.IsValidLabelValue(v); len(e) > 0 {
				errs = append(errs, fmt.Errorf("%q is not a valid label value", v))
			}
		}
	case toolbox.ToolNodeShell, toolbox.ToolNodeCommand:
		if r.Profile == "" {
			r.Profile = toolbox.ProfileGeneral
		}
		if r.Tool == toolbox.ToolNodeCommand {
			r.Profile = toolbox.ProfileGeneral
		}
		if !contains(c.NodeShell.Profiles, r.Profile) {
			errs = append(errs, fmt.Errorf("the node shell profiles here are %s", strings.Join(c.NodeShell.Profiles, ", ")))
		}
	}
	if r.Tool == toolbox.ToolCommand || r.Tool == toolbox.ToolNodeCommand {
		r.Command = strings.TrimSpace(r.Command)
		if r.Command == "" {
			errs = append(errs, errors.New("type the command to run"))
		}
		if len(r.Command) > maxCommand {
			errs = append(errs, fmt.Errorf("the command is longer than %d characters", maxCommand))
		}
	} else {
		r.Command = ""
	}
	if r.Minutes < 0 {
		errs = append(errs, errors.New("the duration is a number of minutes"))
	}
	if time.Duration(r.Minutes)*time.Minute > c.MaxDuration.D() {
		errs = append(errs, fmt.Errorf("sessions here last %s at most", short(c.MaxDuration.D())))
	}
	r.Reason = strings.TrimSpace(r.Reason)
	if len(r.Reason) > maxReason {
		errs = append(errs, fmt.Errorf("the reason is longer than %d characters", maxReason))
	}
	if production && len(r.Reason) < minReason {
		errs = append(errs, fmt.Errorf("say why, in %d characters or more: the approver reads it, and the audit log keeps it", minReason))
	}
	return errors.Join(errs...)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ToolName is how the console names a tool.
func ToolName(tool string) string {
	switch tool {
	case toolbox.ToolShell:
		return "Shell"
	case toolbox.ToolCommand:
		return "Run a command"
	case toolbox.ToolForward:
		return "Port forward"
	case toolbox.ToolDebugContainer:
		return "Debug container"
	case toolbox.ToolDebugPod:
		return "Debug pod"
	case toolbox.ToolNodeShell:
		return "Node shell"
	case toolbox.ToolNodeCommand:
		return "Run a command on the node"
	}
	return tool
}

// Interactive says whether a tool's session is a terminal.
func Interactive(tool string) bool {
	switch tool {
	case toolbox.ToolShell, toolbox.ToolDebugContainer, toolbox.ToolDebugPod, toolbox.ToolNodeShell:
		return true
	}
	return false
}

// NeedsTwo says whether a request needs a second person whatever the
// cluster (TB-32).
func NeedsTwo(c toolbox.Config, tool string) bool {
	return (tool == toolbox.ToolNodeShell || tool == toolbox.ToolNodeCommand) && c.NodeShell.Approval == toolbox.ApprovalAlways
}

// Summary is the one line a card and the audit log show for a request.
func (r Request) Summary() string {
	switch r.Tool {
	case toolbox.ToolShell:
		return fmt.Sprintf("shell in pod %s/%s%s", r.Namespace, r.Pod, ofContainer(r.Container))
	case toolbox.ToolCommand:
		return fmt.Sprintf("run in pod %s/%s%s: %s", r.Namespace, r.Pod, ofContainer(r.Container), r.Command)
	case toolbox.ToolForward:
		return fmt.Sprintf("port forward to pod %s/%s port %d", r.Namespace, r.Pod, r.Port)
	case toolbox.ToolDebugContainer:
		return fmt.Sprintf("debug container (%s, %s) in pod %s/%s", r.Image, r.Profile, r.Namespace, r.Pod)
	case toolbox.ToolDebugPod:
		where := ""
		if r.Node != "" {
			where = " on node " + r.Node
		}
		return fmt.Sprintf("debug pod (%s) in namespace %s%s", r.Image, r.Namespace, where)
	case toolbox.ToolNodeShell:
		return fmt.Sprintf("node shell on node %s (%s)", r.Node, r.Profile)
	case toolbox.ToolNodeCommand:
		return fmt.Sprintf("run on node %s: %s", r.Node, r.Command)
	}
	return r.Tool
}

func ofContainer(c string) string {
	if c == "" {
		return ""
	}
	return " container " + c
}

// ShellCommand is what a terminal runs: the chosen shell, or bash where
// the image has it and sh otherwise.
func ShellCommand(shell string) []string {
	switch shell {
	case "", "auto":
		return []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
	}
	return []string{shell}
}

// ---------- what a session creates ----------

// Meta is the name and labels of what a session creates.
func meta(id, tool, namespace, owner string, extra map[string]string) metav1.ObjectMeta {
	labels := map[string]string{}
	for k, v := range extra {
		labels[k] = v
	}
	labels[LabelManagedBy] = ManagedBy
	labels[LabelSession] = id
	labels[LabelTool] = tool
	return metav1.ObjectMeta{
		Name:        "221b-" + tool + "-" + id[:8],
		Namespace:   namespace,
		Labels:      labels,
		Annotations: map[string]string{AnnotationOwner: owner},
	}
}

// Pod Security levels (pod-security.kubernetes.io/enforce).
const (
	LevelPrivileged = "privileged"
	LevelBaseline   = "baseline"
	LevelRestricted = "restricted"
)

// PodSecurity reads a namespace's enforced level; no label is privileged,
// as the admission plugin treats it by default.
func PodSecurity(ns *corev1.Namespace) string {
	if ns == nil {
		return LevelPrivileged
	}
	switch ns.Labels["pod-security.kubernetes.io/enforce"] {
	case LevelBaseline:
		return LevelBaseline
	case LevelRestricted:
		return LevelRestricted
	}
	return LevelPrivileged
}

// baselineCaps are the capabilities the baseline level allows a container
// to add.
var baselineCaps = map[string]bool{"AUDIT_WRITE": true, "CHOWN": true, "DAC_OVERRIDE": true, "FOWNER": true, "FSETID": true, "KILL": true, "MKNOD": true, "NET_BIND_SERVICE": true, "SETFCAP": true, "SETGID": true, "SETPCAP": true, "SETUID": true, "SYS_CHROOT": true}

// ProfilesAllowed lists the debug container capability profiles a
// namespace's level admits (TB-24, TB-52).
func ProfilesAllowed(level string) []string {
	switch level {
	case LevelRestricted:
		return []string{toolbox.ProfileRestricted}
	case LevelBaseline:
		return []string{toolbox.ProfileGeneral, toolbox.ProfileRestricted}
	}
	return []string{toolbox.ProfileGeneral, toolbox.ProfileRestricted, toolbox.ProfileNetAdmin, toolbox.ProfileSysAdmin}
}

func ptr[T any](v T) *T { return &v }

// securityFor is the security context of a capability profile. General
// runs as the image's own user with the capabilities its image asks for,
// as many as the namespace's Pod Security level allows.
func securityFor(profile string, img toolbox.Image, level string) *corev1.SecurityContext {
	switch profile {
	case toolbox.ProfileGeneral:
		sc := &corev1.SecurityContext{}
		for _, cap := range img.Caps {
			if level == LevelPrivileged || (level == LevelBaseline && baselineCaps[cap]) {
				if sc.Capabilities == nil {
					sc.Capabilities = &corev1.Capabilities{}
				}
				sc.Capabilities.Add = append(sc.Capabilities.Add, corev1.Capability(cap))
			}
		}
		return sc
	case toolbox.ProfileNetAdmin:
		return &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"}}}
	case toolbox.ProfileSysAdmin:
		return &corev1.SecurityContext{Privileged: ptr(true)}
	case toolbox.ProfileRestricted:
		return restrictedContext()
	}
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false)}
}

func restrictedContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             ptr(true),
		RunAsUser:                ptr(int64(65532)),
		AllowPrivilegeEscalation: ptr(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// EphemeralContainer is the debug container of TB-24: an allowlisted
// image that sleeps until a shell is opened into it, sees the target's
// processes, and mounts the pod's volumes at the target's paths (without
// subPath, which ephemeral containers may not use).
func EphemeralContainer(c toolbox.Config, id string, r Request, pod *corev1.Pod, level string) (corev1.EphemeralContainer, error) {
	img, ok := c.Images[r.Image]
	if !ok {
		return corev1.EphemeralContainer{}, fmt.Errorf("no image named %q", r.Image)
	}
	ec := corev1.EphemeralContainer{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
		Name:            "221b-" + r.Image + "-" + id[:4],
		Image:           img.Image,
		Command:         []string{"sleep", "infinity"},
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: securityFor(r.Profile, img, level),
		Stdin:           true,
		TTY:             true,
	}}
	target := r.Target
	if target == "" && len(pod.Spec.Containers) > 0 {
		target = pod.Spec.Containers[0].Name
	}
	var tc *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == target {
			tc = &pod.Spec.Containers[i]
		}
	}
	if tc == nil {
		return ec, fmt.Errorf("pod %s has no container named %s", pod.Name, target)
	}
	ec.TargetContainerName = target
	if r.MountVolumes {
		claims := map[string]bool{}
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				claims[v.Name] = true
			}
		}
		seen := map[string]bool{}
		for _, m := range tc.VolumeMounts {
			if m.SubPath != "" || m.SubPathExpr != "" || seen[m.MountPath] {
				continue
			}
			seen[m.MountPath] = true
			ro := m.ReadOnly || (r.ReadOnly && claims[m.Name])
			ec.VolumeMounts = append(ec.VolumeMounts, corev1.VolumeMount{Name: m.Name, MountPath: m.MountPath, ReadOnly: ro, MountPropagation: nil})
		}
	}
	return ec, nil
}

// DebugJob is the debug pod of TB-28: a Job with one pod, a deadline and
// a time to live, labels copied from a pod or typed, a readiness gate that
// never holds so no Service sends it traffic, and only the capabilities
// its image may add and the namespace allows.
func DebugJob(c toolbox.Config, id, owner string, r Request, level string, copied map[string]string, limit time.Duration) (*batchv1.Job, []string, error) {
	img, ok := c.Images[r.Image]
	if !ok {
		return nil, nil, fmt.Errorf("no image named %q", r.Image)
	}
	var notes []string
	labels := map[string]string{}
	for k, v := range copied {
		if controllerLabel(k) {
			continue
		}
		labels[k] = v
	}
	for k, v := range r.Labels {
		labels[k] = v
	}
	m := meta(id, toolbox.ToolDebugPod, r.Namespace, owner, labels)
	sc := &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	for _, cap := range img.Caps {
		if level == LevelPrivileged || (level == LevelBaseline && baselineCaps[cap]) {
			sc.Capabilities.Add = append(sc.Capabilities.Add, corev1.Capability(cap))
		} else {
			notes = append(notes, fmt.Sprintf("%s is left out: namespace %s enforces the %s Pod Security level.", cap, r.Namespace, level))
		}
	}
	if level == LevelRestricted {
		sc.RunAsNonRoot, sc.RunAsUser = ptr(true), ptr(int64(65532))
		notes = append(notes, fmt.Sprintf("It runs as a user other than root: namespace %s enforces the restricted Pod Security level.", r.Namespace))
	}
	secs := int64(limit / time.Second)
	ttl := int32(c.DebugPod.TTLAfterFinished.D() / time.Second)
	pod := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: m.Labels, Annotations: m.Annotations},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ServiceAccountName:            r.ServiceAccount,
			NodeName:                      r.Node,
			TerminationGracePeriodSeconds: ptr(int64(5)),
			ReadinessGates:                []corev1.PodReadinessGate{{ConditionType: NeverReady}},
			Containers: []corev1.Container{{
				Name:            "debug",
				Image:           img.Image,
				Command:         []string{"sleep", "infinity"},
				ImagePullPolicy: corev1.PullIfNotPresent,
				Stdin:           true,
				TTY:             true,
				SecurityContext: sc,
				Resources: corev1.ResourceRequirements{
					Requests: resources("50m", "64Mi"),
					Limits:   resources("500m", "256Mi"),
				},
			}},
		},
	}
	if r.Node != "" {
		pod.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	}
	job := &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: m,
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(0)),
			ActiveDeadlineSeconds:   &secs,
			TTLSecondsAfterFinished: &ttl,
			Template:                pod,
		},
	}
	return job, notes, nil
}

// controllerLabel says whether a label is one a controller sets to find
// its own pods; a debug pod must not carry it, or the controller would
// count it as one of its own.
func controllerLabel(k string) bool {
	switch k {
	case "pod-template-hash", "controller-uid", "batch.kubernetes.io/controller-uid", "job-name", "batch.kubernetes.io/job-name", "controller-revision-hash", "statefulset.kubernetes.io/pod-name", "apps.kubernetes.io/pod-index", "pod-template-generation":
		return true
	}
	return false
}

// NodePod is the pod of a node shell or node command (TB-31): pinned to
// the node, tolerating every taint, with the host's namespaces and its
// filesystem at /host as the profile says, ended by its deadline at the
// latest.
func NodePod(c toolbox.Config, id, owner string, r Request, limit time.Duration) (*corev1.Pod, error) {
	img, ok := c.Images[c.NodeShell.Image]
	if !ok {
		return nil, fmt.Errorf("no image named %q", c.NodeShell.Image)
	}
	m := meta(id, r.Tool, c.NodeShell.Namespace, owner, nil)
	secs := int64(limit / time.Second)
	spec := corev1.PodSpec{
		NodeName:                      r.Node,
		RestartPolicy:                 corev1.RestartPolicyNever,
		ActiveDeadlineSeconds:         &secs,
		TerminationGracePeriodSeconds: ptr(int64(1)),
		Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
		AutomountServiceAccountToken:  ptr(false),
		EnableServiceLinks:            ptr(false),
		Containers: []corev1.Container{{
			Name:            "node",
			Image:           img.Image,
			Command:         []string{"sleep", "infinity"},
			ImagePullPolicy: corev1.PullIfNotPresent,
			Stdin:           true,
			TTY:             true,
		}},
	}
	host := func() {
		spec.HostPID, spec.HostIPC, spec.HostNetwork = true, true, true
		spec.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}
		spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "host", MountPath: "/host"}}
	}
	switch r.Profile {
	case toolbox.ProfileGeneral, "":
		host()
	case toolbox.ProfileNetAdmin:
		spec.HostNetwork = true
		spec.Containers[0].SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"}}}
	case toolbox.ProfileSysAdmin:
		host()
		spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr(true)}
	default:
		return nil, fmt.Errorf("%q is not a node shell profile", r.Profile)
	}
	return &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: m, Spec: spec}, nil
}

// NodeCommand is what a node command runs inside the node pod: in the
// host's root, with a shell.
func NodeCommand(command string) []string {
	return []string{"chroot", "/host", "sh", "-c", command}
}

// Containers lists a pod's containers a shell may open in: app, init (a
// running sidecar) and ephemeral ones, app first.
func Containers(p *corev1.Pod) []string {
	var out []string
	for _, c := range p.Spec.Containers {
		out = append(out, c.Name)
	}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, c.Name)
		}
	}
	var eph []string
	for _, c := range p.Spec.EphemeralContainers {
		eph = append(eph, c.Name)
	}
	sort.Strings(eph)
	return append(out, eph...)
}

func short(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}
