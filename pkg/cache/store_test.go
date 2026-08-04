package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// A cache that doesn't survive a round trip is worse than no cache at all: the
// second run would silently re-resolve everything while reporting hits.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")

	attempt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	original := New()
	original.Set("9780134190440", Entry{
		Status: StatusSuccess,
		Metadata: &resolver.BookMetadata{
			ISBN:            "9780134190440",
			ISBN13:          "9780134190440",
			Title:           "The Go Programming Language",
			Authors:         []string{"Alan A. A. Donovan", "Brian W. Kernighan"},
			Publisher:       "Addison-Wesley",
			PublicationDate: "2015",
			Pages:           380,
			Categories:      []string{"Computers"},
		},
		LastAttempt: attempt,
	})
	original.Set("9780306406157", Entry{
		Status:      StatusError,
		Error:       "not found in any source",
		LastAttempt: attempt,
	})

	if err := original.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", loaded.Len())
	}

	success, ok := loaded.Get("9780134190440")
	if !ok {
		t.Fatal("success entry missing after round trip")
	}
	if success.Status != StatusSuccess {
		t.Errorf("Status = %q, want %q", success.Status, StatusSuccess)
	}
	if success.Metadata == nil {
		t.Fatal("Metadata = nil, want the stored metadata")
	}
	if success.Metadata.Title != "The Go Programming Language" {
		t.Errorf("Title = %q, want %q", success.Metadata.Title, "The Go Programming Language")
	}
	// Slice fields are the easiest thing to lose to a bad struct tag, so assert
	// one explicitly rather than trusting the title alone.
	if len(success.Metadata.Authors) != 2 {
		t.Errorf("Authors = %v, want 2 entries", success.Metadata.Authors)
	}
	if success.Metadata.Pages != 380 {
		t.Errorf("Pages = %d, want 380", success.Metadata.Pages)
	}
	if !success.LastAttempt.Equal(attempt) {
		t.Errorf("LastAttempt = %v, want %v", success.LastAttempt, attempt)
	}

	failure, ok := loaded.Get("9780306406157")
	if !ok {
		t.Fatal("error entry missing after round trip")
	}
	if failure.Status != StatusError || failure.Error != "not found in any source" {
		t.Errorf("got %+v, want the stored error entry", failure)
	}
	if failure.Metadata != nil {
		t.Errorf("Metadata = %+v, want nil for an error entry", failure.Metadata)
	}
}

// The first ever run has no cache file. Treating that as an error would make
// every fresh install fail before resolving anything.
func TestLoadMissingFileReturnsEmptyCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for a missing file", err)
	}
	if c == nil {
		t.Fatal("Load() returned a nil cache")
	}
	if c.Len() != 0 {
		t.Errorf("Len() = %d, want 0", c.Len())
	}

	// The returned cache must be usable immediately — the caller writes misses
	// straight into it.
	c.Set("9780134190440", Entry{Status: StatusSuccess})
	if _, ok := c.Get("9780134190440"); !ok {
		t.Error("cache from a missing file is not writable")
	}
}

// Silently starting from empty on corrupt JSON would discard a large cache and
// re-resolve everything without telling the user why the run got slow.
func TestLoadCorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte(`{"entries": {"978013419`), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want an error for corrupt JSON")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the offending file %q", err, path)
	}
}

// A zero-length file is what a truncating writer leaves behind; there is nothing
// to salvage and nothing to warn about, so it behaves like a missing file.
func TestLoadEmptyFileReturnsEmptyCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for an empty file", err)
	}
	if c.Len() != 0 {
		t.Errorf("Len() = %d, want 0", c.Len())
	}
}

// A cache file whose JSON object omits "entries" decodes to a nil map; Load must
// normalise it so callers don't have to special-case it.
func TestLoadFileWithoutEntriesObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Entries == nil {
		t.Error("Entries = nil, want an allocated map")
	}
}

// Atomicity is the whole reason for the temp-file dance: a failed write must not
// destroy the cache the user already has. A read-only parent directory blocks
// the temp file creation while leaving the existing file readable.
func TestSaveLeavesExistingFileIntactWhenTempWriteFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not block file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	existing := New()
	existing.Set("9780134190440", Entry{
		Status:      StatusSuccess,
		Metadata:    &resolver.BookMetadata{ISBN: "9780134190440", Title: "The Go Programming Language"},
		LastAttempt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	})
	if err := existing.Save(path); err != nil {
		t.Fatalf("setup: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Restore write permission so t.TempDir cleanup can remove the directory.
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	updated := New()
	updated.Set("9780306406157", Entry{Status: StatusError, Error: "boom"})
	if err := updated.Save(path); err == nil {
		t.Fatal("Save() error = nil, want an error when the temp file cannot be created")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("existing cache file unreadable after a failed Save: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("existing cache file was modified by a failed Save:\nbefore: %s\nafter:  %s", before, after)
	}

	// And it must still parse — a clobbered-then-restored file is no good if the
	// next Load rejects it.
	os.Chmod(dir, 0700)
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() after a failed Save: %v", err)
	}
	if _, ok := reloaded.Get("9780134190440"); !ok {
		t.Error("original entry lost after a failed Save")
	}
}

// A failed Save must not litter the cache directory with temp files that
// accumulate across runs.
func TestSaveLeavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	c := New()
	c.Set("9780134190440", Entry{Status: StatusSuccess})
	if err := c.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "cache.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only cache.json", names)
	}
}

