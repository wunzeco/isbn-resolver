package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

			cfg, err := parseArgs(t, args...).resolveConfig()
			if err != nil {
				t.Fatalf("resolveConfig() error = %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

// TestResolveConfigWithoutFile covers the no---config path, where the defaults
// must still show through and flags must still win over the environment.
func TestResolveConfigWithoutFile(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := parseArgs(t).resolveConfig()
		if err != nil {
			t.Fatalf("resolveConfig() error = %v", err)
		}

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
		cfg, err := parseArgs(t, "--format", "csv").resolveConfig()
		if err != nil {
			t.Fatalf("resolveConfig() error = %v", err)
		}

		if cfg.Format != output.FormatCSV {
			t.Errorf("format = %q, want csv", cfg.Format)
		}
	})
}

// TestResolveConfigEmptyCacheFileFallsBackToDefault covers a config file that
// sets "cache_file": "" explicitly. Without this fallback, cache.Load("")
// fails with an opaque "open : no such file or directory" and the run exits 1
// before doing any work, even though the file's only mistake is an empty
// string where the key could just as well have been omitted.
func TestResolveConfigEmptyCacheFileFallsBackToDefault(t *testing.T) {
	path := writeConfigFile(t, `{"cache_file": ""}`)
	cfg, err := parseArgs(t, "--config", path).resolveConfig()
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}

	if cfg.CacheFile != cache.DefaultFile {
		t.Errorf("cache_file = %q, want %q", cfg.CacheFile, cache.DefaultFile)
	}
}

// TestResolveConfigUnreadableFileFails makes an explicitly-passed --config
// that cannot be read a hard failure rather than a warning: the flag only
// exists because the user asked for that specific file, so falling back to
// defaults would silently run with the wrong settings instead of surfacing
// the typo'd path.
func TestResolveConfigUnreadableFileFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	cfg, err := parseArgs(t, "--config", missing, "--concurrency", "7").resolveConfig()

	if err == nil {
		t.Fatal("resolveConfig() error = nil, want an error for an unreadable --config file")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("resolveConfig() error = %q, want it to name the path %q", err.Error(), missing)
	}
	if cfg != nil {
		t.Errorf("resolveConfig() cfg = %+v, want nil on error", cfg)
	}
}

// TestResolveConfigValidation covers the seam main() relies on: Validate runs
// on the *merged* config, so a bad value in a config file is fatal only when no
// later layer has replaced it. Validating any single layer in isolation would
// either miss the flag that fixes it or reject a run that is actually fine.
func TestResolveConfigValidation(t *testing.T) {
	badFile := writeConfigFile(t, `{"concurrency": 0}`)

	t.Run("bad file value is rejected", func(t *testing.T) {
		cfg, err := parseArgs(t, "--config", badFile).resolveConfig()
		if err != nil {
			t.Fatalf("resolveConfig() error = %v", err)
		}

		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate() = nil for a config file with concurrency 0, want an error")
		}
	})

	t.Run("flag overrides the bad file value", func(t *testing.T) {
		cfg, err := parseArgs(t, "--config", badFile, "--concurrency", "4").resolveConfig()
		if err != nil {
			t.Fatalf("resolveConfig() error = %v", err)
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil once --concurrency overrides the file", err)
		}
	})

	t.Run("bad flag value is rejected", func(t *testing.T) {
		cfg, err := parseArgs(t, "--concurrency", "0").resolveConfig()
		if err != nil {
			t.Fatalf("resolveConfig() error = %v", err)
		}

		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate() = nil for --concurrency 0, want an error")
		}
	})
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
//
// resolveISBNs now dispatches misses across a worker pool, so Resolve can be
// called from multiple goroutines concurrently — the mutex is what keeps
// `go test -race` clean.
type countingResolver struct {
	mu     sync.Mutex
	calls  []string
	fail   map[string]bool
	titles map[string]string
}

