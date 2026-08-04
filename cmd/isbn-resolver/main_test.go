package main

import (
	"strings"
	"testing"

	"github.com/wunzeco/isbn-resolver/pkg/cache"
	"github.com/wunzeco/isbn-resolver/pkg/config"
)

// TestResolveCacheMode covers every combination of the three cache-control
// settings, including the ones that can only be reached through a config file
// (the flags themselves are booleans a user can set in any mix). The whole
// point of collapsing them into one cache.Mode here is that the resolve loop
// never has to re-derive these combinations, so this is the only place the
// precedence between them is asserted.
func TestResolveCacheMode(t *testing.T) {
	tests := []struct {
		name        string
		resolveAll  bool
		retryFailed bool
		noCache     bool
		want        cache.Mode
		wantErr     bool
	}{
		{
			name: "no flags reuses successes and errors",
			want: cache.ModeNormal,
		},
		{
			name:       "resolve-all",
			resolveAll: true,
			want:       cache.ModeResolveAll,
		},
		{
			name:        "retry-failed",
			retryFailed: true,
			want:        cache.ModeRetryFailed,
		},
		{
			name:    "no-cache",
			noCache: true,
			want:    cache.ModeNoCache,
		},
		{
			// --no-cache disables reads and writes outright, which subsumes
			// any reuse policy --resolve-all would describe.
			name:       "no-cache wins over resolve-all",
			resolveAll: true,
			noCache:    true,
			want:       cache.ModeNoCache,
		},
		{
			name:        "no-cache wins over retry-failed",
			retryFailed: true,
			noCache:     true,
			want:        cache.ModeNoCache,
		},
		{
			name:        "resolve-all with retry-failed is rejected",
			resolveAll:  true,
			retryFailed: true,
			wantErr:     true,
		},
		{
			// Even though --no-cache makes both moot, the contradiction still
			// signals a wrong expectation, so it is reported rather than
			// swallowed.
			name:        "resolve-all with retry-failed is rejected even under no-cache",
			resolveAll:  true,
			retryFailed: true,
			noCache:     true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.ResolveAll = tt.resolveAll
			cfg.RetryFailed = tt.retryFailed
			cfg.NoCache = tt.noCache

			got, err := resolveCacheMode(cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveCacheMode() = %v, want error", got)
				}
				// The message must name both flags: it is the only thing the
				// user sees before the process exits 1.
				for _, want := range []string{"--resolve-all", "--retry-failed"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %s", err, want)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveCacheMode() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveCacheMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
