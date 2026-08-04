package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/output"
)

// Defaults for the caching and concurrency settings. They live as constants so
// the CLI can print them in --help text without duplicating the literals.
const (
	// DefaultConcurrency is the worker-pool size for cache-miss resolution.
	DefaultConcurrency = 5
	// DefaultMaxRetries is how many retries a 429/503 response earns.
	DefaultMaxRetries = 3
	// DefaultBaseBackoff seeds the exponential backoff between retries.
	DefaultBaseBackoff = 500 * time.Millisecond
)

// Duration is a custom type that handles JSON unmarshaling of duration strings
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler interface
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	switch value := v.(type) {
	case string:
		// Parse string as duration (e.g., "30s", "1m", "1h30m")
		dur, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration format: %w", err)
		}
		*d = Duration(dur)
		return nil
	case float64:
		// Handle numeric value as nanoseconds
		*d = Duration(time.Duration(value))
		return nil
	default:
		return fmt.Errorf("invalid duration type: %T", value)
	}
}

// MarshalJSON implements json.Marshaler interface
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// RateLimit holds the retry/backoff settings applied when an upstream API
// answers with 429 or 503. Both APIs are free public services, so backing off
// politely is the only way to stay within their limits.
type RateLimit struct {
	// MaxRetries is how many extra attempts a rate-limited request gets
	// before the ISBN is failed or handed to the fallback source.
	MaxRetries int `json:"max_retries"`
	// BaseBackoff is the first backoff interval; subsequent attempts grow
	// exponentially from it (a Retry-After header, when present, wins).
	BaseBackoff Duration `json:"base_backoff"`
}

// Config holds the application configuration
type Config struct {
	Timeout    Duration      `json:"timeout"`
	Format     output.Format `json:"format"`
	Verbose    bool          `json:"verbose"`
	InputFile  string        `json:"input_file"`
	ConfigFile string        `json:"config_file"`

	// Google Sheets configuration
	SheetsURL         string `json:"sheets_url"`
	SheetsID          string `json:"sheets_id"`
	SheetsRange       string `json:"sheets_range"`
	SheetsCredentials string `json:"sheets_credentials"`
	SheetsOutputRange string `json:"sheets_output_range"`
	SheetsCreateTab   string `json:"sheets_create_tab"`
	SheetsDryRun      bool   `json:"sheets_dry_run"`

	// Caching and concurrency configuration
	//
	// CacheFile may keep a leading "~"; cache.ExpandPath resolves it at load
	// and save time, so the configured value stays portable between machines.
	CacheFile string `json:"cache_file"`
	// Concurrency bounds the worker pool. It is deliberately small by
	// default: this talks to two free public APIs, not infrastructure we own.
	Concurrency int `json:"concurrency"`
	// ResolveAll, RetryFailed and NoCache are the cache-control modes.
	// ResolveAll and RetryFailed are mutually exclusive; NoCache is
	// orthogonal and makes both moot. The CLI resolves them into a
	// single cache.Mode.
	ResolveAll  bool      `json:"resolve_all"`
	RetryFailed bool      `json:"retry_failed"`
	NoCache     bool      `json:"no_cache"`
	RateLimit   RateLimit `json:"rate_limit"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		Timeout:     Duration(30 * time.Second),
		Format:      output.FormatText,
		Verbose:     false,
		CacheFile:   cache.DefaultFile,
		Concurrency: DefaultConcurrency,
		RateLimit: RateLimit{
			MaxRetries:  DefaultMaxRetries,
			BaseBackoff: Duration(DefaultBaseBackoff),
		},
	}
}

// LoadFromFile loads configuration from a JSON file
func LoadFromFile(filename string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFromEnv loads configuration from environment variables
func (c *Config) LoadFromEnv() {
	if timeout := os.Getenv("ISBN_TIMEOUT"); timeout != "" {
		if d, err := time.ParseDuration(timeout); err == nil {
			c.Timeout = Duration(d)
		}
	}

	if format := os.Getenv("ISBN_FORMAT"); format != "" {
		c.Format = output.Format(format)
	}

	if verbose := os.Getenv("ISBN_VERBOSE"); verbose == "true" {
		c.Verbose = true
	}

	if cacheFile := os.Getenv("ISBN_CACHE_FILE"); cacheFile != "" {
		c.CacheFile = cacheFile
	}

	// A non-numeric or non-positive worker count is ignored rather than
	// fatal, matching how ISBN_TIMEOUT treats an unparseable value: a stray
	// environment variable shouldn't stop a run, and zero workers would mean
	// nothing ever gets resolved.
	if concurrency := os.Getenv("ISBN_CONCURRENCY"); concurrency != "" {
		if n, err := strconv.Atoi(concurrency); err == nil && n > 0 {
			c.Concurrency = n
		}
	}
}
