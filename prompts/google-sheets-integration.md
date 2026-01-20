# ISBN Resolver - Google Sheets Integration Feature

## Feature Overview

Add Google Sheets integration to the existing ISBN resolver tool, enabling it to read ISBN numbers from Google Spreadsheets and write the resolved book metadata back to the sheet or output to other formats.

## Current State

The ISBN resolver tool currently supports:
- Reading ISBNs from command-line arguments, files, and stdin
- Validating ISBN-10 and ISBN-13 formats
- Resolving book metadata via Open Library and Google Books APIs
- Concurrent processing with worker pools
- Output in text, JSON, and CSV formats

## Requirements

### Core Functionality

1. **Google Sheets Authentication**
   - Support OAuth 2.0 authentication flow
   - Support service account authentication (recommended for automation)
   - Store credentials securely (credentials file or environment variables)
   - Handle token refresh automatically
   - Provide clear setup instructions for obtaining Google API credentials

2. **Reading from Google Sheets**
   - Accept Google Sheets URL or sheet ID as input
   - Support specifying sheet name/tab (default to first sheet)
   - Support specifying the column containing ISBNs (e.g., "A", "B:B", "ISBN")
   - Support specifying row range (e.g., "A2:A100" to skip header)
   - Handle empty cells gracefully
   - Support both numeric and string ISBN formats in sheets

3. **Writing to Google Sheets**
   - Write resolved metadata back to the same sheet or a different sheet
   - Support specifying output columns for each metadata field
   - Support creating new columns if they don't exist
   - Option to write to a new sheet/tab within the same spreadsheet
   - Preserve existing data in other columns
   - Support append mode (add to existing data) or overwrite mode
   - Handle API rate limits with exponential backoff

4. **Output Options**
   - Default: Write results back to Google Sheets
   - Optional: Output to stdout in existing formats (text, JSON, CSV)
   - Optional: Write to both Google Sheets and local file
   - Support dry-run mode to preview changes without writing

### Technical Implementation

1. **Package Structure**
   ```
   pkg/
   ├── sheets/
   │   ├── client.go          # Google Sheets API client
   │   ├── auth.go            # Authentication handling
   │   ├── reader.go          # Read ISBNs from sheets
   │   ├── writer.go          # Write metadata to sheets
   │   └── sheets_test.go     # Unit tests
   ```

2. **Dependencies**
   - `google.golang.org/api/sheets/v4` - Google Sheets API
   - `google.golang.org/api/option` - API client options
   - `golang.org/x/oauth2` - OAuth 2.0 support
   - `golang.org/x/oauth2/google` - Google-specific OAuth

3. **Configuration**
   - Add command-line flags:
     - `--sheets-url` or `--sheets-id`: Google Sheets URL or ID
     - `--sheets-range`: Cell range for ISBNs (e.g., "Sheet1!A2:A")
     - `--sheets-credentials`: Path to credentials file
     - `--sheets-output-range`: Where to write results
     - `--sheets-create-tab`: Create new tab for results
     - `--sheets-dry-run`: Preview changes without writing
   - Environment variables:
     - `GOOGLE_APPLICATION_CREDENTIALS`: Path to service account JSON
     - `GOOGLE_SHEETS_CREDENTIALS`: Alternative credentials path

4. **Error Handling**
   - Handle authentication failures with clear error messages
   - Handle API quota/rate limit errors with retry logic
   - Handle permission errors (read/write access)
   - Continue processing other ISBNs if individual writes fail
   - Provide summary of successful and failed operations

### Data Mapping

**Input:**
- Read ISBNs from a single column in Google Sheets
- Ignore empty cells
- Trim whitespace from ISBN values

**Output (default column mapping):**
| Column | Field |
|--------|-------|
| A | ISBN (original input) |
| B | Status (Success/Error) |
| C | Title |
| D | Authors |
| E | Publisher |
| F | Publication Date |
| G | Pages |
| H | Language |
| I | Categories |
| J | Error Message (if failed) |

**Customizable Output:**
- Allow users to specify column mapping via config file
- Support custom column headers

## Example Usage

### Using Service Account (Recommended)

```bash
# Set credentials environment variable
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account.json"

# Read from Google Sheets and write results back
isbn-resolver --sheets-url "https://docs.google.com/spreadsheets/d/SHEET_ID/edit" \
              --sheets-range "Sheet1!A2:A"

# Specify output range
isbn-resolver --sheets-id "SHEET_ID" \
              --sheets-range "ISBNs!A2:A" \
              --sheets-output-range "ISBNs!B2:J"

# Create a new tab for results
isbn-resolver --sheets-url "URL" \
              --sheets-range "Input!A2:A" \
              --sheets-create-tab "Resolved Books"

# Dry run to preview changes
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --sheets-dry-run

# Write to both sheets and local CSV file
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --format csv > output.csv
```

### Using OAuth 2.0

```bash
# First-time authentication (opens browser)
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --sheets-credentials "client_secret.json"

# Subsequent runs use cached token
isbn-resolver --sheets-url "URL" --sheets-range "A2:A"
```

## Implementation Steps

1. **Phase 1: Authentication Setup**
   - Implement service account authentication
   - Implement OAuth 2.0 flow with token caching
   - Add credential validation
   - Create setup documentation

2. **Phase 2: Read Functionality**
   - Implement Google Sheets API client
   - Parse sheet URLs and extract sheet ID
   - Read ISBN values from specified range
   - Handle various cell formats and edge cases

3. **Phase 3: Write Functionality**
   - Implement batch writing to minimize API calls
   - Map resolved metadata to sheet columns
   - Handle rate limiting and retries
   - Support creating new tabs/sheets

