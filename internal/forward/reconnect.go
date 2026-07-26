package forward

import (
	"math/rand"
	"time"
)

// Backoff implements full-jitter exponential backoff.
//
//	duration = rand(0, min(Cap, Base * 2^attempt))
//
// The first call to Next returns a duration in (0, Base]; subsequent
// calls double the upper bound until Cap.
type Backoff struct {
	Base time.Duration
	Cap  time.Duration

	attempt int
}

// NewBackoff returns a Backoff with the kpf defaults: 500ms base, 30s cap.
func NewBackoff() *Backoff {
	return &Backoff{Base: 500 * time.Millisecond, Cap: 30 * time.Second}
}

// Reset returns the backoff to its zero state.
func (b *Backoff) Reset() {
	b.attempt = 0
}

// Next returns the duration to wait before the next attempt.
func (b *Backoff) Next() time.Duration {
	b.attempt++
	upper := b.Base << minInt(b.attempt-1, 10)
	if upper <= 0 || upper > b.Cap {
		upper = b.Cap
	}
	if upper <= 0 {
		return 0
	}
	// full jitter: random duration in (0, upper]
	return time.Duration(rand.Int63n(int64(upper))) + 1
}

// Attempt returns the current attempt count (zero before the first Next call).
func (b *Backoff) Attempt() int { return b.attempt }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}