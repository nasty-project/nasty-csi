package main

import (
	"context"
	"testing"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	"github.com/nasty-project/nasty-csi/pkg/tnsapi"
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
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{}, nil
				}
			},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "returns only datasets with csi volume name",
			setupMock: func(m *mockClient) {
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{
						{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/pvc-111",
								Name: "tank/csi/pvc-111",
								Type: "FILESYSTEM",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:      {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName:  {Value: "pvc-111"},
								tnsapi.PropertyProtocol:       {Value: "nfs"},
								tnsapi.PropertyCapacityBytes:  {Value: "1073741824"},
								tnsapi.PropertyDeleteStrategy: {Value: "delete"},
							},
						},
						{
							// Unmanaged / parent dataset (no CSI volume name)
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi",
								Name: "tank/csi",
								Type: "FILESYSTEM",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
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
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{
						{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/pvc-222",
								Name: "tank/csi/pvc-222",
								Type: "FILESYSTEM",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName:    {Value: "pvc-222"},
								tnsapi.PropertyProtocol:         {Value: "nfs"},
								tnsapi.PropertyDetachedSnapshot: {Value: "true"},
							},
						},
						{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/pvc-333",
								Name: "tank/csi/pvc-333",
								Type: "FILESYSTEM",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:     {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName: {Value: "pvc-333"},
								tnsapi.PropertyProtocol:      {Value: "nfs"},
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
			name: "skip datasets without volume ID",
			setupMock: func(m *mockClient) {
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{
						{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/parent",
								Name: "tank/csi/parent",
								Type: "FILESYSTEM",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy: {Value: tnsapi.ManagedByValue},
								// No PropertyCSIVolumeName
							},
						},
						{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/pvc-444",
								Name: "tank/csi/pvc-444",
								Type: "FILESYSTEM",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:     {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName: {Value: ""},
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
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{
						{
							Dataset: tnsapi.Dataset{
								ID:   "tank/csi/pvc-555",
								Name: "tank/csi/pvc-555",
								Type: "VOLUME",
							},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:         {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName:     {Value: "pvc-555"},
								tnsapi.PropertyProtocol:          {Value: "nvmeof"},
								tnsapi.PropertyCapacityBytes:     {Value: "10737418240"},
								tnsapi.PropertyDeleteStrategy:    {Value: "retain"},
								tnsapi.PropertyAdoptable:         {Value: "true"},
								tnsapi.PropertyContentSourceType: {Value: "snapshot"},
								tnsapi.PropertyContentSourceID:   {Value: "snap-abc"},
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
				if v.Dataset != "tank/csi/pvc-555" {
					t.Errorf("Dataset = %q, want %q", v.Dataset, "tank/csi/pvc-555")
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
				if v.Type != "VOLUME" {
					t.Errorf("Type = %q, want %q", v.Type, "VOLUME")
				}
			},
		},
		{
			name: "API error propagates",
			setupMock: func(m *mockClient) {
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
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
