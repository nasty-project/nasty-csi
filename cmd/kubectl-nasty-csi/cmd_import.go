package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	"github.com/nasty-project/nasty-csi/pkg/nasty-api"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for import command.
var (
	errInvalidProtocol     = errors.New("invalid protocol: must be 'nfs', 'nvmeof', 'iscsi', or 'smb'")
	errAlreadyManaged      = errors.New("dataset is already managed by nasty-csi")
	errNoNFSShareForImport = errors.New("no NFS share found, use --create-share to create one")
	errPoolOrParentMissing = errors.New("either --pool or --parent must be specified")
	errISCSIRequiresZvol   = errors.New("iSCSI requires a zvol")
	errNoISCSITargetFound  = errors.New("no iSCSI target found for block device")
	errNoSMBShareForPath   = errors.New("no SMB share found for path")
)

// ImportResult contains the result of the import operation.
//
//nolint:govet // field alignment not critical for CLI output struct
type ImportResult struct {
	Dataset       string            `json:"dataset"                 yaml:"dataset"`
	VolumeID      string            `json:"volumeId"                yaml:"volumeId"`
	Protocol      string            `json:"protocol"                yaml:"protocol"`
	NFSShareID    string            `json:"nfsShareId,omitempty"    yaml:"nfsShareId,omitempty"`
	NFSSharePath  string            `json:"nfsSharePath,omitempty"  yaml:"nfsSharePath,omitempty"`
	ISCSITargetID string            `json:"iscsiTargetId,omitempty" yaml:"iscsiTargetId,omitempty"`
	ISCSIIQN      string            `json:"iscsiIqn,omitempty"      yaml:"iscsiIqn,omitempty"`
	SMBShareID    string            `json:"smbShareId,omitempty"    yaml:"smbShareId,omitempty"`
	SMBShareName  string            `json:"smbShareName,omitempty"  yaml:"smbShareName,omitempty"`
	CapacityBytes int64             `json:"capacityBytes"           yaml:"capacityBytes"`
	Properties    map[string]string `json:"properties"              yaml:"properties"`
	Success       bool              `json:"success"                 yaml:"success"`
	Message       string            `json:"message"                 yaml:"message"`
}

func newImportCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var (
		protocol     string
		volumeID     string
		createShare  bool
		storageClass string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "import <dataset-path>",
		Short: "Import an existing dataset into nasty-csi management",
		Long: `Import an existing NASty dataset into nasty-csi management.

This command adds nasty-csi properties to an existing dataset, allowing it to be
managed by the nasty-csi driver. This is useful for:
  - Migrating volumes from democratic-csi
  - Adopting manually created datasets
  - Taking over volumes from other CSI drivers

The command will:
  1. Verify the dataset exists
  2. Detect or create NFS share (for NFS protocol)
  3. Add nasty-csi management properties
  4. Prepare the volume for adoption into Kubernetes

After importing, use 'kubectl nasty-csi adopt <dataset>' to generate PV/PVC manifests.

Examples:
  # Import an NFS dataset (auto-detect existing share)
  kubectl nasty-csi import storage/k8s/pvc-xxx --protocol nfs

  # Import and create NFS share if missing
  kubectl nasty-csi import storage/data/myvolume --protocol nfs --create-share

  # Import with custom volume ID
  kubectl nasty-csi import storage/k8s/pvc-xxx --protocol nfs --volume-id my-volume

  # Dry run to see what would happen
  kubectl nasty-csi import storage/k8s/pvc-xxx --protocol nfs --dry-run

  # Import a zvol for NVMe-oF (future support)
  kubectl nasty-csi import storage/zvols/myvol --protocol nvmeof`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetPath := args[0]
			return runImport(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify,
				datasetPath, protocol, volumeID, createShare, storageClass, dryRun)
		},
	}

	cmd.Flags().StringVar(&protocol, "protocol", "", "Protocol: nfs or nvmeof (required)")
	cmd.Flags().StringVar(&volumeID, "volume-id", "", "Custom volume ID (defaults to dataset name)")
	cmd.Flags().BoolVar(&createShare, "create-share", false, "Create NFS share if it doesn't exist")
	cmd.Flags().StringVar(&storageClass, "storage-class", "", "StorageClass to associate with the volume")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes")

	//nolint:errcheck,gosec // MarkFlagRequired doesn't fail for valid flag names
	cmd.MarkFlagRequired("protocol")

	return cmd
}

