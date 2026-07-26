package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// nsStep is step ②: pick a namespace.
type nsStep struct {
	list list.Model
}

// nsDelegate is a 1-line delegate: namespaces only have a name + a
// "(current)" badge, no description worth showing.
func newNsDelegate() *list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.
		Foreground(lipgloss.Color("#FFFFFF")).
		BorderForeground(accent).
		Bold(true)
	d.Styles.NormalTitle = d.Styles.NormalTitle.Foreground(white)
	d.SetSpacing(0)
	return &d
}

func newNsStep(namespaces []string, current string) nsStep {
	items := make([]list.Item, 0, len(namespaces))
	for _, n := range namespaces {
		items = append(items, nsItem{name: n, current: n == current})
	}
	l := list.New(items, newNsDelegate(), 0, 0)
	l.Title = "Select a namespace"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = ListTitle
	l.SetSize(80, 18)
	return nsStep{list: l}
}

func (n nsStep) Init() tea.Cmd { return nil }

func (n nsStep) Update(msg tea.Msg) (nsStep, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		n.list.SetSize(msg.Width-4, msg.Height-5)
	case tea.KeyMsg:
		if n.list.FilterState() == list.Filtering {
			break
		}
		if msg.String() == "enter" {
			if item, ok := n.list.SelectedItem().(nsItem); ok {
				return n, func() tea.Msg {
					return NsChosenMsg{Name: item.name}
				}
			}
		}
	}
	n.list, cmd = n.list.Update(msg)
	return n, cmd
}

func (n nsStep) View() string {
	return n.list.View()
}
