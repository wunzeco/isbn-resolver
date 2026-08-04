package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultFile is the cache location used when neither --cache-file nor the
// cache_file config key is set. It is stored unexpanded so it can be printed
// back to the user in the form they'd recognise; ExpandPath resolves the ~.
const DefaultFile = "~/.isbn-resolver/cache.json"

// ExpandPath resolves a leading ~ to the current user's home directory. Config
// files and flags are written by humans, who expect ~ to work; os.Open does not
// expand it and would create a literal "~" directory instead.
func ExpandPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to expand %q: %w", path, err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// Load reads the cache file at path.
//
// A missing file is not an error: the first ever run has no cache, and treating
// that as a failure would make caching opt-in-by-accident. Corrupt JSON *is* an
// error — silently starting from empty would discard a large cache and quietly
// re-resolve everything, so the user should be told to delete or fix the file.
func Load(path string) (*Cache, error) {
	expanded, err := ExpandPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("unable to read cache file %s: %w", expanded, err)
	}

	// An empty (e.g. truncated-to-zero) file is treated like a missing one:
	// there is nothing to lose and nothing to report.
	if len(strings.TrimSpace(string(data))) == 0 {
		return New(), nil
	}

	cache := New()
	if err := json.Unmarshal(data, cache); err != nil {
		return nil, fmt.Errorf("unable to parse cache file %s: %w", expanded, err)
	}
	if cache.Entries == nil {
		cache.Entries = make(map[string]Entry)
	}

	return cache, nil
}

// Save writes the cache to path atomically: the JSON goes to a temp file in the
// same directory and is then renamed over the target. Rename within a directory
// is atomic, so a process killed mid-write (spec §1) leaves either the old cache
// or the new one — never a half-written file.
func (c *Cache) Save(path string) error {
	expanded, err := ExpandPath(path)
	if err != nil {
		return err
	}

	toWrite := c
	if toWrite == nil {
		toWrite = New()
	}

	// Marshal before touching the filesystem so an encoding failure can't leave
	// a stray temp file behind.
	data, err := json.MarshalIndent(toWrite, "", "  ")
	if err != nil {
		return fmt.Errorf("unable to encode cache: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(expanded)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("unable to create cache directory %s: %w", dir, err)
	}

	// The temp file must live in the destination directory: os.Rename cannot
	// move across filesystems, and /tmp is frequently a different mount.
	tmp, err := os.CreateTemp(dir, filepath.Base(expanded)+".tmp-*")
	if err != nil {
		return fmt.Errorf("unable to create temporary cache file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// From here on any failure must remove the temp file and leave the existing
	// cache untouched.
	cleanup := func(cause error) error {
		tmp.Close()
		os.Remove(tmpName)
		return cause
	}

	if err := tmp.Chmod(0600); err != nil {
		return cleanup(fmt.Errorf("unable to set permissions on %s: %w", tmpName, err))
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("unable to write cache file: %w", err))
	}
	// Flush to disk before the rename so a crash immediately after cannot leave
	// the renamed file empty.
	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("unable to flush cache file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("unable to close cache file: %w", err)
	}

	if err := os.Rename(tmpName, expanded); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("unable to replace cache file %s: %w", expanded, err)
	}

	return nil
}
