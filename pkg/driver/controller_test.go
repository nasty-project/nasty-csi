package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestControllerGetCapabilities(t *testing.T) {
	service := NewControllerService(nil, NewNodeRegistry(), "")

	resp, err := service.ControllerGetCapabilities(context.Background(), nil)
	if err != nil {
		t.Fatalf("ControllerGetCapabilities() error = %v", err)
	}

	requireNotNilController(t, resp, "ControllerGetCapabilities() returned nil response")

	if len(resp.Capabilities) == 0 {
		t.Error("ControllerGetCapabilities() returned no capabilities")
	}

	expectedCaps := map[string]bool{
		"CREATE_DELETE_VOLUME":     false,
		"PUBLISH_UNPUBLISH_VOLUME": false,
		"LIST_VOLUMES":             false,
		"GET_CAPACITY":             false,
	}

	for _, cap := range resp.Capabilities {
		if rpc := cap.GetRpc(); rpc != nil {
			switch rpc.Type.String() {
			case "CREATE_DELETE_VOLUME":
				expectedCaps["CREATE_DELETE_VOLUME"] = true
			case "PUBLISH_UNPUBLISH_VOLUME":
				expectedCaps["PUBLISH_UNPUBLISH_VOLUME"] = true
			case "LIST_VOLUMES":
				expectedCaps["LIST_VOLUMES"] = true
			case "GET_CAPACITY":
				expectedCaps["GET_CAPACITY"] = true
			}
		}
	}

	for cap, found := range expectedCaps {
		if !found {
			t.Errorf("Expected capability %s not found", cap)
		}
	}
}

// requireNotNilController fails the test immediately if v is nil.
func requireNotNilController(t *testing.T, v any, msg string) {
	t.Helper()
	if v == nil {
		t.Fatal(msg)
	}
}

func uint64Ptr(value uint64) *uint64 {
	return &value
}

// mockAPIClient is the primary mock implementation of nastyapi.ClientInterface for testing.
// All methods have optional Func fields; if nil, a sensible default is returned.
type mockAPIClient struct {
	QueryPoolFunc                    func(ctx context.Context, fsName string) (*nastyapi.Filesystem, error)
	CreateSubvolumeFunc              func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error)
	DeleteSubvolumeFunc              func(ctx context.Context, filesystem, name string) error
	GetSubvolumeFunc                 func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error)
	ListAllSubvolumesFunc            func(ctx context.Context, filesystem string) ([]nastyapi.Subvolume, error)
	SetSubvolumePropertiesFunc       func(ctx context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error)
	RemoveSubvolumePropertiesFunc    func(ctx context.Context, filesystem, name string, keys []string) (*nastyapi.Subvolume, error)
	FindSubvolumesByPropertyFunc     func(ctx context.Context, key, value, filesystem string) ([]nastyapi.Subvolume, error)
	FindManagedSubvolumesFunc        func(ctx context.Context, filesystem string) ([]nastyapi.Subvolume, error)
	FindSubvolumeByCSIVolumeNameFunc func(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error)
	CreateSnapshotFunc               func(ctx context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error)
	DeleteSnapshotFunc               func(ctx context.Context, filesystem, subvolume, name string) error
	ListSnapshotsFunc                func(ctx context.Context, filesystem string) ([]nastyapi.Snapshot, error)
	CreateNFSShareFunc               func(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error)
	DeleteNFSShareFunc               func(ctx context.Context, id string) error
	ListNFSSharesFunc                func(ctx context.Context) ([]nastyapi.NFSShare, error)
	GetNFSShareFunc                  func(ctx context.Context, id string) (*nastyapi.NFSShare, error)
	CreateSMBShareFunc               func(ctx context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error)
	DeleteSMBShareFunc               func(ctx context.Context, id string) error
	ListSMBSharesFunc                func(ctx context.Context) ([]nastyapi.SMBShare, error)
	GetSMBShareFunc                  func(ctx context.Context, id string) (*nastyapi.SMBShare, error)
	CreateISCSITargetFunc            func(ctx context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error)
	AddISCSILunFunc                  func(ctx context.Context, targetID, backstorePath string) (*nastyapi.ISCSITarget, error)
	AddISCSIACLFunc                  func(ctx context.Context, targetID, initiatorIQN string) (*nastyapi.ISCSITarget, error)
	DeleteISCSITargetFunc            func(ctx context.Context, id string) error
	ListISCSITargetsFunc             func(ctx context.Context) ([]nastyapi.ISCSITarget, error)
	GetISCSITargetByIQNFunc          func(ctx context.Context, iqn string) (*nastyapi.ISCSITarget, error)
	CreateNVMeOFSubsystemFunc        func(ctx context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error)
	DeleteNVMeOFSubsystemFunc        func(ctx context.Context, id string) error
	ListNVMeOFSubsystemsFunc         func(ctx context.Context) ([]nastyapi.NVMeOFSubsystem, error)
	GetNVMeOFSubsystemByNQNFunc      func(ctx context.Context, nqn string) (*nastyapi.NVMeOFSubsystem, error)
	ResizeSubvolumeFunc              func(ctx context.Context, filesystem, name string, volsizeBytes uint64) (*nastyapi.Subvolume, error)
	CloneSnapshotFunc                func(ctx context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error)
	CloneSubvolumeFunc               func(ctx context.Context, filesystem, name, newName string) (*nastyapi.Subvolume, error)
}

var errNotImplemented = errors.New("mock method not implemented")

// Verify mockAPIClient implements ClientInterface at compile time.
var _ nastyapi.ClientInterface = (*mockAPIClient)(nil)

// Pool methods

func (m *mockAPIClient) QueryFilesystem(ctx context.Context, fsName string) (*nastyapi.Filesystem, error) {
	if m.QueryPoolFunc != nil {
		return m.QueryPoolFunc(ctx, fsName)
	}
	return nil, errNotImplemented
}

