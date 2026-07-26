package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kpf/internal/forward"
)

// portStep is step ⑤: pick which remote ports to forward (multi-select) and
// edit the local port for each one. On submit it emits PortMapReadyMsg with
// the full PortPair list (so the remotes reach the daemon unchanged) —
// previously the message only carried the local ports and startForwardCmd
// re-derived the remotes as the locals, producing forward records like
// `27017:27017` when the user actually wanted `27017:8380`.
type portStep struct {
	pairs    []PortPair
	list     list.Model
	inputs   []textinput.Model
	focused  int // index of the input currently focused (-1 = none)
	width    int
	bind     string // local bind address — used for pre-flight port conflict checks

	// claimed is the daemon's authoritative list of ports registered in
	// its manager's spec.Ports. Refreshed once when entering step ⑤ via
	// an async IPC call (loadClaimedPortsCmd). Conflicts that the local
	// OS-level tryListen misses — because the daemon's SPDY listener
	// hasn't bound the port yet — are caught here instead.
	claimed map[int]bool

	// conflicts[i] is true when pairs[i].Local can't be bound (either
	// already taken on this host by some other process, claimed by
	// another forward in the daemon, or out of range). Refreshed on
	// every input edit and once on entry. Pre-flight blocks the submit
	// path so the user sees an error immediately instead of waiting for
	// a daemon roundtrip.
	conflicts []bool
	// submitErr is set when the user pressed Enter with a conflict; shown
	// in the right pane and cleared as soon as the offending port is fixed.
	submitErr string
}

func newPortStep(remotePorts []int, bind string) portStep {
	pairs := make([]PortPair, 0, len(remotePorts))
	for _, p := range remotePorts {
		pairs = append(pairs, PortPair{Remote: p, Local: p})
	}
	items := make([]list.Item, 0, len(pairs))
	for _, p := range pairs {
		items = append(items, portPairItem{p: p})
	}
	l := list.New(items, NewDefaultDelegate(), 0, 0)
	l.Title = "Configure port mapping"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Styles.Title = ListTitle
	l.SetSize(40, 12)

	inputs := make([]textinput.Model, len(pairs))
	for i, p := range pairs {
		ti := textinput.New()
		ti.Placeholder = fmt.Sprintf("%d", p.Local)
		ti.SetValue(fmt.Sprintf("%d", p.Local))
		ti.CharLimit = 5
		ti.Width = 8
		inputs[i] = ti
	}
	ps := portStep{
		pairs:     pairs,
		list:      l,
		inputs:    inputs,
		focused:   -1,
		bind:      bind,
		claimed:   map[int]bool{},
		conflicts: make([]bool, len(pairs)),
	}
	// Probe the local OS once on entry so the UI starts in a known state.
	// The authoritative daemon-side claim list arrives asynchronously via
	// ClaimedPortsLoadedMsg; refreshConflicts is called again then to
	// catch ports the OS-level probe can't see yet (the daemon hasn't
	// bound them at the kernel level but they're claimed in m.forwards).
	ps.refreshConflicts()
	return ps
}

type portPairItem struct {
	p        PortPair
	conflict bool
}

func (i portPairItem) FilterValue() string {
	return fmt.Sprintf("%d %d", i.p.Local, i.p.Remote)
}
func (i portPairItem) Title() string {
	if i.conflict {
		return fmt.Sprintf("remote %d → local %d  ✗ in use", i.p.Remote, i.p.Local)
	}
	return fmt.Sprintf("remote %d → local %d", i.p.Remote, i.p.Local)
}
func (i portPairItem) Description() string {
	return "press enter to edit local"
}

func (p portStep) Init() tea.Cmd {
	if len(p.inputs) > 0 {
		p.inputs[0].Focus()
		p.focused = 0
		return textinput.Blink
	}
	return nil
}

// refreshConflicts probes each local port and updates p.conflicts[i].
// A port is conflicted if ANY of these are true:
//   1. The local OS can't bind it (some other process holds it).
//   2. The daemon has another registered forward that claims it.
// The OS probe is fast but racy against the daemon's SPDY dial timing;
// the daemon-side claim list is authoritative and catches duplicates
// the OS probe misses (e.g. two forwards submitted during the same
// dial phase, neither of which has bound anything yet).
func (p *portStep) refreshConflicts() {
	for i := range p.pairs {
		var conflicted bool
		if err := forward.IsLocalPortFree(p.bind, p.pairs[i].Local); err != nil {
			conflicted = true
		}
		if p.claimed[p.pairs[i].Local] {
			conflicted = true
		}
		p.conflicts[i] = conflicted
	}
	p.rebuildListItems()
}

func (p *portStep) rebuildListItems() {
	items := make([]list.Item, 0, len(p.pairs))
	for i, pp := range p.pairs {
		items = append(items, portPairItem{p: pp, conflict: p.conflicts[i]})
	}
	p.list.SetItems(items)
}

// anyConflict reports whether any pair has a local-port conflict. The
// caller is expected to refuse submit and surface the message when this
// is true.
func (p portStep) anyConflict() bool {
	for _, c := range p.conflicts {
		if c {
			return true
		}
	}
	return false
}

