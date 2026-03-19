package driver

import (
	"context"

	nastyapi "github.com/nasty-project/nasty-go"
)

// MockAPIClient is a mock implementation of nastyapi.ClientInterface for testing.
// Each method has a corresponding function field that can be set by tests.
// If the function field is nil, the method panics to indicate unexpected calls.
type MockAPIClient struct {
	QueryPoolFunc                 func(ctx context.Context, poolName string) (*nastyapi.Pool, error)
	CreateSubvolumeFunc           func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error)
	DeleteSubvolumeFunc           func(ctx context.Context, pool, name string) error
	GetSubvolumeFunc              func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error)
	ListAllSubvolumesFunc         func(ctx context.Context, pool string) ([]nastyapi.Subvolume, error)
	SetSubvolumePropertiesFunc    func(ctx context.Context, pool, name string, props map[string]string) (*nastyapi.Subvolume, error)
	RemoveSubvolumePropertiesFunc func(ctx context.Context, pool, name string, keys []string) (*nastyapi.Subvolume, error)
	FindSubvolumesByPropertyFunc  func(ctx context.Context, key, value, pool string) ([]nastyapi.Subvolume, error)
	FindManagedSubvolumesFunc     func(ctx context.Context, pool string) ([]nastyapi.Subvolume, error)
	FindSubvolumeByCSIVolumeNameFunc func(ctx context.Context, pool, volumeName string) (*nastyapi.Subvolume, error)
	CreateSnapshotFunc            func(ctx context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error)
	DeleteSnapshotFunc            func(ctx context.Context, pool, subvolume, name string) error
	ListSnapshotsFunc             func(ctx context.Context, pool string) ([]nastyapi.Snapshot, error)
	CreateNFSShareFunc            func(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error)
	DeleteNFSShareFunc            func(ctx context.Context, id string) error
	ListNFSSharesFunc             func(ctx context.Context) ([]nastyapi.NFSShare, error)
	GetNFSShareFunc               func(ctx context.Context, id string) (*nastyapi.NFSShare, error)
	CreateSMBShareFunc            func(ctx context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error)
	DeleteSMBShareFunc            func(ctx context.Context, id string) error
	ListSMBSharesFunc             func(ctx context.Context) ([]nastyapi.SMBShare, error)
	GetSMBShareFunc               func(ctx context.Context, id string) (*nastyapi.SMBShare, error)
	CreateISCSITargetFunc         func(ctx context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error)
	AddISCSILunFunc               func(ctx context.Context, targetID, backstorePath string) (*nastyapi.ISCSITarget, error)
	AddISCSIACLFunc               func(ctx context.Context, targetID, initiatorIQN string) (*nastyapi.ISCSITarget, error)
	DeleteISCSITargetFunc         func(ctx context.Context, id string) error
	ListISCSITargetsFunc          func(ctx context.Context) ([]nastyapi.ISCSITarget, error)
	GetISCSITargetByIQNFunc       func(ctx context.Context, iqn string) (*nastyapi.ISCSITarget, error)
	CreateNVMeOFSubsystemFunc     func(ctx context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystemFunc     func(ctx context.Context, id string) error
	ListNVMeOFSubsystemsFunc      func(ctx context.Context) ([]nastyapi.NVMeOFSubsystem, error)
	GetNVMeOFSubsystemByNQNFunc   func(ctx context.Context, nqn string) (*nastyapi.NVMeOFSubsystem, error)
	ResizeSubvolumeFunc           func(ctx context.Context, pool, name string, volsizeBytes uint64) (*nastyapi.Subvolume, error)
	CloneSnapshotFunc             func(ctx context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error)
	CloneSubvolumeFunc            func(ctx context.Context, pool, name, newName string) (*nastyapi.Subvolume, error)
}

// MockAPIClientForSnapshots is an alias for MockAPIClient for backward compatibility in tests.
type MockAPIClientForSnapshots = MockAPIClient

func (m *MockAPIClient) Close() {}

func (m *MockAPIClient) QueryPool(ctx context.Context, poolName string) (*nastyapi.Pool, error) {
	if m.QueryPoolFunc != nil {
		return m.QueryPoolFunc(ctx, poolName)
	}
	panic("QueryPool called unexpectedly")
}

func (m *MockAPIClient) CreateSubvolume(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
	if m.CreateSubvolumeFunc != nil {
		return m.CreateSubvolumeFunc(ctx, params)
	}
	panic("CreateSubvolume called unexpectedly")
}

func (m *MockAPIClient) DeleteSubvolume(ctx context.Context, pool, name string) error {
	if m.DeleteSubvolumeFunc != nil {
		return m.DeleteSubvolumeFunc(ctx, pool, name)
	}
	panic("DeleteSubvolume called unexpectedly")
}

