package daemon

import (
	"fmt"
	"sort"
	"time"

	"kpf/internal/forward"
)

// computeParity is the pure (testable) symmetric-difference helper that
// auditListeners builds up to. Given two port sets, it returns:
//   - orphan: ports in listens but not in state (kpf is holding a listener
//     that no forward record explains — likely a leak)
//   - missing: ports in state but not in listens (forward is registered but
//     its TCP listener never opened — likely a restore dial failure)
//
// Both result slices are sorted ascending. Empty inputs yield nil.
func computeParity(listens, state map[int]bool) (orphan, missing []int) {
	for p := range listens {
		if !state[p] {
			orphan = append(orphan, p)
		}
	}
	for p := range state {
		if !listens[p] {
			missing = append(missing, p)
		}
	}
	sort.Ints(orphan)
	sort.Ints(missing)
	return orphan, missing
}

// auditListeners enumerates the local TCP ports currently bound by kpf's
// own portforwarders and cross-references them with the ports recorded in
// state.json.
//
// Returns:
//   - orphan: ports that kpf believes are bound (per the in-process
//     tracker) but have no matching forward in state.json. Likely a leak:
//     a forward was stopped but its listener wasn't released.
//   - missing: forward ports recorded in state.json but for which kpf
//     has no live listener. Usually means restore dial failed (e.g.
//     backing pod is gone, API unreachable).
//
// Implementation: uses forward.Manager.LivePorts(), which calls
// portforward.PortForwarder.GetPorts() on each forward. No external
// commands (no lsof, no /proc walk) — works on every Unix where Go runs
// and on Windows if we ever cross-compile.
func (h *Handler) auditListeners() (orphan, missing []int) {
	listens := map[int]bool{}
	for _, p := range h.manager.LivePorts() {
		listens[p] = true
	}

	statePorts := map[int]bool{}
	// Read state.json directly so the audit also catches forwards that
	// failed to restore (e.g. the k8s API was unreachable). Manager.List()
	// would only contain forwards that successfully started.
	if st, err := h.state.Load(); err == nil {
		for _, f := range st.Forwards {
			if f.Status == string(forward.StatusStopped) || f.Status == string(forward.StatusStale) {
				continue
			}
			for _, p := range f.Ports {
				statePorts[p.Local] = true
			}
		}
	} else {
		h.log.Debug("auditListeners: state load failed", "err", err)
	}

	return computeParity(listens, statePorts)
}

// reportListenerParity runs auditListeners and emits a structured log entry.
// Called at startup (after restore completes) and on a periodic timer.
func (h *Handler) reportListenerParity(reason string) {
	orphan, missing := h.auditListeners()
	if len(orphan) == 0 && len(missing) == 0 {
		h.log.Info("listener parity ok", "reason", reason)
		return
	}
	if len(orphan) > 0 {
		h.log.Warn("listener parity: orphan LISTEN ports",
			"reason", reason,
			"ports", fmt.Sprintf("%v", orphan),
			"hint", "these listeners have no matching forward — previous forward may have leaked a socket")
	}
	if len(missing) > 0 {
		h.log.Warn("listener parity: forward without listener",
			"reason", reason,
			"ports", fmt.Sprintf("%v", missing),
			"hint", "these forwards are registered but their TCP listener never opened (pod gone? dial failed?)")
	}
}

// startParityAudit spawns a goroutine that periodically checks listener
// parity. The first check runs after a 5s grace period so the restored
// forwards have time to dial and open their listeners.
func (h *Handler) startParityAudit() {
	h.log.Info("parity audit scheduled", "first_check_in", "5s", "interval", "60s")
	go func() {
		time.Sleep(5 * time.Second)
		h.reportListenerParity("startup")

		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for range t.C {
			h.reportListenerParity("periodic")
		}
	}()
}