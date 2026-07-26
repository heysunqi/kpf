// Package k8s is a thin facade over client-go for kpf's needs:
// namespace listing, resource listing (Pod/Service/Deployment/STS/RS),
// port extraction, and high-level-workload → pod resolution.
package k8s

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"kpf/internal/kubeconfig"
)

// New builds a Clientset (kubernetes.Interface) from a *rest.Config.
func New(cfg *rest.Config) (kubernetes.Interface, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return cs, nil
}

// NewForSpec loads the kubeconfig at path (optionally named context), and
// returns a clientset. The provided context is used to short-circuit loading
// of long-running rest calls (this implementation does not yet honor it; it's
// reserved for future cancellation support).
func NewForSpec(_ context.Context, path, contextName string) (kubernetes.Interface, error) {
	cfg, err := kubeconfig.Load(path, contextName)
	if err != nil {
		return nil, err
	}
	return New(cfg)
}