func (m *MockAPIClient) GetSubvolume(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
	if m.GetSubvolumeFunc != nil {
		return m.GetSubvolumeFunc(ctx, pool, name)
	}
	panic("GetSubvolume called unexpectedly")
}

func (m *MockAPIClient) ListAllSubvolumes(ctx context.Context, pool string) ([]nastyapi.Subvolume, error) {
	if m.ListAllSubvolumesFunc != nil {
		return m.ListAllSubvolumesFunc(ctx, pool)
	}
	panic("ListAllSubvolumes called unexpectedly")
}

func (m *MockAPIClient) SetSubvolumeProperties(ctx context.Context, pool, name string, props map[string]string) (*nastyapi.Subvolume, error) {
	if m.SetSubvolumePropertiesFunc != nil {
		return m.SetSubvolumePropertiesFunc(ctx, pool, name, props)
	}
	// Default: return empty subvolume (many tests don't care about this)
	return &nastyapi.Subvolume{Pool: pool, Name: name}, nil
}

func (m *MockAPIClient) RemoveSubvolumeProperties(ctx context.Context, pool, name string, keys []string) (*nastyapi.Subvolume, error) {
	if m.RemoveSubvolumePropertiesFunc != nil {
		return m.RemoveSubvolumePropertiesFunc(ctx, pool, name, keys)
	}
	panic("RemoveSubvolumeProperties called unexpectedly")
}

func (m *MockAPIClient) FindSubvolumesByProperty(ctx context.Context, key, value, pool string) ([]nastyapi.Subvolume, error) {
	if m.FindSubvolumesByPropertyFunc != nil {
		return m.FindSubvolumesByPropertyFunc(ctx, key, value, pool)
	}
	panic("FindSubvolumesByProperty called unexpectedly")
}

func (m *MockAPIClient) FindManagedSubvolumes(ctx context.Context, pool string) ([]nastyapi.Subvolume, error) {
	if m.FindManagedSubvolumesFunc != nil {
		return m.FindManagedSubvolumesFunc(ctx, pool)
	}
	panic("FindManagedSubvolumes called unexpectedly")
}

func (m *MockAPIClient) FindSubvolumeByCSIVolumeName(ctx context.Context, pool, volumeName string) (*nastyapi.Subvolume, error) {
	if m.FindSubvolumeByCSIVolumeNameFunc != nil {
		return m.FindSubvolumeByCSIVolumeNameFunc(ctx, pool, volumeName)
	}
	panic("FindSubvolumeByCSIVolumeName called unexpectedly")
}

func (m *MockAPIClient) CreateSnapshot(ctx context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, params)
	}
	panic("CreateSnapshot called unexpectedly")
}

func (m *MockAPIClient) DeleteSnapshot(ctx context.Context, pool, subvolume, name string) error {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, pool, subvolume, name)
	}
	panic("DeleteSnapshot called unexpectedly")
}

func (m *MockAPIClient) ListSnapshots(ctx context.Context, pool string) ([]nastyapi.Snapshot, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc(ctx, pool)
	}
	panic("ListSnapshots called unexpectedly")
}

func (m *MockAPIClient) CreateNFSShare(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
	if m.CreateNFSShareFunc != nil {
		return m.CreateNFSShareFunc(ctx, params)
	}
	panic("CreateNFSShare called unexpectedly")
}

func (m *MockAPIClient) DeleteNFSShare(ctx context.Context, id string) error {
	if m.DeleteNFSShareFunc != nil {
		return m.DeleteNFSShareFunc(ctx, id)
	}
	panic("DeleteNFSShare called unexpectedly")
}

func (m *MockAPIClient) ListNFSShares(ctx context.Context) ([]nastyapi.NFSShare, error) {
	if m.ListNFSSharesFunc != nil {
		return m.ListNFSSharesFunc(ctx)
	}
	panic("ListNFSShares called unexpectedly")
}

func (m *MockAPIClient) GetNFSShare(ctx context.Context, id string) (*nastyapi.NFSShare, error) {
	if m.GetNFSShareFunc != nil {
		return m.GetNFSShareFunc(ctx, id)
	}
	panic("GetNFSShare called unexpectedly")
}

func (m *MockAPIClient) CreateSMBShare(ctx context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
	if m.CreateSMBShareFunc != nil {
		return m.CreateSMBShareFunc(ctx, params)
	}
	panic("CreateSMBShare called unexpectedly")
}

func (m *MockAPIClient) DeleteSMBShare(ctx context.Context, id string) error {
	if m.DeleteSMBShareFunc != nil {
		return m.DeleteSMBShareFunc(ctx, id)
	}
	panic("DeleteSMBShare called unexpectedly")
}