// Subvolume methods

func (m *mockAPIClient) CreateSubvolume(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
	if m.CreateSubvolumeFunc != nil {
		return m.CreateSubvolumeFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteSubvolume(ctx context.Context, filesystem, name string) error {
	if m.DeleteSubvolumeFunc != nil {
		return m.DeleteSubvolumeFunc(ctx, filesystem, name)
	}
	return nil
}

func (m *mockAPIClient) GetSubvolume(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
	if m.GetSubvolumeFunc != nil {
		return m.GetSubvolumeFunc(ctx, filesystem, name)
	}
	return nil, errors.New("subvolume not found")
}

func (m *mockAPIClient) ListAllSubvolumes(ctx context.Context, filesystem string) ([]nastyapi.Subvolume, error) {
	if m.ListAllSubvolumesFunc != nil {
		return m.ListAllSubvolumesFunc(ctx, filesystem)
	}
	return []nastyapi.Subvolume{}, nil
}

// Property methods

func (m *mockAPIClient) SetSubvolumeProperties(ctx context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error) {
	if m.SetSubvolumePropertiesFunc != nil {
		return m.SetSubvolumePropertiesFunc(ctx, filesystem, name, props)
	}
	return &nastyapi.Subvolume{Name: name, Filesystem: filesystem, Properties: props}, nil
}

func (m *mockAPIClient) RemoveSubvolumeProperties(ctx context.Context, filesystem, name string, keys []string) (*nastyapi.Subvolume, error) {
	if m.RemoveSubvolumePropertiesFunc != nil {
		return m.RemoveSubvolumePropertiesFunc(ctx, filesystem, name, keys)
	}
	return &nastyapi.Subvolume{Name: name, Filesystem: filesystem}, nil
}

func (m *mockAPIClient) FindSubvolumesByProperty(ctx context.Context, key, value, filesystem string) ([]nastyapi.Subvolume, error) {
	if m.FindSubvolumesByPropertyFunc != nil {
		return m.FindSubvolumesByPropertyFunc(ctx, key, value, filesystem)
	}
	return []nastyapi.Subvolume{}, nil
}

func (m *mockAPIClient) FindManagedSubvolumes(ctx context.Context, filesystem string) ([]nastyapi.Subvolume, error) {
	if m.FindManagedSubvolumesFunc != nil {
		return m.FindManagedSubvolumesFunc(ctx, filesystem)
	}
	return []nastyapi.Subvolume{}, nil
}

func (m *mockAPIClient) FindSubvolumeByCSIVolumeName(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
	if m.FindSubvolumeByCSIVolumeNameFunc != nil {
		return m.FindSubvolumeByCSIVolumeNameFunc(ctx, filesystem, volumeName)
	}
	return nil, nil //nolint:nilnil // nil means not found
}

// Snapshot methods

func (m *mockAPIClient) CreateSnapshot(ctx context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error) {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteSnapshot(ctx context.Context, filesystem, subvolume, name string) error {
	if m.DeleteSnapshotFunc != nil {
		return m.DeleteSnapshotFunc(ctx, filesystem, subvolume, name)
	}
	return nil
}

func (m *mockAPIClient) ListSnapshots(ctx context.Context, filesystem string) ([]nastyapi.Snapshot, error) {
	if m.ListSnapshotsFunc != nil {
		return m.ListSnapshotsFunc(ctx, filesystem)
	}
	return []nastyapi.Snapshot{}, nil
}

// NFS methods

func (m *mockAPIClient) CreateNFSShare(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
	if m.CreateNFSShareFunc != nil {
		return m.CreateNFSShareFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteNFSShare(ctx context.Context, id string) error {
	if m.DeleteNFSShareFunc != nil {
		return m.DeleteNFSShareFunc(ctx, id)
	}
	return nil
}

func (m *mockAPIClient) ListNFSShares(ctx context.Context) ([]nastyapi.NFSShare, error) {
	if m.ListNFSSharesFunc != nil {
		return m.ListNFSSharesFunc(ctx)
	}
	return []nastyapi.NFSShare{}, nil
}

func (m *mockAPIClient) GetNFSShare(ctx context.Context, id string) (*nastyapi.NFSShare, error) {
	if m.GetNFSShareFunc != nil {
		return m.GetNFSShareFunc(ctx, id)
	}
	return nil, errors.New("NFS share not found")
}

// SMB methods

func (m *mockAPIClient) CreateSMBShare(ctx context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
	if m.CreateSMBShareFunc != nil {
		return m.CreateSMBShareFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteSMBShare(ctx context.Context, id string) error {
	if m.DeleteSMBShareFunc != nil {
		return m.DeleteSMBShareFunc(ctx, id)
	}
	return nil
}

func (m *mockAPIClient) ListSMBShares(ctx context.Context) ([]nastyapi.SMBShare, error) {
	if m.ListSMBSharesFunc != nil {
		return m.ListSMBSharesFunc(ctx)
	}
	return []nastyapi.SMBShare{}, nil
}

func (m *mockAPIClient) GetSMBShare(ctx context.Context, id string) (*nastyapi.SMBShare, error) {
	if m.GetSMBShareFunc != nil {
		return m.GetSMBShareFunc(ctx, id)
	}
	return nil, errors.New("SMB share not found")
}

// iSCSI methods

func (m *mockAPIClient) CreateISCSITarget(ctx context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
	if m.CreateISCSITargetFunc != nil {
		return m.CreateISCSITargetFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) AddISCSILun(ctx context.Context, targetID, backstorePath string) (*nastyapi.ISCSITarget, error) {
	if m.AddISCSILunFunc != nil {
		return m.AddISCSILunFunc(ctx, targetID, backstorePath)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) AddISCSIACL(ctx context.Context, targetID, initiatorIQN string) (*nastyapi.ISCSITarget, error) {
	if m.AddISCSIACLFunc != nil {
		return m.AddISCSIACLFunc(ctx, targetID, initiatorIQN)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteISCSITarget(ctx context.Context, id string) error {
	if m.DeleteISCSITargetFunc != nil {
		return m.DeleteISCSITargetFunc(ctx, id)
	}
	return nil
}

func (m *mockAPIClient) ListISCSITargets(ctx context.Context) ([]nastyapi.ISCSITarget, error) {
	if m.ListISCSITargetsFunc != nil {
		return m.ListISCSITargetsFunc(ctx)
	}
	return []nastyapi.ISCSITarget{}, nil
}

func (m *mockAPIClient) GetISCSITargetByIQN(ctx context.Context, iqn string) (*nastyapi.ISCSITarget, error) {
	if m.GetISCSITargetByIQNFunc != nil {
		return m.GetISCSITargetByIQNFunc(ctx, iqn)
	}
	return nil, errors.New("iSCSI target not found")
}

// NVMe-oF methods

func (m *mockAPIClient) CreateNVMeOFSubsystem(ctx context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error) {
	if m.CreateNVMeOFSubsystemFunc != nil {
		return m.CreateNVMeOFSubsystemFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) DeleteNVMeOFSubsystem(ctx context.Context, id string) error {
	if m.DeleteNVMeOFSubsystemFunc != nil {
		return m.DeleteNVMeOFSubsystemFunc(ctx, id)
	}
	return nil
}

func (m *mockAPIClient) ListNVMeOFSubsystems(ctx context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
	if m.ListNVMeOFSubsystemsFunc != nil {
		return m.ListNVMeOFSubsystemsFunc(ctx)
	}
	return []nastyapi.NVMeOFSubsystem{}, nil
}

func (m *mockAPIClient) GetNVMeOFSubsystemByNQN(ctx context.Context, nqn string) (*nastyapi.NVMeOFSubsystem, error) {
	if m.GetNVMeOFSubsystemByNQNFunc != nil {
		return m.GetNVMeOFSubsystemByNQNFunc(ctx, nqn)
	}
	return nil, errors.New("NVMe-oF subsystem not found")
}

// Resize and Clone

func (m *mockAPIClient) ResizeSubvolume(ctx context.Context, filesystem, name string, volsizeBytes uint64) (*nastyapi.Subvolume, error) {
	if m.ResizeSubvolumeFunc != nil {
		return m.ResizeSubvolumeFunc(ctx, filesystem, name, volsizeBytes)
	}
	return &nastyapi.Subvolume{Filesystem: filesystem, Name: name}, nil
}

func (m *mockAPIClient) CloneSnapshot(ctx context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error) {
	if m.CloneSnapshotFunc != nil {
		return m.CloneSnapshotFunc(ctx, params)
	}
	return nil, errNotImplemented
}

func (m *mockAPIClient) CloneSubvolume(ctx context.Context, filesystem, name, newName string) (*nastyapi.Subvolume, error) {
	if m.CloneSubvolumeFunc != nil {
		return m.CloneSubvolumeFunc(ctx, filesystem, name, newName)
	}
	return &nastyapi.Subvolume{Name: newName, Filesystem: filesystem}, nil
}

// Connection

func (m *mockAPIClient) Close() {}

func (m *mockAPIClient) IsConnected() bool { return true }

// --- Tests ---

func TestValidateCreateVolumeRequest(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		req      *csi.CreateVolumeRequest
		name     string
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "missing name",
			req: &csi.CreateVolumeRequest{
				Name: "",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil,
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call CreateVolume to trigger validation; supply a minimal client that returns not-found
			client := &mockAPIClient{
				GetSubvolumeFunc: func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				},
			}
			service := NewControllerService(client, NewNodeRegistry(), "")
			_, err := service.CreateVolume(ctx, tt.req)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestControllerPublishVolume(t *testing.T) {
	ctx := context.Background()
	volumeID := "first/test-volume"

	tests := []struct {
		req      *csi.ControllerPublishVolumeRequest
		nodeReg  *NodeRegistry
		name     string
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "successful publish",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
				VolumeContext: map[string]string{
					VolumeContextKeyProtocol: ProtocolNFS,
				},
			},
			nodeReg: func() *NodeRegistry {
				r := NewNodeRegistry()
				r.Register("test-node")
				return r
			}(),
			wantErr: false,
		},
		{
			name: "missing volume ID",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: "",
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg:  NewNodeRegistry(),
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing node ID",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg:  NewNodeRegistry(),
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing volume capability",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         volumeID,
				NodeId:           "test-node",
				VolumeCapability: nil,
			},
			nodeReg: func() *NodeRegistry {
				r := NewNodeRegistry()
				r.Register("test-node")
				return r
			}(),
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "unknown node rejected when registry has nodes (single-process mode)",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "unknown-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg: func() *NodeRegistry {
				r := NewNodeRegistry()
				r.Register("other-node")
				return r
			}(),
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name: "unknown node accepted when registry is empty (production separate pods)",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: volumeID,
				NodeId:   "unknown-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			nodeReg: NewNodeRegistry(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			service := NewControllerService(mockClient, tt.nodeReg, "")

			_, err := service.ControllerPublishVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req      *csi.ControllerUnpublishVolumeRequest
		name     string
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "missing volume ID",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "",
				NodeId:   "test-node",
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "successful unpublish",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "test-volume",
				NodeId:   "test-node",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewControllerService(&mockAPIClient{}, NewNodeRegistry(), "")
			_, err := service.ControllerUnpublishVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestRequestedBlockFilesystem(t *testing.T) {
	mountCapability := func(fsType string) *csi.VolumeCapability {
		return &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fsType}},
		}
	}
	blockCapability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
	}

	//nolint:govet // Keeping the table fields together is clearer than optimizing test-only padding.
	tests := []struct {
		caps    []*csi.VolumeCapability
		name    string
		want    string
		wantErr bool
	}{
		{name: "mount defaults to ext4", caps: []*csi.VolumeCapability{mountCapability("")}, want: fsTypeExt4},
		{name: "xfs is preserved", caps: []*csi.VolumeCapability{mountCapability(fsTypeXFS)}, want: fsTypeXFS},
		{name: "raw block has no filesystem", caps: []*csi.VolumeCapability{blockCapability}},
		{name: "mixed presentation is rejected", caps: []*csi.VolumeCapability{mountCapability(fsTypeExt4), blockCapability}, wantErr: true},
		{name: "conflicting filesystems are rejected", caps: []*csi.VolumeCapability{mountCapability(fsTypeExt4), mountCapability(fsTypeXFS)}, wantErr: true},
		{name: "unsupported filesystem is rejected", caps: []*csi.VolumeCapability{mountCapability("btrfs")}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := requestedBlockFilesystem(tt.caps)
			if (err != nil) != tt.wantErr {
				t.Fatalf("requestedBlockFilesystem() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("requestedBlockFilesystem() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateInitializedBlockFilesystem(t *testing.T) {
	ext4 := fsTypeExt4
	uuid := "abc-123"
	base := nastyapi.Subvolume{Name: "pvc", Filesystem: "tank"}

	if err := validateInitializedBlockFilesystem(&base, ""); err != nil {
		t.Fatalf("raw block validation failed: %v", err)
	}
	if err := validateInitializedBlockFilesystem(&base, fsTypeExt4); err == nil {
		t.Fatal("missing initialization metadata was accepted")
	}
	base.BlockFilesystem = &ext4
	if err := validateInitializedBlockFilesystem(&base, fsTypeExt4); err == nil {
		t.Fatal("missing filesystem UUID was accepted")
	}
	base.BlockFilesystemUUID = &uuid
	if err := validateInitializedBlockFilesystem(&base, fsTypeExt4); err != nil {
		t.Fatalf("valid initialization metadata rejected: %v", err)
	}
	if err := validateInitializedBlockFilesystem(&base, fsTypeXFS); err == nil {
		t.Fatal("conflicting filesystem metadata was accepted")
	}
	legacy := nastyapi.Subvolume{Name: "legacy", Filesystem: "tank"}
	if err := validateKnownBlockFilesystem(&legacy, fsTypeExt4); err != nil {
		t.Fatalf("legacy subvolume without metadata rejected: %v", err)
	}
	if err := validateKnownBlockFilesystem(&base, fsTypeXFS); err == nil {
		t.Fatal("known conflicting filesystem metadata was accepted")
	}
}

func TestGetOrCreateSubvolumeUsesBackendCreationAuthority(t *testing.T) {
	ext4 := fsTypeExt4
	uuid := "abc-123"
	device := "/dev/loop7"
	backendCreated := true
	var captured nastyapi.SubvolumeCreateParams
	client := &mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			return nil, errors.New("subvolume not found")
		},
		CreateSubvolumeFunc: func(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
			captured = params
			return &nastyapi.Subvolume{
				Name:                params.Name,
				Filesystem:          params.Filesystem,
				BlockDevice:         &device,
				BlockFilesystem:     &ext4,
				BlockFilesystemUUID: &uuid,
				Created:             backendCreated,
			}, nil
		},
	}
	service := &ControllerService{apiClient: client}
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "test")

	_, created, err := service.getOrCreateSubvolume(
		context.Background(), "tank", "pvc", "block", "", "", "", "", "", "", fsTypeExt4,
		0, 1024*1024*1024, timer,
	)
	if err != nil {
		t.Fatalf("getOrCreateSubvolume() error = %v", err)
	}
	if captured.BlockFilesystem != fsTypeExt4 {
		t.Fatalf("BlockFilesystem = %q, want %q", captured.BlockFilesystem, fsTypeExt4)
	}
	if !created {
		t.Fatal("backend-created subvolume was not reported as new")
	}

	backendCreated = false
	_, created, err = service.getOrCreateSubvolume(
		context.Background(), "tank", "pvc", "block", "", "", "", "", "", "", fsTypeExt4,
		0, 1024*1024*1024, timer,
	)
	if err != nil {
		t.Fatalf("raced getOrCreateSubvolume() error = %v", err)
	}
	if created {
		t.Fatal("backend-existing subvolume was incorrectly reported as new")
	}
}

func TestValidateVolumeCapabilities(t *testing.T) {
	ctx := context.Background()
	volumeID := "tank/csi/test-volume"

	tests := []struct {
		req       *csi.ValidateVolumeCapabilitiesRequest
		mockSetup func(m *mockAPIClient)
		name      string
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name: "valid capabilities - volume exists",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				VolumeContext: map[string]string{
					VolumeContextKeyProtocol: ProtocolNFS,
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name:       name,
						Filesystem: filesystem,
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolNFS,
						},
					}, nil
				}
			},
			wantErr: false,
		},
		{
			name: "volume not found",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: "tank/csi/nonexistent",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.FindSubvolumeByCSIVolumeNameFunc = func(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
					return nil, nil //nolint:nilnil // not found
				}
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name: "missing volume ID",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: "",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: nil,
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "empty capabilities",
			req: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)
			service := NewControllerService(mockClient, NewNodeRegistry(), "")

			resp, err := service.ValidateVolumeCapabilities(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if resp.Confirmed == nil {
				t.Error("Expected Confirmed to be non-nil")
			}
		})
	}
}

func TestControllerExpandVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req           *csi.ControllerExpandVolumeRequest
		mockSetup     func(*mockAPIClient)
		checkResponse func(*testing.T, *csi.ControllerExpandVolumeResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "missing volume ID",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing capacity range",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "tank/csi/test-nfs-volume",
				CapacityRange: nil,
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "empty capacity range",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "tank/csi/test-nfs-volume",
				CapacityRange: &csi.CapacityRange{},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "capacity below minimum rounds up when mutation is required",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "tank/csi/test-nfs-volume",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 500 * 1024 * 1024}, // 500 MiB
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(_ context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name: name, Filesystem: filesystem,
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolNFS,
						},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.CapacityBytes != MinVolumeSize {
					t.Errorf("Expected capacity %d, got %d", MinVolumeSize, resp.CapacityBytes)
				}
			},
		},
		{
			name: "NFS expansion - NodeExpansionRequired should be false",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "tank/csi/test-nfs-volume",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name: name, Filesystem: filesystem,
						SubvolumeType: subvolumeTypeFilesystem,
						QuotaBytes:    uint64Ptr(1024 * 1024 * 1024),
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolNFS,
						},
					}, nil
				}
				m.ResizeSubvolumeFunc = func(_ context.Context, _, _ string, volsizeBytes uint64) (*nastyapi.Subvolume, error) {
					if volsizeBytes != 5*1024*1024*1024 {
						return nil, errors.New("unexpected resize target")
					}
					return &nastyapi.Subvolume{}, nil
				}
				m.FindSubvolumeByCSIVolumeNameFunc = func(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
					return nil, nil //nolint:nilnil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be false for NFS volumes")
				}
				if resp.CapacityBytes != 5*1024*1024*1024 {
					t.Errorf("Expected capacity 5GB, got %d", resp.CapacityBytes)
				}
			},
		},
		{
			name: "existing capacity satisfies a smaller request without shrinking",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "tank/csi/existing-nfs-volume",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 500 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(_ context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name: name, Filesystem: filesystem,
						SubvolumeType: subvolumeTypeFilesystem,
						QuotaBytes:    uint64Ptr(10 * 1024 * 1024 * 1024),
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolNFS,
						},
					}, nil
				}
				m.ResizeSubvolumeFunc = func(_ context.Context, _, _ string, capacity uint64) (*nastyapi.Subvolume, error) {
					if capacity != 10*1024*1024*1024 {
						return nil, errors.New("reconciliation must use current backend capacity")
					}
					return &nastyapi.Subvolume{}, nil
				}
				m.SetSubvolumePropertiesFunc = func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
					if props[nastyapi.PropertyCapacityBytes] != "10737418240" {
						return nil, errors.New("current backend capacity was not reconciled")
					}
					return &nastyapi.Subvolume{}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.CapacityBytes != 10*1024*1024*1024 {
					t.Errorf("Expected existing capacity 10GB, got %d", resp.CapacityBytes)
				}
				if resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be false for NFS")
				}
			},
		},
		{
			name: "current capacity above requested limit is rejected",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: "tank/csi/existing-block-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 5 * 1024 * 1024 * 1024,
					LimitBytes:    8 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(_ context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name: name, Filesystem: filesystem,
						SubvolumeType: subvolumeTypeBlock,
						VolsizeBytes:  uint64Ptr(10 * 1024 * 1024 * 1024),
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolISCSI,
						},
					}, nil
				}
			},
			wantErr:  true,
			wantCode: codes.OutOfRange,
		},
		{
			name: "quota granularity must fit the requested limit",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: "tank/csi/unaligned-nfs-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024*1024*1024 + 1,
					LimitBytes:    1024*1024*1024 + 1,
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(_ context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name: name, Filesystem: filesystem,
						SubvolumeType: subvolumeTypeFilesystem,
						QuotaBytes:    uint64Ptr(1024 * 1024 * 1024),
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolNFS,
						},
					}, nil
				}
			},
			wantErr:  true,
			wantCode: codes.OutOfRange,
		},
		{
			name: "volume not found",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId:      "tank/csi/nonexistent-volume",
				CapacityRange: &csi.CapacityRange{RequiredBytes: 5 * 1024 * 1024 * 1024},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.FindSubvolumeByCSIVolumeNameFunc = func(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
					return nil, nil //nolint:nilnil
				}
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)
			service := NewControllerService(mockClient, NewNodeRegistry(), "")

			resp, err := service.ControllerExpandVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestAuthoritativeSubvolumeCapacity(t *testing.T) {
	filesystem := &nastyapi.Subvolume{
		SubvolumeType: subvolumeTypeFilesystem,
		QuotaBytes:    uint64Ptr(5 * 1024 * 1024 * 1024),
		VolsizeBytes:  uint64Ptr(99),
	}
	if got := authoritativeSubvolumeCapacity(filesystem); got == nil || *got != 5*1024*1024*1024 {
		t.Fatalf("filesystem capacity = %v, want quota", got)
	}

	block := &nastyapi.Subvolume{
		SubvolumeType: subvolumeTypeBlock,
		QuotaBytes:    uint64Ptr(99),
		VolsizeBytes:  uint64Ptr(10 * 1024 * 1024 * 1024),
	}
	if got := authoritativeSubvolumeCapacity(block); got == nil || *got != 10*1024*1024*1024 {
		t.Fatalf("block capacity = %v, want volsize", got)
	}
}

