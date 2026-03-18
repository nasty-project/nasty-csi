package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	nastyapi "github.com/nasty-project/nasty-go"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for status command.
var (
	errVolumeNotFound        = errors.New("volume not found")
	errNFSShareIDNotFound    = errors.New("NFS share ID not found in properties")
	errNFSShareDisabled      = errors.New("NFS share is disabled")
	errNFSShareNotFound      = errors.New("NFS share not found")
	errNVMeSubsystemNotFound = errors.New("NVMe-oF subsystem not found")
	errNVMeNamespaceNotFound = errors.New("NVMe-oF namespace not found")
	errStatusUnknownFormat   = errors.New("unknown output format")
)

// VolumeStatus represents detailed status of a volume.
type VolumeStatus struct {
	UsedHuman       string   `json:"usedHuman,omitempty"       yaml:"usedHuman,omitempty"`
	UsedPercent     string   `json:"usedPercent,omitempty"     yaml:"usedPercent,omitempty"`
	NVMeNQN         string   `json:"nvmeNqn,omitempty"         yaml:"nvmeNqn,omitempty"`
	AvailableHuman  string   `json:"availableHuman,omitempty"  yaml:"availableHuman,omitempty"`
	Dataset         string   `json:"dataset"                   yaml:"dataset"`
	Protocol        string   `json:"protocol"                  yaml:"protocol"`
	Type            string   `json:"type"                      yaml:"type"`
	CapacityHuman   string   `json:"capacityHuman"             yaml:"capacityHuman"`
	NFSSharePath    string   `json:"nfsSharePath,omitempty"    yaml:"nfsSharePath,omitempty"`
	VolumeID        string   `json:"volumeId"                  yaml:"volumeId"`
	NFSShareID      string   `json:"nfsShareId,omitempty"      yaml:"nfsShareId,omitempty"`
	NVMeSubsystemID string   `json:"nvmeSubsystemId,omitempty" yaml:"nvmeSubsystemId,omitempty"`
	Issues          []string `json:"issues,omitempty"          yaml:"issues,omitempty"`
	CapacityBytes   int64    `json:"capacityBytes"             yaml:"capacityBytes"`
	UsedBytes       int64    `json:"usedBytes,omitempty"       yaml:"usedBytes,omitempty"`
	AvailableBytes  int64    `json:"availableBytes,omitempty"  yaml:"availableBytes,omitempty"`
	Healthy         bool     `json:"healthy"                   yaml:"healthy"`
	NFSEnabled      bool     `json:"nfsEnabled,omitempty"      yaml:"nfsEnabled,omitempty"`
}

func newStatusCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <volume-id>",
		Short: "Show detailed status of a volume from NASty",
		Long: `Show detailed status of a nasty-csi managed volume, including:
  - Dataset information (capacity, used space)
  - NFS share status (enabled, path)
  - NVMe-oF subsystem/namespace status
  - Health check results

Examples:
  # Get status of a specific volume
  kubectl nasty-csi status pvc-abc123

  # Output as JSON for scripting
  kubectl nasty-csi status pvc-abc123 -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			volumeID := args[0]
			return runStatus(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify, volumeID)
		},
	}
	return cmd
}

func runStatus(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool, volumeID string) error {
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

	// Find the volume by CSI volume name
	subvol, err := client.FindSubvolumeByCSIVolumeName(ctx, "", volumeID)
	if err != nil {
		return fmt.Errorf("failed to find volume: %w", err)
	}
	if subvol == nil {
		return fmt.Errorf("%w: %s", errVolumeNotFound, volumeID)
	}

	// Build status
	status, err := buildVolumeStatus(ctx, client, subvol, volumeID)
	if err != nil {
		return fmt.Errorf("failed to get volume status: %w", err)
	}

	// Output
	return outputStatus(status, *outputFormat)
}

//nolint:unparam // error kept for API consistency
func buildVolumeStatus(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume, volumeID string) (*VolumeStatus, error) {
	props := sv.Properties

	status := &VolumeStatus{
		VolumeID: volumeID,
		Dataset:  sv.Pool + "/" + sv.Name,
		Type:     sv.SubvolumeType,
		Healthy:  true,
	}

	// Extract protocol
	if props != nil {
		status.Protocol = props[nastyapi.PropertyProtocol]

		// Extract capacity from properties
		if capStr := props[nastyapi.PropertyCapacityBytes]; capStr != "" {
			status.CapacityBytes = nastyapi.StringToInt64(capStr)
			status.CapacityHuman = dashboard.FormatBytes(status.CapacityBytes)
		}
	}

	// Used bytes from subvolume
	if sv.UsedBytes != nil {
		status.UsedBytes = int64(*sv.UsedBytes)
		status.UsedHuman = dashboard.FormatBytes(status.UsedBytes)
	}

	// Check protocol-specific resources
	switch status.Protocol {
	case nastyapi.ProtocolNFS:
		if err := checkNFSStatus(ctx, client, sv, props, status); err != nil {
			status.Healthy = false
			status.Issues = append(status.Issues, err.Error())
		}

	case nastyapi.ProtocolNVMeOF:
		if err := checkNVMeOFStatus(ctx, client, sv, props, status); err != nil {
			status.Healthy = false
			status.Issues = append(status.Issues, err.Error())
		}
	}

	return status, nil
}

func checkNFSStatus(ctx context.Context, client nastyapi.ClientInterface, _ *nastyapi.Subvolume, props map[string]string, status *VolumeStatus) error {
	// Get NFS share ID from properties
	if props != nil {
		status.NFSShareID = props[nastyapi.PropertyNFSShareID]
		status.NFSSharePath = props[nastyapi.PropertyNFSSharePath]
	}

	if status.NFSShareID == "" {
		return errNFSShareIDNotFound
	}

	// Query NFS shares to verify share exists and is enabled
	shares, err := client.ListNFSShares(ctx)
	if err != nil {
		return fmt.Errorf("failed to query NFS shares: %w", err)
	}

	for _, share := range shares {
		if share.ID == status.NFSShareID {
			status.NFSEnabled = share.Enabled
			if status.NFSSharePath == "" {
				status.NFSSharePath = share.Path
			}
			if !share.Enabled {
				return fmt.Errorf("%w: %s", errNFSShareDisabled, share.ID)
			}
			return nil
		}
	}

	return fmt.Errorf("%w: %s", errNFSShareNotFound, status.NFSShareID)
}

func checkNVMeOFStatus(ctx context.Context, client nastyapi.ClientInterface, _ *nastyapi.Subvolume, props map[string]string, status *VolumeStatus) error {
	// Get NVMe-oF IDs from properties
	if props != nil {
		status.NVMeSubsystemID = props[nastyapi.PropertyNVMeSubsystemID]
		status.NVMeNQN = props[nastyapi.PropertyNVMeSubsystemNQN]
	}

	// Verify subsystem exists by NQN
	if status.NVMeNQN != "" {
		subsystem, err := client.GetNVMeOFSubsystemByNQN(ctx, status.NVMeNQN)
		if err != nil {
			return fmt.Errorf("failed to query NVMe-oF subsystem: %w", err)
		}
		if subsystem == nil {
			return fmt.Errorf("%w: %s", errNVMeSubsystemNotFound, status.NVMeNQN)
		}
		if status.NVMeSubsystemID == "" {
			status.NVMeSubsystemID = subsystem.ID
		}
	} else if status.NVMeSubsystemID != "" {
		// Fallback: scan all subsystems for matching ID
		subsystems, err := client.ListNVMeOFSubsystems(ctx)
		if err != nil {
			return fmt.Errorf("failed to query NVMe-oF subsystems: %w", err)
		}

		found := false
		for _, subsys := range subsystems {
			if subsys.ID == status.NVMeSubsystemID {
				found = true
				if status.NVMeNQN == "" {
					status.NVMeNQN = subsys.NQN
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %s", errNVMeSubsystemNotFound, status.NVMeSubsystemID)
		}
	}

	// errNVMeNamespaceNotFound is kept for error definitions but namespaces are now
	// embedded in the subsystem — no separate namespace check needed.
	_ = errNVMeNamespaceNotFound

	return nil
}

func outputStatus(status *VolumeStatus, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(status)

	case outputFormatTable, "":
		fmt.Printf("Volume:     %s\n", status.VolumeID)
		fmt.Printf("Dataset:    %s\n", status.Dataset)
		fmt.Printf("Protocol:   %s\n", protocolBadge(status.Protocol))
		fmt.Printf("Type:       %s\n", status.Type)
		fmt.Printf("Capacity:   %s\n", status.CapacityHuman)

		if status.Protocol == nastyapi.ProtocolNFS {
			colorHeader.Printf("\nNFS Status:\n") //nolint:errcheck,gosec
			fmt.Printf("  Share ID:   %s\n", status.NFSShareID)
			fmt.Printf("  Share Path: %s\n", status.NFSSharePath)
			fmt.Printf("  Enabled:    %t\n", status.NFSEnabled)
		}

		if status.Protocol == nastyapi.ProtocolNVMeOF {
			colorHeader.Printf("\nNVMe-oF Status:\n") //nolint:errcheck,gosec
			fmt.Printf("  Subsystem ID:  %s\n", status.NVMeSubsystemID)
			fmt.Printf("  NQN:           %s\n", status.NVMeNQN)
		}

		fmt.Printf("\nHealth:     ")
		if status.Healthy {
			colorSuccess.Printf("OK\n") //nolint:errcheck,gosec
		} else {
			colorError.Printf("UNHEALTHY\n") //nolint:errcheck,gosec
			for _, issue := range status.Issues {
				fmt.Printf("  %s %s\n", colorError.Sprint("-"), issue)
			}
		}

		return nil

	default:
		return fmt.Errorf("%w: %s", errStatusUnknownFormat, format)
	}
}
