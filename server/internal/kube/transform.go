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
	n.Annotations = nil
	n.Status.Images = nil
	n.Status.VolumesAttached = nil
	n.Status.VolumesInUse = nil
	return n, nil
}

// StripPod keeps only what the Nodes tab needs from a pod: where it runs,
// what it requests and whether it is healthy. With 15,000 pods this keeps
// the cache to a few megabytes. It returns a new object and is idempotent.
func StripPod(obj any) (any, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil
	}
	out := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              p.Name,
			Namespace:         p.Namespace,
			UID:               p.UID,
			ResourceVersion:   p.ResourceVersion,
			CreationTimestamp: p.CreationTimestamp,
			DeletionTimestamp: p.DeletionTimestamp,
		},
		Spec: corev1.PodSpec{
			NodeName:       p.Spec.NodeName,
			Overhead:       p.Spec.Overhead,
			Resources:      p.Spec.Resources,
			Containers:     stripContainers(p.Spec.Containers),
			InitContainers: stripContainers(p.Spec.InitContainers),
		},
		Status: corev1.PodStatus{
			Phase:                 p.Status.Phase,
			Reason:                p.Status.Reason,
			StartTime:             p.Status.StartTime,
			Conditions:            p.Status.Conditions,
			ContainerStatuses:     stripStatuses(p.Status.ContainerStatuses),
			InitContainerStatuses: stripStatuses(p.Status.InitContainerStatuses),
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
		out[i] = corev1.Container{Name: c.Name, Resources: c.Resources, RestartPolicy: c.RestartPolicy}
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
			RestartCount:         s.RestartCount,
			State:                s.State,
			LastTerminationState: s.LastTerminationState,
		}
	}
	return out
}
