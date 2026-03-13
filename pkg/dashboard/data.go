package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nasty-project/nasty-csi/pkg/nasty-api"
)

// Static errors for data operations.
var (
	errVolumeNotFound = errors.New("volume not found")
	errNoSharePath    = errors.New("no share path found")
	errNoNFSShare     = errors.New("no NFS share found")
	errNoSMBShare     = errors.New("no SMB share found")
	errNoSubsystemNQN = errors.New("no subsystem NQN found")
	errNoISCSIIQN     = errors.New("no iSCSI IQN found")
)

// FindManagedVolumes finds all subvolumes managed by nasty-csi.
// If clusterID is non-empty, only returns volumes that either match the clusterID
// or have no cluster_id property (legacy volumes).
func FindManagedVolumes(ctx context.Context, client nastyapi.ClientInterface, clusterID string) ([]VolumeInfo, error) {
	subvols, err := client.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, "")
	if err != nil {
		return nil, err
	}
	volumes := extractVolumes(subvols)
	return filterByClusterID(volumes, clusterID), nil
}

// FindManagedSnapshots finds all snapshots managed by nasty-csi.
func FindManagedSnapshots(ctx context.Context, client nastyapi.ClientInterface, clusterID string) ([]SnapshotInfo, error) {
	snaps, err := client.ListSnapshots(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	// Find managed subvolumes to cross-reference snapshot ownership
	subvols, err := client.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, "")
	if err != nil {
		return nil, fmt.Errorf("failed to find managed subvolumes: %w", err)
	}
	if clusterID != "" {
		subvols = filterSubvolumesByClusterID(subvols, clusterID)
	}

	managedSubvols := make(map[string]struct {
		volumeID string
		protocol string
	})
	for _, sv := range subvols {
		volumeID := sv.Properties[nastyapi.PropertyCSIVolumeName]
		protocol := sv.Properties[nastyapi.PropertyProtocol]
		if volumeID != "" {
			key := sv.Pool + "/" + sv.Name
			managedSubvols[key] = struct {
				volumeID string
				protocol string
			}{volumeID: volumeID, protocol: protocol}
		}
	}

	var snapshots []SnapshotInfo
	for _, snap := range snaps {
		subvolKey := snap.Pool + "/" + snap.Subvolume
		meta, ok := managedSubvols[subvolKey]
		if !ok {
			continue
		}
		snapshots = append(snapshots, SnapshotInfo{
			Name:          snap.Name,
			SourceVolume:  meta.volumeID,
			SourceDataset: subvolKey,
			Protocol:      meta.protocol,
			Type:          "attached",
		})
	}

	return snapshots, nil
}

// FindClonedVolumes finds all volumes that were cloned from snapshots or other volumes.
func FindClonedVolumes(ctx context.Context, client nastyapi.ClientInterface, clusterID string) ([]CloneInfo, error) {
	subvols, err := client.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, "")
	if err != nil {
		return nil, err
	}
	if clusterID != "" {
		subvols = filterSubvolumesByClusterID(subvols, clusterID)
	}
	return extractClones(subvols), nil
}

// FindUnmanagedVolumes finds volumes not managed by nasty-csi.
func FindUnmanagedVolumes(ctx context.Context, client nastyapi.ClientInterface, searchPath string, showAll bool, clusterID string) ([]UnmanagedVolume, error) {
	allSubvols, err := client.ListAllSubvolumes(ctx, searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to list subvolumes: %w", err)
	}

	managedSubvols, err := client.FindManagedSubvolumes(ctx, searchPath)
	if err != nil {
		managedSubvols = nil
	}

	managedIDs := make(map[string]bool)
	for i := range managedSubvols {
		managedIDs[managedSubvols[i].Pool+"/"+managedSubvols[i].Name] = true
	}

	nfsShares, err := client.ListNFSShares(ctx)
	if err != nil {
		nfsShares = nil
	}
	nfsShareByPath := make(map[string]*nastyapi.NFSShare)
	for i := range nfsShares {
		nfsShareByPath[nfsShares[i].Path] = &nfsShares[i]
	}

	var volumes []UnmanagedVolume
	for i := range allSubvols {
		sv := &allSubvols[i]
		svID := sv.Pool + "/" + sv.Name

		if managedIDs[svID] {
			continue
		}

		vol := UnmanagedVolume{
			Dataset: svID,
			Name:    sv.Name,
			Type:    sv.SubvolumeType,
		}

		if sv.UsedBytes != nil {
			vol.SizeBytes = int64(*sv.UsedBytes)
			vol.Size = FormatBytes(vol.SizeBytes)
		}

		if share, ok := nfsShareByPath[sv.Path]; ok {
			vol.Protocol = protocolNFS
			vol.NFSShareID = share.ID
			vol.NFSSharePath = share.Path
		} else if sv.SubvolumeType == "block" {
			vol.Protocol = "block"
		}

		volumes = append(volumes, vol)
	}

	return volumes, nil
}

