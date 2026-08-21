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

func TestExistingSMBShareRequiresSelectedBackendName(t *testing.T) {
	subvol := &nastyapi.Subvolume{
		Filesystem: "tank",
		Name:       "claim",
		Path:       "/fs/tank/claim",
		Properties: map[string]string{
			nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
			nastyapi.PropertyCSIVolumeName: "claim",
			nastyapi.PropertyProtocol:      ProtocolSMB,
			propertyCSIRequestName:         "claim",
			propertyVolumeUUID:             "12345678-1234-4123-8123-123456789abc",
			propertyIdentityVersion:        identityVersion2,
		},
	}
	client := &mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) { return subvol, nil },
		ListSMBSharesFunc: func(context.Context) ([]nastyapi.SMBShare, error) {
			return []nastyapi.SMBShare{{ID: "wrong", Name: "another-claim", Path: subvol.Path}}, nil
		},
		CreateSMBShareFunc: func(context.Context, nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
			t.Fatal("wrong-name path match must not create another SMB share")
			return nil, errors.New("unexpected share creation")
		},
		SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
			t.Fatal("wrong-name path match must fail before property mutation")
			return nil, errors.New("unexpected property mutation")
		},
	}
	controller := NewControllerService(client, NewNodeRegistry(), "")
	if _, err := controller.createSMBVolume(context.Background(), identityTestRequest(ProtocolSMB, false)); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("wrong-name SMB retry code=%v, want FailedPrecondition (error: %v)", status.Code(err), err)
	}
}
