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

// TestEncodeDecodeSnapshotID tests encoding and decoding of snapshot IDs.
func TestEncodeDecodeSnapshotID(t *testing.T) {
	tests := []struct {
		name    string
		meta    SnapshotMetadata
		wantErr bool
	}{
		{
			name: "NFS snapshot",
			meta: SnapshotMetadata{
				SnapshotName: "snap1",
				SourceVolume: "tank/test-volume",
				Protocol:     "nfs",
			},
			wantErr: false,
		},
		{
			name: "NVMe-oF snapshot",
			meta: SnapshotMetadata{
				SnapshotName: "snap2",
				SourceVolume: "tank/test-zvol",
				Protocol:     "nvmeof",
			},
			wantErr: false,
		},
		{
			name: "missing protocol",
			meta: SnapshotMetadata{
				SnapshotName: "snap",
				SourceVolume: "tank/vol",
			},
			wantErr: true,
		},
		{
			name: "missing source volume",
			meta: SnapshotMetadata{
				SnapshotName: "snap",
				Protocol:     "nfs",
			},
			wantErr: true,
		},
		{
			name: "missing snapshot name",
			meta: SnapshotMetadata{
				SourceVolume: "tank/vol",
				Protocol:     "nfs",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := encodeSnapshotID(tt.meta)
			if (err != nil) != tt.wantErr {
				t.Errorf("encodeSnapshotID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if encoded == "" {
				t.Error("encodeSnapshotID() returned empty string")
				return
			}
			if len(encoded) > 128 {
				t.Errorf("encodeSnapshotID() returned string of %d bytes, want <= 128", len(encoded))
			}

			decoded, decErr := decodeSnapshotID(encoded)
			if decErr != nil {
				t.Errorf("decodeSnapshotID() error = %v", decErr)
				return
			}

			if decoded.SnapshotName != tt.meta.SnapshotName {
				t.Errorf("SnapshotName = %v, want %v", decoded.SnapshotName, tt.meta.SnapshotName)
			}
			if decoded.SourceVolume != tt.meta.SourceVolume {
				t.Errorf("SourceVolume = %v, want %v", decoded.SourceVolume, tt.meta.SourceVolume)
			}
			if decoded.Protocol != tt.meta.Protocol {
				t.Errorf("Protocol = %v, want %v", decoded.Protocol, tt.meta.Protocol)
			}
		})
	}
}

func TestCreateSnapshot(t *testing.T) {
	ctx := context.Background()
	volumeID := "tank/csi/test-volume"

	tests := []struct {
		req           *csi.CreateSnapshotRequest
		mockSetup     func(*mockAPIClient)
		checkResponse func(*testing.T, *csi.CreateSnapshotResponse)
		name          string
		wantCode      codes.Code
		wantErr       bool
	}{
		{
			name: "successful snapshot creation",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name:      name,
						Pool:      pool,
						Snapshots: []string{},
						Properties: map[string]string{
							nastyapi.PropertyProtocol:      ProtocolNFS,
							nastyapi.PropertyCapacityBytes: "1073741824",
						},
					}, nil
				}
				m.CreateSnapshotFunc = func(ctx context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error) {
					return &nastyapi.Snapshot{
						Name:      params.Name,
						Subvolume: params.Subvolume,
						Pool:      params.Pool,
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateSnapshotResponse) {
				t.Helper()
				if resp.Snapshot == nil {
					t.Error("Expected snapshot to be non-nil")
					return
				}
				if resp.Snapshot.SnapshotId == "" {
					t.Error("Expected snapshot ID to be non-empty")
				}
				if resp.Snapshot.SourceVolumeId != volumeID {
					t.Errorf("Expected SourceVolumeId %s, got %s", volumeID, resp.Snapshot.SourceVolumeId)
				}
				if !resp.Snapshot.ReadyToUse {
					t.Error("Expected ReadyToUse to be true")
				}
			},
		},
		{
			name: "idempotent - snapshot already exists",
			req: &csi.CreateSnapshotRequest{
				Name:           "existing-snap",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name:      name,
						Pool:      pool,
						Snapshots: []string{"existing-snap"},
						Properties: map[string]string{
							nastyapi.PropertyProtocol: ProtocolNFS,
						},
					}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateSnapshotResponse) {
				t.Helper()
				if resp.Snapshot == nil {
					t.Error("Expected snapshot to be non-nil")
				}
			},
		},
		{
			name: "missing snapshot name",
			req: &csi.CreateSnapshotRequest{
				Name:           "",
				SourceVolumeId: volumeID,
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "missing source volume ID",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: "",
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "invalid source volume ID format",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: "no-slash-volume",
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "source volume not found",
			req: &csi.CreateSnapshotRequest{
				Name:           "test-snapshot",
				SourceVolumeId: "tank/csi/nonexistent",
			},
			mockSetup: func(m *mockAPIClient) {
				m.GetSubvolumeFunc = func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("not found")
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

			resp, err := service.CreateSnapshot(ctx, tt.req)
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

func TestDeleteSnapshot(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		req       *csi.DeleteSnapshotRequest
		mockSetup func(*mockAPIClient)
		name      string
		wantCode  codes.Code
		wantErr   bool
	}{
		{
			name: "missing snapshot ID",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "",
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   true,
			wantCode:  codes.InvalidArgument,
		},
		{
			name: "invalid snapshot ID - returns success (idempotent)",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "invalid-id-format",
			},
			mockSetup: func(m *mockAPIClient) {},
			wantErr:   false, // Idempotent per CSI spec
		},
		{
			name: "successful deletion",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "nfs:tank/csi/test-volume@my-snapshot",
			},
			mockSetup: func(m *mockAPIClient) {
				m.DeleteSnapshotFunc = func(ctx context.Context, pool, subvolume, name string) error {
					return nil
				}
			},
			wantErr: false,
		},
		{
			name: "snapshot not found - returns success (idempotent)",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "nfs:tank/csi/test-volume@nonexistent-snap",
			},
			mockSetup: func(m *mockAPIClient) {
				m.DeleteSnapshotFunc = func(ctx context.Context, pool, subvolume, name string) error {
					return errors.New("not found")
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &mockAPIClient{}
			tt.mockSetup(mockClient)
			service := NewControllerService(mockClient, NewNodeRegistry(), "")

			_, err := service.DeleteSnapshot(ctx, tt.req)
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

func TestListSnapshots(t *testing.T) {
	ctx := context.Background()

	t.Run("list by snapshot ID - found", func(t *testing.T) {
		snapshotID := "nfs:tank/csi/test-volume@my-snap"
		mockClient := &mockAPIClient{
			GetSubvolumeFunc: func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					Name:      name,
					Pool:      pool,
					Snapshots: []string{"my-snap"},
					Properties: map[string]string{
						nastyapi.PropertyCapacityBytes: "1073741824",
					},
				}, nil
			},
		}
		service := NewControllerService(mockClient, NewNodeRegistry(), "")
		resp, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{
			SnapshotId: snapshotID,
		})
		if err != nil {
			t.Fatalf("ListSnapshots() error = %v", err)
		}
		if len(resp.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(resp.Entries))
		}
	})

	t.Run("list by snapshot ID - not found", func(t *testing.T) {
		snapshotID := "nfs:tank/csi/test-volume@nonexistent"
		mockClient := &mockAPIClient{
			GetSubvolumeFunc: func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					Name:      name,
					Pool:      pool,
					Snapshots: []string{"other-snap"},
				}, nil
			},
		}
		service := NewControllerService(mockClient, NewNodeRegistry(), "")
		resp, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{
			SnapshotId: snapshotID,
		})
		if err != nil {
			t.Fatalf("ListSnapshots() error = %v", err)
		}
		if len(resp.Entries) != 0 {
			t.Errorf("Expected 0 entries, got %d", len(resp.Entries))
		}
	})

	t.Run("list by source volume ID", func(t *testing.T) {
		volumeID := "tank/csi/test-volume"
		mockClient := &mockAPIClient{
			GetSubvolumeFunc: func(ctx context.Context, pool, name string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					Name:      name,
					Pool:      pool,
					Snapshots: []string{"snap1", "snap2"},
					Properties: map[string]string{
						nastyapi.PropertyProtocol:      ProtocolNFS,
						nastyapi.PropertyCapacityBytes: "1073741824",
					},
				}, nil
			},
		}
		service := NewControllerService(mockClient, NewNodeRegistry(), "")
		resp, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{
			SourceVolumeId: volumeID,
		})
		if err != nil {
			t.Fatalf("ListSnapshots() error = %v", err)
		}
		if len(resp.Entries) != 2 {
			t.Errorf("Expected 2 entries, got %d", len(resp.Entries))
		}
	})

	t.Run("list all - empty when no pool configured", func(t *testing.T) {
		service := NewControllerService(&mockAPIClient{}, NewNodeRegistry(), "")
		resp, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{})
		if err != nil {
			t.Fatalf("ListSnapshots() error = %v", err)
		}
		if len(resp.Entries) != 0 {
			t.Errorf("Expected 0 entries, got %d", len(resp.Entries))
		}
	})
}

