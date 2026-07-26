package k8s

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveOnePod_PicksReadyNewest(t *testing.T) {
	now := metav1.Now()
	older := metav1.NewTime(now.Add(-1 * 60 * 1_000_000_000)) // -1m

	podNewer := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web-newer",
			Namespace:         "default",
			Labels:            map[string]string{"app": "web"},
			CreationTimestamp: now,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
		},
	}
	podOlder := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web-older",
			Namespace:         "default",
			Labels:            map[string]string{"app": "web"},
			CreationTimestamp: older,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
		},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rs-web",
			Namespace: "default",
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
		},
	}
	cs := fake.NewSimpleClientset(rs, podNewer, podOlder)

	got, err := ResolveOnePod(context.Background(), cs, "ReplicaSet", "default", "rs-web")
	if err != nil {
		t.Fatalf("ResolveOnePod: %v", err)
	}
	if got.Name != "web-newer" {
		t.Errorf("got %q, want web-newer", got.Name)
	}
}

func TestResolveOnePod_NoPods(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "x",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "x"},
			},
		},
	}
	cs := fake.NewSimpleClientset(d)
	_, err := ResolveOnePod(context.Background(), cs, "Deployment", "default", "x")
	if err == nil {
		t.Fatal("expected error when no pods match")
	}
	var nbp *NoBackingPodError
	if !errors.As(err, &nbp) {
		t.Fatalf("err type = %T, want *NoBackingPodError", err)
	}
	if nbp.Kind != "Deployment" || nbp.Name != "x" {
		t.Errorf("nbp = %+v, want Kind=Deployment Name=x", nbp)
	}
	// Error() must be short — under 100 chars even with a long selector.
	if len(nbp.Error()) > 100 {
		t.Errorf("Error() too long (%d chars): %q", len(nbp.Error()), nbp.Error())
	}
	// SelectorString() preserves the full literal for logs.
	if nbp.SelectorString() == "" {
		t.Error("SelectorString() returned empty")
	}
}

func TestResolveOnePod_Service(t *testing.T) {
	now := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "kafka-0",
			Namespace:         "base",
			Labels:            map[string]string{"app": "kafka", "tier": "global"},
			CreationTimestamp: now,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: "base"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "kafka"},
			Ports:    []corev1.ServicePort{{Port: 9092}},
		},
	}
	cs := fake.NewSimpleClientset(svc, pod)

	got, err := ResolveOnePod(context.Background(), cs, "Service", "base", "kafka")
	if err != nil {
		t.Fatalf("ResolveOnePod: %v", err)
	}
	if got.Name != "kafka-0" {
		t.Errorf("got %q, want kafka-0", got.Name)
	}
}

func TestResolveOnePod_Service_NoSelector(t *testing.T) {
	// Headless / ExternalName services have no pod selector; ResolveOnePod
	// should surface a clear error instead of returning a random pod.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: "base"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports:     []corev1.ServicePort{{Port: 9092}},
		},
	}
	cs := fake.NewSimpleClientset(svc)
	_, err := ResolveOnePod(context.Background(), cs, "Service", "base", "headless")
	if err == nil {
		t.Fatal("expected error for headless service")
	}
}