4. **Phase 4: Integration**
   - Integrate with existing CLI flags and config
   - Update main.go to support sheets input
   - Combine with existing worker pool for concurrent processing
   - Add progress reporting for large sheets

5. **Phase 5: Testing & Documentation**
   - Write unit tests with mocked API calls
   - Write integration tests with test spreadsheets
   - Update README with Google Sheets setup guide
   - Add troubleshooting section

## Configuration File Example

```json
{
  "workers": 5,
  "timeout": "30s",
  "format": "json",
  "verbose": true,
  "google_sheets": {
    "credentials_path": "/path/to/credentials.json",
    "default_input_range": "A2:A",
    "column_mapping": {
      "isbn": "A",
      "status": "B",
      "title": "C",
      "authors": "D",
      "publisher": "E",
      "publication_date": "F",
      "pages": "G",
      "language": "H",
      "categories": "I",
      "error": "J"
    },
    "batch_size": 100,
    "retry_attempts": 3
  }
}
```

## Google Sheets Setup Guide

### For Service Account (Recommended for Automation)

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select existing one
3. Enable Google Sheets API
4. Create a service account
5. Download JSON key file
6. Share your Google Sheet with the service account email
7. Set `GOOGLE_APPLICATION_CREDENTIALS` environment variable

### For OAuth 2.0 (Recommended for Personal Use)

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create OAuth 2.0 credentials (Desktop application)
3. Download client_secret.json
4. Run the tool with `--sheets-credentials` flag
5. Authorize in browser
6. Token is cached for future use

## Expected Output

### Console Output (Verbose Mode)
```
Authenticating with Google Sheets...
✓ Successfully authenticated
Reading ISBNs from sheet "Book List" (range: A2:A50)...
Found 48 ISBNs to process
Processing 48 ISBNs with 5 workers...
[█████████████████████████████] 48/48 (100%)
Writing results to sheet "Book List" (range: B2:J49)...
✓ Successfully wrote 45 results
⚠ 3 ISBNs failed to resolve

Summary:
- Total: 48
- Successful: 45
- Failed: 3
- Duration: 12.5s
```

### Google Sheets Output

| ISBN | Status | Title | Authors | Publisher | Publication Date | Pages | Language | Categories | Error |
|------|--------|-------|---------|-----------|------------------|-------|----------|------------|-------|
| 978-0134190440 | Success | The Go Programming Language | Alan A. A. Donovan, Brian W. Kernighan | Addison-Wesley | 2015-11-16 | 400 | English | Programming, Computer Science | |
| 0596520689 | Success | Programming Perl | Larry Wall, Tom Christiansen | O'Reilly Media | 2000-07-14 | 1092 | English | Programming | |
| 1234567890 | Error | | | | | | | | ISBN not found in database |

## Error Handling Examples

1. **Authentication Error:**
   ```
   Error: Failed to authenticate with Google Sheets
   Please check your credentials file: /path/to/credentials.json
   Documentation: https://github.com/wunzeco/isbn-resolver#google-sheets-setup
   ```

2. **Permission Error:**
   ```
   Error: Insufficient permissions for spreadsheet
   The service account/user needs "Editor" access to write results
   Share the spreadsheet with: service-account@project.iam.gserviceaccount.com
   ```

3. **Rate Limit Error:**
   ```
   Warning: Google Sheets API rate limit reached
   Retrying in 30 seconds... (attempt 1/3)
   ```

## Testing Requirements

1. **Unit Tests**
   - Test authentication with mock credentials
   - Test parsing sheet URLs and ranges
   - Test cell range validation
   - Test data mapping and formatting
   - Mock API responses

2. **Integration Tests**
   - Test reading from real test spreadsheet
   - Test writing to real test spreadsheet
   - Test error handling with invalid credentials
   - Test rate limiting behavior

3. **Manual Testing Checklist**
   - [ ] Service account authentication works
   - [ ] OAuth 2.0 flow completes successfully
   - [ ] Reading ISBNs from various column formats
   - [ ] Writing results to same sheet
   - [ ] Writing results to new tab
   - [ ] Handling large sheets (1000+ rows)
   - [ ] Dry-run mode displays correct preview
   - [ ] Error messages are clear and actionable

## Documentation Updates

1. **README.md**
   - Add "Google Sheets Integration" section
   - Include setup instructions for both auth methods
   - Add example commands and screenshots
   - Add troubleshooting section

2. **GOOGLE_SHEETS.md** (New file)
   - Detailed setup guide
   - API quota information
   - Best practices for large datasets
   - Security considerations

3. **examples/**
   - `config-with-sheets.json` - Example config
   - `sheets-setup.sh` - Script to help with setup
   - Example spreadsheet template (link)

## Security Considerations

1. **Credentials Storage**
   - Never commit credentials to version control
   - Add credentials files to .gitignore
   - Use environment variables in CI/CD
   - Document secure credential management

2. **API Access**
   - Request minimum necessary scopes
   - Implement token rotation
   - Log authentication events
   - Handle credential expiration gracefully

3. **Data Privacy**
   - Warn users about data sharing with APIs
   - Support on-premise/self-hosted alternatives
   - Document data retention policies

## Future Enhancements

- Support for Microsoft Excel Online
- Support for Airtable
- Bulk update mode (only update changed ISBNs)
- Scheduled/automated runs with cron
- Web UI for non-technical users
- Webhook support for real-time updates

## Success Criteria

- [ ] Successfully authenticate with both service account and OAuth
- [ ] Read ISBNs from Google Sheets
- [ ] Write resolved metadata back to sheets
- [ ] Handle errors gracefully
- [ ] Respect API rate limits
- [ ] Documentation is clear and complete
- [ ] Tests achieve >80% coverage
- [ ] Performance: Process 100 ISBNs in <60 seconds
