package sheets

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// docsPath is GOOGLE_SHEETS.md relative to this package directory.
const docsPath = "../../GOOGLE_SHEETS.md"

// TestDocumentedOutputColumnsMatchTheWriter pins GOOGLE_SHEETS.md §"Output
// Format" to the header row formatResultsForSheet actually writes.
//
// The table drifted badly enough to be wrong in three separate ways at once —
// it named Status as the first output column, invented a Language column that
// resolver.BookMetadata has no field for, and listed Status two positions off.
// A hand-maintained table describing a layout defined in code will drift again,
// and this one is no longer only documentation: --sheet-cache decodes these
// exact columns, so a reader who lays their sheet out to match a stale table
// gets a cache that silently never hits rather than a visible error.
func TestDocumentedOutputColumnsMatchTheWriter(t *testing.T) {
	c := &Client{}
	rows := c.formatResultsForSheet(nil, nil)
	if len(rows) != 1 {
		t.Fatalf("expected only a header row for empty results, got %d rows", len(rows))
	}

	want := make([]string, 0, len(rows[0]))
	for _, cell := range rows[0] {
		want = append(want, cell.(string))
	}

	// Guards the assertion below: if the header stops being outputColumns wide,
	// the two constants have diverged and ReadExistingStatus is already broken.
	if len(want) != outputColumns {
		t.Fatalf("header row has %d columns, outputColumns says %d", len(want), outputColumns)
	}

	got := documentedOutputFields(t)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GOOGLE_SHEETS.md §\"Output Format\" is stale.\n got: %v\nwant: %v\n"+
			"Correct the table in %s to match formatResultsForSheet; do not change the writer to match the docs.",
			got, want, docsPath)
	}
}

// documentedOutputFields extracts the Field column of the markdown table under
// the "## Output Format" heading, in document order.
func documentedOutputFields(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", docsPath, err)
	}

	const heading = "## Output Format"
	body := string(data)
	start := strings.Index(body, heading)
	if start < 0 {
		t.Fatalf("%s has no %q heading", docsPath, heading)
	}
	body = body[start+len(heading):]
	// Stop at the next section so a table further down the file can never be
	// mistaken for this one.
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}

	var fields []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		field := strings.TrimSpace(cells[1])
		// Skip the header and the |---|---| separator.
		if field == "Field" || strings.Trim(field, "-") == "" {
			continue
		}
		fields = append(fields, field)
	}

	if len(fields) == 0 {
		t.Fatalf("found no table rows under %q in %s", heading, docsPath)
	}
	return fields
}
