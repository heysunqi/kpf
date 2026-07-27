// Package daemon hosts the kpf background process. It owns the IPC socket,
// the forward.Manager, and lifecycle (signals, state persistence, etc.).
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kpf/internal/config"
	"kpf/internal/forward"
	"kpf/internal/ipc"
	"kpf/internal/k8s"
	"kpf/internal/kubeconfig"
	"kpf/internal/state"
)

// Handler implements the IPC method table for the daemon.
type Handler struct {
	startedAt time.Time
	paths     config.Paths
	version   string
	log       *slog.Logger
	manager   *forward.Manager
	state     *state.Store

	persistWG sync.WaitGroup
}

// NewHandler constructs an IPC handler.
func NewHandler(paths config.Paths, version string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{
		startedAt: time.Now(),
		paths:     paths,
		version:   version,
		log:       log,
		state:     state.NewStore(paths.StateFile),
	}
	h.manager = forward.NewManager(log)
	h.startPersistence()
	h.restoreOnStart()
	h.startParityAudit()
	return h
}

// startPersistence subscribes to manager events and persists the state file
// whenever a forward's status transitions.
func (h *Handler) startPersistence() {
	sub := h.manager.Subscribe()
	h.persistWG.Add(1)
	go func() {
		defer h.persistWG.Done()
		for e := range sub {
			h.persistForward(e)
		}
	}()
}

// restoreOnStart reads state.json and rehydrates the manager with any
// forwards that were active at the previous shutdown. Each forward is
// restored in its own goroutine so a slow k8s API (or one that's
// unreachable) doesn't block the IPC server from coming up.
func (h *Handler) restoreOnStart() {
	st, err := h.state.Load()
	if err != nil {
		h.log.Warn("load state.json", "err", err)
		return
	}
	for _, f := range st.Forwards {
		if f.Status == string(forward.StatusStopped) || f.Status == string(forward.StatusStale) {
			continue
		}
		f := f // capture
		spec := forward.Spec{
			ID:             f.ID,
			KubeconfigPath: f.Kubeconfig,
			Namespace:      f.Namespace,
			Kind:           f.Kind,
			Object:         f.Object,
			PodName:        f.PodName,
			Bind:           f.Bind,
		}
		for _, p := range f.Ports {
			spec.Ports = append(spec.Ports, forward.PortPair{Local: p.Local, Remote: p.Remote})
		}
		go func() {
			// Fire-and-forget: don't block the IPC server. WaitReady is
			// short so Manager.Start returns quickly after spec validation
			// + port-conflict check; f.Run() continues dialing in the
			// background and the forward transitions to Dropped/Stale on
			// its own if the dial fails.
			if _, err := h.manager.Start(forward.StartOptions{Spec: spec, WaitReady: 200 * time.Millisecond}); err != nil {
				h.log.Warn("restore forward", "id", f.ID, "err", err)
			} else {
				h.log.Info("restored forward", "id", f.ID, "ns", f.Namespace, "kind", f.Kind, "object", f.Object)
			}
		}()
	}
}

// persistForward upserts the state.json record for the forward that emitted e.
func (h *Handler) persistForward(e forward.Event) {
	info, ok := h.manager.Get(e.ForwardID)

	// If the forward is gone (e.g. user stopped it) or terminal, drop the record.
	if !ok || info.Status == forward.StatusStopped || info.Status == forward.StatusStale {
		_ = h.state.Mutate(func(s *state.State) error {
			for i, f := range s.Forwards {
				if f.ID == e.ForwardID {
					s.Forwards = append(s.Forwards[:i], s.Forwards[i+1:]...)
					return nil
				}
			}
			return nil
		})
		return
	}

	startedAt, _ := time.Parse(time.RFC3339, info.StartedAt)
	rec := state.Forward{
		ID:            info.ID,
		Kubeconfig:    info.Kubeconfig,
		Namespace:     info.Namespace,
		Kind:          info.Kind,
		Object:        info.Object,
		PodName:       info.PodName,
		Bind:          info.Bind,
		StartedAt:     startedAt,
		Status:        string(info.Status),
		StatusMessage: info.StatusMessage,
	}
	for _, p := range info.Ports {
		rec.Ports = append(rec.Ports, state.PortMap{Local: p.Local, Remote: p.Remote})
	}

	if err := h.state.Mutate(func(s *state.State) error {
		for i, f := range s.Forwards {
			if f.ID == rec.ID {
				s.Forwards[i] = rec
				return nil
			}
		}
		s.Forwards = append(s.Forwards, rec)
		return nil
	}); err != nil {
		h.log.Warn("persist state", "id", rec.ID, "err", err)
	}
}

