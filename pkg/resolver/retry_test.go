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

	resp, err := client.doWithRetry(APIOpenLibrary, "9780134190440", func() (*http.Response, error) {
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

	resp, err := client.doWithRetry(APIOpenLibrary, "9780134190440", func() (*http.Response, error) {
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

	resp, err := client.doWithRetry(APIOpenLibrary, "9780134190440", func() (*http.Response, error) {
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

	resp, err := client.doWithRetry(APIOpenLibrary, "9780134190440", func() (*http.Response, error) {
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

// The verbose "Warning: rate limited by ..." line is the only signal that a
// run is sleeping off a backoff rather than hung, so every retry must be
// reported — and reported before the sleep, not after it.
func TestDoWithRetryReportsEveryRetryThroughOnRetry(t *testing.T) {
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

	// Recording the notice inside the sleep stub too would be fragile, so
	// instead assert ordering by counting sleeps seen at notice time: a
	// notice must always arrive before its own sleep.
	var sleepsAtNotice []int
	var sleeps int
	realSleep := client.sleep
	client.sleep = func(d time.Duration) {
		sleeps++
		realSleep(d)
	}

	var notices []RetryNotice
	client.OnRetry = func(n RetryNotice) {
		notices = append(notices, n)
		sleepsAtNotice = append(sleepsAtNotice, sleeps)
	}

	resp, err := client.doWithRetry(APIOpenLibrary, "9780596520687", func() (*http.Response, error) {
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if len(notices) != 2 {
		t.Fatalf("got %d notices, want 2 (one per retry)", len(notices))
	}
	want := []RetryNotice{
		{API: APIOpenLibrary, ISBN: "9780596520687", StatusCode: 429, Attempt: 1, MaxRetries: 3, Delay: 500 * time.Millisecond},
		{API: APIOpenLibrary, ISBN: "9780596520687", StatusCode: 429, Attempt: 2, MaxRetries: 3, Delay: 1 * time.Second},
	}
	for i, n := range notices {
		if n != want[i] {
			t.Errorf("notice[%d] = %+v, want %+v", i, n, want[i])
		}
		if sleepsAtNotice[i] != i {
			t.Errorf("notice[%d] fired after %d sleeps, want %d — the warning must precede its own backoff", i, sleepsAtNotice[i], i)
		}
	}
}

// A response that is never retried must stay silent: a warning about a
// retry that did not happen would be worse than no warning at all.
func TestDoWithRetryReportsNothingWhenNotRetried(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client, _ := newTestClient()
	var notices int
	client.OnRetry = func(RetryNotice) { notices++ }

	resp, err := client.doWithRetry(APIOpenLibrary, "9780134190440", func() (*http.Response, error) {
		return client.httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("doWithRetry returned error: %v", err)
	}
	defer resp.Body.Close()

	if notices != 0 {
		t.Errorf("got %d notices for a non-retryable 404, want 0", notices)
	}
}

// doWithRetry takes an opaque fn, so the API name can only be right if each
// fetchFrom* caller passes its own. Google Books must not be reported as
// Open Library.
func TestFetchFromGoogleBooksNamesItselfInRetryNotice(t *testing.T) {
	var attempts int
	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"totalItems": 1,
			"items": []map[string]interface{}{
				{"volumeInfo": map[string]interface{}{"title": "Programming Erlang"}},
			},
		})
	}))
	defer googleBooks.Close()

	client, _ := newTestClient()
	client.GoogleBooksBaseURL = googleBooks.URL

	var notices []RetryNotice
	client.OnRetry = func(n RetryNotice) { notices = append(notices, n) }

	if _, err := client.fetchFromGoogleBooks("9780596518189"); err != nil {
		t.Fatalf("fetchFromGoogleBooks returned error: %v", err)
	}

	if len(notices) != 1 {
		t.Fatalf("got %d notices, want 1", len(notices))
	}
	if notices[0].API != APIGoogleBooks {
		t.Errorf("API = %q, want %q", notices[0].API, APIGoogleBooks)
	}
	if notices[0].ISBN != "9780596518189" {
		t.Errorf("ISBN = %q, want %q", notices[0].ISBN, "9780596518189")
	}
	// An honoured Retry-After must be what the warning reports, not the
	// backoff it overrode.
	if notices[0].Delay != 2*time.Second {
		t.Errorf("Delay = %v, want 2s (the Retry-After value)", notices[0].Delay)
	}
}