func TestNormalizeCreateCapacity(t *testing.T) {
	tests := []struct {
		capacity  *csi.CapacityRange
		name      string
		wantBytes int64
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name:      "missing range uses the one GiB default",
			wantBytes: MinVolumeSize,
		},
		{
			name: "required capacity below minimum rounds up",
			capacity: &csi.CapacityRange{
				RequiredBytes: 500 * 1024 * 1024,
			},
			wantBytes: MinVolumeSize,
		},
		{
			name: "minimum capacity must fit the limit",
			capacity: &csi.CapacityRange{
				RequiredBytes: 500 * 1024 * 1024,
				LimitBytes:    500 * 1024 * 1024,
			},
			wantErr:  true,
			wantCode: codes.OutOfRange,
		},
		{
			name: "filesystem capacity rounds to quota granularity",
			capacity: &csi.CapacityRange{
				RequiredBytes: MinVolumeSize + 1,
				LimitBytes:    MinVolumeSize + 1024,
			},
			wantBytes: MinVolumeSize + 1024,
		},
		{
			name: "rounded capacity cannot exceed an exact limit",
			capacity: &csi.CapacityRange{
				RequiredBytes: MinVolumeSize + 1,
				LimitBytes:    MinVolumeSize + 1,
			},
			wantErr:  true,
			wantCode: codes.OutOfRange,
		},
		{
			name: "backend maximum is enforced",
			capacity: &csi.CapacityRange{
				RequiredBytes: MaxVolumeSize + 1,
			},
			wantErr:  true,
			wantCode: codes.OutOfRange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.CreateVolumeRequest{CapacityRange: tt.capacity}
			err := normalizeCreateCapacity(req, ProtocolNFS)
			if tt.wantErr {
				if status.Code(err) != tt.wantCode {
					t.Fatalf("error code = %v, want %v (err=%v)", status.Code(err), tt.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeCreateCapacity() error = %v", err)
			}
			if got := req.GetCapacityRange().GetRequiredBytes(); got != tt.wantBytes {
				t.Fatalf("RequiredBytes = %d, want %d", got, tt.wantBytes)
			}
		})
	}
}

