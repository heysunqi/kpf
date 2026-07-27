package forward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kpf/internal/k8s"
)

// StartOptions configures a new forward.
type StartOptions struct {
	Spec         Spec
	WaitReady    time.Duration
	EventSink    func(Event)
}

// Manager owns the lifetime of every active Forward.
type Manager struct {
	log *slog.Logger

	mu       sync.RWMutex
	forwards map[string]*Forward
	nextSeq  int

	// history holds the last-known spec for every ID the manager has ever
	// started. Entries outlive Stop so that a forward.restart IPC can look
	// up the original spec and re-issue it with the same ID. Cleared on
	// Shutdown — survives individual Stop calls but not daemon restarts.
	history map[string]Spec

	subsMu     sync.Mutex
	subs       []chan Event
}

// NewManager creates an empty manager.
func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		log:      log,
		forwards: make(map[string]*Forward),
		history:  make(map[string]Spec),
	}
}

// Start spawns a new forward with the given spec. It returns the forward's
// Info once it has reached either Ready or Error state (capped by WaitReady).
func (m *Manager) Start(opts StartOptions) (Info, error) {
	spec := opts.Spec
	if err := validateSpec(spec); err != nil {
		return Info{}, err
	}

	if err := m.checkLocalPortsFree(spec); err != nil {
		return Info{}, err
	}

	if spec.Kind != "Pod" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cs, err := m.clientsetFor(ctx, spec)
		cancel()
		if err != nil {
			return Info{}, fmt.Errorf("connect: %w", err)
		}
		pod, err := k8s.ResolveOnePod(context.Background(), cs, spec.Kind, spec.Namespace, spec.Object)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return Info{}, fmt.Errorf("%s %q not found", strings.ToLower(spec.Kind), spec.Object)
			}
			var nbp *k8s.NoBackingPodError
			if errors.As(err, &nbp) {
				// Log the full selector for ops visibility; the user-facing
				// Error() string stays short.
				m.log.Warn("no backing pod",
					"kind", spec.Kind,
					"name", spec.Object,
					"namespace", spec.Namespace,
					"selector", nbp.SelectorString())
			}
			return Info{}, err
		}
		spec.PodName = pod.Name
	}

	m.mu.Lock()
	var id string
	if spec.ID != "" {
		// Honor caller-supplied id (used by daemon restart). Reject if it
		// collides with an in-memory forward.
		if _, exists := m.forwards[spec.ID]; exists {
			m.mu.Unlock()
			return Info{}, fmt.Errorf("forward id %q already in use", spec.ID)
		}
		id = spec.ID
	} else {
		m.nextSeq++
		id = fmt.Sprintf("fwd_%04d", m.nextSeq)
		spec.ID = id
	}
	m.mu.Unlock()

	// Remember the spec so a subsequent forward.restart can re-issue it
	// with the same ID. Spec.ID is set above so the key always matches the
	// forward's actual ID.
	m.mu.Lock()
	m.history[id] = spec
	m.mu.Unlock()

	f := newForward(id, spec, m.log)

	// Subscribe to forward events.
	sub := f.Subscribe()
	go m.relayEvents(sub)

	// Bridge manager-level subscribers too.
	if opts.EventSink != nil {
		f.Subscribe() // drain — events go through `subs` channel
	}

	// Register.
	m.mu.Lock()
	m.forwards[id] = f
	m.mu.Unlock()

	// Start the forward goroutine.
	go f.Run()

	// Wait for terminal transition (Ready or Error).
	wait := opts.WaitReady
	if wait == 0 {
		wait = 10 * time.Second
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		s := f.Info().Status
		if s == StatusReady || s == StatusError || s == StatusStopped {
			info := f.Info()
			if s == StatusError {
				return info, errors.New(info.StatusMessage)
			}
			return info, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return f.Info(), nil
}

// List returns a snapshot of all forwards.
func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.forwards))
	for _, f := range m.forwards {
		out = append(out, f.Info())
	}
	return out
}

// Get returns the Info for a single forward.
func (m *Manager) Get(id string) (Info, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.forwards[id]
	if !ok {
		return Info{}, false
	}
	return f.Info(), true
}

