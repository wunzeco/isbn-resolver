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

// Resolve fetches book metadata for an ISBN
func (c *APIClient) Resolve(isbn string) (*BookMetadata, error) {
	// Try Open Library API first
	metadata, err := c.fetchFromOpenLibrary(isbn)
	if err == nil && metadata != nil {
		return metadata, nil
	}

	// Fallback to Google Books API
	metadata, err = c.fetchFromGoogleBooks(isbn)
	if err == nil && metadata != nil {
		return metadata, nil
	}

	return nil, fmt.Errorf("failed to resolve ISBN from all APIs")
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
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	key := "ISBN:" + isbn
	bookData, ok := result[key].(map[string]interface{})
	if !ok || bookData == nil {
		return nil, fmt.Errorf("no data found for ISBN")
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
// The wrapping chain is deliberately dropped rather than preserved: errors.Is
// and errors.As are unused on this path, and re-wrapping the original would let
// the unredacted message resurface through Unwrap — which is precisely the leak
// this is here to close. Errors reach stderr via the verbose progress line and
// are persisted verbatim into the cache file, so a leak here is durable.
func (c *APIClient) redactAPIKey(err error) error {
	if err == nil || c.GoogleBooksAPIKey == "" {
		return err
	}

	msg := err.Error()
	// The escaped form is what actually appears in the URL; the raw form is
	// what a caller-supplied message would carry. They are identical for a
	// typical Google key, so replacing both costs nothing and covers keys
	// with characters QueryEscape rewrites.
	for _, form := range []string{url.QueryEscape(c.GoogleBooksAPIKey), c.GoogleBooksAPIKey} {
		msg = strings.ReplaceAll(msg, form, redactedAPIKey)
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
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
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
		return nil, fmt.Errorf("no data found for ISBN")
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
