package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/config"
	"github.com/wunzeco/isbn-resolver/pkg/isbn"
	"github.com/wunzeco/isbn-resolver/pkg/output"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
	"github.com/wunzeco/isbn-resolver/pkg/sheets"
)

func main() {
	start := time.Now()

	flags := registerFlags(flag.CommandLine)
	flag.Parse()
	flags.harvest()

	cfg, err := flags.resolveConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Reject unusable values before any work starts. A config file is free to
	// say "concurrency": 0, and nothing downstream would complain — the run
	// would simply resolve nothing and report every ISBN as missing.
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Resolve the cache-control settings into a single mode before doing any
	// work, so a contradictory invocation fails immediately rather than after
	// authenticating with Google Sheets or reading a large input file.
	cacheMode, err := resolveCacheMode(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Get ISBNs from various sources
	isbns, err := getISBNs(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(isbns) == 0 {
		fmt.Fprintln(os.Stderr, "Error: No ISBNs provided")
		flag.Usage()
		os.Exit(1)
	}

	// Validate ISBNs
	validISBNs := make([]string, 0, len(isbns))
	for _, isbnStr := range isbns {
		result := isbn.Validate(isbnStr)
		if result.Type == isbn.Invalid {
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "Invalid ISBN '%s': %s\n", isbnStr, result.Error)
			}
			continue
		}
		validISBNs = append(validISBNs, result.Normalized)
	}

	if len(validISBNs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: No valid ISBNs to process")
		os.Exit(1)
	}

	// Load the cache before any network work. A corrupt cache file is fatal
	// rather than a warning: starting from empty would silently re-resolve
	// everything and then overwrite the file the user still has a chance to
	// repair.
	bookCache := cache.New()
	if cacheMode.Persists() {
		loaded, err := cache.Load(cfg.CacheFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		bookCache = loaded
	}

	// A single coherent cache header, rather than a separate "Cache mode" line
	// printed before the cache is even loaded: which fields are meaningful
	// depends on whether the mode persists a cache file at all.
	if cfg.Verbose {
		if cacheMode.Persists() {
			fmt.Fprintf(os.Stderr, "Loaded cache: %d entries (%s, mode=%s)\n", bookCache.Len(), cfg.CacheFile, cacheMode)
		} else {
			fmt.Fprintf(os.Stderr, "Cache disabled (mode=%s)\n", cacheMode)
		}
		fmt.Fprintf(os.Stderr, "Processing %d valid ISBN(s) with %d worker(s)...\n", len(validISBNs), cfg.Concurrency)
	}

	progress := io.Discard
	if cfg.Verbose {
		progress = os.Stderr
	}

	// Create API client
	client := newAPIClient(cfg)
	client.OnRetry = retryWarner(progress)

	// The policy reads from a view that may include the sheet cache; the run
	// still *writes* only to bookCache. See sheetCacheLookup.
	policy := cache.NewPolicy(sheetCacheLookup(cfg, bookCache, os.Stderr), cacheMode)
	results, errors := resolveISBNs(cfg.Concurrency, validISBNs, client, bookCache, policy, progress)

	if cfg.Verbose {
		counters := policy.Counters()
		fmt.Fprintf(os.Stderr, "Cache: %d hit, %d miss, %d retried\n", counters.Hits, counters.Misses, counters.Retried)
	}

	// Write through before producing any output, so the cache is up to date
	// even on the Google Sheets path (which returns early) and so a failure to
	// write is reported before the results scroll past. A save failure is not
	// fatal: the results in hand are still correct and worth emitting.
	if cacheMode.Persists() {
		if err := bookCache.Save(cfg.CacheFile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save cache: %v\n", err)
		}
	}

	// Write to Google Sheets if configured
	if cfg.SheetsURL != "" || cfg.SheetsID != "" {
		if err := writeToSheets(cfg, results, errors); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to Google Sheets: %v\n", err)
			os.Exit(1)
		}

		if cfg.Verbose {
			printSummary(os.Stderr, len(validISBNs)-len(errors), len(errors), len(validISBNs), time.Since(start))
		}
		return
	}

	// Format and output results
	formatter := output.NewFormatter(cfg.Format, os.Stdout)

	if cfg.Format == output.FormatText {
		// For text format, output each result as it's processed
		for _, metadata := range results {
			err := errors[metadata.ISBN]
			if formatErr := formatter.FormatResult(&metadata, err); formatErr != nil {
				fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", formatErr)
			}
		}
	} else {
		// For JSON and CSV, output all results at once
		if err := formatter.FormatBatch(results, errors); err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			os.Exit(1)
		}
	}

	// Print summary in verbose mode
	if cfg.Verbose {
		printSummary(os.Stderr, len(validISBNs)-len(errors), len(errors), len(validISBNs), time.Since(start))
	}
}