// The default cache path lives under ~/.isbn-resolver, which won't exist on a
// fresh machine — Save has to create it rather than failing.
func TestSaveCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "cache.json")

	c := New()
	c.Set("9780134190440", Entry{Status: StatusSuccess})
	if err := c.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
}

// Cache entries can include metadata pulled from a user's private sheet, so the
// file should not be world-readable.
func TestSaveUsesRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not meaningful on Windows")
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	if err := New().Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}

// Overwriting is the normal path — every run rewrites the cache — and the
// replacement must fully supersede the old contents rather than merging.
func TestSaveOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")

	first := New()
	first.Set("9780134190440", Entry{Status: StatusSuccess})
	if err := first.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	second := New()
	second.Set("9780306406157", Entry{Status: StatusError, Error: "boom"})
	if err := second.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := loaded.Get("9780134190440"); ok {
		t.Error("entry from the first Save survived the overwrite")
	}
	if _, ok := loaded.Get("9780306406157"); !ok {
		t.Error("entry from the second Save is missing")
	}
}

// A nil cache is what a --no-cache run holds; Save must not panic if it is
// called anyway.
func TestSaveNilCacheWritesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")

	var c *Cache
	if err := c.Save(path); err != nil {
		t.Fatalf("Save() on a nil cache: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Len() != 0 {
		t.Errorf("Len() = %d, want 0", loaded.Len())
	}
}

// The on-disk shape is a compatibility surface: users delete and inspect this
// file by hand, and future versions must be able to read today's.
func TestSaveProducesStableJSONShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")

	c := New()
	c.Set("9780134190440", Entry{
		Status:      StatusSuccess,
		Metadata:    &resolver.BookMetadata{ISBN: "9780134190440", Title: "The Go Programming Language"},
		LastAttempt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	})
	if err := c.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var raw struct {
		Entries map[string]struct {
			Status      string          `json:"status"`
			Metadata    json.RawMessage `json:"metadata"`
			Error       string          `json:"error"`
			LastAttempt string          `json:"last_attempt"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}

	entry, ok := raw.Entries["9780134190440"]
	if !ok {
		t.Fatalf("saved file has no entry for 9780134190440: %s", data)
	}
	if entry.Status != "success" {
		t.Errorf("status = %q, want %q", entry.Status, "success")
	}
	if entry.LastAttempt != "2026-08-04T12:00:00Z" {
		t.Errorf("last_attempt = %q, want RFC 3339 %q", entry.LastAttempt, "2026-08-04T12:00:00Z")
	}
	if len(entry.Metadata) == 0 {
		t.Error("metadata missing from the saved entry")
	}
}

// Read failures other than "not found" must surface. Pointing the cache at a
// directory is the easy reproduction of one, and is a plausible typo in
// --cache-file (e.g. passing ~/.isbn-resolver instead of the file inside it).
func TestLoadUnreadablePathErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := Load(dir); err == nil {
		t.Fatal("Load() error = nil, want an error when the path is a directory")
	}
}

// If the cache directory cannot be created the run must report it rather than
// silently dropping every write.
func TestSaveErrorsWhenParentIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := New().Save(filepath.Join(blocker, "cache.json")); err == nil {
		t.Fatal("Save() error = nil, want an error when the parent path is a file")
	}
}

// The rename is the last step and the one that can still fail after a fully
// written temp file; a directory sitting at the target path forces it.
func TestSaveErrorsWhenTargetIsADirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := New().Save(path); err == nil {
		t.Fatal("Save() error = nil, want an error when the target is a directory")
	}

	// The failed rename must still clean up its temp file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only the target directory (temp file leaked)", len(entries))
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "default cache path expands",
			in:   DefaultFile,
			want: filepath.Join(home, ".isbn-resolver", "cache.json"),
		},
		{
			name: "bare tilde is the home directory",
			in:   "~",
			want: home,
		},
		{
			name: "absolute path is untouched",
			in:   "/var/tmp/cache.json",
			want: "/var/tmp/cache.json",
		},
		{
			name: "relative path is untouched",
			in:   "cache.json",
			want: "cache.json",
		},
		{
			// Only a leading ~ that starts a path component is a home reference;
			// "~cache" is a legitimate (if odd) file name, not user "cache".
			name: "tilde inside a name is untouched",
			in:   "~cache.json",
			want: "~cache.json",
		},
		{
			name: "tilde mid-path is untouched",
			in:   "/tmp/~/cache.json",
			want: "/tmp/~/cache.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPath(tt.in)
			if err != nil {
				t.Fatalf("ExpandPath(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Load and Save must agree on expansion, or a saved cache would be invisible to
// the next run.
func TestLoadAndSaveAgreeOnTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	const path = "~/.isbn-resolver/cache.json"

	c := New()
	c.Set("9780134190440", Entry{Status: StatusSuccess})
	if err := c.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".isbn-resolver", "cache.json")); err != nil {
		t.Fatalf("cache not written to the expanded path: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := loaded.Get("9780134190440"); !ok {
		t.Error("entry missing after a tilde-path round trip")
	}
}
