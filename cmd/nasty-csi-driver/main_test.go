package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run the real CLI validation, stopping before any connection can succeed.
func TestAPIKeyFromEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name, envKey, flagKey string
		wantMissing           bool
	}{
		{"environment", "fixture-env-secret", "", false},
		{"flag compatible", "", "fixture-flag-secret", false},
		{"flag and environment", "fixture-env-secret", "fixture-flag-secret", false},
		{"missing", "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAPIKeyCLIHelper$")
			cmd.Env = append(os.Environ(), "NASTY_CLI_TEST=1", "NASTY_API_KEY="+tt.envKey, "NASTY_TEST_FLAG_KEY="+tt.flagKey)
			output, _ := cmd.CombinedOutput()
			text := string(output)
			if strings.Contains(text, "Storage API key must be provided") != tt.wantMissing {
				t.Fatalf("unexpected CLI validation: %s", text)
			}
			if !tt.wantMissing && !strings.Contains(text, "invalid port") {
				t.Fatalf("CLI did not reach API URL validation: %s", text)
			}
			for _, secret := range []string{"fixture-env-secret", "fixture-flag-secret"} {
				if strings.Contains(text, secret) {
					t.Fatal("CLI output exposed a credential")
				}
			}
		})
	}
}

func TestAPIKeyCLIHelper(t *testing.T) {
	if os.Getenv("NASTY_CLI_TEST") != "1" {
		t.Skip("subprocess CLI helper")
	}
	os.Args = []string{"nasty-csi-driver", "--api-url=ws://127.0.0.1:invalid/ws"}
	if key := os.Getenv("NASTY_TEST_FLAG_KEY"); key != "" {
		os.Args = append(os.Args, "--api-key="+key)
	}
	main()
}
