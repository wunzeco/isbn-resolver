package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/output"
)

// Duration is a custom type that handles JSON unmarshaling of duration strings
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler interface
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	switch value := v.(type) {
	case string:
		// Parse string as duration (e.g., "30s", "1m", "1h30m")
		dur, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration format: %w", err)
		}
		*d = Duration(dur)
		return nil
	case float64:
		// Handle numeric value as nanoseconds
		*d = Duration(time.Duration(value))
		return nil
	default:
		return fmt.Errorf("invalid duration type: %T", value)
	}
}

// MarshalJSON implements json.Marshaler interface
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Config holds the application configuration
type Config struct {
	Timeout    Duration      `json:"timeout"`
	Format     output.Format `json:"format"`
	Verbose    bool          `json:"verbose"`
	InputFile  string        `json:"input_file"`
	ConfigFile string        `json:"config_file"`
	
	// Google Sheets configuration
	SheetsURL         string `json:"sheets_url"`
	SheetsID          string `json:"sheets_id"`
	SheetsRange       string `json:"sheets_range"`
	SheetsCredentials string `json:"sheets_credentials"`
	SheetsOutputRange string `json:"sheets_output_range"`
	SheetsCreateTab   string `json:"sheets_create_tab"`
	SheetsDryRun      bool   `json:"sheets_dry_run"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		Timeout: Duration(30 * time.Second),
		Format:  output.FormatText,
		Verbose: false,
	}
}

// LoadFromFile loads configuration from a JSON file
func LoadFromFile(filename string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFromEnv loads configuration from environment variables
func (c *Config) LoadFromEnv() {
	if timeout := os.Getenv("ISBN_TIMEOUT"); timeout != "" {
		if d, err := time.ParseDuration(timeout); err == nil {
			c.Timeout = Duration(d)
		}
	}

	if format := os.Getenv("ISBN_FORMAT"); format != "" {
		c.Format = output.Format(format)
	}

	if verbose := os.Getenv("ISBN_VERBOSE"); verbose == "true" {
		c.Verbose = true
	}
}