// printSummary writes the verbose-mode "Summary: ..." / "Duration: ..." block
// (spec §"Expected Output (Verbose Mode)") to w. Both the Google Sheets output
// path and the stdout formatting path print an identical block, so it lives
// here once rather than twice in main.
func printSummary(w io.Writer, successful, failed, total int, elapsed time.Duration) {
	fmt.Fprintf(w, "\nSummary: %d successful, %d failed out of %d total\n", successful, failed, total)
	fmt.Fprintf(w, "Duration: %s\n", elapsed)
}

// cliFlags owns the flag-bound values and knows which of them the user actually
// typed.
//
// The values land in their own Config rather than in the one the program runs
// on, because a flag's *default* must not outrank a config file or an
// environment variable — only a flag the user passed may. Binding straight to
// the live config made that distinction impossible to draw, which is how
// `--config` came to discard every other flag on the command line.
type cliFlags struct {
	fs  *flag.FlagSet
	cfg *config.Config

	// timeout and format cannot bind directly to their Config fields because
	// flag has no accessor for the named types; harvest folds them in.
	timeout time.Duration
	format  string

	// apply maps a flag name to the assignment that copies its value onto a
	// destination config. Every registered flag needs an entry — see
	// TestEveryFlagHasAnOverride, which fails the build rather than letting a
	// new flag silently become unsettable whenever --config is used.
	apply map[string]func(dst *config.Config)
}