//nolint:gocyclo,gocognit // complexity from protocol switch handling is acceptable
func runImport(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool,
	datasetPath, protocol, volumeID string, createShare bool, storageClass string, dryRun bool) error {

	// Validate protocol
	if protocol != protocolNFS && protocol != protocolNVMeOF && protocol != protocolISCSI && protocol != protocolSMB {
		return fmt.Errorf("%w: %s", errInvalidProtocol, protocol)
	}

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

	// Parse pool/name from dataset path
	pool, name := parsePoolName(datasetPath)

	// Verify dataset exists
	subvol, err := client.GetSubvolume(ctx, pool, name)
	if err != nil {
		return fmt.Errorf("dataset not found: %w", err)
	}

	// Check if already managed by nasty-csi
	if subvol.Properties != nil {
		if val, ok := subvol.Properties[nastyapi.PropertyManagedBy]; ok && val == nastyapi.ManagedByValue {
			return fmt.Errorf("%w: %s", errAlreadyManaged, datasetPath)
		}
	}

	// Prepare result
	result := &ImportResult{
		Dataset:    datasetPath,
		Protocol:   protocol,
		Properties: make(map[string]string),
	}

	// Determine volume ID
	if volumeID == "" {
		parts := strings.Split(datasetPath, "/")
		volumeID = parts[len(parts)-1]
	}
	result.VolumeID = volumeID

	// Get capacity from used space
	if subvol.UsedBytes != nil {
		result.CapacityBytes = int64(*subvol.UsedBytes)
	}

	// Build properties to set
	props := map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: volumeID,
		nastyapi.PropertyProtocol:      protocol,
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(result.CapacityBytes, 10),
		nastyapi.PropertyAdoptable:     nastyapi.PropertyValueTrue, // Mark as adoptable
	}

	if storageClass != "" {
		props[nastyapi.PropertyStorageClass] = storageClass
	}

	// Protocol-specific handling
	switch protocol {
	case protocolNFS:
		nfsProps, nfsErr := handleNFSImport(ctx, client, subvol, createShare, dryRun)
		if nfsErr != nil {
			return fmt.Errorf("NFS setup failed: %w", nfsErr)
		}
		for k, v := range nfsProps {
			if k == "_nfs_share_id" {
				result.NFSShareID = v
			} else {
				props[k] = v
			}
		}
		if sharePath, ok := nfsProps[nastyapi.PropertyNFSSharePath]; ok {
			result.NFSSharePath = sharePath
		}

	case protocolNVMeOF:
		// NVMe-oF import would need subsystem handling
		// For now, just warn that it's not fully supported
		fmt.Fprintln(os.Stderr, "Warning: NVMe-oF import is experimental. Subsystem must already exist.")

	case protocolISCSI:
		iscsiProps, iscsiErr := handleISCSIImport(ctx, client, subvol, dryRun)
		if iscsiErr != nil {
			return fmt.Errorf("iSCSI setup failed: %w", iscsiErr)
		}
		for k, v := range iscsiProps {
			switch k {
			case "_iscsi_target_id":
				result.ISCSITargetID = v
			default:
				props[k] = v
			}
		}
		if iqn, ok := iscsiProps[nastyapi.PropertyISCSIIQN]; ok {
			result.ISCSIIQN = iqn
		}

	case protocolSMB:
		smbProps, smbErr := handleSMBImport(ctx, client, subvol, dryRun)
		if smbErr != nil {
			return fmt.Errorf("SMB setup failed: %w", smbErr)
		}
		for k, v := range smbProps {
			if k == "_smb_share_id" {
				result.SMBShareID = v
			} else {
				props[k] = v
			}
		}
		if name, ok := smbProps[nastyapi.PropertySMBShareName]; ok {
			result.SMBShareName = name
		}
	}

	result.Properties = props

	if dryRun {
		result.Success = true
		result.Message = "Dry run - no changes made"
		fmt.Println("DRY RUN - Would set the following properties:")
		for k, v := range props {
			fmt.Printf("  %s = %s\n", k, v)
		}
		return outputImportResult(result, *outputFormat)
	}

	// Apply properties
	_, err = client.SetSubvolumeProperties(ctx, pool, name, props)
	if err != nil {
		result.Success = false
		result.Message = "Failed to set properties: " + err.Error()
		return outputImportResult(result, *outputFormat)
	}

	result.Success = true
	result.Message = "Volume imported successfully"

	if err := outputImportResult(result, *outputFormat); err != nil {
		return err
	}

	// Print next steps for table format
	if *outputFormat == "" || *outputFormat == outputFormatTable {
		fmt.Println("\nNext steps:")
		fmt.Printf("  kubectl nasty-csi adopt %s --pvc-name <name> --namespace <ns>\n", datasetPath)
	}

	return nil
}