// WaitPersistence blocks until the persistence goroutine has flushed its
// in-flight work. Called from the daemon shutdown path.
func (h *Handler) WaitPersistence() {
	h.persistWG.Wait()
}

// Methods returns the ipc.Handler that dispatches to the registered methods.
func (h *Handler) Methods() *ipc.Handler {
	return &ipc.Handler{
		Methods: map[string]ipc.HandlerFunc{
			ipc.MethodPing:           h.handlePing,
			ipc.MethodShutdown:       h.handleShutdown,
			ipc.MethodKubeconfigs:    h.handleKubeconfigs,
			ipc.MethodNamespaces:     h.handleNamespaces,
			ipc.MethodResources:      h.handleResources,
			ipc.MethodPorts:          h.handlePorts,
			ipc.MethodForwardStart:   h.handleForwardStart,
			ipc.MethodForwardList:    h.handleForwardList,
			ipc.MethodForwardStop:    h.handleForwardStop,
			ipc.MethodForwardStopAll: h.handleForwardStopAll,
			ipc.MethodForwardRestart: h.handleForwardRestart,
			ipc.MethodForwardEvents:  h.handleForwardEvents,
			ipc.MethodForwardLogs:    h.handleForwardLogs,
			ipc.MethodForwardClaimed: h.handleForwardClaimed,
			ipc.MethodForwardLivePorts: h.handleForwardLivePorts,
		},
	}
}

func (h *Handler) handlePing(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	res := ipc.PingResult{
		Version:   h.version,
		UptimeSec: int(time.Since(h.startedAt).Seconds()),
	}
	return conn.WriteResponse(ipc.OK(req.ID, res))
}

func (h *Handler) handleShutdown(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	h.log.Info("shutdown requested via IPC")
	_ = conn.WriteResponse(ipc.OK(req.ID, map[string]bool{"ok": true}))
	go func() {
		// Best-effort: stop all forwards, flush persistence, then exit.
		h.manager.Shutdown()
		h.WaitPersistence()
		time.Sleep(50 * time.Millisecond)
		os.Exit(0)
	}()
	return nil
}

// ---------------------------------------------------------------------------
// forward.* handlers
// ---------------------------------------------------------------------------

func (h *Handler) handleForwardStart(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.StartForwardParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	spec := forward.Spec{
		KubeconfigPath: p.Kubeconfig,
		Context:        p.Context,
		Namespace:      p.Namespace,
		Kind:           p.Kind,
		Object:         p.Object,
		PodName:        p.PodName,
		Bind:           p.Bind,
	}
	if spec.Bind == "" {
		spec.Bind = "0.0.0.0"
	}
	for _, pm := range p.Ports {
		spec.Ports = append(spec.Ports, forward.PortPair{Local: pm.Local, Remote: pm.Remote})
	}

	info, err := h.manager.Start(forward.StartOptions{
		Spec:      spec,
		WaitReady: 15 * time.Second,
	})
	if err != nil {
		code := classifyStartError(err)
		return conn.WriteResponse(ipc.Fail(req.ID, code, err.Error()))
	}
	res := ipc.StartForwardResult{
		ForwardID:  info.ID,
		LocalPorts: info.LocalPorts,
	}
	return conn.WriteResponse(ipc.OK(req.ID, res))
}

func (h *Handler) handleForwardList(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	all := h.manager.List()
	out := ipc.ListForwardsResult{Forwards: make([]ipc.ForwardInfo, 0, len(all))}
	for _, info := range all {
		out.Forwards = append(out.Forwards, infoToWire(info))
	}
	return conn.WriteResponse(ipc.OK(req.ID, out))
}