// registerFlags declares every command-line flag on fs. Defaults come from
// DefaultConfig so --help and the config defaults cannot drift apart.
func registerFlags(fs *flag.FlagSet) *cliFlags {
	c := &cliFlags{fs: fs, cfg: config.DefaultConfig()}
	cfg := c.cfg

	c.timeout = time.Duration(cfg.Timeout)
	fs.DurationVar(&c.timeout, "timeout", c.timeout, "API request timeout")
	fs.StringVar(&cfg.InputFile, "file", "", "Input file containing ISBNs (one per line)")
	fs.StringVar(&c.format, "format", string(cfg.Format), "Output format: text, json, csv")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "Enable verbose output")
	fs.StringVar(&cfg.ConfigFile, "config", "", "Configuration file path")

	// Google Sheets flags
	fs.StringVar(&cfg.SheetsURL, "sheets-url", "", "Google Sheets URL")
	fs.StringVar(&cfg.SheetsID, "sheets-id", "", "Google Sheets ID")
	fs.StringVar(&cfg.SheetsRange, "sheets-range", "", "Cell range for ISBNs (e.g., 'Sheet1!A2:A')")
	fs.StringVar(&cfg.SheetsCredentials, "sheets-credentials", "", "Path to Google Sheets credentials file")
	fs.StringVar(&cfg.SheetsOutputRange, "sheets-output-range", "", "Where to write results")
	fs.StringVar(&cfg.SheetsCreateTab, "sheets-create-tab", "", "Create new tab for results")
	fs.BoolVar(&cfg.SheetsDryRun, "sheets-dry-run", false, "Preview changes without writing")

	// Caching and concurrency flags
	fs.StringVar(&cfg.CacheFile, "cache-file", cfg.CacheFile, "Resolution cache file path")
	fs.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "Number of concurrent resolution workers")
	fs.BoolVar(&cfg.ResolveAll, "resolve-all", cfg.ResolveAll, "Ignore cached entries and re-resolve every ISBN")
	fs.BoolVar(&cfg.RetryFailed, "retry-failed", cfg.RetryFailed, "Reuse cached successes but re-attempt cached failures")
	fs.BoolVar(&cfg.NoCache, "no-cache", cfg.NoCache, "Bypass the cache entirely for this run")
	fs.BoolVar(&cfg.SheetCache, "sheet-cache", cfg.SheetCache,
		"Treat Success rows already in the Sheets output range as cache hits (implies --sheets-output-range's column layout; disabled by --no-cache)")

	// Optional Google Books credential. The flag is the least private of the
	// three sources — it lands in shell history and in `ps` output — so its
	// help text points at ISBN_GOOGLE_BOOKS_API_KEY for anything but a
	// one-off. Unset means query anonymously, as the tool always has.
	fs.StringVar(&cfg.GoogleBooksAPIKey, "google-books-api-key", cfg.GoogleBooksAPIKey,
		"Google Books API key (optional; prefer ISBN_GOOGLE_BOOKS_API_KEY, which avoids shell history and ps)")

	c.apply = map[string]func(*config.Config){
		"timeout":            func(dst *config.Config) { dst.Timeout = cfg.Timeout },
		"file":               func(dst *config.Config) { dst.InputFile = cfg.InputFile },
		"format":             func(dst *config.Config) { dst.Format = cfg.Format },
		"verbose":            func(dst *config.Config) { dst.Verbose = cfg.Verbose },
		"config":             func(dst *config.Config) { dst.ConfigFile = cfg.ConfigFile },
		"sheets-url":         func(dst *config.Config) { dst.SheetsURL = cfg.SheetsURL },
		"sheets-id":          func(dst *config.Config) { dst.SheetsID = cfg.SheetsID },
		"sheets-range":       func(dst *config.Config) { dst.SheetsRange = cfg.SheetsRange },
		"sheets-credentials": func(dst *config.Config) { dst.SheetsCredentials = cfg.SheetsCredentials },
		"sheets-output-range": func(dst *config.Config) {
			dst.SheetsOutputRange = cfg.SheetsOutputRange
		},
		"sheets-create-tab": func(dst *config.Config) { dst.SheetsCreateTab = cfg.SheetsCreateTab },
		"sheets-dry-run":    func(dst *config.Config) { dst.SheetsDryRun = cfg.SheetsDryRun },
		"cache-file":        func(dst *config.Config) { dst.CacheFile = cfg.CacheFile },
		"concurrency":       func(dst *config.Config) { dst.Concurrency = cfg.Concurrency },
		"resolve-all":       func(dst *config.Config) { dst.ResolveAll = cfg.ResolveAll },
		"retry-failed":      func(dst *config.Config) { dst.RetryFailed = cfg.RetryFailed },
		"no-cache":          func(dst *config.Config) { dst.NoCache = cfg.NoCache },
		"sheet-cache":       func(dst *config.Config) { dst.SheetCache = cfg.SheetCache },
		"google-books-api-key": func(dst *config.Config) {
			dst.GoogleBooksAPIKey = cfg.GoogleBooksAPIKey
		},
	}

	return c
}

// harvest folds the two flag values that could not bind directly to their
// Config fields into c.cfg. It must run after the FlagSet is parsed.
func (c *cliFlags) harvest() {
	c.cfg.Timeout = config.Duration(c.timeout)
	c.cfg.Format = output.Format(c.format)
}

// applyTo copies onto dst only the flags that appeared on the command line.
// fs.Visit — as opposed to fs.VisitAll — is what makes that distinction.
func (c *cliFlags) applyTo(dst *config.Config) {
	c.fs.Visit(func(f *flag.Flag) {
		if apply, ok := c.apply[f.Name]; ok {
			apply(dst)
		}
	})
}

