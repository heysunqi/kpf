// Package ipc implements the JSON-over-Unix-socket protocol between the
// TUI/CLI front-ends and the background daemon.
package ipc

import "encoding/json"

// Method names.
const (
	MethodPing            = "ping"
	MethodKubeconfigs     = "kubeconfigs.list"
	MethodNamespaces      = "namespaces.list"
	MethodResources       = "resources.list"
	MethodPorts           = "ports.list"
	MethodForwardStart    = "forward.start"
	MethodForwardList     = "forward.list"
	MethodForwardStop     = "forward.stop"
	MethodForwardStopAll  = "forward.stopAll"
	MethodForwardRestart  = "forward.restart"
	MethodForwardLogs     = "forward.logs"
	MethodForwardEvents   = "forward.events"
	MethodForwardClaimed  = "forward.claimedPorts"
	MethodForwardLivePorts = "forward.livePorts"
	MethodShutdown        = "shutdown"
)

// Event types pushed from daemon to subscribed clients.
const (
	EventForwardReady   = "forward.ready"
	EventForwardDropped = "forward.dropped"
	EventForwardStopped = "forward.stopped"
	EventForwardLog     = "forward.log"
)

// Forward status values.
const (
	StatusStarting = "starting"
	StatusReady    = "ready"
	StatusDropped  = "dropped"
	StatusStopped  = "stopped"
	StatusStale    = "stale"
)

// Error codes.
const (
	ErrCodeInternal      = "internal"
	ErrCodeBadRequest    = "bad_request"
	ErrCodeUnknownMethod = "unknown_method"
	ErrCodePortInUse     = "port_in_use"
	ErrCodeNotFound      = "not_found"
	ErrCodeKubeError     = "kube_error"
	ErrCodeAuthError     = "auth_error"
	ErrCodeAlreadyExists = "already_exists"
)

