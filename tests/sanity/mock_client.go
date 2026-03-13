// Package sanity provides mock implementations for CSI sanity testing.
package sanity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/nasty-project/nasty-csi/pkg/tnsapi"
)

var (
	// ErrDatasetExists indicates a subvolume already exists.
	ErrDatasetExists = errors.New("dataset already exists")
	// ErrDatasetNotFound indicates a subvolume was not found.
	ErrDatasetNotFound = errors.New("dataset not found")
	// ErrNFSShareNotFound indicates an NFS share was not found.
	ErrNFSShareNotFound = errors.New("NFS share not found")
	// ErrNVMeOFTargetNotFound indicates an NVMe-oF subsystem was not found.
	ErrNVMeOFTargetNotFound = errors.New("NVMe-oF target not found")
	// ErrSnapshotNotFound indicates a snapshot was not found.
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrSubsystemNotFound indicates a subsystem was not found.
	ErrSubsystemNotFound = errors.New("subsystem not found")
	// ErrISCSITargetNotFound indicates an iSCSI target was not found.
	ErrISCSITargetNotFound = errors.New("iSCSI target not found")
	// ErrSMBShareNotFound indicates an SMB share was not found.
	ErrSMBShareNotFound = errors.New("SMB share not found")
)

// MockClient is a mock implementation of the NASty API client for sanity testing.
type MockClient struct {
	mu         sync.Mutex
	subvolumes map[string]*tnsapi.Subvolume  // key: "pool/name"
	snapshots  map[string]*tnsapi.Snapshot   // key: "pool/subvolume@name"
	nfsShares  map[string]*tnsapi.NFSShare   // key: UUID
	smbShares  map[string]*tnsapi.SMBShare   // key: UUID
	iscsiTargets map[string]*tnsapi.ISCSITarget // key: UUID
	nvmeofSubsystems map[string]*tnsapi.NVMeOFSubsystem // key: UUID
	nextID     uint64
}

// NewMockClient creates a new mock client for testing.
func NewMockClient() *MockClient {
	return &MockClient{
		subvolumes:       make(map[string]*tnsapi.Subvolume),
		snapshots:        make(map[string]*tnsapi.Snapshot),
		nfsShares:        make(map[string]*tnsapi.NFSShare),
		smbShares:        make(map[string]*tnsapi.SMBShare),
		iscsiTargets:     make(map[string]*tnsapi.ISCSITarget),
		nvmeofSubsystems: make(map[string]*tnsapi.NVMeOFSubsystem),
	}
}

func (m *MockClient) genID() string {
	return fmt.Sprintf("mock-%d", atomic.AddUint64(&m.nextID, 1))
}

// Close is a no-op for the mock client.
func (m *MockClient) Close() {}

// QueryPool returns a fake pool for testing.
func (m *MockClient) QueryPool(_ context.Context, poolName string) (*tnsapi.Pool, error) {
	total := uint64(10 * 1024 * 1024 * 1024)
	used := uint64(1 * 1024 * 1024 * 1024)
	return &tnsapi.Pool{
		Name:           poolName,
		Mounted:        true,
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: total - used,
	}, nil
}

// CreateSubvolume creates a subvolume in the mock.
func (m *MockClient) CreateSubvolume(_ context.Context, params tnsapi.SubvolumeCreateParams) (*tnsapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := params.Pool + "/" + params.Name
	if _, exists := m.subvolumes[key]; exists {
		return nil, ErrDatasetExists
	}

	sv := &tnsapi.Subvolume{
		Name:          params.Name,
		Pool:          params.Pool,
		SubvolumeType: params.SubvolumeType,
		Path:          "/" + params.Pool + "/" + params.Name,
		Properties:    make(map[string]string),
		Snapshots:     []string{},
	}
	if params.VolsizeBytes != nil {
		sv.VolsizeBytes = params.VolsizeBytes
		if params.SubvolumeType == "block" {
			dev := "/dev/zvol/" + params.Pool + "/" + params.Name
			sv.BlockDevice = &dev
		}
	}

	m.subvolumes[key] = sv
	return sv, nil
}