// resolveConfig layers the configuration sources in precedence order:
// defaults < config file < environment < explicitly-passed flags.
//
// Flags sit on top because they are the most specific statement of intent a
// user can make: they describe this one invocation, while a config file or an
// exported variable describes every invocation. An explicitly-passed --config
// that cannot be read is fatal rather than a warning: the flag is only ever
// present because the user asked for that specific file, so silently running
// on defaults would mask a typo'd path behind what looks like a normal run.
func (c *cliFlags) resolveConfig() (*config.Config, error) {
	cfg := config.DefaultConfig()

	if c.cfg.ConfigFile != "" {
		fileCfg, err := config.LoadFromFile(c.cfg.ConfigFile)
		if err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", c.cfg.ConfigFile, err)
		}
		cfg = fileCfg
	}

	cfg.LoadFromEnv()
	c.applyTo(cfg)

	// A config file may set "cache_file": "" explicitly (or a blank flag/env
	// value could, in principle, overwrite the default). cache.Load("") fails
	// with an opaque "open : no such file or directory" rather than a message
	// that points at the setting, so treat empty the same as "unset".
	if cfg.CacheFile == "" {
		cfg.CacheFile = cache.DefaultFile
	}

	return cfg, nil
}

// resolveCacheMode collapses the three cache-control settings into the single
// cache.Mode the resolve loop consults (spec §2).
//
// --resolve-all and --retry-failed contradict each other, so the combination is
// rejected rather than silently ranked: a user who asks to re-resolve
// everything *and* only the failures has a wrong expectation about one of them,
// and quietly honouring one would hide that. The check runs even alongside
// --no-cache, where the modes are moot, because the contradiction is still a
// mistake worth reporting.
//
// --no-cache is orthogonal and wins: it disables cache reads and writes
// outright, which subsumes whatever reuse policy the other two describe.
func resolveCacheMode(cfg *config.Config) (cache.Mode, error) {
	if cfg.ResolveAll && cfg.RetryFailed {
		return cache.ModeNormal, fmt.Errorf(
			"--resolve-all and --retry-failed are mutually exclusive: " +
				"--resolve-all re-resolves every ISBN, --retry-failed re-resolves only cached failures")
	}

	switch {
	case cfg.NoCache:
		return cache.ModeNoCache, nil
	case cfg.ResolveAll:
		return cache.ModeResolveAll, nil
	case cfg.RetryFailed:
		return cache.ModeRetryFailed, nil
	default:
		return cache.ModeNormal, nil
	}
}

// sheetCacheLookup returns the cache the resolve policy should read from: the
// local cache on its own, or a merged view of it and the Google Sheets output
// range when --sheet-cache is on (specs/deferred-cache-features.md §1).
//
// Merging into a view consulted by the existing cache.Policy — rather than
// running a second policy pass over the sheet rows — is what makes
// --resolve-all and --retry-failed apply to both caches without being spelled
// out twice, and keeps the "Cache: H hit, M miss, R retried" counters honest:
// Policy.Lookup counts every call, so each ISBN must be looked up exactly once.
//
// Every failure here is a warning rather than a fatal error. The sheet cache
// only ever saves work; a run that cannot read it is still a correct run that
// merely costs more API calls, and dying at this point would make an
// unreachable sheet break runs that used to succeed.
func sheetCacheLookup(cfg *config.Config, local *cache.Cache, warn io.Writer) *cache.Cache {
	if !cfg.SheetCacheEnabled() {
		return local
	}

	if cfg.SheetsURL == "" && cfg.SheetsID == "" {
		// A flag that silently does nothing is worse than a noisy one: the
		// sheet cache reads the *output* range, which only exists when the run
		// is writing to a sheet at all.
		fmt.Fprintln(warn, "Warning: --sheet-cache has no effect without --sheets-url or --sheets-id")
		return local
	}

	rows, err := readSheetCache(cfg)
	if err != nil {
		fmt.Fprintf(warn, "Warning: failed to read the sheet cache: %v\n", err)
		return local
	}

	if cfg.Verbose {
		fmt.Fprintf(warn, "Sheet cache: %d usable row(s) in the output range\n", len(rows))
	}

	return mergeSheetCache(local, rows)
}

