package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
	"github.com/wunzeco/isbn-resolver/pkg/sheets"
	"google.golang.org/api/option"
	sheetsapi "google.golang.org/api/sheets/v4"
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

// TestIntegrationResolvedLineNamesTheAnsweringTier proves the fallback chain is
// observable from --verbose output: two ISBNs resolve identically as far as the
// metadata is concerned, but one was carried by Open Library and the other only
// by the Google Books fallback, and the progress line has to say which.
//
// This is what makes a re-measurement run interpretable
// (specs/third-fallback-api.md §4) — without it, "the second tier is earning
// its keep" is not a claim the output can support.
func TestIntegrationResolvedLineNamesTheAnsweringTier(t *testing.T) {
	const (
		openLibraryISBN = "9780134190440"
		googleBooksISBN = "9780132350884"
	)

	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isbnStr := strings.TrimPrefix(r.URL.Query().Get("bibkeys"), "ISBN:")
		w.Header().Set("Content-Type", "application/json")
		if isbnStr != openLibraryISBN {
			// An empty body is Open Library's "no record", which is what
			// drives the fallback to the next tier.
			json.NewEncoder(w).Encode(map[string]interface{}{})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:" + isbnStr: map[string]interface{}{"title": "Open Library Book"},
		})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"totalItems": 1,
			"items": []map[string]interface{}{
				{"volumeInfo": map[string]interface{}{"title": "Google Books Book"}},
			},
		})
	}))
	defer googleBooks.Close()

	client := resolver.NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	var progress strings.Builder
	store := cache.New()
	policy := cache.NewPolicy(store, cache.ModeNoCache)

	// Concurrency 1 keeps the two lines in input order so each can be matched
	// whole, rather than asserting on two independently-ordered substrings.
	if _, failures := resolveISBNs(1, []string{openLibraryISBN, googleBooksISBN}, client, store, policy, &progress); len(failures) != 0 {
		t.Fatalf("run reported failures: %v", failures)
	}

	got := progress.String()
	for _, want := range []string{
		"✓ Resolved ISBN " + openLibraryISBN + ": Open Library Book (via Open Library)\n",
		"✓ Resolved ISBN " + googleBooksISBN + ": Google Books Book (via Google Books)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress output = %q, want it to contain %q", got, want)
		}
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

// fakeSheetsAPI is an httptest-backed stand-in for the Sheets API that keeps
// whatever WriteResults last wrote and serves it back to ReadExistingStatus.
//
// It stores rows rather than asserting on them because the assertion that
// matters is behavioural: the second run must not call the resolver. Serving
// back the writer's own output — rather than hand-authored rows, which is all
// the unit tests in main_test.go can do — is what makes the round trip real,
// so the nine columns the writer encodes and the reader decodes cannot drift
// apart unnoticed.
type fakeSheetsAPI struct {
	mu     sync.Mutex
	values [][]interface{}
}

func (f *fakeSheetsAPI) put(values [][]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values = values
}

func (f *fakeSheetsAPI) get() [][]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values
}

func newFakeSheetsAPI(t *testing.T) *sheets.Client {
	t.Helper()

	fake := &fakeSheetsAPI{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodPut: // Values.Update, i.e. WriteResults.
			var body sheetsapi.ValueRange
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding write body: %v", err)
			}
			fake.put(body.Values)
			w.Write([]byte(`{}`))
		case http.MethodGet: // Values.Get, i.e. ReadExistingStatus.
			json.NewEncoder(w).Encode(&sheetsapi.ValueRange{
				MajorDimension: "ROWS",
				Values:         fake.get(),
			})
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	service, err := sheetsapi.NewService(ctx,
		option.WithEndpoint(server.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	return sheets.NewClient(ctx, service)
}

// resolverRequests counts outbound resolver requests per ISBN. Per-ISBN rather
// than a single total because "which ISBNs cost a network call" is the actual
// claim under test — a total alone cannot tell a skipped success from a
// re-attempted failure.
type resolverRequests struct {
	mu     sync.Mutex
	byISBN map[string]int
}

func (r *resolverRequests) record(isbnStr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byISBN == nil {
		r.byISBN = make(map[string]int)
	}
	r.byISBN[isbnStr]++
}

// requested returns the ISBNs that cost at least one request since the last
// reset. The count itself is not asserted on: an unresolvable ISBN costs one
// request per tier, and pinning that number would make the test a statement
// about the fallback chain's length rather than about the cache.
func (r *resolverRequests) requested() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	isbns := make([]string, 0, len(r.byISBN))
	for isbnStr := range r.byISBN {
		isbns = append(isbns, isbnStr)
	}
	sort.Strings(isbns)

	return isbns
}

// sorted matches requested()'s ordering, so an expectation can be written in
// whatever order reads best at the call site.
func sorted(isbns ...string) []string {
	sort.Strings(isbns)
	return isbns
}

func (r *resolverRequests) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byISBN = nil
}

