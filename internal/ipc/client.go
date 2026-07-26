package ipc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	DefaultDialTimeout = 2 * time.Second
	DefaultCallTimeout = 30 * time.Second
)

// ErrNotRunning is returned when the daemon socket cannot be reached.
var ErrNotRunning = errors.New("ipc: daemon not running")

// Client is a TUI/CLI-side IPC client.
type Client struct {
	socket string

	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	closed bool
}

// NewClient returns a Client targeting the given socket path.
func NewClient(socket string) *Client {
	return &Client{socket: socket}
}

// Connect dials the daemon socket. Reentrant: a second call is a no-op.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	if d, ok := ctx.Deadline(); ok {
		dialer.Deadline = d
	}
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	c.writer = bufio.NewWriter(conn)
	return nil
}

// Close releases the underlying connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Call sends a request and waits for the response.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return ErrNotRunning
	}
	id := newID()
	req := Request{ID: id, Method: method}
	switch p := params.(type) {
	case nil:
		// no params
	case json.RawMessage:
		req.Params = p
	case []byte:
		req.Params = json.RawMessage(p)
	default:
		raw, err := json.Marshal(params)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		req.Params = raw
	}
	if err := WriteFrame(c.writer, req); err != nil {
		c.mu.Unlock()
		return err
	}
	conn := c.conn
	reader := c.reader
	c.mu.Unlock()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultCallTimeout)
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	raw, err := ReadFrame(reader)
	if err != nil {
		return err
	}
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return err
	}
	if resp.ID != id {
		return fmt.Errorf("ipc: id mismatch (got %q, want %q)", resp.ID, id)
	}
	if !resp.OK {
		if resp.Error != nil {
			return &CallError{Code: resp.Error.Code, Message: resp.Error.Message}
		}
		return errors.New("ipc: response without ok=true and no error")
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// CallError is the typed error returned by Call when ok=false.
type CallError struct {
	Code    string
	Message string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// SubscribeEvents sends a method call and returns a channel of Events streamed
// back on the same connection. The stream ends when ctx is cancelled, the
// connection errors, or the daemon closes.
func (c *Client) SubscribeEvents(ctx context.Context, method string, params any) (<-chan Event, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return nil, ErrNotRunning
	}
	id := newID()
	req := Request{ID: id, Method: method}
	if params != nil {
		raw, _ := json.Marshal(params)
		req.Params = raw
	}
	if err := WriteFrame(c.writer, req); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	conn := c.conn
	reader := c.reader
	c.mu.Unlock()

	out := make(chan Event, 64)
	go func() {
		defer close(out)
		for {
			if err := conn.SetReadDeadline(time.Now().Add(1 * time.Hour)); err != nil {
				return
			}
			raw, err := ReadFrame(reader)
			if err != nil {
				return
			}
			var ev Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}