// DeleteSubvolume removes a subvolume from the mock.
func (m *MockClient) DeleteSubvolume(_ context.Context, pool, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := pool + "/" + name
	if _, exists := m.subvolumes[key]; !exists {
		return ErrDatasetNotFound
	}
	delete(m.subvolumes, key)
	return nil
}

// GetSubvolume retrieves a subvolume by pool and name.
func (m *MockClient) GetSubvolume(_ context.Context, pool, name string) (*tnsapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := pool + "/" + name
	sv, exists := m.subvolumes[key]
	if !exists {
		return nil, ErrDatasetNotFound
	}
	cp := *sv
	return &cp, nil
}

// ListAllSubvolumes lists all subvolumes in a pool.
func (m *MockClient) ListAllSubvolumes(_ context.Context, pool string) ([]tnsapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.Subvolume
	for key, sv := range m.subvolumes {
		if pool == "" || sv.Pool == pool {
			_ = key
			result = append(result, *sv)
		}
	}
	return result, nil
}

// SetSubvolumeProperties sets xattr properties on a subvolume.
func (m *MockClient) SetSubvolumeProperties(_ context.Context, pool, name string, props map[string]string) (*tnsapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := pool + "/" + name
	sv, exists := m.subvolumes[key]
	if !exists {
		return nil, ErrDatasetNotFound
	}
	if sv.Properties == nil {
		sv.Properties = make(map[string]string)
	}
	for k, v := range props {
		sv.Properties[k] = v
	}
	cp := *sv
	return &cp, nil
}

// RemoveSubvolumeProperties removes xattr properties from a subvolume.
func (m *MockClient) RemoveSubvolumeProperties(_ context.Context, pool, name string, keys []string) (*tnsapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := pool + "/" + name
	sv, exists := m.subvolumes[key]
	if !exists {
		return nil, ErrDatasetNotFound
	}
	for _, k := range keys {
		delete(sv.Properties, k)
	}
	cp := *sv
	return &cp, nil
}

// FindSubvolumesByProperty finds subvolumes by xattr property key/value pair.
func (m *MockClient) FindSubvolumesByProperty(_ context.Context, propKey, propValue, pool string) ([]tnsapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.Subvolume
	for _, sv := range m.subvolumes {
		if pool != "" && sv.Pool != pool {
			continue
		}
		if sv.Properties[propKey] == propValue {
			result = append(result, *sv)
		}
	}
	return result, nil
}

// FindManagedSubvolumes finds all subvolumes managed by nasty-csi.
func (m *MockClient) FindManagedSubvolumes(ctx context.Context, pool string) ([]tnsapi.Subvolume, error) {
	return m.FindSubvolumesByProperty(ctx, tnsapi.PropertyManagedBy, tnsapi.ManagedByValue, pool)
}

// FindSubvolumeByCSIVolumeName finds a subvolume by its CSI volume name xattr.
func (m *MockClient) FindSubvolumeByCSIVolumeName(ctx context.Context, pool, volumeName string) (*tnsapi.Subvolume, error) {
	subvols, err := m.FindSubvolumesByProperty(ctx, tnsapi.PropertyCSIVolumeName, volumeName, pool)
	if err != nil {
		return nil, err
	}
	if len(subvols) == 0 {
		return nil, tnsapi.ErrDatasetNotFound
	}
	return &subvols[0], nil
}

// CreateSnapshot creates a snapshot.
func (m *MockClient) CreateSnapshot(_ context.Context, params tnsapi.SnapshotCreateParams) (*tnsapi.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	svKey := params.Pool + "/" + params.Subvolume
	if _, exists := m.subvolumes[svKey]; !exists {
		return nil, ErrDatasetNotFound
	}

	snapKey := svKey + "@" + params.Name
	if _, exists := m.snapshots[snapKey]; exists {
		return nil, errors.New("snapshot already exists")
	}

	snap := &tnsapi.Snapshot{
		Name:      params.Name,
		Subvolume: params.Subvolume,
		Pool:      params.Pool,
		Path:      "/" + params.Pool + "/" + params.Subvolume + "@" + params.Name,
		ReadOnly:  params.ReadOnly,
	}
	m.snapshots[snapKey] = snap
	return snap, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *MockClient) DeleteSnapshot(_ context.Context, pool, subvolume, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapKey := pool + "/" + subvolume + "@" + name
	if _, exists := m.snapshots[snapKey]; !exists {
		return ErrSnapshotNotFound
	}
	delete(m.snapshots, snapKey)
	return nil
}

