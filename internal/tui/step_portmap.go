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
//
// Multi-select model: each pair has an `excluded[i]` flag. By default ALL
// pairs are excluded — the user explicitly opts in by pressing space on the
// rows they want forwarded. This gives a clear "whitelist" mental model:
// if you see a row in the active view, you meant to forward it. The submit
// path only collects included pairs and blocks with a friendly error if no
// pair is selected (the daemon's validateSpec rejects empty specs anyway).
// Pre-flight (refreshConflicts) skips excluded rows, so an excluded port
// can never block submit even if its default local port is unavailable.
type portStep struct {
	pairs    []PortPair
	list     list.Model
	inputs   []textinput.Model
	focused  int // index of the input currently focused (-1 = none)
	width    int
	bind     string // local bind address — used for pre-flight port conflict checks

	// excluded[i] is true when the user has toggled pairs[i] OFF (default).
	// A pair is INCLUDED in the submit payload iff excluded[i] == false.
	// This flag is mirrored onto portPairItem.selected so the bubbles/list
	// delegate can render a checkbox glyph ([x] vs [ ]) in Title().
	excluded []bool

	// claimed is the daemon's authoritative list of ports registered in
	// its manager's spec.Ports. Refreshed once when entering step ⑤ via
	// an async IPC call (loadClaimedPortsCmd). Conflicts that the local
	// OS-level tryListen misses — because the daemon's SPDY listener
	// hasn't bound the port yet — are caught here instead.
	claimed map[int]bool

	// conflicts[i] is true when pairs[i].Local can't be bound (either
	// already taken on this host by some other process, claimed by
	// another forward in the daemon, or out of range). Refreshed on
	// every input edit and once on entry. Excluded rows are never
	// probed, so conflicts[i] stays false for them. Pre-flight blocks
	// the submit path so the user sees an error immediately instead of
	// waiting for a daemon roundtrip.
	conflicts []bool
	// submitErr is set when the user pressed Enter with a conflict, or
	// when Enter was pressed with zero included pairs. Cleared as soon
	// as the offending input is fixed (next refreshConflicts).
	submitErr string
}

func newPortStep(remotePorts []int, bind string) portStep {
	pairs := make([]PortPair, 0, len(remotePorts))
	for _, p := range remotePorts {
		pairs = append(pairs, PortPair{Remote: p, Local: p})
	}
	// Default every pair to EXCLUDED — the user explicitly opts in by
	// pressing space on the rows they want forwarded. This is the
	// whitelist UX: an excluded port can never block submit, and the
	// user can never accidentally forward a port they didn't intend to.
	items := make([]list.Item, 0, len(pairs))
	excluded := make([]bool, len(pairs))
	for i, p := range pairs {
		items = append(items, portPairItem{p: p, selected: false})
		excluded[i] = true
	}
	// Title-only delegate (no description line) — the "space to
	// include/exclude" hint lives in the right pane help line and the
	// footer, so we don't need to repeat it under each row. Halving
	// the per-row height lets all 4 (or more) Pod ports fit on screen
	// without scrolling, which matters when the user is picking a
	// subset to forward.
	l := list.New(items, NewTitleOnlyDelegate(), 0, 0)
	l.Title = "Configure port mapping"
	l.SetShowStatusBar(false)
	l.SetShowPagination(false) // 4-8 ports always fit on one page now
	l.SetFilteringEnabled(false)
	l.Styles.Title = ListTitle
	l.SetSize(40, 20)

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
		excluded:  excluded,
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
	selected bool // true = included in submit payload (checkbox ON)
}

