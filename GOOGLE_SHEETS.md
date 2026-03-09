# Google Sheets Integration Guide

This guide explains how to set up and use the Google Sheets integration feature in ISBN Resolver.

## Overview

The Google Sheets integration allows you to:
- Read ISBNs directly from a Google Spreadsheet
- Automatically resolve book metadata for each ISBN
- Write results back to the same or different sheet
- Create new tabs for organized results
- Preview changes before writing (dry-run mode)

## Setup

### Option 1: Service Account (Recommended for Automation)

Service accounts are ideal for automated workflows, scripts, and server-side applications.

#### Step 1: Create a Google Cloud Project

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Click "Select a project" → "New Project"
3. Enter a project name (e.g., "ISBN Resolver")
4. Click "Create"

#### Step 2: Enable Google Sheets API

1. In your project, go to "APIs & Services" → "Library"
2. Search for "Google Sheets API"
3. Click on it and click "Enable"

#### Step 3: Create Service Account

1. Go to "APIs & Services" → "Credentials"
2. Click "Create Credentials" → "Service Account"
3. Enter a name (e.g., "isbn-resolver-sa")
4. Click "Create and Continue"
5. Skip optional steps and click "Done"

#### Step 4: Create and Download Key

1. Click on the service account you just created
2. Go to the "Keys" tab
3. Click "Add Key" → "Create new key"
4. Choose "JSON" format
5. Click "Create" - the key file will download automatically

#### Step 5: Share Your Spreadsheet

1. Open your Google Sheet
2. Click the "Share" button
3. Paste the service account email (format: `name@project-id.iam.gserviceaccount.com`)
4. Give it "Editor" permissions
5. Click "Send"

#### Step 6: Set Environment Variable

```bash
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/your/service-account-key.json"
```

Add this to your `~/.bashrc`, `~/.zshrc`, or equivalent to make it permanent.

### Option 2: OAuth 2.0 (Recommended for Personal Use)

OAuth is better for personal use as it uses your Google account credentials.

#### Step 1-3: Same as Service Account

Follow steps 1-3 from the service account setup to create a project and enable the API.

#### Step 4: Create OAuth Credentials

1. Go to "APIs & Services" → "Credentials"
2. Click "Create Credentials" → "OAuth client ID"
3. If prompted, configure the OAuth consent screen:
   - Choose "External" for user type
   - Fill in required fields (app name, user support email)
   - Add your email to test users
4. Choose "Desktop app" as application type
5. Enter a name (e.g., "ISBN Resolver Desktop")
6. Click "Create"
7. Download the JSON file (click the download icon)

#### Step 5: First-Time Authentication

```bash
isbn-resolver --sheets-url "YOUR_SHEET_URL" \
              --sheets-range "A2:A" \
              --sheets-credentials "/path/to/client_secret.json"
```

This will:
1. Open your browser for authentication
2. Ask you to authorize the application
3. Cache the token for future use

Subsequent runs won't require the browser:

```bash
isbn-resolver --sheets-url "YOUR_SHEET_URL" --sheets-range "A2:A"
```

## Usage Examples

### Basic Usage

Read ISBNs from column A (starting at row 2) and write results to columns B-J:

```bash
isbn-resolver --sheets-url "https://docs.google.com/spreadsheets/d/YOUR_SHEET_ID/edit" \
              --sheets-range "Sheet1!A2:A"
```

### Using Sheet ID Instead of URL

```bash
isbn-resolver --sheets-id "YOUR_SHEET_ID" \
              --sheets-range "Sheet1!A2:A"
```

### Specify Output Range

```bash
isbn-resolver --sheets-id "YOUR_SHEET_ID" \
              --sheets-range "ISBNs!A2:A" \
              --sheets-output-range "ISBNs!B2:J"
```

### Create New Tab for Results

```bash
isbn-resolver --sheets-url "URL" \
              --sheets-range "Input!A2:A" \
              --sheets-create-tab "Resolved Books"
```

### Dry Run (Preview Only)

Preview what would be written without making changes:

```bash
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --sheets-dry-run \
              --verbose
```

### With Custom Settings

```bash
isbn-resolver --sheets-url "URL" \
              --sheets-range "A2:A" \
              --timeout 60s \
              --verbose
```

## Input Format

### ISBN Column

Your Google Sheet should have ISBNs in a single column:

| A | B | C |
|---|---|---|
| ISBN | (results will appear here) | |
| 978-0134190440 | | |
| 0-596-52068-9 | | |
| 9780132350884 | | |

**Notes:**
- The tool automatically skips header rows containing "ISBN", "ISBN-10", "ISBN-13"
- Empty cells are ignored
- ISBNs can be formatted with or without hyphens
- Numeric ISBNs (formatted as numbers in sheets) are handled correctly

## Output Format

Results are written with the following columns:

