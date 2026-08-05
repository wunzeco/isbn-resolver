package resolver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestResolveOpenLibrarySuccess(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:9780134190440": map[string]interface{}{
				"title": "The Go Programming Language",
				"authors": []map[string]interface{}{
					{"name": "Alan Donovan"},
					{"name": "Brian Kernighan"},
				},
				"publishers": []map[string]interface{}{
					{"name": "Addison-Wesley"},
				},
				"publish_date":    "2015",
				"number_of_pages": 380,
				"subjects": []map[string]interface{}{
					{"name": "Go (Computer program language)"},
				},
			},
		})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Google Books should not be called when Open Library succeeds")
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	metadata, err := client.Resolve("9780134190440")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if metadata.Title != "The Go Programming Language" {
		t.Errorf("Title = %q, want %q", metadata.Title, "The Go Programming Language")
	}
	if metadata.Publisher != "Addison-Wesley" {
		t.Errorf("Publisher = %q, want %q", metadata.Publisher, "Addison-Wesley")
	}
	if metadata.Pages != 380 {
		t.Errorf("Pages = %d, want 380", metadata.Pages)
	}
	if len(metadata.Authors) != 2 {
		t.Errorf("Authors = %v, want 2 authors", metadata.Authors)
	}
}

func TestResolveFallsBackToGoogleBooks(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"totalItems": 1,
			"items": []map[string]interface{}{
				{
					"volumeInfo": map[string]interface{}{
						"title":         "The Go Programming Language",
						"authors":       []string{"Alan Donovan", "Brian Kernighan"},
						"publisher":     "Addison-Wesley",
						"publishedDate": "2015-10-26",
						"pageCount":     380,
						"categories":    []string{"Computers"},
						"industryIdentifiers": []map[string]string{
							{"type": "ISBN_10", "identifier": "0134190440"},
							{"type": "ISBN_13", "identifier": "9780134190440"},
						},
					},
				},
			},
		})
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	metadata, err := client.Resolve("9780134190440")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if metadata.Title != "The Go Programming Language" {
		t.Errorf("Title = %q, want %q", metadata.Title, "The Go Programming Language")
	}
	if metadata.ISBN10 != "0134190440" {
		t.Errorf("ISBN10 = %q, want %q", metadata.ISBN10, "0134190440")
	}
	if metadata.ISBN13 != "9780134190440" {
		t.Errorf("ISBN13 = %q, want %q", metadata.ISBN13, "9780134190440")
	}
}

func TestResolveFailsWhenBothAPIsHaveNoData(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"totalItems": 0})
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	_, err := client.Resolve("0000000000")
	if err == nil {
		t.Fatal("expected an error when neither API has data, got nil")
	}
	// A dual "no data" answer is the one failure shape that is genuinely the
	// tool's answer rather than an environmental accident, so it has to stay
	// recognisable through the aggregate error.
	if !errors.Is(err, ErrNoData) {
		t.Errorf("error = %v, want it to wrap ErrNoData", err)
	}
}

