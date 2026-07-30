package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// NewDefaultDelegate returns a styled default delegate with description.
// Returned as a pointer so callers can mutate Spacing/Height before
// handing it to a list (the ItemDelegate interface boxes by value).
func NewDefaultDelegate() *list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.
		Foreground(lipgloss.Color("#FFFFFF")).
		BorderForeground(accent).
		Bold(true)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.
		Foreground(lipgloss.Color("#FFFFFF")).
		BorderForeground(accent)
	d.Styles.NormalTitle = d.Styles.NormalTitle.
		Foreground(white)
	d.Styles.NormalDesc = d.Styles.NormalDesc.
		Foreground(muted)
	d.SetSpacing(0)
	return &d
}

// NewTitleOnlyDelegate returns a styled default delegate with the
// description line suppressed. Use this when the right pane or a
// help line already communicates the secondary information — keeping
// each row to one line lets more items fit in the viewport, which
// matters most for the port step where a single Pod can expose
// 4-8 ports and the user needs to see all of them at once to pick
// which subset to forward.
func NewTitleOnlyDelegate() *list.DefaultDelegate {
	d := NewDefaultDelegate()
	d.ShowDescription = false
	return d
}

// nsItem implements list.Item for a namespace.
type nsItem struct {
	name    string
	current bool // highlight if matches the kubeconfig's default ns
}

func (i nsItem) FilterValue() string { return i.name }
func (i nsItem) Title() string {
	if i.current {
		return i.name + "  (current)"
	}
	return i.name
}
func (i nsItem) Description() string { return "" }

// rtItem implements list.Item for a resource type.
type rtItem struct {
	kind  string
	count int
}

func (i rtItem) FilterValue() string { return i.kind }
func (i rtItem) Title() string       { return i.kind }
func (i rtItem) Description() string {
	return fmt.Sprintf("%d resources in namespace", i.count)
}

// objectItem implements list.Item for one k8s object.
type objectItem struct {
	name        string
	status      string
	kind        string
	ports       []int
	replicas    string
	age         string
	resolvedPod string // for Deployment/STS/RS
}

func (i objectItem) FilterValue() string { return i.name }
func (i objectItem) Title() string       { return i.name }
func (i objectItem) Description() string {
	parts := []string{}
	if i.status != "" {
		parts = append(parts, i.status)
	}
	if i.replicas != "" {
		parts = append(parts, i.replicas)
	}
	if len(i.ports) > 0 {
		parts = append(parts, fmt.Sprintf("ports=%v", i.ports))
	}
	if i.resolvedPod != "" {
		parts = append(parts, "→ "+i.resolvedPod)
	}
	if i.age != "" {
		parts = append(parts, "age="+i.age)
	}
	return joinComma(parts)
}

// helpers

func joinShort(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	if len(s) == 1 {
		return s[0]
	}
	return fmt.Sprintf("%s (+%d)", s[0], len(s)-1)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "  ·  "
		}
		out += p
	}
	return out
}

// teaQuit is a small helper around tea.QuitMsg to ensure consistent types.
type teaQuit struct{}
