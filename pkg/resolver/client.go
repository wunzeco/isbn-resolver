package resolver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BookMetadata represents book information
type BookMetadata struct {
	ISBN            string   `json:"isbn"`
	ISBN10          string   `json:"isbn_10,omitempty"`
	ISBN13          string   `json:"isbn_13,omitempty"`
	Title           string   `json:"title"`
	Authors         []string `json:"authors"`
	Publisher       string   `json:"publisher"`
	PublicationDate string   `json:"publication_date"`
	Pages           int      `json:"pages"`
	Categories      []string `json:"categories"`
	Error           string   `json:"error,omitempty"`
}

const (
	defaultOpenLibraryBaseURL = "https://openlibrary.org"
	defaultGoogleBooksBaseURL = "https://www.googleapis.com/books/v1"
	defaultBaseBackoff        = 500 * time.Millisecond
	defaultMaxRetries         = 3

	// redactedAPIKey is what an API key is replaced with wherever it would
	// otherwise appear in an error message.
	redactedAPIKey = "REDACTED"
)

// Human-readable names for the upstream APIs, used to say which one rate
// limited us. doWithRetry takes an opaque fn and cannot infer the API from
// it, so the fetchFrom* callers name themselves.
const (
	APIOpenLibrary = "Open Library"
	APIGoogleBooks = "Google Books"
)

// ErrNoData is returned by a fetchFrom* method when the upstream answered
// normally but holds no record for the ISBN — a genuine catalog gap, as
// opposed to a transport, quota or parse failure. It is a sentinel so callers
// can tell those apart with errors.Is rather than by matching message text.
var ErrNoData = errors.New("no data found for ISBN")

// StatusError reports a non-200 response from an upstream API.
//
// It carries the code as a field rather than only in its message because "429,
// the shared anonymous quota is spent" and "404, this catalog has never heard
// of the book" mean opposite things — one is environmental and retryable, the
// other is the real signal — yet both read as "failed" once flattened into a
// string. Conflating them is what made the original 76/488 failure measurement
// uninterpretable (specs/third-fallback-api.md §0).
type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("API returned status %d", e.StatusCode)
}

// APIFailure is one upstream's reason for not producing metadata.
type APIFailure struct {
	// API is the upstream that failed (APIOpenLibrary or APIGoogleBooks).
	API string
	// Err is that upstream's own error, already redacted where the tier
	// sends a credential.
	Err error
}

// ResolveError reports that no tier produced metadata, naming each tier and
// the reason it gave.
//
// The flat "failed to resolve ISBN from all APIs" this replaced was written
// verbatim into the cache file, so a run's failures were permanently
// indistinguishable from one another: a quota-exhausted 429 and a book neither
// catalog carries looked identical on disk and in output.
type ResolveError struct {
	// ISBN is the ISBN that could not be resolved.
	ISBN string
	// Failures holds one entry per tier tried, in the order they were tried.
	Failures []APIFailure
}

func (e *ResolveError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", f.API, f.Err))
	}

	return fmt.Sprintf("failed to resolve ISBN from all APIs (%s)", strings.Join(parts, "; "))
}

// Unwrap exposes the per-tier errors so errors.Is(err, ErrNoData) and
// errors.As(err, &statusErr) reach through the aggregate. Every error stored
// here has already passed through redactAPIKey, so nothing unredacted can
// resurface this way.
func (e *ResolveError) Unwrap() []error {
	errs := make([]error, 0, len(e.Failures))
	for _, f := range e.Failures {
		errs = append(errs, f.Err)
	}

	return errs
}

// add records why one tier did not produce metadata. A nil err means the tier
// returned neither metadata nor an error — no current tier does that, but
// recording it explicitly beats printing a bare "<nil>" if one ever starts.
func (e *ResolveError) add(api string, err error) {
	if err == nil {
		err = errors.New("returned no metadata and no error")
	}

	e.Failures = append(e.Failures, APIFailure{API: api, Err: err})
}

// RetryNotice describes a retry that is about to happen: the upstream
// returned a retryable status and the client is about to sleep Delay before
// re-issuing the request. It carries everything the verbose progress line
// needs, so the presentation of the message stays in the CLI rather than in
// the resolver.
type RetryNotice struct {
	// API is the upstream that rate limited us (APIOpenLibrary or APIGoogleBooks).
	API string
	// ISBN is the ISBN being resolved when the limit was hit.
	ISBN string
	// StatusCode is the retryable status that triggered the wait (429 or 503).
	StatusCode int
	// Attempt is 1-based: 1 is the first retry, i.e. the response to the
	// initial request was retryable.
	Attempt int
	// MaxRetries is the ceiling Attempt counts towards, so a consumer can
	// render "attempt 1/3" without reaching back into the client.
	MaxRetries int
	// Delay is how long the client will wait before the next attempt,
	// either the computed backoff+jitter or an honoured Retry-After.
	Delay time.Duration
}

