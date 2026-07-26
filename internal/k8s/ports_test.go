package k8s

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPodPorts_DedupAndSort(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Ports: []corev1.ContainerPort{{ContainerPort: 9090}}},
				{Ports: []corev1.ContainerPort{{ContainerPort: 8080}}},
			},
			Containers: []corev1.Container{
				{Ports: []corev1.ContainerPort{{ContainerPort: 8080}, {ContainerPort: 7070}}},
				{Ports: []corev1.ContainerPort{{ContainerPort: 9090}}},
			},
		},
	}
	got := PodPorts(pod)
	want := []int{7070, 8080, 9090}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PodPorts = %v, want %v", got, want)
	}
}

func TestPodPorts_Empty(t *testing.T) {
	if got := PodPorts(&corev1.Pod{}); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestServicePorts_Sorted(t *testing.T) {
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 443},
				{Port: 80},
				{Port: 8080},
			},
		},
	}
	got := ServicePorts(svc)
	want := []int{80, 443, 8080}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ServicePorts = %v, want %v", got, want)
	}
}
