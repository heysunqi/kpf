package forward

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"kpf/internal/kubeconfig"
)

// probeInterval is how often the post-ready health probe dials each local
// forwarded port. Short enough to detect a dead forwarder before the user's
// next request hits a wedged accept loop, long enough to be invisible at
// idle. The probe doesn't traverse SPDY itself (it stops at the kernel
// listener) but combined with client-go's SPDY-level ping (5s) and the
// kpf reconnect loop, it forms a three-tier liveness stack:
//
//	1. SPDY ping      (5s)  — surfaces "kubelet not responding" to klog
//	2. local-port probe (5s) — surfaces "accept loop / kernel listener dead"
//	3. user request   (∞)  — last-resort: surfaces "SPDY half-dead but kernel OK"
//
// The probe only triggers an explicit Close when the kernel refuses the
// dial. The other tiers surface through ForwardPorts's existing
// CloseChan() select (see portforward.go:339-344), which our Run() loop
// already converts into a backoff reconnect.
const probeInterval = 5 * time.Second

// Forward holds the state of one active port-forward.
type Forward struct {
	id   string
	spec Spec
	log  *slog.Logger

	mu        sync.Mutex
	status    Status
	message   string
	locals    []int
	startedAt time.Time

	stopCh chan struct{}
	doneCh chan struct{}
	pf     *portforward.PortForwarder

	subMu sync.Mutex
	subs  []chan Event
}

