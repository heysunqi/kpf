package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kpf/internal/kubeconfig"
)

// kubeStep is step ①: pick a kubeconfig.
//
// Renders 5 columns: BASENAME, CTX, CLUSTERS, CONTEXTS, USERS. Tuned for
// 80-col terminals: 22+22+12+12+10 = 78 visible cells with no padding.
//
// When no kubeconfigs are found (entries == nil), the table is replaced
// by an empty-state message that names the scanned dirs — the user needs
// to know where kpf looked so they can drop a config there or set
// KPF_KUBECONFIG_DIR.
type kubeStep struct {
	table      table.Model
	entries    []kubeconfig.Entry // indexed parallel to table rows
	empty      bool               // true when no kubeconfigs were discovered
	emptyDirs  []string           // scanned dirs to show in the empty-state message
}

var kubeColumnWidths = []int{22, 22, 12, 12, 10}
var kubeColumnTitles = []string{"BASENAME", "CTX", "CLUSTERS", "CONTEXTS", "USERS"}

func newKubeStep(entries []kubeconfig.Entry, dirs []string) kubeStep {
	if len(entries) == 0 {
		return kubeStep{
			empty:     true,
			emptyDirs: dirs,
		}
	}

	cols := make([]table.Column, 0, len(kubeColumnTitles))
	for i, title := range kubeColumnTitles {
		cols = append(cols, table.Column{Title: title, Width: kubeColumnWidths[i]})
	}

	rows := make([]table.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, buildKubeRow(e))
	}

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithStyles(kubeTableStyles()),
	)

	return kubeStep{
		table:   t,
		entries: append([]kubeconfig.Entry(nil), entries...),
	}
}

// buildKubeRow turns a kubeconfig.Entry into a table row. Counts are
// shown as "N clusters" / "N contexts" / "N users" so the user can scan
// for the heavy multi-cluster config at a glance.
func buildKubeRow(e kubeconfig.Entry) table.Row {
	return table.Row{
		truncateCell(e.Basename, kubeColumnWidths[0]),
		truncateCell(e.CurrentContext, kubeColumnWidths[1]),
		truncateCell(fmt.Sprintf("%d clusters", len(e.Clusters)), kubeColumnWidths[2]),
		truncateCell(fmt.Sprintf("%d contexts", len(e.Contexts)), kubeColumnWidths[3]),
		truncateCell(fmt.Sprintf("%d users", len(e.Users)), kubeColumnWidths[4]),
	}
}

func kubeTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(muted).
		BorderBottom(true).
		Bold(true)
	s.Cell = s.Cell.Padding(0, 0)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(accent).
		Bold(true)
	return s
}

func (k kubeStep) Init() tea.Cmd { return nil }

// filtering reports whether the step is currently accepting filter-input
// keystrokes. Used by app.isAnyListFiltering() to gate global shortcuts
// like 'a' so they don't fire while the user is typing in a filter.
// kubeStep uses a plain bubbles/table without a filter overlay, so this
// is always false — kept as a method to match the list-based steps and
// to make adding filter support later a single-site change.
func (k kubeStep) filtering() bool { return false }

func (k kubeStep) Update(msg tea.Msg) (kubeStep, tea.Cmd) {
	if k.empty {
		// No rows to navigate; just let the user hit esc/quit from here.
		return k, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		k.table.SetWidth(msg.Width - 4)
		k.table.SetHeight(h)
		return k, nil
	case tea.KeyMsg:
		if msg.String() == "enter" {
			idx := k.table.Cursor()
			if idx < 0 || idx >= len(k.entries) {
				return k, nil
			}
			e := k.entries[idx]
			return k, func() tea.Msg {
				return KubeChosenMsg{
					Path:    e.Path,
					Context: e.CurrentContext,
				}
			}
		}
	}
	var cmd tea.Cmd
	k.table, cmd = k.table.Update(msg)
	return k, cmd
}

func (k kubeStep) View() string {
	title := ListTitle.Render(fmt.Sprintf(" Select a kubeconfig (%d) ", len(k.entries)))
	if k.empty {
		return title + "\n\n" +
			StatusWarn.Render("No kubeconfig found.") + "\n\n" +
			"scanned: " + joinComma(k.emptyDirs) + "\n\n" +
			FooterHelp("esc") + " to quit"
	}
	return title + "\n" + k.table.View()
}