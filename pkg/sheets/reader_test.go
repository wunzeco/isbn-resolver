package sheets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"google.golang.org/api/option"
	sheetsapi "google.golang.org/api/sheets/v4"
)

// newTestClient stands an httptest server in for the Sheets API. The handler
// receives every request, so tests can both serve fixtures and assert on the
// range that was actually requested.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx := context.Background()
	service, err := sheetsapi.NewService(ctx,
		option.WithEndpoint(server.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	return NewClient(ctx, service)
}

// valuesHandler serves a Values.Get response holding the given rows.
func valuesHandler(t *testing.T, values [][]interface{}) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&sheetsapi.ValueRange{
			MajorDimension: "ROWS",
			Values:         values,
		}); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}
}

func TestReadExistingStatusPopulatedRange(t *testing.T) {
	values := [][]interface{}{
		{"ISBN-13", "Title", "Authors", "Publisher", "Publication Date", "Pages", "Categories", "Status", "Error"},
		{"9780134190440", "The Go Programming Language", "Alan A. A. Donovan, Brian W. Kernighan", "Addison-Wesley", "2015-11-16", "380", "Computers, Programming", "Success", ""},
		{"9780000000000", "", "", "", "", "", "", "Error", "failed to resolve ISBN from all APIs"},
	}

	client := newTestClient(t, valuesHandler(t, values))

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v", err)
	}

	if len(existing) != 2 {
		t.Fatalf("got %d rows, want 2: %#v", len(existing), existing)
	}

	// Keyed by cache.Key so the sheet cache and the local cache agree on
	// identity for the same book.
	success, ok := existing[cache.Key("9780134190440")]
	if !ok {
		t.Fatalf("success row missing; got keys %v", keysOf(existing))
	}
	if success.Status != cache.StatusSuccess {
		t.Errorf("status = %q, want %q", success.Status, cache.StatusSuccess)
	}
	if success.Metadata == nil {
		t.Fatal("success row has no metadata to reuse")
	}
	if got, want := success.Metadata.Title, "The Go Programming Language"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	if got, want := success.Metadata.Authors, []string{"Alan A. A. Donovan", "Brian W. Kernighan"}; !reflect.DeepEqual(got, want) {
		t.Errorf("authors = %#v, want %#v", got, want)
	}
	if got, want := success.Metadata.Categories, []string{"Computers", "Programming"}; !reflect.DeepEqual(got, want) {
		t.Errorf("categories = %#v, want %#v", got, want)
	}
	if got, want := success.Metadata.Pages, 380; got != want {
		t.Errorf("pages = %d, want %d", got, want)
	}
	if got, want := success.Metadata.ISBN13, "9780134190440"; got != want {
		t.Errorf("ISBN13 = %q, want %q", got, want)
	}

	failed, ok := existing[cache.Key("9780000000000")]
	if !ok {
		t.Fatalf("error row missing; got keys %v", keysOf(existing))
	}
	if failed.Status != cache.StatusError {
		t.Errorf("status = %q, want %q", failed.Status, cache.StatusError)
	}
	if got, want := failed.Error, "failed to resolve ISBN from all APIs"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	// An error row has no metadata worth carrying forward, and cache.Policy
	// doesn't require any to re-attempt it.
	if failed.Metadata != nil {
		t.Errorf("error row carried metadata %#v, want nil", failed.Metadata)
	}
}

func TestReadExistingStatusEmptyRange(t *testing.T) {
	// The first-ever run against a sheet: the output range exists but holds
	// nothing. That must read as "nothing cached", not as a failure.
	client := newTestClient(t, valuesHandler(t, nil))

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("got %d rows, want 0: %#v", len(existing), existing)
	}
}

