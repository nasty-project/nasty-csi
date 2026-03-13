package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newListSnapshotsCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-snapshots",
		Short: "List all nasty-csi managed snapshots on NASty",
		Long: `List all snapshots managed by nasty-csi on NASty.

This command queries NASty for all snapshots associated with nasty-csi managed
volumes, including both attached (on-volume) and detached snapshots.

Examples:
  # List all snapshots in table format
  kubectl nasty-csi list-snapshots

  # List all snapshots in YAML format
  kubectl nasty-csi list-snapshots -o yaml

  # List snapshots using specific NASty connection
  kubectl nasty-csi list-snapshots --url wss://nasty:443/api/current --api-key <key>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListSnapshots(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, clusterID)
		},
	}
	return cmd
}

func runListSnapshots(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, clusterID *string) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to NASty
	client, err := connectToNASty(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	// Find all snapshots
	snapshots, err := dashboard.FindManagedSnapshots(ctx, client, *clusterID)
	if err != nil {
		return fmt.Errorf("failed to query snapshots: %w", err)
	}

	// Output based on format
	return outputSnapshots(snapshots, *outputFormat)
}

// outputSnapshots outputs snapshots in the specified format.
func outputSnapshots(snapshots []SnapshotInfo, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snapshots)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(snapshots)

	case outputFormatTable, "":
		t := newStyledTable()
		t.AppendHeader(table.Row{"NAME", "SOURCE_VOLUME", "PROTOCOL", "TYPE", "SOURCE_DATASET"})
		for _, s := range snapshots {
			snapType := colorSuccess.Sprint(s.Type)
			if s.Type == "detached" {
				snapType = colorProtocolNFS.Sprint(s.Type)
			}
			t.AppendRow(table.Row{s.Name, s.SourceVolume, protocolBadge(s.Protocol), snapType, s.SourceDataset})
		}
		renderTable(t)
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
