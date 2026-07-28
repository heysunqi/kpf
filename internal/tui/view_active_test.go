package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestActiveStep_RendersTableNotPanic locks in that the new table-based
// active view renders cleanly with both empty and populated inputs. The
// previous list-based implementation could produce empty output on no
// rows; the table widget should at least produce the column headers.
func TestActiveStep_RendersTableNotPanic(t *testing.T) {
	empty := newActiveStep(nil)
	v := empty.View()
	if v == "" {
		t.Error("empty active view should still render headers")
	}

	populated := newActiveStep([]ipcForward{
		{
			ID:         "fwd_0001",
			Kubeconfig: "prod-config",
			Namespace:  "default",
			Kind:       "Pod",
			Object:     "my-pod",
			Bind:       "127.0.0.1",
			Ports:      "8080:80,9090:90",
			Status:     "ready",
			StartedAt:  "2026-07-28T10:00:00Z",
		},
		{
			ID:         "fwd_0002",
			Kubeconfig: "dev-config",
			Namespace:  "staging",
			Kind:       "Service",
			Object:     "api",
			Bind:       "0.0.0.0",
			Ports:      "8443:443",
			Status:     "starting",
			StartedAt:  "2026-07-28T10:01:00Z",
		},
	})
	v = populated.View()
	// The header line carries "Active forwards (2) · 3 ports" — count
	// signals parity intent, not arbitrary.
	if !strings.Contains(v, "Active forwards (2)") {
		t.Errorf("expected count in header, got:\n%s", v)
	}
	if !strings.Contains(v, "3 ports") {
		t.Errorf("expected port count in header, got:\n%s", v)
	}
	// Status cells should keep their prefix glyphs.
	if !strings.Contains(v, "● ready") {
		t.Errorf("expected ready status glyph, got:\n%s", v)
	}
	if !strings.Contains(v, "◐ starting") {
		t.Errorf("expected starting status glyph, got:\n%s", v)
	}
}

// TestActiveStep_DHotkeyEmitsStopForwardMsg verifies that pressing 'd'
// on the highlighted row emits a StopForwardMsg carrying that forward's
// ID. The previous list-based version read the ID via SelectedItem()'s
// activeItem; the table-based version maps cursor() back to its
// parallel forwards slice.
func TestActiveStep_DHotkeyEmitsStopForwardMsg(t *testing.T) {
	step := newActiveStep([]ipcForward{
		{ID: "fwd_first", Status: "ready"},
		{ID: "fwd_second", Status: "ready"},
	})
	step, cmd := step.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("expected a tea.Cmd (StopForwardMsg) on 'd' hotkey")
	}
	msg := cmd()
	stop, ok := msg.(StopForwardMsg)
	if !ok {
		t.Fatalf("expected StopForwardMsg, got %T", msg)
	}
	if stop.ID != "fwd_first" {
		t.Errorf("cursor-0 row should yield fwd_first, got %q", stop.ID)
	}

	// Move cursor down then press d again — second row's ID should be emitted.
	step.table.SetCursor(1)
	_, cmd = step.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	msg = cmd()
	stop, ok = msg.(StopForwardMsg)
	if !ok {
		t.Fatalf("expected StopForwardMsg on row 1, got %T", msg)
	}
	if stop.ID != "fwd_second" {
		t.Errorf("cursor-1 row should yield fwd_second, got %q", stop.ID)
	}
}

// TestActiveStep_NoSelectionNoOp ensures pressing 'd' on an empty
// active view doesn't blow up. Without the forwards slice guard the
// code would have indexed out of bounds.
func TestActiveStep_NoSelectionNoOp(t *testing.T) {
	step := newActiveStep(nil)
	_, cmd := step.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd != nil {
		t.Errorf("'d' on empty active view should be a no-op, got cmd %T", cmd())
	}
}

// TestActiveStep_TruncatesLongContent ensures that namespace/cluster/ports
// strings wider than their column don't break the rendered table. The
// table widget's width is clamped to its column widths, so content beyond
// should be truncated (or visually overflow, depending on the widget's
// behavior) — but rendering must not panic.
func TestActiveStep_TruncatesLongContent(t *testing.T) {
	step := newActiveStep([]ipcForward{
		{
			ID:         "fwd_0001",
			Kubeconfig: "very-long-kubeconfig-basename-that-exceeds-cluster-column-width.config",
			Namespace:  "very-long-namespace-name-exceeding-the-eleven-char-limit",
			Kind:       "Deployment",
			Object:     "frontend-with-a-really-long-name-exceeding-the-22-char-limit",
			Bind:       "192.168.1.1",
			Ports:      "8080:80,9090:90,1234:1234,5678:5678,9999:9999",
			Status:     "ready",
			StartedAt:  "2026-07-28T10:00:00Z",
		},
	})
	v := step.View()
	if v == "" {
		t.Error("long-content row should still render something")
	}
}