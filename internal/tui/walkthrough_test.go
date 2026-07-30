package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestApp_FullWalkThrough(t *testing.T) {
	m := New("")
	m.width = 100
	m.height = 30

	// Step ①: kubeconfig (will be empty until the daemon responds; with no
	// daemon, the step renders with no entries — that's fine for the test.)
	v := m.View()
	if !strings.Contains(v, "Select a kubeconfig") {
		t.Errorf("step ① missing title")
	}
}

func TestApp_PortStepView(t *testing.T) {
	m := New("")
	m.width = 100
	m.height = 30

	// Walk to port step quickly.
	for _, msg := range []tea.Msg{
		KubeChosenMsg{Path: "k.config", Context: "ctx"},
		NsChosenMsg{Name: "default"},
		ResourceTypeChosenMsg{Kind: "Pod"},
		PortsLoadedMsg{Kind: "Pod", Object: "pod-1", Ports: []int{8080, 9090}, Pod: "pod-1"},
	} {
		m2, _ := m.Update(msg)
		m = m2.(Model)
	}
	if m.step != stepPort {
		t.Fatalf("expected stepPort, got %v", m.step)
	}
	v := m.View()
	if !strings.Contains(v, "remote 8080") {
		t.Errorf("port step view missing port entries")
	}
	if !strings.Contains(v, "remote 9090") {
		t.Errorf("port step view missing port entries")
	}
	if !strings.Contains(v, "Local ports") {
		t.Errorf("port step view missing 'Local ports' label")
	}
	// Whitelist UX: ports default to excluded, so the checkbox glyph
	// must be [ ] (not [x]) for every row. The exact textinput value
	// may also be present (e.g. "> 8080"), so substring match is fine.
	if strings.Contains(v, "[x]") {
		t.Errorf("port step should default to [ ] checkboxes (excluded), found [x]")
	}
	if !strings.Contains(v, "[ ]") {
		t.Errorf("port step view missing '[ ]' checkbox glyph for default-excluded rows")
	}

	// Render output preview
	t.Logf("\n=== Port step preview (width=100, height=30) ===\n%s", v)
}