func handleISCSIImport(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume, dryRun bool) (map[string]string, error) {
	props := make(map[string]string)

	// iSCSI volumes are block devices
	if sv.SubvolumeType != "block" {
		return nil, fmt.Errorf("%w: subvolume type is %s", errISCSIRequiresZvol, sv.SubvolumeType)
	}

	// Check if IQN is already stored in properties
	if sv.Properties != nil {
		if storedIQN, ok := sv.Properties[nastyapi.PropertyISCSIIQN]; ok && storedIQN != "" {
			// Look up target by IQN
			target, err := client.GetISCSITargetByIQN(ctx, storedIQN)
			if err == nil && target != nil {
				if dryRun {
					fmt.Printf("DRY RUN - Found iSCSI target by stored IQN:\n")
					fmt.Printf("  Target ID: %s, IQN: %s\n", target.ID, target.IQN)
					return props, nil
				}
				props[nastyapi.PropertyISCSIIQN] = target.IQN
				props[nastyapi.PropertyISCSITargetID] = target.ID
				props["_iscsi_target_id"] = target.ID
				fmt.Printf("Found iSCSI target by IQN: %s (ID: %s)\n", target.IQN, target.ID)
				return props, nil
			}
		}
	}

	// Scan all targets for one whose LUN backstore path matches the block device
	blockDevice := ""
	if sv.BlockDevice != nil {
		blockDevice = *sv.BlockDevice
	}

	targets, err := client.ListISCSITargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list iSCSI targets: %w", err)
	}

	var matchedTarget *nastyapi.ISCSITarget
	for i := range targets {
		t := &targets[i]
		for _, lun := range t.Luns {
			if blockDevice != "" && lun.BackstorePath == blockDevice {
				matchedTarget = t
				break
			}
		}
		if matchedTarget != nil {
			break
		}
	}

	if matchedTarget == nil {
		return nil, fmt.Errorf("%w: %s", errNoISCSITargetFound, blockDevice)
	}

	if dryRun {
		fmt.Printf("DRY RUN - Found iSCSI resources:\n")
		fmt.Printf("  Target: %s (ID: %s, IQN: %s)\n", matchedTarget.ID, matchedTarget.ID, matchedTarget.IQN)
		return props, nil
	}

	props[nastyapi.PropertyISCSIIQN] = matchedTarget.IQN
	props[nastyapi.PropertyISCSITargetID] = matchedTarget.ID
	props["_iscsi_target_id"] = matchedTarget.ID

	fmt.Printf("Found iSCSI target: %s (IQN: %s)\n", matchedTarget.ID, matchedTarget.IQN)
	return props, nil
}