func newForward(id string, spec Spec, log *slog.Logger) *Forward {
	return &Forward{
		id:        id,
		spec:      spec,
		log:       log,
		status:    StatusStarting,
		startedAt: nowFunc(),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// ID returns the forward's unique identifier.
func (f *Forward) ID() string { return f.id }

// Spec returns the forward's spec.
func (f *Forward) Spec() Spec { return f.spec }

// Info returns a snapshot of the forward's state.
func (f *Forward) Info() Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	ports := append([]PortPair(nil), f.spec.Ports...)
	locals := append([]int(nil), f.locals...)
	return Info{
		ID:            f.id,
		Kubeconfig:    f.spec.KubeconfigPath,
		Namespace:     f.spec.Namespace,
		Kind:          f.spec.Kind,
		Object:        f.spec.Object,
		PodName:       f.spec.PodName,
		Bind:          f.spec.Bind,
		Ports:         ports,
		Status:        f.status,
		StatusMessage: f.message,
		LocalPorts:    locals,
		StartedAt:     f.startedAt.UTC().Format(time.RFC3339),
		UptimeSec:     int(nowFunc().Sub(f.startedAt).Seconds()),
	}
}

// Subscribe returns a channel of events from this forward. The channel is
// closed when the forward terminates.
func (f *Forward) Subscribe() <-chan Event {
	ch := make(chan Event, 32)
	f.subMu.Lock()
	f.subs = append(f.subs, ch)
	f.subMu.Unlock()
	return ch
}

// Stop signals the forward to terminate and blocks until done.
func (f *Forward) Stop() {
	f.mu.Lock()
	select {
	case <-f.stopCh:
		// already stopping
		f.mu.Unlock()
		<-f.doneCh
		return
	default:
		close(f.stopCh)
	}
	f.mu.Unlock()
	<-f.doneCh
}

// Run blocks until the forward terminates. It must be called once per Forward.
//
// The Run loop:
//   1. Establishes the SPDY connection (dial).
//   2. Waits for ready or stop.
//   3. Once ready, waits for an unexpected drop or stop.
//   4. On unexpected drop: schedules a reconnect with exponential backoff
//      and tries again. After MaxStaleAttempts consecutive "not found"
//      errors the forward transitions to stale and stops retrying.
func (f *Forward) Run() {
	defer close(f.doneCh)
	defer f.shutdown()

	bo := NewBackoff()
	notFoundCount := 0

	for {
		// Run one attempt. runOnce returns (err, status):
		//   err == nil, status == ready  → still running; will signal via stopCh
		//   err == nil, status == stopped → user requested stop, exit cleanly
		//   err != nil                    → unexpected drop, may retry
		err := f.runOnce()

		// Successful stop = done.
		if err == nil && f.IsStopped() {
			return
		}

		// Reached here = unexpected drop or initial dial error.
		if err != nil {
			if f.log != nil {
				f.log.Warn("forward drop", "id", f.id, "err", err)
			}

			// Pod-missing is non-recoverable after a few tries.
			if isPodMissing(err) {
				notFoundCount++
				if notFoundCount >= MaxStaleAttempts {
					f.setStatus(StatusStale, err)
					f.broadcast(Event{Type: EventStale, Message: err.Error()})
					return
				}
			} else {
				notFoundCount = 0
			}

			f.setStatus(StatusDropped, err)
			f.broadcast(Event{Type: EventDropped, Message: err.Error()})

			wait := bo.Next()
			if f.log != nil {
				f.log.Info("forward reconnect scheduled", "id", f.id, "wait", wait)
			}
			select {
			case <-f.stopCh:
				f.setStatus(StatusStopped, nil)
				f.broadcast(Event{Type: EventStopped})
				return
			case <-time.After(wait):
				bo.attempt = 0 // reset backoff window for next iteration
				continue
			}
		}
		// err == nil && !IsStopped: shouldn't happen, but be safe.
		return
	}
}

// IsStopped reports whether the forward has been (or is being) stopped by the user.
func (f *Forward) IsStopped() bool {
	select {
	case <-f.stopCh:
		return true
	default:
		return false
	}
}

// runOnce performs one dial → wait-for-ready → wait-for-drop attempt.
// Returns nil if the user stopped the forward; non-nil on unexpected error.
func (f *Forward) runOnce() error {
	if err := f.dial(); err != nil {
		return err
	}

	readyCh := f.pf.Ready
	pfErrCh := make(chan error, 1)
	go func() {
		pfErrCh <- f.pf.ForwardPorts()
	}()

	select {
	case <-readyCh:
		locals := make([]int, 0, len(f.spec.Ports))
		for _, pp := range f.spec.Ports {
			locals = append(locals, pp.Local)
		}
		f.setStatus(StatusReady, nil)
		f.setLocals(locals)
		f.broadcast(Event{Type: EventReady})
	case err := <-pfErrCh:
		// ForwardPorts exited before the portforwarder became Ready. Most
		// common cause is bind failure (port already in use) — without
		// this case the error would sit unread in pfErrCh and the
		// forward would hang in "starting" forever. Surface it so the
		// retry loop in Run() can mark the forward Dropped / Stale.
		msg := errMsgOr(err, "portforward failed before ready")
		return errors.New(msg)
	case <-f.stopCh:
		<-pfErrCh // drain
		f.setStatus(StatusStopped, nil)
		f.broadcast(Event{Type: EventStopped})
		return nil
	}

	// Post-ready health probe: surfaces "accept loop dead" / "kernel
	// listener gone" faster than the user's next request. Doesn't catch
	// every SPDY half-death (those go through the upstream
	// streamConn.CloseChan() select below) but closes the gap for the
	// common case where the listener is gone but the SPDY conn is still
	// alive enough not to fail a read.
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		f.runHealthCheck()
	}()

	// Once ready, wait for an unexpected drop or stop.
	select {
	case err := <-pfErrCh:
		<-healthDone // probe exits via its own stopCh branch on pf.Close
		if f.IsStopped() {
			f.setStatus(StatusStopped, nil)
			f.broadcast(Event{Type: EventStopped})
			return nil
		}
		msg := errMsgOr(err, "spdy disconnected")
		return errors.New(msg)
	case <-f.stopCh:
		<-pfErrCh // drain
		<-healthDone
		f.setStatus(StatusStopped, nil)
		f.broadcast(Event{Type: EventStopped})
		return nil
	}
}

// dial sets up the SPDY connection and creates the PortForwarder.
func (f *Forward) dial() error {
	portSpecs := make([]string, 0, len(f.spec.Ports))
	for _, p := range f.spec.Ports {
		portSpecs = append(portSpecs, fmt.Sprintf("%d:%d", p.Local, p.Remote))
	}

	cfg, err := kubeconfig.Load(f.spec.KubeconfigPath, f.spec.Context)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	rt, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return fmt.Errorf("spdy roundtripper: %w", err)
	}

	podName := f.spec.PodName
	if podName == "" {
		podName = f.spec.Object
	}
	if podName == "" {
		return fmt.Errorf("pod name is empty (set PodName or use Kind=Pod)")
	}
	host := strings.TrimPrefix(cfg.Host, "https://")
	host = strings.TrimPrefix(host, "http://")
	podURL := &url.URL{
		Scheme: "https",
		Host:   host,
		Path:   fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", f.spec.Namespace, podName),
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, "POST", podURL)

	outSink := &lineSink{stream: "out", f: f}
	errSink := &lineSink{stream: "err", f: f}

	pf, err := portforward.NewOnAddresses(
		dialer,
		[]string{f.spec.Bind},
		portSpecs,
		f.stopCh,
		make(chan struct{}),
		outSink, errSink,
	)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.pf != nil {
		// A previous dial succeeded but the SPDY stream was eventually dropped.
		// Close that portforwarder now so its TCP listeners can be released
		// before we open new ones.
		f.pf.Close()
		f.pf = nil
	}
	f.pf = pf
	f.mu.Unlock()
	return nil
}

