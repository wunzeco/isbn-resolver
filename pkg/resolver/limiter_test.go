package resolver

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterAllowsInitialBurstImmediately(t *testing.T) {
	l := NewRateLimiter(10, 3)
	start := time.Now()
	for i := 0; i < 3; i++ {
		l.Wait()
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("burst of 3 against burst capacity 3 took %v, want near-instant", elapsed)
	}
}

func TestRateLimiterEnforcesFloorDuration(t *testing.T) {
	const rate = 50.0 // tokens/sec -> 20ms/token after the burst is spent
	const burst = 1
	const n = 5

	l := NewRateLimiter(rate, burst)
	start := time.Now()
	for i := 0; i < n; i++ {
		l.Wait()
	}
	elapsed := time.Since(start)

	// First token is free (bucket starts full); the remaining n-1 tokens
	// each cost 1/rate seconds, so the whole run cannot finish faster than
	// that floor even though scheduling jitter may push it slightly over.
	floor := time.Duration(float64(n-burst)/rate*float64(time.Second)) - 5*time.Millisecond
	if elapsed < floor {
		t.Errorf("elapsed = %v, want at least floor %v (n=%d, rate=%v/s)", elapsed, floor, n, rate)
	}
}

// NewRateLimiter coerces a non-positive burst up to 1 rather than leaving a
// bucket with zero capacity, which would make Wait spin forever waiting for
// a token that a zero-capacity bucket could never hold.
func TestNewRateLimiterCoercesBurstToAtLeastOne(t *testing.T) {
	for _, burst := range []int{0, -5} {
		l := NewRateLimiter(10, burst)
		if l.burst != 1 {
			t.Errorf("NewRateLimiter(10, %d).burst = %v, want 1", burst, l.burst)
		}
		if l.tokens != 1 {
			t.Errorf("NewRateLimiter(10, %d).tokens = %v, want 1 (bucket starts full)", burst, l.tokens)
		}
	}
}

func TestRateLimiterZeroOrNegativeRateIsUnlimited(t *testing.T) {
	l := NewRateLimiter(0, 1)
	start := time.Now()
	for i := 0; i < 100; i++ {
		l.Wait()
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("100 waits against a disabled (rate<=0) limiter took %v, want near-instant", elapsed)
	}
}

func TestNilRateLimiterWaitIsANoop(t *testing.T) {
	var l *RateLimiter
	start := time.Now()
	l.Wait()
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Errorf("Wait on a nil *RateLimiter took %v, want no-op", elapsed)
	}
}

// TestRateLimiterDoesNotSerializeOtherWorkers asserts the spec §4 property
// that one caller waiting for its own token never blocks another caller
// whose token is already available: two goroutines sharing a limiter with
// enough burst for both should both return promptly, not one-after-another
// serialized by a shared lock held across the sleep.
func TestRateLimiterDoesNotSerializeOtherWorkers(t *testing.T) {
	l := NewRateLimiter(1000, 2)

	var wg sync.WaitGroup
	durations := make([]time.Duration, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			l.Wait()
			durations[idx] = time.Since(start)
		}(i)
	}
	wg.Wait()

	for i, d := range durations {
		if d > 50*time.Millisecond {
			t.Errorf("worker %d waited %v for an available token, want near-instant", i, d)
		}
	}
}

func TestRateLimiterConcurrentUseIsRaceFree(t *testing.T) {
	l := NewRateLimiter(1000, 10)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Wait()
		}()
	}
	wg.Wait()
}

func TestDoWithRetryAcquiresLimiterBeforeEachAttempt(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, _ := newTestClient()
	var waits int
	limiter := NewRateLimiter(1000, 10)
	limiter.sleep = func(time.Duration) {}
	// Freeze the limiter's clock so real wall-clock time spent on the
	// httptest round trips doesn't refill the bucket between Wait calls,
	// which would mask how many tokens each attempt actually consumed.
	frozen := time.Now()
	limiter.now = func() time.Time { return frozen }
	client.Limiter = limiter

	// Assert the observable effect of the limiter being acquired on every
	// attempt: doWithRetry still succeeds with a limiter attached, and the
	// limiter's bucket was drawn down by the expected number of attempts.
	resp, err := client.doWithRetry(func() (*http.Response, error) {
		waits++
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if waits != 3 {
		t.Errorf("attempts = %d, want 3 (2 failures + 1 success)", waits)
	}
	if limiter.tokens != 7 {
		t.Errorf("limiter tokens remaining = %v, want 7 (10 burst - 3 consumed, frozen clock so no refill)", limiter.tokens)
	}
}
