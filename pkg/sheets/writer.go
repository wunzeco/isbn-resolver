package sheets

import (
	"fmt"
	"strings"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
	"google.golang.org/api/sheets/v4"
)

// WriteConfig holds configuration for writing results
type WriteConfig struct {
	SpreadsheetID string
	OutputRange   string
	CreateNewTab  string
	DryRun        bool
}

// WriteResults writes book metadata results to Google Sheets
func (c *Client) WriteResults(config WriteConfig, results []resolver.BookMetadata, errors map[string]error) error {
	if config.DryRun {
		return c.previewResults(config, results, errors)
	}

	// Create new tab if specified
	if config.CreateNewTab != "" {
		if err := c.createNewTab(config.SpreadsheetID, config.CreateNewTab); err != nil {
			return fmt.Errorf("failed to create new tab: %w", err)
		}
		// Update output range to use the new tab
		if config.OutputRange == "" {
			config.OutputRange = fmt.Sprintf("%s!A1", config.CreateNewTab)
		} else if !strings.Contains(config.OutputRange, "!") {
			config.OutputRange = fmt.Sprintf("%s!%s", config.CreateNewTab, config.OutputRange)
		}
	}

	// Convert results to sheet values
	values := c.formatResultsForSheet(results, errors)

	// Determine the range to write to
	writeRange := config.OutputRange
	if writeRange == "" {
		// Default to writing next to the input column
		writeRange = "B1"
	}

	// Prepare the update request
	valueRange := &sheets.ValueRange{
		Values: values,
	}

	// Write to sheets
	_, err := c.service.Spreadsheets.Values.Update(
		config.SpreadsheetID,
		writeRange,
		valueRange,
	).ValueInputOption("RAW").Context(c.ctx).Do()

	if err != nil {
		return fmt.Errorf("unable to write data to sheet: %w", err)
	}

	return nil
}

// formatResultsForSheet converts book metadata to sheet rows
func (c *Client) formatResultsForSheet(results []resolver.BookMetadata, errors map[string]error) [][]interface{} {
	// Header row
	values := [][]interface{}{
		{"ISBN-13", "Title", "Authors", "Publisher", "Publication Date", "Pages", "Categories", "Status", "Error"},
	}

	// Data rows
	for _, metadata := range results {
		status := "Success"
		errorMsg := ""

		if err, hasError := errors[metadata.ISBN]; hasError {
			status = "Error"
			errorMsg = err.Error()
		}

		pages := ""
		if metadata.Pages > 0 {
			pages = fmt.Sprintf("%d", metadata.Pages)
		}

		// Use ISBN-13 if available, otherwise use the original ISBN
		isbn13 := metadata.ISBN13
		if isbn13 == "" {
			isbn13 = metadata.ISBN
		}

		row := []interface{}{
			isbn13,
			metadata.Title,
			strings.Join(metadata.Authors, ", "),
			metadata.Publisher,
			metadata.PublicationDate,
			pages,
			strings.Join(metadata.Categories, ", "),
			status,
			errorMsg,
		}

		values = append(values, row)
	}

	return values
}

// previewResults shows what would be written without actually writing
func (c *Client) previewResults(config WriteConfig, results []resolver.BookMetadata, errors map[string]error) error {
	fmt.Println("DRY RUN - Preview of changes:")
	fmt.Println("=============================")
	fmt.Printf("Spreadsheet ID: %s\n", config.SpreadsheetID)
	fmt.Printf("Output Range: %s\n", config.OutputRange)
	
	if config.CreateNewTab != "" {
		fmt.Printf("Would create new tab: %s\n", config.CreateNewTab)
	}

	fmt.Printf("\nWould write %d rows:\n\n", len(results)+1)

	values := c.formatResultsForSheet(results, errors)
	
	// Print first few rows as preview
	maxPreview := 5
	if len(values) < maxPreview {
		maxPreview = len(values)
	}

	for i := 0; i < maxPreview; i++ {
		fmt.Printf("Row %d: %v\n", i+1, values[i])
	}

	if len(values) > maxPreview {
		fmt.Printf("... and %d more rows\n", len(values)-maxPreview)
	}

	fmt.Println("\nNo changes were made (dry run mode)")
	return nil
}

// createNewTab creates a new sheet tab in the spreadsheet
func (c *Client) createNewTab(spreadsheetID, tabName string) error {
	req := &sheets.Request{
		AddSheet: &sheets.AddSheetRequest{
			Properties: &sheets.SheetProperties{
				Title: tabName,
			},
		},
	}

	batchUpdateRequest := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{req},
	}

	_, err := c.service.Spreadsheets.BatchUpdate(spreadsheetID, batchUpdateRequest).Context(c.ctx).Do()
	if err != nil {
		// Check if sheet already exists
		if strings.Contains(err.Error(), "already exists") {
			return nil // Tab already exists, continue
		}
		return err
	}

	return nil
}

// AppendResults appends results to the end of existing data
func (c *Client) AppendResults(config WriteConfig, results []resolver.BookMetadata, errors map[string]error) error {
	if config.DryRun {
		return c.previewResults(config, results, errors)
	}

	values := c.formatResultsForSheet(results, errors)

	valueRange := &sheets.ValueRange{
		Values: values,
	}

	appendRange := config.OutputRange
	if appendRange == "" {
		appendRange = "A1"
	}

	_, err := c.service.Spreadsheets.Values.Append(
		config.SpreadsheetID,
		appendRange,
		valueRange,
	).ValueInputOption("RAW").Context(c.ctx).Do()

	if err != nil {
		return fmt.Errorf("unable to append data to sheet: %w", err)
	}

	return nil
}
