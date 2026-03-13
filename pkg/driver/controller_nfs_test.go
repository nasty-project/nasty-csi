package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/nasty-api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateNFSVolume(t *testing.T) {
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
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024, // 1GB
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					// No existing subvolume
					return nil, errors.New("not found")
				}
				m.CreateSubvolumeFunc = func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Pool: params.Pool,
						Name: params.Name,
						Path: "/mnt/tank/test-nfs-volume",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					return &nastyapi.NFSShare{
						ID:      "share-uuid-1",
						Path:    "/mnt/tank/test-nfs-volume",
						Enabled: true,
					}, nil
				}
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, pool, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Pool: pool, Name: name, Properties: props}, nil
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
				if resp.Volume.CapacityBytes != 1*1024*1024*1024 {
					t.Errorf("Expected capacity 1GB, got %d", resp.Volume.CapacityBytes)
				}
				if resp.Volume.VolumeContext["server"] != "192.168.1.100" {
					t.Errorf("Expected server 192.168.1.100, got %s", resp.Volume.VolumeContext["server"])
				}
			},
		},
		{
			name: "NFS volume creation with default capacity",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume-default",
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
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
				// No capacity specified - should default to 1GB
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.CreateSubvolumeFunc = func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Pool: params.Pool,
						Name: params.Name,
						Path: "/mnt/tank/test-nfs-volume-default",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					return &nastyapi.NFSShare{
						ID:      "share-uuid-2",
						Path:    "/mnt/tank/test-nfs-volume-default",
						Enabled: true,
					}, nil
				}
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, pool, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Pool: pool, Name: name, Properties: props}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateVolumeResponse) {
				t.Helper()
				if resp.Volume.CapacityBytes != 1*1024*1024*1024 {
					t.Errorf("Expected default capacity 1GB, got %d", resp.Volume.CapacityBytes)
				}
			},
		},
		{
			name: "missing pool parameter",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nfs",
					"server":   "192.168.1.100",
					// Missing pool parameter
				},
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "subvolume creation failure",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.CreateSubvolumeFunc = func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return nil, errors.New("pool not found")
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
		{
			name: "NFS share creation failure triggers subvolume cleanup",
			req: &csi.CreateVolumeRequest{
				Name: "test-nfs-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"protocol": "nfs",
					"pool":     "tank",
					"server":   "192.168.1.100",
				},
			},
			mockSetup: func(m *mockAPIClient) {
				subvolCreated := false
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
				}
				m.CreateSubvolumeFunc = func(ctx context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					subvolCreated = true
					return &nastyapi.Subvolume{
						Pool: params.Pool,
						Name: params.Name,
						Path: "/mnt/tank/test-nfs-volume",
					}, nil
				}
				m.CreateNFSShareFunc = func(ctx context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					return nil, errors.New("NFS service not running")
				}
				m.DeleteSubvolumeFunc = func(ctx context.Context, pool, name string) error {
					if !subvolCreated {
						t.Error("DeleteSubvolume called before CreateSubvolume")
					}
					return nil
				}
			},
			wantErr:  true,
			wantCode: codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)

			controller := NewControllerService(mockClient, NewNodeRegistry(), "")
			resp, err := controller.createNFSVolume(ctx, tt.req)

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

func TestDeleteNFSVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		meta      *VolumeMetadata
		mockSetup func(*mockAPIClient)
		name      string
		wantErr   bool
	}{
		{
			name: "successful deletion",
			meta: &VolumeMetadata{
				Name:         "test-nfs-volume",
				Protocol:     ProtocolNFS,
				DatasetID:    "tank/test-nfs-volume",
				DatasetName:  "test-nfs-volume",
				NFSShareUUID: "share-uuid-1",
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Pool:       pool,
						Name:       name,
						Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue},
					}, nil
				}
				m.DeleteNFSShareFunc = func(ctx context.Context, id string) error {
					if id != "share-uuid-1" {
						t.Errorf("Expected share ID share-uuid-1, got %s", id)
					}
					return nil
				}
				m.DeleteSubvolumeFunc = func(ctx context.Context, pool, name string) error {
					if pool != "tank" || name != "test-nfs-volume" {
						t.Errorf("Expected tank/test-nfs-volume, got %s/%s", pool, name)
					}
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "idempotent deletion - subvolume already deleted",
			meta: &VolumeMetadata{
				Name:         "test-nfs-volume",
				Protocol:     ProtocolNFS,
				DatasetID:    "tank/test-nfs-volume",
				DatasetName:  "test-nfs-volume",
				NFSShareUUID: "share-uuid-1",
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					// Subvolume already gone
					return nil, errors.New("Object not found")
				}
			},
			wantErr: false, // Should succeed due to idempotency
		},
		{
			name: "deletion with subvolume backend error should fail",
			meta: &VolumeMetadata{
				Name:         "test-nfs-volume",
				Protocol:     ProtocolNFS,
				DatasetID:    "tank/test-nfs-volume",
				DatasetName:  "test-nfs-volume",
				NFSShareUUID: "share-uuid-1",
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Pool:       pool,
						Name:       name,
						Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue},
					}, nil
				}
				m.DeleteNFSShareFunc = func(ctx context.Context, id string) error {
					return nil
				}
				m.DeleteSubvolumeFunc = func(ctx context.Context, pool, name string) error {
					return errors.New("some backend error")
				}
			},
			wantErr: true, // Should fail to prevent orphaned subvolumes
		},
		{
			name: "deletion with no share UUID skips share deletion",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "tank/test-nfs-volume",
				DatasetName: "test-nfs-volume",
				// No NFSShareUUID
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Pool:       pool,
						Name:       name,
						Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue},
					}, nil
				}
				m.DeleteNFSShareFunc = func(ctx context.Context, id string) error {
					t.Error("DeleteNFSShare should not be called when share UUID is empty")
					return nil
				}
				m.DeleteSubvolumeFunc = func(ctx context.Context, pool, name string) error {
					return nil
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)

			controller := NewControllerService(mockClient, NewNodeRegistry(), "")
			_, err := controller.deleteNFSVolume(ctx, tt.meta)

			if tt.wantErr && err == nil {
				t.Error("Expected error but got nil")
			} else if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestExpandNFSVolume(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		mockSetup     func(*mockAPIClient)
		checkResponse func(*testing.T, *csi.ControllerExpandVolumeResponse)
		meta          *VolumeMetadata
		name          string
		requiredBytes int64
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful NFS volume expansion",
			meta: &VolumeMetadata{
				Name:         "test-nfs-volume",
				Protocol:     ProtocolNFS,
				DatasetID:    "tank/test-nfs-volume",
				DatasetName:  "test-nfs-volume",
				NFSShareUUID: "share-uuid-1",
			},
			requiredBytes: 5 * 1024 * 1024 * 1024, // 5GB
			mockSetup: func(m *mockAPIClient) {
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, pool, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					if pool != "tank" || name != "test-nfs-volume" {
						t.Errorf("Expected tank/test-nfs-volume, got %s/%s", pool, name)
					}
					return &nastyapi.Subvolume{Pool: pool, Name: name, Properties: props}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.CapacityBytes != 5*1024*1024*1024 {
					t.Errorf("Expected capacity 5GB, got %d", resp.CapacityBytes)
				}
				if resp.NodeExpansionRequired {
					t.Error("Expected NodeExpansionRequired to be false for NFS")
				}
			},
		},
		{
			name: "expansion with missing dataset ID",
			meta: &VolumeMetadata{
				Name:        "test-nfs-volume",
				Protocol:    ProtocolNFS,
				DatasetID:   "", // Missing dataset ID
				DatasetName: "test-nfs-volume",
			},
			requiredBytes: 5 * 1024 * 1024 * 1024,
			mockSetup:     func(m *mockAPIClient) {},
			wantErr:       true,
			wantCode:      codes.InvalidArgument,
		},
		{
			name: "NASty API error during expansion",
			meta: &VolumeMetadata{
				Name:         "test-nfs-volume",
				Protocol:     ProtocolNFS,
				DatasetID:    "tank/test-nfs-volume",
				DatasetName:  "test-nfs-volume",
				NFSShareUUID: "share-uuid-1",
			},
			requiredBytes: 5 * 1024 * 1024 * 1024,
			mockSetup: func(m *mockAPIClient) {
				m.SetSubvolumePropertiesFunc = func(ctx context.Context, pool, name string, props map[string]string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("subvolume not found")
				}
			},
			// expandNFSVolume logs the error but still returns success with the requested capacity
			// (the xattr set failure is non-fatal)
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.ControllerExpandVolumeResponse) {
				t.Helper()
				if resp.CapacityBytes != 5*1024*1024*1024 {
					t.Errorf("Expected capacity 5GB, got %d", resp.CapacityBytes)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)

			controller := NewControllerService(mockClient, NewNodeRegistry(), "")
			resp, err := controller.expandNFSVolume(ctx, tt.meta, tt.requiredBytes)

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

func TestSetupNFSVolumeFromClone_Unimplemented(t *testing.T) {
	ctx := context.Background()

	mockClient := &mockAPIClient{}
	controller := NewControllerService(mockClient, NewNodeRegistry(), "")

	resp, err := controller.setupNFSVolumeFromClone(ctx, &csi.CreateVolumeRequest{Name: "test"}, &nastyapi.Subvolume{}, "server", &cloneInfo{})
	if resp != nil {
		t.Error("Expected nil response")
	}
	if err == nil {
		t.Fatal("Expected error but got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented, got %v", st.Code())
	}
}

func TestParseNFSClients(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectCount int
		expectFirst nastyapi.NFSClient
	}{
		{
			name:        "empty string defaults to wildcard",
			input:       "",
			expectCount: 1,
			expectFirst: nastyapi.NFSClient{Host: "*", Options: "rw,no_root_squash"},
		},
		{
			name:        "single client with options",
			input:       "10.0.0.1:rw,no_root_squash",
			expectCount: 2,
			expectFirst: nastyapi.NFSClient{Host: "10.0.0.1", Options: "rw"},
		},
		{
			name:        "client without options gets defaults",
			input:       "10.0.0.1",
			expectCount: 1,
			expectFirst: nastyapi.NFSClient{Host: "10.0.0.1", Options: "rw,no_root_squash"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseNFSClients(tt.input)
			if len(result) != tt.expectCount {
				t.Errorf("Expected %d clients, got %d", tt.expectCount, len(result))
				return
			}
			if result[0].Host != tt.expectFirst.Host {
				t.Errorf("Expected host %q, got %q", tt.expectFirst.Host, result[0].Host)
			}
			if result[0].Options != tt.expectFirst.Options {
				t.Errorf("Expected options %q, got %q", tt.expectFirst.Options, result[0].Options)
			}
		})
	}
}

func TestParseCapacityFromComment(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    int64
	}{
		{
			name:    "valid comment",
			comment: "CSI Volume: my-vol | Capacity: 1073741824",
			want:    1073741824,
		},
		{
			name:    "empty comment returns 0",
			comment: "",
			want:    0,
		},
		{
			name:    "invalid format returns 0",
			comment: "some random comment",
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCapacityFromComment(tt.comment)
			if got != tt.want {
				t.Errorf("Expected %d, got %d", tt.want, got)
			}
		})
	}
}
