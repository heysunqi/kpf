// Package kubeconfig discovers and loads kubeconfig files.
package kubeconfig

import (
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Load returns a *rest.Config built from the given kubeconfig file.
// If context is empty, the kubeconfig's current-context is used.
func Load(path, context string) (*rest.Config, error) {
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig %s: %w", path, err)
	}
	return cfg, nil
}

// DefaultContext returns the current-context name in the kubeconfig.
func DefaultContext(path string) (string, error) {
	apiConfig, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return "", err
	}
	return apiConfig.CurrentContext, nil
}