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
	// DefaultRequestsPerSecond paces every outbound request across the whole
	// run. Both upstreams are free public APIs with undocumented per-IP
	// limits, and a 489-ISBN sample exhausted Google Books' anonymous quota
	// with no pacing at all (specs/third-fallback-api.md §0), so the default
	// is deliberately conservative: with the default pool of 5 workers this
	// is roughly one request per worker per second.
	DefaultRequestsPerSecond = 5.0
	// DefaultBurst is the token bucket's capacity. It matches
	// DefaultConcurrency so a full pool can start its first request without
	// waiting, while steady state is still governed by the rate above.
	DefaultBurst = DefaultConcurrency
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
	// RequestsPerSecond paces requests proactively, across all workers, so
	// the run avoids provoking a 429 rather than only reacting to one. It is
	// the rate half of the shared token bucket in pkg/resolver.
	//
	// Zero is a legal, reachable setting meaning "unlimited" — it is how a
	// user opts out of pacing entirely (e.g. when pointing the tool at a
	// local fixture server), and matches resolver.NewRateLimiter's own
	// treatment of a non-positive rate. Negative values are rejected by
	// Validate rather than silently folded into that meaning.
	RequestsPerSecond float64 `json:"requests_per_second"`
	// Burst is the token bucket's capacity: how many requests may be issued
	// back-to-back before RequestsPerSecond starts to bite.
	Burst int `json:"burst"`
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

	// SheetCache opts into treating the Google Sheets *output* range as a
	// second cache alongside the local file: a row already marked Success is
	// not re-resolved. It exists for ephemeral environments — a CI job has no
	// ~/.isbn-resolver/cache.json between runs, but the sheet it writes to
	// persists (specs/deferred-cache-features.md §1).
	//
	// Off by default because it is not free and not universally safe: it costs
	// an extra read call per run, and it assumes the output range carries the
	// column layout sheets.WriteResults writes. A range a user has since
	// reshaped by hand would be read as if it still had that shape.
	//
	// It is additive to the local cache rather than a replacement — see
	// SheetCacheEnabled for how --no-cache disables both.
	SheetCache bool `json:"sheet_cache"`

	// GoogleBooksAPIKey, when set, is sent as the `key` query parameter on
	// every Google Books request, moving the run off Google's shared
	// anonymous per-IP quota and onto a registered project's much higher one.
	// Exhausting the anonymous quota is what made 74 of a 489-ISBN sample look
	// like genuine catalog gaps (specs/third-fallback-api.md §0).
	//
	// It is optional by design: empty means today's anonymous behaviour, so
	// the tool never requires an account to run.
	GoogleBooksAPIKey string `json:"google_books_api_key"`
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
			MaxRetries:        DefaultMaxRetries,
			BaseBackoff:       Duration(DefaultBaseBackoff),
			RequestsPerSecond: DefaultRequestsPerSecond,
			Burst:             DefaultBurst,
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

// Validate reports the first setting that cannot produce a working run.
//
// It exists because LoadFromFile accepts anything that parses as JSON: a config
// file saying "concurrency": 0 starts a worker pool with no workers, so nothing
// is ever resolved and the run looks like an upstream outage rather than a
// typo. LoadFromEnv already ignores non-positive values, which leaves the
// config file and the flags as the paths that need checking.
//
// Call it after the precedence merge, so it judges the values the run will
// actually use rather than those of any single layer.
func (c *Config) Validate() error {
	if c.Concurrency < 1 {
		return fmt.Errorf("invalid concurrency %d: must be at least 1", c.Concurrency)
	}

	// Zero retries is a legitimate choice — fail fast on the first 429 — so
	// only a negative count, which no backoff loop can honour, is rejected.
	if c.RateLimit.MaxRetries < 0 {
		return fmt.Errorf("invalid rate_limit.max_retries %d: must not be negative", c.RateLimit.MaxRetries)
	}

	if c.RateLimit.BaseBackoff < 0 {
		return fmt.Errorf("invalid rate_limit.base_backoff %s: must not be negative",
			time.Duration(c.RateLimit.BaseBackoff))
	}

	// Zero deliberately stays legal here: resolver.NewRateLimiter reads a
	// non-positive rate as "unlimited", so a config file saying 0 is an
	// explicit opt-out of pacing. A negative rate has no such meaning — the
	// limiter's wait computation divides by the rate, so it would produce a
	// negative wait and pace nothing while looking like it configured
	// something.
	if c.RateLimit.RequestsPerSecond < 0 {
		return fmt.Errorf("invalid rate_limit.requests_per_second %g: must not be negative",
			c.RateLimit.RequestsPerSecond)
	}

	// NewRateLimiter silently coerces a burst below 1 up to 1, which would
	// make a config file's "burst": 0 look honoured when it was not. Reject
	// it here so the value the user wrote is the value the run uses.
	if c.RateLimit.Burst < 1 {
		return fmt.Errorf("invalid rate_limit.burst %d: must be at least 1", c.RateLimit.Burst)
	}

	// A negative timeout expires before the request is even sent, and zero
	// disables http.Client's deadline entirely (net/http's "no timeout"
	// sentinel) — neither is something a config file should be able to
	// request without a clear error naming the value.
	if c.Timeout <= 0 {
		return fmt.Errorf("invalid timeout %s: must be positive", time.Duration(c.Timeout))
	}

	return nil
}

// SheetCacheEnabled reports whether the run should consult the Sheets output
// range as a cache.
//
// It exists so the "--no-cache means ignore *both* caches"
// (specs/deferred-cache-features.md §1) rule lives next to the two fields it
// relates, rather than being re-derived at each place the sheet cache is
// consulted — the local cache's equivalent rule is already collapsed into a
// single cache.Mode for exactly that reason.
//
// --resolve-all and --retry-failed deliberately do not appear here: they change
// *how* a cached entry is reused, not whether the cache is read at all, and they
// apply to the sheet cache through the same cache.Mode the local cache uses.
func (c *Config) SheetCacheEnabled() bool {
	return c.SheetCache && !c.NoCache
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

	// An environment variable is the safest of the three sources for a
	// credential: unlike a flag it does not land in the shell history or in
	// `ps` output, and unlike a config file it need not be written to disk.
	if key := os.Getenv("ISBN_GOOGLE_BOOKS_API_KEY"); key != "" {
		c.GoogleBooksAPIKey = key
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
