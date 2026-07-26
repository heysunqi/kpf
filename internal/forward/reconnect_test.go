package forward

import (
	"testing"
	"time"
)

func TestBackoff_FirstAttempt(t *testing.T) {
	bo := NewBackoff()
	d := bo.Next()
	if d <= 0 {
		t.Errorf("first Next should be > 0, got %v", d)
	}
	if d > bo.Base {
		t.Errorf("first Next should be <= Base (%v), got %v", bo.Base, d)
	}
}

func TestBackoff_BoundedByCap(t *testing.T) {
	bo := NewBackoff()
	// Many iterations should never exceed the cap (jitter bound).
	for i := 0; i < 50; i++ {
		d := bo.Next()
		if d <= 0 {
			t.Errorf("attempt %d: zero/negative duration %v", i, d)
		}
		if d > bo.Cap {
			t.Errorf("attempt %d: %v > cap %v", i, d, bo.Cap)
		}
	}
}

func TestBackoff_Reset(t *testing.T) {
	bo := NewBackoff()
	for i := 0; i < 5; i++ {
		bo.Next()
	}
	bo.Reset()
	if bo.Attempt() != 0 {
		t.Errorf("after Reset attempt = %d", bo.Attempt())
	}
	d := bo.Next()
	if d > bo.Base {
		t.Errorf("post-reset first should be <= Base, got %v", d)
	}
}

func TestIsPodMissing(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain text", errorString("connection refused"), false},
		{"not found", errorString("pod not found"), true},
		{"no pods match", errorString("no pods match dep/x (selector=foo)"), true},
	}
	for _, c := range cases {
		if got := isPodMissing(c.err); got != c.want {
			t.Errorf("%s: isPodMissing = %v, want %v", c.name, got, c.want)
		}
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

var _ = time.Now