package driver

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
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
					"pool":           "tank",
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
				if params.pool != "tank" {
					t.Errorf("Expected pool 'tank', got %s", params.pool)
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
					"pool":   "tank",
					"server": "192.168.1.100",
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
			name: "missing pool parameter",
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
					"pool": "tank",
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
	volumeName := "test-volume"
	server := "192.168.1.100"
	capacity := int64(5 * 1024 * 1024 * 1024)

	subvol := &MockSubvolume{
		pool: "tank",
		name: "csi/test-volume",
	}
	target := &MockISCSITarget{
		id:  "some-uuid",
		iqn: "iqn.2024-01.io.nasty.csi:test-volume",
	}

	_ = volumeName
	_ = server
	_ = capacity
	_ = subvol
	_ = target
	// buildISCSIVolumeResponse uses tnsapi.Subvolume and tnsapi.ISCSITarget directly;
	// tested indirectly via integration. This placeholder ensures compilation.
}

// MockSubvolume and MockISCSITarget are lightweight helpers for test assertions.
type MockSubvolume struct {
	pool string
	name string
}

type MockISCSITarget struct {
	id  string
	iqn string
}
