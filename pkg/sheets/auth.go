package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

const (
	tokenCacheFile = ".sheets_token.json"
)

// AuthConfig holds authentication configuration
type AuthConfig struct {
	CredentialsPath string
	UseServiceAccount bool
}

// Authenticate creates an authenticated Sheets service
func Authenticate(ctx context.Context, config AuthConfig) (*sheets.Service, error) {
	if config.UseServiceAccount || os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		return authenticateServiceAccount(ctx, config.CredentialsPath)
	}
	return authenticateOAuth(ctx, config.CredentialsPath)
}

// authenticateServiceAccount uses service account credentials
func authenticateServiceAccount(ctx context.Context, credPath string) (*sheets.Service, error) {
	// Use environment variable if credentials path not provided
	if credPath == "" {
		credPath = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	}

	if credPath == "" {
		return nil, fmt.Errorf("service account credentials not found. Set GOOGLE_APPLICATION_CREDENTIALS or provide --sheets-credentials")
	}

	data, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read service account file: %w", err)
	}

	creds, err := google.CredentialsFromJSON(ctx, data, sheets.SpreadsheetsScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse service account credentials: %w", err)
	}

	srv, err := sheets.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("unable to create Sheets service: %w", err)
	}

	return srv, nil
}

// authenticateOAuth uses OAuth 2.0 with token caching
func authenticateOAuth(ctx context.Context, credPath string) (*sheets.Service, error) {
	if credPath == "" {
		credPath = os.Getenv("GOOGLE_SHEETS_CREDENTIALS")
	}

	if credPath == "" {
		return nil, fmt.Errorf("OAuth credentials not found. Provide --sheets-credentials with client_secret.json")
	}

	b, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read OAuth credentials file: %w", err)
	}

	config, err := google.ConfigFromJSON(b, sheets.SpreadsheetsScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse OAuth credentials: %w", err)
	}

	client, err := getOAuthClient(ctx, config)
	if err != nil {
		return nil, err
	}

	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create Sheets service: %w", err)
	}

	return srv, nil
}

// getOAuthClient gets an OAuth client with token caching
func getOAuthClient(ctx context.Context, config *oauth2.Config) (*http.Client, error) {
	// Try to load cached token
	token, err := loadToken(tokenCacheFile)
	if err != nil {
		// Get new token
		token, err = getTokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		// Save token for next time
		if err := saveToken(tokenCacheFile, token); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Unable to cache token: %v\n", err)
		}
	}

	return config.Client(ctx, token), nil
}

// getTokenFromWeb requests a token from the web
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser:\n%v\n\n", authURL)
	fmt.Print("Enter authorization code: ")

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %w", err)
	}

	token, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}

	return token, nil
}

// loadToken loads a token from a file
func loadToken(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	token := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(token)
	return token, err
}

// saveToken saves a token to a file
func saveToken(path string, token *oauth2.Token) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("unable to create token cache directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to cache token: %w", err)
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(token)
}
