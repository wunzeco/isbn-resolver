package isbn

import (
	"regexp"
	"strconv"
	"strings"
)

// ValidationType represents the type of ISBN validation result
type ValidationType int

const (
	Invalid ValidationType = iota
	ISBN10
	ISBN13
)

// ValidationResult contains the validation result and normalized ISBN
type ValidationResult struct {
	Type       ValidationType
	Normalized string
	Error      string
}

var (
	isbn10Regex = regexp.MustCompile(`^(?:\d{9}[\dXx]|\d{1,5}-\d{1,7}-\d{1,7}-[\dXx])$`)
	isbn13Regex = regexp.MustCompile(`^(?:97[89]\d{10}|97[89]-\d{1,5}-\d{1,7}-\d{1,7}-\d)$`)
)

// Validate validates an ISBN-10 or ISBN-13 and returns the validation result
func Validate(isbn string) ValidationResult {
	// Remove hyphens and spaces
	normalized := strings.ReplaceAll(strings.ReplaceAll(isbn, "-", ""), " ", "")

	// Try ISBN-13 first
	if len(normalized) == 13 {
		if validateISBN13(normalized) {
			return ValidationResult{
				Type:       ISBN13,
				Normalized: normalized,
			}
		}
		return ValidationResult{
			Type:  Invalid,
			Error: "Invalid ISBN-13 checksum",
		}
	}

	// Try ISBN-10
	if len(normalized) == 10 {
		if validateISBN10(normalized) {
			return ValidationResult{
				Type:       ISBN10,
				Normalized: normalized,
			}
		}
		return ValidationResult{
			Type:  Invalid,
			Error: "Invalid ISBN-10 checksum",
		}
	}

	return ValidationResult{
		Type:  Invalid,
		Error: "Invalid ISBN format (must be 10 or 13 digits)",
	}
}

// validateISBN10 validates an ISBN-10 checksum
func validateISBN10(isbn string) bool {
	if len(isbn) != 10 {
		return false
	}

	sum := 0
	for i := 0; i < 9; i++ {
		digit := int(isbn[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		sum += digit * (10 - i)
	}

	// Handle the check digit (can be X for 10)
	var checkDigit int
	if isbn[9] == 'X' || isbn[9] == 'x' {
		checkDigit = 10
	} else {
		checkDigit = int(isbn[9] - '0')
		if checkDigit < 0 || checkDigit > 9 {
			return false
		}
	}

	sum += checkDigit
	return sum%11 == 0
}

// validateISBN13 validates an ISBN-13 checksum
func validateISBN13(isbn string) bool {
	if len(isbn) != 13 {
		return false
	}

	// Must start with 978 or 979
	if !strings.HasPrefix(isbn, "978") && !strings.HasPrefix(isbn, "979") {
		return false
	}

	sum := 0
	for i := 0; i < 12; i++ {
		digit, err := strconv.Atoi(string(isbn[i]))
		if err != nil {
			return false
		}
		if i%2 == 0 {
			sum += digit
		} else {
			sum += digit * 3
		}
	}

	checkDigit, err := strconv.Atoi(string(isbn[12]))
	if err != nil {
		return false
	}

	checksum := (10 - (sum % 10)) % 10
	return checksum == checkDigit
}

// ConvertISBN10to13 converts an ISBN-10 to ISBN-13
func ConvertISBN10to13(isbn10 string) string {
	if len(isbn10) != 10 {
		return ""
	}

	// Remove check digit and prepend 978
	base := "978" + isbn10[:9]

	// Calculate new checksum
	sum := 0
	for i := 0; i < 12; i++ {
		digit := int(base[i] - '0')
		if i%2 == 0 {
			sum += digit
		} else {
			sum += digit * 3
		}
	}

	checksum := (10 - (sum % 10)) % 10
	return base + strconv.Itoa(checksum)
}
