// Package framework provides utilities for E2E testing of the NASty CSI driver.
package framework

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Configuration errors.
var (
	ErrMissingNAStyHost   = errors.New("NASTY_HOST environment variable is required")
	ErrMissingNAStyAPIKey = errors.New("NASTY_API_KEY environment variable is required")
)

// Config holds the configuration for E2E tests.
type Config struct {
	// NASty connection settings
	NAStyHost   string
	NAStyAPIKey string
	NAStyPool   string

	// CSI driver image settings
	CSIImageRepo       string
	CSIImageTag        string
	CSIImagePullPolicy string

	// Kubernetes settings
	Kubeconfig string

	// SMB credentials (optional - tests skip if empty)
	SMBUsername string
	SMBPassword string

	// Multi-cluster isolation
	ClusterID string // Cluster ID for multi-cluster testing (E2E_CLUSTER_ID, default: "e2e-test-cluster")

	// Test settings
	Timeout time.Duration
	Verbose bool // Enable verbose test output (E2E_VERBOSE=true)
}

// NewConfig creates a Config from environment variables.
func NewConfig() (*Config, error) {
	cfg := &Config{
		NAStyHost:        os.Getenv("NASTY_HOST"),
		NAStyAPIKey:      os.Getenv("NASTY_API_KEY"),
		NAStyPool:        getEnvOrDefault("NASTY_POOL", "csi"),
		CSIImageRepo:       getEnvOrDefault("CSI_IMAGE_REPO", "ghcr.io/nasty-project/nasty-csi"),
		CSIImageTag:        getEnvOrDefault("CSI_IMAGE_TAG", "latest"),
		CSIImagePullPolicy: getEnvOrDefault("CSI_IMAGE_PULL_POLICY", "Always"),
		Kubeconfig:         getEnvOrDefault("KUBECONFIG", defaultKubeconfig()),
		SMBUsername:        os.Getenv("SMB_USERNAME"),
		SMBPassword:        os.Getenv("SMB_PASSWORD"),
		ClusterID:          getEnvOrDefault("E2E_CLUSTER_ID", "e2e-test-cluster"),
		Timeout:            parseDurationOrDefault(os.Getenv("TEST_TIMEOUT"), 5*time.Minute),
		Verbose:            os.Getenv("E2E_VERBOSE") == "true",
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that all required configuration is present.
func (c *Config) Validate() error {
	if c.NAStyHost == "" {
		return ErrMissingNAStyHost
	}
	if c.NAStyAPIKey == "" {
		return ErrMissingNAStyAPIKey
	}
	return nil
}

// NAStyURL returns the WebSocket URL for the NASty API.
func (c *Config) NAStyURL() string {
	return fmt.Sprintf("wss://%s/api/current", c.NAStyHost)
}

// CSIImage returns the full Docker image reference.
func (c *Config) CSIImage() string {
	return fmt.Sprintf("%s:%s", c.CSIImageRepo, c.CSIImageTag)
}

// getEnvOrDefault returns the environment variable value or a default.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// defaultKubeconfig returns the default kubeconfig path.
func defaultKubeconfig() string {
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}

// parseDurationOrDefault parses a duration string or returns a default.
func parseDurationOrDefault(s string, defaultValue time.Duration) time.Duration {
	if s == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultValue
	}
	return d
}