func handleSMBImport(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume, dryRun bool) (map[string]string, error) {
	props := make(map[string]string)

	// Check for existing SMB share by path
	shares, err := client.ListSMBShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list SMB shares: %w", err)
	}

	var matchedShare *nastyapi.SMBShare
	for i := range shares {
		if shares[i].Path == sv.Path {
			matchedShare = &shares[i]
			break
		}
	}

	if matchedShare == nil {
		return nil, fmt.Errorf("%w: %s", errNoSMBShareForPath, sv.Path)
	}

	if dryRun {
		fmt.Printf("DRY RUN - Found SMB share: %s (ID: %s)\n", matchedShare.Name, matchedShare.ID)
		return props, nil
	}

	props[nastyapi.PropertySMBShareName] = matchedShare.Name
	props[nastyapi.PropertySMBShareID] = matchedShare.ID
	props["_smb_share_id"] = matchedShare.ID

	fmt.Printf("Found SMB share: %s (ID: %s)\n", matchedShare.Name, matchedShare.ID)
	return props, nil
}

func handleNFSImport(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume, createShare, dryRun bool) (map[string]string, error) {
	props := make(map[string]string)

	// Check for existing NFS share
	shares, err := client.ListNFSShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list NFS shares: %w", err)
	}

	var existingShare *nastyapi.NFSShare
	for i := range shares {
		if shares[i].Path == sv.Path {
			existingShare = &shares[i]
			break
		}
	}

	if existingShare != nil {
		// Use existing share
		props[nastyapi.PropertyNFSSharePath] = existingShare.Path
		props[nastyapi.PropertyNFSShareID] = existingShare.ID
		props["_nfs_share_id"] = existingShare.ID
		fmt.Printf("Found existing NFS share: %s (ID: %s)\n", existingShare.Path, existingShare.ID)
		return props, nil
	}

	// No existing share
	if !createShare {
		return nil, fmt.Errorf("%w: %s", errNoNFSShareForImport, sv.Path)
	}

	if dryRun {
		fmt.Printf("DRY RUN - Would create NFS share for path: %s\n", sv.Path)
		props[nastyapi.PropertyNFSSharePath] = sv.Path
		return props, nil
	}

	// Create NFS share
	enabled := true
	shareParams := nastyapi.NFSShareCreateParams{
		Path:    sv.Path,
		Comment: "nasty-csi imported volume: " + sv.Pool + "/" + sv.Name,
		Enabled: &enabled,
	}

	share, err := client.CreateNFSShare(ctx, shareParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create NFS share: %w", err)
	}

	props[nastyapi.PropertyNFSSharePath] = sv.Path
	props[nastyapi.PropertyNFSShareID] = share.ID
	props["_nfs_share_id"] = share.ID

	fmt.Printf("Created NFS share: %s (ID: %s)\n", sv.Path, share.ID)
	return props, nil
}

func outputImportResult(result *ImportResult, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(result)

	case outputFormatTable, "":
		if result.Success {
			printStepf(colorSuccess, iconOK, "Successfully imported %s", result.Dataset)
			fmt.Printf("  Volume ID: %s\n", result.VolumeID)
			fmt.Printf("  Protocol:  %s\n", protocolBadge(result.Protocol))
			fmt.Printf("  Capacity:  %s\n", dashboard.FormatBytes(result.CapacityBytes))
			if result.NFSSharePath != "" {
				fmt.Printf("  NFS Share: %s (ID: %s)\n", result.NFSSharePath, result.NFSShareID)
			}
			if result.ISCSIIQN != "" {
				fmt.Printf("  iSCSI IQN: %s (Target ID: %s)\n",
					result.ISCSIIQN, result.ISCSITargetID)
			}
			if result.SMBShareName != "" {
				fmt.Printf("  SMB Share: %s (ID: %s)\n", result.SMBShareName, result.SMBShareID)
			}
		} else {
			printStepf(colorError, iconError, "Failed to import %s: %s", result.Dataset, result.Message)
		}
		return nil

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}
