package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"kpf/internal/kubeconfig"
)

// kubeStep is step ①: pick a kubeconfig.
type kubeStep struct {
	list list.Model
}

func newKubeStep(entries []kubeconfig.Entry, dirs []string) kubeStep {
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, kubeItem{entry: e})
	}
	if len(items) == 0 {
		items = append(items, noKubeconfigItem{dirs: dirs})
	}
	l := list.New(items, NewDefaultDelegate(), 0, 0)
	l.Title = "Select a kubeconfig"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = ListTitle
	l.SetSize(80, 18)
	return kubeStep{list: l}
}

type noKubeconfigItem struct {
	dirs []string
}

func (i noKubeconfigItem) FilterValue() string { return "" }
func (i noKubeconfigItem) Title() string {
	return "No kubeconfig found"
}
func (i noKubeconfigItem) Description() string {
	return "scanned: " + joinComma(i.dirs)
}

func (k kubeStep) Init() tea.Cmd { return nil }

func (k kubeStep) Update(msg tea.Msg) (kubeStep, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		k.list.SetSize(msg.Width-4, msg.Height-5)
	case tea.KeyMsg:
		if k.list.FilterState() == list.Filtering {
			break
		}
		if msg.String() == "enter" {
			if item, ok := k.list.SelectedItem().(kubeItem); ok {
				return k, func() tea.Msg {
					return KubeChosenMsg{
						Path:    item.entry.Path,
						Context: item.entry.CurrentContext,
					}
				}
			}
		}
	}
	k.list, cmd = k.list.Update(msg)
	return k, cmd
}

func (k kubeStep) View() string {
	return k.list.View()
}
