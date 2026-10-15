package kube

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StripNode drops the parts of a node the console never shows, before the
// node is cached. Image lists alone can run to tens of kilobytes per node.
func StripNode(obj any) (any, error) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		return obj, nil
	}
	n.ManagedFields = nil
	n.Annotations = trimAnnotations(n.Annotations)
	n.Status.Images = nil
	n.Status.VolumesAttached = nil
	n.Status.VolumesInUse = nil
	return n, nil
}

// StripPod keeps what the Nodes and Pods tabs and the pod details show:
// where the pod runs, what it requests, its containers with their
// commands, environment and mounts, its volumes, scheduling and status.
// Pod Security's checks read the rest of the security-related fields
// (P4-06). It drops managed fields, the last-applied annotation and other
// fields no screen reads. It returns a new object and is idempotent.
func StripPod(obj any) (any, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil
	}
	out := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              p.Name,
			Namespace:         p.Namespace,
			UID:               p.UID,
			ResourceVersion:   p.ResourceVersion,
			CreationTimestamp: p.CreationTimestamp,
			DeletionTimestamp: p.DeletionTimestamp,
			Labels:            p.Labels,
			Annotations:       trimAnnotations(p.Annotations),
			OwnerReferences:   p.OwnerReferences,
		},
		Spec: corev1.PodSpec{
			NodeName:                      p.Spec.NodeName,
			RestartPolicy:                 p.Spec.RestartPolicy,
			Overhead:                      p.Spec.Overhead,
			Resources:                     p.Spec.Resources,
			Containers:                    stripContainers(p.Spec.Containers),
			InitContainers:                stripContainers(p.Spec.InitContainers),
			Volumes:                       p.Spec.Volumes,
			ServiceAccountName:            p.Spec.ServiceAccountName,
			NodeSelector:                  p.Spec.NodeSelector,
			Tolerations:                   p.Spec.Tolerations,
			Affinity:                      p.Spec.Affinity,
			PriorityClassName:             p.Spec.PriorityClassName,
			Priority:                      p.Spec.Priority,
			HostNetwork:                   p.Spec.HostNetwork,
			HostPID:                       p.Spec.HostPID,
			HostIPC:                       p.Spec.HostIPC,
			HostUsers:                     p.Spec.HostUsers,
			OS:                            p.Spec.OS,
			EphemeralContainers:           stripEphemeral(p.Spec.EphemeralContainers),
			SecurityContext:               p.Spec.SecurityContext,
			TerminationGracePeriodSeconds: p.Spec.TerminationGracePeriodSeconds,
			SchedulerName:                 p.Spec.SchedulerName,
			RuntimeClassName:              p.Spec.RuntimeClassName,
		},
		Status: corev1.PodStatus{
			Phase:                 p.Status.Phase,
			Reason:                p.Status.Reason,
			Message:               p.Status.Message,
			StartTime:             p.Status.StartTime,
			Conditions:            p.Status.Conditions,
			ContainerStatuses:     stripStatuses(p.Status.ContainerStatuses),
			InitContainerStatuses: stripStatuses(p.Status.InitContainerStatuses),
			PodIP:                 p.Status.PodIP,
			PodIPs:                p.Status.PodIPs,
			HostIP:                p.Status.HostIP,
			QOSClass:              p.Status.QOSClass,
			NominatedNodeName:     p.Status.NominatedNodeName,
		},
	}
	return out, nil
}

func stripContainers(in []corev1.Container) []corev1.Container {
	if len(in) == 0 {
		return nil
	}
	out := make([]corev1.Container, len(in))
	for i, c := range in {
		out[i] = corev1.Container{
			Name:            c.Name,
			Image:           c.Image,
			Command:         c.Command,
			Args:            c.Args,
			WorkingDir:      c.WorkingDir,
			Ports:           c.Ports,
			Env:             c.Env,
			EnvFrom:         c.EnvFrom,
			Resources:       c.Resources,
			RestartPolicy:   c.RestartPolicy,
			VolumeMounts:    c.VolumeMounts,
			ImagePullPolicy: c.ImagePullPolicy,
			SecurityContext: c.SecurityContext,
			LivenessProbe:   c.LivenessProbe,
			ReadinessProbe:  c.ReadinessProbe,
			StartupProbe:    c.StartupProbe,
			// Pod Security reads the hooks' host fields.
			Lifecycle: c.Lifecycle,
		}
	}
	return out
}

// stripEphemeral keeps of debug containers what Pod Security checks.
func stripEphemeral(in []corev1.EphemeralContainer) []corev1.EphemeralContainer {
	if len(in) == 0 {
		return nil
	}
	out := make([]corev1.EphemeralContainer, len(in))
	for i, c := range in {
		out[i] = corev1.EphemeralContainer{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: c.Name, Image: c.Image, Ports: c.Ports, SecurityContext: c.SecurityContext, Lifecycle: c.Lifecycle,
		}, TargetContainerName: c.TargetContainerName}
	}
	return out
}

func stripStatuses(in []corev1.ContainerStatus) []corev1.ContainerStatus {
	if len(in) == 0 {
		return nil
	}
	out := make([]corev1.ContainerStatus, len(in))
	for i, s := range in {
		out[i] = corev1.ContainerStatus{
			Name:                 s.Name,
			Ready:                s.Ready,
			Started:              s.Started,
			RestartCount:         s.RestartCount,
			State:                s.State,
			LastTerminationState: s.LastTerminationState,
			Image:                s.Image,
			ImageID:              s.ImageID,
			ContainerID:          s.ContainerID,
			// In-place resizes: what the kubelet allocated and applied.
			AllocatedResources: s.AllocatedResources,
			Resources:          s.Resources,
		}
	}
	return out
}