// GetVolumeDetails retrieves detailed information about a volume.
//
//nolint:gocyclo // complexity from protocol and property extraction is acceptable
func GetVolumeDetails(ctx context.Context, client nastyapi.ClientInterface, volumeRef string) (*VolumeDetails, error) {
	var subvol *nastyapi.Subvolume

	sv, err := client.FindSubvolumeByCSIVolumeName(ctx, "", volumeRef)
	if err == nil && sv != nil {
		subvol = sv
	} else {
		subvols, findErr := client.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, "")
		if findErr != nil {
			return nil, fmt.Errorf("failed to query subvolumes: %w", findErr)
		}
		for i := range subvols {
			if subvols[i].Pool+"/"+subvols[i].Name == volumeRef {
				subvol = &subvols[i]
				break
			}
		}
	}

	if subvol == nil {
		return nil, fmt.Errorf("%w: %s", errVolumeNotFound, volumeRef)
	}

	svID := subvol.Pool + "/" + subvol.Name
	details := &VolumeDetails{
		Dataset:    svID,
		Type:       subvol.SubvolumeType,
		MountPath:  subvol.Path,
		Properties: make(map[string]string),
	}

	if subvol.UsedBytes != nil {
		details.UsedBytes = int64(*subvol.UsedBytes)
		details.UsedHuman = FormatBytes(details.UsedBytes)
	}

	for key, value := range subvol.Properties {
		details.Properties[key] = value

		switch key {
		case nastyapi.PropertyCSIVolumeName:
			details.VolumeID = value
		case nastyapi.PropertyProtocol:
			details.Protocol = value
		case nastyapi.PropertyCapacityBytes:
			details.CapacityBytes = nastyapi.StringToInt64(value)
			details.CapacityHuman = FormatBytes(details.CapacityBytes)
		case nastyapi.PropertyCreatedAt:
			details.CreatedAt = value
		case nastyapi.PropertyDeleteStrategy:
			details.DeleteStrategy = value
		case nastyapi.PropertyAdoptable:
			details.Adoptable = value == valueTrue
		case nastyapi.PropertyContentSourceType:
			details.ContentSourceType = value
		case nastyapi.PropertyContentSourceID:
			details.ContentSourceID = value
		case nastyapi.PropertyCloneMode:
			details.CloneMode = value
		case nastyapi.PropertyOriginSnapshot:
			details.OriginSnapshot = value
		}
	}

	switch details.Protocol {
	case protocolNFS:
		if shareDetails, shareErr := getNFSShareDetails(ctx, client, subvol); shareErr == nil {
			details.NFSShare = shareDetails
		}
	case protocolNVMeOF:
		if subsysDetails, subsysErr := getNVMeOFSubsystemDetails(ctx, client, subvol); subsysErr == nil {
			details.NVMeOFSubsystem = subsysDetails
		}
	case protocolSMB:
		if smbDetails, smbErr := getSMBShareDetails(ctx, client, subvol); smbErr == nil {
			details.SMBShare = smbDetails
		}
	case protocolISCSI:
		if iscsiDetails, iscsiErr := getISCSITargetDetails(ctx, client, subvol); iscsiErr == nil {
			details.ISCSITarget = iscsiDetails
		}
	}

	return details, nil
}