func TestPlanExpansionRejectsBackendMaximum(t *testing.T) {
	_, err := planExpansion(&VolumeMetadata{Protocol: ProtocolISCSI}, MaxVolumeSize+1, 0)
	if status.Code(err) != codes.OutOfRange {
		t.Fatalf("error code = %v, want %v (err=%v)", status.Code(err), codes.OutOfRange, err)
	}
}

func TestEnsureClonedVolumeCapacity(t *testing.T) {
	t.Run("grows a clone below required capacity", func(t *testing.T) {
		client := &mockAPIClient{
			GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					SubvolumeType: subvolumeTypeBlock,
					VolsizeBytes:  uint64Ptr(MinVolumeSize),
				}, nil
			},
			ResizeSubvolumeFunc: func(_ context.Context, _, _ string, capacity uint64) (*nastyapi.Subvolume, error) {
				if capacity != 2*MinVolumeSize {
					return nil, errors.New("unexpected clone resize target")
				}
				return &nastyapi.Subvolume{}, nil
			},
		}
		req := &csi.CreateVolumeRequest{CapacityRange: &csi.CapacityRange{
			RequiredBytes: 2 * MinVolumeSize,
			LimitBytes:    3 * MinVolumeSize,
		}}
		service := NewControllerService(client, NewNodeRegistry(), "")
		capacity, err := service.ensureClonedVolumeCapacity(context.Background(), req, "tank", "clone")
		if err != nil {
			t.Fatalf("ensureClonedVolumeCapacity() error = %v", err)
		}
		if capacity != 2*MinVolumeSize {
			t.Fatalf("capacity = %d, want %d", capacity, 2*MinVolumeSize)
		}
	})

	t.Run("rejects a clone above limit", func(t *testing.T) {
		client := &mockAPIClient{
			GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					SubvolumeType: subvolumeTypeBlock,
					VolsizeBytes:  uint64Ptr(3 * MinVolumeSize),
				}, nil
			},
		}
		req := &csi.CreateVolumeRequest{CapacityRange: &csi.CapacityRange{
			RequiredBytes: MinVolumeSize,
			LimitBytes:    2 * MinVolumeSize,
		}}
		service := NewControllerService(client, NewNodeRegistry(), "")
		_, err := service.ensureClonedVolumeCapacity(context.Background(), req, "tank", "clone")
		if status.Code(err) != codes.OutOfRange {
			t.Fatalf("error code = %v, want %v (err=%v)", status.Code(err), codes.OutOfRange, err)
		}
	})
}

