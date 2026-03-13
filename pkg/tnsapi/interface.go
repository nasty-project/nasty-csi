// Package tnsapi provides a WebSocket client for the NASty storage API.
package tnsapi

import (
	"context"
)

// ClientInterface defines the interface for NASty API operations.
// This allows for dependency injection and easier testing.
//
//nolint:interfacebloat // NASty API client naturally has many methods covering different resource types
type ClientInterface interface {
	// Pool
	QueryPool(ctx context.Context, poolName string) (*Pool, error)

	// Subvolumes
	CreateSubvolume(ctx context.Context, params SubvolumeCreateParams) (*Subvolume, error)
	DeleteSubvolume(ctx context.Context, pool, name string) error
	GetSubvolume(ctx context.Context, pool, name string) (*Subvolume, error)
	ListAllSubvolumes(ctx context.Context, pool string) ([]Subvolume, error)

	// Properties (xattrs)
	SetSubvolumeProperties(ctx context.Context, pool, name string, props map[string]string) (*Subvolume, error)
	RemoveSubvolumeProperties(ctx context.Context, pool, name string, keys []string) (*Subvolume, error)
	FindSubvolumesByProperty(ctx context.Context, key, value, pool string) ([]Subvolume, error)
	FindManagedSubvolumes(ctx context.Context, pool string) ([]Subvolume, error)
	FindSubvolumeByCSIVolumeName(ctx context.Context, pool, volumeName string) (*Subvolume, error)

	// Snapshots
	CreateSnapshot(ctx context.Context, params SnapshotCreateParams) (*Snapshot, error)
	DeleteSnapshot(ctx context.Context, pool, subvolume, name string) error
	ListSnapshots(ctx context.Context, pool string) ([]Snapshot, error)

	// NFS
	CreateNFSShare(ctx context.Context, params NFSShareCreateParams) (*NFSShare, error)
	DeleteNFSShare(ctx context.Context, id string) error
	ListNFSShares(ctx context.Context) ([]NFSShare, error)
	GetNFSShare(ctx context.Context, id string) (*NFSShare, error)

	// SMB
	CreateSMBShare(ctx context.Context, params SMBShareCreateParams) (*SMBShare, error)
	DeleteSMBShare(ctx context.Context, id string) error
	ListSMBShares(ctx context.Context) ([]SMBShare, error)
	GetSMBShare(ctx context.Context, id string) (*SMBShare, error)

	// iSCSI
	CreateISCSITarget(ctx context.Context, params ISCSITargetCreateParams) (*ISCSITarget, error)
	AddISCSILun(ctx context.Context, targetID, backstorePath string) (*ISCSITarget, error)
	AddISCSIACL(ctx context.Context, targetID, initiatorIQN string) (*ISCSITarget, error)
	DeleteISCSITarget(ctx context.Context, id string) error
	ListISCSITargets(ctx context.Context) ([]ISCSITarget, error)
	GetISCSITargetByIQN(ctx context.Context, iqn string) (*ISCSITarget, error)

	// NVMe-oF
	CreateNVMeOFSubsystem(ctx context.Context, params NVMeOFCreateParams) (*NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystem(ctx context.Context, id string) error
	ListNVMeOFSubsystems(ctx context.Context) ([]NVMeOFSubsystem, error)
	GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*NVMeOFSubsystem, error)

	// Connection
	Close()
}