func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		err  error
		name string
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "not found error",
			err:  errors.New("resource not found"),
			want: true,
		},
		{
			name: "does not exist error",
			err:  errors.New("object does not exist"),
			want: true,
		},
		{
			name: "ENOENT error",
			err:  errors.New("ENOENT"),
			want: true,
		},
		{
			name: "404 error",
			err:  errors.New("404 HTTP"),
			want: true,
		},
		{
			name: "other error",
			err:  errors.New("internal server error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFoundError(tt.err); got != tt.want {
				t.Errorf("isNotFoundError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEncodeSnapshotToken(t *testing.T) {
	tests := []struct {
		name   string
		offset int
		want   string
	}{
		{name: "zero offset", offset: 0, want: "0"},
		{name: "positive offset", offset: 5, want: "5"},
		{name: "large offset", offset: 100, want: "100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeSnapshotToken(tt.offset)
			if got != tt.want {
				t.Errorf("encodeSnapshotToken(%d) = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}

func TestParseSnapshotToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		want    int
		wantErr bool
	}{
		{name: "zero token", token: "0", want: 0},
		{name: "valid token", token: "42", want: 42},
		{name: "invalid token", token: "abc", wantErr: true},
		{name: "empty token", token: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSnapshotToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSnapshotToken(%q) error = %v, wantErr %v", tt.token, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseSnapshotToken(%q) = %d, want %d", tt.token, got, tt.want)
			}
		})
	}
}

func TestSnapshotTokenRoundtrip(t *testing.T) {
	for _, offset := range []int{0, 1, 10, 100, 999} {
		encoded := encodeSnapshotToken(offset)
		decoded, err := parseSnapshotToken(encoded)
		if err != nil {
			t.Errorf("roundtrip failed for offset %d: %v", offset, err)
			continue
		}
		if decoded != offset {
			t.Errorf("roundtrip: encoded %d as %q, decoded as %d", offset, encoded, decoded)
		}
	}
}

func TestCreateVolumeFromSnapshot_Unimplemented(t *testing.T) {
	// Clone/restore from snapshot is stubbed to Unimplemented.
	ctx := context.Background()
	service := NewControllerService(&mockAPIClient{}, NewNodeRegistry(), "")

	// A CreateVolume request with a snapshot source triggers createVolumeFromSnapshot
	req := &csi.CreateVolumeRequest{
		Name: "restored-volume",
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			},
		},
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: "nfs:tank/csi/source-vol@snap1",
				},
			},
		},
	}

	_, err := service.CreateVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected Unimplemented error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected codes.Unimplemented, got %v", st.Code())
	}
}