// TestResolveErrorNamesEachAPIFailure is the core of reporting *why* an ISBN
// failed. A 429 from Google Books means its quota is spent and the ISBN may
// well be resolvable later; a 404 from Open Library means that catalog has
// nothing. The old flat "failed to resolve ISBN from all APIs" collapsed both
// into one string — in output and, worse, in the persisted cache file — which
// is what made the 76/488 sample measurement impossible to interpret
// (specs/third-fallback-api.md §0).
func TestResolveErrorNamesEachAPIFailure(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL
	// 429 is retryable; without this the test would sit out real backoff.
	client.MaxRetries = 0

	_, err := client.Resolve("9780134190440")
	if err == nil {
		t.Fatal("expected an error when both APIs fail, got nil")
	}

	msg := err.Error()
	for _, want := range []string{APIOpenLibrary, "404", APIGoogleBooks, "429"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}

	var resolveErr *ResolveError
	if !errors.As(err, &resolveErr) {
		t.Fatalf("error = %T, want *ResolveError", err)
	}
	if resolveErr.ISBN != "9780134190440" {
		t.Errorf("ResolveError.ISBN = %q, want %q", resolveErr.ISBN, "9780134190440")
	}

	// Structured status codes, not just prose: the next step is categorising
	// several hundred failures, and regexing message text to do it would be
	// fragile in exactly the place accuracy matters.
	wantStatus := map[string]int{APIOpenLibrary: 404, APIGoogleBooks: 429}
	if len(resolveErr.Failures) != len(wantStatus) {
		t.Fatalf("Failures = %v, want one per API tier", resolveErr.Failures)
	}
	for _, f := range resolveErr.Failures {
		var statusErr *StatusError
		if !errors.As(f.Err, &statusErr) {
			t.Errorf("%s failure = %T (%v), want *StatusError", f.API, f.Err, f.Err)
			continue
		}
		if statusErr.StatusCode != wantStatus[f.API] {
			t.Errorf("%s status = %d, want %d", f.API, statusErr.StatusCode, wantStatus[f.API])
		}
	}
}

// TestResolveErrorDoesNotLeakAPIKey extends the redaction guarantee to the
// aggregate error. Resolve's error is what main writes into the cache file and
// prints on the failure line, so it — not just the Google Books error it wraps
// — is the string a user is most likely to paste into a bug report.
func TestResolveErrorDoesNotLeakAPIKey(t *testing.T) {
	const apiKey = "super-secret-key"

	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer openLibrary.Close()

	// A closed server forces the *url.Error path, whose message embeds the
	// full request URL — key included.
	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := googleBooks.URL
	googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = closedURL
	client.GoogleBooksAPIKey = apiKey

	_, err := client.Resolve("9780134190440")
	if err == nil {
		t.Fatal("expected an error when Google Books is unreachable, got nil")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("Resolve error leaked the API key: %v", err)
	}
	if !strings.Contains(err.Error(), redactedAPIKey) {
		t.Errorf("error = %q, want it to name the redaction placeholder %q", err, redactedAPIKey)
	}
}

// TestRedactAPIKeyPreservesErrorsWithoutTheKey pins the type-preserving half of
// redaction. A *StatusError carries no URL and so no key; flattening it into a
// bare errors.New would silently cost ResolveError the ability to tell a 429
// from a 404 whenever a key happens to be configured — the exact distinction
// the key exists to expose.
func TestRedactAPIKeyPreservesErrorsWithoutTheKey(t *testing.T) {
	client := NewAPIClient(5 * time.Second)
	client.GoogleBooksAPIKey = "super-secret-key"

	original := &StatusError{StatusCode: http.StatusTooManyRequests}

	var statusErr *StatusError
	if got := client.redactAPIKey(original); !errors.As(got, &statusErr) {
		t.Fatalf("redactAPIKey(%v) = %T, want the *StatusError preserved", original, got)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, http.StatusTooManyRequests)
	}
}

// googleBooksVolumeResponse is the minimal well-formed volumes payload the key
// tests need — they assert on the request, not the parse, which the fallback
// test above already covers.
func googleBooksVolumeResponse() map[string]interface{} {
	return map[string]interface{}{
		"totalItems": 1,
		"items": []map[string]interface{}{
			{"volumeInfo": map[string]interface{}{"title": "The Go Programming Language"}},
		},
	}
}

// TestGoogleBooksSendsAPIKeyWhenConfigured pins the whole point of key support:
// without `key=` on the query string Google bills the request against the
// shared anonymous per-IP quota, which is what exhausted mid-run and made 53
// resolvable ISBNs look like catalog gaps (specs/third-fallback-api.md §0).
func TestGoogleBooksSendsAPIKeyWhenConfigured(t *testing.T) {
	const apiKey = "test-api-key-12345"

	var gotKey string
	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(googleBooksVolumeResponse())
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.GoogleBooksBaseURL = googleBooks.URL
	client.GoogleBooksAPIKey = apiKey

	if _, err := client.fetchFromGoogleBooks("9780134190440"); err != nil {
		t.Fatalf("fetchFromGoogleBooks returned error: %v", err)
	}
	if gotKey != apiKey {
		t.Errorf("key query parameter = %q, want %q", gotKey, apiKey)
	}
}

