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

func TestValidateNVMeOFParams(t *testing.T) {
	tests := []struct {
		req      *csi.CreateVolumeRequest
		check    func(*testing.T, *nvmeofVolumeParams)
		name     string
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "valid request with all parameters",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				Parameters: map[string]string{
					"filesystem":     "first",
					"server":         "192.168.1.100",
					"deleteStrategy": "retain",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 10 * 1024 * 1024 * 1024, // 10GB
				},
			},
			wantErr: false,
			check: func(t *testing.T, params *nvmeofVolumeParams) {
				t.Helper()
				if params.filesystem != "first" {
					t.Errorf("Expected filesystem 'first', got %s", params.filesystem)
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
				Name: "test-nvmeof-volume",
				Parameters: map[string]string{
					"filesystem": "first",
					"server":     "192.168.1.100",
				},
			},
			wantErr: false,
			check: func(t *testing.T, params *nvmeofVolumeParams) {
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
				Name: "test-nvmeof-volume",
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
				Name: "test-nvmeof-volume",
				Parameters: map[string]string{
					"filesystem": "first",
				},
			},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := validateNVMeOFParams(tt.req)
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

func TestGenerateNQN(t *testing.T) {
	tests := []struct {
		name       string
		nqnPrefix  string
		volumeName string
		want       string
	}{
		{
			name:       "standard volume name",
			nqnPrefix:  defaultNQNPrefix,
			volumeName: "test-volume",
			want:       defaultNQNPrefix + ":test-volume",
		},
		{
			name:       "pvc volume name",
			nqnPrefix:  defaultNQNPrefix,
			volumeName: "pvc-abc123-def456",
			want:       defaultNQNPrefix + ":pvc-abc123-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateNQN(tt.nqnPrefix, tt.volumeName)
			if got != tt.want {
				t.Errorf("generateNQN(%q, %q) = %q, want %q", tt.nqnPrefix, tt.volumeName, got, tt.want)
			}
		})
	}
}

func TestCreateNVMeOFVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req           *csi.CreateVolumeRequest
		mockSetup     func(*MockAPIClientForSnapshots)
		checkResponse func(*testing.T, *csi.CreateVolumeResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "missing filesystem parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nvmeof",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing server parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol":   "nvmeof",
					"filesystem": "first",
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "subvolume creation failure",
			req: &csi.CreateVolumeRequest{
				Name: "test-nvmeof-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					"protocol":   "nvmeof",
					"filesystem": "first",
					"server":     "192.168.1.100",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 5 * 1024 * 1024 * 1024,
				},
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, nastyapi.ErrDatasetNotFound
				}
				m.CreateSubvolumeFunc = func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return nil, errors.New("insufficient space on filesystem")
				}
			},
			wantErr:  true,
			wantCode: codes.ResourceExhausted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.createNVMeOFVolume(ctx, tt.req)
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
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestDeleteNVMeOFVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		meta      *VolumeMetadata
		mockSetup func(*MockAPIClientForSnapshots)
		name      string
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name: "successful deletion",
			meta: &VolumeMetadata{
				Name:                "test-volume",
				Protocol:            ProtocolNVMeOF,
				DatasetID:           "first/test-volume",
				NVMeOFSubsystemUUID: "some-uuid",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Filesystem: filesystem,
						Name:       name,
						Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue},
					}, nil
				}
				m.DeleteSubvolumeFunc = func(ctx context.Context, filesystem, name string) error {
					return nil
				}
				m.DeleteNVMeOFSubsystemFunc = func(ctx context.Context, id string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "subvolume not found is idempotent",
			meta: &VolumeMetadata{
				Name:      "missing-volume",
				Protocol:  ProtocolNVMeOF,
				DatasetID: "first/missing-volume",
			},
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return nil, nastyapi.ErrDatasetNotFound
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.deleteNVMeOFVolume(ctx, tt.meta)
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
			if resp == nil {
				t.Error("Expected response to be non-nil")
			}
		})
	}
}

func TestExpandNVMeOFVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		meta          *VolumeMetadata
		mockSetup     func(*MockAPIClientForSnapshots)
		name          string
		requiredBytes int64
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful expansion",
			meta: &VolumeMetadata{
				Name:        "test-volume",
				Protocol:    ProtocolNVMeOF,
				DatasetID:   "first/test-volume",
				DatasetName: "first/test-volume",
			},
			requiredBytes: 10 * 1024 * 1024 * 1024, // 10GB
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Filesystem: filesystem, Name: name}, nil
				}
			},
			wantErr: false,
		},
		{
			name: "expansion fails - no dataset ID",
			meta: &VolumeMetadata{
				Name:        "volume-no-dataset",
				Protocol:    ProtocolNVMeOF,
				DatasetID:   "", // Empty dataset ID
				DatasetName: "tank/csi/volume-no-dataset",
			},
			requiredBytes: 10 * 1024 * 1024 * 1024,
			wantErr:       true,
			wantCode:      codes.InvalidArgument,
		},
		{
			name: "expansion fails - update error",
			meta: &VolumeMetadata{
				Name:        "missing-volume",
				Protocol:    ProtocolNVMeOF,
				DatasetID:   "first/missing-volume",
				DatasetName: "first/missing-volume",
			},
			requiredBytes: 10 * 1024 * 1024 * 1024,
			mockSetup: func(m *MockAPIClientForSnapshots) {
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("subvolume not found")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockAPIClientForSnapshots{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			controller := &ControllerService{
				apiClient: mockClient,
			}

			resp, err := controller.expandNVMeOFVolume(ctx, tt.meta, tt.requiredBytes)
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
			if resp == nil {
				t.Error("Expected response to be non-nil")
				return
			}
			if resp.CapacityBytes != tt.requiredBytes {
				t.Errorf("Expected capacity %d, got %d", tt.requiredBytes, resp.CapacityBytes)
			}
			if resp.NodeExpansionRequired {
				t.Error("Expected NodeExpansionRequired to be false for NVMe-oF volumes (block devices don't need node-side expansion)")
			}
		})
	}
}
