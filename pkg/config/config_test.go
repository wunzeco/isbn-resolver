package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wunzeco/isbn-resolver/pkg/output"
)

func TestLoadFromFile(t *testing.T) {
	// Create a temporary config file
	configContent := `{
		"timeout": "30s",
		"format": "json",
		"verbose": false
	}`

	tmpFile, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(configContent)); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	// Test loading the config
	cfg, err := LoadFromFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	// Verify the loaded config
	if time.Duration(cfg.Timeout) != 30*time.Second {
		t.Errorf("Expected timeout=30s, got %v", cfg.Timeout)
	}

	if cfg.Format != output.FormatJSON {
		t.Errorf("Expected format=json, got %s", cfg.Format)
	}

	if cfg.Verbose != false {
		t.Errorf("Expected verbose=false, got %v", cfg.Verbose)
	}
}

func TestLoadFromFileWithVariousTimeouts(t *testing.T) {
	tests := []struct {
		name           string
		timeoutStr     string
		expectedDur    time.Duration
		shouldError    bool
	}{
		{
			name:        "30 seconds",
			timeoutStr:  "30s",
			expectedDur: 30 * time.Second,
			shouldError: false,
		},
		{
			name:        "1 minute",
			timeoutStr:  "1m",
			expectedDur: 1 * time.Minute,
			shouldError: false,
		},
		{
			name:        "90 seconds",
			timeoutStr:  "90s",
			expectedDur: 90 * time.Second,
			shouldError: false,
		},
		{
			name:        "1 minute 30 seconds",
			timeoutStr:  "1m30s",
			expectedDur: 90 * time.Second,
			shouldError: false,
		},
		{
			name:        "invalid format",
			timeoutStr:  "invalid",
			expectedDur: 0,
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configContent := `{
				"timeout": "` + tt.timeoutStr + `",
				"format": "text",
				"verbose": false
			}`

			tmpFile, err := os.CreateTemp("", "config-*.json")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(tmpFile.Name())

			if _, err := tmpFile.Write([]byte(configContent)); err != nil {
				t.Fatalf("Failed to write to temp file: %v", err)
			}
			tmpFile.Close()

			cfg, err := LoadFromFile(tmpFile.Name())
			
			if tt.shouldError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("LoadFromFile failed: %v", err)
			}

			if time.Duration(cfg.Timeout) != tt.expectedDur {
				t.Errorf("Expected timeout=%v, got %v", tt.expectedDur, cfg.Timeout)
			}
		})
	}
}

func TestUnmarshalJSON(t *testing.T) {
	jsonData := `{
		"timeout": "45s",
		"format": "csv",
		"verbose": true,
		"sheets_url": "https://example.com/sheet",
		"sheets_range": "A1:A100"
	}`

	var cfg Config
	err := json.Unmarshal([]byte(jsonData), &cfg)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if time.Duration(cfg.Timeout) != 45*time.Second {
		t.Errorf("Expected timeout=45s, got %v", cfg.Timeout)
	}

	if cfg.Format != output.FormatCSV {
		t.Errorf("Expected format=csv, got %s", cfg.Format)
	}

	if cfg.Verbose != true {
		t.Errorf("Expected verbose=true, got %v", cfg.Verbose)
	}

	if cfg.SheetsURL != "https://example.com/sheet" {
		t.Errorf("Expected sheets_url, got %s", cfg.SheetsURL)
	}

	if cfg.SheetsRange != "A1:A100" {
		t.Errorf("Expected sheets_range, got %s", cfg.SheetsRange)
	}
}
