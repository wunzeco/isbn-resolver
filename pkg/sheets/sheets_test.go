package sheets

import (
	"testing"
)

func TestExtractSheetID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Full URL",
			input:    "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit",
			expected: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms",
		},
		{
			name:     "URL with additional params",
			input:    "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit#gid=0",
			expected: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms",
		},
		{
			name:     "Just ID",
			input:    "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms",
			expected: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractSheetID(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractSheetID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestValidateRange(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		shouldErr bool
	}{
		{
			name:      "Valid range with sheet name",
			input:     "Sheet1!A2:A",
			shouldErr: false,
		},
		{
			name:      "Valid range without sheet name",
			input:     "A2:A100",
			shouldErr: false,
		},
		{
			name:      "Valid full column range",
			input:     "A:A",
			shouldErr: false,
		},
		{
			name:      "Invalid - no colon or exclamation",
			input:     "A2",
			shouldErr: true,
		},
		{
			name:      "Invalid - empty range",
			input:     "",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateRange(tt.input)
			if (err != nil) != tt.shouldErr {
				t.Errorf("ValidateRange(%q) error = %v, shouldErr = %v", tt.input, err, tt.shouldErr)
			}
		})
	}
}
