package cache

import (
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// Key is the join point between input ISBNs and cache entries, so a wrong key
// silently turns every cache hit into a miss. The ISBN-10/ISBN-13 cases matter
// most: the same book must map to one entry however the user spelled it.
func TestKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "isbn-10 is converted to isbn-13",
			input: "0306406152",
			want:  "9780306406157",
		},
		{
			name:  "isbn-10 with X check digit converts",
			input: "080442957X",
			want:  "9780804429573",
		},
		{
			name:  "lowercase x check digit converts identically",
			input: "080442957x",
			want:  "9780804429573",
		},
		{
			name:  "hyphenated isbn-10 converts",
			input: "0-306-40615-2",
			want:  "9780306406157",
		},
		{
			name:  "isbn-13 passes through unchanged",
			input: "9780134190440",
			want:  "9780134190440",
		},
		{
			name:  "hyphenated isbn-13 is normalized",
			input: "978-0-13-419044-0",
			want:  "9780134190440",
		},
		{
			name:  "spaces are stripped",
			input: " 978 0 13 419044 0 ",
			want:  "9780134190440",
		},
		{
			// A bad checksum still needs a stable, non-empty key so the failure
			// gets cached rather than colliding under "".
			name:  "invalid checksum falls back to normalized input",
			input: "978-0-13-419044-1",
			want:  "9780134190441",
		},
		{
			name:  "non-isbn input falls back to normalized input",
			input: "not-an-isbn",
			want:  "NOTANISBN",
		},
		{
			name:  "empty input yields empty key",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Key(tt.input); got != tt.want {
				t.Errorf("Key(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Both spellings of one book must land on the same entry — that is the whole
// point of preferring ISBN-13 as the key.
func TestKeyISBN10AndISBN13Agree(t *testing.T) {
	if got, want := Key("0306406152"), Key("9780306406157"); got != want {
		t.Errorf("ISBN-10 key %q != ISBN-13 key %q", got, want)
	}
}

func TestCacheSetGet(t *testing.T) {
	c := New()

	if _, ok := c.Get("9780306406157"); ok {
		t.Fatal("Get on an empty cache reported a hit")
	}

	attempt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	entry := Entry{
		Status:      StatusSuccess,
		Metadata:    &resolver.BookMetadata{ISBN: "9780306406157", Title: "Structure and Interpretation"},
		LastAttempt: attempt,
	}
	c.Set("9780306406157", entry)

	got, ok := c.Get("9780306406157")
	if !ok {
		t.Fatal("Get after Set reported a miss")
	}
	if got.Status != StatusSuccess {
		t.Errorf("Status = %q, want %q", got.Status, StatusSuccess)
	}
	if got.Metadata == nil || got.Metadata.Title != "Structure and Interpretation" {
		t.Errorf("Metadata = %+v, want the stored metadata", got.Metadata)
	}
	if !got.LastAttempt.Equal(attempt) {
		t.Errorf("LastAttempt = %v, want %v", got.LastAttempt, attempt)
	}

	if c.Len() != 1 {
		t.Errorf("Len() = %d, want 1", c.Len())
	}
}

// Error entries carry the reason so verbose output and --retry-failed can
// distinguish a known-bad ISBN from an unseen one.
func TestCacheStoresErrorEntries(t *testing.T) {
	c := New()
	c.Set("9780306406157", Entry{
		Status:      StatusError,
		Error:       "not found in any source",
		LastAttempt: time.Now(),
	})

	got, ok := c.Get("9780306406157")
	if !ok {
		t.Fatal("error entry not found")
	}
	if got.Status != StatusError || got.Error != "not found in any source" {
		t.Errorf("got %+v, want an error entry with the stored message", got)
	}
	if got.Metadata != nil {
		t.Errorf("Metadata = %+v, want nil for an error entry", got.Metadata)
	}
}

// A zero-valued Cache is what JSON decoding of a cache file without an "entries"
// object produces; it must not panic on use.
func TestZeroValueCacheIsUsable(t *testing.T) {
	var c Cache

	if _, ok := c.Get("9780306406157"); ok {
		t.Error("Get on a zero-value cache reported a hit")
	}
	if c.Len() != 0 {
		t.Errorf("Len() = %d, want 0", c.Len())
	}

	c.Set("9780306406157", Entry{Status: StatusSuccess})
	if _, ok := c.Get("9780306406157"); !ok {
		t.Error("Set on a zero-value cache did not store the entry")
	}
}

func TestNilCacheReads(t *testing.T) {
	var c *Cache

	if _, ok := c.Get("9780306406157"); ok {
		t.Error("Get on a nil cache reported a hit")
	}
	if c.Len() != 0 {
		t.Errorf("Len() = %d, want 0", c.Len())
	}
}
