package main

import (
	"context"
	"errors"

	"github.com/nasty-project/nasty-csi/pkg/tnsapi"
)

// Compile-time verification that mockClient implements tnsapi.ClientInterface.
var _ tnsapi.ClientInterface = (*mockClient)(nil)

// mockClient is a function-injection mock implementing tnsapi.ClientInterface.
// Each method has an optional func field; if nil, returns a default error.
type mockClient struct {
	// Pool operations
	QueryPoolFunc func(ctx context.Context, poolName string) (*tnsapi.Pool, error)

	// Subvolume operations
	CreateSubvolumeFunc          func(ctx context.Context, params tnsapi.SubvolumeCreateParams) (*tnsapi.Subvolume, error)
	DeleteSubvolumeFunc          func(ctx context.Context, pool, name string) error
	GetSubvolumeFunc             func(ctx context.Context, pool, name string) (*tnsapi.Subvolume, error)
	ListAllSubvolumesFunc        func(ctx context.Context, pool string) ([]tnsapi.Subvolume, error)
	SetSubvolumePropertiesFunc   func(ctx context.Context, pool, name string, props map[string]string) (*tnsapi.Subvolume, error)
	RemoveSubvolumePropertiesFunc func(ctx context.Context, pool, name string, keys []string) (*tnsapi.Subvolume, error)
	FindSubvolumesByPropertyFunc func(ctx context.Context, key, value, pool string) ([]tnsapi.Subvolume, error)
	FindManagedSubvolumesFunc    func(ctx context.Context, pool string) ([]tnsapi.Subvolume, error)
	FindSubvolumeByCSIVolumeNameFunc func(ctx context.Context, pool, volumeName string) (*tnsapi.Subvolume, error)

	// Snapshot operations
	CreateSnapshotFunc func(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error)
	DeleteSnapshotFunc func(ctx context.Context, pool, subvolume, name string) error
	ListSnapshotsFunc  func(ctx context.Context, pool string) ([]tnsapi.Snapshot, error)

	// NFS share operations
	CreateNFSShareFunc func(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error)
	DeleteNFSShareFunc func(ctx context.Context, id string) error
	ListNFSSharesFunc  func(ctx context.Context) ([]tnsapi.NFSShare, error)
	GetNFSShareFunc    func(ctx context.Context, id string) (*tnsapi.NFSShare, error)

	// SMB share operations
	CreateSMBShareFunc func(ctx context.Context, params tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error)
	DeleteSMBShareFunc func(ctx context.Context, id string) error
	ListSMBSharesFunc  func(ctx context.Context) ([]tnsapi.SMBShare, error)
	GetSMBShareFunc    func(ctx context.Context, id string) (*tnsapi.SMBShare, error)

	// iSCSI operations
	CreateISCSITargetFunc    func(ctx context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error)
	AddISCSILunFunc          func(ctx context.Context, targetID, backstorePath string) (*tnsapi.ISCSITarget, error)
	AddISCSIACLFunc          func(ctx context.Context, targetID, initiatorIQN string) (*tnsapi.ISCSITarget, error)
	DeleteISCSITargetFunc    func(ctx context.Context, id string) error
	ListISCSITargetsFunc     func(ctx context.Context) ([]tnsapi.ISCSITarget, error)
	GetISCSITargetByIQNFunc  func(ctx context.Context, iqn string) (*tnsapi.ISCSITarget, error)

	// NVMe-oF operations
	CreateNVMeOFSubsystemFunc   func(ctx context.Context, params tnsapi.NVMeOFCreateParams) (*tnsapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystemFunc   func(ctx context.Context, id string) error
	ListNVMeOFSubsystemsFunc    func(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error)
	GetNVMeOFSubsystemByNQNFunc func(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error)
}

// errNotImplemented is the default error returned when a mock function is not set.
var errNotImplemented = errors.New("not implemented in mock")

// Pool operations.

func (m *mockClient) QueryPool(ctx context.Context, poolName string) (*tnsapi.Pool, error) {
	if m.QueryPoolFunc != nil {
		return m.QueryPoolFunc(ctx, poolName)
	}
	return nil, errNotImplemented
}

// Subvolume operations.

func (m *mockClient) CreateSubvolume(ctx context.Context, params tnsapi.SubvolumeCreateParams) (*tnsapi.Subvolume, error) {
	if m.CreateSubvolumeFunc != nil {
		return m.CreateSubvolumeFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteSubvolume(ctx context.Context, pool, name string) error {
	if m.DeleteSubvolumeFunc != nil {
		return m.DeleteSubvolumeFunc(ctx, pool, name)
	}
	return errNotImplemented
}

func (m *mockClient) GetSubvolume(ctx context.Context, pool, name string) (*tnsapi.Subvolume, error) {
	if m.GetSubvolumeFunc != nil {
		return m.GetSubvolumeFunc(ctx, pool, name)
	}
	return nil, errNotImplemented
}

func (m *mockClient) ListAllSubvolumes(ctx context.Context, pool string) ([]tnsapi.Subvolume, error) {
	if m.ListAllSubvolumesFunc != nil {
		return m.ListAllSubvolumesFunc(ctx, pool)
	}
	return nil, errNotImplemented
}

func (m *mockClient) SetSubvolumeProperties(ctx context.Context, pool, name string, props map[string]string) (*tnsapi.Subvolume, error) {
	if m.SetSubvolumePropertiesFunc != nil {
		return m.SetSubvolumePropertiesFunc(ctx, pool, name, props)
	}
	return nil, errNotImplemented
}

func (m *mockClient) RemoveSubvolumeProperties(ctx context.Context, pool, name string, keys []string) (*tnsapi.Subvolume, error) {
	if m.RemoveSubvolumePropertiesFunc != nil {
		return m.RemoveSubvolumePropertiesFunc(ctx, pool, name, keys)
	}
	return nil, errNotImplemented
}

func (m *mockClient) FindSubvolumesByProperty(ctx context.Context, key, value, pool string) ([]tnsapi.Subvolume, error) {
	if m.FindSubvolumesByPropertyFunc != nil {
		return m.FindSubvolumesByPropertyFunc(ctx, key, value, pool)
	}
	return nil, errNotImplemented
}

func (m *mockClient) FindManagedSubvolumes(ctx context.Context, pool string) ([]tnsapi.Subvolume, error) {
	if m.FindManagedSubvolumesFunc != nil {
		return m.FindManagedSubvolumesFunc(ctx, pool)
	}
	return nil, errNotImplemented
}

func (m *mockClient) FindSubvolumeByCSIVolumeName(ctx context.Context, pool, volumeName string) (*tnsapi.Subvolume, error) {
	if m.FindSubvolumeByCSIVolumeNameFunc != nil {
		return m.FindSubvolumeByCSIVolumeNameFunc(ctx, pool, volumeName)
	}
	return nil, errNotImplemented
}

// Snapshot operations.

func (m *mockClient) CreateSnapshot(ctx context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteSnapshot(ctx context.Context, pool, subvolume, name string) error {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, pool, subvolume, name)
	}
	return errNotImplemented
}

func (m *mockClient) ListSnapshots(ctx context.Context, pool string) ([]tnsapi.Snapshot, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc(ctx, pool)
	}
	return nil, errNotImplemented
}

// NFS share operations.

func (m *mockClient) CreateNFSShare(ctx context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	if m.CreateNFSShareFunc != nil {
		return m.CreateNFSShareFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteNFSShare(ctx context.Context, id string) error {
	if m.DeleteNFSShareFunc != nil {
		return m.DeleteNFSShareFunc(ctx, id)
	}
	return errNotImplemented
}

func (m *mockClient) ListNFSShares(ctx context.Context) ([]tnsapi.NFSShare, error) {
	if m.ListNFSSharesFunc != nil {
		return m.ListNFSSharesFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) GetNFSShare(ctx context.Context, id string) (*tnsapi.NFSShare, error) {
	if m.GetNFSShareFunc != nil {
		return m.GetNFSShareFunc(ctx, id)
	}
	return nil, errNotImplemented
}

// SMB share operations.

func (m *mockClient) CreateSMBShare(ctx context.Context, params tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error) {
	if m.CreateSMBShareFunc != nil {
		return m.CreateSMBShareFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteSMBShare(ctx context.Context, id string) error {
	if m.DeleteSMBShareFunc != nil {
		return m.DeleteSMBShareFunc(ctx, id)
	}
	return errNotImplemented
}

func (m *mockClient) ListSMBShares(ctx context.Context) ([]tnsapi.SMBShare, error) {
	if m.ListSMBSharesFunc != nil {
		return m.ListSMBSharesFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) GetSMBShare(ctx context.Context, id string) (*tnsapi.SMBShare, error) {
	if m.GetSMBShareFunc != nil {
		return m.GetSMBShareFunc(ctx, id)
	}
	return nil, errNotImplemented
}

// iSCSI operations.

func (m *mockClient) CreateISCSITarget(ctx context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
	if m.CreateISCSITargetFunc != nil {
		return m.CreateISCSITargetFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) AddISCSILun(ctx context.Context, targetID, backstorePath string) (*tnsapi.ISCSITarget, error) {
	if m.AddISCSILunFunc != nil {
		return m.AddISCSILunFunc(ctx, targetID, backstorePath)
	}
	return nil, errNotImplemented
}

func (m *mockClient) AddISCSIACL(ctx context.Context, targetID, initiatorIQN string) (*tnsapi.ISCSITarget, error) {
	if m.AddISCSIACLFunc != nil {
		return m.AddISCSIACLFunc(ctx, targetID, initiatorIQN)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteISCSITarget(ctx context.Context, id string) error {
	if m.DeleteISCSITargetFunc != nil {
		return m.DeleteISCSITargetFunc(ctx, id)
	}
	return errNotImplemented
}

func (m *mockClient) ListISCSITargets(ctx context.Context) ([]tnsapi.ISCSITarget, error) {
	if m.ListISCSITargetsFunc != nil {
		return m.ListISCSITargetsFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) GetISCSITargetByIQN(ctx context.Context, iqn string) (*tnsapi.ISCSITarget, error) {
	if m.GetISCSITargetByIQNFunc != nil {
		return m.GetISCSITargetByIQNFunc(ctx, iqn)
	}
	return nil, errNotImplemented
}

// NVMe-oF operations.

func (m *mockClient) CreateNVMeOFSubsystem(ctx context.Context, params tnsapi.NVMeOFCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	if m.CreateNVMeOFSubsystemFunc != nil {
		return m.CreateNVMeOFSubsystemFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockClient) DeleteNVMeOFSubsystem(ctx context.Context, id string) error {
	if m.DeleteNVMeOFSubsystemFunc != nil {
		return m.DeleteNVMeOFSubsystemFunc(ctx, id)
	}
	return errNotImplemented
}

func (m *mockClient) ListNVMeOFSubsystems(ctx context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
	if m.ListNVMeOFSubsystemsFunc != nil {
		return m.ListNVMeOFSubsystemsFunc(ctx)
	}
	return nil, errNotImplemented
}

func (m *mockClient) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	if m.GetNVMeOFSubsystemByNQNFunc != nil {
		return m.GetNVMeOFSubsystemByNQNFunc(ctx, nqn)
	}
	return nil, errNotImplemented
}

// Connection management.

func (m *mockClient) Close() {
	// Mock client does not need cleanup.
}
