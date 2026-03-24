// Package sanity provides mock implementations for CSI sanity testing.
package sanity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	nastyapi "github.com/nasty-project/nasty-go"
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
	// ErrSnapshotAlreadyExists indicates a snapshot already exists.
	ErrSnapshotAlreadyExists = errors.New("snapshot already exists")
	// ErrSubsystemNotFound indicates a subsystem was not found.
	ErrSubsystemNotFound = errors.New("subsystem not found")
	// ErrISCSITargetNotFound indicates an iSCSI target was not found.
	ErrISCSITargetNotFound = errors.New("iSCSI target not found")
	// ErrSMBShareNotFound indicates an SMB share was not found.
	ErrSMBShareNotFound = errors.New("SMB share not found")
)

// MockClient is a mock implementation of the NASty API client for sanity testing.
type MockClient struct {
	subvolumes       map[string]*nastyapi.Subvolume       // key: "filesystem/name"
	snapshots        map[string]*nastyapi.Snapshot        // key: "filesystem/subvolume@name"
	nfsShares        map[string]*nastyapi.NFSShare        // key: UUID
	smbShares        map[string]*nastyapi.SMBShare        // key: UUID
	iscsiTargets     map[string]*nastyapi.ISCSITarget     // key: UUID
	nvmeofSubsystems map[string]*nastyapi.NVMeOFSubsystem // key: UUID
	mu               sync.Mutex
	nextID           uint64
}

// NewMockClient creates a new mock client for testing.
func NewMockClient() *MockClient {
	return &MockClient{
		subvolumes:       make(map[string]*nastyapi.Subvolume),
		snapshots:        make(map[string]*nastyapi.Snapshot),
		nfsShares:        make(map[string]*nastyapi.NFSShare),
		smbShares:        make(map[string]*nastyapi.SMBShare),
		iscsiTargets:     make(map[string]*nastyapi.ISCSITarget),
		nvmeofSubsystems: make(map[string]*nastyapi.NVMeOFSubsystem),
	}
}

func (m *MockClient) genID() string {
	return fmt.Sprintf("mock-%d", atomic.AddUint64(&m.nextID, 1))
}

// Close is a no-op for the mock client.
func (m *MockClient) Close() {}

// QueryFilesystem returns a fake filesystem for testing.
func (m *MockClient) QueryFilesystem(_ context.Context, fsName string) (*nastyapi.Filesystem, error) {
	total := uint64(10 * 1024 * 1024 * 1024)
	used := uint64(1 * 1024 * 1024 * 1024)
	return &nastyapi.Filesystem{
		Name:           fsName,
		Mounted:        true,
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: total - used,
	}, nil
}

// CreateSubvolume creates a subvolume in the mock.
func (m *MockClient) CreateSubvolume(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := params.Filesystem + "/" + params.Name
	if _, exists := m.subvolumes[key]; exists {
		return nil, ErrDatasetExists
	}

	sv := &nastyapi.Subvolume{
		Name:          params.Name,
		Filesystem:          params.Filesystem,
		SubvolumeType: params.SubvolumeType,
		Path:          "/" + params.Filesystem + "/" + params.Name,
		Properties:    make(map[string]string),
		Snapshots:     []string{},
	}
	if params.VolsizeBytes != nil {
		sv.VolsizeBytes = params.VolsizeBytes
		if params.SubvolumeType == "block" {
			dev := "/dev/zvol/" + params.Filesystem + "/" + params.Name
			sv.BlockDevice = &dev
		}
	}

	m.subvolumes[key] = sv
	return sv, nil
}

// DeleteSubvolume removes a subvolume from the mock.
func (m *MockClient) DeleteSubvolume(_ context.Context, filesystem, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := filesystem + "/" + name
	if _, exists := m.subvolumes[key]; !exists {
		return ErrDatasetNotFound
	}
	delete(m.subvolumes, key)
	return nil
}

// GetSubvolume retrieves a subvolume by filesystem and name.
func (m *MockClient) GetSubvolume(_ context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := filesystem + "/" + name
	sv, exists := m.subvolumes[key]
	if !exists {
		return nil, ErrDatasetNotFound
	}
	cp := *sv
	return &cp, nil
}

// ListAllSubvolumes lists all subvolumes in a filesystem.
func (m *MockClient) ListAllSubvolumes(_ context.Context, filesystem string) ([]nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []nastyapi.Subvolume
	for key, sv := range m.subvolumes {
		if filesystem == "" || sv.Filesystem == filesystem {
			_ = key
			result = append(result, *sv)
		}
	}
	return result, nil
}