func TestReadExistingStatusHeaderOnly(t *testing.T) {
	// A run that wrote a header but resolved nothing leaves exactly one row.
	// The header must not be mistaken for an ISBN.
	values := [][]interface{}{
		{"ISBN-13", "Title", "Authors", "Publisher", "Publication Date", "Pages", "Categories", "Status", "Error"},
	}

	client := newTestClient(t, valuesHandler(t, values))

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("got %d rows, want 0: %#v", len(existing), existing)
	}
}

func TestReadExistingStatusSkipsRowsWithoutAUsableStatus(t *testing.T) {
	// Rows a human added or partially edited carry no decision we can trust, so
	// they must fall through to a normal resolution rather than being guessed at.
	values := [][]interface{}{
		{"9780134190440", "The Go Programming Language", "", "", "", "", "", "", ""},
		{"9780596520687", "Some Book", "", "", "", "", "", "Pending", ""},
		{"", "orphan row", "", "", "", "", "", "Success", ""},
	}

	client := newTestClient(t, valuesHandler(t, values))

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("got %d rows, want 0: %#v", len(existing), existing)
	}
}

func TestReadExistingStatusToleratesShortRows(t *testing.T) {
	// The Sheets API truncates trailing empty cells, so a successful row comes
	// back with eight columns rather than nine.
	values := [][]interface{}{
		{"9780134190440", "The Go Programming Language", "Alan A. A. Donovan", "Addison-Wesley", "2015-11-16", "380", "Computers", "Success"},
	}

	client := newTestClient(t, valuesHandler(t, values))

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v", err)
	}

	entry, ok := existing[cache.Key("9780134190440")]
	if !ok {
		t.Fatalf("row missing; got keys %v", keysOf(existing))
	}
	if entry.Status != cache.StatusSuccess {
		t.Errorf("status = %q, want %q", entry.Status, cache.StatusSuccess)
	}
	if entry.Error != "" {
		t.Errorf("error = %q, want empty", entry.Error)
	}
}

func TestReadExistingStatusKeysISBN10AsISBN13(t *testing.T) {
	// The writer falls back to the original ISBN when no ISBN-13 was resolved,
	// so an ISBN-10 can legitimately appear in the first column. It has to key
	// the same way the local cache keys it or the two caches disagree.
	values := [][]interface{}{
		{"0-13-419044-0", "The Go Programming Language", "", "", "", "", "", "Success", ""},
	}

	client := newTestClient(t, valuesHandler(t, values))

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v", err)
	}

	if _, ok := existing[cache.Key("9780134190440")]; !ok {
		t.Errorf("ISBN-10 row not keyed as ISBN-13; got keys %v", keysOf(existing))
	}
}

func TestReadExistingStatusMissingTabIsNotAnError(t *testing.T) {
	// --create-new-tab hasn't run yet, so the tab named by the output range
	// doesn't exist. The Sheets API reports that as a 400, but for the sheet
	// cache it is indistinguishable from "nothing resolved yet".
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(`{"error":{"code":400,"message":"Unable to parse range: Results!A1:I","status":"INVALID_ARGUMENT"}}`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	existing, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"})
	if err != nil {
		t.Fatalf("ReadExistingStatus() error = %v, want nil", err)
	}
	if len(existing) != 0 {
		t.Errorf("got %d rows, want 0", len(existing))
	}
}

func TestReadExistingStatusPropagatesRealErrors(t *testing.T) {
	// A permission failure is a genuine problem and must not be silently read
	// as an empty cache — that would resolve everything from scratch and then
	// fail again on write.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	if _, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "Results!A1"}); err == nil {
		t.Fatal("ReadExistingStatus() error = nil, want a permission error")
	}
}

