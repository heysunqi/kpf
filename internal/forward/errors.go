package forward

import (
	"errors"
	"fmt"
	"net"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// MaxStaleAttempts is the number of consecutive "pod not found" failures
// that flip a forward into the Stale state (no further retries).
const MaxStaleAttempts = 3

// PortInUseError is returned when the local port can't be bound.
type PortInUseError struct {
	Local int
	Bind  string
	Cause error
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("local port %s:%d already in use: %v", e.Bind, e.Local, e.Cause)
}

// IsPortInUse reports whether err is a PortInUseError.
func IsPortInUse(err error) bool {
	var e *PortInUseError
	return errors.As(err, &e)
}

// tryListen attempts to bind a TCP socket. Used purely for conflict detection;
// the listener is closed immediately.
func tryListen(bind string, port int) (net.Listener, error) {
	host := bind
	if host == "" {
		host = "127.0.0.1"
	}
	return net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
}

// IsLocalPortFree reports whether the (bind, port) pair can be bound on
// this host right now. Returns nil if the bind succeeds (port is free),
// or a non-nil error if the bind fails (port is in use or otherwise
// unavailable). The probe listener is closed immediately, so this is
// race-prone against other processes that may grab the port between
// the probe and the actual forwarder starting — the daemon still does
// its own authoritative check before the forward goes live.
//
// Used by the TUI to give immediate "port in use" feedback so the user
// doesn't wait for an IPC roundtrip just to learn their local port is
// already taken by a previous forward or another process.
func IsLocalPortFree(bind string, port int) error {
	ln, err := tryListen(bind, port)
	if err != nil {
		return err
	}
	_ = ln.Close()
	return nil
}

// isPodMissing reports whether err signals that the backing pod is gone
// (or never existed).
func isPodMissing(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	s := err.Error()
	for _, sub := range []string{"not found", "no pods match"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// classifyDropError categorizes a drop error for the StatusMessage field.
func classifyDropError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errAuthLost) {
		return "auth lost"
	}
	if isPodMissing(err) {
		return "pod missing"
	}
	return err.Error()
}

// errAuthLost is the sentinel for auth-token failures surfaced by spdy.
var errAuthLost = errors.New("auth lost")