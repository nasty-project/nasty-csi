package main

import (
	"context"
	"testing"

	"github.com/fenio/tns-csi/pkg/dashboard"
	"github.com/fenio/tns-csi/pkg/tnsapi"
)

func TestCheckNFSHealth(t *testing.T) {
	tests := []struct {
		nfsShareMap map[string]*tnsapi.NFSShare
		ds          *tnsapi.DatasetWithProperties
		wantShareOK *bool
		name        string
		wantIssues  int
	}{
		{
			name: "share found and enabled",
			ds: &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{ID: "tank/csi/pvc-1"},
				UserProperties: map[string]tnsapi.UserProperty{
					tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-1"},
				},
			},
			nfsShareMap: map[string]*tnsapi.NFSShare{
				"/mnt/tank/csi/pvc-1": {Path: "/mnt/tank/csi/pvc-1", Enabled: true, ID: 1},
			},
			wantShareOK: boolPtr(true),
			wantIssues:  0,
		},
		{
			name: "share found but disabled",
			ds: &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{ID: "tank/csi/pvc-2"},
				UserProperties: map[string]tnsapi.UserProperty{
					tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-2"},
				},
			},
			nfsShareMap: map[string]*tnsapi.NFSShare{
				"/mnt/tank/csi/pvc-2": {Path: "/mnt/tank/csi/pvc-2", Enabled: false, ID: 2},
			},
			wantShareOK: boolPtr(false),
			wantIssues:  1,
		},
		{
			name: "share not found",
			ds: &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{ID: "tank/csi/pvc-3"},
				UserProperties: map[string]tnsapi.UserProperty{
					tnsapi.PropertyNFSSharePath: {Value: "/mnt/tank/csi/pvc-3"},
				},
			},
			nfsShareMap: map[string]*tnsapi.NFSShare{},
			wantShareOK: boolPtr(false),
			wantIssues:  1,
		},
		{
			name: "share path not in properties",
			ds: &tnsapi.DatasetWithProperties{
				Dataset:        tnsapi.Dataset{ID: "tank/csi/pvc-4", Mountpoint: ""},
				UserProperties: map[string]tnsapi.UserProperty{
					// No PropertyNFSSharePath set
				},
			},
			nfsShareMap: map[string]*tnsapi.NFSShare{},
			wantShareOK: boolPtr(false),
			wantIssues:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := &VolumeHealth{
				Issues: make([]string, 0),
			}
			dashboard.CheckNFSHealth(tt.ds, tt.nfsShareMap, health)

			if len(health.Issues) != tt.wantIssues {
				t.Errorf("got %d issues, want %d; issues: %v", len(health.Issues), tt.wantIssues, health.Issues)
			}
			if tt.wantShareOK != nil {
				if health.ShareOK == nil {
					t.Fatal("ShareOK is nil, want non-nil")
				}
				if *health.ShareOK != *tt.wantShareOK {
					t.Errorf("ShareOK = %v, want %v", *health.ShareOK, *tt.wantShareOK)
				}
			}
		})
	}
}

func TestCheckNVMeOFHealth(t *testing.T) {
	tests := []struct {
		nvmeSubsysMap map[string]*tnsapi.NVMeOFSubsystem
		ds            *tnsapi.DatasetWithProperties
		wantSubsysOK  *bool
		name          string
		wantIssues    int
	}{
		{
			name: "subsystem found",
			ds: &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{ID: "tank/zvols/pvc-1"},
				UserProperties: map[string]tnsapi.UserProperty{
					tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2024.io.truenas:nvme:pvc-1"},
				},
			},
			nvmeSubsysMap: map[string]*tnsapi.NVMeOFSubsystem{
				"nqn.2024.io.truenas:nvme:pvc-1": {Name: "pvc-1", NQN: "nqn.2024.io.truenas:nvme:pvc-1", ID: 1},
			},
			wantSubsysOK: boolPtr(true),
			wantIssues:   0,
		},
		{
			name: "subsystem not found",
			ds: &tnsapi.DatasetWithProperties{
				Dataset: tnsapi.Dataset{ID: "tank/zvols/pvc-2"},
				UserProperties: map[string]tnsapi.UserProperty{
					tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2024.io.truenas:nvme:pvc-2"},
				},
			},
			nvmeSubsysMap: map[string]*tnsapi.NVMeOFSubsystem{},
			wantSubsysOK:  boolPtr(false),
			wantIssues:    1,
		},
		{
			name: "NQN not in properties",
			ds: &tnsapi.DatasetWithProperties{
				Dataset:        tnsapi.Dataset{ID: "tank/zvols/pvc-3"},
				UserProperties: map[string]tnsapi.UserProperty{
					// No PropertyNVMeSubsystemNQN set
				},
			},
			nvmeSubsysMap: map[string]*tnsapi.NVMeOFSubsystem{},
			wantSubsysOK:  boolPtr(false),
			wantIssues:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := &VolumeHealth{
				Issues: make([]string, 0),
			}
			dashboard.CheckNVMeOFHealth(tt.ds, tt.nvmeSubsysMap, health)

			if len(health.Issues) != tt.wantIssues {
				t.Errorf("got %d issues, want %d; issues: %v", len(health.Issues), tt.wantIssues, health.Issues)
			}
			if tt.wantSubsysOK != nil {
				if health.SubsysOK == nil {
					t.Fatal("SubsysOK is nil, want non-nil")
				}
				if *health.SubsysOK != *tt.wantSubsysOK {
					t.Errorf("SubsysOK = %v, want %v", *health.SubsysOK, *tt.wantSubsysOK)
				}
			}
		})
	}
}