func getNFSShareDetails(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume) (*NFSShareDetails, error) {
	sharePath := sv.Properties[nastyapi.PropertyNFSSharePath]
	if sharePath == "" {
		sharePath = sv.Path
	}
	if sharePath == "" {
		return nil, errNoSharePath
	}

	shares, err := client.ListNFSShares(ctx)
	if err != nil {
		return nil, err
	}
	for _, share := range shares {
		if share.Path == sharePath {
			clients := make([]string, 0, len(share.Clients))
			for _, c := range share.Clients {
				clients = append(clients, c.Host)
			}
			return &NFSShareDetails{
				ID:      share.ID,
				Path:    share.Path,
				Clients: clients,
				Enabled: share.Enabled,
			}, nil
		}
	}
	return nil, fmt.Errorf("%w for path %s", errNoNFSShare, sharePath)
}

func getNVMeOFSubsystemDetails(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume) (*NVMeOFSubsystemDetails, error) {
	nqn := sv.Properties[nastyapi.PropertyNVMeSubsystemNQN]
	if nqn == "" {
		return nil, errNoSubsystemNQN
	}

	subsystem, err := client.GetNVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		return nil, err
	}
	if subsystem == nil {
		return nil, fmt.Errorf("NVMe-oF subsystem not found: %s", nqn)
	}

	return &NVMeOFSubsystemDetails{
		ID:      subsystem.ID,
		NQN:     subsystem.NQN,
		Enabled: subsystem.Enabled,
	}, nil
}

func getSMBShareDetails(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume) (*SMBShareDetails, error) {
	// Try by stored share ID first
	if shareID := sv.Properties[nastyapi.PropertySMBShareID]; shareID != "" {
		share, shareErr := client.GetSMBShare(ctx, shareID)
		if shareErr == nil {
			return &SMBShareDetails{
				ID:      share.ID,
				Name:    share.Name,
				Path:    share.Path,
				Enabled: share.Enabled,
			}, nil
		}
	}

	// Fall back to searching by path
	sharePath := sv.Path
	if sharePath == "" {
		return nil, errNoSharePath
	}

	shares, err := client.ListSMBShares(ctx)
	if err != nil {
		return nil, err
	}
	for _, share := range shares {
		if share.Path == sharePath {
			return &SMBShareDetails{
				ID:      share.ID,
				Name:    share.Name,
				Path:    share.Path,
				Enabled: share.Enabled,
			}, nil
		}
	}
	return nil, fmt.Errorf("%w for path %s", errNoSMBShare, sharePath)
}

func getISCSITargetDetails(ctx context.Context, client nastyapi.ClientInterface, sv *nastyapi.Subvolume) (*ISCSITargetDetails, error) {
	iqn := sv.Properties[nastyapi.PropertyISCSIIQN]
	if iqn == "" {
		return nil, errNoISCSIIQN
	}

	target, err := client.GetISCSITargetByIQN(ctx, iqn)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("iSCSI target not found: %s", iqn)
	}

	return &ISCSITargetDetails{
		ID:  target.ID,
		IQN: target.IQN,
	}, nil
}

// extractVolumes extracts VolumeInfo from pre-fetched managed subvolumes (no API calls).
func extractVolumes(subvols []nastyapi.Subvolume) []VolumeInfo {
	var volumes []VolumeInfo
	for _, sv := range subvols {
		if sv.Properties[nastyapi.PropertyDetachedSnapshot] == valueTrue {
			continue
		}

		volumeID := sv.Properties[nastyapi.PropertyCSIVolumeName]
		if volumeID == "" {
			continue
		}

		vol := VolumeInfo{
			Dataset:  sv.Pool + "/" + sv.Name,
			VolumeID: volumeID,
			Type:     sv.SubvolumeType,
		}

		vol.Protocol = sv.Properties[nastyapi.PropertyProtocol]
		if v := sv.Properties[nastyapi.PropertyCapacityBytes]; v != "" {
			vol.CapacityBytes = nastyapi.StringToInt64(v)
			vol.CapacityHuman = FormatBytes(vol.CapacityBytes)
		}
		vol.DeleteStrategy = sv.Properties[nastyapi.PropertyDeleteStrategy]
		vol.Adoptable = sv.Properties[nastyapi.PropertyAdoptable] == valueTrue
		vol.ClusterID = sv.Properties[nastyapi.PropertyClusterID]
		vol.ContentSourceType = sv.Properties[nastyapi.PropertyContentSourceType]
		vol.ContentSourceID = sv.Properties[nastyapi.PropertyContentSourceID]

		volumes = append(volumes, vol)
	}
	return volumes
}