func TestGetCapacity(t *testing.T) {
	tests := []struct {
		params              map[string]string
		mockQueryFilesystem func(ctx context.Context, fsName string) (*nastyapi.Filesystem, error)
		name                string
		wantCapacity        int64
		wantErrCode         codes.Code
		wantErr             bool
		wantEmptyResponse   bool
	}{
		{
			name: "successful capacity query",
			params: map[string]string{
				"filesystem": "first",
			},
			mockQueryFilesystem: func(ctx context.Context, fsName string) (*nastyapi.Filesystem, error) {
				return &nastyapi.Filesystem{
					Name:           "first",
					TotalBytes:     1000000000000,
					UsedBytes:      400000000000,
					AvailableBytes: 600000000000,
				}, nil
			},
			wantErr:      false,
			wantCapacity: 600000000000,
		},
		{
			name:              "no parameters - returns empty response",
			params:            nil,
			wantErr:           false,
			wantEmptyResponse: true,
		},
		{
			name:              "no filesystem parameter - returns empty response",
			params:            map[string]string{},
			wantErr:           false,
			wantEmptyResponse: true,
		},
		{
			name: "filesystem query fails",
			params: map[string]string{
				"filesystem": "nonexistent",
			},
			mockQueryFilesystem: func(ctx context.Context, fsName string) (*nastyapi.Filesystem, error) {
				return nil, errors.New("pool not found")
			},
			wantErr:     true,
			wantErrCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{
				QueryPoolFunc: tt.mockQueryFilesystem,
			}

			service := NewControllerService(mockClient, NewNodeRegistry(), "")
			req := &csi.GetCapacityRequest{
				Parameters: tt.params,
			}

			resp, err := service.GetCapacity(context.Background(), req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %v", err)
					return
				}
				if st.Code() != tt.wantErrCode {
					t.Errorf("Expected error code %v, got %v", tt.wantErrCode, st.Code())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if resp == nil {
				t.Fatal("GetCapacity returned nil response")
			}
			if tt.wantEmptyResponse {
				if resp.AvailableCapacity != 0 {
					t.Errorf("Expected empty response (capacity=0), got capacity=%d", resp.AvailableCapacity)
				}
				return
			}
			if resp.AvailableCapacity != tt.wantCapacity {
				t.Errorf("AvailableCapacity = %d, want %d", resp.AvailableCapacity, tt.wantCapacity)
			}
		})
	}
}

