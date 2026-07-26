package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxFrameSize = 1 << 20 // 1 MiB

// ErrFrameTooLarge is returned when a single JSON frame exceeds maxFrameSize.
var ErrFrameTooLarge = errors.New("ipc: frame too large")

// ReadFrame reads a single newline-terminated JSON value.
func ReadFrame(r *bufio.Reader) (json.RawMessage, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	return line, nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(out) > 0 {
				return out, io.EOF
			}
			return out, err
		}
		if b == '\n' {
			return out, nil
		}
		out = append(out, b)
		if len(out) > maxFrameSize {
			// drain the rest of the line so the stream stays aligned
			for {
				c, err := r.ReadByte()
				if err != nil {
					return out, ErrFrameTooLarge
				}
				if c == '\n' {
					return out, ErrFrameTooLarge
				}
			}
		}
	}
}

// WriteFrame marshals payload as JSON, writes it followed by '\n', and flushes.
func WriteFrame(w *bufio.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

// ReadRequest reads one Request frame.
func ReadRequest(r *bufio.Reader) (Request, error) {
	var req Request
	raw, err := ReadFrame(r)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, fmt.Errorf("ipc: decode request: %w", err)
	}
	return req, nil
}

// ReadEvent reads one Event frame.
func ReadEvent(r *bufio.Reader) (Event, error) {
	var ev Event
	raw, err := ReadFrame(r)
	if err != nil {
		return ev, err
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, fmt.Errorf("ipc: decode event: %w", err)
	}
	return ev, nil
}