func TestCheckVolumeHealth(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		setupMock        func(*mockClient)
		name             string
		wantTotal        int
		wantHealthy      int
		wantUnhealthy    int
		wantProblemCount int
		wantErr          bool
	}{
		{
			name: "empty datasets yields empty report",
			setupMock: func(m *mockClient) {
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{}, nil
				}
				m.QueryAllNFSSharesFunc = func(_ context.Context, _ string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{}, nil
				}
				m.ListAllNVMeOFSubsystemsFunc = func(_ context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{}, nil
				}
			},
			wantErr:          false,
			wantTotal:        0,
			wantHealthy:      0,
			wantUnhealthy:    0,
			wantProblemCount: 0,
		},
		{
			name: "mix of healthy and unhealthy volumes",
			setupMock: func(m *mockClient) {
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return []tnsapi.DatasetWithProperties{
						{
							// Healthy NFS volume
							Dataset: tnsapi.Dataset{ID: "tank/csi/pvc-healthy"},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:     {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName: {Value: "pvc-healthy"},
								tnsapi.PropertyProtocol:      {Value: "nfs"},
								tnsapi.PropertyNFSSharePath:  {Value: "/mnt/tank/csi/pvc-healthy"},
							},
						},
						{
							// Unhealthy NFS volume (share missing)
							Dataset: tnsapi.Dataset{ID: "tank/csi/pvc-unhealthy"},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:     {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName: {Value: "pvc-unhealthy"},
								tnsapi.PropertyProtocol:      {Value: "nfs"},
								tnsapi.PropertyNFSSharePath:  {Value: "/mnt/tank/csi/pvc-unhealthy"},
							},
						},
						{
							// Healthy NVMe-oF volume
							Dataset: tnsapi.Dataset{ID: "tank/zvols/pvc-nvme"},
							UserProperties: map[string]tnsapi.UserProperty{
								tnsapi.PropertyManagedBy:        {Value: tnsapi.ManagedByValue},
								tnsapi.PropertyCSIVolumeName:    {Value: "pvc-nvme"},
								tnsapi.PropertyProtocol:         {Value: "nvmeof"},
								tnsapi.PropertyNVMeSubsystemNQN: {Value: "nqn.2024.io.truenas:nvme:pvc-nvme"},
							},
						},
					}, nil
				}
				m.QueryAllNFSSharesFunc = func(_ context.Context, _ string) ([]tnsapi.NFSShare, error) {
					return []tnsapi.NFSShare{
						{Path: "/mnt/tank/csi/pvc-healthy", Enabled: true, ID: 1},
						// pvc-unhealthy share is missing
					}, nil
				}
				m.ListAllNVMeOFSubsystemsFunc = func(_ context.Context) ([]tnsapi.NVMeOFSubsystem, error) {
					return []tnsapi.NVMeOFSubsystem{
						{Name: "pvc-nvme", NQN: "nqn.2024.io.truenas:nvme:pvc-nvme", ID: 10},
					}, nil
				}
			},
			wantErr:          false,
			wantTotal:        3,
			wantHealthy:      2,
			wantUnhealthy:    1,
			wantProblemCount: 1,
		},
		{
			name: "API error propagates",
			setupMock: func(m *mockClient) {
				m.FindDatasetsByPropertyFunc = func(_ context.Context, _, _, _ string) ([]tnsapi.DatasetWithProperties, error) {
					return nil, errNotImplemented
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &mockClient{}
			tt.setupMock(mc)

			report, err := dashboard.CheckVolumeHealth(ctx, mc)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if report.Summary.TotalVolumes != tt.wantTotal {
				t.Errorf("TotalVolumes = %d, want %d", report.Summary.TotalVolumes, tt.wantTotal)
			}
			if report.Summary.HealthyVolumes != tt.wantHealthy {
				t.Errorf("HealthyVolumes = %d, want %d", report.Summary.HealthyVolumes, tt.wantHealthy)
			}
			if report.Summary.UnhealthyVolumes != tt.wantUnhealthy {
				t.Errorf("UnhealthyVolumes = %d, want %d", report.Summary.UnhealthyVolumes, tt.wantUnhealthy)
			}
			if len(report.Problems) != tt.wantProblemCount {
				t.Errorf("Problems count = %d, want %d", len(report.Problems), tt.wantProblemCount)
			}
		})
	}
}

// boolPtr returns a pointer to a bool value.
func boolPtr(v bool) *bool {
	return &v
}