func TestNodeRegistryUnregisterAndCount(t *testing.T) {
	t.Run("basic operations", func(t *testing.T) {
		registry := NewNodeRegistry()

		if registry.Count() != 0 {
			t.Errorf("Expected count 0, got %d", registry.Count())
		}

		registry.Register("node1")
		if registry.Count() != 1 {
			t.Errorf("Expected count 1, got %d", registry.Count())
		}
		if !registry.IsRegistered("node1") {
			t.Error("node1 should be registered")
		}

		registry.Register("node2")
		registry.Register("node3")
		if registry.Count() != 3 {
			t.Errorf("Expected count 3, got %d", registry.Count())
		}

		registry.Unregister("node2")
		if registry.Count() != 2 {
			t.Errorf("Expected count 2 after unregister, got %d", registry.Count())
		}
		if registry.IsRegistered("node2") {
			t.Error("node2 should not be registered after unregister")
		}
		if !registry.IsRegistered("node1") {
			t.Error("node1 should still be registered")
		}
		if !registry.IsRegistered("node3") {
			t.Error("node3 should still be registered")
		}

		registry.Unregister("nonexistent")
		if registry.Count() != 2 {
			t.Errorf("Count should still be 2, got %d", registry.Count())
		}

		registry.Unregister("node1")
		registry.Unregister("node3")
		if registry.Count() != 0 {
			t.Errorf("Expected count 0, got %d", registry.Count())
		}
	})

	t.Run("re-registration", func(t *testing.T) {
		registry := NewNodeRegistry()
		registry.Register("node1")
		registry.Unregister("node1")
		registry.Register("node1")
		if registry.Count() != 1 {
			t.Errorf("Expected count 1, got %d", registry.Count())
		}
		if !registry.IsRegistered("node1") {
			t.Error("node1 should be registered after re-registration")
		}
	})
}

