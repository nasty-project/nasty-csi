package driver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeGetVolumeHealth(t *testing.T) {
	path := t.TempDir()
	tests := []struct {
		name     string
		req      *csi.NodeGetVolumeHealthRequest
		code     codes.Code
		abnormal bool
	}{
		{name: "missing ID", req: &csi.NodeGetVolumeHealthRequest{}, code: codes.InvalidArgument},
		{name: "missing optional paths", req: &csi.NodeGetVolumeHealthRequest{VolumeId: "tank/claim"}, abnormal: true},
		{name: "relative publish path", req: &csi.NodeGetVolumeHealthRequest{VolumeId: "tank/claim", VolumePublishPath: "relative"}, code: codes.InvalidArgument},
		{name: "relative staging path", req: &csi.NodeGetVolumeHealthRequest{VolumeId: "tank/claim", VolumePublishPath: path, StagingTargetPath: "relative"}, code: codes.InvalidArgument},
		{name: "healthy published path", req: &csi.NodeGetVolumeHealthRequest{VolumeId: "tank/claim", VolumePublishPath: path}},
		{name: "healthy staging fallback", req: &csi.NodeGetVolumeHealthRequest{VolumeId: "tank/claim", StagingTargetPath: path}},
		{name: "missing published path reports health", req: &csi.NodeGetVolumeHealthRequest{VolumeId: "tank/claim", VolumePublishPath: filepath.Join(path, "missing"), StagingTargetPath: path}, abnormal: true},
	}
	service := NewNodeService("test-node", nil, true, nil, false, 5)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := service.NodeGetVolumeHealth(context.Background(), tt.req)
			if status.Code(err) != tt.code {
				t.Fatalf("code = %v, want %v: %v", status.Code(err), tt.code, err)
			}
			if err != nil {
				return
			}
			if resp.VolumeHealth == nil || resp.VolumeHealth.VolumeId != tt.req.VolumeId {
				t.Fatalf("unexpected response: %v", resp)
			}
			if (len(resp.VolumeHealth.HealthStatuses) != 0) != tt.abnormal {
				t.Fatalf("unexpected health: %v", resp.VolumeHealth)
			}
		})
	}
}

func TestControllerGetVolumeHealth(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolNVMeOF, ProtocolISCSI, ProtocolSMB} {
		for _, abnormal := range []bool{false, true} {
			name := protocol + "/healthy"
			if abnormal {
				name = protocol + "/query-failure"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				client := &mockAPIClient{
					GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
						calls++
						if abnormal && calls > 1 {
							return nil, errors.New("backend health query failed")
						}
						return &nastyapi.Subvolume{
							Filesystem: "tank", Name: "claim",
							Properties: map[string]string{
								nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
								nastyapi.PropertyProtocol:      protocol,
								nastyapi.PropertyCapacityBytes: "1073741824",
							},
						}, nil
					},
				}
				service := NewControllerService(client, NewNodeRegistry(), "")
				resp, err := service.ControllerGetVolumeHealth(context.Background(), &csi.ControllerGetVolumeHealthRequest{VolumeId: "tank/claim"})
				if err != nil {
					t.Fatal(err)
				}
				if resp.VolumeHealth == nil || resp.VolumeHealth.VolumeId != "tank/claim" {
					t.Fatalf("unexpected response: %v", resp)
				}
				if (len(resp.VolumeHealth.HealthStatuses) != 0) != abnormal {
					t.Fatalf("unexpected health: %v", resp.VolumeHealth)
				}
				if abnormal && resp.VolumeHealth.HealthStatuses[0].Message == "" {
					t.Fatal("health failure must preserve the diagnostic message")
				}
			})
		}
	}
}

func TestControllerVolumeHealthValidation(t *testing.T) {
	service := NewControllerService(&mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			return nil, nastyapi.ErrDatasetNotFound
		},
	}, NewNodeRegistry(), "")
	for _, tt := range []struct {
		id   string
		code codes.Code
	}{{"", codes.InvalidArgument}, {"tank/missing", codes.NotFound}} {
		_, err := service.ControllerGetVolumeHealth(context.Background(), &csi.ControllerGetVolumeHealthRequest{VolumeId: tt.id})
		if status.Code(err) != tt.code {
			t.Errorf("ID %q: code = %v, want %v", tt.id, status.Code(err), tt.code)
		}
	}
	resp, err := service.ControllerGetCapabilities(context.Background(), &csi.ControllerGetCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, capability := range resp.Capabilities {
		if capability.GetRpc().GetType() == csi.ControllerServiceCapability_RPC_GET_VOLUME_HEALTH {
			found = true
		}
		if capability.GetRpc().GetType() == csi.ControllerServiceCapability_RPC_LIST_VOLUME_HEALTH {
			t.Fatal("must not advertise unimplemented health listing")
		}
	}
	if !found {
		t.Fatal("GET_VOLUME_HEALTH must be advertised")
	}
}
