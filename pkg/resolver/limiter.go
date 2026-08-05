package resolver

import (
	"sync"
	"time"
)

// RateLimiter is a thread-safe token bucket meant to be shared across all
// workers in a pool, acquired once before each outbound request, so the
// group as a whole stays under a request rate that avoids triggering 429s
// in the first place (spec §4). It is distinct from the per-request
// backoff in retry.go: that reacts to a 429 already received on one
// request, while this limiter proactively paces every request and must
// never let one caller's wait delay another caller whose token is already
// available.
type RateLimiter struct {
	mu         sync.Mutex
	rate       float64 // tokens added per second; <= 0 means unlimited
	burst      float64 // bucket capacity
	tokens     float64
	lastRefill time.Time

	now   func() time.Time
	sleep func(time.Duration)
}

// NewRateLimiter creates a limiter allowing `rate` requests per second with
// a burst capacity of `burst` (coerced up to at least 1). The bucket starts
// full so an initial burst of up to `burst` requests pays no wait. A
// non-positive rate disables limiting entirely.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		rate:       rate,
		burst:      float64(burst),
		tokens:     float64(burst),
		lastRefill: time.Now(),
		now:        time.Now,
		sleep:      time.Sleep,
	}
}

// Wait blocks until a token is available, then consumes it. Safe for
// concurrent use by multiple workers. The lock is only held long enough to
// refill/consume the bucket; the actual wait happens outside the lock so a
// caller sleeping for its own token never blocks another caller from
// consuming a token that's already available.
func (l *RateLimiter) Wait() {
	if l == nil {
		return
	}
	for {
		l.mu.Lock()
		l.refillLocked()
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return
		}
		wait := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()

		if wait <= 0 {
			wait = time.Millisecond
		}
		l.sleep(wait)
	}
}

// refillLocked adds tokens for elapsed wall-clock time since the last
// refill, capped at the bucket's burst capacity. Callers must hold l.mu.
func (l *RateLimiter) refillLocked() {
	if l.rate <= 0 {
		l.tokens = l.burst
		return
	}
	now := l.now()
	elapsed := now.Sub(l.lastRefill).Seconds()
	l.lastRefill = now

	l.tokens += elapsed * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
}
