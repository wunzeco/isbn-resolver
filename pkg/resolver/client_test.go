package resolver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveOpenLibrarySuccess(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ISBN:9780134190440": map[string]interface{}{
				"title": "The Go Programming Language",
				"authors": []map[string]interface{}{
					{"name": "Alan Donovan"},
					{"name": "Brian Kernighan"},
				},
				"publishers": []map[string]interface{}{
					{"name": "Addison-Wesley"},
				},
				"publish_date":    "2015",
				"number_of_pages": 380,
				"subjects": []map[string]interface{}{
					{"name": "Go (Computer program language)"},
				},
			},
		})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Google Books should not be called when Open Library succeeds")
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	metadata, err := client.Resolve("9780134190440")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if metadata.Title != "The Go Programming Language" {
		t.Errorf("Title = %q, want %q", metadata.Title, "The Go Programming Language")
	}
	if metadata.Publisher != "Addison-Wesley" {
		t.Errorf("Publisher = %q, want %q", metadata.Publisher, "Addison-Wesley")
	}
	if metadata.Pages != 380 {
		t.Errorf("Pages = %d, want 380", metadata.Pages)
	}
	if len(metadata.Authors) != 2 {
		t.Errorf("Authors = %v, want 2 authors", metadata.Authors)
	}
}

func TestResolveFallsBackToGoogleBooks(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"totalItems": 1,
			"items": []map[string]interface{}{
				{
					"volumeInfo": map[string]interface{}{
						"title":         "The Go Programming Language",
						"authors":       []string{"Alan Donovan", "Brian Kernighan"},
						"publisher":     "Addison-Wesley",
						"publishedDate": "2015-10-26",
						"pageCount":     380,
						"categories":    []string{"Computers"},
						"industryIdentifiers": []map[string]string{
							{"type": "ISBN_10", "identifier": "0134190440"},
							{"type": "ISBN_13", "identifier": "9780134190440"},
						},
					},
				},
			},
		})
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	metadata, err := client.Resolve("9780134190440")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if metadata.Title != "The Go Programming Language" {
		t.Errorf("Title = %q, want %q", metadata.Title, "The Go Programming Language")
	}
	if metadata.ISBN10 != "0134190440" {
		t.Errorf("ISBN10 = %q, want %q", metadata.ISBN10, "0134190440")
	}
	if metadata.ISBN13 != "9780134190440" {
		t.Errorf("ISBN13 = %q, want %q", metadata.ISBN13, "9780134190440")
	}
}

func TestResolveFailsWhenBothAPIsHaveNoData(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer openLibrary.Close()

	googleBooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"totalItems": 0})
	}))
	defer googleBooks.Close()

	client := NewAPIClient(5 * time.Second)
	client.OpenLibraryBaseURL = openLibrary.URL
	client.GoogleBooksBaseURL = googleBooks.URL

	_, err := client.Resolve("0000000000")
	if err == nil {
		t.Fatal("expected an error when neither API has data, got nil")
	}
}
