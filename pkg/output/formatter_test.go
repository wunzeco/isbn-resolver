package output

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"reflect"
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

// TestFormatCSV pins the full 10-column CSV schema. Downstream consumers
// (spreadsheets, the Sheets writer) depend on both the column set and its
// order, so the whole record is compared rather than a substring: a reordered
// or dropped column must fail here.
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
		{
			ISBN: "9780000000000",
		},
	}

	// Keyed by ISBN: an entry here flips the row's Status column to "error"
	// and populates the trailing Error column.
	errors := map[string]error{
		"9780000000000": fmt.Errorf("not found in any source"),
	}

	if err := formatter.FormatBatch(results, errors); err != nil {
		t.Fatalf("FormatBatch failed: %v", err)
	}

	records, err := csv.NewReader(buf).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected header + 2 rows, got %d records: %v", len(records), records)
	}

	want := [][]string{
		{"ISBN", "ISBN-13", "Title", "Authors", "Publisher", "Publication Date", "Pages", "Categories", "Status", "Error"},
		// ISBN13 is empty on the input, so the ISBN-13 column falls back to ISBN.
		// Multi-valued fields are joined with "; " to stay comma-safe.
		{"9780134190440", "9780134190440", "The Go Programming Language", "Alan A. A. Donovan; Brian W. Kernighan", "", "", "400", "", "success", ""},
		// Pages 0 renders as empty rather than "0" so unknown page counts are distinguishable.
		{"9780000000000", "9780000000000", "", "", "", "", "", "", "error", "not found in any source"},
	}

	for i, wantRecord := range want {
		if !reflect.DeepEqual(records[i], wantRecord) {
			t.Errorf("record %d mismatch:\n got: %q\nwant: %q", i, records[i], wantRecord)
		}
	}
}