func (h *Handler) handleForwardStop(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.StopForwardParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	if err := h.manager.Stop(p.ForwardID); err != nil {
		code := ipc.ErrCodeInternal
		if strings.Contains(err.Error(), "not found") {
			code = ipc.ErrCodeNotFound
		}
		return conn.WriteResponse(ipc.Fail(req.ID, code, err.Error()))
	}
	return conn.WriteResponse(ipc.OK(req.ID, ipc.StopForwardResult{Stopped: true}))
}

// handleForwardRestart re-issues the spec for an ID against the daemon's
// in-memory history (populated by Start, retained past Stop, cleared on
// Shutdown). If the forward is currently alive it is stopped first; then
// Start is called with the original spec, which preserves the ID via
// Spec.ID. Returns not_found if the ID has no retained spec — e.g. it was
// never started in this daemon, or Shutdown has run.
func (h *Handler) handleForwardRestart(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.RestartForwardParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	spec, ok := h.manager.Spec(p.ForwardID)
	if !ok {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeNotFound,
			fmt.Sprintf("forward %q not found in history", p.ForwardID)))
	}
	if _, alive := h.manager.Get(p.ForwardID); alive {
		if err := h.manager.Stop(p.ForwardID); err != nil {
			h.log.Warn("restart: stop before restart failed", "id", p.ForwardID, "err", err)
		}
	}
	info, err := h.manager.Start(forward.StartOptions{
		Spec:      spec,
		WaitReady: 15 * time.Second,
	})
	if err != nil {
		code := classifyStartError(err)
		return conn.WriteResponse(ipc.Fail(req.ID, code, err.Error()))
	}
	return conn.WriteResponse(ipc.OK(req.ID, ipc.RestartForwardResult{
		ForwardID:  info.ID,
		LocalPorts: info.LocalPorts,
	}))
}

func (h *Handler) handleForwardStopAll(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	count := len(h.manager.List())
	h.manager.StopAll()
	return conn.WriteResponse(ipc.OK(req.ID, ipc.StopAllResult{StoppedCount: count}))
}

// handleForwardEvents subscribes the calling connection to manager-wide
// forward events. A successful Response is sent first; subsequent Event
// frames are pushed until the connection closes.
func (h *Handler) handleForwardEvents(ctx context.Context, conn *ipc.Conn, req ipc.Request) error {
	if err := conn.WriteResponse(ipc.OK(req.ID, map[string]bool{"ok": true})); err != nil {
		return err
	}
	sub := h.manager.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub:
			if !ok {
				return nil
			}
			if err := conn.WriteEvent(eventToWire(e)); err != nil {
				return err
			}
		}
	}
}

// handleForwardLogs subscribes the calling connection to log events for a
// single forward. Same wire shape as forward.events: an OK ack, then a
// stream of Event frames carrying forward.log payloads (stream + line).
// The IPC client treats the OK frame as a no-op (SubscribeEvents unmarshals
// every frame as an Event and continues on mismatch).
func (h *Handler) handleForwardLogs(ctx context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.LogsParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	if p.ForwardID == "" {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest, "forward_id is required"))
	}
	if _, ok := h.manager.Get(p.ForwardID); !ok {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeNotFound,
			fmt.Sprintf("forward %q not found", p.ForwardID)))
	}
	if err := conn.WriteResponse(ipc.OK(req.ID, map[string]bool{"ok": true})); err != nil {
		return err
	}
	sub := h.manager.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub:
			if !ok {
				return nil
			}
			if e.ForwardID != p.ForwardID || e.Type != forward.EventLog {
				continue
			}
			if err := conn.WriteEvent(eventToWire(e)); err != nil {
				return err
			}
		}
	}
}

// handleForwardClaimed returns the local ports claimed by every registered
// forward's spec — i.e. ports the manager considers taken. The TUI's
// pre-flight uses this for authoritative conflict detection that doesn't
// race against the daemon's SPDY dial timing.
func (h *Handler) handleForwardClaimed(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	ports := h.manager.ClaimedPorts()
	return conn.WriteResponse(ipc.OK(req.ID, ipc.ClaimedPortsResult{Ports: ports}))
}

