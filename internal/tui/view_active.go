package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// activeStep is the 'a' shortcut view: a tabular listing of active forwards.
//
// Renders 8 columns: ID, STATUS, KIND/OBJECT, NS, BIND, PORTS, CLUSTER, AGE.
// Designed for terminals ≥120 wide; on narrower widths the rightmost columns
// (CLUSTER, AGE) get truncated visually by the lipgloss styled output.
//
// Implementation note: we previously handed colored ANSI cells to
// bubbles/table's renderer. bubbles/table uses
// `runewidth.Truncate(value, colWidth, "…")` (from mattn/go-runewidth),
// which counts raw bytes — including ANSI escape codes — so an "● ready"
// cell painted with `\x1b[32m…\x1b[0m` got its escapes counted as content
// and the visible text was chopped at the wrong offset, shifting every
// subsequent column by several cells. We now render the table ourselves
// and only use the bubbles/table model for cursor state + key dispatch.
//
// The forwards slice is retained alongside the table so d/x/delete hotkeys
// can map the current cursor index back to a forward id and emit a
// StopForwardMsg.
type activeStep struct {
	table    table.Model // cursor + key navigation only; View() is ours.
	forwards []ipcForward
	title    string
	height   int // body rows visible; updated on WindowSizeMsg
}

// Column widths. These match the visible cell widths exactly because we
// control padding via celled() (truncate-then-pad with plain spaces).
// Total: 9+12+22+11+11+24+16+10 = 115, plus 7 single-space separators.
var activeColumnWidths = []int{9, 12, 22, 11, 11, 24, 16, 10}
var activeColumnTitles = []string{"ID", "STATUS", "KIND/OBJECT", "NS", "BIND", "PORTS", "CLUSTER", "AGE"}

func newActiveStep(forwards []ipcForward) activeStep {
	// We keep a bubbles/table instance around for cursor state and key
	// dispatch (its Update handler does up/down/page-up/page-down/home/end
	// correctly and reports Cursor() for our d/x/delete mapping). What
	// we DON'T do is call its View() — see the comment on activeStep.
	cols := make([]table.Column, 0, len(activeColumnTitles))
	for i, t := range activeColumnTitles {
		cols = append(cols, table.Column{Title: t, Width: activeColumnWidths[i]})
	}
	rows := make([]table.Row, 0, len(forwards))
	for _, f := range forwards {
		rows = append(rows, table.Row{f.ID, activePlainStatus(f.Status)})
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
	)
	portCount := 0
	for _, f := range forwards {
		if f.Ports != "" {
			portCount += strings.Count(f.Ports, ",") + 1
		}
	}
	title := fmt.Sprintf(" Active forwards (%d) · %d ports ", len(forwards), portCount)
	return activeStep{
		table:    t,
		forwards: append([]ipcForward(nil), forwards...),
		title:    title,
	}
}

// activePlainStatus returns the icon + word form of the status (no ANSI).
// bubbles/table only stores this string as a Cursor-tracking hint — it is
// never rendered by us, because doing so would re-introduce the
// ANSI-vs-runewidth corruption.
func activePlainStatus(status string) string {
	switch status {
	case "ready":
		return "● ready"
	case "starting":
		return "◐ starting"
	case "dropped":
		return "⚠ dropped"
	case "stopped":
		return "○ stopped"
	case "stale":
		return "✗ stale"
	case "error":
		return "! error"
	}
	return status
}

// activeStatusParts returns the icon-word label and the matching style for
// the status string. Used by our own View() where we can safely render
// styled ANSI bytes because we control the cell width math directly.
func activeStatusParts(status string) (string, lipgloss.Style) {
	switch status {
	case "ready":
		return "● ready", StatusOK
	case "starting":
		return "◐ starting", StatusWarn
	case "dropped":
		return "⚠ dropped", StatusWarn
	case "stopped":
		return "○ stopped", StatusErr
	case "stale":
		return "✗ stale", StatusErr
	case "error":
		return "! error", StatusErr
	}
	return status, lipgloss.NewStyle()
}

func (a activeStep) Init() tea.Cmd { return nil }

