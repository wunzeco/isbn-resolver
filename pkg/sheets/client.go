package sheets

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/api/sheets/v4"
)

// SheetConfig holds configuration for sheet operations
type SheetConfig struct {
	SpreadsheetID string
	Range         string
	DryRun        bool
}

// Client handles interactions with Google Sheets
type Client struct {
	service *sheets.Service
	ctx     context.Context
}

// NewClient creates a new Sheets client
func NewClient(ctx context.Context, service *sheets.Service) *Client {
	return &Client{
		service: service,
		ctx:     ctx,
	}
}

// ExtractSheetID extracts spreadsheet ID from URL or returns the ID directly
func ExtractSheetID(urlOrID string) string {
	// Pattern to match Google Sheets URL
	pattern := regexp.MustCompile(`/spreadsheets/d/([a-zA-Z0-9-_]+)`)
	matches := pattern.FindStringSubmatch(urlOrID)
	
	if len(matches) > 1 {
		return matches[1]
	}
	
	// Assume it's already an ID
	return urlOrID
}

// ValidateRange validates and normalizes a range string
func ValidateRange(rangeStr string) (string, error) {
	if rangeStr == "" {
		return "", fmt.Errorf("range cannot be empty")
	}

	// Simple validation - ranges should be in A1 notation
	// Examples: "A2:A", "Sheet1!A2:A100", "A:A"
	if !strings.Contains(rangeStr, ":") && !strings.Contains(rangeStr, "!") {
		return "", fmt.Errorf("invalid range format: %s (use A1 notation like 'A2:A' or 'Sheet1!A2:A')", rangeStr)
	}

	return rangeStr, nil
}

// GetSpreadsheetInfo retrieves basic information about a spreadsheet
func (c *Client) GetSpreadsheetInfo(spreadsheetID string) (*sheets.Spreadsheet, error) {
	spreadsheet, err := c.service.Spreadsheets.Get(spreadsheetID).Context(c.ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve spreadsheet info: %w", err)
	}

	return spreadsheet, nil
}

// GetSheetTitle gets the title of the first sheet or a specific sheet
func (c *Client) GetSheetTitle(spreadsheetID string, sheetIndex int) (string, error) {
	info, err := c.GetSpreadsheetInfo(spreadsheetID)
	if err != nil {
		return "", err
	}

	if len(info.Sheets) == 0 {
		return "", fmt.Errorf("spreadsheet has no sheets")
	}

	if sheetIndex >= len(info.Sheets) {
		return "", fmt.Errorf("sheet index %d out of range (total sheets: %d)", sheetIndex, len(info.Sheets))
	}

	return info.Sheets[sheetIndex].Properties.Title, nil
}