// handleForwardLivePorts returns the local TCP ports that kpf's
// portforwarders are currently bound to (kernel-level, not spec-declared).
// Used by `kpf doctor` for listener-parity checks. Returns nil (not an
// empty slice) when no forward has reached Ready yet — distinguishing
// "haven't checked" from "checked, none bound".
func (h *Handler) handleForwardLivePorts(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	ports := h.manager.LivePorts()
	return conn.WriteResponse(ipc.OK(req.ID, ipc.LivePortsResult{Ports: ports}))
}

// ---------------------------------------------------------------------------
// k8s.* handlers (used by the TUI)
// ---------------------------------------------------------------------------

func (h *Handler) handleKubeconfigs(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	dirs := kubeconfig.DefaultDirs()
	entries, err := kubeconfig.ScanDirs(dirs)
	if err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeInternal, err.Error()))
	}
	out := ipc.KubeconfigsResult{Dirs: dirs}
	for _, e := range entries {
		out.Entries = append(out.Entries, ipc.KubeconfigEntry{
			Path:           e.Path,
			Basename:       e.Basename,
			CurrentContext: e.CurrentContext,
			Clusters:       e.Clusters,
			Contexts:       e.Contexts,
			Users:          e.Users,
			Size:           e.Size,
		})
	}
	return conn.WriteResponse(ipc.OK(req.ID, out))
}

func (h *Handler) handleNamespaces(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.NamespacesParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	cs, err := k8s.NewForSpec(context.Background(), p.Kubeconfig, p.Context)
	if err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeKubeError, err.Error()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nss, err := k8s.ListNamespaces(ctx, cs)
	if err != nil {
		code := ipc.ErrCodeKubeError
		if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
			code = ipc.ErrCodeAuthError
		}
		return conn.WriteResponse(ipc.Fail(req.ID, code, err.Error()))
	}
	return conn.WriteResponse(ipc.OK(req.ID, ipc.NamespacesResult{Namespaces: nss}))
}

func (h *Handler) handleResources(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.ResourcesParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	if p.Kind == "" {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest, "kind is required"))
	}
	cs, err := k8s.NewForSpec(context.Background(), p.Kubeconfig, p.Context)
	if err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeKubeError, err.Error()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	items, err := listByKind(ctx, cs, p.Kind, p.Namespace)
	if err != nil {
		code := ipc.ErrCodeKubeError
		if apierrors.IsNotFound(err) {
			code = ipc.ErrCodeNotFound
		}
		return conn.WriteResponse(ipc.Fail(req.ID, code, err.Error()))
	}
	// For high-level resources, optionally resolve one pod each so the TUI can
	// show "→ pod-name".
	out := make([]ipc.ResourceSummary, 0, len(items))
	for _, r := range items {
		sum := ipc.ResourceSummary{
			Kind:     r.Kind,
			Name:     r.Name,
			Age:      r.AgeStr,
			Ready:    r.Ready,
			Status:   r.Status,
			Replicas: r.Replicas,
			Selector: r.Selector,
			Type:     r.Type,
			ClusterIP: r.ClusterIP,
		}
		if r.Kind != "Pod" {
			pod, perr := k8s.ResolveOnePod(ctx, cs, r.Kind, p.Namespace, r.Name)
			if perr == nil {
				sum.PodName = pod.Name
			}
		} else {
			sum.PodName = r.Name
		}
		out = append(out, sum)
	}
	return conn.WriteResponse(ipc.OK(req.ID, ipc.ResourcesResult{Items: out}))
}