// ListSnapshots lists all snapshots in a pool.
func (m *MockClient) ListSnapshots(_ context.Context, pool string) ([]tnsapi.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []tnsapi.Snapshot
	for _, snap := range m.snapshots {
		if pool == "" || snap.Pool == pool {
			result = append(result, *snap)
		}
	}
	return result, nil
}

// CreateNFSShare creates an NFS share.
func (m *MockClient) CreateNFSShare(_ context.Context, params tnsapi.NFSShareCreateParams) (*tnsapi.NFSShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	enabled := true
	if params.Enabled != nil {
		enabled = *params.Enabled
	}
	comment := params.Comment
	share := &tnsapi.NFSShare{
		ID:      id,
		Path:    params.Path,
		Comment: &comment,
		Clients: params.Clients,
		Enabled: enabled,
	}
	m.nfsShares[id] = share
	return share, nil
}

// DeleteNFSShare deletes an NFS share by UUID.
func (m *MockClient) DeleteNFSShare(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.nfsShares[id]; !exists {
		return ErrNFSShareNotFound
	}
	delete(m.nfsShares, id)
	return nil
}

// ListNFSShares lists all NFS shares.
func (m *MockClient) ListNFSShares(_ context.Context) ([]tnsapi.NFSShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.NFSShare, 0, len(m.nfsShares))
	for _, share := range m.nfsShares {
		result = append(result, *share)
	}
	return result, nil
}

// GetNFSShare retrieves an NFS share by UUID.
func (m *MockClient) GetNFSShare(_ context.Context, id string) (*tnsapi.NFSShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	share, exists := m.nfsShares[id]
	if !exists {
		return nil, ErrNFSShareNotFound
	}
	cp := *share
	return &cp, nil
}

// CreateSMBShare creates an SMB share.
func (m *MockClient) CreateSMBShare(_ context.Context, params tnsapi.SMBShareCreateParams) (*tnsapi.SMBShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	comment := params.Comment
	share := &tnsapi.SMBShare{
		ID:      id,
		Name:    params.Name,
		Path:    params.Path,
		Comment: &comment,
		Enabled: true,
	}
	m.smbShares[id] = share
	return share, nil
}

// DeleteSMBShare deletes an SMB share by UUID.
func (m *MockClient) DeleteSMBShare(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.smbShares[id]; !exists {
		return ErrSMBShareNotFound
	}
	delete(m.smbShares, id)
	return nil
}

// ListSMBShares lists all SMB shares.
func (m *MockClient) ListSMBShares(_ context.Context) ([]tnsapi.SMBShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.SMBShare, 0, len(m.smbShares))
	for _, share := range m.smbShares {
		result = append(result, *share)
	}
	return result, nil
}

// GetSMBShare retrieves an SMB share by UUID.
func (m *MockClient) GetSMBShare(_ context.Context, id string) (*tnsapi.SMBShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	share, exists := m.smbShares[id]
	if !exists {
		return nil, ErrSMBShareNotFound
	}
	cp := *share
	return &cp, nil
}

// CreateISCSITarget creates an iSCSI target.
func (m *MockClient) CreateISCSITarget(_ context.Context, params tnsapi.ISCSITargetCreateParams) (*tnsapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	iqn := "iqn.2024-01.io.nasty:" + params.Name
	target := &tnsapi.ISCSITarget{
		ID:      id,
		IQN:     iqn,
		Portals: []tnsapi.ISCSIPortal{{IP: "0.0.0.0", Port: 3260}},
		Luns:    []tnsapi.ISCSILun{},
		Enabled: true,
	}
	m.iscsiTargets[id] = target
	return target, nil
}

