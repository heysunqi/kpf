package tui

import (
	"testing"
)

// TestActiveStep_VisualCheck is a manual-render check: builds a table
// with three rows covering ready/starting/dropped states, sets a known
// width, and prints the rendered output to stdout so a developer running
// `go test -v -run TestActiveStep_VisualCheck ./internal/tui/...` can
// eyeball the layout. Not a real assertion — the production code paths
// are covered by TestActiveStep_RendersTableNotPanic.
func TestActiveStep_VisualCheck(t *testing.T) {
	s := newActiveStep([]ipcForward{
		{ID: "fwd_0001", Kubeconfig: "prod-config", Namespace: "default", Kind: "Pod", Object: "my-pod", Bind: "127.0.0.1", Ports: "27017:8380,9090:9090", Status: "ready", StartedAt: "2026-07-28T10:00:00Z"},
		{ID: "fwd_0002", Kubeconfig: "dev-config", Namespace: "staging", Kind: "Deployment", Object: "web", Bind: "0.0.0.0", Ports: "8080:80", Status: "starting", StartedAt: "2026-07-28T10:01:00Z"},
		{ID: "fwd_0003", Kubeconfig: "staging-config", Namespace: "default", Kind: "Service", Object: "api", Bind: "0.0.0.0", Ports: "8443:443", Status: "dropped", StartedAt: "2026-07-28T09:50:00Z"},
	})
	s.table.SetWidth(140)
	s.table.SetHeight(8)
	t.Log("\n" + s.View())
}