# ISBN Resolver

A high-performance command-line tool written in Go that resolves ISBN numbers to retrieve comprehensive book metadata from multiple sources.

## Features

- ✅ **ISBN Validation**: Validates both ISBN-10 and ISBN-13 formats with checksum verification
-  **Multiple Input Methods**: Accepts ISBNs via command-line arguments, files, stdin, or Google Sheets
- 📊 **Multiple Output Formats**: Supports text, JSON, and CSV output formats
- 📋 **Google Sheets Integration**: Read ISBNs from and write results to Google Sheets
- 🌐 **API Fallback**: Queries multiple APIs (Open Library, Google Books) with automatic fallback
- ⚙️ **Flexible Configuration**: Configure via command-line flags, environment variables, or config files
- 🛡️ **Robust Error Handling**: Continues processing even when individual lookups fail

## Installation

### Prerequisites

- Go 1.21 or later

### Build from Source

```bash
git clone https://github.com/wunzeco/isbn-resolver.git
cd isbn-resolver
go build -o isbn-resolver ./cmd/isbn-resolver
```

### Install

```bash
go install github.com/wunzeco/isbn-resolver/cmd/isbn-resolver@latest
```

## Usage

### Basic Usage

```bash
# Single ISBN
isbn-resolver 978-0134190440

# Multiple ISBNs
isbn-resolver 978-0134190440 0-596-52068-9 978-0132350884
```

### Read from File

```bash
# Create a file with ISBNs (one per line)
isbn-resolver --file examples/sample-isbns.txt
```

### Read from stdin

```bash
# Pipe ISBNs to the tool
cat isbns.txt | isbn-resolver

# With format option
echo "978-0134190440" | isbn-resolver --format json
```

### Output Formats

#### Text Format (Default)
```bash
isbn-resolver 978-0134190440
```

Output:
```
ISBN: 978-0134190440
Title: The Go Programming Language
Authors: Alan A. A. Donovan, Brian W. Kernighan
Publisher: Addison-Wesley Professional
Publication Date: 2015-11-16
Pages: 400
Language: English
Categories: Programming, Computer Science
Status: ✓ Resolved

---
```

#### JSON Format
```bash
isbn-resolver --format json 978-0134190440
```

Output:
```json
{
  "results": [
    {
      "isbn": "978-0134190440",
      "status": "success",
      "data": {
        "isbn": "978-0134190440",
        "title": "The Go Programming Language",
        "authors": ["Alan A. A. Donovan", "Brian W. Kernighan"],
        "publisher": "Addison-Wesley Professional",
        "publication_date": "2015-11-16",
        "pages": 400,
        "language": "English",
        "categories": ["Programming", "Computer Science"]
      }
    }
  ],
  "summary": {
    "total": 1,
    "successful": 1,
    "failed": 0
  }
}
```

#### CSV Format
```bash
isbn-resolver --format csv --file isbns.txt > output.csv
```

### Advanced Options

```bash
# Custom timeout
isbn-resolver --timeout 60s 978-0134190440

# Verbose mode for debugging
isbn-resolver --verbose --file isbns.txt

# Using a configuration file
isbn-resolver --config config.json --file isbns.txt
```

### Google Sheets Integration

Read ISBNs from Google Sheets and write results back:

```bash
# Set up credentials (one-time setup)
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account.json"

# Read from Google Sheets and write results back
isbn-resolver --sheets-url "https://docs.google.com/spreadsheets/d/SHEET_ID/edit" \
              --sheets-range "Sheet1!A2:A"

# Using sheet ID directly
isbn-resolver --sheets-id "SHEET_ID" \
              --sheets-range "ISBNs!A2:A" \
              --sheets-output-range "ISBNs!B2:J"

# Create a new tab for results
isbn-resolver --sheets-url "URL" \
              --sheets-range "Input!A2:A" \
              --sheets-create-tab "Resolved Books"

# Preview changes without writing (dry run)
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --sheets-dry-run

# With verbose output
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --verbose
```

See [GOOGLE_SHEETS.md](GOOGLE_SHEETS.md) for detailed setup instructions.

## Configuration

