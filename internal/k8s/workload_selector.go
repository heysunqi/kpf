package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// workloadSelector returns the effective label selector for a high-level
// workload (Deployment/StatefulSet/ReplicaSet) or a Service.
func workloadSelector(ctx context.Context, cs kubernetes.Interface, kind, ns, name string) (labels.Selector, error) {
	switch kind {
	case "Deployment":
		d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if d.Spec.Selector == nil {
			return labels.Nothing(), nil
		}
		sel, err := labels.ValidatedSelectorFromSet(d.Spec.Selector.MatchLabels)
		if err != nil {
			return nil, err
		}
		return sel, nil
	case "StatefulSet":
		s, err := cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if s.Spec.Selector == nil {
			return labels.Nothing(), nil
		}
		sel, err := labels.ValidatedSelectorFromSet(s.Spec.Selector.MatchLabels)
		if err != nil {
			return nil, err
		}
		return sel, nil
	case "ReplicaSet":
		r, err := cs.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if r.Spec.Selector == nil {
			return labels.Nothing(), nil
		}
		sel, err := labels.ValidatedSelectorFromSet(r.Spec.Selector.MatchLabels)
		if err != nil {
			return nil, err
		}
		return sel, nil
	case "Service":
		svc, err := cs.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if len(svc.Spec.Selector) == 0 {
			// Headless services (ClusterIP=None) and ExternalName services
			// have no selector — there's nothing to port-forward to.
			return labels.Nothing(), fmt.Errorf(
				"service %q has no pod selector (headless or ExternalName?)", name)
		}
		sel, err := labels.ValidatedSelectorFromSet(svc.Spec.Selector)
		if err != nil {
			return nil, err
		}
		return sel, nil
	default:
		return nil, fmt.Errorf("workloadSelector: unsupported kind %q", kind)
	}
}
