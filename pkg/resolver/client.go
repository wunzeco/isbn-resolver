package resolver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// BookMetadata represents book information
type BookMetadata struct {
	ISBN            string   `json:"isbn"`
	ISBN10          string   `json:"isbn_10,omitempty"`
	ISBN13          string   `json:"isbn_13,omitempty"`
	Title           string   `json:"title"`
	Authors         []string `json:"authors"`
	Publisher       string   `json:"publisher"`
	PublicationDate string   `json:"publication_date"`
	Pages           int      `json:"pages"`
	Categories      []string `json:"categories"`
	Error           string   `json:"error,omitempty"`
}

// APIClient handles API requests to book metadata services
type APIClient struct {
	httpClient *http.Client
	timeout    time.Duration
}

// NewAPIClient creates a new API client
func NewAPIClient(timeout time.Duration) *APIClient {
	return &APIClient{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
	}
}

// Resolve fetches book metadata for an ISBN
func (c *APIClient) Resolve(isbn string) (*BookMetadata, error) {
	// Try Open Library API first
	metadata, err := c.fetchFromOpenLibrary(isbn)
	if err == nil && metadata != nil {
		return metadata, nil
	}

	// Fallback to Google Books API
	metadata, err = c.fetchFromGoogleBooks(isbn)
	if err == nil && metadata != nil {
		return metadata, nil
	}

	return nil, fmt.Errorf("failed to resolve ISBN from all APIs")
}

// fetchFromOpenLibrary fetches book data from Open Library API
func (c *APIClient) fetchFromOpenLibrary(isbn string) (*BookMetadata, error) {
	apiURL := fmt.Sprintf("https://openlibrary.org/api/books?bibkeys=ISBN:%s&format=json&jscmd=data", isbn)

	resp, err := c.httpClient.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	key := "ISBN:" + isbn
	bookData, ok := result[key].(map[string]interface{})
	if !ok || bookData == nil {
		return nil, fmt.Errorf("no data found for ISBN")
	}

	metadata := &BookMetadata{
		ISBN: isbn,
	}

	// Extract title
	if title, ok := bookData["title"].(string); ok {
		metadata.Title = title
	}

	// Extract authors
	if authorsData, ok := bookData["authors"].([]interface{}); ok {
		for _, author := range authorsData {
			if authorMap, ok := author.(map[string]interface{}); ok {
				if name, ok := authorMap["name"].(string); ok {
					metadata.Authors = append(metadata.Authors, name)
				}
			}
		}
	}

	// Extract publishers
	if publishersData, ok := bookData["publishers"].([]interface{}); ok {
		if len(publishersData) > 0 {
			if publisher, ok := publishersData[0].(map[string]interface{}); ok {
				if name, ok := publisher["name"].(string); ok {
					metadata.Publisher = name
				}
			}
		}
	}

	// Extract publication date
	if pubDate, ok := bookData["publish_date"].(string); ok {
		metadata.PublicationDate = pubDate
	}

	// Extract number of pages
	if pages, ok := bookData["number_of_pages"].(float64); ok {
		metadata.Pages = int(pages)
	}

	// Extract subjects (categories)
	if subjectsData, ok := bookData["subjects"].([]interface{}); ok {
		for _, subject := range subjectsData {
			if subjectMap, ok := subject.(map[string]interface{}); ok {
				if name, ok := subjectMap["name"].(string); ok {
					metadata.Categories = append(metadata.Categories, name)
				}
			}
		}
	}

	return metadata, nil
}

// fetchFromGoogleBooks fetches book data from Google Books API
func (c *APIClient) fetchFromGoogleBooks(isbn string) (*BookMetadata, error) {
	apiURL := fmt.Sprintf("https://www.googleapis.com/books/v1/volumes?q=isbn:%s", url.QueryEscape(isbn))

	resp, err := c.httpClient.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result struct {
		TotalItems int `json:"totalItems"`
		Items      []struct {
			VolumeInfo struct {
				Title               string   `json:"title"`
				Authors             []string `json:"authors"`
				Publisher           string   `json:"publisher"`
				PublishedDate       string   `json:"publishedDate"`
				PageCount           int      `json:"pageCount"`
				Language            string   `json:"language"`
				Categories          []string `json:"categories"`
				IndustryIdentifiers []struct {
					Type       string `json:"type"`
					Identifier string `json:"identifier"`
				} `json:"industryIdentifiers"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.TotalItems == 0 {
		return nil, fmt.Errorf("no data found for ISBN")
	}

	volumeInfo := result.Items[0].VolumeInfo

	metadata := &BookMetadata{
		ISBN:            isbn,
		Title:           volumeInfo.Title,
		Authors:         volumeInfo.Authors,
		Publisher:       volumeInfo.Publisher,
		PublicationDate: volumeInfo.PublishedDate,
		Pages:           volumeInfo.PageCount,
		Categories:      volumeInfo.Categories,
	}

	// Extract ISBN-10 and ISBN-13 from industry identifiers
	for _, id := range volumeInfo.IndustryIdentifiers {
		switch id.Type {
		case "ISBN_10":
			metadata.ISBN10 = id.Identifier
		case "ISBN_13":
			metadata.ISBN13 = id.Identifier
		}
	}

	return metadata, nil
}