func TestCreateVolumeRPC(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req           *csi.CreateVolumeRequest
		mockSetup     func(*mockAPIClient)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "validation failure - missing name",
			req: &csi.CreateVolumeRequest{
				Name: "",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "validation failure - missing capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil,
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "unsupported protocol",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":   "unsupported-protocol",
					"filesystem": "first",
					"server":     "192.168.1.100",
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.FindSubvolumeByCSIVolumeNameFunc = func(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
					return nil, nil //nolint:nilnil
				}
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "successful NFS volume creation",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":   "nfs",
					"filesystem": "first",
					"server":     "192.168.1.100",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.FindSubvolumeByCSIVolumeNameFunc = func(ctx context.Context, filesystem, volumeName string) (*nastyapi.Subvolume, error) {
					return nil, nil //nolint:nilnil
				}
				m.CreateSubvolumeFunc = func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name:          params.Name,
						Filesystem:    params.Filesystem,
						SubvolumeType: "filesystem",
						Path:          "/mnt/" + params.Filesystem + "/" + params.Name,
						Properties:    map[string]string{},
					}, nil
				}
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Name: name, Filesystem: filesystem, Properties: props}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					return &nastyapi.NFSShare{
						ID:      "uuid-share-1",
						Path:    params.Path,
						Enabled: true,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateVolumeResponse) {
				t.Helper()
				if resp.Volume == nil {
					t.Error("Expected volume to be non-nil")
					return
				}
				if resp.Volume.VolumeId == "" {
					t.Error("Expected volume ID to be non-empty")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)

			service := NewControllerService(mockClient, NewNodeRegistry(), "")
			resp, err := service.CreateVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestDeleteVolumeRPC(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req       *csi.DeleteVolumeRequest
		mockSetup func(*mockAPIClient)
		name      string
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name: "missing volume ID",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "",
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "volume deletion with missing VolumeContext - idempotent success",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "unknown-volume",
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   false, // DeleteVolume is idempotent
		},
		{
			name: "successful NFS volume deletion",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "tank/csi/test-nfs-volume",
				Secrets: map[string]string{
					VolumeContextKeyProtocol:     ProtocolNFS,
					VolumeContextKeyNFSShareUUID: "uuid-nfs-share",
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name: name, Filesystem: filesystem,
						Properties: map[string]string{
							nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							nastyapi.PropertyProtocol:  ProtocolNFS,
						},
					}, nil
				}
				m.ListSnapshotsFunc = func(ctx context.Context, filesystem string) ([]nastyapi.Snapshot, error) {
					return []nastyapi.Snapshot{}, nil
				}
				m.DeleteNFSShareFunc = func(ctx context.Context, id string) error { return nil }
				m.DeleteSubvolumeFunc = func(ctx context.Context, filesystem, name string) error { return nil }
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)

			service := NewControllerService(mockClient, NewNodeRegistry(), "")
			_, err := service.DeleteVolume(ctx, tt.req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				if st, ok := status.FromError(err); ok {
					if st.Code() != tt.wantCode {
						t.Errorf("Expected error code %v, got %v", tt.wantCode, st.Code())
					}
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestListVolumes(t *testing.T) {
	ctx := context.Background()

	t.Run("empty managed subvolumes - returns empty list", func(t *testing.T) {
		mockClient := &mockAPIClient{
			FindManagedSubvolumesFunc: func(ctx context.Context, filesystem string) ([]nastyapi.Subvolume, error) {
				return []nastyapi.Subvolume{}, nil
			},
		}
		service := NewControllerService(mockClient, NewNodeRegistry(), "")
		resp, err := service.ListVolumes(ctx, &csi.ListVolumesRequest{})
		if err != nil {
			t.Fatalf("ListVolumes() error = %v", err)
		}
		if resp == nil {
			t.Fatal("ListVolumes() returned nil")
		}
	})
}

func TestIsVolumeAdoptable(t *testing.T) {
	tests := []struct {
		props map[string]string
		name  string
		want  bool
	}{
		{
			name: "valid NFS volume with all properties",
			props: map[string]string{
				nastyapi.PropertyManagedBy:      nastyapi.ManagedByValue,
				nastyapi.PropertyProtocol:       nastyapi.ProtocolNFS,
				nastyapi.PropertyCSIVolumeName:  "pvc-123",
				nastyapi.PropertyCapacityBytes:  "1073741824",
				nastyapi.PropertyDeleteStrategy: nastyapi.DeleteStrategyDelete,
			},
			want: true,
		},
		{
			name: "valid NVMe-oF volume with all properties",
			props: map[string]string{
				nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
				nastyapi.PropertyProtocol:      nastyapi.ProtocolNVMeOF,
				nastyapi.PropertyCSIVolumeName: "pvc-123",
			},
			want: true,
		},
		{
			name: "NFS volume with minimal properties",
			props: map[string]string{
				nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
				nastyapi.PropertyProtocol:  nastyapi.ProtocolNFS,
			},
			want: true,
		},
		{
			name: "missing managed_by property",
			props: map[string]string{
				nastyapi.PropertyProtocol: nastyapi.ProtocolNFS,
			},
			want: false,
		},
		{
			name: "wrong managed_by value",
			props: map[string]string{
				nastyapi.PropertyManagedBy: "other-csi-driver",
				nastyapi.PropertyProtocol:  nastyapi.ProtocolNFS,
			},
			want: false,
		},
		{
			name: "empty protocol value",
			props: map[string]string{
				nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
				nastyapi.PropertyProtocol:  "",
			},
			want: false,
		},
		{
			name: "missing protocol",
			props: map[string]string{
				nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
			},
			want: false,
		},
		{
			name: "NFS volume is adoptable without share path",
			props: map[string]string{
				nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
				nastyapi.PropertyProtocol:  nastyapi.ProtocolNFS,
			},
			want: true,
		},
		{
			name: "NVMe-oF volume is adoptable without NQN",
			props: map[string]string{
				nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
				nastyapi.PropertyProtocol:  nastyapi.ProtocolNVMeOF,
			},
			want: true,
		},
		{
			name:  "empty properties",
			props: map[string]string{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsVolumeAdoptable(tt.props)
			if got != tt.want {
				t.Errorf("IsVolumeAdoptable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetAdoptionInfo(t *testing.T) {
	props := map[string]string{
		nastyapi.PropertyCSIVolumeName:  "pvc-12345678",
		nastyapi.PropertyProtocol:       nastyapi.ProtocolNFS,
		nastyapi.PropertyCapacityBytes:  "10737418240",
		nastyapi.PropertyDeleteStrategy: nastyapi.DeleteStrategyRetain,
		nastyapi.PropertyPVCName:        "my-data",
		nastyapi.PropertyPVCNamespace:   "production",
		nastyapi.PropertyStorageClass:   "nasty-nfs",
	}

	info := GetAdoptionInfo(props)

	expectedFields := map[string]string{
		"volumeID":       "pvc-12345678",
		"protocol":       "nfs",
		"capacityBytes":  "10737418240",
		"deleteStrategy": "retain",
		"pvcName":        "my-data",
		"pvcNamespace":   "production",
		"storageClass":   "nasty-nfs",
	}

	for key, want := range expectedFields {
		if got := info[key]; got != want {
			t.Errorf("GetAdoptionInfo()[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestGetAdoptionInfo_Partial(t *testing.T) {
	props := map[string]string{
		nastyapi.PropertyCSIVolumeName: "minimal-volume",
		nastyapi.PropertyProtocol:      nastyapi.ProtocolNVMeOF,
	}

	info := GetAdoptionInfo(props)

	if info["volumeID"] != "minimal-volume" {
		t.Errorf("Expected volumeID to be 'minimal-volume', got %q", info["volumeID"])
	}
	if info["protocol"] != "nvmeof" {
		t.Errorf("Expected protocol to be 'nvmeof', got %q", info["protocol"])
	}
	if info["pvcName"] != "" {
		t.Errorf("Expected pvcName to be empty, got %q", info["pvcName"])
	}
	if info["storageClass"] != "" {
		t.Errorf("Expected storageClass to be empty, got %q", info["storageClass"])
	}
}

func TestIsMultiNodeMode(t *testing.T) {
	tests := []struct {
		name string
		mode csi.VolumeCapability_AccessMode_Mode
		want bool
	}{
		{
			name: "multi-node multi-writer",
			mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
			want: true,
		},
		{
			name: "multi-node reader",
			mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
			want: true,
		},
		{
			name: "single node writer",
			mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMultiNodeMode(tt.mode)
			if got != tt.want {
				t.Errorf("isMultiNodeMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
