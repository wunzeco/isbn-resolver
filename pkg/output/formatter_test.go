package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
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

// TestSummarize pins the counting rule the whole run reports against: rows,
// not unique ISBNs. The errors map is keyed by ISBN, so a duplicated failing
// ISBN holds one entry while occupying two output rows — len(results) minus
// len(errors) therefore over-counts successes, which is exactly how the
// verbose summary and the JSON summary came to disagree about the same run.
func TestSummarize(t *testing.T) {
	rows := func(isbns ...string) []resolver.BookMetadata {
		results := make([]resolver.BookMetadata, 0, len(isbns))
		for _, isbnStr := range isbns {
			results = append(results, resolver.BookMetadata{ISBN: isbnStr})
		}

		return results
	}

	failures := func(isbns ...string) map[string]error {
		errs := make(map[string]error, len(isbns))
		for _, isbnStr := range isbns {
			errs[isbnStr] = fmt.Errorf("no data found for ISBN")
		}

		return errs
	}

	tests := []struct {
		name    string
		results []resolver.BookMetadata
		errors  map[string]error
		want    Summary
	}{
		{
			name:    "all successful",
			results: rows("9780134190440", "9780132350884"),
			errors:  failures(),
			want:    Summary{Total: 2, Successful: 2, Failed: 0},
		},
		{
			name:    "a duplicated failing ISBN counts once per row",
			results: rows("9780134190440", "9780132350884", "9780132350884"),
			errors:  failures("9780132350884"),
			want:    Summary{Total: 3, Successful: 1, Failed: 2},
		},
		{
			name:    "a duplicated succeeding ISBN counts once per row",
			results: rows("9780134190440", "9780134190440"),
			errors:  failures(),
			want:    Summary{Total: 2, Successful: 2, Failed: 0},
		},
		{
			name:    "no results",
			results: nil,
			errors:  failures(),
			want:    Summary{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Summarize(tt.results, tt.errors); got != tt.want {
				t.Errorf("Summarize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestFormatJSONSummaryMatchesSummarize keeps the JSON block and the exported
// tally from drifting apart: the block is what a downstream consumer reads,
// Summarize is what the verbose line prints.
func TestFormatJSONSummaryMatchesSummarize(t *testing.T) {
	results := []resolver.BookMetadata{
		{ISBN: "9780134190440", Title: "The Go Programming Language"},
		{ISBN: "9780132350884"},
		{ISBN: "9780132350884"},
	}
	errors := map[string]error{"9780132350884": fmt.Errorf("no data found for ISBN")}

	buf := &bytes.Buffer{}
	if err := NewFormatter(FormatJSON, buf).FormatBatch(results, errors); err != nil {
		t.Fatalf("FormatBatch failed: %v", err)
	}

	var decoded struct {
		Results []struct {
			Status string `json:"status"`
		} `json:"results"`
		Summary Summary `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding %q: %v", buf.String(), err)
	}

	if want := Summarize(results, errors); decoded.Summary != want {
		t.Errorf("JSON summary = %+v, want %+v", decoded.Summary, want)
	}

	// The summary must also match the rows actually emitted, which is the
	// property a reader of the JSON checks it against.
	if len(decoded.Results) != decoded.Summary.Total {
		t.Errorf("JSON has %d result rows but reports total %d", len(decoded.Results), decoded.Summary.Total)
	}

	var failed int
	for _, entry := range decoded.Results {
		if entry.Status == "error" {
			failed++
		}
	}
	if failed != decoded.Summary.Failed {
		t.Errorf("JSON has %d error rows but reports %d failed", failed, decoded.Summary.Failed)
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
