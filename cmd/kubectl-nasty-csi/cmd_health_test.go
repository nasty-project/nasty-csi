package main

import (
	"context"
	"testing"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	"github.com/nasty-project/nasty-csi/pkg/nasty-api"
)

func TestCheckNFSHealth(t *testing.T) {
	tests := []struct {
		nfsShareMap map[string]*nastyapi.NFSShare
		sv          *nastyapi.Subvolume
		wantShareOK *bool
		name        string
		wantIssues  int
	}{
		{
			name: "share found and enabled",
			sv: &nastyapi.Subvolume{
				Name: "csi/pvc-1",
				Pool: "tank",
				Path: "/tank/csi/pvc-1",
				Properties: map[string]string{
					nastyapi.PropertyNFSSharePath: "/mnt/tank/csi/pvc-1",
				},
			},
			nfsShareMap: map[string]*nastyapi.NFSShare{
				"/mnt/tank/csi/pvc-1": {Path: "/mnt/tank/csi/pvc-1", Enabled: true, ID: "1"},
			},
			wantShareOK: boolPtr(true),
			wantIssues:  0,
		},
		{
			name: "share found but disabled",
			sv: &nastyapi.Subvolume{
				Name: "csi/pvc-2",
				Pool: "tank",
				Path: "/tank/csi/pvc-2",
				Properties: map[string]string{
					nastyapi.PropertyNFSSharePath: "/mnt/tank/csi/pvc-2",
				},
			},
			nfsShareMap: map[string]*nastyapi.NFSShare{
				"/mnt/tank/csi/pvc-2": {Path: "/mnt/tank/csi/pvc-2", Enabled: false, ID: "2"},
			},
			wantShareOK: boolPtr(false),
			wantIssues:  1,
		},
		{
			name: "share not found",
			sv: &nastyapi.Subvolume{
				Name: "csi/pvc-3",
				Pool: "tank",
				Path: "/tank/csi/pvc-3",
				Properties: map[string]string{
					nastyapi.PropertyNFSSharePath: "/mnt/tank/csi/pvc-3",
				},
			},
			nfsShareMap: map[string]*nastyapi.NFSShare{},
			wantShareOK: boolPtr(false),
			wantIssues:  1,
		},
		{
			name: "share path not in properties",
			sv: &nastyapi.Subvolume{
				Name:       "csi/pvc-4",
				Pool:       "tank",
				Path:       "",
				Properties: map[string]string{
					// No PropertyNFSSharePath set
				},
			},
			nfsShareMap: map[string]*nastyapi.NFSShare{},
			wantShareOK: boolPtr(false),
			wantIssues:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := &VolumeHealth{
				Issues: make([]string, 0),
			}
			dashboard.CheckNFSHealth(tt.sv, tt.nfsShareMap, health)

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
		nvmeSubsysMap map[string]*nastyapi.NVMeOFSubsystem
		sv            *nastyapi.Subvolume
		wantSubsysOK  *bool
		name          string
		wantIssues    int
	}{
		{
			name: "subsystem found",
			sv: &nastyapi.Subvolume{
				Name: "zvols/pvc-1",
				Pool: "tank",
				Properties: map[string]string{
					nastyapi.PropertyNVMeSubsystemNQN: "nqn.2024.io.nasty:nvme:pvc-1",
				},
			},
			nvmeSubsysMap: map[string]*nastyapi.NVMeOFSubsystem{
				"nqn.2024.io.nasty:nvme:pvc-1": {ID: "1", NQN: "nqn.2024.io.nasty:nvme:pvc-1"},
			},
			wantSubsysOK: boolPtr(true),
			wantIssues:   0,
		},
		{
			name: "subsystem not found",
			sv: &nastyapi.Subvolume{
				Name: "zvols/pvc-2",
				Pool: "tank",
				Properties: map[string]string{
					nastyapi.PropertyNVMeSubsystemNQN: "nqn.2024.io.nasty:nvme:pvc-2",
				},
			},
			nvmeSubsysMap: map[string]*nastyapi.NVMeOFSubsystem{},
			wantSubsysOK:  boolPtr(false),
			wantIssues:    1,
		},
		{
			name: "NQN not in properties",
			sv: &nastyapi.Subvolume{
				Name:       "zvols/pvc-3",
				Pool:       "tank",
				Properties: map[string]string{
					// No PropertyNVMeSubsystemNQN set
				},
			},
			nvmeSubsysMap: map[string]*nastyapi.NVMeOFSubsystem{},
			wantSubsysOK:  boolPtr(false),
			wantIssues:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := &VolumeHealth{
				Issues: make([]string, 0),
			}
			dashboard.CheckNVMeOFHealth(tt.sv, tt.nvmeSubsysMap, health)

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
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{}, nil
				}
				m.ListNFSSharesFunc = func(_ context.Context) ([]nastyapi.NFSShare, error) {
					return []nastyapi.NFSShare{}, nil
				}
				m.ListNVMeOFSubsystemsFunc = func(_ context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
					return []nastyapi.NVMeOFSubsystem{}, nil
				}
				m.ListSMBSharesFunc = func(_ context.Context) ([]nastyapi.SMBShare, error) {
					return []nastyapi.SMBShare{}, nil
				}
				m.ListISCSITargetsFunc = func(_ context.Context) ([]nastyapi.ISCSITarget, error) {
					return []nastyapi.ISCSITarget{}, nil
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
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
					return []nastyapi.Subvolume{
						{
							// Healthy NFS volume
							Name: "csi/pvc-healthy",
							Pool: "tank",
							Path: "/mnt/tank/csi/pvc-healthy",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName: "pvc-healthy",
								nastyapi.PropertyProtocol:      "nfs",
								nastyapi.PropertyNFSSharePath:  "/mnt/tank/csi/pvc-healthy",
							},
						},
						{
							// Unhealthy NFS volume (share missing)
							Name: "csi/pvc-unhealthy",
							Pool: "tank",
							Path: "/mnt/tank/csi/pvc-unhealthy",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName: "pvc-unhealthy",
								nastyapi.PropertyProtocol:      "nfs",
								nastyapi.PropertyNFSSharePath:  "/mnt/tank/csi/pvc-unhealthy",
							},
						},
						{
							// Healthy NVMe-oF volume
							Name: "zvols/pvc-nvme",
							Pool: "tank",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:        nastyapi.ManagedByValue,
								nastyapi.PropertyCSIVolumeName:    "pvc-nvme",
								nastyapi.PropertyProtocol:         "nvmeof",
								nastyapi.PropertyNVMeSubsystemNQN: "nqn.2024.io.nasty:nvme:pvc-nvme",
							},
						},
					}, nil
				}
				m.ListNFSSharesFunc = func(_ context.Context) ([]nastyapi.NFSShare, error) {
					return []nastyapi.NFSShare{
						{Path: "/mnt/tank/csi/pvc-healthy", Enabled: true, ID: "1"},
						// pvc-unhealthy share is missing
					}, nil
				}
				m.ListNVMeOFSubsystemsFunc = func(_ context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
					return []nastyapi.NVMeOFSubsystem{
						{ID: "10", NQN: "nqn.2024.io.nasty:nvme:pvc-nvme"},
					}, nil
				}
				m.ListSMBSharesFunc = func(_ context.Context) ([]nastyapi.SMBShare, error) {
					return []nastyapi.SMBShare{}, nil
				}
				m.ListISCSITargetsFunc = func(_ context.Context) ([]nastyapi.ISCSITarget, error) {
					return []nastyapi.ISCSITarget{}, nil
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
				m.FindSubvolumesByPropertyFunc = func(_ context.Context, _, _, _ string) ([]nastyapi.Subvolume, error) {
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
