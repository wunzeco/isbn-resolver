# ISBN Resolver Tool - Development Prompt

## Project Overview

Build a command-line tool in Golang that takes a list of ISBN (International Standard Book Number) numbers and resolves each one to retrieve book metadata.

## Requirements

### Core Functionality

1. **Input Handling**
   - Accept a list of ISBN numbers (ISBN-10 or ISBN-13 format)
   - Support multiple input methods:
     - Command-line arguments
     - Reading from a file (one ISBN per line)
     - Reading from stdin

2. **ISBN Validation**
   - Validate ISBN-10 format (10 digits with optional hyphens)
   - Validate ISBN-13 format (13 digits with optional hyphens)
   - Implement checksum validation for both formats
   - Provide clear error messages for invalid ISBNs

3. **ISBN Resolution**
   - Query external API(s) to retrieve book metadata
   - Suggested APIs:
     - Open Library API (https://openlibrary.org/dev/docs/api/books)
     - Google Books API
     - ISBNdb API
   - Implement fallback mechanisms if primary API fails
   - Handle rate limiting appropriately

4. **Output Format**
   - Display the following information for each ISBN:
     - ISBN-13
     - Title
     - Author(s)
     - Publisher
     - Publication date
     - Number of pages
     - Categories
   - Support multiple output formats:
     - Human-readable text
     - JSON
     - CSV

### Technical Requirements

1. **Language & Structure**
   - Written in Go (Golang)
   - Use idiomatic Go patterns and conventions
   - Organize code into logical packages
   - Include proper error handling
   - Ensure that the go package is under `github.com/wunzeco/isbn-resolver`

2. **Configuration**
   - Support configuration via:
     - Command-line flags
     - Environment variables
     - Configuration file (e.g., YAML or JSON)
   - Allow configuring:
     - API endpoint(s)
     - API keys (if required)
     - Timeout values
     - Output format

3. **Error Handling**
   - Gracefully handle network errors
   - Continue processing remaining ISBNs if one fails
   - Provide summary of successful and failed lookups
   - Include verbose/debug mode for troubleshooting

4. **Dependencies**
   - Minimize external dependencies
   - Use standard library where possible
   - Recommended packages:
     - `net/http` for API calls
     - `encoding/json` for JSON parsing
     - `flag` or a CLI framework (e.g., `cobra`, `urfave/cli`)

### Optional Enhancements

1. **Caching**
   - Implement local caching to avoid redundant API calls
   - Support both in-memory and file-based caching
   - Include cache expiration mechanism

2. **Batch Processing**
   - Optimize API calls by batching multiple ISBNs when API supports it
   - Implement progress reporting for large batches

3. **Testing**
   - Write unit tests for ISBN validation
   - Include integration tests for API calls (with mocking)
   - Aim for >80% code coverage

4. **Documentation**
   - Include comprehensive README with:
     - Installation instructions
     - Usage examples
     - API configuration guide
     - Troubleshooting section
   - Add inline code documentation
   - Generate godoc-compatible documentation

## Example Usage

```bash
# Single ISBN via command-line argument
isbn-resolver 978-0134190440

# Multiple ISBNs
isbn-resolver 978-0134190440 0-596-52068-9 978-0-13-110362-7

# From file
isbn-resolver --file isbns.txt

# From stdin with JSON output
cat isbns.txt | isbn-resolver --format json

# Verbose mode
isbn-resolver --verbose --file isbns.txt
```

## Expected Output Format

### Text Format
```
ISBN: 978-0134190440
Title: The Go Programming Language
Authors: Alan A. A. Donovan, Brian W. Kernighan
Publisher: Addison-Wesley Professional
Publication Date: 2015-11-16
Pages: 400
Status: ✓ Resolved

---

ISBN: 0-596-52068-9
Title: [Title not found]
Status: ✗ Failed - Invalid ISBN or not found in database
```

### JSON Format
```json
{
  "results": [
    {
      "isbn": "978-0134190440",
      "status": "success",
      "data": {
        "title": "The Go Programming Language",
        "authors": ["Alan A. A. Donovan", "Brian W. Kernighan"],
        "publisher": "Addison-Wesley Professional",
        "publication_date": "2015-11-16",
        "pages": 400
      }
    },
    {
      "isbn": "0-596-52068-9",
      "status": "error",
      "error": "ISBN not found in database"
    }
  ],
  "summary": {
    "total": 2,
    "successful": 1,
    "failed": 1
  }
}
```

## Implementation Guidelines

1. Start with a minimal working version that validates ISBNs and queries a single API
2. Implement multiple output formats
3. Add configuration options and error handling
4. Include tests and documentation
5. Consider optional enhancements based on use case

## Deliverables

- Complete Go source code with proper package structure
- README.md with installation and usage instructions
- Unit and integration tests
- Example configuration file
- Sample ISBN list file for testing
- Binary releases for major platforms (Linux, macOS, Windows)