// SetSubvolumeProperties sets xattr properties on a subvolume.
func (m *MockClient) SetSubvolumeProperties(_ context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := filesystem + "/" + name
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
func (m *MockClient) RemoveSubvolumeProperties(_ context.Context, filesystem, name string, keys []string) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := filesystem + "/" + name
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
func (m *MockClient) FindSubvolumesByProperty(_ context.Context, propKey, propValue, filesystem string) ([]nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []nastyapi.Subvolume
	for _, sv := range m.subvolumes {
		if filesystem != "" && sv.Filesystem != filesystem {
			continue
		}
		if sv.Properties[propKey] == propValue {
			result = append(result, *sv)
		}
	}
	return result, nil
}

// FindManagedSubvolumes finds all subvolumes managed by nasty-csi.
func (m *MockClient) FindManagedSubvolumes(ctx context.Context, filesystem string) ([]nastyapi.Subvolume, error) {
	return m.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, filesystem)
}

// FindSubvolumeByCSIVolumeName finds a subvolume by its CSI volume name xattr.
func (m *MockClient) FindSubvolumeByCSIVolumeName(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
	subvols, err := m.FindSubvolumesByProperty(ctx, nastyapi.PropertyCSIVolumeName, volumeName, filesystem)
	if err != nil {
		return nil, err
	}
	if len(subvols) == 0 {
		return nil, nastyapi.ErrDatasetNotFound
	}
	return &subvols[0], nil
}

// CreateSnapshot creates a snapshot.
func (m *MockClient) CreateSnapshot(_ context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	svKey := params.Filesystem + "/" + params.Subvolume
	if _, exists := m.subvolumes[svKey]; !exists {
		return nil, ErrDatasetNotFound
	}

	snapKey := svKey + "@" + params.Name
	if _, exists := m.snapshots[snapKey]; exists {
		return nil, ErrSnapshotAlreadyExists
	}

	snap := &nastyapi.Snapshot{
		Name:      params.Name,
		Subvolume: params.Subvolume,
		Filesystem:      params.Filesystem,
		Path:      "/" + params.Filesystem + "/" + params.Subvolume + "@" + params.Name,
		ReadOnly:  params.ReadOnly,
	}
	m.snapshots[snapKey] = snap

	// Update the parent subvolume's snapshot list (used by CSI for idempotency checks)
	if sv, exists := m.subvolumes[svKey]; exists {
		sv.Snapshots = append(sv.Snapshots, params.Name)
	}

	return snap, nil
}

// DeleteSnapshot deletes a snapshot.
func (m *MockClient) DeleteSnapshot(_ context.Context, filesystem, subvolume, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapKey := filesystem + "/" + subvolume + "@" + name
	if _, exists := m.snapshots[snapKey]; !exists {
		return ErrSnapshotNotFound
	}
	delete(m.snapshots, snapKey)

	// Update the parent subvolume's snapshot list
	svKey := filesystem + "/" + subvolume
	if sv, exists := m.subvolumes[svKey]; exists {
		for i, s := range sv.Snapshots {
			if s == name {
				sv.Snapshots = append(sv.Snapshots[:i], sv.Snapshots[i+1:]...)
				break
			}
		}
	}

	return nil
}

// ListSnapshots lists all snapshots in a filesystem.
func (m *MockClient) ListSnapshots(_ context.Context, filesystem string) ([]nastyapi.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []nastyapi.Snapshot
	for _, snap := range m.snapshots {
		if filesystem == "" || snap.Filesystem == filesystem {
			result = append(result, *snap)
		}
	}
	return result, nil
}

// CreateNFSShare creates an NFS share.
func (m *MockClient) CreateNFSShare(_ context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	enabled := true
	if params.Enabled != nil {
		enabled = *params.Enabled
	}
	comment := params.Comment
	share := &nastyapi.NFSShare{
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
func (m *MockClient) ListNFSShares(_ context.Context) ([]nastyapi.NFSShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]nastyapi.NFSShare, 0, len(m.nfsShares))
	for _, share := range m.nfsShares {
		result = append(result, *share)
	}
	return result, nil
}

