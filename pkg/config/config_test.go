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
	// Pacing must be on by default: the whole point of the token bucket is to
	// avoid provoking a 429, which it cannot do if the shipped default is 0
	// ("unlimited").
	if cfg.RateLimit.RequestsPerSecond != 5 {
		t.Errorf("RateLimit.RequestsPerSecond = %g, want 5", cfg.RateLimit.RequestsPerSecond)
	}
	// Burst tracks the pool size so a full pool starts without waiting.
	if cfg.RateLimit.Burst != cfg.Concurrency {
		t.Errorf("RateLimit.Burst = %d, want it to match Concurrency %d",
			cfg.RateLimit.Burst, cfg.Concurrency)
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

// The sheet cache is opt-in: it costs an extra read call per run and assumes
// the output range still has the column layout the writer produced, so a user
// who never asked for it must never pay for it.
func TestDefaultConfigSheetCacheIsOff(t *testing.T) {
	if cfg := DefaultConfig(); cfg.SheetCache {
		t.Error("SheetCache = true by default, want false — it is opt-in")
	}
}

// The sheet cache is the feature CI runs turn on, and CI configures the tool
// with a checked-in config file rather than a command line, so the config key
// matters at least as much as the flag.
func TestLoadFromFileSheetCache(t *testing.T) {
	cfg, err := LoadFromFile(writeConfig(t, `{"sheet_cache": true}`))
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if !cfg.SheetCache {
		t.Error("SheetCache = false, want true from the config file")
	}
}

// --no-cache means "ignore both caches" (specs/deferred-cache-features.md §1).
// The combination that matters is a config file enabling the sheet cache and a
// command line disabling caching for one run: the run must make no cache
// assumptions at all, so the more specific instruction has to win.
func TestSheetCacheEnabled(t *testing.T) {
	tests := []struct {
		name       string
		sheetCache bool
		noCache    bool
		want       bool
	}{
		{name: "off by default", want: false},
		{name: "enabled on its own", sheetCache: true, want: true},
		{name: "no-cache disables it", sheetCache: true, noCache: true, want: false},
		{name: "no-cache alone changes nothing", noCache: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.SheetCache = tt.sheetCache
			cfg.NoCache = tt.noCache

			if got := cfg.SheetCacheEnabled(); got != tt.want {
				t.Errorf("SheetCacheEnabled() = %v, want %v (sheet_cache=%v, no_cache=%v)",
					got, tt.want, tt.sheetCache, tt.noCache)
			}
		})
	}
}

// --resolve-all and --retry-failed change how a cached entry is reused, not
// whether the cache is consulted, so neither may switch the sheet cache off —
// the sheet cache has to reach the policy for those modes to apply to it.
func TestSheetCacheEnabledIgnoresReusePolicyFlags(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "resolve_all", mutate: func(c *Config) { c.ResolveAll = true }},
		{name: "retry_failed", mutate: func(c *Config) { c.RetryFailed = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.SheetCache = true
			tt.mutate(cfg)

			if !cfg.SheetCacheEnabled() {
				t.Errorf("SheetCacheEnabled() = false with %s set, want true", tt.name)
			}
		})
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
		{
			// 0 is the documented opt-out: resolver.NewRateLimiter reads a
			// non-positive rate as "unlimited", so a config file must be able
			// to reach that state deliberately.
			name:   "zero requests_per_second means unlimited and is valid",
			mutate: func(c *Config) { c.RateLimit.RequestsPerSecond = 0 },
		},
		{
			name:   "burst of one is valid",
			mutate: func(c *Config) { c.RateLimit.Burst = 1 },
		},
		{
			name:      "negative requests_per_second",
			mutate:    func(c *Config) { c.RateLimit.RequestsPerSecond = -2.5 },
			wantError: "invalid rate_limit.requests_per_second -2.5: must not be negative",
		},
		{
			// NewRateLimiter would coerce this to 1, making the config file
			// look honoured when it was not.
			name:      "zero burst",
			mutate:    func(c *Config) { c.RateLimit.Burst = 0 },
			wantError: "invalid rate_limit.burst 0: must be at least 1",
		},
		{
			name:      "negative burst",
			mutate:    func(c *Config) { c.RateLimit.Burst = -4 },
			wantError: "invalid rate_limit.burst -4: must be at least 1",
		},
		{
			name:      "negative timeout",
			mutate:    func(c *Config) { c.Timeout = Duration(-time.Second) },
			wantError: "invalid timeout -1s: must be positive",
		},
		{
			// net/http treats a zero Timeout as "no timeout at all", which a
			// config file should never be able to request silently.
			name:      "zero timeout",
			mutate:    func(c *Config) { c.Timeout = 0 },
			wantError: "invalid timeout 0s: must be positive",
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

// The pacing knobs are what stand between a 489-ISBN run and an exhausted
// upstream quota, so a config file has to be able to set them — and setting one
// must not reset the other rate-limit fields, since the nested struct is
// unmarshalled over DefaultConfig field by field.
func TestLoadFromFileRateLimitRateAndBurst(t *testing.T) {
	tests := []struct {
		name           string
		contents       string
		wantRate       float64
		wantBurst      int
		wantMaxRetries int
	}{
		{
			name:           "both keys set",
			contents:       `{"rate_limit": {"requests_per_second": 2.5, "burst": 3}}`,
			wantRate:       2.5,
			wantBurst:      3,
			wantMaxRetries: DefaultMaxRetries,
		},
		{
			name:           "only requests_per_second set",
			contents:       `{"rate_limit": {"requests_per_second": 1}}`,
			wantRate:       1,
			wantBurst:      DefaultBurst,
			wantMaxRetries: DefaultMaxRetries,
		},
		{
			// Explicit opt-out of pacing; must survive the load as 0 rather
			// than being backfilled with the default.
			name:           "requests_per_second zero survives as unlimited",
			contents:       `{"rate_limit": {"requests_per_second": 0}}`,
			wantRate:       0,
			wantBurst:      DefaultBurst,
			wantMaxRetries: DefaultMaxRetries,
		},
		{
			name:           "keys omitted keep their defaults",
			contents:       `{"rate_limit": {"max_retries": 1}}`,
			wantRate:       DefaultRequestsPerSecond,
			wantBurst:      DefaultBurst,
			wantMaxRetries: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadFromFile(writeConfig(t, tt.contents))
			if err != nil {
				t.Fatalf("LoadFromFile failed: %v", err)
			}

			if cfg.RateLimit.RequestsPerSecond != tt.wantRate {
				t.Errorf("RateLimit.RequestsPerSecond = %g, want %g",
					cfg.RateLimit.RequestsPerSecond, tt.wantRate)
			}
			if cfg.RateLimit.Burst != tt.wantBurst {
				t.Errorf("RateLimit.Burst = %d, want %d", cfg.RateLimit.Burst, tt.wantBurst)
			}
			if cfg.RateLimit.MaxRetries != tt.wantMaxRetries {
				t.Errorf("RateLimit.MaxRetries = %d, want %d",
					cfg.RateLimit.MaxRetries, tt.wantMaxRetries)
			}
		})
	}
}

