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

## Caching & Performance

Repeated runs skip ISBNs that have already been resolved, and cache misses
are resolved concurrently under a bounded worker pool with rate-limit
backoff.

```bash
# Normal run: cache hits are skipped, only new ISBNs are resolved
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --cache-file ~/.isbn-resolver/cache.json

# Force re-resolution of every ISBN, refreshing the cache
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --resolve-all

# Re-attempt only previously-failed ISBNs
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --retry-failed

# Increase worker concurrency for a large first-time run
isbn-resolver --file isbns.txt --concurrency 10 --resolve-all

# Ad hoc run that shouldn't touch the cache
isbn-resolver 978-0134190440 --no-cache
```

- The cache is a JSON file (default `~/.isbn-resolver/cache.json`) keyed by
  normalized ISBN (ISBN-13 preferred), storing resolved metadata, status,
  error message, and last-attempt timestamp. Writes are atomic (temp file +
  rename) so a killed process can't corrupt it.
- On a normal run, cached successes *and* errors are reused without a
  network call. `--retry-failed` reuses successes but re-attempts cached
  errors. `--resolve-all` and `--no-cache` always hit the network;
  `--no-cache` additionally skips reading/writing the cache file.
  `--resolve-all` and `--retry-failed` are mutually exclusive.
- Cache misses are resolved across `--concurrency` workers (default 5) under
  a shared token-bucket rate limiter. HTTP 429/503 responses are retried
  with exponential backoff and jitter, honoring `Retry-After` when present.
- `--verbose` prints a cache breakdown (`Cache: 812 hit, 40 miss, 6 retried`)
  alongside the usual per-ISBN progress output.

### Using the sheet itself as the cache (`--sheet-cache`)

When the tool runs from CI, a container, or any other ephemeral environment,
`~/.isbn-resolver/cache.json` doesn't survive between runs — so every run
re-resolves the whole sheet from scratch. `--sheet-cache` uses the results
already written to the Google Sheets *output* range as the cache instead,
which does persist:

```bash
# Second and subsequent runs re-resolve only rows the sheet doesn't already
# have a Success for, even with no local cache file present
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" \
              --sheets-output-range "Sheet1!B2:J" --sheet-cache

# Re-attempt the rows the sheet marks Error, keep its successes
isbn-resolver --sheets-url "URL" --sheets-range "A2:A" --sheet-cache --retry-failed
```

- Off by default: it costs one extra read call per run and it assumes the
  output range holds the columns this tool writes
  (`ISBN-13, Title, Authors, Publisher, Publication Date, Pages, Categories, Status, Error`).
- Additive to the local file cache rather than a replacement. Both are
  consulted and a hit in *either* is enough to skip an ISBN, so a workstation
  cache and a shared sheet complement each other. `--no-cache` ignores both.
- `--resolve-all` and `--retry-failed` mean the same thing for the sheet
  cache as for the local one: `--resolve-all` re-resolves regardless of
  `Status`, `--retry-failed` keeps `Success` rows and re-attempts `Error`
  rows.
- Metadata is reused from the existing row, so a skipped row is rewritten
  with what the sheet already held instead of being blanked.
- Requires `--sheets-url`/`--sheets-id`; with no sheet configured it warns
  rather than silently doing nothing. Read failures (permissions, an
  unreachable API, an output range that doesn't exist yet) are warnings too —
  the run continues without the cache, since a cache can only ever save work.

### Google Books API key (optional)

Google Books requests are anonymous by default, which draws on a shared
per-IP quota. On large runs that quota can be exhausted mid-run, and every
subsequent ISBN then fails with a 429 that looks indistinguishable from
"this book isn't in the catalog" — on a 489-ISBN sample, 53 ISBNs reported
as unresolvable turned out to be resolvable once a key was in play.

Supply a key (from a Google Cloud project with the Books API enabled) to run
against that project's much higher quota instead:

```bash
# Preferred: keeps the key out of shell history and `ps` output
export ISBN_GOOGLE_BOOKS_API_KEY="your-key"
isbn-resolver --file isbns.txt

# One-off runs
isbn-resolver --file isbns.txt --google-books-api-key "your-key"
```

The key is entirely optional — with none set, the tool behaves exactly as it
always has. It is never printed: error messages that would otherwise embed
the request URL have the key replaced with `REDACTED`, which matters because
those messages reach stderr and are stored in the cache file.

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
| `--cache-file` | Resolution cache file path | `~/.isbn-resolver/cache.json` |
| `--concurrency` | Number of concurrent resolution workers | 5 |
| `--resolve-all` | Ignore cached entries and re-resolve every ISBN | false |
| `--retry-failed` | Reuse cached successes but re-attempt cached failures | false |
| `--no-cache` | Bypass the cache entirely for this run | false |
| `--sheet-cache` | Treat `Success` rows already in the Sheets output range as cache hits | false |
| `--google-books-api-key` | Google Books API key (optional; see below) | - |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ISBN_TIMEOUT` | API request timeout |
| `ISBN_FORMAT` | Output format |
| `ISBN_VERBOSE` | Enable verbose mode (true/false) |
| `ISBN_CACHE_FILE` | Resolution cache file path |
| `ISBN_CONCURRENCY` | Number of concurrent resolution workers |
| `ISBN_GOOGLE_BOOKS_API_KEY` | Google Books API key (optional) |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to service account JSON |
| `GOOGLE_SHEETS_CREDENTIALS` | Alternative credentials path |

### Configuration File

Create a JSON configuration file:

```json
{
  "timeout": "30s",
  "format": "json",
  "verbose": false,
  "cache_file": "~/.isbn-resolver/cache.json",
  "concurrency": 5,
  "resolve_all": false,
  "retry_failed": false,
  "sheet_cache": false,
  "rate_limit": {
    "max_retries": 3,
    "base_backoff": "500ms",
    "requests_per_second": 5,
    "burst": 5
  }
}
```

Use it with:
```bash
isbn-resolver --config config.json --file isbns.txt
```

The `rate_limit` block has two halves. `max_retries` and `base_backoff` are
*reactive*: they govern how a request that already got a `429`/`503` backs off
before trying again. `requests_per_second` and `burst` are *proactive*: they are
the rate and capacity of a single token bucket shared by every worker, which
paces the run so it avoids provoking a `429` in the first place. The defaults
above pair with the default `concurrency` of 5 — roughly one request per worker
per second, with a full pool able to start at once.

Setting `"requests_per_second": 0` means **unlimited** — an explicit opt-out of
pacing, useful when pointing the tool at a local fixture server. A negative rate
is rejected, as is a `burst` below 1.

A `google_books_api_key` key is also accepted, but is deliberately absent from
the examples above and from `examples/config.json`: a config file is copied
verbatim and then usually committed, which is the wrong place for a
credential. Prefer `ISBN_GOOGLE_BOOKS_API_KEY` (see
[Google Books API key](#google-books-api-key-optional)).

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