// TestGoogleBooksOmitsAPIKeyWhenUnset guards the degrade-to-anonymous promise:
// a user without a Google account must keep the exact behaviour the tool had
// before key support existed, so `key` must be absent entirely rather than
// present and empty (an empty key is a 400 from Google, not an anonymous call).
func TestGoogleBooksOmitsAPIKeyWhenUnset(t *testing.T) {
	var hadKey bool
	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadKey = r.URL.Query()["key"]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(googleBooksVolumeResponse())
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.GoogleBooksBaseURL = googleBooks.URL

	if _, err := client.fetchFromGoogleBooks("9780134190440"); err != nil {
		t.Fatalf("fetchFromGoogleBooks returned error: %v", err)
	}
	if hadKey {
		t.Error("request carried a key query parameter with no API key configured")
	}
}

// TestGoogleBooksErrorDoesNotLeakAPIKey is the security case. net/http reports
// a transport failure as a *url.Error whose message embeds the whole request
// URL — including the key. That message is printed to stderr under --verbose
// and written verbatim into the on-disk cache file, so a leak here is durable
// and easy to share by accident.
//
// A closed server is the cheapest way to force that path: doWithRetry returns
// transport errors immediately, so this costs no backoff sleeps.
func TestGoogleBooksErrorDoesNotLeakAPIKey(t *testing.T) {
	const apiKey = "super-secret-key"

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := googleBooks.URL
	googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.GoogleBooksBaseURL = closedURL
	client.GoogleBooksAPIKey = apiKey

	_, err := client.fetchFromGoogleBooks("9780134190440")
	if err == nil {
		t.Fatal("expected an error from an unreachable Google Books server, got nil")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error message leaked the API key: %v", err)
	}
	if !strings.Contains(err.Error(), redactedAPIKey) {
		t.Errorf("error message = %q, want it to name the redaction placeholder %q", err, redactedAPIKey)
	}
}

// TestGoogleBooksErrorDoesNotLeakEscapedAPIKey covers the form the key actually
// takes on the wire. A key containing characters url.QueryEscape rewrites
// appears in the URL — and so in the *url.Error — percent-encoded, which a
// naive search for the raw key would walk straight past.
func TestGoogleBooksErrorDoesNotLeakEscapedAPIKey(t *testing.T) {
	const apiKey = "key with spaces/and+slashes"

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := googleBooks.URL
	googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.GoogleBooksBaseURL = closedURL
	client.GoogleBooksAPIKey = apiKey

	_, err := client.fetchFromGoogleBooks("9780134190440")
	if err == nil {
		t.Fatal("expected an error from an unreachable Google Books server, got nil")
	}
	for _, form := range []string{apiKey, url.QueryEscape(apiKey)} {
		if strings.Contains(err.Error(), form) {
			t.Errorf("error message leaked the API key as %q: %v", form, err)
		}
	}
}

// TestRedactAPIKeyIsANoopWithoutAKey keeps the anonymous error path byte-for-
// byte what it was: with no key configured there is nothing to hide, and
// rewriting the message would only degrade it.
func TestRedactAPIKeyIsANoopWithoutAKey(t *testing.T) {
	client := NewAPIClient(5 * time.Second)

	original := errors.New("Get \"http://example.test/volumes?q=isbn:9780134190440\": dial tcp: refused")
	if got := client.redactAPIKey(original); got != original {
		t.Errorf("redactAPIKey rewrote the error with no key configured: %v", got)
	}
	if got := client.redactAPIKey(nil); got != nil {
		t.Errorf("redactAPIKey(nil) = %v, want nil", got)
	}
}
