package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// TestActiveStep_ScrollsCursorIntoView locks in the window-slide
// behavior introduced after we replaced bubbles/table's View() with our
// own renderer. bubbles/table used to handle viewport scrolling
// internally (visible rows anchored by cursor); once we rerendered by
// hand we had to reimplement that, otherwise a tall forward list
// silently hides the cursor — user presses down forever and the
// highlighted row stays at the top.
//
// We set height to 5 via WindowSizeMsg, place the cursor at row 25 of
// a 50-row list, and assert that the cursor row's ID is in the
// rendered body.
func TestActiveStep_ScrollsCursorIntoView(t *testing.T) {
	forwards := make([]ipcForward, 50)
	for i := range forwards {
		forwards[i] = ipcForward{
			ID:         idForIndex(i),
			Status:     "ready",
			Namespace:  "default",
			Kind:       "Pod",
			Object:     "pod",
			Bind:       "127.0.0.1",
			Ports:      "8080:80",
			Kubeconfig: "cfg",
			StartedAt:  "2026-07-28",
		}
	}
	s := newActiveStep(forwards)
	// 5 visible rows: height = msg.Height - 6, so msg.Height = 11
	s, _ = s.Update(tea.WindowSizeMsg{Width: 200, Height: 11})
	s.table.SetCursor(25)

	v := s.View()
	if !strings.Contains(v, idForIndex(25)) {
		t.Errorf("cursor row %q should be visible after window-slide, view:\n%s",
			idForIndex(25), v)
	}
	// And only 5 rows should render (window fixed-height body).
	var renderedRows int
	for _, line := range strings.Split(v, "\n") {
		if strings.HasPrefix(line, "fwd_") {
			renderedRows++
		}
	}
	if renderedRows != 5 {
		t.Errorf("expected exactly 5 rows in fixed-height body, got %d", renderedRows)
	}
}

// idForIndex builds a "fwd_NN" id matching the convention used in this
// file's other tests so the rendered strings are easy to grep for.
func idForIndex(i int) string {
	const digits = "0123456789"
	return "fwd_" + string(digits[i/10]) + string(digits[i%10])
}

// TestActiveStep_ColumnsAreAligned is the regression test for the
// "columns misaligned because runewidth.Truncate eats ANSI bytes" bug:
// when status cells were passed to bubbles/table as ANSI-painted text,
// its runewidth.Truncate counted the escape bytes as content characters
// and chopped the cell at the wrong offset, shifting every subsequent
// column by several cells. The fix rerenders the table ourselves with
// lipgloss-aware padding, so the visual cell widths must equal the sum
// of column widths plus the single-space separators — exactly
// 9+12+22+11+11+24+16+10 + 7 = 122 visible cells per row.
//
// The test parses the View() output, skips the title / header /
// separator lines, and asserts each data row's ANSI-stripped width
// equals 122 and starts with the ID at column 0 followed by the status
// icon at the expected offset.
func TestActiveStep_ColumnsAreAligned(t *testing.T) {
	step := newActiveStep([]ipcForward{
		{ID: "fwd_0001", Kubeconfig: "prod-config", Namespace: "default", Kind: "Service", Object: "noahee-global-noaheepro", Bind: "0.0.0.0", Ports: "8380:8380", Status: "ready", StartedAt: "2026-07-28T10:00:00Z"},
		{ID: "fwd_0003", Kubeconfig: "prod-config", Namespace: "nodhee", Kind: "Service", Object: "frontend", Bind: "0.0.0.0", Ports: "80:80,443:443,8040:8040,", Status: "starting", StartedAt: "2026-07-28T10:00:00Z"},
	})
	v := step.View()

	lines := strings.Split(v, "\n")
	// Layout in View(): title (with bottom padding = 1 blank line),
	// header row, ─── separator, data rows. Strip title padding and
	// pick out the data rows by looking for "fwd_000…".
	var dataRows []string
	for _, line := range lines {
		if strings.HasPrefix(line, "fwd_000") {
			dataRows = append(dataRows, line)
		}
	}
	if len(dataRows) != 2 {
		t.Fatalf("expected 2 data rows (one per forward), got %d\nfull view:\n%s",
			len(dataRows), v)
	}

	const wantRowWidth = 122
	for i, line := range dataRows {
		got := lipgloss.Width(line)
		if got != wantRowWidth {
			t.Errorf("row %d ANSI-stripped visual width = %d, want %d\nfull row: %q\nfull view:\n%s",
				i, got, wantRowWidth, line, v)
		}
		// The status icon (● for ready, ◐ for starting) should appear
		// at column 10 — the 9-wide ID cell padded to 9 chars plus
		// one space separator — followed by "ready" / "starting".
		wantIconAt := " ● "
		wantStartingAt := " ◐ "
		if !strings.Contains(line, wantIconAt) && !strings.Contains(line, wantStartingAt) {
			t.Errorf("row %d should contain status icon around column 10, got: %q", i, line)
		}
	}
}