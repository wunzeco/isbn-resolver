package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/config"
	"github.com/wunzeco/isbn-resolver/pkg/output"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// parseArgs runs the real flag registration against a throwaway FlagSet so the
// precedence tests exercise exactly the flags main() registers, without
// touching the global flag.CommandLine or os.Args.
func parseArgs(t *testing.T, args ...string) *cliFlags {
	t.Helper()

	fs := flag.NewFlagSet("isbn-resolver", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	flags := registerFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	flags.harvest()

	return flags
}

// writeConfigFile writes a config JSON into a temp dir and returns its path.
func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	return path
}

// TestResolveConfigPrecedence pins the four-layer precedence order:
// defaults < config file < environment < explicitly-passed flags.
//
// This exists because the previous implementation replaced the whole config
// with the file's contents (`cfg = fileCfg`), silently discarding every flag
// the user passed alongside --config. That failure mode is invisible — the run
// succeeds, just with the wrong settings — so each layer boundary is asserted
// separately rather than in one end-to-end case.
func TestResolveConfigPrecedence(t *testing.T) {
	const fileContents = `{
	  "timeout": "45s",
	  "format": "json",
	  "concurrency": 9,
	  "cache_file": "/from/file.json"
	}`

	tests := []struct {
		name  string
		env   map[string]string
		args  []string
		check func(t *testing.T, cfg *config.Config)
	}{
		{
			// The regression itself: a flag passed next to --config survives.
			name: "explicit flag beats config file",
			args: []string{"--timeout", "5s"},
			check: func(t *testing.T, cfg *config.Config) {
				assertDuration(t, "timeout", cfg.Timeout, 5*time.Second)
			},
		},
		{
			// ...while the rest of the file is still honoured, so overriding
			// one key does not reset the others to their defaults.
			name: "unset flags leave config file values intact",
			args: []string{"--timeout", "5s"},
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.Concurrency != 9 {
					t.Errorf("concurrency = %d, want 9 (from file)", cfg.Concurrency)
				}
				if cfg.Format != output.FormatJSON {
					t.Errorf("format = %q, want json (from file)", cfg.Format)
				}
			},
		},
		{
			// A flag left off the command line must not push its default over
			// the file — this is the distinction fs.Visit draws.
			name: "flag default does not beat config file",
			check: func(t *testing.T, cfg *config.Config) {
				assertDuration(t, "timeout", cfg.Timeout, 45*time.Second)
				if cfg.CacheFile != "/from/file.json" {
					t.Errorf("cache_file = %q, want /from/file.json", cfg.CacheFile)
				}
			},
		},
		{
			name: "environment beats config file",
			env:  map[string]string{"ISBN_TIMEOUT": "20s", "ISBN_CONCURRENCY": "3"},
			check: func(t *testing.T, cfg *config.Config) {
				assertDuration(t, "timeout", cfg.Timeout, 20*time.Second)
				if cfg.Concurrency != 3 {
					t.Errorf("concurrency = %d, want 3 (from env)", cfg.Concurrency)
				}
			},
		},
		{
			// Flags describe one invocation; an exported variable describes
			// every invocation, so the flag wins.
			name: "explicit flag beats environment",
			env:  map[string]string{"ISBN_TIMEOUT": "20s", "ISBN_CACHE_FILE": "/from/env.json"},
			args: []string{"--timeout", "5s", "--cache-file", "/from/flag.json"},
			check: func(t *testing.T, cfg *config.Config) {
				assertDuration(t, "timeout", cfg.Timeout, 5*time.Second)
				if cfg.CacheFile != "/from/flag.json" {
					t.Errorf("cache_file = %q, want /from/flag.json", cfg.CacheFile)
				}
			},
		},
		{
			// Booleans are the easiest layer to get wrong: false is both a
			// legitimate value and the zero value.
			name: "explicit boolean flag beats config file",
			args: []string{"--verbose", "--no-cache"},
			check: func(t *testing.T, cfg *config.Config) {
				if !cfg.Verbose || !cfg.NoCache {
					t.Errorf("verbose = %v, no-cache = %v, want both true", cfg.Verbose, cfg.NoCache)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			path := writeConfigFile(t, fileContents)
			args := append([]string{"--config", path}, tt.args...)

			tt.check(t, parseArgs(t, args...).resolveConfig())
		})
	}
}

