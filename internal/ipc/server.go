package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
)

// HandlerFunc handles one Request. It may emit additional events to conn
// for streaming methods like forward.events or forward.logs.
type HandlerFunc func(ctx context.Context, conn *Conn, req Request) error

// Handler maps method names to handler functions.
type Handler struct {
	Methods map[string]HandlerFunc
}

// Conn wraps a net.Conn with a thread-safe buffered codec.
type Conn struct {
	netConn net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	mu      sync.Mutex
}

func newConn(c net.Conn) *Conn {
	return &Conn{
		netConn: c,
		reader:  bufio.NewReader(c),
		writer:  bufio.NewWriter(c),
	}
}

// WriteResponse writes a Response to the connection.
func (c *Conn) WriteResponse(resp Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.writer, resp)
}

// WriteEvent writes an Event to the connection.
func (c *Conn) WriteEvent(ev Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.writer, ev)
}

// LocalAddr returns the underlying conn's local address.
func (c *Conn) LocalAddr() net.Addr { return c.netConn.LocalAddr() }

// RemoteAddr returns the underlying conn's remote address.
func (c *Conn) RemoteAddr() net.Addr { return c.netConn.RemoteAddr() }

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.netConn.Close() }

// OK builds a successful Response.
func OK(id string, result any) Response {
	raw, _ := json.Marshal(result)
	return Response{ID: id, OK: true, Result: raw}
}

// Fail builds a failed Response.
func Fail(id, code, msg string) Response {
	return Response{ID: id, OK: false, Error: NewError(code, msg)}
}

// Server is a Unix-socket IPC server.
type Server struct {
	Socket   string
	Handler  *Handler
	Log      *slog.Logger

	listener net.Listener
	wg       sync.WaitGroup
}

// Serve accepts connections until ctx is cancelled or the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	// Clean any stale socket from a previous run.
	if err := os.Remove(s.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.Socket, err)
	}
	if err := os.Chmod(s.Socket, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.listener = ln
	if s.Log != nil {
		s.Log.Info("ipc server listening", "socket", s.Socket)
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		raw, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			if s.Log != nil {
				s.Log.Warn("accept failed", "err", err)
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, raw)
		}()
	}
	s.wg.Wait()
	_ = os.Remove(s.Socket)
	return nil
}

func (s *Server) handleConn(ctx context.Context, raw net.Conn) {
	conn := newConn(raw)
	defer conn.Close()
	for {
		req, err := ReadRequest(conn.reader)
		if err != nil {
			if s.Log != nil && !errors.Is(err, net.ErrClosed) {
				s.Log.Debug("read request ended", "err", err)
			}
			return
		}
		if s.Log != nil {
			s.Log.Debug("ipc request", "method", req.Method, "id", req.ID)
		}
		fn, ok := s.Handler.Methods[req.Method]
		if !ok {
			_ = conn.WriteResponse(Fail(req.ID, ErrCodeUnknownMethod,
				fmt.Sprintf("unknown method: %s", req.Method)))
			continue
		}
		if err := fn(ctx, conn, req); err != nil {
			if s.Log != nil {
				s.Log.Warn("handler error", "method", req.Method, "err", err)
			}
			_ = conn.WriteResponse(Fail(req.ID, ErrCodeInternal, err.Error()))
		}
	}
}