// A config the tool marshals must be one it can load back with the same pacing,
// so the two new keys have to survive the round trip under their documented
// names.
func TestRateLimitRateAndBurstMarshalRoundTrip(t *testing.T) {
	data, err := json.Marshal(DefaultConfig())
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw struct {
		RateLimit struct {
			RequestsPerSecond float64 `json:"requests_per_second"`
			Burst             int     `json:"burst"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if raw.RateLimit.RequestsPerSecond != DefaultRequestsPerSecond {
		t.Errorf("requests_per_second marshalled as %g, want %g",
			raw.RateLimit.RequestsPerSecond, DefaultRequestsPerSecond)
	}
	if raw.RateLimit.Burst != DefaultBurst {
		t.Errorf("burst marshalled as %d, want %d", raw.RateLimit.Burst, DefaultBurst)
	}

	reloaded, err := LoadFromFile(writeConfig(t, string(data)))
	if err != nil {
		t.Fatalf("LoadFromFile of a marshalled config failed: %v", err)
	}
	if reloaded.RateLimit != DefaultConfig().RateLimit {
		t.Errorf("RateLimit round trip = %+v, want %+v",
			reloaded.RateLimit, DefaultConfig().RateLimit)
	}
}

// Validate runs after the precedence merge, so a hand-edited config file is the
// path that can actually deliver a burst of 0 to the limiter.
func TestValidateRejectsConfigFileBurst(t *testing.T) {
	cfg, err := LoadFromFile(writeConfig(t, `{"rate_limit": {"burst": 0}}`))
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a config file with rate_limit.burst 0, want an error")
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

// The API key is a credential, so the environment variable is the source we
// most expect people to use — it keeps the key out of shell history, out of
// `ps`, and off disk. It must also stay optional: an unset variable leaves the
// anonymous behaviour the tool has always had.
func TestLoadFromEnvGoogleBooksAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		initial string
		want    string
	}{
		{
			name: "unset leaves the key empty",
			want: "",
		},
		{
			name: "set is adopted",
			env:  "env-key",
			want: "env-key",
		},
		{
			// Consistent with every other ISBN_* variable: an empty value is
			// treated as "unset" rather than as an explicit blank, so it cannot
			// silently wipe a key a config file supplied.
			name:    "empty does not clear a configured key",
			initial: "file-key",
			want:    "file-key",
		},
		{
			// Environment outranks the config file, matching the documented
			// precedence order (defaults < file < env < explicit flags).
			name:    "set overrides a config-file key",
			env:     "env-key",
			initial: "file-key",
			want:    "env-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ISBN_GOOGLE_BOOKS_API_KEY", tt.env)

			cfg := DefaultConfig()
			cfg.GoogleBooksAPIKey = tt.initial
			cfg.LoadFromEnv()

			if cfg.GoogleBooksAPIKey != tt.want {
				t.Errorf("GoogleBooksAPIKey = %q, want %q", cfg.GoogleBooksAPIKey, tt.want)
			}
		})
	}
}

// A config file is the third way to supply the key, for people who would rather
// keep it with the rest of their settings than export it every session.
func TestLoadFromFileGoogleBooksAPIKey(t *testing.T) {
	cfg, err := LoadFromFile(writeConfig(t, `{"google_books_api_key": "file-key"}`))
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if cfg.GoogleBooksAPIKey != "file-key" {
		t.Errorf("GoogleBooksAPIKey = %q, want %q", cfg.GoogleBooksAPIKey, "file-key")
	}

	// Omitting the key must not become an error or a non-empty placeholder:
	// running without a Google account has to stay the default experience.
	anon, err := LoadFromFile(writeConfig(t, `{"concurrency": 3}`))
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if anon.GoogleBooksAPIKey != "" {
		t.Errorf("GoogleBooksAPIKey = %q with the key omitted, want empty", anon.GoogleBooksAPIKey)
	}
	if err := anon.Validate(); err != nil {
		t.Errorf("Validate() rejected a config with no API key: %v", err)
	}
}