// newCountingResolverAPI stands up both upstreams. Only resolvable answers;
// every other ISBN is a genuine catalog miss in both tiers, which is what
// produces an Error row in the sheet for the retry-failed case below.
func newCountingResolverAPI(t *testing.T, resolvable string) (*resolver.APIClient, *resolverRequests) {
	t.Helper()

	requests := &resolverRequests{}

	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isbnStr := strings.TrimPrefix(r.URL.Query().Get("bibkeys"), "ISBN:")
		requests.record(isbnStr)

		w.Header().Set("Content-Type", "application/json")
		if isbnStr != resolvable {
			// An empty body is Open Library's "no record".
			json.NewEncoder(w).Encode(map[string]interface{}{})
			return
		}

		// A full record, so the round trip through the sheet's nine columns is
		// exercised on every field the writer emits rather than on a title
		// alone.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:" + isbnStr: map[string]interface{}{
				"title":           "The Go Programming Language",
				"authors":         []map[string]interface{}{{"name": "Alan A. A. Donovan"}, {"name": "Brian W. Kernighan"}},
				"publishers":      []map[string]interface{}{{"name": "Addison-Wesley"}},
				"publish_date":    "2015-11-16",
				"number_of_pages": 380,
				"subjects":        []map[string]interface{}{{"name": "Computers"}, {"name": "Programming"}},
			},
		})
	}))
	t.Cleanup(openLibrary.Close)

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.record(strings.TrimPrefix(r.URL.Query().Get("q"), "isbn:"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"totalItems": 0})
	}))
	t.Cleanup(googleBooks.Close)

	client := resolver.NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	return client, requests
}

// sheetCarriedFields narrows a result to the part the nine output columns can
// carry, for comparing a freshly-resolved run against one served from the
// sheet.
//
// ISBN13 is deliberately excluded: the writer stores the original ISBN in the
// ISBN-13 column when no ISBN-13 was resolved, so a row that round-trips comes
// back with ISBN13 populated where the cold run left it empty. That is the
// writer's documented behaviour, not a cache defect, and asserting on the whole
// struct would make this test fail for it.
func sheetCarriedFields(m resolver.BookMetadata) resolver.BookMetadata {
	return resolver.BookMetadata{
		ISBN:            m.ISBN,
		Title:           m.Title,
		Authors:         m.Authors,
		Publisher:       m.Publisher,
		PublicationDate: m.PublicationDate,
		Pages:           m.Pages,
		Categories:      m.Categories,
	}
}

// TestIntegrationSheetCacheSecondRunMakesZeroResolverRequests is
// specs/deferred-cache-features.md §1's "Integration Tests" requirement: a
// second run with --sheet-cache and no local cache file makes zero resolver
// calls for rows the sheet already marks Success.
//
// It is the only test that drives the feature end to end — a real Sheets API
// over HTTP, the real writer and reader, the real resolver over HTTP — and so
// the only one that can catch the failure mode the unit tests structurally
// cannot: the writer and the sheet-cache reader disagreeing about the column
// layout, which would leave the cache silently never hitting while every unit
// test still passed.
//
// Each run loads a cache file that does not exist, the way a fresh CI checkout
// does, so anything skipped is skipped because of the sheet and nothing else.
func TestIntegrationSheetCacheSecondRunMakesZeroResolverRequests(t *testing.T) {
	const (
		resolvable   = "9780134190440"
		unresolvable = "9780132350884"
	)
	isbns := []string{resolvable, unresolvable}

	client, requests := newCountingResolverAPI(t, resolvable)
	sheetClient := newFakeSheetsAPI(t)
	writeConfig := sheets.WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"}

	run := func(mode cache.Mode) ([]resolver.BookMetadata, cache.Counters) {
		t.Helper()

		local, err := cache.Load(filepath.Join(t.TempDir(), "cache.json"))
		if err != nil {
			t.Fatalf("loading absent cache file: %v", err)
		}

		rows, err := sheetClient.ReadExistingStatus(writeConfig)
		if err != nil {
			t.Fatalf("ReadExistingStatus() error = %v", err)
		}

		policy := cache.NewPolicy(mergeSheetCache(local, rows), mode)
		results, failures := resolveISBNs(4, isbns, client, local, policy, io.Discard)

		if err := sheetClient.WriteResults(writeConfig, results, failures); err != nil {
			t.Fatalf("WriteResults() error = %v", err)
		}

		return results, policy.Counters()
	}

	cold, coldCounters := run(cache.ModeNormal)
	if got, want := requests.requested(), sorted(resolvable, unresolvable); !reflect.DeepEqual(got, want) {
		t.Fatalf("cold run requested %v, want %v", got, want)
	}
	if coldCounters.Misses != len(isbns) {
		t.Fatalf("cold run counters = %+v, want %d misses", coldCounters, len(isbns))
	}
	if cold[0].Title == "" {
		t.Fatal("cold run did not resolve the resolvable ISBN")
	}

	requests.reset()
	warm, warmCounters := run(cache.ModeNormal)

	if got := requests.requested(); len(got) != 0 {
		t.Errorf("warm run requested %v, want none — the sheet already holds both rows", got)
	}
	if warmCounters.Hits != len(isbns) {
		t.Errorf("warm run counters = %+v, want %d hits", warmCounters, len(isbns))
	}
	for i := range isbns {
		if got, want := sheetCarriedFields(warm[i]), sheetCarriedFields(cold[i]); !reflect.DeepEqual(got, want) {
			t.Errorf("row %d served from the sheet = %+v, want %+v", i, got, want)
		}
	}

	// --retry-failed has to mean the same thing for the sheet cache as for the
	// local one, and the Error column is the only place the sheet records that
	// an ISBN failed — so this is what proves an error row round-trips as an
	// error rather than as an unusable blank.
	requests.reset()
	if _, counters := run(cache.ModeRetryFailed); counters.Hits != 1 || counters.Retried != 1 {
		t.Errorf("--retry-failed counters = %+v, want 1 hit and 1 retried", counters)
	}
	if got, want := requests.requested(), sorted(unresolvable); !reflect.DeepEqual(got, want) {
		t.Errorf("--retry-failed requested %v, want %v", got, want)
	}
}