func (r *countingResolver) Resolve(isbnStr string) (*resolver.BookMetadata, error) {
	r.mu.Lock()
	r.calls = append(r.calls, isbnStr)
	r.mu.Unlock()

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
	results, failures := resolveISBNs(4, isbns, client, store, policy, io.Discard)

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
		r, f := resolveISBNs(4, []string{"9780134190440", "9780134190440"}, client, store, policy, io.Discard)
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

// TestPrintSummaryIncludesDuration asserts the verbose "Summary"/"Duration"
// block (spec §"Expected Output (Verbose Mode)") is emitted in the right
// shape. It checks the Duration line's format rather than an exact value,
// since elapsed time is inherently non-deterministic.
func TestPrintSummaryIncludesDuration(t *testing.T) {
	var buf strings.Builder
	printSummary(&buf, 848, 4, 852, 9200*time.Millisecond)

	got := buf.String()

	wantSummary := "Summary: 848 successful, 4 failed out of 852 total"
	if !strings.Contains(got, wantSummary) {
		t.Errorf("printSummary() output = %q, want it to contain %q", got, wantSummary)
	}

	wantDuration := "Duration: 9.2s"
	if !strings.Contains(got, wantDuration) {
		t.Errorf("printSummary() output = %q, want it to contain %q", got, wantDuration)
	}

	// Duration must follow Summary, matching the spec's ordering.
	if strings.Index(got, wantSummary) > strings.Index(got, wantDuration) {
		t.Errorf("printSummary() output = %q, want Summary before Duration", got)
	}
}

// TestNewAPIClientAttachesARateLimiter pins the wiring that was missing:
// APIClient.Limiter was declared and consumed by doWithRetry, but never
// assigned outside tests. Because RateLimiter.Wait is a documented no-op on a
// nil receiver, that gap was silent — every real run issued unpaced traffic and
// looked fine until Google Books' anonymous quota ran out
// (specs/third-fallback-api.md §0).
func TestNewAPIClientAttachesARateLimiter(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RateLimit.MaxRetries = 7
	cfg.RateLimit.BaseBackoff = config.Duration(250 * time.Millisecond)

	client := newAPIClient(cfg)

	if client.Limiter == nil {
		t.Fatal("newAPIClient() left Limiter nil; every request would be unpaced")
	}
	if client.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d, want 7", client.MaxRetries)
	}
	if client.BaseBackoff != 250*time.Millisecond {
		t.Errorf("BaseBackoff = %v, want 250ms", client.BaseBackoff)
	}
}

// TestNewAPIClientLimiterPacesConcurrentResolution proves the limiter is
// actually consulted on the path the pool takes, not merely non-nil, and that
// one bucket governs all workers rather than one per worker.
//
// The assertion is a duration floor rather than an exact time: a bucket of
// `burst` tokens refilling at `rate`/s cannot release N requests in less than
// (N-burst)/rate seconds, no matter how fast the server answers. With a
// per-worker limiter the same run would finish in roughly a quarter of that,
// so the floor is what distinguishes shared pacing from per-worker pacing.
func TestNewAPIClientLimiterPacesConcurrentResolution(t *testing.T) {
	const (
		requests    = 8
		concurrency = 4
		rate        = 40.0
		burst       = 1
	)

	var mu sync.Mutex
	var served int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		served++
		mu.Unlock()

		bibkey := r.URL.Query().Get("bibkeys")
		fmt.Fprintf(w, `{%q: {"title": "Paced"}}`, bibkey)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.RateLimit.RequestsPerSecond = rate
	cfg.RateLimit.Burst = burst

	client := newAPIClient(cfg)
	client.OpenLibraryBaseURL = server.URL

	isbns := make([]string, requests)
	for i := range isbns {
		isbns[i] = fmt.Sprintf("978013419044%d", i)
	}

	store := cache.New()
	policy := cache.NewPolicy(store, cache.ModeNoCache)

	start := time.Now()
	results, failures := resolveISBNs(concurrency, isbns, client, store, policy, io.Discard)
	elapsed := time.Since(start)

	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none", failures)
	}
	for i, res := range results {
		if res.Title != "Paced" {
			t.Fatalf("results[%d].Title = %q, want %q", i, res.Title, "Paced")
		}
	}
	if served != requests {
		t.Fatalf("server saw %d requests, want %d", served, requests)
	}

	// Allow a small tolerance below the theoretical floor for timer coarseness;
	// the point is that pacing happened at all, not its precise magnitude.
	floor := time.Duration(float64(requests-burst) / rate * float64(time.Second))
	if min := floor - floor/10; elapsed < min {
		t.Errorf("resolving %d ISBNs at %g/s took %v, want at least %v — the shared limiter was not consulted",
			requests, rate, elapsed, min)
	}
}