func (h *Handler) handlePorts(_ context.Context, conn *ipc.Conn, req ipc.Request) error {
	var p ipc.PortsParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeBadRequest,
			fmt.Sprintf("decode params: %v", err)))
	}
	cs, err := k8s.NewForSpec(context.Background(), p.Kubeconfig, p.Context)
	if err != nil {
		return conn.WriteResponse(ipc.Fail(req.ID, ipc.ErrCodeKubeError, err.Error()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ports, podName, err := k8s.ComputePorts(ctx, cs, p.Kind, p.Namespace, p.Object)
	if err != nil {
		code := ipc.ErrCodeKubeError
		if apierrors.IsNotFound(err) {
			code = ipc.ErrCodeNotFound
		}
		return conn.WriteResponse(ipc.Fail(req.ID, code, err.Error()))
	}
	return conn.WriteResponse(ipc.OK(req.ID, ipc.PortsResult{
		RemotePorts: ports,
		PodName:     podName,
	}))
}

// listByKind routes to the appropriate k8s.List* function.
func listByKind(ctx context.Context, cs kubernetesLike, kind, ns string) ([]k8s.ResourceSummary, error) {
	switch kind {
	case "Pod":
		return k8s.ListPods(ctx, cs, ns)
	case "Service":
		return k8s.ListServices(ctx, cs, ns)
	case "Deployment":
		return k8s.ListDeployments(ctx, cs, ns)
	case "StatefulSet":
		return k8s.ListStatefulSets(ctx, cs, ns)
	case "ReplicaSet":
		return k8s.ListReplicaSets(ctx, cs, ns)
	}
	return nil, fmt.Errorf("unsupported kind %q", kind)
}

// kubernetesLike is the subset of kubernetes.Interface our helpers need.
// Defined locally to keep the handler file self-contained.
type kubernetesLike = interface {
	k8s.KubernetesHelpers
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func infoToWire(info forward.Info) ipc.ForwardInfo {
	ports := make([]ipc.PortMap, 0, len(info.Ports))
	for _, p := range info.Ports {
		ports = append(ports, ipc.PortMap{Local: p.Local, Remote: p.Remote})
	}
	return ipc.ForwardInfo{
		ID:            info.ID,
		Kubeconfig:    info.Kubeconfig,
		Namespace:     info.Namespace,
		Kind:          info.Kind,
		Object:        info.Object,
		PodName:       info.PodName,
		Bind:          info.Bind,
		Ports:         ports,
		Status:        string(info.Status),
		StatusMessage: info.StatusMessage,
		StartedAt:     info.StartedAt,
	}
}

func eventToWire(e forward.Event) ipc.Event {
	// Convert status values from forward.Status to ipc.* constants where they overlap.
	var status string
	if e.Status != "" {
		status = string(e.Status)
	}
	payload, _ := json.Marshal(struct {
		Status  string    `json:"status,omitempty"`
		Message string    `json:"message,omitempty"`
		Stream  string    `json:"stream,omitempty"`
		Line    string    `json:"line,omitempty"`
		Time    time.Time `json:"time,omitempty"`
	}{
		Status:  status,
		Message: e.Message,
		Stream:  e.Stream,
		Line:    e.Line,
		Time:    e.Time,
	})

	// Map internal event types to wire event names.
	name := string(e.Type)
	switch e.Type {
	case forward.EventReady:
		name = ipc.EventForwardReady
	case forward.EventDropped:
		name = ipc.EventForwardDropped
	case forward.EventStopped:
		name = ipc.EventForwardStopped
	case forward.EventLog:
		name = ipc.EventForwardLog
	}
	return ipc.Event{
		Event:     name,
		ForwardID: e.ForwardID,
		Payload:   payload,
	}
}

// classifyStartError maps an error returned by Manager.Start to an ipc code.
func classifyStartError(err error) string {
	switch {
	case forward.IsPortInUse(err):
		return ipc.ErrCodePortInUse
	case errors.Is(err, errNotFoundSentinel):
		return ipc.ErrCodeNotFound
	case apierrors.IsUnauthorized(err), apierrors.IsForbidden(err):
		return ipc.ErrCodeAuthError
	case isKubeError(err):
		return ipc.ErrCodeKubeError
	default:
		return ipc.ErrCodeInternal
	}
}

func isKubeError(err error) bool {
	if err == nil {
		return false
	}
	var s *apierrors.StatusError
	if errors.As(err, &s) {
		return true
	}
	for _, k := range []string{"kubeconfig", "clientset", "connection refused", "dial tcp"} {
		if strings.Contains(err.Error(), k) {
			return true
		}
	}
	return false
}

// errNotFoundSentinel is used by classifyStartError. Manager.Start produces
// not-found errors as fmt.Errorf strings, not sentinel values.
var errNotFoundSentinel = errors.New("not found")

// to keep net import alive for future TCP probe use
var _ net.Listener = (*net.UnixListener)(nil)