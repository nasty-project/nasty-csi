package main

import (
	"context"
	"testing"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	nastyapi "github.com/nasty-project/nasty-go"
)

func TestFindManagedVolumes(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		setupMock func(*mockClient)
		checkVols func(*testing.T, []VolumeInfo)
		name      string
		wantCount int
		wantErr   bool
	}{
		{
			name: "empty result",
			setupMock: func(m *mockClient) {
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{}, nil
				}
			},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "returns only subvolumes with csi volume name",
			setupMock: func(m *mockClient) {
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{
						{
							Name:          "csi/pvc-111",
							Pool:          "tank",
							SubvolumeType: "filesystem",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:      nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName:  "pvc-111",
								nastyapi.PropertyProtocol:       "nfs",
								nastyapi.PropertyCapacityBytes:  "1073741824",
								nastyapi.PropertyDeleteStrategy: "delete",
							},
						},
						{
							// Unmanaged / parent subvolume (no CSI volume name)
							Name:          "csi",
							Pool:          "tank",
							SubvolumeType: "filesystem",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
							},
						},
					}, nil
				}
			},
			wantCount: 1,
			wantErr:   false,
			checkVols: func(t *testing.T, vols []VolumeInfo) {
				t.Helper()
				if vols[0].VolumeID != "pvc-111" {
					t.Errorf("VolumeID = %q, want %q", vols[0].VolumeID, "pvc-111")
				}
				if vols[0].Protocol != "nfs" {
					t.Errorf("Protocol = %q, want %q", vols[0].Protocol, "nfs")
				}
			},
		},
		{
			name: "skip detached snapshots",
			setupMock: func(m *mockClient) {
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{
						{
							Name:          "csi/pvc-222",
							Pool:          "tank",
							SubvolumeType: "filesystem",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:        nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName:    "pvc-222",
								nastyapi.PropertyProtocol:         "nfs",
								nastyapi.PropertyDetachedSnapshot: "true",
							},
						},
						{
							Name:          "csi/pvc-333",
							Pool:          "tank",
							SubvolumeType: "filesystem",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName: "pvc-333",
								nastyapi.PropertyProtocol:      "nfs",
							},
						},
					}, nil
				}
			},
			wantCount: 1,
			wantErr:   false,
			checkVols: func(t *testing.T, vols []VolumeInfo) {
				t.Helper()
				if vols[0].VolumeID != "pvc-333" {
					t.Errorf("VolumeID = %q, want %q", vols[0].VolumeID, "pvc-333")
				}
			},
		},
		{
			name: "skip subvolumes without volume ID",
			setupMock: func(m *mockClient) {
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{
						{
							Name:          "csi/parent",
							Pool:          "tank",
							SubvolumeType: "filesystem",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
								// No PropertyCSIVolumeName
							},
						},
						{
							Name:          "csi/pvc-444",
							Pool:          "tank",
							SubvolumeType: "filesystem",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName: "",
							},
						},
					}, nil
				}
			},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "property extraction",
			setupMock: func(m *mockClient) {
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{
						{
							Name:          "zvols/pvc-555",
							Pool:          "tank",
							SubvolumeType: "block",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:         nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName:     "pvc-555",
								nastyapi.PropertyProtocol:          "nvmeof",
								nastyapi.PropertyCapacityBytes:     "10737418240",
								nastyapi.PropertyDeleteStrategy:    "retain",
								nastyapi.PropertyAdoptable:         "true",
								nastyapi.PropertyContentSourceType: "snapshot",
								nastyapi.PropertyContentSourceID:   "snap-abc",
							},
						},
					}, nil
				}
			},
			wantCount: 1,
			wantErr:   false,
			checkVols: func(t *testing.T, vols []VolumeInfo) {
				t.Helper()
				v := vols[0]
				if v.Dataset != "tank/zvols/pvc-555" {
					t.Errorf("Dataset = %q, want %q", v.Dataset, "tank/zvols/pvc-555")
				}
				if v.VolumeID != "pvc-555" {
					t.Errorf("VolumeID = %q, want %q", v.VolumeID, "pvc-555")
				}
				if v.Protocol != "nvmeof" {
					t.Errorf("Protocol = %q, want %q", v.Protocol, "nvmeof")
				}
				if v.CapacityBytes != 10737418240 {
					t.Errorf("CapacityBytes = %d, want %d", v.CapacityBytes, 10737418240)
				}
				if v.CapacityHuman != "10.0Gi" {
					t.Errorf("CapacityHuman = %q, want %q", v.CapacityHuman, "10.0Gi")
				}
				if v.DeleteStrategy != "retain" {
					t.Errorf("DeleteStrategy = %q, want %q", v.DeleteStrategy, "retain")
				}
				if !v.Adoptable {
					t.Error("Adoptable = false, want true")
				}
				if v.ContentSourceType != "snapshot" {
					t.Errorf("ContentSourceType = %q, want %q", v.ContentSourceType, "snapshot")
				}
				if v.ContentSourceID != "snap-abc" {
					t.Errorf("ContentSourceID = %q, want %q", v.ContentSourceID, "snap-abc")
				}
				if v.Type != "block" {
					t.Errorf("Type = %q, want %q", v.Type, "block")
				}
			},
		},
		{
			name: "API error propagates",
			setupMock: func(m *mockClient) {
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return nil, errNotImplemented
				}
			},
			wantCount: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &mockClient{}
			tt.setupMock(mc)

			volumes, err := dashboard.FindManagedVolumes(ctx, mc, "")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(volumes) != tt.wantCount {
				t.Fatalf("got %d volumes, want %d", len(volumes), tt.wantCount)
			}
			if tt.checkVols != nil {
				tt.checkVols(t, volumes)
			}
		})
	}
}