// Request is a single client→daemon call.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the daemon's reply to a Request.
type Response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error describes a failed Response.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewError constructs an Error.
func NewError(code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// Event is a server-pushed notification. It has no ID.
type Event struct {
	Event     string          `json:"event"`
	ForwardID string          `json:"forward_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// PortMap mirrors state.PortMap but lives in the wire protocol.
type PortMap struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

// StartForwardParams is the parameters of forward.start.
type StartForwardParams struct {
	Kubeconfig string    `json:"kubeconfig"`
	Context    string    `json:"context,omitempty"`
	Namespace  string    `json:"namespace"`
	Kind       string    `json:"kind"`
	Object     string    `json:"object"`
	PodName    string    `json:"pod_name,omitempty"`
	Bind       string    `json:"bind"`
	Ports      []PortMap `json:"ports"`
}

// StopForwardParams is the parameters of forward.stop.
type StopForwardParams struct {
	ForwardID string `json:"forward_id"`
}

// LogsParams is the parameters of forward.logs.
type LogsParams struct {
	ForwardID string `json:"forward_id"`
	Since     string `json:"since,omitempty"`
	Follow    bool   `json:"follow,omitempty"`
}

// PingResult is the result of ping.
type PingResult struct {
	Version   string `json:"version"`
	UptimeSec int    `json:"uptime_sec"`
	// Echo is echoed back from the raw params if the caller passed any.
	// Used by tests to verify concurrent request/response matching.
	Echo string `json:"echo,omitempty"`
}

// StartForwardResult is the result of forward.start.
type StartForwardResult struct {
	ForwardID  string `json:"forward_id"`
	LocalPorts []int  `json:"local_ports"`
}

// ForwardInfo describes a forward in list/logs responses.
type ForwardInfo struct {
	ID            string    `json:"id"`
	Kubeconfig    string    `json:"kubeconfig"`
	Namespace     string    `json:"namespace"`
	Kind          string    `json:"kind"`
	Object        string    `json:"object"`
	PodName       string    `json:"pod_name,omitempty"`
	Bind          string    `json:"bind"`
	Ports         []PortMap `json:"ports"`
	Status        string    `json:"status"`
	StatusMessage string    `json:"status_message,omitempty"`
	StartedAt     string    `json:"started_at"`
}

// ListForwardsResult is the result of forward.list.
type ListForwardsResult struct {
	Forwards []ForwardInfo `json:"forwards"`
}

// StopForwardResult is the result of forward.stop.
type StopForwardResult struct {
	Stopped bool `json:"stopped"`
}

// RestartForwardParams is the parameters of forward.restart.
type RestartForwardParams struct {
	ForwardID string `json:"forward_id"`
}

// RestartForwardResult is the result of forward.restart. Mirrors
// StartForwardResult — the new forward keeps the same ID and port mappings.
type RestartForwardResult struct {
	ForwardID  string `json:"forward_id"`
	LocalPorts []int  `json:"local_ports"`
}

// LivePortsResult is the result of forward.livePorts — the local TCP ports
// that kpf's portforwarders are currently bound to at the kernel level.
type LivePortsResult struct {
	Ports []int `json:"ports"`
}

// StopAllResult is the result of forward.stopAll.
type StopAllResult struct {
	StoppedCount int `json:"stopped_count"`
}

// ClaimedPortsResult is the result of forward.claimedPorts. The daemon
// returns every local port it has registered in its manager's spec.Ports
// (not the kernel-bound ports — those can briefly differ during the SPDY
// dial phase). The TUI uses this for authoritative pre-flight conflict
// detection, which beats the OS-level tryListen probe because it doesn't
// race against the daemon's own bind timing.
type ClaimedPortsResult struct {
	Ports []int `json:"ports"`
}

// Event payloads.
type ForwardReadyPayload struct {
	LocalPorts []int `json:"local_ports"`
}

type ForwardDroppedPayload struct {
	Reason            string `json:"reason"`
	NextReconnectInMs int    `json:"next_reconnect_in_ms,omitempty"`
}

type ForwardLogPayload struct {
	TS     string `json:"ts"`
	Stream string `json:"stream"` // "out" or "err"
	Line   string `json:"line"`
}

type ForwardStoppedPayload struct {
	Reason string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// kubeconfigs.list
// ---------------------------------------------------------------------------

// KubeconfigEntry is a single kubeconfig file's parsed metadata.
type KubeconfigEntry struct {
	Path           string   `json:"path"`
	Basename       string   `json:"basename"`
	CurrentContext string   `json:"current_context"`
	Clusters       []string `json:"clusters"`
	Contexts       []string `json:"contexts"`
	Users          []string `json:"users"`
	Size           int64    `json:"size"`
}

// KubeconfigsParams: kubeconfigs.list takes no parameters; the daemon scans
// $KPF_KUBECONFIG_DIR and ~/.kube.
type KubeconfigsParams struct{}

// KubeconfigsResult is the result of kubeconfigs.list.
type KubeconfigsResult struct {
	Entries []KubeconfigEntry `json:"entries"`
	Dirs    []string          `json:"dirs"`
}

// ---------------------------------------------------------------------------
// namespaces.list
// ---------------------------------------------------------------------------

// NamespacesParams is the parameters of namespaces.list.
type NamespacesParams struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context,omitempty"`
}

// NamespacesResult is the result of namespaces.list.
type NamespacesResult struct {
	Namespaces []string `json:"namespaces"`
}

// ---------------------------------------------------------------------------
// resources.list
// ---------------------------------------------------------------------------

// ResourcesParams is the parameters of resources.list.
type ResourcesParams struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context,omitempty"`
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"` // Pod | Service | Deployment | StatefulSet | ReplicaSet
}

// ResourceSummary describes a single k8s resource for the TUI.
type ResourceSummary struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Age       string `json:"age,omitempty"`
	Ready     string `json:"ready,omitempty"`
	Status    string `json:"status,omitempty"`
	Replicas  string `json:"replicas,omitempty"`
	Selector  string `json:"selector,omitempty"`
	Type      string `json:"type,omitempty"`
	ClusterIP string `json:"cluster_ip,omitempty"`
	PodName   string `json:"pod_name,omitempty"` // resolved pod for high-level resources
}

// ResourcesResult is the result of resources.list.
type ResourcesResult struct {
	Items []ResourceSummary `json:"items"`
}

// ---------------------------------------------------------------------------
// ports.list
// ---------------------------------------------------------------------------

// PortsParams is the parameters of ports.list.
type PortsParams struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context,omitempty"`
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"`
	Object     string `json:"object"`
}

// PortsResult is the result of ports.list.
type PortsResult struct {
	RemotePorts []int `json:"remote_ports"`
	PodName     string `json:"pod_name,omitempty"`
}
