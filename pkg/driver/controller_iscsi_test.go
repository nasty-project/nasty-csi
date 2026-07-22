package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateISCSIParams(t *testing.T) {
	tests := []struct {
		req      *csi.CreateVolumeRequest
		check    func(*testing.T, *iscsiVolumeParams)
		name     string
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "valid request with all parameters",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"filesystem":     "tank",
					"server":         "192.168.1.100",
					"deleteStrategy": "retain",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 10 * 1024 * 1024 * 1024, // 10GB
				},
			},
			wantErr: false,
			check: func(t *testing.T, params *iscsiVolumeParams) {
				t.Helper()
				if params.filesystem != "tank" {
					t.Errorf("Expected filesystem 'tank', got %s", params.filesystem)
				}
				if params.server != "192.168.1.100" {
					t.Errorf("Expected server '192.168.1.100', got %s", params.server)
				}
				if params.deleteStrategy != "retain" {
					t.Errorf("Expected deleteStrategy 'retain', got %s", params.deleteStrategy)
				}
				if params.requestedCapacity != 10*1024*1024*1024 {
					t.Errorf("Expected capacity 10GB, got %d", params.requestedCapacity)
				}
			},
		},
		{
			name: "valid request with minimal parameters",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"filesystem": "tank",
					"server":     "192.168.1.100",
				},
			},
			wantErr: false,
			check: func(t *testing.T, params *iscsiVolumeParams) {
				t.Helper()
				// deleteStrategy defaults to "delete"
				if params.deleteStrategy != "delete" {
					t.Errorf("Expected deleteStrategy to default to 'delete', got %s", params.deleteStrategy)
				}
				// Capacity defaults to 1GB
				if params.requestedCapacity != 1*1024*1024*1024 {
					t.Errorf("Expected default capacity 1GB, got %d", params.requestedCapacity)
				}
			},
		},
		{
			name: "missing filesystem parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"server": "192.168.1.100",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "missing server parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-iscsi-volume",
				Parameters: map[string]string{
					"filesystem": "tank",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := validateISCSIParams(tt.req)
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
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.check != nil {
				tt.check(t, params)
			}
		})
	}
}

func TestGenerateIQN(t *testing.T) {
	tests := []struct {
		name       string
		volumeName string
		want       string
	}{
		{
			name:       "simple volume name",
			volumeName: "my-volume",
			want:       "iqn.2024-01.io.nasty.csi:my-volume",
		},
		{
			name:       "volume with special characters",
			volumeName: "pvc-abc123-def456",
			want:       "iqn.2024-01.io.nasty.csi:pvc-abc123-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateIQN(tt.volumeName)
			if got != tt.want {
				t.Errorf("generateIQN(%q) = %q, want %q", tt.volumeName, got, tt.want)
			}
		})
	}
}

func TestBuildISCSIVolumeResponse(t *testing.T) {
	// buildISCSIVolumeResponse uses nastyapi.Subvolume and nastyapi.ISCSITarget directly;
	// tested indirectly via integration. This placeholder ensures compilation.
	t.Log("buildISCSIVolumeResponse is tested indirectly via integration tests")
}

func TestCreateISCSIVolumeInitializesOnlyMountedVolumes(t *testing.T) {
	tests := []struct {
		name               string
		capability         *csi.VolumeCapability
		expectedFilesystem string
	}{
		{
			name: "mounted volume requests backend ext4 initialization",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			},
			expectedFilesystem: fsTypeExt4,
		},
		{
			name: "raw block volume remains unformatted",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := "/dev/loop7"
			uuid := "abc-123"
			created := false
			client := &mockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("subvolume not found")
				},
				CreateSubvolumeFunc: func(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					if params.BlockFilesystem != tt.expectedFilesystem {
						t.Fatalf("BlockFilesystem = %q, want %q", params.BlockFilesystem, tt.expectedFilesystem)
					}
					created = true
					subvolume := &nastyapi.Subvolume{
						Filesystem:  params.Filesystem,
						Name:        params.Name,
						BlockDevice: &device,
						Created:     true,
					}
					if tt.expectedFilesystem != "" {
						subvolume.BlockFilesystem = &tt.expectedFilesystem
						subvolume.BlockFilesystemUUID = &uuid
					}
					return subvolume, nil
				},
				CreateISCSITargetFunc: func(_ context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
					if !created {
						t.Fatal("iSCSI target was created before the backend subvolume was ready")
					}
					return &nastyapi.ISCSITarget{ID: "target-1", IQN: generateIQN("test-volume")}, nil
				},
			}
			controller := NewControllerService(client, NewNodeRegistry(), "")
			request := &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{tt.capability},
				Parameters: map[string]string{
					"protocol":      ProtocolISCSI,
					"filesystem":    "tank",
					"server":        "192.0.2.1",
					"markAdoptable": VolumeContextValueTrue,
				},
			}

			if _, err := controller.createISCSIVolume(context.Background(), request); err != nil {
				t.Fatalf("createISCSIVolume() error = %v", err)
			}
		})
	}
}

func TestAdoptISCSIVolumeFailsClosedOnTargetListError(t *testing.T) {
	device := "/dev/loop7"
	client := &mockAPIClient{
		ListISCSITargetsFunc: func(context.Context) ([]nastyapi.ISCSITarget, error) {
			return nil, errors.New("backend unavailable")
		},
		CreateISCSITargetFunc: func(context.Context, nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
			t.Fatal("target creation must not follow an inconclusive list")
			return nil, errors.New("unexpected target creation")
		},
	}
	controller := NewControllerService(client, NewNodeRegistry(), "")
	request := &csi.CreateVolumeRequest{Name: "test-volume"}
	subvolume := &nastyapi.Subvolume{Filesystem: "tank", Name: "test-volume", BlockDevice: &device}

	if _, err := controller.adoptISCSIVolume(context.Background(), request, subvolume, map[string]string{"server": "192.0.2.1"}); err == nil {
		t.Fatal("expected adoption to fail when existing targets cannot be listed")
	}
}
