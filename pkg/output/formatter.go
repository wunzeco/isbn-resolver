package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

// Format represents the output format type
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
	FormatCSV  Format = "csv"
)

// Formatter handles formatting of book metadata
type Formatter struct {
	format Format
	writer io.Writer
}

// NewFormatter creates a new formatter
func NewFormatter(format Format, writer io.Writer) *Formatter {
	return &Formatter{
		format: format,
		writer: writer,
	}
}

// FormatResult formats a single result
func (f *Formatter) FormatResult(metadata *resolver.BookMetadata, err error) error {
	switch f.format {
	case FormatText:
		return f.formatText(metadata, err)
	case FormatJSON:
		// JSON formatting is handled in batch mode
		return nil
	case FormatCSV:
		// CSV formatting is handled in batch mode
		return nil
	default:
		return fmt.Errorf("unsupported format: %s", f.format)
	}
}

// formatText formats output in human-readable text
func (f *Formatter) formatText(metadata *resolver.BookMetadata, err error) error {
	if err != nil {
		_, writeErr := fmt.Fprintf(f.writer, "ISBN: %s\nStatus: ✗ Failed - %s\n\n---\n\n", metadata.ISBN, err.Error())
		return writeErr
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("ISBN: %s\n", metadata.ISBN))
	
	if metadata.ISBN13 != "" {
		sb.WriteString(fmt.Sprintf("ISBN-13: %s\n", metadata.ISBN13))
	}
	
	if metadata.Title != "" {
		sb.WriteString(fmt.Sprintf("Title: %s\n", metadata.Title))
	}
	
	if len(metadata.Authors) > 0 {
		sb.WriteString(fmt.Sprintf("Authors: %s\n", strings.Join(metadata.Authors, ", ")))
	}
	
	if metadata.Publisher != "" {
		sb.WriteString(fmt.Sprintf("Publisher: %s\n", metadata.Publisher))
	}
	
	if metadata.PublicationDate != "" {
		sb.WriteString(fmt.Sprintf("Publication Date: %s\n", metadata.PublicationDate))
	}
	
	if metadata.Pages > 0 {
		sb.WriteString(fmt.Sprintf("Pages: %d\n", metadata.Pages))
	}
	
	if len(metadata.Categories) > 0 {
		sb.WriteString(fmt.Sprintf("Categories: %s\n", strings.Join(metadata.Categories, ", ")))
	}
	
	sb.WriteString("Status: ✓ Resolved\n\n---\n\n")

	_, err = f.writer.Write([]byte(sb.String()))
	return err
}

// FormatBatch formats multiple results
func (f *Formatter) FormatBatch(results []resolver.BookMetadata, errors map[string]error) error {
	switch f.format {
	case FormatJSON:
		return f.formatJSON(results, errors)
	case FormatCSV:
		return f.formatCSV(results, errors)
	default:
		return nil // Text format is handled per-result
	}
}

// formatJSON formats output as JSON
func (f *Formatter) formatJSON(results []resolver.BookMetadata, errors map[string]error) error {
	type resultEntry struct {
		ISBN   string                  `json:"isbn"`
		Status string                  `json:"status"`
		Data   *resolver.BookMetadata  `json:"data,omitempty"`
		Error  string                  `json:"error,omitempty"`
	}

	output := struct {
		Results []resultEntry `json:"results"`
		Summary struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
		} `json:"summary"`
	}{}

	for _, metadata := range results {
		entry := resultEntry{
			ISBN: metadata.ISBN,
		}

		if err, hasError := errors[metadata.ISBN]; hasError {
			entry.Status = "error"
			entry.Error = err.Error()
			output.Summary.Failed++
		} else {
			entry.Status = "success"
			entry.Data = &metadata
			output.Summary.Successful++
		}

		output.Results = append(output.Results, entry)
	}

	output.Summary.Total = len(results)

	encoder := json.NewEncoder(f.writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// formatCSV formats output as CSV
func (f *Formatter) formatCSV(results []resolver.BookMetadata, errors map[string]error) error {
	writer := csv.NewWriter(f.writer)
	defer writer.Flush()

	// Write header
	header := []string{
		"ISBN",
		"ISBN-13",
		"Title",
		"Authors",
		"Publisher",
		"Publication Date",
		"Pages",
		"Categories",
		"Status",
		"Error",
	}
	if err := writer.Write(header); err != nil {
		return err
	}

	// Write data
	for _, metadata := range results {
		status := "success"
		errorMsg := ""

		if err, hasError := errors[metadata.ISBN]; hasError {
			status = "error"
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

		row := []string{
			metadata.ISBN,
			isbn13,
			metadata.Title,
			strings.Join(metadata.Authors, "; "),
			metadata.Publisher,
			metadata.PublicationDate,
			pages,
			strings.Join(metadata.Categories, "; "),
			status,
			errorMsg,
		}

		if err := writer.Write(row); err != nil {
			return err
		}
	}

	return nil
}