| Column | Field | Description |
|--------|-------|-------------|
| B (or first output column) | Status | "Success" or "Error" |
| C | Title | Book title |
| D | Authors | Comma-separated list of authors |
| E | Publisher | Publisher name |
| F | Publication Date | Date published |
| G | Pages | Number of pages |
| H | Language | Language code |
| I | Categories | Comma-separated categories |
| J | Error | Error message (if failed) |

## Troubleshooting

### Authentication Errors

**Error: "Failed to authenticate with Google Sheets"**

Solutions:
- Check that credentials file path is correct
- Ensure `GOOGLE_APPLICATION_CREDENTIALS` is set
- Verify the credentials file is valid JSON
- For service account: check that API is enabled in Google Cloud Console

### Permission Errors

**Error: "Insufficient permissions for spreadsheet"**

Solutions:
- Ensure the sheet is shared with your service account email
- Give "Editor" access (not just "Viewer")
- For OAuth: ensure you authorized the application

### API Quota Errors

**Error: "Google Sheets API rate limit reached"**

Solutions:
- Process in smaller batches
- Wait a few minutes and retry
- Check [Google's quota limits](https://developers.google.com/sheets/api/limits)

### Range Errors

**Error: "Invalid range format"**

Solutions:
- Use A1 notation: `A2:A` or `Sheet1!A2:A100`
- Include the sheet name if you have multiple tabs
- Ensure the range exists in your sheet

### No Data Found

**Error: "No data found in range"**

Solutions:
- Verify the range is correct
- Check that cells contain data
- Try expanding the range: `A1:A` instead of `A2:A`

## API Quotas and Limits

Google Sheets API has the following limits:

- **Read requests**: 100 requests per 100 seconds per project
- **Write requests**: 100 requests per 100 seconds per project
- **Requests per day**: 500 requests per day per project (can be increased)

The tool automatically:
- Batches write operations to minimize API calls
- Handles rate limit errors with retries
- Uses efficient reading strategies

For large datasets (1000+ ISBNs):
- Consider breaking into smaller batches
- Process during off-peak hours

## Security Best Practices

### Service Account Keys

1. **Never commit to version control**
   ```bash
   # Add to .gitignore
   echo "*service-account*.json" >> .gitignore
   echo "*client_secret*.json" >> .gitignore
   ```

2. **Restrict file permissions**
   ```bash
   chmod 600 /path/to/service-account.json
   ```

3. **Use environment variables**
   ```bash
   export GOOGLE_APPLICATION_CREDENTIALS="$HOME/.config/isbn-resolver/credentials.json"
   ```

4. **Rotate keys periodically**
   - Create new keys every 90 days
   - Delete old keys from Google Cloud Console

### OAuth Tokens

- Tokens are cached in `.sheets_token.json` in the current directory
- This file should also be added to `.gitignore`
- Delete the token file to force re-authentication

### Access Control

- Only share sheets with necessary service accounts
- Use the principle of least privilege
- Regularly audit sheet permissions

## Advanced Usage

### Custom Configuration File

Create a config file with Google Sheets settings:

```json
{
  "timeout": "30s",
  "verbose": true,
  "sheets_url": "https://docs.google.com/spreadsheets/d/YOUR_ID/edit",
  "sheets_range": "Sheet1!A2:A",
  "sheets_output_range": "Sheet1!B2:J",
  "sheets_credentials": "/path/to/credentials.json"
}
```

Use it:
```bash
isbn-resolver --config my-config.json
```

### Processing Multiple Sheets

Process multiple sheets in sequence:

```bash
# Sheet 1
isbn-resolver --sheets-id "ID" --sheets-range "Books2024!A2:A" --sheets-create-tab "Results2024"

# Sheet 2
isbn-resolver --sheets-id "ID" --sheets-range "Books2025!A2:A" --sheets-create-tab "Results2025"
```

### Automation with Cron

Create a script for automated processing:

```bash
#!/bin/bash
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account.json"

isbn-resolver \
  --sheets-id "YOUR_SHEET_ID" \
  --sheets-range "New ISBNs!A2:A" \
  --sheets-create-tab "Results $(date +%Y-%m-%d)" \
  --verbose \
  >> /var/log/isbn-resolver.log 2>&1
```

Schedule with cron:
```bash
# Run daily at 2 AM
0 2 * * * /path/to/script.sh
```

## Support

For issues or questions:
- Check the [main README](README.md)
- Review [troubleshooting section](#troubleshooting)
- Open an issue on [GitHub](https://github.com/wunzeco/isbn-resolver/issues)

## References

- [Google Sheets API Documentation](https://developers.google.com/sheets/api)
- [Service Account Overview](https://cloud.google.com/iam/docs/service-accounts)
- [OAuth 2.0 for Desktop Apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [API Quotas and Limits](https://developers.google.com/sheets/api/limits)
