package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/output"
)

// writeConfig writes cfg JSON to a temp file and returns its path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}
	return path
}

// The defaults are what every run that doesn't configure caching gets, so they
// are part of the tool's contract: a shared cache location and a worker count
// low enough not to trip the free public APIs' rate limits.
func TestDefaultConfigCachingAndConcurrency(t *testing.T) {
	cfg := DefaultConfig()

	// The default is stored unexpanded — cache.ExpandPath resolves the "~" at
	// load/save time, which keeps a config file portable across machines.
	if cfg.CacheFile != cache.DefaultFile {
		t.Errorf("CacheFile = %q, want %q", cfg.CacheFile, cache.DefaultFile)
	}
	if cfg.Concurrency != 5 {
		t.Errorf("Concurrency = %d, want 5", cfg.Concurrency)
	}
	if cfg.ResolveAll || cfg.RetryFailed || cfg.NoCache {
		t.Errorf("cache-control flags = (%v, %v, %v), want all false",
			cfg.ResolveAll, cfg.RetryFailed, cfg.NoCache)
	}
	if cfg.RateLimit.MaxRetries != 3 {
		t.Errorf("RateLimit.MaxRetries = %d, want 3", cfg.RateLimit.MaxRetries)
	}
	if time.Duration(cfg.RateLimit.BaseBackoff) != 500*time.Millisecond {
		t.Errorf("RateLimit.BaseBackoff = %v, want 500ms", time.Duration(cfg.RateLimit.BaseBackoff))
	}
}

// The caching spec publishes a config example; users will copy it verbatim, so
// it has to load with exactly the values it shows.
func TestLoadFromFileSpecExample(t *testing.T) {
	// Copied byte-for-byte from specs/performance-caching.md
	// §"Configuration File Example".
	path := writeConfig(t, `{
  "cache_file": "~/.isbn-resolver/cache.json",
  "concurrency": 5,
  "resolve_all": false,
  "retry_failed": false,
  "rate_limit": {
    "max_retries": 3,
    "base_backoff": "500ms"
  }
}`)

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg.CacheFile != "~/.isbn-resolver/cache.json" {
		t.Errorf("CacheFile = %q, want the unexpanded tilde path", cfg.CacheFile)
	}
	if cfg.Concurrency != 5 {
		t.Errorf("Concurrency = %d, want 5", cfg.Concurrency)
	}
	if cfg.ResolveAll || cfg.RetryFailed {
		t.Errorf("ResolveAll/RetryFailed = %v/%v, want false/false", cfg.ResolveAll, cfg.RetryFailed)
	}
	if cfg.RateLimit.MaxRetries != 3 {
		t.Errorf("RateLimit.MaxRetries = %d, want 3", cfg.RateLimit.MaxRetries)
	}
	if time.Duration(cfg.RateLimit.BaseBackoff) != 500*time.Millisecond {
		t.Errorf("RateLimit.BaseBackoff = %v, want 500ms", time.Duration(cfg.RateLimit.BaseBackoff))
	}

	// The example omits the Sheets keys and the timeout; those must keep their
	// defaults rather than being zeroed by the load.
	if time.Duration(cfg.Timeout) != 30*time.Second {
		t.Errorf("Timeout = %v, want the 30s default to survive a partial config", cfg.Timeout)
	}
}

// LoadFromFile unmarshals over DefaultConfig, which merges nested objects field
// by field. That is what lets a user override one rate-limit knob without
// having to restate the other — assert it, because a switch to a fresh Config
// would silently zero the omitted field.
func TestLoadFromFileRateLimitPartialOverride(t *testing.T) {
	tests := []struct {
		name            string
		contents        string
		wantMaxRetries  int
		wantBaseBackoff time.Duration
	}{
		{
			name:            "rate_limit omitted entirely",
			contents:        `{"concurrency": 10}`,
			wantMaxRetries:  3,
			wantBaseBackoff: 500 * time.Millisecond,
		},
		{
			name:            "only max_retries set",
			contents:        `{"rate_limit": {"max_retries": 7}}`,
			wantMaxRetries:  7,
			wantBaseBackoff: 500 * time.Millisecond,
		},
		{
			name:            "only base_backoff set",
			contents:        `{"rate_limit": {"base_backoff": "2s"}}`,
			wantMaxRetries:  3,
			wantBaseBackoff: 2 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadFromFile(writeConfig(t, tt.contents))
			if err != nil {
				t.Fatalf("LoadFromFile failed: %v", err)
			}

			if cfg.RateLimit.MaxRetries != tt.wantMaxRetries {
				t.Errorf("RateLimit.MaxRetries = %d, want %d", cfg.RateLimit.MaxRetries, tt.wantMaxRetries)
			}
			if got := time.Duration(cfg.RateLimit.BaseBackoff); got != tt.wantBaseBackoff {
				t.Errorf("RateLimit.BaseBackoff = %v, want %v", got, tt.wantBaseBackoff)
			}
		})
	}
}