// runHealthCheck probes each local forwarded port every probeInterval. If a
// port's kernel listener refuses a dial (e.g. accept loop crashed or the
// port was externally closed), the probe calls f.pf.Close() which causes
// ForwardPorts() to return via its streamConn.CloseChan() select (see
// portforward.go:339-344). The outer Run() loop then surfaces this as a
// drop and reconnects via backoff.
//
// The probe is a kernel-level check — it does not traverse SPDY. That means
// it can't detect the "SPDY half-dead but kernel listener alive" failure
// mode (kubelet crashed but TCP FIN not yet delivered). For that we rely on
// client-go's SPDY-level ping (5s, already on by default via RoundTripperFor)
// and the kpf backoff loop. See the package comment on probeInterval for
// the three-tier liveness story.
func (f *Forward) runHealthCheck() {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-f.stopCh:
			return
		case <-ticker.C:
		}
		if !f.probeLocalPorts() {
			return
		}
	}
}

// probeLocalPorts dials each spec.Ports[i].Local once with a 2s timeout.
// On the first dial failure it calls pf.Close() (which propagates to the
// outer select via pfErrCh) and returns false. Returns true if all ports
// dialed cleanly or if there's no portforwarder yet (e.g. between dial and
// Ready).
func (f *Forward) probeLocalPorts() bool {
	f.mu.Lock()
	pf := f.pf
	bind := f.spec.Bind
	ports := append([]PortPair(nil), f.spec.Ports...)
	f.mu.Unlock()

	if pf == nil {
		return true
	}

	for _, p := range ports {
		addr := net.JoinHostPort(bind, strconv.Itoa(p.Local))
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			if f.log != nil {
				f.log.Warn("forward health probe failed; closing portforwarder",
					"id", f.id, "local", p.Local, "err", err)
			}
			pf.Close()
			return false
		}
		_ = conn.Close()
	}
	return true
}

// shutdown closes all subscriber channels and releases the portforwarder's
// TCP listeners. Without the pf.Close() call, listeners can stay bound to
// their local ports even after the forward goroutine returns — observed
// as orphaned LISTEN sockets with no matching forward in the manager.
func (f *Forward) shutdown() {
	f.subMu.Lock()
	for _, ch := range f.subs {
		close(ch)
	}
	f.subs = nil
	f.subMu.Unlock()

	f.mu.Lock()
	if f.pf != nil {
		f.pf.Close()
		f.pf = nil
	}
	f.mu.Unlock()
}

// helpers

func (f *Forward) setStatus(s Status, msg error) {
	f.mu.Lock()
	f.status = s
	if msg != nil {
		f.message = msg.Error()
	} else {
		f.message = ""
	}
	f.mu.Unlock()
}

func (f *Forward) setLocals(ports []int) {
	f.mu.Lock()
	f.locals = ports
	f.mu.Unlock()
}

func (f *Forward) broadcast(e Event) {
	e.ForwardID = f.id
	e.Time = nowFunc()
	if e.Status == "" {
		e.Status = f.Info().Status
	}

	f.subMu.Lock()
	defer f.subMu.Unlock()
	for _, ch := range f.subs {
		select {
		case ch <- e:
		default:
			// drop if subscriber is slow; a real ring buffer lands in Phase 6
		}
	}
}

func errMsgOr(err error, def string) string {
	if err == nil {
		return def
	}
	return err.Error()
}

// lineSink implements io.Writer for portforward output streams, emitting each
// line as a forward event.
type lineSink struct {
	stream string
	f      *Forward
	mu     sync.Mutex
	buf    strings.Builder
}

func (l *lineSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		s := l.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(s[:idx], "\r")
		l.buf.Reset()
		l.buf.WriteString(s[idx+1:])
		if line != "" {
			l.f.broadcast(Event{Type: EventLog, Stream: l.stream, Line: line})
		}
	}
	return len(p), nil
}

func (l *lineSink) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() > 0 {
		l.f.broadcast(Event{Type: EventLog, Stream: l.stream, Line: l.buf.String()})
		l.buf.Reset()
	}
}

var _ io.Writer = (*lineSink)(nil)