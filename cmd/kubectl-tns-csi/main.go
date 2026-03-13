// Package main implements the kubectl-nasty-csi plugin for managing NASty CSI volumes.
//
// Installation:
//
//	go build -o kubectl-tns_csi ./cmd/kubectl-nasty-csi
//	mv kubectl-tns_csi /usr/local/bin/  # or anywhere in PATH
//
// Usage:
//
//	kubectl tns-csi list                     # List all tns-csi managed volumes
//	kubectl tns-csi list-orphaned            # Find volumes with no matching PVC
//	kubectl tns-csi adopt <dataset-path>     # Generate static PV manifest
//	kubectl tns-csi status <pvc-name>        # Show volume status from NASty
//	kubectl tns-csi connectivity             # Test NASty connection
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// Build information (set via ldflags).
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		nastyURL    string
		nastyAPIKey string
		secretRef     string
		outputFormat  string
		skipTLSVerify bool
		clusterID     string
	)

	rootCmd := &cobra.Command{
		Use:   "kubectl-nasty-csi",
		Short: "Manage NASty CSI volumes",
		Long: `kubectl-nasty-csi is a kubectl plugin for managing NASty CSI driver volumes.

It provides commands for discovering orphaned volumes, adopting volumes across
clusters, and troubleshooting volume issues.

Connection to NASty can be configured via:
  - Flags: --url and --api-key
  - Kubernetes secret: --secret <namespace>/<name>
  - Environment: NASTY_URL and NASTY_API_KEY`,
		Version: version + " (" + commit + ")",
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&nastyURL, "url", "", "NASty WebSocket URL (wss://host/api/current)")
	rootCmd.PersistentFlags().StringVar(&nastyAPIKey, "api-key", "", "NASty API key")
	rootCmd.PersistentFlags().StringVar(&secretRef, "secret", "", "Kubernetes secret with NASty credentials (namespace/name)")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format: table, yaml, json")
	rootCmd.PersistentFlags().BoolVar(&skipTLSVerify, "insecure-skip-tls-verify", true, "Skip TLS certificate verification")
	rootCmd.PersistentFlags().StringVar(&clusterID, "cluster-id", "", "Filter by cluster ID (for multi-cluster NASty sharing)")

	// Add subcommands
	rootCmd.AddCommand(newListCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newListSnapshotsCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newListClonesCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newListOrphanedCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newDescribeCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newHealthCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newTroubleshootCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newSummaryCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newCleanupCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newMarkAdoptableCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newAdoptCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newStatusCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newConnectivityCmd(&nastyURL, &nastyAPIKey, &secretRef, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newListUnmanagedCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))
	rootCmd.AddCommand(newImportCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify))
	rootCmd.AddCommand(newDashboardCmd(&nastyURL, &nastyAPIKey, &secretRef, &outputFormat, &skipTLSVerify, &clusterID))

	return rootCmd
}
