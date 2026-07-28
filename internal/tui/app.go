// Package tui implements the kpf Bubble Tea application.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kpf/internal/kubeconfig"
)

// step enumerates the wizard states plus the active view.
type step int

const (
	stepKubeconfig step = iota
	stepNamespace
	stepResource
	stepObject
	stepPort
	stepActive
	stepQuitting
)

func (s step) label() string {
	switch s {
	case stepKubeconfig:
		return "① Kubeconfig"
	case stepNamespace:
		return "② Namespace"
	case stepResource:
		return "③ Resource"
	case stepObject:
		return "④ Object"
	case stepPort:
		return "⑤ Ports"
	case stepActive:
		return "Active"
	}
	return ""
}

// Model is the root Bubble Tea model.
type Model struct {
	width, height int

	// Wizard state accumulates across steps.
	spec WizardSpec

	// Current step.
	step step

	// Sub-models for each step.
	kube   kubeStep
	ns     nsStep
	rt     rtStep
	obj    objStep
	port   portStep
	active activeStep

	// Bridge to the daemon.
	bridge *Bridge
	socket string

	// Transient status message shown after a transition or success.
	status string
	err    error

	// starting is true between the moment the user presses Enter at step ⑤
	// and the moment the daemon's forward.start IPC returns. While true, the
	// root swallows Enter at step ⑤ so the user can't enqueue duplicate
	// forwards by mashing the key during a slow k8s dial.
	starting bool
	// spinner animates the "starting forward…" indicator. The tick loop runs
	// from boot but the view is only rendered when m.starting is true.
	spinner spinner.Model

	keys GlobalKeys
}

// New constructs the root model and starts loading kubeconfigs from the
// daemon in the background.
func New(socket string) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := Model{
		step:    stepKubeconfig,
		spec:    WizardSpec{Bind: "0.0.0.0"},
		keys:    DefaultGlobalKeys(),
		socket:  socket,
		bridge:  newBridge(socket),
		spinner: sp,
	}
	// Start with an empty kube step; the Init cmd loads the real entries.
	m.kube = newKubeStep(nil, nil)
	return m
}