// An unparseable base_backoff must fail loudly: silently falling back to the
// default would hide a typo that changes how hard the tool hits the APIs.
func TestLoadFromFileInvalidBaseBackoff(t *testing.T) {
	if _, err := LoadFromFile(writeConfig(t, `{"rate_limit": {"base_backoff": "soon"}}`)); err == nil {
		t.Fatal("LoadFromFile succeeded on an invalid base_backoff, want an error")
	}
}

// The cache-control flags are booleans in the config file as well as on the
// command line, so a config-only invocation can select a mode.
func TestLoadFromFileCacheControlFlags(t *testing.T) {
	cfg, err := LoadFromFile(writeConfig(t, `{
		"cache_file": "/tmp/isbn-cache.json",
		"retry_failed": true,
		"no_cache": true
	}`))
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg.CacheFile != "/tmp/isbn-cache.json" {
		t.Errorf("CacheFile = %q, want /tmp/isbn-cache.json", cfg.CacheFile)
	}
	if !cfg.RetryFailed || !cfg.NoCache || cfg.ResolveAll {
		t.Errorf("flags = resolve_all:%v retry_failed:%v no_cache:%v, want false/true/true",
			cfg.ResolveAll, cfg.RetryFailed, cfg.NoCache)
	}
}

// Validate is the only thing standing between a hand-edited config file and a
// run that quietly does nothing, so every rejected value needs a case here —
// including the boundary values that must stay legal (concurrency 1, zero
// retries, zero backoff), which are easy to over-reject.
func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		// mutate adjusts a default (therefore valid) config.
		mutate    func(*Config)
		wantError string
	}{
		{
			name:   "defaults are valid",
			mutate: func(*Config) {},
		},
		{
			name:   "single worker is valid",
			mutate: func(c *Config) { c.Concurrency = 1 },
		},
		{
			// Fail fast on the first 429 rather than retry — a choice, not a mistake.
			name:   "zero retries is valid",
			mutate: func(c *Config) { c.RateLimit.MaxRetries = 0 },
		},
		{
			name:   "zero backoff is valid",
			mutate: func(c *Config) { c.RateLimit.BaseBackoff = 0 },
		},
		{
			// The motivating bug: a pool of zero workers resolves nothing.
			name:      "zero concurrency",
			mutate:    func(c *Config) { c.Concurrency = 0 },
			wantError: "invalid concurrency 0: must be at least 1",
		},
		{
			name:      "negative concurrency",
			mutate:    func(c *Config) { c.Concurrency = -3 },
			wantError: "invalid concurrency -3: must be at least 1",
		},
		{
			name:      "negative max_retries",
			mutate:    func(c *Config) { c.RateLimit.MaxRetries = -1 },
			wantError: "invalid rate_limit.max_retries -1: must not be negative",
		},
		{
			name:      "negative base_backoff",
			mutate:    func(c *Config) { c.RateLimit.BaseBackoff = Duration(-time.Second) },
			wantError: "invalid rate_limit.base_backoff -1s: must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)

			err := cfg.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", tt.wantError)
			}
			// The message is the only thing the user sees before exit 1, so
			// it has to name both the setting and the offending value.
			if err.Error() != tt.wantError {
				t.Errorf("Validate() = %q, want %q", err, tt.wantError)
			}
		})
	}
}

// The file path is the gap Validate closes: LoadFromEnv already drops a
// non-positive ISBN_CONCURRENCY, but LoadFromFile takes any number that parses.
func TestValidateRejectsConfigFileConcurrency(t *testing.T) {
	cfg, err := LoadFromFile(writeConfig(t, `{"concurrency": 0}`))
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a config file with concurrency 0, want an error")
	}
}

