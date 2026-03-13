// Package tnsapi provides a WebSocket client for the NASty storage API.
package tnsapi

import "strconv"

// Schema versioning for future migrations.
const (
	// PropertySchemaVersion stores the metadata schema version.
	// Value: "1" for Schema v1.
	PropertySchemaVersion = "nasty-csi:schema_version"

	// SchemaVersionV1 is the current schema version.
	SchemaVersionV1 = "1"
)

// Xattr Property Constants - Schema v1
//
// These properties are stored as POSIX xattrs (user.* namespace) on bcachefs subvolumes
// to track CSI metadata. This approach provides:
// - Reliable metadata storage native to the filesystem (no sidecar files)
// - Ownership verification before deletion (prevents accidental deletion when IDs are reused)
// - Easy debugging via `getfattr -d <subvolume_path>` on NASty
// - Cross-cluster volume adoption support
//
// All properties use the "nasty-csi:" prefix to avoid conflicts with other tools.
const (
	// PropertyPrefix is the prefix for all nasty-csi xattr properties.
	PropertyPrefix = "nasty-csi:"

	// PropertyManagedBy indicates this resource is managed by nasty-csi.
	// Value: "nasty-csi".
	PropertyManagedBy = "nasty-csi:managed_by"

	// PropertyCSIVolumeName stores the CSI volume name (PVC name).
	// Value: e.g., "pvc-12345678-1234-1234-1234-123456789012".
	PropertyCSIVolumeName = "nasty-csi:csi_volume_name"

	// PropertyCapacityBytes stores the volume capacity in bytes.
	// Value: e.g., "10737418240" for 10GiB.
	PropertyCapacityBytes = "nasty-csi:capacity_bytes"

	// PropertyProtocol stores the storage protocol used.
	// Value: "nfs", "nvmeof", "iscsi", or "smb".
	PropertyProtocol = "nasty-csi:protocol"

	// PropertyDeleteStrategy stores the deletion strategy for the volume.
	// Value: "delete" (default) or "retain".
	// When "retain", the volume will not be deleted when the PVC is deleted.
	PropertyDeleteStrategy = "nasty-csi:delete_strategy"

	// PropertyCreatedAt stores the timestamp when the volume was created.
	// Value: RFC3339 timestamp, e.g., "2024-01-15T10:30:00Z".
	PropertyCreatedAt = "nasty-csi:created_at"
)

// Adoption metadata properties - for cross-cluster volume adoption.
const (
	// PropertyAdoptable marks a volume as adoptable by a new cluster.
	// When set to "true", CreateVolume will automatically adopt this volume
	// if found by CSI volume name, re-creating any missing NASty resources.
	// Value: "true" or "false".
	PropertyAdoptable = "nasty-csi:adoptable"

	// PropertyPVCName stores the original PVC name for adoption.
	// Value: e.g., "my-data".
	PropertyPVCName = "nasty-csi:pvc_name"

	// PropertyPVCNamespace stores the original PVC namespace for adoption.
	// Value: e.g., "default".
	PropertyPVCNamespace = "nasty-csi:pvc_namespace"

	// PropertyStorageClass stores the original StorageClass name for adoption.
	// Value: e.g., "nasty-nfs".
	PropertyStorageClass = "nasty-csi:storage_class"
)

// NFS-specific properties.
const (
	// PropertyNFSShareID stores the NASty NFS share ID (mutable on re-share).
	// Value: e.g., "42" (integer stored as string).
	PropertyNFSShareID = "nasty-csi:nfs_share_id"

	// PropertyNFSSharePath stores the NFS export path (stable identifier).
	// Value: e.g., "/mnt/tank/csi/pvc-xxx".
	PropertyNFSSharePath = "nasty-csi:nfs_share_path"
)

// NVMe-oF-specific properties.
const (
	// PropertyNVMeSubsystemID stores the NASty NVMe-oF subsystem ID (mutable).
	// Value: e.g., "338" (integer stored as string).
	PropertyNVMeSubsystemID = "nasty-csi:nvmeof_subsystem_id"

	// PropertyNVMeSubsystemNQN stores the NVMe-oF subsystem NQN (stable identifier).
	// Value: e.g., "nqn.2024.io.nasty:nvme:pvc-xxx".
	PropertyNVMeSubsystemNQN = "nasty-csi:nvmeof_subsystem_nqn"
)

// iSCSI-specific properties (future).
const (
	// PropertyISCSIIQN stores the iSCSI target IQN (stable identifier).
	// Value: e.g., "iqn.2024.io.nasty:target:pvc-xxx".
	PropertyISCSIIQN = "nasty-csi:iscsi_iqn"

	// PropertyISCSITargetID stores the NASty iSCSI target ID (mutable).
	// Value: e.g., "10" (integer stored as string).
	PropertyISCSITargetID = "nasty-csi:iscsi_target_id"
)

