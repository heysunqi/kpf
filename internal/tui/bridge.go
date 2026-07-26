package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"kpf/internal/ipc"
)

// Bridge wraps an ipc.Client for use from the TUI. It is recreated on each
// step transition (cheap; just sets a deadline).
type Bridge struct {
	c       *ipc.Client
	timeout time.Duration
}

func newBridge(socket string) *Bridge {
	return &Bridge{
		c:       ipc.NewClient(socket),
		timeout: 30 * time.Second,
	}
}

func (b *Bridge) close() {
	if b != nil && b.c != nil {
		_ = b.c.Close()
	}
}

func (b *Bridge) callCtx(parent context.Context, method string, params any, out any) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, b.timeout)
	defer cancel()
	if err := b.c.Connect(ctx); err != nil {
		return err
	}
	return b.c.Call(ctx, method, params, out)
}

// ---------------------------------------------------------------------------
// Step 1: list kubeconfigs (no daemon call needed; local scan)
// ---------------------------------------------------------------------------

type kubeEntry struct {
	Path           string
	Basename       string
	CurrentContext string
	Clusters       []string
	Contexts       []string
	Users          []string
	Size           int64
}

func (b *Bridge) listKubeconfigs() ([]kubeEntry, []string, error) {
	var res ipc.KubeconfigsResult
	if err := b.callCtx(context.Background(), ipc.MethodKubeconfigs, nil, &res); err != nil {
		return nil, res.Dirs, err
	}
	out := make([]kubeEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, kubeEntry{
			Path: e.Path, Basename: e.Basename,
			CurrentContext: e.CurrentContext,
			Clusters: e.Clusters, Contexts: e.Contexts,
			Users: e.Users, Size: e.Size,
		})
	}
	return out, res.Dirs, nil
}

// ---------------------------------------------------------------------------
// Step 2: list namespaces
// ---------------------------------------------------------------------------

func (b *Bridge) listNamespaces(path, contextName string) ([]string, error) {
	var res ipc.NamespacesResult
	params, _ := json.Marshal(ipc.NamespacesParams{Kubeconfig: path, Context: contextName})
	if err := b.callCtx(context.Background(), ipc.MethodNamespaces, params, &res); err != nil {
		return nil, err
	}
	return res.Namespaces, nil
}

// ---------------------------------------------------------------------------
// Step 3: list resources by kind
// ---------------------------------------------------------------------------

type resourceItem struct {
	Kind      string
	Name      string
	Age       string
	Ready     string
	Status    string
	Replicas  string
	Selector  string
	Type      string
	ClusterIP string
	PodName   string
}

func (b *Bridge) listResources(path, contextName, ns, kind string) ([]resourceItem, error) {
	var res ipc.ResourcesResult
	params, _ := json.Marshal(ipc.ResourcesParams{
		Kubeconfig: path, Context: contextName, Namespace: ns, Kind: kind,
	})
	if err := b.callCtx(context.Background(), ipc.MethodResources, params, &res); err != nil {
		return nil, err
	}
	out := make([]resourceItem, 0, len(res.Items))
	for _, r := range res.Items {
		out = append(out, resourceItem{
			Kind: r.Kind, Name: r.Name, Age: r.Age, Ready: r.Ready,
			Status: r.Status, Replicas: r.Replicas, Selector: r.Selector,
			Type: r.Type, ClusterIP: r.ClusterIP, PodName: r.PodName,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Step 4: compute remote ports for an object
// ---------------------------------------------------------------------------

func (b *Bridge) computePorts(path, contextName, ns, kind, object string) ([]int, string, error) {
	var res ipc.PortsResult
	params, _ := json.Marshal(ipc.PortsParams{
		Kubeconfig: path, Context: contextName,
		Namespace: ns, Kind: kind, Object: object,
	})
	if err := b.callCtx(context.Background(), ipc.MethodPorts, params, &res); err != nil {
		return nil, "", err
	}
	return res.RemotePorts, res.PodName, nil
}

// ---------------------------------------------------------------------------
// Step 5: start a forward
// ---------------------------------------------------------------------------

func (b *Bridge) startForward(path, contextName, ns, kind, object, podName, bind string, pairs []PortPair) (string, []int, error) {
	ports := make([]ipc.PortMap, 0, len(pairs))
	for _, p := range pairs {
		ports = append(ports, ipc.PortMap{Local: p.Local, Remote: p.Remote})
	}
	params, _ := json.Marshal(ipc.StartForwardParams{
		Kubeconfig: path, Context: contextName,
		Namespace: ns, Kind: kind, Object: object, PodName: podName,
		Bind: bind, Ports: ports,
	})
	var res ipc.StartForwardResult
	if err := b.callCtx(context.Background(), ipc.MethodForwardStart, params, &res); err != nil {
		return "", nil, err
	}
	return res.ForwardID, res.LocalPorts, nil
}

// ---------------------------------------------------------------------------
// Active view: list forwards + subscribe to events
// ---------------------------------------------------------------------------

func (b *Bridge) listForwards() ([]ipc.ForwardInfo, error) {
	var res ipc.ListForwardsResult
	if err := b.callCtx(context.Background(), ipc.MethodForwardList, nil, &res); err != nil {
		return nil, err
	}
	return res.Forwards, nil
}

func (b *Bridge) stopForward(id string) error {
	params, _ := json.Marshal(ipc.StopForwardParams{ForwardID: id})
	var res ipc.StopForwardResult
	return b.callCtx(context.Background(), ipc.MethodForwardStop, params, &res)
}

// claimedPorts asks the daemon for the local ports it has registered in
// its manager. Used by the TUI's port-step pre-flight check for
// authoritative conflict detection (no race against the daemon's
// SPDY dial timing, unlike cross-process tryListen).
func (b *Bridge) claimedPorts() ([]int, error) {
	var res ipc.ClaimedPortsResult
	if err := b.callCtx(context.Background(), ipc.MethodForwardClaimed, nil, &res); err != nil {
		return nil, err
	}
	return res.Ports, nil
}

// ---------------------------------------------------------------------------
// small helper
// ---------------------------------------------------------------------------

// shortErr returns the first line of err.Error(), truncated to 120 chars.
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexAny(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

var _ = fmt.Sprintf