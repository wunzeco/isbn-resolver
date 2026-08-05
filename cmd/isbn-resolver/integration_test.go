package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// newIntegrationClient stands up a real Open Library server (Google Books
// fails the test if hit, since these fixtures always answer) and returns a
// resolver.APIClient wired to it plus the live request counter.
//
// Unlike countingResolver (used elsewhere in this package), this exercises
// the actual HTTP path resolveISBNs drives in production: cache lookups
// happen on this goroutine, cache misses go out over the network via
// resolver.Resolve, exactly as spec's "Integration Tests" section requires.
func newIntegrationClient(t *testing.T) (*resolver.APIClient, *int64) {
	t.Helper()

	var requests int64

	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)

		isbnStr := strings.TrimPrefix(r.URL.Query().Get("bibkeys"), "ISBN:")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:" + isbnStr: map[string]interface{}{
				"title": "Title for " + isbnStr,
			},
		})
	}))
	t.Cleanup(openLibrary.Close)

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Google Books should not be called: Open Library always answers in this fixture")
	}))
	t.Cleanup(googleBooks.Close)

	client := resolver.NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	return client, &requests
}

// TestIntegrationWarmCacheSecondRunMakesZeroRequests is spec's "Integration
// Tests" requirement #1: a second run against the same input list makes zero
// network calls when nothing changed. Unlike
// TestResolveISBNsSecondRunMakesNoNetworkCalls (which fakes bookResolver
// directly), this drives a real resolver.APIClient over HTTP so the request
// count comes from the server, not from a call-recording stub.
func TestIntegrationWarmCacheSecondRunMakesZeroRequests(t *testing.T) {
	client, requests := newIntegrationClient(t)
	path := filepath.Join(t.TempDir(), "cache.json")
	isbns := []string{"9780134190440", "9780132350884", "0596520689"}

	first, _, _ := runOnce(t, path, cache.ModeNormal, client, isbns)
	if got := atomic.LoadInt64(requests); got != int64(len(isbns)) {
		t.Fatalf("cold run made %d requests, want %d", got, len(isbns))
	}

	atomic.StoreInt64(requests, 0)
	second, _, counters := runOnce(t, path, cache.ModeNormal, client, isbns)

	if got := atomic.LoadInt64(requests); got != 0 {
		t.Errorf("warm run made %d requests, want 0", got)
	}
	if counters.Hits != len(isbns) {
		t.Errorf("warm run counters = %+v, want %d hits", counters, len(isbns))
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("warm run results differ from cold run:\n cold: %+v\n warm: %+v", first, second)
	}
}

// TestIntegrationResolveAllReissuesRequestsDespiteWarmCache is spec's
// "Integration Tests" requirement #2: --resolve-all re-issues network calls
// for every ISBN even though the cache is warm.
func TestIntegrationResolveAllReissuesRequestsDespiteWarmCache(t *testing.T) {
	client, requests := newIntegrationClient(t)
	path := filepath.Join(t.TempDir(), "cache.json")
	isbns := []string{"9780134190440", "9780132350884"}

	runOnce(t, path, cache.ModeNormal, client, isbns)
	if got := atomic.LoadInt64(requests); got != int64(len(isbns)) {
		t.Fatalf("cold run made %d requests, want %d", got, len(isbns))
	}

	atomic.StoreInt64(requests, 0)
	_, _, counters := runOnce(t, path, cache.ModeResolveAll, client, isbns)

	if got := atomic.LoadInt64(requests); got != int64(len(isbns)) {
		t.Errorf("--resolve-all made %d requests despite a warm cache, want %d", got, len(isbns))
	}
	if counters.Retried != len(isbns) {
		t.Errorf("--resolve-all counters = %+v, want %d retried", counters, len(isbns))
	}
}

// TestIntegrationConcurrentMatchesSequential is spec's "Integration Tests"
// requirement #3: concurrent resolution produces identical results to
// sequential resolution for the same input set, driven over real HTTP rather
// than an in-memory fake.
func TestIntegrationConcurrentMatchesSequential(t *testing.T) {
	client, _ := newIntegrationClient(t)
	isbns := []string{
		"9780134190440", "9780132350884", "0596520689",
		"9781491910740", "9780596007126", "9780262033848",
	}

	sequential, _, _ := func() ([]resolver.BookMetadata, map[string]error, cache.Counters) {
		store := cache.New()
		policy := cache.NewPolicy(store, cache.ModeNoCache)
		r, f := resolveISBNs(1, isbns, client, store, policy, io.Discard)
		return r, f, policy.Counters()
	}()

	concurrent, _, _ := func() ([]resolver.BookMetadata, map[string]error, cache.Counters) {
		store := cache.New()
		policy := cache.NewPolicy(store, cache.ModeNoCache)
		r, f := resolveISBNs(6, isbns, client, store, policy, io.Discard)
		return r, f, policy.Counters()
	}()

	if !reflect.DeepEqual(sequential, concurrent) {
		t.Errorf("concurrent results differ from sequential:\n sequential: %+v\n concurrent: %+v", sequential, concurrent)
	}
}

// TestIntegrationRateLimitWarningReachesProgressOutput closes the loop the
// unit tests only cover in halves: a real 429 over HTTP, through the real
// retry loop, through the pool, out to the same progress writer --verbose
// points at stderr. It is what proves the callback is actually wired rather
// than merely correct in isolation.
func TestIntegrationRateLimitWarningReachesProgressOutput(t *testing.T) {
	var attempts int64
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isbnStr := strings.TrimPrefix(r.URL.Query().Get("bibkeys"), "ISBN:")
		// Rate limit the first attempt only, so the run still succeeds and
		// the warning is the sole difference from a clean run.
		if atomic.AddInt64(&attempts, 1) == 1 {
			// Retry-After: 0 keeps the test instant — the client's sleep
			// stub is unexported and out of reach from package main, so the
			// backoff here is real time.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:" + isbnStr: map[string]interface{}{"title": "Title for " + isbnStr},
		})
	}))
	defer openLibrary.Close()

	client := resolver.NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL

	var progress strings.Builder
	client.OnRetry = retryWarner(&progress)

	store := cache.New()
	policy := cache.NewPolicy(store, cache.ModeNoCache)
	results, failures := resolveISBNs(1, []string{"9780596520687"}, client, store, policy, &progress)

	if len(failures) != 0 {
		t.Fatalf("run reported failures: %v", failures)
	}
	if results[0].Title == "" {
		t.Fatal("ISBN did not resolve after the retry")
	}

	// The exact delay rendering is pinned by TestRetryWarnerFormatsSpecLine;
	// what matters here is that a real 429 produces the line at all, naming
	// the API, the ISBN, and the attempt.
	got := progress.String()
	for _, want := range []string{
		"Warning: rate limited by Open Library, retrying ISBN 9780596520687 in ",
		"(attempt 1/3)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress output = %q, want it to contain %q", got, want)
		}
	}
}