// Multi-cluster isolation properties.
const (
	// PropertyClusterID stores the cluster identifier for multi-cluster NASty sharing.
	// When multiple K8s clusters share a NASty box, this property distinguishes
	// which cluster owns each volume/snapshot.
	// Value: user-defined cluster identifier, e.g., "prod-east", "staging".
	PropertyClusterID = "nasty-csi:cluster_id"
)

// SMB-specific properties.
const (
	// PropertySMBShareID stores the NASty SMB share ID (mutable on re-share).
	// Value: e.g., "42" (integer stored as string).
	PropertySMBShareID = "nasty-csi:smb_share_id"

	// PropertySMBShareName stores the SMB share name (stable identifier).
	// Value: e.g., "pvc-xxx".
	PropertySMBShareName = "nasty-csi:smb_share_name"
)

// Snapshot-specific properties.
const (
	// PropertySnapshotID stores the CSI snapshot ID for detached snapshots.
	// Value: e.g., "snapshot-12345678-1234-1234-1234-123456789012".
	PropertySnapshotID = "nasty-csi:snapshot_id"

	// PropertySourceVolumeID stores the source volume ID for snapshots.
	// Value: e.g., "pvc-12345678-1234-1234-1234-123456789012".
	PropertySourceVolumeID = "nasty-csi:source_volume_id"

	// PropertyDetachedSnapshot indicates this dataset is a detached snapshot.
	// Value: "true" or "false".
	PropertyDetachedSnapshot = "nasty-csi:detached_snapshot"

	// PropertySourceDataset stores the source dataset path for detached snapshots.
	// Value: e.g., "pool/datasets/pvc-xxx".
	PropertySourceDataset = "nasty-csi:source_dataset"
)

// Clone/content source properties.
const (
	// PropertyContentSourceType stores the content source type for cloned volumes.
	// Value: "snapshot" or "volume".
	PropertyContentSourceType = "nasty-csi:content_source_type"

	// PropertyContentSourceID stores the content source ID for cloned volumes.
	// Value: The snapshot ID or volume ID used as source.
	PropertyContentSourceID = "nasty-csi:content_source_id"

	// PropertyCloneMode stores how the clone was created.
	// Value: "cow" (default COW clone), "promoted" (clone+promote), or "detached" (send/receive).
	// This affects deletion order and dependency relationships.
	PropertyCloneMode = "nasty-csi:clone_mode"

	// PropertyOriginSnapshot stores the ZFS origin snapshot for COW clones.
	// Value: Full ZFS snapshot path, e.g., "pool/dataset@snapshot".
	// Only set for COW clones (not promoted or detached).
	PropertyOriginSnapshot = "nasty-csi:origin_snapshot"
)

// Clone mode values.
const (
	// CloneModeCOW indicates a standard COW clone (clone depends on snapshot).
	CloneModeCOW = "cow"

	// CloneModePromoted indicates a promoted clone (dependency reversed).
	CloneModePromoted = "promoted"

	// CloneModeDetached indicates a detached clone via send/receive (no dependency).
	CloneModeDetached = "detached"
)

// Property values.
const (
	// ManagedByValue is the value stored in PropertyManagedBy.
	ManagedByValue = "nasty-csi"

	// ProtocolNFS indicates NFS protocol.
	ProtocolNFS = "nfs"

	// ProtocolNVMeOF indicates NVMe-oF protocol.
	ProtocolNVMeOF = "nvmeof"

	// ProtocolISCSI indicates iSCSI protocol.
	ProtocolISCSI = "iscsi"

	// ProtocolSMB indicates SMB/CIFS protocol.
	ProtocolSMB = "smb"

	// ContentSourceSnapshot indicates the volume was created from a snapshot.
	ContentSourceSnapshot = "snapshot"

	// ContentSourceVolume indicates the volume was created from another volume (clone).
	ContentSourceVolume = "volume"

	// DeleteStrategyDelete is the default strategy - volume is deleted when PVC is deleted.
	DeleteStrategyDelete = "delete"

	// DeleteStrategyRetain means the volume is retained when PVC is deleted.
	DeleteStrategyRetain = "retain"

	// PropertyValueTrue is the string value "true" used in boolean ZFS properties.
	PropertyValueTrue = "true"
)