// APIClient handles API requests to book metadata services
type APIClient struct {
	httpClient         *http.Client
	timeout            time.Duration
	OpenLibraryBaseURL string
	GoogleBooksBaseURL string

	// GoogleBooksAPIKey, when non-empty, is sent as the `key` query parameter
	// on Google Books requests so the run draws on a registered project's
	// quota instead of the shared anonymous per-IP one. Empty means query
	// anonymously, exactly as before this field existed — a key is never
	// required to resolve an ISBN.
	//
	// It is a credential, so it must not reach an error message: every error
	// returned from fetchFromGoogleBooks goes through redactAPIKey.
	GoogleBooksAPIKey string

	// MaxRetries is how many additional attempts a 429/503 response earns
	// before falling through to the next API/fallback.
	MaxRetries int
	// BaseBackoff is the first backoff interval; subsequent attempts grow
	// exponentially from it (base * 2^attempt), before jitter is added.
	BaseBackoff time.Duration

	// Limiter, when set, is acquired before every outbound request
	// (including retries) to pace the request rate proactively. It is
	// nil by default (no limiting) so a single client used without a
	// worker pool is unaffected; a pool of workers should share one
	// APIClient (or assign the same *RateLimiter to each client) so the
	// limit applies across all of them, per spec §4.
	Limiter *RateLimiter

	// OnRetry, when set, is called once per retry, immediately before the
	// backoff sleep — so a long wait is reported as it starts rather than
	// after it finishes, which is the whole point of the progress line.
	// It is nil by default (silent).
	//
	// resolveISBNs shares one APIClient across every pool worker, so this
	// is called from multiple goroutines concurrently and any
	// implementation must be safe for concurrent use.
	OnRetry func(RetryNotice)

	// sleep and jitter are overridable in tests so retry tests don't
	// actually wait out real backoff delays.
	sleep  func(time.Duration)
	jitter func(base time.Duration) time.Duration
}

// NewAPIClient creates a new API client
func NewAPIClient(timeout time.Duration) *APIClient {
	return &APIClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout:            timeout,
		OpenLibraryBaseURL: defaultOpenLibraryBaseURL,
		GoogleBooksBaseURL: defaultGoogleBooksBaseURL,
		MaxRetries:         defaultMaxRetries,
		BaseBackoff:        defaultBaseBackoff,
		sleep:              time.Sleep,
		jitter:             defaultJitter,
	}
}

// Resolve fetches book metadata for an ISBN, trying each API tier in turn and
// returning a *ResolveError naming every tier's reason when none succeeds.
//
// The second return value names the tier that answered (APIOpenLibrary or
// APIGoogleBooks), and is empty when none did. Without it the fallback chain is
// invisible from the outside: two ISBNs that both "resolved" say nothing about
// whether the second tier is earning its keep, which is exactly the question
// specs/third-fallback-api.md §1's measurement has to answer before a third
// tier is worth building.
func (c *APIClient) Resolve(isbn string) (*BookMetadata, string, error) {
	resolveErr := &ResolveError{ISBN: isbn}

	// Try Open Library API first
	metadata, err := c.fetchFromOpenLibrary(isbn)
	if err == nil && metadata != nil {
		return metadata, APIOpenLibrary, nil
	}
	resolveErr.add(APIOpenLibrary, err)

	// Fallback to Google Books API
	metadata, err = c.fetchFromGoogleBooks(isbn)
	if err == nil && metadata != nil {
		return metadata, APIGoogleBooks, nil
	}
	resolveErr.add(APIGoogleBooks, err)

	return nil, "", resolveErr
}