// readSheetCache authenticates and reads the rows already present in the output
// range this run would write to.
func readSheetCache(cfg *config.Config) (map[string]sheets.ExistingRow, error) {
	ctx := context.Background()

	id := spreadsheetID(cfg)
	if id == "" {
		return nil, fmt.Errorf("no Google Sheets ID or URL provided")
	}

	service, err := sheets.Authenticate(ctx, sheets.AuthConfig{CredentialsPath: cfg.SheetsCredentials})
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	// The same WriteConfig writeToSheets will use, so the cache is read from
	// exactly the range the results land in — including under --create-new-tab,
	// which moves that range.
	return sheets.NewClient(ctx, service).ReadExistingStatus(sheets.WriteConfig{
		SpreadsheetID: id,
		OutputRange:   cfg.SheetsOutputRange,
		CreateNewTab:  cfg.SheetsCreateTab,
	})
}

// mergeSheetCache builds the read-only view the policy consults: the sheet's
// rows, overlaid with the local cache's entries.
//
// The result is deliberately *not* the cache that gets saved. A sheet row is a
// lossy reconstruction — nine columns, no ISBN-10, no attempt timestamp — so
// folding those rows into the cache file would leave a later run serving
// degraded entries as if they had been freshly resolved. The file therefore
// keeps recording only what this machine actually resolved, and the sheet stays
// the record for the rest.
func mergeSheetCache(local *cache.Cache, rows map[string]sheets.ExistingRow) *cache.Cache {
	merged := cache.New()

	for key, row := range rows {
		merged.Set(key, cache.Entry{Status: row.Status, Metadata: row.Metadata, Error: row.Error})
	}

	if local == nil {
		return merged
	}

	for key, entry := range local.Entries {
		// The local entry normally wins: it carries the full metadata the
		// resolver returned rather than the nine columns the sheet keeps.
		//
		// The exception is a local failure over a sheet success. Both caches
		// are consulted and a hit in either is enough, so an ISBN that failed
		// here but was resolved into the sheet by some other run must not be
		// re-attempted — that is precisely the CI-vs-workstation case the sheet
		// cache exists for.
		if entry.Status == cache.StatusError {
			if row, ok := merged.Get(key); ok && row.Status == cache.StatusSuccess {
				continue
			}
		}

		merged.Set(key, entry)
	}

	return merged
}

// spreadsheetID resolves the sheet the run operates on from whichever of
// --sheets-id / --sheets-url was given. Empty means no sheet is configured.
func spreadsheetID(cfg *config.Config) string {
	if cfg.SheetsID != "" {
		return cfg.SheetsID
	}

	if cfg.SheetsURL != "" {
		return sheets.ExtractSheetID(cfg.SheetsURL)
	}

	return ""
}

// bookResolver is the slice of resolver.APIClient the resolve loop needs. It
// exists so the loop can be tested against a fake that counts calls — the point
// of the cache is the calls it does *not* make, which is only observable from
// the resolver's side.
type bookResolver interface {
	Resolve(isbn string) (metadata *resolver.BookMetadata, source string, err error)
}

// newAPIClient builds the single APIClient the whole run shares, including the
// one rate limiter every pool worker paces itself against.
//
// The limiter is constructed here, once, and deliberately not per worker: a
// per-worker bucket would let a pool of N workers issue N times the configured
// rate, which is the opposite of the point. resolveISBNs hands this same
// *APIClient to every goroutine in the pool, so a single shared *RateLimiter on
// it governs the run as a whole (spec §4).
//
// Until this existed, APIClient.Limiter was declared and consumed by
// doWithRetry but never assigned outside tests, so RateLimiter.Wait's nil
// receiver no-op meant real runs paced nothing at all — the quota exhaustion
// recorded in specs/third-fallback-api.md §0.
func newAPIClient(cfg *config.Config) *resolver.APIClient {
	client := resolver.NewAPIClient(time.Duration(cfg.Timeout))
	client.MaxRetries = cfg.RateLimit.MaxRetries
	client.BaseBackoff = time.Duration(cfg.RateLimit.BaseBackoff)
	client.Limiter = resolver.NewRateLimiter(cfg.RateLimit.RequestsPerSecond, cfg.RateLimit.Burst)

	// Empty is the normal case and means anonymous Google Books requests; the
	// key only ever raises the quota the run draws on, so a missing one must
	// never be fatal (specs/third-fallback-api.md §0).
	client.GoogleBooksAPIKey = cfg.GoogleBooksAPIKey

	return client
}