func TestReadExistingStatusReadsAllOutputColumns(t *testing.T) {
	// The output range is a write *anchor* far more often than a bounded range,
	// and reading an anchor back returns one cell. Each case asserts the anchor
	// is widened to span all nine written columns.
	tests := []struct {
		name        string
		outputRange string
		want        string
	}{
		{name: "Anchor with tab", outputRange: "Results!A1", want: "Results!A1:I"},
		{name: "Anchor without tab", outputRange: "B1", want: "B1:J"},
		{name: "Anchor past column Z", outputRange: "Z2", want: "Z2:AH"},
		{name: "Empty defaults to the writer's default", outputRange: "", want: "B1:J"},
		{name: "Explicit range is honoured", outputRange: "Results!A1:I500", want: "Results!A1:I500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := outputReadRange(tt.outputRange)
			if err != nil {
				t.Fatalf("outputReadRange(%q) error = %v", tt.outputRange, err)
			}
			if got != tt.want {
				t.Errorf("outputReadRange(%q) = %q, want %q", tt.outputRange, got, tt.want)
			}
		})
	}
}

func TestReadExistingStatusRejectsUnparseableRange(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Sheets API called despite an invalid range")
	})

	if _, err := client.ReadExistingStatus(WriteConfig{SpreadsheetID: "sheet-id", OutputRange: "not a range"}); err == nil {
		t.Fatal("ReadExistingStatus() error = nil, want an invalid-range error")
	}
}

// TestReadAndWriteResolveTheSameOutputRange is what keeps the sheet cache
// pointed at the sheet the results are actually in.
//
// --create-new-tab redirects the write to a tab the configured output range
// never names. A reader that took OutputRange at face value would read a
// different tab: the sheet cache would silently never hit, or — if that other
// tab happened to carry a Status column — would hit on rows for a different
// data set entirely. Both sides therefore derive their range from one rule, and
// this test drives both against a live handler rather than testing that rule in
// isolation, so a caller that forgets to apply it still fails.
func TestReadAndWriteResolveTheSameOutputRange(t *testing.T) {
	tests := []struct {
		name   string
		config WriteConfig
	}{
		{
			name:   "New tab with no output range",
			config: WriteConfig{SpreadsheetID: "sheet-id", CreateNewTab: "Results"},
		},
		{
			name:   "New tab with a bare anchor",
			config: WriteConfig{SpreadsheetID: "sheet-id", CreateNewTab: "Results", OutputRange: "B1"},
		},
		{
			name:   "New tab with an already-qualified range",
			config: WriteConfig{SpreadsheetID: "sheet-id", CreateNewTab: "Results", OutputRange: "Other!C3"},
		},
		{
			name:   "No new tab and no output range",
			config: WriteConfig{SpreadsheetID: "sheet-id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var written, read string

			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")

				switch r.Method {
				case http.MethodPut:
					written = rangeFromPath(r.URL.Path)
					w.Write([]byte(`{}`))
				case http.MethodGet:
					read = rangeFromPath(r.URL.Path)
					json.NewEncoder(w).Encode(&sheetsapi.ValueRange{MajorDimension: "ROWS"})
				default:
					// The tab creation batchUpdate.
					w.Write([]byte(`{}`))
				}
			})

			if err := client.WriteResults(tt.config, nil, nil); err != nil {
				t.Fatalf("WriteResults() error = %v", err)
			}
			if _, err := client.ReadExistingStatus(tt.config); err != nil {
				t.Fatalf("ReadExistingStatus() error = %v", err)
			}

			// The read range is the write range widened to span every written
			// column — an anchor read back verbatim would return one cell — so
			// agreement means the read covers exactly what the write targets.
			want, err := outputReadRange(written)
			if err != nil {
				t.Fatalf("outputReadRange(%q) error = %v", written, err)
			}
			if read != want {
				t.Errorf("read range = %q, want %q (write range %q)", read, want, written)
			}
		})
	}
}

// rangeFromPath pulls the A1 range out of a Sheets values request path,
// ".../values/<range>".
func rangeFromPath(path string) string {
	_, rangeStr, found := strings.Cut(path, "/values/")
	if !found {
		return ""
	}
	return rangeStr
}

func keysOf(m map[string]ExistingRow) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
