package sheets

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/isbn"
	"github.com/wunzeco/isbn-resolver/pkg/resolver"
	"google.golang.org/api/googleapi"
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
		if cellValue == "" || isISBNHeader(cellValue) {
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

// isISBNHeader reports whether a cell is one of the header labels a human is
// likely to have put above an ISBN column, so it isn't mistaken for data.
func isISBNHeader(cellValue string) bool {
	switch strings.ToLower(strings.TrimSpace(cellValue)) {
	case "isbn", "isbn-10", "isbn-13", "isbn number":
		return true
	default:
		return false
	}
}

// ExistingRow is one row already present in the output range, decoded back into
// the shape the resolver produced it in. It is what lets a run that has no local
// cache file — a CI checkout, say — still skip ISBNs a previous run resolved
// into the sheet (specs/deferred-cache-features.md §1).
type ExistingRow struct {
	// Status mirrors the sheet's Status column, mapped onto the local cache's
	// vocabulary so both caches can be fed through the same cache.Policy.
	Status cache.Status
	// Error is the sheet's Error column, populated for StatusError rows.
	Error string
	// Metadata is the row's book metadata, reconstructed so a skipped ISBN can
	// be re-written unchanged rather than blanked out. Only set for
	// StatusSuccess rows; an error row has nothing worth reusing.
	Metadata *resolver.BookMetadata
}

// ReadExistingStatus reads the output range and returns the rows already written
// there, keyed by cache.Key so the result drops straight into the same lookup
// the local cache uses.
//
// A range with no data — the first-ever run, or a tab that doesn't exist yet
// because --create-new-tab hasn't run — yields an empty map and no error. That
// is the normal first-run path, not a failure. Rows whose Status column holds
// neither Success nor Error are omitted rather than guessed at.
func (c *Client) ReadExistingStatus(spreadsheetID, outputRange string) (map[string]ExistingRow, error) {
	readRange, err := outputReadRange(outputRange)
	if err != nil {
		return nil, err
	}

	resp, err := c.service.Spreadsheets.Values.Get(spreadsheetID, readRange).Context(c.ctx).Do()
	if err != nil {
		// A range naming a tab that doesn't exist yet is indistinguishable from
		// an empty one for our purposes: there is nothing cached either way. A
		// genuinely mistyped range still surfaces when the run writes results
		// back to it, so swallowing it here doesn't hide a misconfiguration.
		if isMissingRangeError(err) {
			return map[string]ExistingRow{}, nil
		}
		return nil, fmt.Errorf("unable to read existing results from sheet: %w", err)
	}

	existing := make(map[string]ExistingRow, len(resp.Values))
	for _, row := range resp.Values {
		isbnCell := strings.TrimSpace(cell(row, 0))
		if isbnCell == "" || isISBNHeader(isbnCell) {
			continue
		}

		var status cache.Status
		switch strings.ToLower(strings.TrimSpace(cell(row, 7))) {
		case "success":
			status = cache.StatusSuccess
		case "error":
			status = cache.StatusError
		default:
			// Blank, "Status" (the header row), or something a human typed.
			// Not enough to justify skipping a resolution.
			continue
		}

		entry := ExistingRow{Status: status, Error: strings.TrimSpace(cell(row, 8))}
		if status == cache.StatusSuccess {
			entry.Metadata = rowMetadata(row, isbnCell)
		}

		existing[cache.Key(isbnCell)] = entry
	}

	return existing, nil
}

// rowMetadata rebuilds book metadata from an output row, inverting
// formatResultsForSheet.
func rowMetadata(row []interface{}, isbnCell string) *resolver.BookMetadata {
	metadata := &resolver.BookMetadata{
		ISBN:            isbnCell,
		Title:           strings.TrimSpace(cell(row, 1)),
		Authors:         splitList(cell(row, 2)),
		Publisher:       strings.TrimSpace(cell(row, 3)),
		PublicationDate: strings.TrimSpace(cell(row, 4)),
		Categories:      splitList(cell(row, 6)),
	}

	// The writer stores the original ISBN when no ISBN-13 was available, so only
	// claim an ISBN-13 when the cell actually is one.
	if result := isbn.Validate(isbnCell); result.Type == isbn.ISBN13 {
		metadata.ISBN13 = result.Normalized
	}

	// A blank or non-numeric page count is normal (the writer leaves it empty
	// when Pages is 0), so a parse failure just means "unknown".
	if pages, err := strconv.Atoi(strings.TrimSpace(cell(row, 5))); err == nil {
		metadata.Pages = pages
	}

	return metadata
}

// cell reads one column from a sheet row, tolerating short rows — the Sheets API
// truncates trailing empty cells, so a row with no Error value comes back with
// eight entries rather than nine.
func cell(row []interface{}, index int) string {
	if index >= len(row) || row[index] == nil {
		return ""
	}
	return fmt.Sprintf("%v", row[index])
}

// splitList inverts the ", " joining formatResultsForSheet applies to authors
// and categories.
func splitList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	list := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			list = append(list, trimmed)
		}
	}

	if len(list) == 0 {
		return nil
	}
	return list
}

