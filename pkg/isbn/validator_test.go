package isbn

import (
	"testing"
)

func TestValidateISBN10(t *testing.T) {
	tests := []struct {
		name     string
		isbn     string
		expected ValidationType
	}{
		{
			name:     "Valid ISBN-10",
			isbn:     "0596520689",
			expected: ISBN10,
		},
		{
			name:     "Valid ISBN-10 with X",
			isbn:     "043942089X",
			expected: ISBN10,
		},
		{
			name:     "Valid ISBN-10 with hyphens",
			isbn:     "0-596-52068-9",
			expected: ISBN10,
		},
		{
			name:     "Invalid ISBN-10 checksum",
			isbn:     "0596520688",
			expected: Invalid,
		},
		{
			name:     "Invalid ISBN-10 length",
			isbn:     "059652068",
			expected: Invalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Validate(tt.isbn)
			if result.Type != tt.expected {
				t.Errorf("Validate(%q) = %v, want %v", tt.isbn, result.Type, tt.expected)
			}
		})
	}
}

func TestValidateISBN13(t *testing.T) {
	tests := []struct {
		name     string
		isbn     string
		expected ValidationType
	}{
		{
			name:     "Valid ISBN-13",
			isbn:     "9780134190440",
			expected: ISBN13,
		},
		{
			name:     "Valid ISBN-13 with hyphens",
			isbn:     "978-0-13-419044-0",
			expected: ISBN13,
		},
		{
			name:     "Invalid ISBN-13 checksum",
			isbn:     "9780134190441",
			expected: Invalid,
		},
		{
			name:     "Invalid ISBN-13 prefix",
			isbn:     "9990134190440",
			expected: Invalid,
		},
		{
			name:     "Invalid ISBN-13 length",
			isbn:     "978013419044",
			expected: Invalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Validate(tt.isbn)
			if result.Type != tt.expected {
				t.Errorf("Validate(%q) = %v, want %v (error: %s)", tt.isbn, result.Type, tt.expected, result.Error)
			}
		})
	}
}

func TestConvertISBN10to13(t *testing.T) {
	tests := []struct {
		name     string
		isbn10   string
		expected string
	}{
		{
			name:     "Convert valid ISBN-10",
			isbn10:   "0596520689",
			expected: "9780596520687",
		},
		{
			name:     "Convert ISBN-10 with X",
			isbn10:   "043942089X",
			expected: "9780439420891",
		},
		{
			name:     "Invalid length",
			isbn10:   "059652068",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertISBN10to13(tt.isbn10)
			if result != tt.expected {
				t.Errorf("ConvertISBN10to13(%q) = %q, want %q", tt.isbn10, result, tt.expected)
			}
		})
	}
}

func TestValidateISBN10Checksum(t *testing.T) {
	tests := []struct {
		name     string
		isbn     string
		expected bool
	}{
		{"Valid checksum", "0596520689", true},
		{"Valid checksum with X", "043942089X", true},
		{"Invalid checksum", "0596520688", false},
		{"Wrong length", "059652068", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateISBN10(tt.isbn)
			if result != tt.expected {
				t.Errorf("validateISBN10(%q) = %v, want %v", tt.isbn, result, tt.expected)
			}
		})
	}
}

func TestValidateISBN13Checksum(t *testing.T) {
	tests := []struct {
		name     string
		isbn     string
		expected bool
	}{
		{"Valid checksum", "9780134190440", true},
		{"Valid checksum 979", "9791234567896", true},
		{"Invalid checksum", "9780134190441", false},
		{"Wrong length", "978013419044", false},
		{"Invalid prefix", "9990134190440", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateISBN13(tt.isbn)
			if result != tt.expected {
				t.Errorf("validateISBN13(%q) = %v, want %v", tt.isbn, result, tt.expected)
			}
		})
	}
}
