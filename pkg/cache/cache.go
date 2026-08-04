// Package cache implements the persistent local resolution cache described in
// specs/performance-caching.md. Repeat runs over a growing ISBN list should not
// re-resolve ISBNs that were already looked up, so every attempt — successful or
// not — is recorded here keyed by a canonical form of the ISBN.
package cache

import (
	"strings"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/isbn"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// Status records the outcome of a resolution attempt. Failures are cached too so
// known-bad ISBNs don't hammer the upstream APIs on every run; --retry-failed is
// the escape hatch for re-attempting them.
type Status string

const (
	// StatusSuccess means metadata was resolved and is reusable.
	StatusSuccess Status = "success"
	// StatusError means the attempt failed; Entry.Error holds the reason.
	StatusError Status = "error"
)

// Entry is one cached resolution attempt.
type Entry struct {
	Status      Status                 `json:"status"`
	Metadata    *resolver.BookMetadata `json:"metadata,omitempty"`
	Error       string                 `json:"error,omitempty"`
	LastAttempt time.Time              `json:"last_attempt"`
}

// Cache is the in-memory view of the cache file: entries keyed by Key.
//
// It is a struct rather than a bare map so the on-disk format can gain
// top-level fields later without breaking existing cache files.
type Cache struct {
	Entries map[string]Entry `json:"entries"`
}

// New returns an empty cache ready for use.
func New() *Cache {
	return &Cache{Entries: make(map[string]Entry)}
}

// Key derives the cache key for an ISBN. ISBN-13 is preferred so that the ISBN-10
// and ISBN-13 spellings of the same book share a single entry; anything that
// isn't a valid ISBN-10 falls back to the normalized input so callers never get
// an empty key.
func Key(input string) string {
	normalized := normalize(input)

	if result := isbn.Validate(normalized); result.Type == isbn.ISBN10 {
		if converted := isbn.ConvertISBN10to13(result.Normalized); converted != "" {
			return converted
		}
	}

	return normalized
}

// Get returns the entry for a key, if any.
func (c *Cache) Get(key string) (Entry, bool) {
	if c == nil || c.Entries == nil {
		return Entry{}, false
	}
	entry, ok := c.Entries[key]
	return entry, ok
}

// Set stores an entry, creating the backing map if the cache was zero-valued
// (e.g. decoded from a cache file with no entries object).
func (c *Cache) Set(key string, entry Entry) {
	if c.Entries == nil {
		c.Entries = make(map[string]Entry)
	}
	c.Entries[key] = entry
}

// Len reports how many entries the cache holds, for the verbose
// "Loaded cache: N entries" line.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.Entries)
}

// normalize strips the separators permitted in printed ISBNs and upper-cases the
// ISBN-10 check digit so "080442957x" and "0-8044-2957-X" agree on a key.
func normalize(input string) string {
	replacer := strings.NewReplacer("-", "", " ", "")
	return strings.ToUpper(replacer.Replace(strings.TrimSpace(input)))
}
