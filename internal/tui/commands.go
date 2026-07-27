package tui

import (
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kpf/internal/ipc"
)

// ---------------------------------------------------------------------------
// Async data loading commands
// ---------------------------------------------------------------------------

func loadKubeconfigsCmd(socket string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		entries, dirs, err := b.listKubeconfigs()
		return KubeconfigsLoadedMsg{Entries: entries, Dirs: dirs, Err: err}
	}
}

func loadNamespacesCmd(socket, path, ctxName string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		list, err := b.listNamespaces(path, ctxName)
		return NamespacesLoadedMsg{Path: path, List: list, Err: err}
	}
}

func loadResourcesCmd(socket, path, ctxName, ns, kind string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		list, err := b.listResources(path, ctxName, ns, kind)
		return ResourcesLoadedMsg{Kind: kind, List: list, Err: err}
	}
}

func loadPortsCmd(socket, path, ctxName, ns, kind, object string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		ports, pod, err := b.computePorts(path, ctxName, ns, kind, object)
		return PortsLoadedMsg{Kind: kind, Object: object, Ports: ports, Pod: pod, Err: err}
	}
}

// loadClaimedPortsCmd asks the daemon for the local ports it has
// registered in its manager. Used by step ⑤ for authoritative
// pre-flight conflict detection — unlike cross-process tryListen, it
// doesn't race against the daemon's SPDY dial timing, so a port that
// the daemon has claimed but hasn't bound yet still gets flagged.
func loadClaimedPortsCmd(socket string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		ports, err := b.claimedPorts()
		return ClaimedPortsLoadedMsg{Ports: ports, Err: err}
	}
}

// startForwardCmd tells the daemon to start a forward with the given
// (local, remote) port pairs. The pairs must reach the daemon verbatim —
// re-deriving the remotes from the locals here would produce wrong forward
// records (the previous version did `PortPair{Local: lp, Remote: lp}` and
// forwarded service port 8380 as 27017:27017 instead of 27017:8380).
func startForwardCmd(socket, path, ctxName, ns, kind, object, pod, bind string, portPairs []PortPair) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		id, locals, err := b.startForward(path, ctxName, ns, kind, object, pod, bind, portPairs)
		return ForwardStartedMsg{ID: id, LocalPorts: locals, Err: err}
	}
}

func loadForwardsCmd(socket string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		raw, err := b.listForwards()
		if err != nil {
			return ForwardsLoadedMsg{Err: err}
		}
		out := make([]ipcForward, 0, len(raw))
		for _, f := range raw {
			ports := ""
			for i, p := range f.Ports {
				if i > 0 {
					ports += ","
				}
				ports += formatPort(p)
			}
			startedAt := f.StartedAt
			out = append(out, ipcForward{
				ID:         f.ID,
				Kubeconfig: trimPath(f.Kubeconfig),
				Namespace:  f.Namespace,
				Kind:       f.Kind,
				Object:     f.Object,
				PodName:    f.PodName,
				Bind:       f.Bind,
				Ports:      ports,
				Status:     f.Status,
				StartedAt:  startedAt,
			})
		}
		// Manager.List returns forwards in Go map iteration order (random),
		// so the active view would re-shuffle on every refresh. Pin the order
		// by ID ascending — matches what `kpf ls` prints on the CLI side.
		sort.Slice(out, func(i, j int) bool {
			return out[i].ID < out[j].ID
		})
		return ForwardsLoadedMsg{List: out, Err: nil}
	}
}

func formatPort(p ipc.PortMap) string {
	return itoa(p.Local) + ":" + itoa(p.Remote)
}

func trimPath(p string) string {
	if len(p) > 32 {
		return "…" + p[len(p)-31:]
	}
	return p
}

// itoa is a minimal base-10 int-to-string without pulling in strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// tickActiveCmd schedules a periodic refresh of the active-forwards view.
func tickActiveCmd() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
		return TickActiveMsg{}
	})
}

// stopActiveForwardCmd stops the given forward id via IPC. The result is
// reported as StopForwardResultMsg (a distinct type from the user-trigger
// StopForwardMsg) so the app's handler can distinguish "user pressed d" from
// "IPC returned" and not re-fire the IPC on success.
func stopActiveForwardCmd(socket, id string) tea.Cmd {
	return func() tea.Msg {
		b := newBridge(socket)
		defer b.close()
		err := b.stopForward(id)
		return StopForwardResultMsg{ID: id, Err: err}
	}
}