// extractClones extracts CloneInfo from pre-fetched managed subvolumes (no API calls).
func extractClones(subvols []nastyapi.Subvolume) []CloneInfo {
	var clones []CloneInfo
	for _, sv := range subvols {
		if sv.Properties[nastyapi.PropertyDetachedSnapshot] == valueTrue {
			continue
		}

		sourceType := sv.Properties[nastyapi.PropertyContentSourceType]
		if sourceType == "" {
			continue
		}

		clone := CloneInfo{
			Dataset:    sv.Pool + "/" + sv.Name,
			SourceType: sourceType,
		}

		clone.VolumeID = sv.Properties[nastyapi.PropertyCSIVolumeName]
		clone.Protocol = sv.Properties[nastyapi.PropertyProtocol]
		clone.SourceID = sv.Properties[nastyapi.PropertyContentSourceID]
		clone.OriginSnapshot = sv.Properties[nastyapi.PropertyOriginSnapshot]

		clone.CloneMode = sv.Properties[nastyapi.PropertyCloneMode]
		if clone.CloneMode == "" {
			clone.CloneMode = nastyapi.CloneModeCOW
		}

		switch clone.CloneMode {
		case nastyapi.CloneModeCOW:
			clone.DependencyNote = "Source snapshot CANNOT be deleted"
		case nastyapi.CloneModePromoted:
			clone.DependencyNote = "Source snapshot CAN be deleted"
		case nastyapi.CloneModeDetached:
			clone.DependencyNote = "Fully independent (no dependencies)"
		default:
			clone.DependencyNote = "Unknown mode"
		}

		clones = append(clones, clone)
	}
	return clones
}

// filterByClusterID filters volumes to only include those matching the cluster ID.
// Volumes with no ClusterID (legacy) are always included.
func filterByClusterID(volumes []VolumeInfo, clusterID string) []VolumeInfo {
	if clusterID == "" {
		return volumes
	}
	filtered := make([]VolumeInfo, 0, len(volumes))
	for i := range volumes {
		if volumes[i].ClusterID == "" || volumes[i].ClusterID == clusterID {
			filtered = append(filtered, volumes[i])
		}
	}
	return filtered
}

// filterSubvolumesByClusterID filters subvolumes to only include those matching the cluster ID.
// Subvolumes with no cluster_id property (legacy) are always included.
func filterSubvolumesByClusterID(subvols []nastyapi.Subvolume, clusterID string) []nastyapi.Subvolume {
	if clusterID == "" {
		return subvols
	}
	filtered := make([]nastyapi.Subvolume, 0, len(subvols))
	for i := range subvols {
		prop := subvols[i].Properties[nastyapi.PropertyClusterID]
		if prop == "" || prop == clusterID {
			filtered = append(filtered, subvols[i])
		}
	}
	return filtered
}

// extractDetachedSnapshots is kept for interface compatibility but returns empty for NASty.
// Detached snapshots (ZFS send/receive) are a NASty concept not used with bcachefs.
func extractDetachedSnapshots(_ []nastyapi.Subvolume) []SnapshotInfo {
	return nil
}

// FormatBytes converts bytes to human-readable format.
func FormatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1fTi", float64(bytes)/TB)
	case bytes >= GB:
		return fmt.Sprintf("%.1fGi", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1fMi", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1fKi", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// isSystemDataset is kept for compatibility — checks if a path looks like a system path.
func isSystemDataset(name, _ string) bool {
	systemPrefixes := []string{"ix-applications", "ix-", ".system", "iocage"}
	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
