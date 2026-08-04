package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/config"
	"github.com/wunzeco/isbn-resolver/pkg/output"
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