// firstConflict returns the index of the first conflicted pair, or -1.
// Used to label the submit error with the actual bad port.
func (p portStep) firstConflict() int {
	for i, c := range p.conflicts {
		if c {
			return i
		}
	}
	return -1
}

func (p portStep) Update(msg tea.Msg) (portStep, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case ClaimedPortsLoadedMsg:
		// Daemon returned the authoritative list of ports its manager
		// currently has registered. Re-run the conflict check so any
		// port the local OS probe missed (because the daemon hasn't
		// bound it yet during SPDY dial) gets marked.
		p.claimed = map[int]bool{}
		for _, port := range msg.Ports {
			p.claimed[port] = true
		}
		p.refreshConflicts()
		return p, nil
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.list.SetSize(msg.Width/2-4, msg.Height-12)
	case tea.KeyMsg:
		// Tab cycles focus between inputs.
		switch msg.String() {
		case "tab":
			if p.focused >= 0 {
				p.inputs[p.focused].Blur()
			}
			p.focused = (p.focused + 1) % len(p.inputs)
			p.inputs[p.focused].Focus()
			cmds = append(cmds, textinput.Blink)
			return p, tea.Batch(cmds...)
		case "shift+tab":
			if p.focused >= 0 {
				p.inputs[p.focused].Blur()
			}
			p.focused--
			if p.focused < 0 {
				p.focused = len(p.inputs) - 1
			}
			p.inputs[p.focused].Focus()
			cmds = append(cmds, textinput.Blink)
			return p, tea.Batch(cmds...)
		case "enter":
			// Pre-flight: every local port must be bindable. If any are
			// not, refuse to submit and tell the user which port. This
			// mirrors the daemon's authoritative check (which still
			// runs), but turns a multi-second IPC roundtrip into an
			// instant in-process error — which is the whole point of
			// having a wizard TUI.
			p.refreshConflicts()
			if idx := p.firstConflict(); idx >= 0 {
				pp := p.pairs[idx]
				p.submitErr = fmt.Sprintf("local port %d is already in use — pick a different one", pp.Local)
				return p, nil
			}
			p.submitErr = ""
			// Submit: send the full pair list so the remote ports reach
			// the daemon unchanged. (Previously we sent only the local
			// ports and startForwardCmd re-derived the remotes as the
			// locals, producing wrong records like 27017:27017 when the
			// user actually wanted to forward service port 8380.)
			pairsCopy := make([]PortPair, len(p.pairs))
			copy(pairsCopy, p.pairs)
			return p, func() tea.Msg {
				return PortMapReadyMsg{PortPairs: pairsCopy}
			}
		}
	}
	if p.focused >= 0 && p.focused < len(p.inputs) {
		var cmd tea.Cmd
		p.inputs[p.focused], cmd = p.inputs[p.focused].Update(msg)
		cmds = append(cmds, cmd)
		// Sync list display with the current value and re-probe the
		// local port. The probe fires every keystroke so the conflict
		// marker is live (tryListen is cheap; bind then close).
		var v int
		if _, err := fmt.Sscanf(p.inputs[p.focused].Value(), "%d", &v); err == nil && v > 0 {
			p.pairs[p.focused].Local = v
			p.refreshConflicts()
		}
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	cmds = append(cmds, cmd)
	return p, tea.Batch(cmds...)
}

func (p portStep) View() string {
	if len(p.pairs) == 0 {
		return Body.Render(StatusWarn.Render(
			"No remote ports available for this resource.\n  " +
				FooterHelp("esc") + " to go back."))
	}

	left := p.list.View()
	right := p.renderEditors()

	sep := lipgloss.NewStyle().
		Foreground(muted).
		Render(strings.Repeat("│\n", strings.Count(left, "\n")+1))

	gap := "   "
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		Body.Render(left),
		gap+sep+gap,
		Body.Render(right),
	)
}

func (p portStep) renderEditors() string {
	var b strings.Builder
	b.WriteString(ListTitle.Render("Local ports (edit)") + "\n")
	for i, pp := range p.pairs {
		label := fmt.Sprintf("remote %d →", pp.Remote)
		if i == p.focused {
			label = StatusOK.Render("▶ ") + label
		} else {
			label = "  " + label
		}
		b.WriteString(FormLabel.Render(label) + " ")
		b.WriteString(p.inputs[i].View())
		if p.conflicts[i] {
			b.WriteString("  " + StatusErr.Render("✗ in use"))
		}
		b.WriteString("\n")
	}
	if p.submitErr != "" {
		b.WriteString("\n" + StatusErr.Render("✗ " + p.submitErr))
	}
	b.WriteString("\n")
	b.WriteString(PortRowSelected.Render("press enter to start (mock)"))
	b.WriteString("\n")
	b.WriteString(ListHelp.Render("tab/shift+tab cycle · enter submit"))
	return b.String()
}

func FooterHelp(keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k)
	}
	return strings.Join(parts, " ")
}