package config

import (
	"encoding/json"
	"os"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/output"
)

// Config holds the application configuration
type Config struct {
	Workers    int           `json:"workers"`
	Timeout    time.Duration `json:"timeout"`
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
		Workers: 5,
		Timeout: 30 * time.Second,
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
	if workers := os.Getenv("ISBN_WORKERS"); workers != "" {
		// Parse and set workers if valid
	}

	if timeout := os.Getenv("ISBN_TIMEOUT"); timeout != "" {
		if d, err := time.ParseDuration(timeout); err == nil {
			c.Timeout = d
		}
	}

	if format := os.Getenv("ISBN_FORMAT"); format != "" {
		c.Format = output.Format(format)
	}

	if verbose := os.Getenv("ISBN_VERBOSE"); verbose == "true" {
		c.Verbose = true
	}
}