// AddISCSILun adds a LUN to an iSCSI target.
func (m *MockClient) AddISCSILun(_ context.Context, targetID, backstorePath string) (*tnsapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	target, exists := m.iscsiTargets[targetID]
	if !exists {
		return nil, ErrISCSITargetNotFound
	}
	lun := tnsapi.ISCSILun{
		LunID:         uint32(len(target.Luns)),
		BackstorePath: backstorePath,
	}
	target.Luns = append(target.Luns, lun)
	cp := *target
	return &cp, nil
}

// AddISCSIACL adds an initiator ACL to an iSCSI target.
func (m *MockClient) AddISCSIACL(_ context.Context, targetID, _ string) (*tnsapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	target, exists := m.iscsiTargets[targetID]
	if !exists {
		return nil, ErrISCSITargetNotFound
	}
	cp := *target
	return &cp, nil
}

// DeleteISCSITarget deletes an iSCSI target by UUID.
func (m *MockClient) DeleteISCSITarget(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.iscsiTargets[id]; !exists {
		return ErrISCSITargetNotFound
	}
	delete(m.iscsiTargets, id)
	return nil
}

// ListISCSITargets lists all iSCSI targets.
func (m *MockClient) ListISCSITargets(_ context.Context) ([]tnsapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.ISCSITarget, 0, len(m.iscsiTargets))
	for _, t := range m.iscsiTargets {
		result = append(result, *t)
	}
	return result, nil
}

// GetISCSITargetByIQN finds an iSCSI target by IQN.
func (m *MockClient) GetISCSITargetByIQN(_ context.Context, iqn string) (*tnsapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.iscsiTargets {
		if t.IQN == iqn {
			cp := *t
			return &cp, nil
		}
	}
	return nil, nil //nolint:nilnil // nil, nil indicates "not found"
}

// CreateNVMeOFSubsystem creates an NVMe-oF subsystem.
func (m *MockClient) CreateNVMeOFSubsystem(_ context.Context, params tnsapi.NVMeOFCreateParams) (*tnsapi.NVMeOFSubsystem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	subsystem := &tnsapi.NVMeOFSubsystem{
		ID:           id,
		NQN:          "nqn.2024-01.io.nasty:" + params.Name,
		Namespaces:   []tnsapi.NVMeOFNamespace{{NSID: 1, DevicePath: params.DevicePath, Enabled: true}},
		Ports:        []tnsapi.NVMeOFPort{{PortID: 1, Transport: "tcp", Addr: params.Addr}},
		AllowedHosts: params.Hosts,
		AllowAnyHost: len(params.Hosts) == 0,
		Enabled:      true,
	}
	m.nvmeofSubsystems[id] = subsystem
	return subsystem, nil
}

// DeleteNVMeOFSubsystem deletes an NVMe-oF subsystem by UUID.
func (m *MockClient) DeleteNVMeOFSubsystem(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.nvmeofSubsystems[id]; !exists {
		return ErrSubsystemNotFound
	}
	delete(m.nvmeofSubsystems, id)
	return nil
}

// ListNVMeOFSubsystems lists all NVMe-oF subsystems.
func (m *MockClient) ListNVMeOFSubsystems(_ context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]tnsapi.NVMeOFSubsystem, 0, len(m.nvmeofSubsystems))
	for _, s := range m.nvmeofSubsystems {
		result = append(result, *s)
	}
	return result, nil
}

// GetNVMeOFSubsystemByNQN finds an NVMe-oF subsystem by NQN.
func (m *MockClient) GetNVMeOFSubsystemByNQN(_ context.Context, nqn string) (*tnsapi.NVMeOFSubsystem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.nvmeofSubsystems {
		if s.NQN == nqn {
			cp := *s
			return &cp, nil
		}
	}
	return nil, nil //nolint:nilnil // nil, nil indicates "not found"
}