// TestRetryWarnerFormatsSpecLine pins the exact message shape from
// specs/performance-caching.md §"Expected Output (Verbose Mode)". The line is
// the only cue that a stalled-looking run is actually sleeping off a backoff,
// so its content — which API, which ISBN, how long, which attempt — is the
// feature, not incidental formatting.
func TestRetryWarnerFormatsSpecLine(t *testing.T) {
	var buf strings.Builder
	warn := retryWarner(&buf)

	warn(resolver.RetryNotice{
		API:        resolver.APIOpenLibrary,
		ISBN:       "9780596520687",
		StatusCode: http.StatusTooManyRequests,
		Attempt:    1,
		MaxRetries: 3,
		// Jittered backoff: rounded to a tenth for display.
		Delay: 2100371842 * time.Nanosecond,
	})

	want := "Warning: rate limited by Open Library, retrying ISBN 9780596520687 in 2.1s (attempt 1/3)\n"
	if got := buf.String(); got != want {
		t.Errorf("retryWarner() wrote %q, want %q", got, want)
	}
}

// TestRetryWarnerIsConcurrencySafe exercises the callback the way the pool
// does — from many goroutines at once against one shared APIClient. Under
// -race this catches an unguarded writer, and the line-count assertion
// catches interleaved partial writes that a race detector alone would miss.
func TestRetryWarnerIsConcurrencySafe(t *testing.T) {
	var buf strings.Builder
	warn := retryWarner(&buf)

	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			warn(resolver.RetryNotice{
				API:        resolver.APIGoogleBooks,
				ISBN:       "9780134190440",
				Attempt:    1,
				MaxRetries: 3,
				Delay:      time.Second,
			})
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != workers {
		t.Fatalf("got %d lines, want %d — lines were interleaved or lost", len(lines), workers)
	}
	want := "Warning: rate limited by Google Books, retrying ISBN 9780134190440 in 1s (attempt 1/3)"
	for i, line := range lines {
		if line != want {
			t.Errorf("line[%d] = %q, want %q", i, line, want)
		}
	}
}

// TestNewAPIClientCarriesTheGoogleBooksAPIKey closes the last gap in the key's
// path: config can hold it and the client can send it, but neither matters if
// main forgets to hand it over. That is exactly how APIClient.Limiter stayed
// nil in production for so long, so it is worth a test of its own.
func TestNewAPIClientCarriesTheGoogleBooksAPIKey(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.GoogleBooksAPIKey = "configured-key"

	if got := newAPIClient(cfg).GoogleBooksAPIKey; got != "configured-key" {
		t.Errorf("GoogleBooksAPIKey = %q, want %q", got, "configured-key")
	}

	// The unset case is the default experience and must stay anonymous rather
	// than picking up a placeholder.
	if got := newAPIClient(config.DefaultConfig()).GoogleBooksAPIKey; got != "" {
		t.Errorf("GoogleBooksAPIKey = %q with none configured, want empty", got)
	}
}

// The flag exists for one-off runs, but it is the least private source, so the
// precedence merge has to honour it for the same reason it honours every other
// flag: it describes this invocation, while the environment describes all of
// them. Without an entry in cliFlags.apply it would be silently dropped
// whenever --config is also passed.
func TestGoogleBooksAPIKeyFlagOutranksTheEnvironment(t *testing.T) {
	t.Setenv("ISBN_GOOGLE_BOOKS_API_KEY", "env-key")

	cfg, err := parseArgs(t, "--google-books-api-key", "flag-key").resolveConfig()
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	if cfg.GoogleBooksAPIKey != "flag-key" {
		t.Errorf("GoogleBooksAPIKey = %q, want %q", cfg.GoogleBooksAPIKey, "flag-key")
	}

	// With no flag passed, the environment must still come through.
	cfg, err = parseArgs(t).resolveConfig()
	if err != nil {
		t.Fatalf("resolveConfig() error = %v", err)
	}
	if cfg.GoogleBooksAPIKey != "env-key" {
		t.Errorf("GoogleBooksAPIKey = %q, want %q", cfg.GoogleBooksAPIKey, "env-key")
	}
}
