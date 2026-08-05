package resolver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient returns an APIClient with sleep/jitter stubbed out so retry
// tests run instantly and deterministically, while recording every delay
// the retry loop asked for.
func newTestClient() (*APIClient, *[]time.Duration) {
	client := NewAPIClient(5 * time.Second)
	var delays []time.Duration
	client.sleep = func(d time.Duration) { delays = append(delays, d) }
	client.jitter = func(time.Duration) time.Duration { return 0 }
	return client, &delays
}

func TestDoWithRetryRetriesOn429ThenSucceeds(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, delays := newTestClient()

	resp, err := client.doWithRetry(func() (*http.Response, error) {
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 4 {
		t.Errorf("attempts = %d, want 4 (3 failures + 1 success)", attempts)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.StatusCode)
	}
	if len(*delays) != 3 {
		t.Fatalf("recorded %d delays, want 3", len(*delays))
	}
	// Exponential backoff from BaseBackoff (500ms default): 500ms, 1s, 2s.
	want := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}
	for i, d := range *delays {
		if d != want[i] {
			t.Errorf("delay[%d] = %v, want %v", i, d, want[i])
		}
	}
}

func TestDoWithRetryHonoursRetryAfterHeader(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, delays := newTestClient()

	resp, err := client.doWithRetry(func() (*http.Response, error) {
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if len(*delays) != 1 {
		t.Fatalf("recorded %d delays, want 1", len(*delays))
	}
	if (*delays)[0] != 1*time.Second {
		t.Errorf("delay = %v, want 1s from Retry-After, not the computed backoff", (*delays)[0])
	}
}

func TestDoWithRetryDoesNotRetry404(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client, delays := newTestClient()

	resp, err := client.doWithRetry(func() (*http.Response, error) {
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 404)", attempts)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if len(*delays) != 0 {
		t.Errorf("recorded %d delays, want 0", len(*delays))
	}
}

func TestDoWithRetryStopsAfterMaxRetries(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, delays := newTestClient()
	client.MaxRetries = 2

	resp, err := client.doWithRetry(func() (*http.Response, error) {
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (1 initial + 2 retries)", attempts)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("final status = %d, want 503", resp.StatusCode)
	}
	if len(*delays) != 2 {
		t.Errorf("recorded %d delays, want 2", len(*delays))
	}
}

func TestRetryDelayHonoursHTTPDateRetryAfter(t *testing.T) {
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	resp := &http.Response{Header: http.Header{"Retry-After": []string{future}}}

	delay := retryDelay(resp, 500*time.Millisecond, 0, func() time.Duration { return 0 })

	if delay <= 0 || delay > 3*time.Second {
		t.Errorf("delay = %v, want roughly 2s from the HTTP-date Retry-After", delay)
	}
}

// defaultJitter is the real jitter function wired into a fresh APIClient;
// every other retry test stubs it out to zero for determinism, so it needs
// its own direct coverage.
func TestDefaultJitterWithinRange(t *testing.T) {
	base := 100 * time.Millisecond
	for i := 0; i < 50; i++ {
		d := defaultJitter(base)
		if d < 0 || d >= base {
			t.Fatalf("defaultJitter(%v) = %v, want [0, %v)", base, d, base)
		}
	}
}

func TestDefaultJitterNonPositiveBaseReturnsZero(t *testing.T) {
	for _, base := range []time.Duration{0, -time.Second} {
		if d := defaultJitter(base); d != 0 {
			t.Errorf("defaultJitter(%v) = %v, want 0", base, d)
		}
	}
}

func TestFetchFromOpenLibraryRetriesThenParses(t *testing.T) {
	var attempts int
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:9780134190440": map[string]interface{}{
				"title": "The Go Programming Language",
			},
		})
	}))
	defer openLibrary.Close()

	client, delays := newTestClient()
	client.OpenLibraryBaseURL = openLibrary.URL

	metadata, err := client.fetchFromOpenLibrary("9780134190440")
	if err != nil {
		t.Fatalf("fetchFromOpenLibrary returned error: %v", err)
	}
	if metadata.Title != "The Go Programming Language" {
		t.Errorf("Title = %q, want %q", metadata.Title, "The Go Programming Language")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if len(*delays) != 2 {
		t.Errorf("recorded %d delays, want 2", len(*delays))
	}
}