// TestResolveConfigWithoutFile covers the no---config path, where the defaults
// must still show through and flags must still win over the environment.
func TestResolveConfigWithoutFile(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg := parseArgs(t).resolveConfig()

		assertDuration(t, "timeout", cfg.Timeout, 30*time.Second)
		if cfg.Concurrency != config.DefaultConcurrency {
			t.Errorf("concurrency = %d, want %d", cfg.Concurrency, config.DefaultConcurrency)
		}
		if cfg.CacheFile != cache.DefaultFile {
			t.Errorf("cache_file = %q, want %q", cfg.CacheFile, cache.DefaultFile)
		}
		if cfg.Format != output.FormatText {
			t.Errorf("format = %q, want text", cfg.Format)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		t.Setenv("ISBN_FORMAT", "json")
		cfg := parseArgs(t, "--format", "csv").resolveConfig()

		if cfg.Format != output.FormatCSV {
			t.Errorf("format = %q, want csv", cfg.Format)
		}
	})
}

// TestResolveConfigUnreadableFileFallsBackToDefaults keeps an unreadable
// --config a warning rather than a hard failure, as it was before the
// precedence rework: the run continues on defaults plus whatever else was set.
func TestResolveConfigUnreadableFileFallsBackToDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	cfg := parseArgs(t, "--config", missing, "--concurrency", "7").resolveConfig()

	assertDuration(t, "timeout", cfg.Timeout, 30*time.Second)
	if cfg.Concurrency != 7 {
		t.Errorf("concurrency = %d, want 7 (from flag)", cfg.Concurrency)
	}
}

// TestEveryFlagHasAnOverride is the guard that keeps the precedence fix from
// rotting: a flag registered without a matching entry in cliFlags.apply is
// silently ignored whenever --config is passed, which is exactly the bug this
// mechanism replaced. Catching it here is far cheaper than in the field.
func TestEveryFlagHasAnOverride(t *testing.T) {
	flags := parseArgs(t)

	flags.fs.VisitAll(func(f *flag.Flag) {
		if _, ok := flags.apply[f.Name]; !ok {
			t.Errorf("flag --%s has no entry in cliFlags.apply, so it would be discarded when --config is used", f.Name)
		}
	})
}

// countingResolver stands in for the API client and records which ISBNs it was
// asked to resolve. The value of the cache is the calls it prevents, and a call
// that never happens is only observable here.
type countingResolver struct {
	calls  []string
	fail   map[string]bool
	titles map[string]string
}

func (r *countingResolver) Resolve(isbnStr string) (*resolver.BookMetadata, error) {
	r.calls = append(r.calls, isbnStr)

	if r.fail[isbnStr] {
		return nil, fmt.Errorf("upstream said no")
	}

	title := r.titles[isbnStr]
	if title == "" {
		title = "Title for " + isbnStr
	}

	return &resolver.BookMetadata{Title: title, Authors: []string{"An Author"}}, nil
}

// runOnce mimics one invocation of the tool over isbns: load the cache file,
// resolve, save it back. Going through the file (rather than reusing the
// in-memory cache) is the point — it is what makes the second call a genuine
// second run rather than a continuation of the first.
func runOnce(t *testing.T, path string, mode cache.Mode, client bookResolver, isbns []string) ([]resolver.BookMetadata, map[string]error, cache.Counters) {
	t.Helper()

	store := cache.New()
	if mode.Persists() {
		loaded, err := cache.Load(path)
		if err != nil {
			t.Fatalf("loading cache: %v", err)
		}
		store = loaded
	}

	policy := cache.NewPolicy(store, mode)
	results, failures := resolveISBNs(isbns, client, store, policy, io.Discard)

	if mode.Persists() {
		if err := store.Save(path); err != nil {
			t.Fatalf("saving cache: %v", err)
		}
	}

	return results, failures, policy.Counters()
}

