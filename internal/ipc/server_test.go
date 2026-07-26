package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestServer_ConcurrentClients spins up a real Unix-socket Server and fires
// N concurrent clients at it. Each client makes K round-trip Calls and
// verifies the response. The test catches races in the per-conn handler,
// the Handler dispatch, and concurrent response writes.
func TestServer_ConcurrentClients(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	sock := filepath.Join(t.TempDir(), "kpf.sock")

	var hits int64
	handler := &Handler{
		Methods: map[string]HandlerFunc{
			"ping": func(ctx context.Context, conn *Conn, req Request) error {
				atomic.AddInt64(&hits, 1)
				// Echo whatever raw params we got — used to verify the
				// response reaches the right client.
				return conn.WriteResponse(OK(req.ID, PingResult{
					Version:   "test-1.0",
					UptimeSec: int(atomic.LoadInt64(&hits)),
					Echo:      string(req.Params),
				}))
			},
		},
	}
	srv := &Server{Socket: sock, Handler: handler}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	// Wait for the socket file to appear before clients dial.
	waitForSocket(t, sock, 2*time.Second)

	const clients = 4
	const callsPerClient = 8

	var wg sync.WaitGroup
	wg.Add(clients)
	errs := make(chan error, clients*callsPerClient)

	for i := 0; i < clients; i++ {
		go func(id int) {
			defer wg.Done()
			c := NewClient(sock)
			dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer dcancel()
			if err := c.Connect(dctx); err != nil {
				errs <- fmt.Errorf("client %d connect: %w", id, err)
				return
			}
			defer c.Close()
			for j := 0; j < callsPerClient; j++ {
				nonce := fmt.Sprintf("c%d-n%d", id, j)
				params, _ := json.Marshal(map[string]string{"nonce": nonce})
				var res PingResult
				cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := c.Call(cctx, "ping", params, &res); err != nil {
					errs <- fmt.Errorf("client %d call %d: %w", id, j, err)
					ccancel()
					continue
				}
				ccancel()
				if !contains(res.Echo, nonce) {
					errs <- fmt.Errorf("client %d call %d: echo = %q, want substring %q",
						id, j, res.Echo, nonce)
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if got := atomic.LoadInt64(&hits); got != int64(clients*callsPerClient) {
		t.Errorf("server saw %d hits, want %d", got, clients*callsPerClient)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("Serve returned %v after shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Serve did not return after ctx cancel")
	}
}

// TestServer_UnknownMethod returns a structured error to a caller asking
// for a method the handler doesn't know about.
func TestServer_UnknownMethod(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "kpf.sock")
	handler := &Handler{Methods: map[string]HandlerFunc{}}
	srv := &Server{Socket: sock, Handler: handler}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	waitForSocket(t, sock, 2*time.Second)

	c := NewClient(sock)
	cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ccancel()
	if err := c.Connect(cctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	var res json.RawMessage
	err := c.Call(cctx, "no.such.method", nil, &res)
	if err == nil {
		t.Fatal("expected error for unknown method, got nil")
	}
	ce, ok := err.(*CallError)
	if !ok {
		t.Fatalf("want *CallError, got %T: %v", err, err)
	}
	if ce.Code != ErrCodeUnknownMethod {
		t.Errorf("code = %q, want %q", ce.Code, ErrCodeUnknownMethod)
	}
}

// contains is a small substring helper that avoids pulling in strings.Contains
// for this single use; kept local so the test reads standalone.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// waitForSocket polls for the unix-socket file to appear, since Serve
// runs in a goroutine and Listen may not have completed by the time
// the test body starts dialing.
func waitForSocket(t *testing.T, sock string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s did not appear within %s", sock, timeout)
}