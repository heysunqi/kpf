package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// activeStep is the 'a' shortcut view: list of active forwards.
type activeStep struct {
	list list.Model
}

func newActiveStep(forwards []ipcForward) activeStep {
	items := make([]list.Item, 0, len(forwards))
	portCount := 0
	for _, f := range forwards {
		items = append(items, activeItem{
			id:       f.ID,
			cluster:  f.Kubeconfig,
			ns:       f.Namespace,
			resource: f.Kind + "/" + f.Object,
			bind:     f.Bind,
			ports:    f.Ports,
			status:   f.Status,
			age:      f.StartedAt,
		})
		// f.Ports is a comma-separated string of "local:remote" pairs.
		if f.Ports != "" {
			portCount += strings.Count(f.Ports, ",") + 1
		}
	}
	l := list.New(items, NewDefaultDelegate(), 0, 0)
	// Title shows both forwards and ports so leaks are visible at a glance:
	// if "forwards (2) · 3 ports" but only 2 listeners exist, the user knows
	// something is off. (Daemon-side parity check is the authoritative one;
	// this is just the surface signal.)
	l.Title = fmt.Sprintf("Active forwards (%d) · %d ports", len(items), portCount)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = ListTitle
	l.SetSize(80, 18)
	return activeStep{list: l}
}

func (a activeStep) Init() tea.Cmd { return nil }

func (a activeStep) Update(msg tea.Msg) (activeStep, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.list.SetSize(msg.Width-4, msg.Height-5)
	case tea.KeyMsg:
		// d / x / delete stops the highlighted forward.
		if a.list.FilterState() != list.Filtering {
			switch msg.String() {
			case "d", "x", "delete":
				if item, ok := a.list.SelectedItem().(activeItem); ok {
					id := item.id
					return a, func() tea.Msg {
						return StopForwardMsg{ID: id, Err: nil}
					}
				}
			}
		}
	}
	a.list, cmd = a.list.Update(msg)
	return a, cmd
}

func (a activeStep) View() string {
	return a.list.View()
}