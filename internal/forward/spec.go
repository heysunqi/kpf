// Package forward implements the runtime side of port forwards: a single
// Forward per resource connection, plus a Manager that owns the lifecycle
// of every active forward.
package forward

import "time"

// PortPair describes one (local, remote) port binding.
type PortPair struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

// Spec is the user-facing description of a forward.
type Spec struct {
	// ID is optional. When set (e.g. on daemon restart when restoring from
	// state.json), the Manager uses it verbatim; otherwise it generates a
	// fresh fwd_NNNN id.
	ID             string    `json:"id,omitempty"`
	KubeconfigPath string    `json:"kubeconfig"`
	Context        string    `json:"context,omitempty"`
	Namespace      string    `json:"namespace"`
	Kind           string    `json:"kind"`   // Pod, Service, Deployment, StatefulSet, ReplicaSet
	Object         string    `json:"object"` // name of the kind/object
	PodName        string    `json:"pod_name,omitempty"`
	Bind           string    `json:"bind"`
	Ports          []PortPair `json:"ports"`
}

// Status enumerates the lifecycle states of a forward.
type Status string

const (
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusDropped  Status = "dropped"
	StatusStopped  Status = "stopped"
	StatusStale    Status = "stale"
	StatusError    Status = "error"
)

// Info is a snapshot of a forward's state, suitable for IPC responses.
type Info struct {
	ID            string    `json:"id"`
	Kubeconfig    string    `json:"kubeconfig"`
	Namespace     string    `json:"namespace"`
	Kind          string    `json:"kind"`
	Object        string    `json:"object"`
	PodName       string    `json:"pod_name,omitempty"`
	Bind          string    `json:"bind"`
	Ports         []PortPair `json:"ports"`
	Status        Status    `json:"status"`
	StatusMessage string    `json:"status_message,omitempty"`
	LocalPorts    []int     `json:"local_ports,omitempty"`
	StartedAt     string    `json:"started_at"`
	UptimeSec     int       `json:"uptime_sec,omitempty"`
}

// nowFunc is overridable in tests.
var nowFunc = time.Now