// retryWarner returns an APIClient.OnRetry callback that prints the spec's
// rate-limit progress line to w:
//
//	Warning: rate limited by Open Library, retrying ISBN 9780596520687 in 2.1s (attempt 1/3)
//
// The line exists so a run that has gone quiet is distinguishable from a run
// that is sleeping off a backoff — without it, a 429 storm looks like a hang.
//
// Every pool worker shares one APIClient and so calls this concurrently; the
// mutex keeps two simultaneous retries from interleaving mid-line on stderr,
// which io.Writer gives no guarantee about on its own.
//
// The delay is rounded to a tenth of a second because the exact jittered
// nanoseconds are noise to a reader, and an un-rounded Duration prints as
// "2.100371842s".
func retryWarner(w io.Writer) func(resolver.RetryNotice) {
	var mu sync.Mutex
	return func(n resolver.RetryNotice) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "Warning: rate limited by %s, retrying ISBN %s in %s (attempt %d/%d)\n",
			n.API, n.ISBN, n.Delay.Round(100*time.Millisecond), n.Attempt, n.MaxRetries)
	}
}

// resolveISBNs resolves each ISBN, consulting the cache first, then resolving
// every cache miss across a bounded worker pool.
//
// The cache is read through rather than around: a policy hit fills the output
// slot straight from the cached entry with no network call. The lookup pass
// itself stays on this goroutine — cache.Policy is not safe for concurrent
// use (see its doc comment) — and only the misses are handed to
// resolver.Resolve for parallel resolution.
//
// Misses are grouped by cache key before dispatch, not resolved one-per-index,
// so a repeated ISBN within a single input list still costs one network call
// (matching the pre-pool behaviour of recording as the loop went). Every
// group's result is applied back to store and to all of that group's output
// slots sequentially, on this goroutine, after resolver.Resolve returns — the
// single collector that lets cache writes, the failures map, and progress
// output stay race-free with no mutex.
//
// Cached failures are replayed as errors, not as empty successes, so a cache
// hit and a fresh resolution produce byte-identical output for the same ISBN.
func resolveISBNs(concurrency int, isbns []string, client bookResolver, store *cache.Cache, policy *cache.Policy, progress io.Writer) ([]resolver.BookMetadata, map[string]error) {
	results := make([]resolver.BookMetadata, len(isbns))
	failures := make(map[string]error)

	type miss struct {
		index int
		isbn  string
	}

	missesByKey := make(map[string][]miss)
	var missKeys []string

	for i, isbnStr := range isbns {
		key := cache.Key(isbnStr)

		if entry, reuse := policy.Lookup(key); reuse {
			results[i] = cachedMetadata(entry, isbnStr)
			if entry.Status == cache.StatusError {
				failures[isbnStr] = cachedError(entry)
			}
			continue
		}

		if _, seen := missesByKey[key]; !seen {
			missKeys = append(missKeys, key)
		}
		missesByKey[key] = append(missesByKey[key], miss{index: i, isbn: isbnStr})
	}

	if len(missKeys) == 0 {
		return results, failures
	}

	// One representative ISBN spelling per key is enough to resolve the
	// group; every group member gets the same metadata back, reset to its
	// own spelling by cachedMetadata's non-cache counterpart below.
	representatives := make([]string, len(missKeys))
	for i, key := range missKeys {
		representatives[i] = missesByKey[key][0].isbn
	}

	resolved := resolver.Resolve(concurrency, representatives, client.Resolve)

	for i, res := range resolved {
		key := missKeys[i]
		group := missesByKey[key]
		attempted := time.Now().UTC()

		if res.Err != nil {
			store.Set(key, cache.Entry{
				Status:      cache.StatusError,
				Error:       res.Err.Error(),
				LastAttempt: attempted,
			})
			for _, m := range group {
				failures[m.isbn] = res.Err
				results[m.index] = resolver.BookMetadata{ISBN: m.isbn}
			}
			fmt.Fprintf(progress, "Failed to resolve ISBN %s: %v\n", group[0].isbn, res.Err)
			continue
		}

		store.Set(key, cache.Entry{
			Status:      cache.StatusSuccess,
			Metadata:    res.Metadata,
			LastAttempt: attempted,
		})
		for _, m := range group {
			metadata := *res.Metadata
			metadata.ISBN = m.isbn
			results[m.index] = metadata
		}
		fmt.Fprintf(progress, "✓ Resolved ISBN %s: %s%s\n", group[0].isbn, res.Metadata.Title, viaSource(res.Source))
	}

	return results, failures
}

