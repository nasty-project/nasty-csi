package driver

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDeletionLookupAbsenceAndReadErrors(t *testing.T) {
	for _, tt := range []struct {
		err    error
		subvol *nastyapi.Subvolume
		name   string
		code   codes.Code
	}{
		{name: "successful empty lookup", code: codes.OK},
		{name: "not found", err: nastyapi.ErrDatasetNotFound, code: codes.OK},
		{name: "wrapped not found", err: fmt.Errorf("lookup: %w", nastyapi.ErrDatasetNotFound), code: codes.OK},
		{name: "not found with stale object", subvol: &nastyapi.Subvolume{}, err: nastyapi.ErrDatasetNotFound, code: codes.OK},
		{name: "read failure", err: errors.New("connection lost"), code: codes.Unavailable},
		{name: "uncertain outcome", err: nastyapi.ErrUncertainOutcome, code: codes.Unavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &MockAPIClient{GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
				return tt.subvol, tt.err
			}}
			s := &ControllerService{apiClient: client}
			meta, err := s.lookupDeletionVolume(t.Context(), "tank/test")
			if status.Code(err) != tt.code || meta != nil {
				t.Fatalf("lookup returned metadata %v, error %v; expected code %v", meta, err, tt.code)
			}
		})
	}
}

func TestStrictDeletionOwnership(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		for _, tt := range []struct {
			props map[string]string
			name  string
		}{
			{nil, "missing properties"},
			{map[string]string{nastyapi.PropertyProtocol: protocol}, "missing marker"},
			{map[string]string{nastyapi.PropertyManagedBy: "someone-else"}, "foreign marker"},
			{map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue, nastyapi.PropertyCSIVolumeName: "other"}, "wrong name"},
			{map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue, nastyapi.PropertyProtocol: "other"}, "wrong protocol"},
			{map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue, nastyapi.PropertyDeleteStrategy: "typo"}, "unknown strategy"},
		} {
			t.Run(protocol+"/"+tt.name, func(t *testing.T) {
				client := &MockAPIClient{GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Filesystem: "tank", Name: "test", Properties: tt.props}, nil
				}}
				s := &ControllerService{apiClient: client}
				_, err := safetyDeleter(s, protocol)(context.Background(), &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"})
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("unsafe metadata permitted deletion: %v", err)
				}
				// Top-level CSI lookup must not turn unmanaged objects into absence.
				_, err = s.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "tank/test"})
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("CSI DeleteVolume hid unsafe metadata: %v", err)
				}
			})
		}
	}
}

func TestDeletionMetadataRecoveryPreservesRetentionPolicy(t *testing.T) {
	var props map[string]string
	client := &MockAPIClient{GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
		return &nastyapi.Subvolume{Filesystem: "tank", Name: "test", Properties: props}, nil
	}}
	s := &ControllerService{apiClient: client}
	req := &csi.DeleteVolumeRequest{VolumeId: "tank/test"}
	if _, err := s.DeleteVolume(context.Background(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("missing ownership: %v", err)
	}
	if props != nil {
		t.Fatal("deletion automatically adopted the object")
	}
	// Simulate explicitly reviewed operator recovery, preserving the intended
	// retain policy. Every presentation/mutation mock remains forbidden.
	props = map[string]string{
		nastyapi.PropertyManagedBy:      nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName:  "test",
		nastyapi.PropertyProtocol:       ProtocolNFS,
		nastyapi.PropertyDeleteStrategy: nastyapi.DeleteStrategyRetain,
	}
	if _, err := s.DeleteVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}
