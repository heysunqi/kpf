package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// objStep is step ④: pick a specific object, see its ports.
type objStep struct {
	list list.Model
	kind string
}

func newObjStepForKind(kind, _ /* objectHint */, _ /* podHint */ string, portsHint []int) objStep {
	items := []list.Item{}
	if len(portsHint) > 0 {
		// We've pre-fetched ports; render a single "use ports" entry until
		// the user navigates and the list is populated by loadResources.
		items = append(items, objectItem{
			name:        "—",
			kind:        kind,
			status:      "loading…",
			ports:       portsHint,
			age:         "",
			resolvedPod: "",
		})
	}
	l := list.New(items, NewDefaultDelegate(), 0, 0)
	l.Title = "Select a " + kind
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = ListTitle
	l.SetSize(80, 18)
	return objStep{list: l, kind: kind}
}

// renderResourcesFromKind builds an objStep pre-populated with the
// real resource list returned by loadResourcesCmd.
func renderResourcesFromKind(kind string, items []resourceItem) objStep {
	rows := make([]list.Item, 0, len(items))
	for _, r := range items {
		rows = append(rows, objectItem{
			name:        r.Name,
			status:      statusOrReady(r),
			kind:        kind,
			ports:       nil, // unknown until user picks; will be filled by loadPortsCmd
			replicas:    r.Replicas,
			age:         r.Age,
			resolvedPod: r.PodName,
		})
	}
	l := list.New(rows, NewDefaultDelegate(), 0, 0)
	l.Title = "Select a " + kind
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = ListTitle
	l.SetSize(80, 18)
	return objStep{list: l, kind: kind}
}

func statusOrReady(r resourceItem) string {
	if r.Status != "" {
		return r.Status
	}
	return r.Ready
}

func (o objStep) Init() tea.Cmd { return nil }

func (o objStep) Update(msg tea.Msg) (objStep, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		o.list.SetSize(msg.Width-4, msg.Height-5)
	case tea.KeyMsg:
		if o.list.FilterState() == list.Filtering {
			break
		}
		if msg.String() == "enter" {
			if item, ok := o.list.SelectedItem().(objectItem); ok {
				name := item.name
				pod := item.resolvedPod
				// Trigger async port fetch — the resulting PortsLoadedMsg will
				// transition us into step ⑤. Until then, the user's local
				// view of step ⑤ is delayed; we send an empty ObjectChosenMsg
				// and let the update handler dispatch the load command.
				return o, func() tea.Msg {
					return ObjectChosenMsg{
						Name:        name,
						PodName:     pod,
						RemotePorts: item.ports,
					}
				}
			}
		}
	}
	o.list, cmd = o.list.Update(msg)
	return o, cmd
}

func (o objStep) View() string {
	if len(o.list.Items()) == 0 {
		return fmt.Sprintf("(no %s found)", o.kind)
	}
	return o.list.View()
}