// PropertyNames returns all tns-csi property names for querying.
func PropertyNames() []string {
	return []string{
		// Schema v1 core properties
		PropertySchemaVersion,
		PropertyManagedBy,
		PropertyCSIVolumeName,
		PropertyCapacityBytes,
		PropertyProtocol,
		PropertyDeleteStrategy,
		PropertyCreatedAt,
		// Adoption properties
		PropertyAdoptable,
		PropertyPVCName,
		PropertyPVCNamespace,
		PropertyStorageClass,
		// NFS properties
		PropertyNFSShareID,
		PropertyNFSSharePath,
		// NVMe-oF properties
		PropertyNVMeSubsystemID,
		PropertyNVMeSubsystemNQN,
		// iSCSI properties
		PropertyISCSIIQN,
		PropertyISCSITargetID,
		// SMB properties
		PropertySMBShareID,
		PropertySMBShareName,
		// Snapshot properties
		PropertySnapshotID,
		PropertySourceVolumeID,
		PropertyDetachedSnapshot,
		PropertySourceDataset,
		// Clone properties
		PropertyContentSourceType,
		PropertyContentSourceID,
		PropertyCloneMode,
		PropertyOriginSnapshot,
		// Multi-cluster
		PropertyClusterID,
	}
}

// NFSVolumeParams contains parameters for creating NFS volume properties.
type NFSVolumeParams struct {
	VolumeID       string
	CreatedAt      string
	DeleteStrategy string
	SharePath      string
	ShareIDStr     string // NASty API UUID share ID
	PVCName        string
	PVCNamespace   string
	StorageClass   string
	ClusterID      string
	CapacityBytes  int64
	Adoptable      bool // Mark volume as adoptable for cross-cluster adoption
}

// NFSVolumePropertiesV1 returns Schema v1 properties for an NFS volume.
//
//nolint:dupl // Intentionally similar structure to SMB volume properties
func NFSVolumePropertiesV1(params NFSVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:  SchemaVersionV1,
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  params.VolumeID,
		PropertyCapacityBytes:  int64ToString(params.CapacityBytes),
		PropertyProtocol:       ProtocolNFS,
		PropertyCreatedAt:      params.CreatedAt,
		PropertyDeleteStrategy: params.DeleteStrategy,
		PropertyNFSShareID:     params.ShareIDStr,
		PropertyNFSSharePath:   params.SharePath,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	if params.ClusterID != "" {
		props[PropertyClusterID] = params.ClusterID
	}
	return props
}

// NVMeOFVolumeParams contains parameters for creating NVMe-oF volume properties.
type NVMeOFVolumeParams struct {
	VolumeID        string
	CreatedAt       string
	DeleteStrategy  string
	SubsystemNQN    string
	SubsystemIDStr  string // NASty API UUID subsystem ID (preferred when non-empty)
	PVCName         string
	PVCNamespace    string
	StorageClass    string
	ClusterID       string
	CapacityBytes   int64
	Adoptable       bool // Mark volume as adoptable for cross-cluster adoption
}

// NVMeOFVolumePropertiesV1 returns Schema v1 properties for an NVMe-oF volume.
func NVMeOFVolumePropertiesV1(params NVMeOFVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:    SchemaVersionV1,
		PropertyManagedBy:        ManagedByValue,
		PropertyCSIVolumeName:    params.VolumeID,
		PropertyCapacityBytes:    int64ToString(params.CapacityBytes),
		PropertyProtocol:         ProtocolNVMeOF,
		PropertyCreatedAt:        params.CreatedAt,
		PropertyDeleteStrategy:   params.DeleteStrategy,
		PropertyNVMeSubsystemID:  params.SubsystemIDStr,
		PropertyNVMeSubsystemNQN: params.SubsystemNQN,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	if params.ClusterID != "" {
		props[PropertyClusterID] = params.ClusterID
	}
	return props
}

// ISCSIVolumeParams contains parameters for creating iSCSI volume properties.
type ISCSIVolumeParams struct {
	VolumeID       string
	CreatedAt      string
	DeleteStrategy string
	TargetIQN      string
	TargetIDStr    string // NASty API UUID target ID
	PVCName        string
	PVCNamespace   string
	StorageClass   string
	ClusterID      string
	CapacityBytes  int64
	Adoptable      bool // Mark volume as adoptable for cross-cluster adoption
}

// ISCSIVolumePropertiesV1 returns Schema v1 properties for an iSCSI volume.
func ISCSIVolumePropertiesV1(params ISCSIVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:  SchemaVersionV1,
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  params.VolumeID,
		PropertyCapacityBytes:  int64ToString(params.CapacityBytes),
		PropertyProtocol:       ProtocolISCSI,
		PropertyCreatedAt:      params.CreatedAt,
		PropertyDeleteStrategy: params.DeleteStrategy,
		PropertyISCSITargetID:  params.TargetIDStr,
		PropertyISCSIIQN:       params.TargetIQN,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	if params.ClusterID != "" {
		props[PropertyClusterID] = params.ClusterID
	}
	return props
}