// base_backoff must survive a save/load round trip in the same string form the
// spec documents, so a config the tool writes stays a config a human can read.
func TestRateLimitMarshalRoundTrip(t *testing.T) {
	data, err := json.Marshal(DefaultConfig())
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw struct {
		RateLimit struct {
			BaseBackoff string `json:"base_backoff"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if raw.RateLimit.BaseBackoff != "500ms" {
		t.Errorf("base_backoff marshalled as %q, want \"500ms\"", raw.RateLimit.BaseBackoff)
	}
}

func TestLoadFromEnvCachingAndConcurrency(t *testing.T) {
	tests := []struct {
		name            string
		cacheFile       string
		concurrency     string
		wantCacheFile   string
		wantConcurrency int
	}{
		{
			name:            "both unset keeps defaults",
			wantCacheFile:   cache.DefaultFile,
			wantConcurrency: 5,
		},
		{
			name:            "both set",
			cacheFile:       "/var/tmp/cache.json",
			concurrency:     "12",
			wantCacheFile:   "/var/tmp/cache.json",
			wantConcurrency: 12,
		},
		{
			// Ignored rather than fatal, matching ISBN_TIMEOUT's handling of
			// an unparseable value.
			name:            "non-numeric concurrency is ignored",
			concurrency:     "many",
			wantCacheFile:   cache.DefaultFile,
			wantConcurrency: 5,
		},
		{
			// Zero workers would resolve nothing at all, so it can never be
			// what the user meant.
			name:            "zero concurrency is ignored",
			concurrency:     "0",
			wantCacheFile:   cache.DefaultFile,
			wantConcurrency: 5,
		},
		{
			name:            "negative concurrency is ignored",
			concurrency:     "-4",
			wantCacheFile:   cache.DefaultFile,
			wantConcurrency: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ISBN_CACHE_FILE", tt.cacheFile)
			t.Setenv("ISBN_CONCURRENCY", tt.concurrency)

			cfg := DefaultConfig()
			cfg.LoadFromEnv()

			if cfg.CacheFile != tt.wantCacheFile {
				t.Errorf("CacheFile = %q, want %q", cfg.CacheFile, tt.wantCacheFile)
			}
			if cfg.Concurrency != tt.wantConcurrency {
				t.Errorf("Concurrency = %d, want %d", cfg.Concurrency, tt.wantConcurrency)
			}
		})
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temporary config file
	configContent := `{
		"timeout": "30s",
		"format": "json",
		"verbose": false
	}`

	tmpFile, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(configContent)); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	// Test loading the config
	cfg, err := LoadFromFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	// Verify the loaded config
	if time.Duration(cfg.Timeout) != 30*time.Second {
		t.Errorf("Expected timeout=30s, got %v", cfg.Timeout)
	}

	if cfg.Format != output.FormatJSON {
		t.Errorf("Expected format=json, got %s", cfg.Format)
	}

	if cfg.Verbose != false {
		t.Errorf("Expected verbose=false, got %v", cfg.Verbose)
	}
}

func TestLoadFromFileWithVariousTimeouts(t *testing.T) {
	tests := []struct {
		name        string
		timeoutStr  string
		expectedDur time.Duration
		shouldError bool
	}{
		{
			name:        "30 seconds",
			timeoutStr:  "30s",
			expectedDur: 30 * time.Second,
			shouldError: false,
		},
		{
			name:        "1 minute",
			timeoutStr:  "1m",
			expectedDur: 1 * time.Minute,
			shouldError: false,
		},
		{
			name:        "90 seconds",
			timeoutStr:  "90s",
			expectedDur: 90 * time.Second,
			shouldError: false,
		},
		{
			name:        "1 minute 30 seconds",
			timeoutStr:  "1m30s",
			expectedDur: 90 * time.Second,
			shouldError: false,
		},
		{
			name:        "invalid format",
			timeoutStr:  "invalid",
			expectedDur: 0,
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configContent := `{
				"timeout": "` + tt.timeoutStr + `",
				"format": "text",
				"verbose": false
			}`

			tmpFile, err := os.CreateTemp("", "config-*.json")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(tmpFile.Name())

			if _, err := tmpFile.Write([]byte(configContent)); err != nil {
				t.Fatalf("Failed to write to temp file: %v", err)
			}
			tmpFile.Close()

			cfg, err := LoadFromFile(tmpFile.Name())

			if tt.shouldError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("LoadFromFile failed: %v", err)
			}

			if time.Duration(cfg.Timeout) != tt.expectedDur {
				t.Errorf("Expected timeout=%v, got %v", tt.expectedDur, cfg.Timeout)
			}
		})
	}
}

func TestUnmarshalJSON(t *testing.T) {
	jsonData := `{
		"timeout": "45s",
		"format": "csv",
		"verbose": true,
		"sheets_url": "https://example.com/sheet",
		"sheets_range": "A1:A100"
	}`

	var cfg Config
	err := json.Unmarshal([]byte(jsonData), &cfg)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if time.Duration(cfg.Timeout) != 45*time.Second {
		t.Errorf("Expected timeout=45s, got %v", cfg.Timeout)
	}

	if cfg.Format != output.FormatCSV {
		t.Errorf("Expected format=csv, got %s", cfg.Format)
	}

	if cfg.Verbose != true {
		t.Errorf("Expected verbose=true, got %v", cfg.Verbose)
	}

	if cfg.SheetsURL != "https://example.com/sheet" {
		t.Errorf("Expected sheets_url, got %s", cfg.SheetsURL)
	}

	if cfg.SheetsRange != "A1:A100" {
		t.Errorf("Expected sheets_range, got %s", cfg.SheetsRange)
	}
}