// fetchFromOpenLibrary fetches book data from Open Library API
func (c *APIClient) fetchFromOpenLibrary(isbn string) (*BookMetadata, error) {
	apiURL := fmt.Sprintf("%s/api/books?bibkeys=ISBN:%s&format=json&jscmd=data", c.OpenLibraryBaseURL, isbn)

	resp, err := c.doWithRetry(APIOpenLibrary, isbn, func() (*http.Response, error) {
		return c.httpClient.Get(apiURL)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	key := "ISBN:" + isbn
	bookData, ok := result[key].(map[string]interface{})
	if !ok || bookData == nil {
		return nil, ErrNoData
	}

	metadata := &BookMetadata{
		ISBN: isbn,
	}

	// Extract title
	if title, ok := bookData["title"].(string); ok {
		metadata.Title = title
	}

	// Extract authors
	if authorsData, ok := bookData["authors"].([]interface{}); ok {
		for _, author := range authorsData {
			if authorMap, ok := author.(map[string]interface{}); ok {
				if name, ok := authorMap["name"].(string); ok {
					metadata.Authors = append(metadata.Authors, name)
				}
			}
		}
	}

	// Extract publishers
	if publishersData, ok := bookData["publishers"].([]interface{}); ok {
		if len(publishersData) > 0 {
			if publisher, ok := publishersData[0].(map[string]interface{}); ok {
				if name, ok := publisher["name"].(string); ok {
					metadata.Publisher = name
				}
			}
		}
	}

	// Extract publication date
	if pubDate, ok := bookData["publish_date"].(string); ok {
		metadata.PublicationDate = pubDate
	}

	// Extract number of pages
	if pages, ok := bookData["number_of_pages"].(float64); ok {
		metadata.Pages = int(pages)
	}

	// Extract subjects (categories)
	if subjectsData, ok := bookData["subjects"].([]interface{}); ok {
		for _, subject := range subjectsData {
			if subjectMap, ok := subject.(map[string]interface{}); ok {
				if name, ok := subjectMap["name"].(string); ok {
					metadata.Categories = append(metadata.Categories, name)
				}
			}
		}
	}

	return metadata, nil
}

// fetchFromGoogleBooks fetches book data from Google Books API.
//
// It is a thin wrapper whose only job is to make redaction unmissable: the
// request URL carries the API key, and net/http reports a transport failure as
// a *url.Error that prints that whole URL. Funnelling every error through one
// place means a future error path cannot leak the key by omission.
func (c *APIClient) fetchFromGoogleBooks(isbn string) (*BookMetadata, error) {
	metadata, err := c.fetchGoogleBooksVolume(isbn)
	if err != nil {
		return nil, c.redactAPIKey(err)
	}

	return metadata, nil
}

// googleBooksURL builds the volumes query, appending the API key only when one
// is configured so the anonymous request stays byte-identical to what the tool
// sent before key support existed.
func (c *APIClient) googleBooksURL(isbn string) string {
	apiURL := fmt.Sprintf("%s/volumes?q=isbn:%s", c.GoogleBooksBaseURL, url.QueryEscape(isbn))
	if c.GoogleBooksAPIKey != "" {
		apiURL += "&key=" + url.QueryEscape(c.GoogleBooksAPIKey)
	}

	return apiURL
}

// redactAPIKey rewrites err's message with the Google Books key masked out.
//
// When a redaction actually fires, the wrapping chain is deliberately dropped
// rather than preserved: re-wrapping the original would let the unredacted
// message resurface through Unwrap — precisely the leak this is here to close.
// Errors reach stderr via the verbose progress line and are persisted verbatim
// into the cache file, so a leak here is durable.
//
// An error whose message never carried the key is returned untouched, keeping
// its type: that is how a *StatusError survives to tell a 429 from a 404 in
// ResolveError. Flattening those would cost the distinction for no benefit,
// since there was nothing in them to hide.
func (c *APIClient) redactAPIKey(err error) error {
	if err == nil || c.GoogleBooksAPIKey == "" {
		return err
	}

	msg := err.Error()
	original := msg
	// The escaped form is what actually appears in the URL; the raw form is
	// what a caller-supplied message would carry. They are identical for a
	// typical Google key, so replacing both costs nothing and covers keys
	// with characters QueryEscape rewrites.
	for _, form := range []string{url.QueryEscape(c.GoogleBooksAPIKey), c.GoogleBooksAPIKey} {
		msg = strings.ReplaceAll(msg, form, redactedAPIKey)
	}

	if msg == original {
		return err
	}

	return errors.New(msg)
}

func (c *APIClient) fetchGoogleBooksVolume(isbn string) (*BookMetadata, error) {
	apiURL := c.googleBooksURL(isbn)

	resp, err := c.doWithRetry(APIGoogleBooks, isbn, func() (*http.Response, error) {
		return c.httpClient.Get(apiURL)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}

	var result struct {
		TotalItems int `json:"totalItems"`
		Items      []struct {
			VolumeInfo struct {
				Title               string   `json:"title"`
				Authors             []string `json:"authors"`
				Publisher           string   `json:"publisher"`
				PublishedDate       string   `json:"publishedDate"`
				PageCount           int      `json:"pageCount"`
				Language            string   `json:"language"`
				Categories          []string `json:"categories"`
				IndustryIdentifiers []struct {
					Type       string `json:"type"`
					Identifier string `json:"identifier"`
				} `json:"industryIdentifiers"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.TotalItems == 0 {
		return nil, ErrNoData
	}

	volumeInfo := result.Items[0].VolumeInfo

	metadata := &BookMetadata{
		ISBN:            isbn,
		Title:           volumeInfo.Title,
		Authors:         volumeInfo.Authors,
		Publisher:       volumeInfo.Publisher,
		PublicationDate: volumeInfo.PublishedDate,
		Pages:           volumeInfo.PageCount,
		Categories:      volumeInfo.Categories,
	}

	// Extract ISBN-10 and ISBN-13 from industry identifiers
	for _, id := range volumeInfo.IndustryIdentifiers {
		switch id.Type {
		case "ISBN_10":
			metadata.ISBN10 = id.Identifier
		case "ISBN_13":
			metadata.ISBN13 = id.Identifier
		}
	}

	return metadata, nil
}