// SMBVolumeParams contains parameters for creating SMB volume properties.
type SMBVolumeParams struct {
	VolumeID       string
	CreatedAt      string
	DeleteStrategy string
	ShareName      string
	ShareIDStr     string // NASty API UUID share ID (preferred when non-empty)
	PVCName        string
	PVCNamespace   string
	StorageClass   string
	ClusterID      string
	CapacityBytes  int64
	Adoptable      bool // Mark volume as adoptable for cross-cluster adoption
}

// SMBVolumePropertiesV1 returns Schema v1 properties for an SMB volume.
//
//nolint:dupl // Intentionally similar structure to NFS volume properties
func SMBVolumePropertiesV1(params SMBVolumeParams) map[string]string {
	props := map[string]string{
		PropertySchemaVersion:  SchemaVersionV1,
		PropertyManagedBy:      ManagedByValue,
		PropertyCSIVolumeName:  params.VolumeID,
		PropertyCapacityBytes:  int64ToString(params.CapacityBytes),
		PropertyProtocol:       ProtocolSMB,
		PropertyCreatedAt:      params.CreatedAt,
		PropertyDeleteStrategy: params.DeleteStrategy,
		PropertySMBShareID:     params.ShareIDStr,
		PropertySMBShareName:   params.ShareName,
	}
	// Add adoption properties if provided
	if params.PVCName != "" {
		props[PropertyPVCName] = params.PVCName
	}
	if params.PVCNamespace != "" {
		props[PropertyPVCNamespace] = params.PVCNamespace
	}
	if params.StorageClass != "" {
		props[PropertyStorageClass] = params.StorageClass
	}
	if params.Adoptable {
		props[PropertyAdoptable] = PropertyValueTrue
	}
	if params.ClusterID != "" {
		props[PropertyClusterID] = params.ClusterID
	}
	return props
}

// SnapshotParams contains parameters for creating snapshot properties.
type SnapshotParams struct {
	SnapshotID     string
	SourceVolumeID string
	Protocol       string
	SourceDataset  string
	ClusterID      string
	Detached       bool
}

// SnapshotPropertiesV1 returns Schema v1 properties for a snapshot.
func SnapshotPropertiesV1(params SnapshotParams) map[string]string {
	detachedValue := "false"
	if params.Detached {
		detachedValue = "true"
	}
	props := map[string]string{
		PropertySchemaVersion:    SchemaVersionV1,
		PropertyManagedBy:        ManagedByValue,
		PropertySnapshotID:       params.SnapshotID,
		PropertySourceVolumeID:   params.SourceVolumeID,
		PropertyProtocol:         params.Protocol,
		PropertyDetachedSnapshot: detachedValue,
		PropertyDeleteStrategy:   DeleteStrategyDelete,
	}
	if params.SourceDataset != "" {
		props[PropertySourceDataset] = params.SourceDataset
	}
	if params.ClusterID != "" {
		props[PropertyClusterID] = params.ClusterID
	}
	return props
}

// ClonedVolumeProperties returns additional properties for cloned volumes.
func ClonedVolumeProperties(sourceType, sourceID string) map[string]string {
	return map[string]string{
		PropertyContentSourceType: sourceType,
		PropertyContentSourceID:   sourceID,
	}
}

// ClonedVolumePropertiesV2 returns additional properties for cloned volumes with clone mode info.
// cloneMode: "cow", "promoted", or "detached"
// originSnapshot: The ZFS snapshot path the clone was created from (empty for detached clones).
func ClonedVolumePropertiesV2(sourceType, sourceID, cloneMode, originSnapshot string) map[string]string {
	props := map[string]string{
		PropertyContentSourceType: sourceType,
		PropertyContentSourceID:   sourceID,
		PropertyCloneMode:         cloneMode,
	}
	// Only set origin for COW clones (promoted and detached break/have no dependency)
	if originSnapshot != "" && cloneMode == CloneModeCOW {
		props[PropertyOriginSnapshot] = originSnapshot
	}
	return props
}

// int64ToString converts an int64 to string for ZFS property storage.
func int64ToString(i int64) string {
	return strconv.FormatInt(i, 10)
}

// StringToInt64 converts a string to int64, returns 0 on error.
// Exported for use in controllers when reading properties.
func StringToInt64(s string) int64 {
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return i
}

// GetSchemaVersion extracts the schema version from properties.
// Returns "0" if not set (legacy volume without schema version).
func GetSchemaVersion(props map[string]string) string {
	if v, ok := props[PropertySchemaVersion]; ok && v != "" {
		return v
	}
	return "0"
}

// IsSchemaV1 checks if properties are Schema v1.
func IsSchemaV1(props map[string]string) bool {
	return GetSchemaVersion(props) == SchemaVersionV1
}