// TestResolveISBNsSecondRunMakesNoNetworkCalls is the headline promise of the
// cache (spec §1 and the "Integration Tests" requirement): rerunning an
// unchanged list costs nothing and changes nothing.
func TestResolveISBNsSecondRunMakesNoNetworkCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	// A mix of spellings so the ISBN-10 input is keyed by its ISBN-13 form and
	// still round-trips to the same output row.
	isbns := []string{"9780134190440", "0596520689", "9780132350884"}

	client := &countingResolver{fail: map[string]bool{"0596520689": true}}

	first, firstErrs, firstCounters := runOnce(t, path, cache.ModeNormal, client, isbns)
	if len(client.calls) != len(isbns) {
		t.Fatalf("cold run made %d calls, want %d", len(client.calls), len(isbns))
	}
	if firstCounters.Misses != len(isbns) {
		t.Errorf("cold run counters = %+v, want %d misses", firstCounters, len(isbns))
	}

	client.calls = nil
	second, secondErrs, secondCounters := runOnce(t, path, cache.ModeNormal, client, isbns)

	if len(client.calls) != 0 {
		t.Errorf("warm run made %d calls (%v), want 0", len(client.calls), client.calls)
	}
	if secondCounters.Hits != len(isbns) {
		t.Errorf("warm run counters = %+v, want %d hits", secondCounters, len(isbns))
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("warm run results differ from cold run:\n cold: %+v\n warm: %+v", first, second)
	}

	// Failures must replay as failures, otherwise a cached error would surface
	// as an empty successful row on every subsequent run.
	if len(secondErrs) != len(firstErrs) {
		t.Fatalf("warm run errors = %v, want same keys as cold run %v", secondErrs, firstErrs)
	}
	for isbnStr, err := range firstErrs {
		got, ok := secondErrs[isbnStr]
		if !ok {
			t.Errorf("warm run lost the cached failure for %s", isbnStr)
			continue
		}
		if got.Error() != err.Error() {
			t.Errorf("warm run error for %s = %q, want %q", isbnStr, got, err)
		}
	}
}

// TestResolveISBNsModes checks that the loop actually honours the resolved
// cache.Mode. The policy owns the decision, but the loop owns whether the
// decision is consulted at all — this is the assertion that catches a loop that
// silently ignores it.
func TestResolveISBNsModes(t *testing.T) {
	isbns := []string{"9780134190440", "9780132350884"}
	failing := "9780132350884"

	tests := []struct {
		name      string
		mode      cache.Mode
		wantCalls []string
	}{
		{
			name:      "normal reuses successes and failures",
			mode:      cache.ModeNormal,
			wantCalls: nil,
		},
		{
			name:      "resolve-all ignores a warm cache",
			mode:      cache.ModeResolveAll,
			wantCalls: isbns,
		},
		{
			name:      "retry-failed re-attempts only the cached failure",
			mode:      cache.ModeRetryFailed,
			wantCalls: []string{failing},
		},
		{
			name:      "no-cache resolves everything without reading the file",
			mode:      cache.ModeNoCache,
			wantCalls: isbns,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache.json")
			client := &countingResolver{fail: map[string]bool{failing: true}}

			runOnce(t, path, cache.ModeNormal, client, isbns)
			client.calls = nil

			runOnce(t, path, tt.mode, client, isbns)

			if !reflect.DeepEqual(client.calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", client.calls, tt.wantCalls)
			}
		})
	}
}

// TestResolveISBNsNoCacheLeavesFileUntouched pins the write half of --no-cache:
// an ad hoc run must not pollute an existing cache, which is the whole reason
// the flag exists (spec §2).
func TestResolveISBNsNoCacheLeavesFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	client := &countingResolver{}

	runOnce(t, path, cache.ModeNormal, client, []string{"9780134190440"})

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading cache file: %v", err)
	}

	runOnce(t, path, cache.ModeNoCache, client, []string{"9780132350884"})

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading cache file: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("--no-cache modified the cache file:\n before: %s\n after: %s", before, after)
	}
}