### Command-Line Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--timeout` | API request timeout | 30s |
| `--file` | Input file with ISBNs | - |
| `--format` | Output format (text, json, csv) | text |
| `--verbose` | Enable verbose logging | false |
| `--config` | Configuration file path | - |
| `--sheets-url` | Google Sheets URL | - |
| `--sheets-id` | Google Sheets ID | - |
| `--sheets-range` | Cell range for ISBNs | - |
| `--sheets-credentials` | Path to credentials file | - |
| `--sheets-output-range` | Where to write results | - |
| `--sheets-create-tab` | Create new tab for results | - |
| `--sheets-dry-run` | Preview without writing | false |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ISBN_TIMEOUT` | API request timeout |
| `ISBN_FORMAT` | Output format |
| `ISBN_VERBOSE` | Enable verbose mode (true/false) |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to service account JSON |
| `GOOGLE_SHEETS_CREDENTIALS` | Alternative credentials path |

### Configuration File

Create a JSON configuration file:

```json
{
  "timeout": "30s",
  "format": "json",
  "verbose": false
}
```

Use it with:
```bash
isbn-resolver --config config.json --file isbns.txt
```

## Project Structure

```
isbn-resolver/
├── cmd/
│   └── isbn-resolver/
│       └── main.go           # Application entry point
├── pkg/
│   ├── isbn/
│   │   ├── validator.go      # ISBN validation logic
│   │   └── validator_test.go # ISBN validation tests
│   ├── resolver/
│   │   └── client.go         # API client for book metadata
│   ├── output/
│   │   ├── formatter.go      # Output formatting logic
│   │   └── formatter_test.go # Formatter tests
│   ├── sheets/
│   │   ├── auth.go           # Google Sheets authentication
│   │   ├── client.go         # Sheets API client
│   │   ├── reader.go         # Read ISBNs from sheets
│   │   ├── writer.go         # Write metadata to sheets
│   │   └── sheets_test.go    # Sheets tests
│   └── config/
│       └── config.go         # Configuration management
├── examples/
│   ├── sample-isbns.txt      # Sample ISBN list
│   └── config.json           # Sample configuration
├── go.mod
├── go.sum
└── README.md
```

## Development

### Running Tests

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run tests with verbose output
go test -v ./...

# Generate coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Building

```bash
# Build for current platform
go build -o isbn-resolver ./cmd/isbn-resolver

# Build for multiple platforms
GOOS=linux GOARCH=amd64 go build -o isbn-resolver-linux-amd64 ./cmd/isbn-resolver
GOOS=darwin GOARCH=amd64 go build -o isbn-resolver-darwin-amd64 ./cmd/isbn-resolver
GOOS=windows GOARCH=amd64 go build -o isbn-resolver-windows-amd64.exe ./cmd/isbn-resolver
```

## API Sources

The tool queries the following APIs with automatic fallback:

1. **Open Library API** (Primary)
   - URL: https://openlibrary.org/dev/docs/api/books
   - No API key required
   - Free to use

2. **Google Books API** (Fallback)
   - URL: https://developers.google.com/books
   - No API key required for basic usage
   - Rate limits apply

## Troubleshooting

### No Results Found

If an ISBN returns no results:
- Verify the ISBN is valid using an online ISBN checker
- The book might not be in the databases yet (very new or obscure titles)
- Try with both ISBN-10 and ISBN-13 formats

### API Timeouts

If you're experiencing timeouts:
- Increase the timeout: `--timeout 60s`
- Check your internet connection
- Try again later (API might be experiencing high load)

### Rate Limiting

If you're processing many ISBNs:
- Add delays between requests
- Consider implementing caching for frequently queried ISBNs

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Acknowledgments

- [Open Library](https://openlibrary.org/) for providing free book metadata API
- [Google Books](https://books.google.com/) for additional book information
- The Go community for excellent tools and libraries

## Roadmap

- [ ] Add caching support (in-memory and file-based)
- [ ] Implement batch API requests where supported
- [ ] Add support for more APIs (ISBNdb, WorldCat, etc.)
- [ ] Create web interface
- [ ] Add database storage for resolved ISBNs
- [ ] Implement retry logic with exponential backoff
- [ ] Add Docker support
- [ ] Create GitHub Actions for CI/CD
