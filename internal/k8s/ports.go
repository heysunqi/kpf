package k8s

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodPorts returns container ports of a pod (main + init), deduped + sorted.
func PodPorts(pod *corev1.Pod) []int {
	seen := make(map[int]bool)
	var out []int
	add := func(c corev1.Container) {
		for _, cp := range c.Ports {
			if cp.ContainerPort == 0 {
				continue
			}
			n := int(cp.ContainerPort)
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	for i := range pod.Spec.InitContainers {
		add(pod.Spec.InitContainers[i])
	}
	for i := range pod.Spec.Containers {
		add(pod.Spec.Containers[i])
	}
	sort.Ints(out)
	return out
}

// ServicePorts returns the service ports.
func ServicePorts(svc *corev1.Service) []int {
	out := make([]int, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		if p.Port != 0 {
			out = append(out, int(p.Port))
		}
	}
	sort.Ints(out)
	return out
}

// ComputePorts returns the list of remote-port candidates for the given object.
// For Service/Deployment/StatefulSet/ReplicaSet, it resolves to a backing pod
// first so callers fail fast if no pods back the resource.
// Returns the resolved pod name when the kind requires a pod lookup (also
// empty for Pod since the object name already IS the pod name).
func ComputePorts(ctx context.Context, cs kubernetes.Interface, kind, ns, name string) ([]int, string, error) {
	switch kind {
	case "Pod":
		pod, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", err
		}
		return PodPorts(pod), pod.Name, nil
	case "Service":
		svc, err := cs.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", err
		}
		// Resolve the backing pod eagerly so the user finds out at step
		// ④→⑤ (status line) rather than after they've configured ports.
		// The pod name is returned alongside the service ports.
		pod, err := ResolveOnePod(ctx, cs, "Service", ns, name)
		if err != nil {
			return nil, "", err
		}
		return ServicePorts(svc), pod.Name, nil
	case "Deployment", "StatefulSet", "ReplicaSet":
		pod, err := ResolveOnePod(ctx, cs, kind, ns, name)
		if err != nil {
			return nil, "", err
		}
		return PodPorts(pod), pod.Name, nil
	default:
		return nil, "", fmt.Errorf("ComputePorts: unsupported kind %q", kind)
	}
}

// KubernetesHelpers is the interface our handler helpers consume. It's an
// alias for kubernetes.Interface, declared here so callers in other packages
// can express their need without importing client-go directly.
type KubernetesHelpers = kubernetes.Interface