// viaSource renders the " (via <API>)" suffix that names which tier answered.
//
// An unnamed source yields no suffix at all rather than "(via )": the resolver
// only leaves it empty when it returns an error, but a fake or a future tier
// that forgets to name itself should degrade to the pre-existing line rather
// than print a hole where an API name belongs.
func viaSource(source string) string {
	if source == "" {
		return ""
	}

	return " (via " + source + ")"
}

// cachedMetadata rebuilds an output row from a cached entry. The ISBN is reset
// to the spelling this run supplied, because the output row and the errors map
// are keyed by it — the cache key may be the ISBN-13 form of an ISBN-10 input.
func cachedMetadata(entry cache.Entry, isbnStr string) resolver.BookMetadata {
	if entry.Metadata == nil {
		return resolver.BookMetadata{ISBN: isbnStr}
	}

	metadata := *entry.Metadata
	metadata.ISBN = isbnStr

	return metadata
}

// cachedError reconstitutes the error a cached failure recorded. A hand-edited
// cache file can carry status "error" with no message, and an empty error
// string would render as a blank failure reason, so it is named instead.
func cachedError(entry cache.Entry) error {
	if entry.Error == "" {
		return fmt.Errorf("cached failure with no recorded error")
	}

	return fmt.Errorf("%s", entry.Error)
}

// getISBNs retrieves ISBNs from command-line args, file, stdin, or Google Sheets
func getISBNs(cfg *config.Config) ([]string, error) {
	// Check if reading from Google Sheets
	if cfg.SheetsURL != "" || cfg.SheetsID != "" {
		return getISBNsFromSheets(cfg)
	}

	// Check if reading from file
	if cfg.InputFile != "" {
		file, err := os.Open(cfg.InputFile)
		if err != nil {
			return nil, fmt.Errorf("failed to open file: %w", err)
		}
		defer file.Close()

		isbns, err := scanISBNs(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %w", err)
		}

		return isbns, nil
	}

	// Check if there are command-line arguments
	args := flag.Args()
	if len(args) > 0 {
		return args, nil
	}

	// Check if stdin has data
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat stdin: %w", err)
	}

	if (stat.Mode() & os.ModeCharDevice) == 0 {
		// Data is being piped to stdin
		isbns, err := scanISBNs(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", err)
		}

		return isbns, nil
	}

	return nil, nil
}