// Init is the bubbletea entry point. It kicks off async data loading.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadKubeconfigsCmd(m.socket), m.spinner.Tick)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case spinner.TickMsg:
		// Keep the spinner animating. The view is only rendered when
		// m.starting is true, so this is a no-op cost when idle.
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		switch m.step {
		case stepKubeconfig:
			m.kube, cmd = m.kube.Update(msg)
		case stepNamespace:
			m.ns, cmd = m.ns.Update(msg)
		case stepResource:
			m.rt, cmd = m.rt.Update(msg)
		case stepObject:
			m.obj, cmd = m.obj.Update(msg)
		case stepPort:
			m.port, cmd = m.port.Update(msg)
		case stepActive:
			m.active, cmd = m.active.Update(msg)
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// While any list is in filter-typing mode, the list owns all keys
		// (letters insert into the filter, esc exits the filter). Don't let
		// global shortcuts like 'a' hijack the input.
		if m.isAnyListFiltering() {
			break
		}
		// Block Enter at step ⑤ while a forward is being started. Without
		// this, a slow k8s dial (15s) lets the user mash Enter and queue
		// multiple forward.start IPC calls, each producing a duplicate
		// forward stuck in Dropped/Starting state.
		if m.step == stepPort && m.starting && msg.String() == "enter" {
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			m.step = stepQuitting
			m.bridge.close()
			return m, tea.Quit
		case "esc":
			if m.step == stepActive {
				m.step = stepKubeconfig
				return m, nil
			}
			if m.step > stepKubeconfig && m.step < stepActive {
				m.step--
				m.status = ""
				return m, nil
			}
		case "a":
			if m.step != stepActive {
				m.step = stepActive
				return m, tea.Batch(loadForwardsCmd(m.socket), tickActiveCmd())
			}
		}

	case KubeconfigsLoadedMsg:
		entries := convertKubeEntries(msg.Entries)
		m.kube = newKubeStep(entries, msg.Dirs)
		m.kube, _ = m.kube.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		if msg.Err != nil {
			m.err = msg.Err
		}
		return m, nil

	case NamespacesLoadedMsg:
		if msg.Err != nil {
			m.status = StatusErr.Render("✗ " + shortErr(msg.Err))
			m.err = nil
			return m, nil
		}
		m.ns = newNsStep(msg.List, "")
		m.ns, _ = m.ns.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		m.status = fmt.Sprintf("kubeconfig: %s", msg.Path)
		return m, nil

	case ResourcesLoadedMsg:
		if msg.Err != nil {
			m.status = StatusErr.Render("✗ " + shortErr(msg.Err))
			m.err = nil
			return m, nil
		}
		// Populate the object picker (step ④). The kind was already chosen,
		// so m.rt doesn't need to be touched.
		m.obj = renderResourcesFromKind(msg.Kind, msg.List)
		m.obj, _ = m.obj.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		m.status = fmt.Sprintf("namespace: %s · %s", m.spec.Namespace, m.spec.ResourceKind)
		return m, nil

	case PortsLoadedMsg:
		if msg.Err != nil {
			// Show in body status with a red ✗ — and clear the footer err
			// so the same message isn't duplicated across both lines.
			m.status = StatusErr.Render("✗ " + shortErr(msg.Err))
			m.err = nil
			return m, nil
		}
		m.spec.PodName = msg.Pod
		m.spec.SelectedPorts = msg.Ports
		m.step = stepPort
		m.port = newPortStep(msg.Ports, m.spec.Bind)
		m.status = fmt.Sprintf("object: %s/%s", m.spec.ResourceKind, msg.Object)
		// Fire the authoritative claimed-ports query in parallel with the
		// port step's textinput blink. The OS-level probe inside
		// newPortStep is a best-effort check; this IPC call returns the
		// daemon's authoritative list of ports its manager claims, which
		// catches duplicates the OS probe misses (daemon SPDY dial race).
		return m, tea.Batch(m.port.Init(), loadClaimedPortsCmd(m.socket))

	case TickActiveMsg:
		if m.step == stepActive {
			return m, tea.Batch(loadForwardsCmd(m.socket), tickActiveCmd())
		}
		return m, nil

	case StopForwardMsg:
		// User-initiated stop from the active view (the 'd' hotkey). Fire
		// the IPC and reload the list. We must NOT also handle the result
		// here — the IPC cmd returns a separate StopForwardResultMsg, which
		// is the only place we read ipc errors from. Mixing the two on one
		// message caused a phantom "not found" error: the success result
		// looked identical to the user trigger, so the app re-fired the
		// stop, which then genuinely failed with "not found".
		return m, tea.Batch(stopActiveForwardCmd(m.socket, msg.ID), loadForwardsCmd(m.socket))

	case StopForwardResultMsg:
		// IPC completed (success or failure). Only show the error on
		// failure; success is silent — the active view will refresh via
		// loadForwardsCmd and the stopped forward will drop out.
		if msg.Err != nil {
			m.status = StatusErr.Render("✗ " + shortErr(msg.Err))
			m.err = nil
		}
		return m, loadForwardsCmd(m.socket)

	case ForwardEventMsg:
		// Instant refresh: any forward.* event in the active view triggers a re-fetch.
		if m.step == stepActive {
			return m, loadForwardsCmd(m.socket)
		}
		return m, nil

	case ForwardsLoadedMsg:
		if msg.Err != nil {
			m.status = StatusErr.Render("✗ " + shortErr(msg.Err))
			m.err = nil
			return m, nil
		}
		m.active = newActiveStep(msg.List)
		m.active, _ = m.active.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		return m, nil

	case ForwardStartedMsg:
		m.starting = false
		if msg.Err != nil {
			m.status = StatusErr.Render("✗ " + shortErr(msg.Err))
			m.err = nil
			return m, nil
		}
		m.status = StatusOK.Render(fmt.Sprintf(
			"✓ forward %s started — local ports: %v — press a to view, enter to add another",
			msg.ID, msg.LocalPorts))
		m.step = stepKubeconfig
		m.spec = WizardSpec{Bind: "0.0.0.0"}
		return m, loadKubeconfigsCmd(m.socket)

	case KubeChosenMsg:
		m.spec.KubeconfigPath = msg.Path
		m.spec.Context = msg.Context
		m.step = stepNamespace
		m.ns = newNsStep(nil, "")
		m.status = "loading namespaces…"
		return m, loadNamespacesCmd(m.socket, msg.Path, msg.Context)

	case NsChosenMsg:
		m.spec.Namespace = msg.Name
		m.step = stepResource
		m.rt = newRtStep(defaultResourceTypes())
		m.status = fmt.Sprintf("namespace: %s", msg.Name)
		return m, nil

	case ResourceTypeChosenMsg:
		m.spec.ResourceKind = msg.Kind
		m.step = stepObject
		m.obj = renderResourcesFromKind(msg.Kind, nil)
		m.status = "loading resources…"
		return m, loadResourcesCmd(m.socket, m.spec.KubeconfigPath, m.spec.Context, m.spec.Namespace, msg.Kind)

	case ObjectChosenMsg:
		m.spec.ObjectName = msg.Name
		m.spec.PodName = msg.PodName
		// For high-level kinds (Service/Deployment/STS/RS) this also
		// resolves a backing pod; the user-visible message reflects that.
		if m.spec.ResourceKind == "Pod" {
			m.status = "loading ports…"
		} else {
			m.status = "checking backing pod…"
		}
		return m, loadPortsCmd(m.socket,
			m.spec.KubeconfigPath, m.spec.Context,
			m.spec.Namespace, m.spec.ResourceKind, msg.Name)

	case PortMapReadyMsg:
		// Arm the in-flight flag so the next Enter at step ⑤ (and any
		// further ones) is swallowed until the daemon reports back via
		// ForwardStartedMsg. The spinner is only rendered while this flag
		// is set, so the visibility of "loading" is exactly the window
		// where duplicate forwards would otherwise be enqueueable.
		//
		// msg.PortPairs carries the (local, remote) pairs from step ⑤
		// verbatim — the remotes have to reach the daemon unchanged,
		// otherwise the recorded forward will be (local, local) instead
		// of (local, container_port). See the bug where forwarding
		// service port 8380 to local 27017 produced `27017:27017`.
		m.starting = true
		m.status = "starting forward…"
		return m, tea.Batch(
			startForwardCmd(m.socket,
				m.spec.KubeconfigPath, m.spec.Context,
				m.spec.Namespace, m.spec.ResourceKind,
				m.spec.ObjectName, m.spec.PodName,
				m.spec.Bind, msg.PortPairs),
			m.spinner.Tick,
		)
	}

	// Step-specific updates.
	var cmd tea.Cmd
	switch m.step {
	case stepKubeconfig:
		m.kube, cmd = m.kube.Update(msg)
	case stepNamespace:
		m.ns, cmd = m.ns.Update(msg)
	case stepResource:
		m.rt, cmd = m.rt.Update(msg)
	case stepObject:
		m.obj, cmd = m.obj.Update(msg)
	case stepPort:
		m.port, cmd = m.port.Update(msg)
	case stepActive:
		m.active, cmd = m.active.Update(msg)
	}
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// View renders the UI.
func (m Model) View() string {
	if m.step == stepQuitting {
		return ""
	}
	if m.width == 0 {
		return "initializing..."
	}

	header := m.renderHeader()
	body := m.renderBody()
	footer := m.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) renderHeader() string {
	bc := []string{"kpf"}
	for s := stepKubeconfig; s < stepActive; s++ {
		name := s.label()
		if s == m.step {
			name = BreadcrumbActive.Render(name)
		} else {
			name = Breadcrumb.Render(name)
		}
		bc = append(bc, BreadcrumbSep.Render("›"), name)
	}
	bc = append(bc, BreadcrumbSep.Render("·"), "(a) Active")
	return HeaderStyle.Render(strings.Join(bc, " "))
}

