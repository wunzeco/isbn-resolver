package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wunzeco/isbn-resolver/pkg/config"
	"github.com/wunzeco/isbn-resolver/pkg/isbn"
	"github.com/wunzeco/isbn-resolver/pkg/output"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
	"github.com/wunzeco/isbn-resolver/pkg/sheets"
	"github.com/wunzeco/isbn-resolver/pkg/worker"
)

func main() {
	cfg := config.DefaultConfig()

	// Define command-line flags
	flag.IntVar(&cfg.Workers, "workers", cfg.Workers, "Number of concurrent workers")
	flag.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "API request timeout")
	flag.StringVar(&cfg.InputFile, "file", "", "Input file containing ISBNs (one per line)")
	formatStr := flag.String("format", "text", "Output format: text, json, csv")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Enable verbose output")
	flag.StringVar(&cfg.ConfigFile, "config", "", "Configuration file path")
	
	// Google Sheets flags
	flag.StringVar(&cfg.SheetsURL, "sheets-url", "", "Google Sheets URL")
	flag.StringVar(&cfg.SheetsID, "sheets-id", "", "Google Sheets ID")
	flag.StringVar(&cfg.SheetsRange, "sheets-range", "", "Cell range for ISBNs (e.g., 'Sheet1!A2:A')")
	flag.StringVar(&cfg.SheetsCredentials, "sheets-credentials", "", "Path to Google Sheets credentials file")
	flag.StringVar(&cfg.SheetsOutputRange, "sheets-output-range", "", "Where to write results")
	flag.StringVar(&cfg.SheetsCreateTab, "sheets-create-tab", "", "Create new tab for results")
	flag.BoolVar(&cfg.SheetsDryRun, "sheets-dry-run", false, "Preview changes without writing")

	flag.Parse()

	// Load configuration from file if specified
	if cfg.ConfigFile != "" {
		if fileCfg, err := config.LoadFromFile(cfg.ConfigFile); err == nil {
			cfg = fileCfg
		} else if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "Warning: Failed to load config file: %v\n", err)
		}
	}

	// Load from environment
	cfg.LoadFromEnv()

	// Override format from flag
	cfg.Format = output.Format(*formatStr)

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

	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "Processing %d valid ISBN(s) with %d workers...\n", len(validISBNs), cfg.Workers)
	}

	// Create API client and worker pool
	client := resolver.NewAPIClient(cfg.Timeout)
	pool := worker.NewPool(cfg.Workers, client)

	// Submit jobs
	for i, isbnStr := range validISBNs {
		pool.Submit(worker.Job{
			ISBN:  isbnStr,
			Index: i,
		})
	}

	// Collect results
	results := make([]resolver.BookMetadata, len(validISBNs))
	errors := make(map[string]error)

	go func() {
		pool.Close()
	}()

	for result := range pool.Results() {
		if result.Error != nil {
			errors[result.Metadata.ISBN] = result.Error
			results[result.Index] = resolver.BookMetadata{ISBN: result.Metadata.ISBN}
		} else {
			results[result.Index] = *result.Metadata
		}
	}

	// Sort results by original order
	sort.Slice(results, func(i, j int) bool {
		return i < j
	})

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