func (a activeStep) Update(msg tea.Msg) (activeStep, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Body height = total - title(1) - header(1) - separator(1) - footer(1)
		// and Body in app.go applies another -2 for its padding, so use
		// the same -6 the previous bubbles/table-based version used.
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		a.height = h
		return a, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "d", "x", "delete":
			idx := a.table.Cursor()
			if idx < 0 || idx >= len(a.forwards) {
				return a, nil
			}
			id := a.forwards[idx].ID
			return a, func() tea.Msg {
				return StopForwardMsg{ID: id, Err: nil}
			}
		}
	}
	var cmd tea.Cmd
	a.table, cmd = a.table.Update(msg)
	return a, cmd
}

func (a activeStep) View() string {
	title := ListTitle.Render(a.title)
	headerLine := renderActiveHeader()
	sep := strings.Repeat("─", activeTotalRowWidth())

	cursor := a.table.Cursor()
	n := len(a.forwards)

	// Decide which slice of [start,end) to render. When forwards exceed
	// a.height we slide the window so the cursor row is always visible,
	// and we always render exactly a.height rows (padding the tail with
	// blank lines so the body height stays stable — important so the
	// header doesn't shift when the cursor crosses a window boundary).
	visible := a.height
	if visible < 1 || visible > n {
		visible = n
	}
	start, end := 0, n
	if n > visible {
		// Keep cursor in [start, end). When the cursor is near the top
		// we anchor at 0; when it moves down we slide so cursor sits
		// at the last row of the window.
		start = cursor - (visible - 1)
		if start < 0 {
			start = 0
		}
		end = start + visible
		if end > n {
			end = n
			start = end - visible
			if start < 0 {
				start = 0
			}
		}
	}

	rowLines := make([]string, visible)
	for i := 0; i < visible; i++ {
		fwdIdx := start + i
		if fwdIdx >= end {
			rowLines[i] = strings.Repeat(" ", activeTotalRowWidth())
			continue
		}
		line := renderActiveRow(a.forwards[fwdIdx])
		if fwdIdx == cursor {
			line = activeSelectedRowStyle.Render(line)
		}
		rowLines[i] = line
	}

	body := strings.Join(rowLines, "\n")
	if n == 0 {
		body = "(no active forwards)"
	}

	return title + "\n" + headerLine + "\n" + sep + "\n" + body
}

// activeTotalRowWidth is the visible width of a fully-populated row, equal
// to the sum of column widths plus the single-space separators between
// the 8 cells.
func activeTotalRowWidth() int {
	w := 0
	for _, cw := range activeColumnWidths {
		w += cw
	}
	w += len(activeColumnWidths) - 1
	return w
}

func renderActiveHeader() string {
	parts := make([]string, len(activeColumnTitles))
	for i, t := range activeColumnTitles {
		parts[i] = celled(t, activeColumnWidths[i])
	}
	return activeHeaderStyle.Render(strings.Join(parts, " "))
}

func renderActiveRow(f ipcForward) string {
	parts := []string{
		celled(f.ID, activeColumnWidths[0]),
		renderActiveStatus(f.Status),
		celled(f.Kind+"/"+f.Object, activeColumnWidths[2]),
		celled(f.Namespace, activeColumnWidths[3]),
		celled(f.Bind, activeColumnWidths[4]),
		celled(f.Ports, activeColumnWidths[5]),
		celled(f.Kubeconfig, activeColumnWidths[6]),
		celled(f.StartedAt, activeColumnWidths[7]),
	}
	return strings.Join(parts, " ")
}

func renderActiveStatus(s string) string {
	label, style := activeStatusParts(s)
	return style.Render(celled(label, activeColumnWidths[1]))
}

// truncateCell shortens s to at most w visible cells (ANSI-safe via
// lipgloss.MaxWidth). Used by buildKubeRow in step_kubeconfig.go and
// elsewhere where plain truncation is enough and the call site is
// responsible for any padding. celled below wraps truncateCell plus
// trailing-space padding — use that for column-fixed layouts.
func truncateCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// celled truncates s to at most w visible cells (lipgloss ANSI-aware)
// and then pads it with trailing spaces to exactly w cells. Truncation
// uses lipgloss.MaxWidth which handles ANSI escapes correctly so this
// helper is safe to use either before or after applying a status color.
func celled(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = truncateCell(s, w)
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
