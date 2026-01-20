package sheets

import (
	"fmt"
	"strings"
)

// ReadISBNs reads ISBN numbers from a Google Sheet
func (c *Client) ReadISBNs(config SheetConfig) ([]string, error) {
	// Validate range
	rangeStr, err := ValidateRange(config.Range)
	if err != nil {
		return nil, err
	}

	// Read values from the sheet
	resp, err := c.service.Spreadsheets.Values.Get(config.SpreadsheetID, rangeStr).Context(c.ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("unable to read data from sheet: %w", err)
	}

	if len(resp.Values) == 0 {
		return nil, fmt.Errorf("no data found in range: %s", rangeStr)
	}

	// Extract ISBNs from the response
	var isbns []string
	for _, row := range resp.Values {
		if len(row) == 0 {
			continue // Skip empty rows
		}

		// Get the first column value
		cellValue := fmt.Sprintf("%v", row[0])
		cellValue = strings.TrimSpace(cellValue)

		// Skip empty cells and headers (common patterns)
		if cellValue == "" || 
		   strings.EqualFold(cellValue, "isbn") || 
		   strings.EqualFold(cellValue, "isbn-10") || 
		   strings.EqualFold(cellValue, "isbn-13") ||
		   strings.EqualFold(cellValue, "isbn number") {
			continue
		}

		// Handle numeric ISBNs (Google Sheets might format as numbers)
		// Remove any formatting characters
		cellValue = strings.ReplaceAll(cellValue, " ", "")
		cellValue = strings.ReplaceAll(cellValue, "-", "")
		cellValue = strings.ReplaceAll(cellValue, ".", "")

		if cellValue != "" {
			isbns = append(isbns, cellValue)
		}
	}

	if len(isbns) == 0 {
		return nil, fmt.Errorf("no valid ISBNs found in range: %s", rangeStr)
	}

	return isbns, nil
}

// GetRangeInfo returns information about a range
func (c *Client) GetRangeInfo(spreadsheetID, rangeStr string) (int, error) {
	resp, err := c.service.Spreadsheets.Values.Get(spreadsheetID, rangeStr).Context(c.ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("unable to get range info: %w", err)
	}

	return len(resp.Values), nil
}