func (m *MockAPIClient) ListSMBShares(ctx context.Context) ([]nastyapi.SMBShare, error) {
	if m.ListSMBSharesFunc != nil {
		return m.ListSMBSharesFunc(ctx)
	}
	panic("ListSMBShares called unexpectedly")
}

func (m *MockAPIClient) GetSMBShare(ctx context.Context, id string) (*nastyapi.SMBShare, error) {
	if m.GetSMBShareFunc != nil {
		return m.GetSMBShareFunc(ctx, id)
	}
	panic("GetSMBShare called unexpectedly")
}

func (m *MockAPIClient) CreateISCSITarget(ctx context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
	if m.CreateISCSITargetFunc != nil {
		return m.CreateISCSITargetFunc(ctx, params)
	}
	panic("CreateISCSITarget called unexpectedly")
}

func (m *MockAPIClient) AddISCSILun(ctx context.Context, targetID, backstorePath string) (*nastyapi.ISCSITarget, error) {
	if m.AddISCSILunFunc != nil {
		return m.AddISCSILunFunc(ctx, targetID, backstorePath)
	}
	panic("AddISCSILun called unexpectedly")
}

func (m *MockAPIClient) AddISCSIACL(ctx context.Context, targetID, initiatorIQN string) (*nastyapi.ISCSITarget, error) {
	if m.AddISCSIACLFunc != nil {
		return m.AddISCSIACLFunc(ctx, targetID, initiatorIQN)
	}
	panic("AddISCSIACL called unexpectedly")
}

func (m *MockAPIClient) DeleteISCSITarget(ctx context.Context, id string) error {
	if m.DeleteISCSITargetFunc != nil {
		return m.DeleteISCSITargetFunc(ctx, id)
	}
	panic("DeleteISCSITarget called unexpectedly")
}

func (m *MockAPIClient) ListISCSITargets(ctx context.Context) ([]nastyapi.ISCSITarget, error) {
	if m.ListISCSITargetsFunc != nil {
		return m.ListISCSITargetsFunc(ctx)
	}
	panic("ListISCSITargets called unexpectedly")
}

func (m *MockAPIClient) GetISCSITargetByIQN(ctx context.Context, iqn string) (*nastyapi.ISCSITarget, error) {
	if m.GetISCSITargetByIQNFunc != nil {
		return m.GetISCSITargetByIQNFunc(ctx, iqn)
	}
	panic("GetISCSITargetByIQN called unexpectedly")
}

func (m *MockAPIClient) CreateNVMeOFSubsystem(ctx context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error) {
	if m.CreateNVMeOFSubsystemFunc != nil {
		return m.CreateNVMeOFSubsystemFunc(ctx, params)
	}
	panic("CreateNVMeOFSubsystem called unexpectedly")
}

func (m *MockAPIClient) DeleteNVMeOFSubsystem(ctx context.Context, id string) error {
	if m.DeleteNVMeOFSubsystemFunc != nil {
		return m.DeleteNVMeOFSubsystemFunc(ctx, id)
	}
	panic("DeleteNVMeOFSubsystem called unexpectedly")
}

func (m *MockAPIClient) ListNVMeOFSubsystems(ctx context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
	if m.ListNVMeOFSubsystemsFunc != nil {
		return m.ListNVMeOFSubsystemsFunc(ctx)
	}
	panic("ListNVMeOFSubsystems called unexpectedly")
}

func (m *MockAPIClient) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*nastyapi.NVMeOFSubsystem, error) {
	if m.GetNVMeOFSubsystemByNQNFunc != nil {
		return m.GetNVMeOFSubsystemByNQNFunc(ctx, nqn)
	}
	panic("GetNVMeOFSubsystemByNQN called unexpectedly")
}

func (m *MockAPIClient) ResizeSubvolume(ctx context.Context, pool, name string, volsizeBytes uint64) (*nastyapi.Subvolume, error) {
	if m.ResizeSubvolumeFunc != nil {
		return m.ResizeSubvolumeFunc(ctx, pool, name, volsizeBytes)
	}
	return &nastyapi.Subvolume{Pool: pool, Name: name}, nil
}

func (m *MockAPIClient) CloneSnapshot(ctx context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error) {
	if m.CloneSnapshotFunc != nil {
		return m.CloneSnapshotFunc(ctx, params)
	}
	panic("CloneSnapshot called unexpectedly")
}

func (m *MockAPIClient) CloneSubvolume(ctx context.Context, pool, name, newName string) (*nastyapi.Subvolume, error) {
	if m.CloneSubvolumeFunc != nil {
		return m.CloneSubvolumeFunc(ctx, pool, name, newName)
	}
	return &nastyapi.Subvolume{Name: newName, Pool: pool}, nil
}
