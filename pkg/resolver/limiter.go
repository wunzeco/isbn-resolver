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

	// now and sleep are test hooks. Wait's loop makes progress only because
	// time passes between passes, so a test that stubs both — a frozen clock
	// plus a no-op sleep — removes the loop's only exit condition. Rather
	// than leave that as an unwritten precondition on the hooks, Wait detects
	// a non-advancing clock and panics (see maxNonAdvancingWaits), turning a
	// suite that hangs to the test deadline into one that fails immediately
	// and says why.
	now   func() time.Time
	sleep func(time.Duration)
}

// maxNonAdvancingWaits bounds how many consecutive sleeps Wait will tolerate
// without l.now() advancing before it declares the clock broken. It is not 1
// because a real clock is only guaranteed to be non-decreasing, not to have
// any particular resolution, and a single coincidental repeat reading must not
// be mistaken for a frozen clock. Under a genuinely frozen clock the sleep
// hook is invariably a no-op too, so the whole budget is spent in microseconds.
const maxNonAdvancingWaits = 100

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
	// Consecutive passes whose sleep did not move the clock. Local to this
	// caller: a shared counter would let two goroutines' unrelated passes add
	// up to a false positive.
	stalled := 0
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

		// Read the clock around the sleep rather than tracking l.lastRefill,
		// which another goroutine's refill can move even when time has not.
		before := l.now()
		l.sleep(wait)
		if l.now().After(before) {
			stalled = 0
			continue
		}
		stalled++
		if stalled >= maxNonAdvancingWaits {
			panic("resolver: RateLimiter.Wait: the bucket is empty and the clock is not advancing, " +
				"so no token can ever be refilled — a test has stubbed the now/sleep hooks such that " +
				"time cannot pass while draining the bucket")
		}
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
