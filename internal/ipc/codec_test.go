package ipc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRoundtripFrame(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := WriteFrame(w, map[string]string{"hello": "world"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := bufio.NewReader(&buf)
	raw, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"hello":"world"`) {
		t.Errorf("got %q", string(raw))
	}
}

func TestReadFrame_Truncated(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader([]byte("{no-newline")))
	_, err := ReadFrame(r)
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want EOF", err)
	}
}

func TestReadFrame_TooLarge(t *testing.T) {
	big := strings.Repeat("a", maxFrameSize+10) + "\n"
	r := bufio.NewReader(bytes.NewReader([]byte(big)))
	_, err := ReadFrame(r)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("err = %v, want ErrFrameTooLarge", err)
	}
}
