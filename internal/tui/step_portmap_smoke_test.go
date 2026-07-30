package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPortStep_SmokeRender exercises the post-fix UI end-to-end against
// the original screenshot scenario (Pod with 80/443/8040/8050, user
// forwards only 8050). The test doesn't assert strictly; it logs the
// rendered view so a human can eyeball the checkbox + count + conflict
// markers match the plan.
func TestPortStep_SmokeRender(t *testing.T) {
	m := New("")
	m.width = 120
	m.height = 30
	for _, msg := range []tea.Msg{
		KubeChosenMsg{Path: "k.config", Context: "ctx"},
		NsChosenMsg{Name: "default"},
		ResourceTypeChosenMsg{Kind: "Pod"},
		PortsLoadedMsg{Kind: "Pod", Object: "pod-1", Ports: []int{80, 443, 8040, 8050}, Pod: "pod-1"},
	} {
		m2, _ := m.Update(msg)
		m = m2.(Model)
	}
	// Resize the list so all 4 rows fit on a single page (avoids the
	// pagination-wrap behaviour that confuses cursor movement in tests).
	m.port.list.SetSize(60, 20)

	// Default state: all excluded.
	v := m.View()
	t.Logf("\n=== Default state (all excluded) ===\n%s\n", v)
	if !strings.Contains(v, "(0 selected)") {
		t.Errorf("default footer should say '(0 selected)', got:\n%s", v)
	}

	// User moves cursor to 8050 (row 3) and presses space.
	for i := 0; i < 3; i++ {
		m.port.list, _ = m.port.list.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = m2.(Model)

	v = m.View()
	t.Logf("\n=== After including 8050 (1 selected) ===\n%s\n", v)
	if !strings.Contains(v, "(1 selected)") {
		t.Errorf("expected footer '(1 selected)', got:\n%s", v)
	}
	// Exactly one [x] should appear (the included 8050 row).
	if got := strings.Count(v, "[x]"); got != 1 {
		t.Errorf("expected 1 '[x]' checkbox after including 8050, got %d in:\n%s", got, v)
	}
	if got := strings.Count(v, "[ ]"); got != 3 {
		t.Errorf("expected 3 '[ ]' checkboxes for the still-excluded rows, got %d in:\n%s", got, v)
	}
}