// Pool represents a NASty storage pool.
type Pool struct {
	Name           string  `json:"name"`
	Mounted        bool    `json:"mounted"`
	MountPoint     *string `json:"mount_point"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
}

// Subvolume represents a NASty subvolume (filesystem or block device).
type Subvolume struct {
	Name          string            `json:"name"`
	Pool          string            `json:"pool"`
	SubvolumeType string            `json:"subvolume_type"` // "filesystem" or "block"
	Path          string            `json:"path"`
	UsedBytes     *uint64           `json:"used_bytes"`
	Compression   *string           `json:"compression"`
	Comments      *string           `json:"comments"`
	VolsizeBytes  *uint64           `json:"volsize_bytes"`
	BlockDevice   *string           `json:"block_device"`
	Snapshots     []string          `json:"snapshots"`
	Owner         *string           `json:"owner"`
	Properties    map[string]string `json:"properties"`
}

// SubvolumeCreateParams holds parameters for subvolume creation.
type SubvolumeCreateParams struct {
	Pool          string  `json:"pool"`
	Name          string  `json:"name"`
	SubvolumeType string  `json:"subvolume_type"`
	VolsizeBytes  *uint64 `json:"volsize_bytes,omitempty"`
	Compression   string  `json:"compression,omitempty"`
	Comments      string  `json:"comments,omitempty"`
}

// Snapshot represents a NASty snapshot.
type Snapshot struct {
	Name      string `json:"name"`
	Subvolume string `json:"subvolume"`
	Pool      string `json:"pool"`
	Path      string `json:"path"`
	ReadOnly  bool   `json:"read_only"`
}

// SnapshotCreateParams holds parameters for snapshot creation.
type SnapshotCreateParams struct {
	Pool      string `json:"pool"`
	Subvolume string `json:"subvolume"`
	Name      string `json:"name"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

// NFSShare represents a NASty NFS share.
type NFSShare struct {
	ID      string      `json:"id"`
	Path    string      `json:"path"`
	Comment *string     `json:"comment"`
	Clients []NFSClient `json:"clients"`
	Enabled bool        `json:"enabled"`
}

// NFSClient represents a single NFS client entry with host and options.
type NFSClient struct {
	Host    string `json:"host"`
	Options string `json:"options"`
}

// NFSShareCreateParams holds parameters for NFS share creation.
type NFSShareCreateParams struct {
	Path    string      `json:"path"`
	Comment string      `json:"comment,omitempty"`
	Clients []NFSClient `json:"clients"`
	Enabled *bool       `json:"enabled,omitempty"`
}

// SMBShare represents a NASty SMB share.
type SMBShare struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Comment *string `json:"comment"`
	Enabled bool    `json:"enabled"`
}

// SMBShareCreateParams holds parameters for SMB share creation.
type SMBShareCreateParams struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Comment string `json:"comment,omitempty"`
}

// ISCSIPortal represents an iSCSI portal.
type ISCSIPortal struct {
	IP   string `json:"ip"`
	Port uint16 `json:"port"`
}

// ISCSILun represents a LUN attached to an iSCSI target.
type ISCSILun struct {
	LunID         uint32  `json:"lun_id"`
	BackstorePath string  `json:"backstore_path"`
	BackstoreName string  `json:"backstore_name"`
	BackstoreType string  `json:"backstore_type"`
	SizeBytes     *uint64 `json:"size_bytes"`
}

// ISCSITarget represents a NASty iSCSI target.
type ISCSITarget struct {
	ID      string        `json:"id"`
	IQN     string        `json:"iqn"`
	Portals []ISCSIPortal `json:"portals"`
	Luns    []ISCSILun    `json:"luns"`
	Enabled bool          `json:"enabled"`
}

// ISCSITargetCreateParams holds parameters for iSCSI target creation.
type ISCSITargetCreateParams struct {
	Name string `json:"name"`
}

// NVMeOFNamespace represents a namespace within an NVMe-oF subsystem.
type NVMeOFNamespace struct {
	NSID       uint32 `json:"nsid"`
	DevicePath string `json:"device_path"`
	Enabled    bool   `json:"enabled"`
}

// NVMeOFPort represents a port on an NVMe-oF subsystem.
type NVMeOFPort struct {
	PortID    uint16 `json:"port_id"`
	Transport string `json:"transport"`
	Addr      string `json:"addr"`
	ServiceID string `json:"service_id"`
}

// NVMeOFSubsystem represents a NASty NVMe-oF subsystem.
type NVMeOFSubsystem struct {
	ID           string            `json:"id"`
	NQN          string            `json:"nqn"`
	Namespaces   []NVMeOFNamespace `json:"namespaces"`
	Ports        []NVMeOFPort      `json:"ports"`
	AllowedHosts []string          `json:"allowed_hosts"`
	AllowAnyHost bool              `json:"allow_any_host"`
	Enabled      bool              `json:"enabled"`
}

// NVMeOFCreateParams holds parameters for NVMe-oF subsystem creation.
type NVMeOFCreateParams struct {
	Name       string   `json:"name"`
	DevicePath string   `json:"device_path"`
	Addr       string   `json:"addr,omitempty"`
	Port       *uint16  `json:"port,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
}

// Verify that Client implements ClientInterface at compile time.
var _ ClientInterface = (*Client)(nil)
