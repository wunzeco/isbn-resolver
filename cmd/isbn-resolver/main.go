package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/config"
	"github.com/wunzeco/isbn-resolver/pkg/isbn"
	"github.com/wunzeco/isbn-resolver/pkg/output"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
	"github.com/wunzeco/isbn-resolver/pkg/sheets"
)

func main() {
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

	// Create API client
	client := resolver.NewAPIClient(time.Duration(cfg.Timeout))
	client.MaxRetries = cfg.RateLimit.MaxRetries
	client.BaseBackoff = time.Duration(cfg.RateLimit.BaseBackoff)

	progress := io.Discard
	if cfg.Verbose {
		progress = os.Stderr
	}

	policy := cache.NewPolicy(bookCache, cacheMode)
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
			successful := len(validISBNs) - len(errors)
			fmt.Fprintf(os.Stderr, "\nSummary: %d successful, %d failed out of %d total\n",
				successful, len(errors), len(validISBNs))
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
		successful := len(validISBNs) - len(errors)
		fmt.Fprintf(os.Stderr, "\nSummary: %d successful, %d failed out of %d total\n",
			successful, len(errors), len(validISBNs))
	}
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

// bookResolver is the slice of resolver.APIClient the resolve loop needs. It
// exists so the loop can be tested against a fake that counts calls — the point
// of the cache is the calls it does *not* make, which is only observable from
// the resolver's side.
type bookResolver interface {
	Resolve(isbn string) (*resolver.BookMetadata, error)
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
		fmt.Fprintf(progress, "✓ Resolved ISBN %s: %s\n", group[0].isbn, res.Metadata.Title)
	}

	return results, failures
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
	var isbns []string

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

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				isbns = append(isbns, line)
			}
		}

		if err := scanner.Err(); err != nil {
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
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				isbns = append(isbns, line)
			}
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", err)
		}

		return isbns, nil
	}

	return isbns, nil
}

// getISBNsFromSheets retrieves ISBNs from Google Sheets
func getISBNsFromSheets(cfg *config.Config) ([]string, error) {
	ctx := context.Background()

	if cfg.Verbose {
		fmt.Fprintln(os.Stderr, "Authenticating with Google Sheets...")
	}

	// Determine spreadsheet ID
	spreadsheetID := cfg.SheetsID
	if spreadsheetID == "" && cfg.SheetsURL != "" {
		spreadsheetID = sheets.ExtractSheetID(cfg.SheetsURL)
	}

	if spreadsheetID == "" {
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
		SpreadsheetID: spreadsheetID,
		Range:         cfg.SheetsRange,
	}

	if cfg.Verbose {
		info, _ := client.GetSpreadsheetInfo(spreadsheetID)
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

	// Determine spreadsheet ID
	spreadsheetID := cfg.SheetsID
	if spreadsheetID == "" && cfg.SheetsURL != "" {
		spreadsheetID = sheets.ExtractSheetID(cfg.SheetsURL)
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
		SpreadsheetID: spreadsheetID,
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