// scanISBNs reads one ISBN per line, ignoring blank lines and `#` comments.
//
// A leading column header is dropped. examples/ISBNs.csv — the sample the
// third-fallback spec measures against — starts with a literal `ISBN` line,
// which would otherwise be fed in as an ISBN and counted as a failure,
// inflating the denominator of exactly the measurement that file exists for.
// Only the first content line is ever considered for this; a header can only
// appear there, and skipping later look-alike lines would hide real input.
func scanISBNs(r io.Reader) ([]string, error) {
	var isbns []string

	atFirstLine := true
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if atFirstLine {
			atFirstLine = false
			if looksLikeHeader(line) {
				continue
			}
		}

		isbns = append(isbns, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return isbns, nil
}

// looksLikeHeader reports whether a line is a column label rather than an ISBN.
//
// The test is deliberately narrow: an ISBN is digits, optionally separated by
// hyphens or spaces, with an optional trailing `X` check digit. A line carrying
// any other character cannot be a mistyped ISBN, so it is a label. A line that
// is purely numeric but the wrong length *is* a malformed ISBN and must still
// reach validation to be reported — silently dropping it would turn a data
// error into a missing row.
func looksLikeHeader(line string) bool {
	compact := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}

		return r
	}, line)

	if compact == "" {
		return false
	}

	for i, r := range compact {
		if r >= '0' && r <= '9' {
			continue
		}

		// The check digit of an ISBN-10 may be `X`, but only in last place.
		if (r == 'X' || r == 'x') && i == len(compact)-1 {
			continue
		}

		return true
	}

	return false
}

// getISBNsFromSheets retrieves ISBNs from Google Sheets
func getISBNsFromSheets(cfg *config.Config) ([]string, error) {
	ctx := context.Background()

	if cfg.Verbose {
		fmt.Fprintln(os.Stderr, "Authenticating with Google Sheets...")
	}

	id := spreadsheetID(cfg)
	if id == "" {
		return nil, fmt.Errorf("no Google Sheets ID or URL provided")
	}

	if cfg.SheetsRange == "" {
		return nil, fmt.Errorf("no range specified (use --sheets-range)")
	}

	// Authenticate
	authConfig := sheets.AuthConfig{
		CredentialsPath: cfg.SheetsCredentials,
	}

	service, err := sheets.Authenticate(ctx, authConfig)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	if cfg.Verbose {
		fmt.Fprintln(os.Stderr, "✓ Successfully authenticated")
	}

	// Read ISBNs
	client := sheets.NewClient(ctx, service)

	sheetConfig := sheets.SheetConfig{
		SpreadsheetID: id,
		Range:         cfg.SheetsRange,
	}

	if cfg.Verbose {
		info, _ := client.GetSpreadsheetInfo(id)
		sheetName := "Unknown"
		if info != nil && len(info.Sheets) > 0 {
			sheetName = info.Properties.Title
		}
		fmt.Fprintf(os.Stderr, "Reading ISBNs from sheet \"%s\" (range: %s)...\n", sheetName, cfg.SheetsRange)
	}

	isbns, err := client.ReadISBNs(sheetConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to read ISBNs from sheet: %w", err)
	}

	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "Found %d ISBNs to process\n", len(isbns))
	}

	return isbns, nil
}

// writeToSheets writes results to Google Sheets
func writeToSheets(cfg *config.Config, results []resolver.BookMetadata, errors map[string]error) error {
	ctx := context.Background()

	if cfg.Verbose && !cfg.SheetsDryRun {
		fmt.Fprintln(os.Stderr, "Writing results to Google Sheets...")
	}

	// Authenticate
	authConfig := sheets.AuthConfig{
		CredentialsPath: cfg.SheetsCredentials,
	}

	service, err := sheets.Authenticate(ctx, authConfig)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Write results
	client := sheets.NewClient(ctx, service)

	writeConfig := sheets.WriteConfig{
		SpreadsheetID: spreadsheetID(cfg),
		OutputRange:   cfg.SheetsOutputRange,
		CreateNewTab:  cfg.SheetsCreateTab,
		DryRun:        cfg.SheetsDryRun,
	}

	if err := client.WriteResults(writeConfig, results, errors); err != nil {
		return err
	}

	if cfg.Verbose && !cfg.SheetsDryRun {
		successful := len(results) - len(errors)
		fmt.Fprintf(os.Stderr, "✓ Successfully wrote %d results\n", successful)
		if len(errors) > 0 {
			fmt.Fprintf(os.Stderr, "⚠ %d ISBNs failed to resolve\n", len(errors))
		}
	}

	return nil
}
