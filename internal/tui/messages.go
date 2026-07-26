package tui

// WizardSpec accumulates the user's choices through the 5-step wizard.
type WizardSpec struct {
	KubeconfigPath string
	Context        string
	Namespace      string
	ResourceKind   string
	ObjectName     string
	PodName        string
	Bind           string
	// SelectedPorts are the remote ports the user picked for forwarding.
	SelectedPorts []int
	// PortPairs maps selected remote ports to local ports.
	PortPairs []PortPair
}

type PortPair struct {
	Remote int
	Local  int
}

// stepChosenMsg indicates a step was completed. Sent by each step's update
// when the user makes a final selection.
type stepChosenMsg struct {
	Step int
}

// KubeChosenMsg indicates step ① was completed.
type KubeChosenMsg struct {
	Path    string
	Context string
}

// NsChosenMsg indicates step ② was completed.
type NsChosenMsg struct {
	Name string
}

// ResourceTypeChosenMsg indicates step ③ was completed.
type ResourceTypeChosenMsg struct {
	Kind string
}

// ObjectChosenMsg indicates step ④ was completed.
type ObjectChosenMsg struct {
	Name        string
	PodName     string
	RemotePorts []int
}

// PortMapReadyMsg indicates step ⑤ was completed and the forward started.
// It carries the user's edited (local, remote) pairs verbatim — the local
// ports may have been re-mapped at step ⑤, but the remotes must stay as
// the container ports the daemon will dial on the pod.
type PortMapReadyMsg struct {
	ID        string
	PortPairs []PortPair
}

// ActiveChosenMsg indicates the active view was returned from.
type ActiveChosenMsg struct{}

// ---------------------------------------------------------------------------
// Async data messages (produced by tea.Cmd functions)
// ---------------------------------------------------------------------------

// KubeconfigsLoadedMsg carries the result of an async kubeconfig scan.
type KubeconfigsLoadedMsg struct {
	Entries []kubeEntry
	Dirs    []string
	Err     error
}

// NamespacesLoadedMsg carries the result of an async namespace list.
type NamespacesLoadedMsg struct {
	Path string
	List []string
	Err  error
}

// ResourcesLoadedMsg carries the result of an async resource list.
type ResourcesLoadedMsg struct {
	Kind string
	List []resourceItem
	Err  error
}

// PortsLoadedMsg carries the result of an async port computation.
type PortsLoadedMsg struct {
	Kind   string
	Object string
	Ports  []int
	Pod    string
	Err    error
}

// ForwardStartedMsg is produced after a successful forward.start.
type ForwardStartedMsg struct {
	ID         string
	LocalPorts []int
	Err        error
}

// ForwardsLoadedMsg carries the active-view list.
type ForwardsLoadedMsg struct {
	List []ipcForward
	Err  error
}

type ipcForward = struct {
	ID         string
	Kubeconfig string
	Namespace  string
	Kind       string
	Object     string
	PodName    string
	Bind       string
	Ports      string
	Status     string
	StartedAt  string
}

// TickActiveMsg is sent periodically while the active-view is shown,
// prompting the TUI to re-fetch the forwards list so it stays current.
type TickActiveMsg struct{}

// ForwardEventMsg is produced when the daemon pushes a forward.* event.
// The TUI ignores it unless the active view is visible.
type ForwardEventMsg struct {
	EventName string
	ForwardID string
}

// StopForwardMsg is produced when the user presses d/x in the active view.
// It is purely a *trigger* — the app handles it by firing the IPC and
// returning a follow-up cmd. The IPC result arrives as StopForwardResultMsg.
type StopForwardMsg struct {
	ID  string
	Err error
}

// StopForwardResultMsg is the IPC result of a forward.stop. Success (Err ==
// nil) means the forward was stopped; non-nil Err means the daemon reported
// a failure (e.g. "not_found" if the forward was already gone). Handle the
// result here — do NOT re-fire the IPC from this handler.
type StopForwardResultMsg struct {
	ID  string
	Err error
}

// ClaimedPortsLoadedMsg carries the result of forward.claimedPorts — the
// authoritative list of local ports the daemon has registered in its
// manager. Used by step ⑤ for pre-flight conflict detection that doesn't
// race against the daemon's SPDY dial timing (unlike cross-process
// tryListen, which can miss duplicates when neither forward has bound
// anything yet).
type ClaimedPortsLoadedMsg struct {
	Ports []int
	Err   error
}
