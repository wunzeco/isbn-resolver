package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wunzeco/isbn-resolver/pkg/resolver"
)

func TestFormatText(t *testing.T) {
	buf := &bytes.Buffer{}
	formatter := NewFormatter(FormatText, buf)

	metadata := &resolver.BookMetadata{
		ISBN:            "9780134190440",
		ISBN13:          "978-0134190440",
		Title:           "The Go Programming Language",
		Authors:         []string{"Alan A. A. Donovan", "Brian W. Kernighan"},
		Publisher:       "Addison-Wesley",
		PublicationDate: "2015-11-16",
		Pages:           400,
		Categories:      []string{"Programming", "Computer Science"},
	}

	err := formatter.FormatResult(metadata, nil)
	if err != nil {
		t.Fatalf("FormatResult failed: %v", err)
	}

	output := buf.String()
	
	// Check that key fields are present
	requiredFields := []string{
		"ISBN: 9780134190440",
		"ISBN-13: 978-0134190440",
		"Title: The Go Programming Language",
		"Authors: Alan A. A. Donovan, Brian W. Kernighan",
		"Publisher: Addison-Wesley",
		"Pages: 400",
		"Categories: Programming, Computer Science",
		"Status: ✓ Resolved",
	}

	for _, field := range requiredFields {
		if !strings.Contains(output, field) {
			t.Errorf("Output missing field: %s", field)
		}
	}
}

func TestFormatJSON(t *testing.T) {
	buf := &bytes.Buffer{}
	formatter := NewFormatter(FormatJSON, buf)

	results := []resolver.BookMetadata{
		{
			ISBN:    "9780134190440",
			Title:   "The Go Programming Language",
			Authors: []string{"Alan A. A. Donovan"},
		},
	}

	errors := make(map[string]error)

	err := formatter.FormatBatch(results, errors)
	if err != nil {
		t.Fatalf("FormatBatch failed: %v", err)
	}

	output := buf.String()
	
	// Check that JSON structure is present
	requiredFields := []string{
		`"results"`,
		`"summary"`,
		`"isbn": "9780134190440"`,
		`"status": "success"`,
	}

	for _, field := range requiredFields {
		if !strings.Contains(output, field) {
			t.Errorf("JSON output missing field: %s", field)
		}
	}
}

func TestFormatCSV(t *testing.T) {
	buf := &bytes.Buffer{}
	formatter := NewFormatter(FormatCSV, buf)

	results := []resolver.BookMetadata{
		{
			ISBN:    "9780134190440",
			Title:   "The Go Programming Language",
			Authors: []string{"Alan A. A. Donovan", "Brian W. Kernighan"},
			Pages:   400,
		},
	}

	errors := make(map[string]error)

	err := formatter.FormatBatch(results, errors)
	if err != nil {
		t.Fatalf("FormatBatch failed: %v", err)
	}

	output := buf.String()
	
	// Check CSV header
	if !strings.Contains(output, "ISBN,Status,Title") {
		t.Error("CSV output missing header")
	}

	// Check data row
	if !strings.Contains(output, "9780134190440,success,The Go Programming Language") {
		t.Error("CSV output missing data row")
	}
}
