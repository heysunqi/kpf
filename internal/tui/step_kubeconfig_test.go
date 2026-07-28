package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kpf/internal/kubeconfig"
)

// TestKubeStep_EmptyStateWhenNoEntries verifies the empty-state message
// is rendered (and the table is skipped) when no kubeconfigs were found.
// The walkthrough test in walkthrough_test.go asserts on the title bar,
// so this confirms the message body doesn't blow up either.
func TestKubeStep_EmptyStateWhenNoEntries(t *testing.T) {
	step := newKubeStep(nil, []string{"/Users/me/.kube", "/etc/kpf"})
	if !step.empty {
		t.Error("empty=true expected when entries is nil")
	}
	v := step.View()
	if !strings.Contains(v, "Select a kubeconfig") {
		t.Errorf("expected title in view, got:\n%s", v)
	}
	if !strings.Contains(v, "No kubeconfig found") {
		t.Errorf("expected empty-state body, got:\n%s", v)
	}
	if !strings.Contains(v, "/Users/me/.kube") {
		t.Errorf("expected scanned dir in body, got:\n%s", v)
	}
}

// TestKubeStep_EnterEmitsKubeChosen verifies that pressing Enter on the
// highlighted row emits a KubeChosenMsg carrying that entry's Path and
// CurrentContext. The previous list-based version read these via
// SelectedItem().(kubeItem); the table-based version maps Cursor() back
// to its parallel entries slice.
func TestKubeStep_EnterEmitsKubeChosen(t *testing.T) {
	step := newKubeStep([]kubeconfig.Entry{
		{Path: "/p/a", Basename: "a", CurrentContext: "ctx-a"},
		{Path: "/p/b", Basename: "b", CurrentContext: "ctx-b"},
	}, nil)
	step, cmd := step.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a tea.Cmd (KubeChosenMsg) on enter")
	}
	msg := cmd()
	chosen, ok := msg.(KubeChosenMsg)
	if !ok {
		t.Fatalf("expected KubeChosenMsg, got %T", msg)
	}
	if chosen.Path != "/p/a" || chosen.Context != "ctx-a" {
		t.Errorf("cursor-0 row should yield /p/a + ctx-a, got %q + %q", chosen.Path, chosen.Context)
	}

	// Move cursor down then press enter — second row's path/context.
	step.table.SetCursor(1)
	_, cmd = step.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg = cmd()
	chosen = msg.(KubeChosenMsg)
	if chosen.Path != "/p/b" || chosen.Context != "ctx-b" {
		t.Errorf("cursor-1 row should yield /p/b + ctx-b, got %q + %q", chosen.Path, chosen.Context)
	}
}

// TestKubeStep_EnterInEmptyStateNoOp ensures pressing Enter on the
// empty-state view doesn't blow up. With no entries to select, the
// handler must early-return nil.
func TestKubeStep_EnterInEmptyStateNoOp(t *testing.T) {
	step := newKubeStep(nil, []string{"/Users/me/.kube"})
	_, cmd := step.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("enter on empty state should be a no-op, got cmd %T", cmd())
	}
}

// TestKubeStep_FilteringAlwaysFalse locks in that the kubeStep filter
// gate (used by app.isAnyListFiltering) returns false. The picker uses
// bubbles/table which has no built-in filter; if we later add a filter
// overlay, this is the single site to flip.
func TestKubeStep_FilteringAlwaysFalse(t *testing.T) {
	step := newKubeStep([]kubeconfig.Entry{{Path: "/p", Basename: "x"}}, nil)
	if step.filtering() {
		t.Error("kubeStep.filtering() should be false (no filter overlay)")
	}
	empty := newKubeStep(nil, nil)
	if empty.filtering() {
		t.Error("empty kubeStep.filtering() should be false")
	}
}

// TestKubeStep_RendersTable verifies the populated table renders the
// expected column headers and at least one row's content. We can't pin
// exact widths (lipgloss Width varies by terminal) but the BASENAME and
// CTX columns must show the entries' basename + context.
func TestKubeStep_RendersTable(t *testing.T) {
	step := newKubeStep([]kubeconfig.Entry{
		{Path: "/p/a", Basename: "prod-config", CurrentContext: "prod-east-1",
			Clusters: []string{"c1", "c2"}, Contexts: []string{"ctx-1", "ctx-2"}, Users: []string{"u1"}},
		{Path: "/p/b", Basename: "dev-config", CurrentContext: "dev",
			Clusters: []string{"dev"}, Contexts: []string{"dev"}, Users: []string{"dev-user"}},
	}, nil)
	v := step.View()
	for _, want := range []string{
		"Select a kubeconfig (2)",
		"BASENAME", "CTX", "CLUSTERS", "CONTEXTS", "USERS",
		"prod-config", "prod-east-1",
		"dev-config", "dev",
		"2 clusters", "2 contexts", "1 users",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("expected %q in view, got:\n%s", want, v)
		}
	}
}

// TestKubeStep_VisualCheck is a manual-render eyeball pass. Builds a
// table with several kubeconfigs and prints it via t.Log so a developer
// running `go test -v -run TestKubeStep_VisualCheck` can confirm the
// layout. Not an assertion — production paths are covered above.
func TestKubeStep_VisualCheck(t *testing.T) {
	step := newKubeStep([]kubeconfig.Entry{
		{Path: "/Users/me/.kube/prod-config", Basename: "prod-config",
			CurrentContext: "prod-east-1",
			Clusters:       []string{"prod-east-1", "prod-west-1", "prod-eu-1"},
			Contexts:       []string{"prod-east-1", "prod-west-1", "prod-eu-1", "prod-staging"},
			Users:          []string{"admin", "readonly"}},
		{Path: "/Users/me/.kube/dev-config", Basename: "dev-config",
			CurrentContext: "dev",
			Clusters:       []string{"dev"},
			Contexts:       []string{"dev"},
			Users:          []string{"dev-user"}},
		{Path: "/Users/me/.kube/very-long-kubeconfig-basename.config", Basename: "very-long-kubeconfig-basename.config",
			CurrentContext: "staging-cluster",
			Clusters:       []string{"a", "b", "c", "d", "e"},
			Contexts:       []string{"x", "y", "z"},
			Users:          []string{"a", "b", "c", "d", "e", "f"}},
	}, []string{"/Users/me/.kube"})
	step.table.SetWidth(80)
	step.table.SetHeight(8)
	t.Log("\n" + step.View())

	empty := newKubeStep(nil, []string{"/Users/me/.kube", "/etc/kpf"})
	t.Log("\n" + empty.View())
}