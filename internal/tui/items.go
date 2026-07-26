package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"

	"kpf/internal/kubeconfig"
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

// kubeItem implements list.Item for a kubeconfig.
type kubeItem struct {
	entry kubeconfig.Entry
}

func (i kubeItem) FilterValue() string { return i.entry.Basename }
func (i kubeItem) Title() string       { return i.entry.Basename }
func (i kubeItem) Description() string {
	return fmt.Sprintf("cluster=%s ctx=%s users=%d",
		joinShort(i.entry.Clusters), i.entry.CurrentContext, len(i.entry.Users))
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

type activeItem struct {
	id       string
	cluster  string
	ns       string
	resource string
	bind     string
	ports    string
	status   string
	age      string
}

func (i activeItem) FilterValue() string { return i.id + " " + i.resource }
func (i activeItem) Title() string       { return fmt.Sprintf("%s  %s", i.id, i.resource) }
func (i activeItem) Description() string {
	statusStr := i.status
	switch statusStr {
	case "ready":
		statusStr = StatusOK.Render("● " + statusStr)
	case "starting":
		statusStr = StatusWarn.Render("◐ " + statusStr)
	case "dropped", "reconnecting":
		statusStr = StatusWarn.Render("⚠ " + statusStr)
	case "stopped":
		statusStr = StatusErr.Render("○ " + statusStr)
	case "stale":
		statusStr = StatusErr.Render("✗ " + statusStr)
	case "error":
		statusStr = StatusErr.Render("! " + statusStr)
	}
	return fmt.Sprintf("%s  %s/%s  bind=%s  ports=%s  age=%s",
		statusStr, i.cluster, i.ns, i.bind, i.ports, i.age)
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