// GetNFSShare retrieves an NFS share by UUID.
func (m *MockClient) GetNFSShare(_ context.Context, id string) (*nastyapi.NFSShare, error) {
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
func (m *MockClient) CreateSMBShare(_ context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	comment := params.Comment
	share := &nastyapi.SMBShare{
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
func (m *MockClient) ListSMBShares(_ context.Context) ([]nastyapi.SMBShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]nastyapi.SMBShare, 0, len(m.smbShares))
	for _, share := range m.smbShares {
		result = append(result, *share)
	}
	return result, nil
}

// GetSMBShare retrieves an SMB share by UUID.
func (m *MockClient) GetSMBShare(_ context.Context, id string) (*nastyapi.SMBShare, error) {
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
func (m *MockClient) CreateISCSITarget(_ context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	iqn := "iqn.2024-01.io.nasty:" + params.Name
	target := &nastyapi.ISCSITarget{
		ID:      id,
		IQN:     iqn,
		Portals: []nastyapi.ISCSIPortal{{IP: "0.0.0.0", Port: 3260}},
		Luns:    []nastyapi.ISCSILun{},
		Enabled: true,
	}
	m.iscsiTargets[id] = target
	return target, nil
}

// AddISCSILun adds a LUN to an iSCSI target.
func (m *MockClient) AddISCSILun(_ context.Context, targetID, backstorePath string) (*nastyapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	target, exists := m.iscsiTargets[targetID]
	if !exists {
		return nil, ErrISCSITargetNotFound
	}
	lun := nastyapi.ISCSILun{
		LunID:         uint32(len(target.Luns)), //nolint:gosec // G115: LUN count always small
		BackstorePath: backstorePath,
	}
	target.Luns = append(target.Luns, lun)
	cp := *target
	return &cp, nil
}

// AddISCSIACL adds an initiator ACL to an iSCSI target.
func (m *MockClient) AddISCSIACL(_ context.Context, targetID, _ string) (*nastyapi.ISCSITarget, error) {
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
func (m *MockClient) ListISCSITargets(_ context.Context) ([]nastyapi.ISCSITarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]nastyapi.ISCSITarget, 0, len(m.iscsiTargets))
	for _, t := range m.iscsiTargets {
		result = append(result, *t)
	}
	return result, nil
}

// GetISCSITargetByIQN finds an iSCSI target by IQN.
func (m *MockClient) GetISCSITargetByIQN(_ context.Context, iqn string) (*nastyapi.ISCSITarget, error) {
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
func (m *MockClient) CreateNVMeOFSubsystem(_ context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.genID()
	subsystem := &nastyapi.NVMeOFSubsystem{
		ID:           id,
		NQN:          "nqn.2024-01.io.nasty:" + params.Name,
		Namespaces:   []nastyapi.NVMeOFNamespace{{NSID: 1, DevicePath: params.DevicePath, Enabled: true}},
		Ports:        []nastyapi.NVMeOFPort{{PortID: 1, Transport: "tcp", Addr: params.Addr}},
		AllowedHosts: params.AllowedHosts,
		AllowAnyHost: len(params.AllowedHosts) == 0,
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
func (m *MockClient) ListNVMeOFSubsystems(_ context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]nastyapi.NVMeOFSubsystem, 0, len(m.nvmeofSubsystems))
	for _, s := range m.nvmeofSubsystems {
		result = append(result, *s)
	}
	return result, nil
}

// GetNVMeOFSubsystemByNQN finds an NVMe-oF subsystem by NQN.
func (m *MockClient) GetNVMeOFSubsystemByNQN(_ context.Context, nqn string) (*nastyapi.NVMeOFSubsystem, error) {
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

// ResizeSubvolume resizes a subvolume.
func (m *MockClient) ResizeSubvolume(_ context.Context, filesystem, name string, volsizeBytes uint64) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := filesystem + "/" + name
	sv, exists := m.subvolumes[key]
	if !exists {
		return nil, ErrDatasetNotFound
	}
	sv.VolsizeBytes = &volsizeBytes
	cp := *sv
	return &cp, nil
}

// CloneSnapshot clones a snapshot into a new writable subvolume.
func (m *MockClient) CloneSnapshot(_ context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify the source snapshot exists
	snapKey := params.Filesystem + "/" + params.Subvolume + "@" + params.Snapshot
	if _, exists := m.snapshots[snapKey]; !exists {
		return nil, ErrSnapshotNotFound
	}

	// Create the new subvolume as a clone
	newKey := params.Filesystem + "/" + params.NewName
	if _, exists := m.subvolumes[newKey]; exists {
		return nil, ErrDatasetExists
	}

	// Copy properties from parent subvolume if it exists
	parentKey := params.Filesystem + "/" + params.Subvolume
	var props map[string]string
	if parent, exists := m.subvolumes[parentKey]; exists && parent.Properties != nil {
		props = make(map[string]string)
		for k, v := range parent.Properties {
			props[k] = v
		}
	} else {
		props = make(map[string]string)
	}

	sv := &nastyapi.Subvolume{
		Name:       params.NewName,
		Filesystem:       params.Filesystem,
		Path:       "/" + params.Filesystem + "/" + params.NewName,
		Properties: props,
		Snapshots:  []string{},
	}
	m.subvolumes[newKey] = sv
	cp := *sv
	return &cp, nil
}

// CloneSubvolume creates a COW clone of a subvolume.
func (m *MockClient) CloneSubvolume(_ context.Context, filesystem, name, newName string) (*nastyapi.Subvolume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sourceKey := filesystem + "/" + name
	source, exists := m.subvolumes[sourceKey]
	if !exists {
		return nil, ErrDatasetNotFound
	}

	newKey := filesystem + "/" + newName
	if _, exists := m.subvolumes[newKey]; exists {
		return nil, ErrDatasetExists
	}

	// COW clone — copy properties from source
	props := make(map[string]string)
	if source.Properties != nil {
		for k, v := range source.Properties {
			props[k] = v
		}
	}

	sv := &nastyapi.Subvolume{
		Name:          newName,
		Filesystem:          filesystem,
		SubvolumeType: source.SubvolumeType,
		Path:          "/" + filesystem + "/" + newName,
		VolsizeBytes:  source.VolsizeBytes,
		Compression:   source.Compression,
		Properties:    props,
		Snapshots:     []string{},
	}
	m.subvolumes[newKey] = sv
	cp := *sv
	return &cp, nil
}
