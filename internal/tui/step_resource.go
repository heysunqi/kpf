package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// rtStep is step ③: pick a resource type (Pod, Service, Deployment, ...).
type rtStep struct {
	list list.Model
}

// rtDelegate is 1-line — only 5 kinds ever, "X resources" desc is
// redundant when step ④ lists the actual resources.
func newRtDelegate() *list.DefaultDelegate {
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

func newRtStep(types []mockResourceType) rtStep {
	items := make([]list.Item, 0, len(types))
	for _, t := range types {
		items = append(items, rtItem{kind: t.Kind, count: t.Count})
	}
	l := list.New(items, newRtDelegate(), 0, 0)
	l.Title = "Select a resource type"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = ListTitle
	l.SetSize(80, 18)
	return rtStep{list: l}
}

func (r rtStep) Init() tea.Cmd { return nil }

func (r rtStep) Update(msg tea.Msg) (rtStep, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.list.SetSize(msg.Width-4, msg.Height-5)
	case tea.KeyMsg:
		if msg.String() == "enter" {
			if item, ok := r.list.SelectedItem().(rtItem); ok {
				return r, func() tea.Msg {
					return ResourceTypeChosenMsg{Kind: item.kind}
				}
			}
		}
	}
	r.list, cmd = r.list.Update(msg)
	return r, cmd
}

func (r rtStep) View() string {
	return r.list.View()
}