// TestResolveISBNsCachesUnderTheISBN13Key guards the read-through against the
// keying rule in cache.Key: an ISBN-10 resolved on one run must be a hit when
// the same book arrives as an ISBN-13 on the next, and the output row must
// carry the spelling the caller supplied rather than the cache key.
func TestResolveISBNsCachesUnderTheISBN13Key(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	client := &countingResolver{}

	runOnce(t, path, cache.ModeNormal, client, []string{"0134190440"})
	client.calls = nil

	results, _, counters := runOnce(t, path, cache.ModeNormal, client, []string{"9780134190440"})

	if len(client.calls) != 0 {
		t.Errorf("ISBN-13 spelling made %d calls, want 0 (should hit the ISBN-10 entry)", len(client.calls))
	}
	if counters.Hits != 1 {
		t.Errorf("counters = %+v, want 1 hit", counters)
	}
	if results[0].ISBN != "9780134190440" {
		t.Errorf("result ISBN = %q, want the spelling this run supplied", results[0].ISBN)
	}
}

// TestResolveISBNsRepeatedInputResolvesOnce covers the reason results are
// recorded during the loop rather than after it: a list containing the same
// ISBN twice should still cost one network call.
func TestResolveISBNsRepeatedInputResolvesOnce(t *testing.T) {
	client := &countingResolver{}
	store := cache.New()

	results, _, _ := func() ([]resolver.BookMetadata, map[string]error, cache.Counters) {
		policy := cache.NewPolicy(store, cache.ModeNormal)
		r, f := resolveISBNs([]string{"9780134190440", "9780134190440"}, client, store, policy, io.Discard)
		return r, f, policy.Counters()
	}()

	if len(client.calls) != 1 {
		t.Errorf("calls = %v, want a single call for the duplicated ISBN", client.calls)
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		// The second row can only match if it came from the cached first.
		t.Errorf("duplicate rows differ: %+v vs %+v", results[0], results[1])
	}
}

func assertDuration(t *testing.T, name string, got config.Duration, want time.Duration) {
	t.Helper()

	if time.Duration(got) != want {
		t.Errorf("%s = %s, want %s", name, time.Duration(got), want)
	}
}

// TestResolveCacheMode covers every combination of the three cache-control
// settings, including the ones that can only be reached through a config file
// (the flags themselves are booleans a user can set in any mix). The whole
// point of collapsing them into one cache.Mode here is that the resolve loop
// never has to re-derive these combinations, so this is the only place the
// precedence between them is asserted.
func TestResolveCacheMode(t *testing.T) {
	tests := []struct {
		name        string
		resolveAll  bool
		retryFailed bool
		noCache     bool
		want        cache.Mode
		wantErr     bool
	}{
		{
			name: "no flags reuses successes and errors",
			want: cache.ModeNormal,
		},
		{
			name:       "resolve-all",
			resolveAll: true,
			want:       cache.ModeResolveAll,
		},
		{
			name:        "retry-failed",
			retryFailed: true,
			want:        cache.ModeRetryFailed,
		},
		{
			name:    "no-cache",
			noCache: true,
			want:    cache.ModeNoCache,
		},
		{
			// --no-cache disables reads and writes outright, which subsumes
			// any reuse policy --resolve-all would describe.
			name:       "no-cache wins over resolve-all",
			resolveAll: true,
			noCache:    true,
			want:       cache.ModeNoCache,
		},
		{
			name:        "no-cache wins over retry-failed",
			retryFailed: true,
			noCache:     true,
			want:        cache.ModeNoCache,
		},
		{
			name:        "resolve-all with retry-failed is rejected",
			resolveAll:  true,
			retryFailed: true,
			wantErr:     true,
		},
		{
			// Even though --no-cache makes both moot, the contradiction still
			// signals a wrong expectation, so it is reported rather than
			// swallowed.
			name:        "resolve-all with retry-failed is rejected even under no-cache",
			resolveAll:  true,
			retryFailed: true,
			noCache:     true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.ResolveAll = tt.resolveAll
			cfg.RetryFailed = tt.retryFailed
			cfg.NoCache = tt.noCache

			got, err := resolveCacheMode(cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveCacheMode() = %v, want error", got)
				}
				// The message must name both flags: it is the only thing the
				// user sees before the process exits 1.
				for _, want := range []string{"--resolve-all", "--retry-failed"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %s", err, want)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveCacheMode() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveCacheMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
