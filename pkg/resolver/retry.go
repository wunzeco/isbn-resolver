package resolver

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// retryableStatus reports whether an HTTP response status should be retried
// rather than falling through to the next API/fallback immediately.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable
}

// retryDelay computes how long to wait before the next attempt. A
// Retry-After header (seconds or an HTTP-date) takes precedence over the
// computed exponential backoff, per spec §4.
func retryDelay(resp *http.Response, base time.Duration, attempt int, jitter func() time.Duration) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				return time.Duration(secs) * time.Second
			}
			if t, err := http.ParseTime(ra); err == nil {
				if d := time.Until(t); d > 0 {
					return d
				}
				return 0
			}
		}
	}
	backoff := base << uint(attempt) // base * 2^attempt
	return backoff + jitter()
}

// doWithRetry executes fn (a single HTTP round trip) and retries on 429/503
// responses up to MaxRetries additional times, sleeping between attempts
// per retryDelay. A transport-level error (fn returning err != nil) is not
// retried here and is returned immediately. Non-retryable status codes
// (e.g. 404) are returned on the first attempt with no retry.
func (c *APIClient) doWithRetry(fn func() (*http.Response, error)) (*http.Response, error) {
	maxRetries := c.MaxRetries
	base := c.BaseBackoff
	if base <= 0 {
		base = defaultBaseBackoff
	}

	for attempt := 0; ; attempt++ {
		c.Limiter.Wait()
		resp, err := fn()
		if err != nil {
			return nil, err
		}
		if !retryableStatus(resp.StatusCode) || attempt >= maxRetries {
			return resp, nil
		}
		delay := retryDelay(resp, base, attempt, func() time.Duration { return c.jitter(base) })
		resp.Body.Close()
		c.sleep(delay)
	}
}

// defaultJitter returns a delay uniformly distributed in [0, base) to spread
// out retries from workers that all backed off at the same time.
func defaultJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(base)))
}
