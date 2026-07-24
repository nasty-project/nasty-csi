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
	const snapshotCreatedAt int64 = 1_700_000_123

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
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name:       name,
						Filesystem: filesystem,
						Snapshots:  []string{},
						Properties: map[string]string{
							nastyapi.PropertyProtocol:      ProtocolNFS,
							nastyapi.PropertyCapacityBytes: "1073741824",
						},
					}, nil
				}
				m.CreateSnapshotFunc = func(ctx context.Context, params nastyapi.SnapshotCreateParams) (*nastyapi.Snapshot, error) {
					return &nastyapi.Snapshot{
						Name:       params.Name,
						Subvolume:  params.Subvolume,
						Filesystem: params.Filesystem,
						CreatedAt:  snapshotTimePtr(snapshotCreatedAt),
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
				if resp.Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt {
					t.Errorf("CreationTime = %d, want %d", resp.Snapshot.CreationTime.GetSeconds(), snapshotCreatedAt)
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
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Name:       name,
						Filesystem: filesystem,
						Snapshots:  []string{"existing-snap"},
						Properties: map[string]string{
							nastyapi.PropertyProtocol: ProtocolNFS,
						},
					}, nil
				}
				m.ListSnapshotsFunc = func(context.Context, string) ([]nastyapi.Snapshot, error) {
					return []nastyapi.Snapshot{{Name: "existing-snap", Subvolume: "csi/test-volume", CreatedAt: snapshotTimePtr(snapshotCreatedAt)}}, nil
				}
			},
			wantErr: false,
			checkResponse: func(t *testing.T, resp *csi.CreateSnapshotResponse) {
				t.Helper()
				if resp.Snapshot == nil {
					t.Error("Expected snapshot to be non-nil")
				}
				if resp.Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt {
					t.Errorf("CreationTime = %d, want %d", resp.Snapshot.CreationTime.GetSeconds(), snapshotCreatedAt)
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
				m.GetSubvolumeFunc = func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
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
				m.DeleteSnapshotFunc = func(ctx context.Context, filesystem, subvolume, name string) error {
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
				m.DeleteSnapshotFunc = func(ctx context.Context, filesystem, subvolume, name string) error {
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
	const snapshotCreatedAt int64 = 1_700_000_456

	t.Run("list by snapshot ID - found", func(t *testing.T) {
		snapshotID := "nfs:tank/csi/test-volume@my-snap"
		mockClient := &mockAPIClient{
			GetSubvolumeFunc: func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					Name:       name,
					Filesystem: filesystem,
					Snapshots:  []string{"my-snap"},
					Properties: map[string]string{
						nastyapi.PropertyCapacityBytes: "1073741824",
					},
				}, nil
			},
			ListSnapshotsFunc: func(context.Context, string) ([]nastyapi.Snapshot, error) {
				return []nastyapi.Snapshot{{Name: "my-snap", Subvolume: "csi/test-volume", CreatedAt: snapshotTimePtr(snapshotCreatedAt)}}, nil
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
		if resp.Entries[0].Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt {
			t.Errorf("CreationTime = %d, want %d", resp.Entries[0].Snapshot.CreationTime.GetSeconds(), snapshotCreatedAt)
		}
		resp, err = service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{
			SnapshotId:    snapshotID,
			StartingToken: "1",
		})
		if err != nil {
			t.Fatalf("ListSnapshots() with exhausted token error = %v", err)
		}
		if len(resp.Entries) != 0 {
			t.Errorf("Expected exhausted token to return 0 entries, got %d", len(resp.Entries))
		}
	})

	t.Run("list by snapshot ID - not found", func(t *testing.T) {
		snapshotID := "nfs:tank/csi/test-volume@nonexistent"
		mockClient := &mockAPIClient{
			GetSubvolumeFunc: func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					Name:       name,
					Filesystem: filesystem,
					Snapshots:  []string{"other-snap"},
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
			GetSubvolumeFunc: func(ctx context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{
					Name:       name,
					Filesystem: filesystem,
					Snapshots:  []string{"snap2", "snap1"},
					Properties: map[string]string{
						nastyapi.PropertyProtocol:      ProtocolNFS,
						nastyapi.PropertyCapacityBytes: "1073741824",
					},
				}, nil
			},
			ListSnapshotsFunc: func(context.Context, string) ([]nastyapi.Snapshot, error) {
				return []nastyapi.Snapshot{
					{Name: "snap1", Subvolume: "csi/test-volume", CreatedAt: snapshotTimePtr(snapshotCreatedAt)},
					{Name: "snap2", Subvolume: "csi/test-volume", CreatedAt: snapshotTimePtr(snapshotCreatedAt + 1)},
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
		if resp.Entries[0].Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt || resp.Entries[1].Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt+1 {
			t.Errorf("snapshot creation times were not preserved: %d, %d", resp.Entries[0].Snapshot.CreationTime.GetSeconds(), resp.Entries[1].Snapshot.CreationTime.GetSeconds())
		}

		firstPage, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{SourceVolumeId: volumeID, MaxEntries: 1})
		if err != nil {
			t.Fatalf("ListSnapshots() first page error = %v", err)
		}
		if len(firstPage.Entries) != 1 || firstPage.Entries[0].Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt || firstPage.NextToken != "1" {
			t.Fatalf("unexpected first page: %+v", firstPage)
		}
		secondPage, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{SourceVolumeId: volumeID, StartingToken: firstPage.NextToken})
		if err != nil {
			t.Fatalf("ListSnapshots() second page error = %v", err)
		}
		if len(secondPage.Entries) != 1 || secondPage.Entries[0].Snapshot.CreationTime.GetSeconds() != snapshotCreatedAt+1 {
			t.Fatalf("unexpected second page: %+v", secondPage)
		}
	})

	t.Run("list all uses stable pagination order", func(t *testing.T) {
		mockClient := &mockAPIClient{
			FindManagedSubvolumesFunc: func(context.Context, string) ([]nastyapi.Subvolume, error) {
				return []nastyapi.Subvolume{
					{Filesystem: "tank", Name: "z-volume", Snapshots: []string{"snap"}, Properties: map[string]string{}},
					{Filesystem: "tank", Name: "a-volume", Snapshots: []string{"snap"}, Properties: map[string]string{}},
				}, nil
			},
			ListSnapshotsFunc: func(context.Context, string) ([]nastyapi.Snapshot, error) {
				return []nastyapi.Snapshot{
					{Name: "snap", Subvolume: "z-volume", CreatedAt: snapshotTimePtr(snapshotCreatedAt + 1)},
					{Name: "snap", Subvolume: "a-volume", CreatedAt: snapshotTimePtr(snapshotCreatedAt)},
				}, nil
			},
		}
		service := NewControllerService(mockClient, NewNodeRegistry(), "")
		firstPage, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{MaxEntries: 1})
		if err != nil {
			t.Fatalf("ListSnapshots() first page error = %v", err)
		}
		if len(firstPage.Entries) != 1 || firstPage.Entries[0].Snapshot.SourceVolumeId != "tank/a-volume" || firstPage.NextToken != "1" {
			t.Fatalf("unexpected first page: %+v", firstPage)
		}
		secondPage, err := service.ListSnapshots(ctx, &csi.ListSnapshotsRequest{StartingToken: firstPage.NextToken})
		if err != nil {
			t.Fatalf("ListSnapshots() second page error = %v", err)
		}
		if len(secondPage.Entries) != 1 || secondPage.Entries[0].Snapshot.SourceVolumeId != "tank/z-volume" {
			t.Fatalf("unexpected second page: %+v", secondPage)
		}
	})

	t.Run("list all - empty when no filesystem configured", func(t *testing.T) {
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

func snapshotTimePtr(value int64) *int64 {
	return &value
}

func TestCSISnapshotCreationTimeRejectsUnknownValue(t *testing.T) {
	if _, err := csiSnapshotCreationTime(nil); status.Code(err) != codes.Internal {
		t.Fatalf("csiSnapshotCreationTime(nil) code = %v, want %v", status.Code(err), codes.Internal)
	}
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
		want   string
		offset int
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
		{name: "negative token", token: "-1", wantErr: true},
		{name: "trailing text", token: "1junk", wantErr: true},
		{name: "noncanonical token", token: "01", wantErr: true},
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

func TestListSnapshotsRejectsInvalidPagination(t *testing.T) {
	service := NewControllerService(&mockAPIClient{}, NewNodeRegistry(), "")
	tests := []struct {
		req  *csi.ListSnapshotsRequest
		name string
		code codes.Code
	}{
		{name: "negative max entries", req: &csi.ListSnapshotsRequest{MaxEntries: -1}, code: codes.InvalidArgument},
		{name: "malformed token", req: &csi.ListSnapshotsRequest{StartingToken: "1junk"}, code: codes.Aborted},
		{name: "negative token on snapshot fast path", req: &csi.ListSnapshotsRequest{SnapshotId: "nfs:tank/vol@snap", StartingToken: "-1"}, code: codes.Aborted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ListSnapshots(context.Background(), tt.req)
			if status.Code(err) != tt.code {
				t.Fatalf("ListSnapshots() code = %v, want %v (error: %v)", status.Code(err), tt.code, err)
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

func TestCreateVolumeFromSnapshot(t *testing.T) {
	ctx := context.Background()

	clonedSubvol := &nastyapi.Subvolume{
		Name:          "restored-volume",
		Filesystem:    "tank",
		SubvolumeType: subvolumeTypeFilesystem,
		Path:          "/tank/restored-volume",
		QuotaBytes:    uint64Ptr(MinVolumeSize),
		Properties:    map[string]string{},
		Snapshots:     []string{},
	}

	mockClient := &mockAPIClient{
		GetSubvolumeFunc: func(_ context.Context, filesystem, name string) (*nastyapi.Subvolume, error) {
			if filesystem == "tank" && name == "restored-volume" {
				return clonedSubvol, nil
			}
			return nil, nastyapi.ErrDatasetNotFound
		},
		CloneSnapshotFunc: func(_ context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error) {
			if params.Filesystem != "tank" || params.Subvolume != "source-vol" || params.Snapshot != "snap1" || params.NewName != "restored-volume" {
				t.Errorf("Unexpected clone params: %+v", params)
			}
			return clonedSubvol, nil
		},
		SetSubvolumePropertiesFunc: func(_ context.Context, filesystem, name string, props map[string]string) (*nastyapi.Subvolume, error) {
			return clonedSubvol, nil
		},
		ListNFSSharesFunc: func(_ context.Context) ([]nastyapi.NFSShare, error) {
			return []nastyapi.NFSShare{}, nil
		},
		CreateNFSShareFunc: func(_ context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
			return &nastyapi.NFSShare{
				ID:      "share-1",
				Path:    params.Path,
				Enabled: true,
			}, nil
		},
	}

	service := NewControllerService(mockClient, NewNodeRegistry(), "")

	req := &csi.CreateVolumeRequest{
		Name: "restored-volume",
		Parameters: map[string]string{
			"filesystem": "tank",
			"protocol":   "nfs",
		},
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
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1073741824,
		},
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: "nfs:tank/source-vol@snap1",
				},
			},
		},
	}

	resp, err := service.CreateVolume(ctx, req)
	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}
	if resp == nil || resp.Volume == nil {
		t.Fatal("Expected non-nil response with volume")
	}
	if resp.Volume.ContentSource == nil {
		t.Fatal("Expected ContentSource to be set on response")
	}
	if resp.Volume.ContentSource.GetSnapshot() == nil {
		t.Fatal("Expected ContentSource to contain snapshot source")
	}
	if resp.Volume.ContentSource.GetSnapshot().GetSnapshotId() != "nfs:tank/source-vol@snap1" {
		t.Errorf("Expected snapshot ID in content source, got %s", resp.Volume.ContentSource.GetSnapshot().GetSnapshotId())
	}
}
