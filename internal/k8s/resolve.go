package k8s

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ResolveOnePod resolves a high-level workload (Deployment/StatefulSet/ReplicaSet)
// or a Service to a single backing pod. It prefers a Ready pod, breaking ties
// by newest CreationTimestamp. Falls back to the newest non-ready pod if none
// are ready.
func ResolveOnePod(ctx context.Context, cs kubernetes.Interface, kind, ns, name string) (*corev1.Pod, error) {
	selector, err := workloadSelector(ctx, cs, kind, ns, name)
	if err != nil {
		return nil, err
	}
	if selector.Empty() {
		return nil, fmt.Errorf("%s/%s has empty selector", kind, name)
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, &NoBackingPodError{
			Kind:     kind,
			Name:     name,
			Selector: selector.String(),
		}
	}
	return pickBestPod(pods.Items), nil
}

// NoBackingPodError is returned by ResolveOnePod when a Service or higher-level
// workload has no pods that match its selector. The error is intentionally
// terse — the long selector literal is exposed via the Selector field for
// callers that want to log it, but the default Error() string stays short
// enough to fit on one terminal line.
type NoBackingPodError struct {
	Kind     string
	Name     string
	Selector string
}

func (e *NoBackingPodError) Error() string {
	return fmt.Sprintf("no pods back %s/%q (selector matched 0 pods in namespace)",
		strings.ToLower(e.Kind), e.Name)
}

// SelectorString returns the full selector literal, useful for logs.
func (e *NoBackingPodError) SelectorString() string { return e.Selector }

func pickBestPod(pods []corev1.Pod) *corev1.Pod {
	var ready, newest *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if isPodReady(p) {
			if ready == nil || p.CreationTimestamp.After(ready.CreationTimestamp.Time) {
				ready = p
			}
		}
		if newest == nil || p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	if ready != nil {
		return ready
	}
	return newest
}

func isPodReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	if len(p.Spec.Containers) == 0 {
		return false
	}
	for _, c := range p.Status.ContainerStatuses {
		if !c.Ready {
			return false
		}
	}
	return true
}
