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
// (CLUSTER, AGE) get truncated by the table widget's column-width clamping.
//
// The forwards slice is retained alongside the table so d/x/delete hotkeys
// can map the current cursor index back to a forward id and emit a
// StopForwardMsg. The table's SelectedRow() returns the rendered cell
// strings, not the original ipcForward — keeping our own copy avoids
// re-fetching via the cursor.
type activeStep struct {
	table    table.Model
	forwards []ipcForward // indexed parallel to table rows
	title    string       // pre-rendered header line ("Active forwards (N) · M ports")
}

// activeColumnWidths are the visible widths (in monospace cells) for each
// table column. The bubbles/table widget pads each cell with 2 chars by
// default; we override the Cell style below to use Padding(0, 0) so the
// Width values here match the rendered width exactly. Total: 9+12+22+11+
// 11+24+16+10 = 115 — fits a 120-col terminal; on narrower widths the
// rightmost columns overflow but the data is still readable.
var activeColumnWidths = []int{9, 12, 22, 11, 11, 24, 16, 10}

func newActiveStep(forwards []ipcForward) activeStep {
	cols := make([]table.Column, 0, len(activeColumnTitles))
	for i, title := range activeColumnTitles {
		cols = append(cols, table.Column{Title: title, Width: activeColumnWidths[i]})
	}

	rows := make([]table.Row, 0, len(forwards))
	portCount := 0
	for _, f := range forwards {
		rows = append(rows, buildActiveRow(f))
		if f.Ports != "" {
			portCount += strings.Count(f.Ports, ",") + 1
		}
	}

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithStyles(activeTableStyles()),
	)

	// Title is rendered manually (the v1.0.0 Model has no Title field).
	// Carries both forward count and total port count so a leak (forwards
	// (2) · 3 ports but only 2 listeners) is visible at a glance. The
	// daemon-side parity check (`kpf doctor`) is authoritative; this is
	// just a surface-level signal.
	title := fmt.Sprintf(" Active forwards (%d) · %d ports ", len(forwards), portCount)

	return activeStep{
		table:    t,
		forwards: append([]ipcForward(nil), forwards...),
		title:    title,
	}
}

var activeColumnTitles = []string{"ID", "STATUS", "KIND/OBJECT", "NS", "BIND", "PORTS", "CLUSTER", "AGE"}

func buildActiveRow(f ipcForward) table.Row {
	status := activeStatusCell(f.Status)
	return table.Row{
		f.ID,
		status,
		truncateCell(f.Kind+"/"+f.Object, activeColumnWidths[2]),
		truncateCell(f.Namespace, activeColumnWidths[3]),
		truncateCell(f.Bind, activeColumnWidths[4]),
		truncateCell(f.Ports, activeColumnWidths[5]),
		truncateCell(f.Kubeconfig, activeColumnWidths[6]),
		truncateCell(f.StartedAt, activeColumnWidths[7]),
	}
}

// activeStatusCell renders a status string with the same icon + color
// scheme as the previous list delegate (kept so muscle memory carries over)
// and returns it as a plain string with embedded ANSI escapes. The table
// widget treats each cell as a single line of text; ANSI escapes are
// transparent to its width math via lipgloss.Width.
func activeStatusCell(status string) string {
	switch status {
	case "ready":
		return StatusOK.Render("● " + status)
	case "starting":
		return StatusWarn.Render("◐ " + status)
	case "dropped", "reconnecting":
		return StatusWarn.Render("⚠ " + status)
	case "stopped":
		return StatusErr.Render("○ " + status)
	case "stale":
		return StatusErr.Render("✗ " + status)
	case "error":
		return StatusErr.Render("! " + status)
	}
	return status
}

// truncateCell shortens s to max visual cells. Used to keep long namespace
// names or multi-pair port lists from blowing out the column layout. The
// result has ellipsis appended if truncation happened.
func truncateCell(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	// lipgloss has a Truncate helper that handles ANSI correctly; use it.
	return lipgloss.NewStyle().MaxWidth(max).Render(s)
}

func activeTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(muted).
		BorderBottom(true).
		Bold(true)
	// Drop the default (0, 1) cell padding so column Width values match
	// the rendered width exactly — see the comment on activeColumnWidths.
	s.Cell = s.Cell.Padding(0, 0)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(accent).
		Bold(true)
	return s
}

func (a activeStep) Init() tea.Cmd { return nil }

func (a activeStep) Update(msg tea.Msg) (activeStep, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Leave 4 cols of horizontal margin and 1 row for the title and 1 for
		// the footer. The bubbles/table implementation clamps widths to its
		// configured column Widths regardless of container width, so a narrow
		// terminal ends up with horizontal overflow rather than truncation —
		// acceptable for now since the wizard itself demands ≥80 cols.
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		a.table.SetWidth(msg.Width - 4)
		a.table.SetHeight(h)
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
	return ListTitle.Render(a.title) + "\n" + a.table.View()
}