// LivePorts returns the union of local TCP ports currently bound by every
// forward's portforwarder. Used by the daemon's listener-parity audit so it
// can detect leaks (a forward stopped but a listener leaked) without
// shelling out to lsof.
//
// Ports are read via portforward.PortForwarder.GetPorts(), which is only
// valid after the forward's listeners have become Ready. Returns nil (not
// an empty slice) if no forward has a portforwarder yet — distinguishing
// "I haven't checked yet" from "I checked, none bound".
func (m *Manager) LivePorts() []int {
	m.mu.RLock()
	forwards := make([]*Forward, 0, len(m.forwards))
	for _, f := range m.forwards {
		forwards = append(forwards, f)
	}
	m.mu.RUnlock()

	seen := map[int]bool{}
	for _, f := range forwards {
		f.mu.Lock()
		pf := f.pf
		f.mu.Unlock()
		if pf == nil {
			continue
		}
		ports, err := pf.GetPorts()
		if err != nil {
			continue
		}
		for _, p := range ports {
			seen[int(p.Local)] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// ClaimedPorts returns the union of local ports CLAIMED by every
// registered forward's spec.Ports, regardless of whether the forward has
// actually bound them at the kernel level yet. This is the authoritative
// pre-flight check the TUI uses — unlike tryListen, it doesn't race
// against the daemon's SPDY dial timing: once a forward is registered,
// its ports are claimed until it's stopped, regardless of state.
func (m *Manager) ClaimedPorts() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[int]bool{}
	for _, f := range m.forwards {
		f.mu.Lock()
		ports := append([]PortPair(nil), f.spec.Ports...)
		f.mu.Unlock()
		for _, p := range ports {
			seen[p.Local] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// Stop terminates a forward by id. The spec is retained in history so that
// a subsequent restart can re-issue it with the same ID; RemoveSpec clears it.
func (m *Manager) Stop(id string) error {
	m.mu.RLock()
	f, ok := m.forwards[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("forward %q not found", id)
	}
	f.Stop()
	m.mu.Lock()
	delete(m.forwards, id)
	m.mu.Unlock()
	return nil
}

// StopAll terminates every forward and waits for them all to settle.
func (m *Manager) StopAll() {
	m.mu.RLock()
	all := make([]*Forward, 0, len(m.forwards))
	for _, f := range m.forwards {
		all = append(all, f)
	}
	m.mu.RUnlock()
	for _, f := range all {
		f.Stop()
	}
	m.mu.Lock()
	m.forwards = make(map[string]*Forward)
	m.mu.Unlock()
}

// Subscribe returns a channel of events for every forward. The channel is
// closed when Manager.Shutdown is called.
func (m *Manager) Subscribe() <-chan Event {
	ch := make(chan Event, 64)
	m.subsMu.Lock()
	m.subs = append(m.subs, ch)
	m.subsMu.Unlock()
	return ch
}

// Shutdown terminates every forward and closes all subscriber channels.
func (m *Manager) Shutdown() {
	m.StopAll()
	m.subsMu.Lock()
	for _, ch := range m.subs {
		close(ch)
	}
	m.subs = nil
	m.subsMu.Unlock()
	m.mu.Lock()
	m.history = make(map[string]Spec)
	m.mu.Unlock()
}

// Spec returns the last-known spec for an ID (i.e. the most recent spec the
// manager accepted via Start). Returns ok=false if the ID has never been
// started in this manager's lifetime. Specs outlive Stop — only Shutdown
// and RemoveSpec clear them — so a restart is possible until then.
func (m *Manager) Spec(id string) (Spec, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.history[id]
	return s, ok
}

// RemoveSpec clears the retained spec for an ID. Call this when the spec is
// no longer valid for a restart (e.g. the kubeconfig path it points to has
// been deleted) or as part of explicit cleanup. Stop does NOT call this —
// restartable history is the point.
func (m *Manager) RemoveSpec(id string) {
	m.mu.Lock()
	delete(m.history, id)
	m.mu.Unlock()
}

// relayEvents fans a single forward's events out to manager subscribers.
func (m *Manager) relayEvents(src <-chan Event) {
	for e := range src {
		m.subsMu.Lock()
		for _, ch := range m.subs {
			select {
			case ch <- e:
			default:
			}
		}
		m.subsMu.Unlock()
	}
}

// validateSpec returns an error for any obvious spec problem.
func validateSpec(s Spec) error {
	if s.KubeconfigPath == "" {
		return errors.New("kubeconfig path is required")
	}
	if s.Namespace == "" {
		return errors.New("namespace is required")
	}
	if s.Kind == "" {
		return errors.New("kind is required")
	}
	if s.Object == "" {
		return errors.New("object name is required")
	}
	if s.Bind == "" {
		s.Bind = "127.0.0.1"
	}
	if len(s.Ports) == 0 {
		return errors.New("at least one port pair is required")
	}
	for _, p := range s.Ports {
		if p.Local <= 0 || p.Local > 65535 {
			return fmt.Errorf("invalid local port %d", p.Local)
		}
		if p.Remote <= 0 || p.Remote > 65535 {
			return fmt.Errorf("invalid remote port %d", p.Remote)
		}
	}
	return nil
}

// checkLocalPortsFree returns ErrPortInUse if any local port is already
// claimed (either by a registered forward in this manager, or by some
// other process holding the kernel-level bind).
//
// The manager-level check is what catches the race that the OS-level
// tryListen misses: when two forwards are submitted back-to-back while
// the first is still in its SPDY dial phase, neither has bound anything
// yet — so tryListen("0.0.0.0", 8959) succeeds for both, and both get
// registered. The first reaches Ready and binds 8959; the second's
// ForwardPorts then fails internally with EADDRINUSE — and would hang
// in "starting" forever. Comparing against the registered forwards'
// claimed ports (via spec.Ports, not their live kernel binds) closes
// the window: the second submission sees the first is already in
// m.forwards and rejects with port_in_use before the second is
// registered.
func (m *Manager) checkLocalPortsFree(spec Spec) error {
	claimed := map[int]string{} // local port → owning forward id
	m.mu.RLock()
	for id, f := range m.forwards {
		f.mu.Lock()
		ports := append([]PortPair(nil), f.spec.Ports...)
		f.mu.Unlock()
		for _, p := range ports {
			claimed[p.Local] = id
		}
	}
	m.mu.RUnlock()

	for _, p := range spec.Ports {
		if owner, dup := claimed[p.Local]; dup {
			return &PortInUseError{
				Local: p.Local,
				Bind:  spec.Bind,
				Cause: fmt.Errorf("claimed by forward %q", owner),
			}
		}
		ln, err := tryListen(spec.Bind, p.Local)
		if err != nil {
			return &PortInUseError{Local: p.Local, Bind: spec.Bind, Cause: err}
		}
		_ = ln.Close()
	}
	return nil
}

func (m *Manager) clientsetFor(ctx context.Context, spec Spec) (kubernetes.Interface, error) {
	// Indirection so the k8s import doesn't pull into cycles when this
	// package is used standalone.
	return k8s.NewForSpec(ctx, spec.KubeconfigPath, spec.Context)
}