package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
)

func TestCreateSMBVolumePreservesPreExistingSubvolumeOnShareFailure(t *testing.T) {
	client := &mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			return &nastyapi.Subvolume{
				Filesystem: "tank",
				Name:       "test-smb-volume",
				Path:       "/fs/tank/test-smb-volume",
			}, nil
		},
		ListSMBSharesFunc: func(context.Context) ([]nastyapi.SMBShare, error) {
			return nil, nil
		},
		CreateSMBShareFunc: func(context.Context, nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
			return nil, errors.New("SMB service unavailable")
		},
		DeleteSubvolumeFunc: func(context.Context, string, string) error {
			t.Fatal("pre-existing subvolume must not be deleted")
			return nil
		},
	}
	controller := NewControllerService(client, NewNodeRegistry(), "")
	request := &csi.CreateVolumeRequest{
		Name: "test-smb-volume",
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		}},
		Parameters: map[string]string{
			"protocol":   ProtocolSMB,
			"filesystem": "tank",
			"server":     "192.0.2.1",
		},
	}

	if _, err := controller.createSMBVolume(context.Background(), request); err == nil {
		t.Fatal("expected SMB share creation failure")
	}
}