// outputReadRange turns a configured output range into one that covers every
// written column. The output range is a *write anchor* ("B1", "Results!A1") far
// more often than a bounded range, and reading an anchor back returns a single
// cell, so an unbounded range spanning the nine output columns is derived from
// it.
func outputReadRange(outputRange string) (string, error) {
	outputRange = strings.TrimSpace(outputRange)
	if outputRange == "" {
		outputRange = defaultOutputRange
	}

	sheetName := ""
	cellRef := outputRange
	if idx := strings.LastIndex(outputRange, "!"); idx >= 0 {
		sheetName = outputRange[:idx+1]
		cellRef = outputRange[idx+1:]
	}

	// Already a range: the caller bounded it deliberately, so honour it.
	if strings.Contains(cellRef, ":") {
		return outputRange, nil
	}

	column, row := splitCellRef(cellRef)
	if column == "" || row == "" {
		return "", fmt.Errorf("invalid output range: %s (use A1 notation like 'B1' or 'Results!A1')", outputRange)
	}

	endColumn := columnName(columnIndex(column) + outputColumns - 1)
	return fmt.Sprintf("%s%s%s:%s", sheetName, column, row, endColumn), nil
}

// splitCellRef splits "B12" into its column letters and row digits. Either half
// coming back empty means the reference wasn't A1 notation.
func splitCellRef(cellRef string) (column, row string) {
	i := 0
	for i < len(cellRef) && ((cellRef[i] >= 'A' && cellRef[i] <= 'Z') || (cellRef[i] >= 'a' && cellRef[i] <= 'z')) {
		i++
	}
	column = strings.ToUpper(cellRef[:i])

	row = cellRef[i:]
	for _, r := range row {
		if r < '0' || r > '9' {
			return "", ""
		}
	}

	return column, row
}

// columnIndex converts spreadsheet column letters to a 1-based index ("A" = 1,
// "AA" = 27).
func columnIndex(column string) int {
	index := 0
	for i := 0; i < len(column); i++ {
		index = index*26 + int(column[i]-'A'+1)
	}
	return index
}

// columnName is the inverse of columnIndex.
func columnName(index int) string {
	name := ""
	for index > 0 {
		index--
		name = string(rune('A'+index%26)) + name
		index /= 26
	}
	return name
}

// isMissingRangeError reports whether a Values.Get failure means "that range
// isn't there", which the Sheets API reports as a 400 rather than a 404.
func isMissingRangeError(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Code != http.StatusBadRequest && apiErr.Code != http.StatusNotFound {
		return false
	}
	return strings.Contains(apiErr.Message, "Unable to parse range")
}

// GetRangeInfo returns information about a range
func (c *Client) GetRangeInfo(spreadsheetID, rangeStr string) (int, error) {
	resp, err := c.service.Spreadsheets.Values.Get(spreadsheetID, rangeStr).Context(c.ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("unable to get range info: %w", err)
	}

	return len(resp.Values), nil
}