func (i portPairItem) FilterValue() string {
	return fmt.Sprintf("%d %d", i.p.Local, i.p.Remote)
}
func (i portPairItem) Title() string {
	// Checkbox glyph makes the multi-select state immediately readable.
	// The "▶" cursor / focused highlight on the right pane still drives
	// which input is being edited; the checkbox on the left is the
	// "is this port in the submit set" signal.
	box := "[x]"
	if !i.selected {
		box = "[ ]"
	}
	// Only included rows can show a conflict marker — excluded rows
	// are skipped by refreshConflicts entirely, so i.conflict is
	// always false for them anyway, but we double-check i.selected
	// defensively in case a future caller forgets to clear conflict.
	if i.conflict && i.selected {
		return fmt.Sprintf("%s remote %d → local %d  ✗ in use", box, i.p.Remote, i.p.Local)
	}
	return fmt.Sprintf("%s remote %d → local %d", box, i.p.Remote, i.p.Local)
}
func (i portPairItem) Description() string {
	if i.selected {
		return "space to exclude"
	}
	return "space to include"
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
//
// Excluded rows are never probed — they're not in the submit payload,
// so a conflict marker on them would be noise. This is the core of
// the whitelist UX: the user can pick a single port out of a Pod that
// exposes 4 privileged ports, and the other three's "in use" status
// will never block submit.
func (p *portStep) refreshConflicts() {
	for i := range p.pairs {
		if p.excluded[i] {
			p.conflicts[i] = false
			continue
		}
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
		items = append(items, portPairItem{
			p:        pp,
			conflict: p.conflicts[i],
			selected: !p.excluded[i],
		})
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
		case " ":
			// Toggle include/exclude on the cursor's row. We handle this
			// BEFORE any other branch so space doesn't fall through to
			// the focused textinput (which would otherwise insert a
			// literal space character). refreshConflicts is intentionally
			// NOT called here — excluded rows aren't probed and included
			// rows haven't changed their local port, so the conflict
			// state is already up to date.
			cursor := p.list.Cursor()
			if cursor < 0 || cursor >= len(p.pairs) {
				return p, nil
			}
			p.excluded[cursor] = !p.excluded[cursor]
			p.refreshConflicts()
			return p, nil
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
			// Pre-flight: every INCLUDED local port must be bindable. If
			// any are not, refuse to submit and tell the user which port.
			// This mirrors the daemon's authoritative check (which still
			// runs), but turns a multi-second IPC roundtrip into an
			// instant in-process error — which is the whole point of
			// having a wizard TUI. Excluded rows are skipped in
			// refreshConflicts, so they never trigger this branch.
			p.refreshConflicts()
			if idx := p.firstConflict(); idx >= 0 {
				pp := p.pairs[idx]
				p.submitErr = fmt.Sprintf("local port %d is already in use — pick a different one", pp.Local)
				return p, nil
			}
			// Collect only included pairs for the submit payload.
			// (Previously all pairs were unconditionally sent; this was
			// the root cause of the "want to forward one port out of a
			// multi-port Pod, but pre-flight blocks on the other ports"
			// bug — see commit message for details.)
			pairsCopy := make([]PortPair, 0, len(p.pairs))
			for i, pp := range p.pairs {
				if !p.excluded[i] {
					pairsCopy = append(pairsCopy, pp)
				}
			}
			if len(pairsCopy) == 0 {
				// Default state is all-excluded, so this is the most
				// common error path. The daemon's validateSpec rejects
				// an empty spec with "at least one port pair is required"
				// — we mirror that as a friendlier TUI-side message so
				// the user knows to press space on at least one row.
				p.submitErr = "no ports selected — press space to include at least one"
				return p, nil
			}
			p.submitErr = ""
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
		// Right-pane state mirrors the left-pane checkbox: excluded
		// rows are muted (visually de-emphasized, no cursor marker)
		// and the focused row still drives which textinput is being
		// edited, but only if it's also included — editing a port you
		// don't plan to forward is pointless and the placeholder
		// would just confuse the submit summary.
		var label string
		switch {
		case p.excluded[i]:
			label = "  " + MutedText.Render(fmt.Sprintf("remote %d →", pp.Remote))
		case i == p.focused:
			label = StatusOK.Render("▶ ") + fmt.Sprintf("remote %d →", pp.Remote)
		default:
			label = "  " + fmt.Sprintf("remote %d →", pp.Remote)
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
	// Show selected count so the user knows how many rows will actually
	// be forwarded. "(N selected)" makes the whitespace model visible:
	// the user has to opt in via space, and the count tells them
	// they've done so for exactly N rows.
	selected := 0
	for _, e := range p.excluded {
		if !e {
			selected++
		}
	}
	b.WriteString(PortRowSelected.Render(fmt.Sprintf(
		"press enter to start (%d selected)", selected)))
	b.WriteString("\n")
	b.WriteString(ListHelp.Render("space toggle · tab/shift+tab cycle · enter submit"))
	return b.String()
}

func FooterHelp(keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k)
	}
	return strings.Join(parts, " ")
}