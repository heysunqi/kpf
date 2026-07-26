package k8s

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ResourceSummary is a TUI-friendly description of a single resource.
type ResourceSummary struct {
	Kind     string        `json:"kind"`
	Name     string        `json:"name"`
	Age      time.Duration `json:"-"`
	AgeStr   string        `json:"age"`
	Ready    string        `json:"ready,omitempty"`
	Status   string        `json:"status,omitempty"`
	Replicas string        `json:"replicas,omitempty"`
	Selector string        `json:"selector,omitempty"`
	Type     string        `json:"type,omitempty"`   // Service.Type
	ClusterIP string       `json:"cluster_ip,omitempty"`
}

// ListPods returns pod summaries in the given namespace.
func ListPods(ctx context.Context, cs kubernetes.Interface, ns string) ([]ResourceSummary, error) {
	list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSummary, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, podSummary(&list.Items[i]))
	}
	return out, nil
}

func podSummary(p *corev1.Pod) ResourceSummary {
	return ResourceSummary{
		Kind:   "Pod",
		Name:   p.Name,
		Age:    time.Since(p.CreationTimestamp.Time),
		AgeStr: humanDuration(time.Since(p.CreationTimestamp.Time)),
		Ready:  podReady(p),
		Status: string(p.Status.Phase),
	}
}

func podReady(p *corev1.Pod) string {
	total := len(p.Spec.Containers)
	ready := 0
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, total)
}

// ListServices returns service summaries in the given namespace.
func ListServices(ctx context.Context, cs kubernetes.Interface, ns string) ([]ResourceSummary, error) {
	list, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSummary, 0, len(list.Items))
	for i := range list.Items {
		s := &list.Items[i]
		out = append(out, ResourceSummary{
			Kind:      "Service",
			Name:      s.Name,
			Age:       time.Since(s.CreationTimestamp.Time),
			AgeStr:    humanDuration(time.Since(s.CreationTimestamp.Time)),
			Type:      string(s.Spec.Type),
			ClusterIP: s.Spec.ClusterIP,
		})
	}
	return out, nil
}

// ListDeployments returns deployment summaries in the given namespace.
func ListDeployments(ctx context.Context, cs kubernetes.Interface, ns string) ([]ResourceSummary, error) {
	list, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSummary, 0, len(list.Items))
	for i := range list.Items {
		d := &list.Items[i]
		out = append(out, workloadSummary("Deployment", d.Name, d.CreationTimestamp.Time, d.Status.ReadyReplicas, d.Status.Replicas, d.Spec.Selector))
	}
	return out, nil
}

// ListStatefulSets returns StatefulSet summaries in the given namespace.
func ListStatefulSets(ctx context.Context, cs kubernetes.Interface, ns string) ([]ResourceSummary, error) {
	list, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSummary, 0, len(list.Items))
	for i := range list.Items {
		s := &list.Items[i]
		out = append(out, workloadSummary("StatefulSet", s.Name, s.CreationTimestamp.Time, s.Status.ReadyReplicas, s.Status.Replicas, s.Spec.Selector))
	}
	return out, nil
}

// ListReplicaSets returns ReplicaSet summaries in the given namespace.
func ListReplicaSets(ctx context.Context, cs kubernetes.Interface, ns string) ([]ResourceSummary, error) {
	list, err := cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSummary, 0, len(list.Items))
	for i := range list.Items {
		r := &list.Items[i]
		out = append(out, workloadSummary("ReplicaSet", r.Name, r.CreationTimestamp.Time, r.Status.ReadyReplicas, r.Status.Replicas, r.Spec.Selector))
	}
	return out, nil
}

func workloadSummary(kind, name string, ts time.Time, ready, total int32, sel *metav1.LabelSelector) ResourceSummary {
	r := ""
	if ready > 0 || total > 0 {
		r = fmt.Sprintf("%d/%d", ready, total)
	}
	return ResourceSummary{
		Kind:     kind,
		Name:     name,
		Age:      time.Since(ts),
		AgeStr:   humanDuration(time.Since(ts)),
		Ready:    r,
		Selector: selectorString(sel),
	}
}

func selectorString(sel *metav1.LabelSelector) string {
	if sel == nil {
		return ""
	}
	parts := make([]string, 0, len(sel.MatchLabels))
	for k, v := range sel.MatchLabels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(parts)
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// humanDuration formats d like kubectl: 5s, 2m, 3h, 4d.
func humanDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