func (m Model) renderBody() string {
	var s string
	switch m.step {
	case stepKubeconfig:
		s = m.kube.View()
	case stepNamespace:
		s = m.ns.View()
	case stepResource:
		s = m.rt.View()
	case stepObject:
		s = m.obj.View()
	case stepPort:
		s = m.port.View()
	case stepActive:
		s = m.active.View()
	default:
		s = ""
	}
	if m.status != "" && m.step != stepPort {
		s = s + "\n\n" + m.status
	}
	// While a forward is dialing, overlay a spinner on the body so the
	// user gets feedback (and is visually nudged not to press Enter again).
	// This is rendered on top of whichever step is current — most often
	// stepPort, since that's where Enter was pressed.
	if m.starting {
		s = s + "\n\n" + m.spinner.View() + "  " + StatusWarn.Render("starting forward…")
	}
	// Pin the footer to the bottom of the terminal: header(1) + body +
	// footer(1) must equal the terminal height, so give body the rest.
	bodyHeight := m.height - 2
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	return Body.Width(m.width - 2).Height(bodyHeight).Render(s)
}

func (m Model) renderFooter() string {
	keys := []struct {
		Key, Desc string
	}{
		{"↑/↓", "select"},
		{"enter", "next"},
		{"esc", "back"},
		{"/", "filter"},
		{"a", "active"},
		{"d", "stop"},
		{"q", "quit"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, FooterKey.Render(k.Key)+" "+k.Desc)
	}
	help := strings.Join(parts, FooterSep.Render(" · "))
	if m.err != nil {
		help += "  " + StatusErr.Render("err: "+m.err.Error())
	}
	return Footer.Width(m.width - 2).Render(help)
}

// convertKubeEntries maps the bridge type to the kubeconfig.Entry shape used
// by the existing kube step.
func convertKubeEntries(in []kubeEntry) []kubeconfig.Entry {
	out := make([]kubeconfig.Entry, 0, len(in))
	for _, e := range in {
		out = append(out, kubeconfig.Entry{
			Path:           e.Path,
			Basename:       e.Basename,
			CurrentContext: e.CurrentContext,
			Clusters:       e.Clusters,
			Contexts:       e.Contexts,
			Users:          e.Users,
			Size:           e.Size,
		})
	}
	return out
}

// defaultResourceTypes returns the kind picker contents.
func defaultResourceTypes() []mockResourceType {
	return []mockResourceType{
		{Kind: "Pod"},
		{Kind: "Service"},
		{Kind: "Deployment"},
		{Kind: "StatefulSet"},
		{Kind: "ReplicaSet"},
	}
}

// isAnyListFiltering reports whether the current step's list is currently
// accepting filter-input characters. When true, global shortcuts (a, esc)
// must yield to the list so that keystrokes land in the filter input.
func (m Model) isAnyListFiltering() bool {
	switch m.step {
	case stepKubeconfig:
		return m.kube.filtering()
	case stepNamespace:
		return m.ns.list.FilterState() == list.Filtering
	case stepResource:
		return m.rt.list.FilterState() == list.Filtering
	case stepObject:
		return m.obj.list.FilterState() == list.Filtering
	}
	// stepActive uses bubbles/table which has no built-in filtering, so
	// there's nothing to yield to here.
	return false
}