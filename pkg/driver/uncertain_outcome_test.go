package driver

import (
	"context"
	"fmt"
	"testing"

	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUncertainCreateOutcomeIsRetryableThroughReconciliation(t *testing.T) {
	err := createVolumeError("creation", fmt.Errorf("wrapped: %w", nastyapi.ErrUncertainOutcome))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("uncertain create outcome: %v", err)
	}
}

func TestUncertainDeletionDoesNotReplayFirstMutation(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			client := &MockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue}}, nil
				},
				ListNFSSharesFunc:       func(context.Context) ([]nastyapi.NFSShare, error) { return nil, nil },
				GetISCSITargetByIQNFunc: func(context.Context, string) (*nastyapi.ISCSITarget, error) { return nil, nastyapi.ErrDatasetNotFound },
				GetNVMeOFSubsystemByNQNFunc: func(context.Context, string) (*nastyapi.NVMeOFSubsystem, error) {
					return nil, nastyapi.ErrDatasetNotFound
				},
				DeleteSubvolumeFunc: func(context.Context, string, string) error { calls++; return nastyapi.ErrUncertainOutcome },
			}
			s := &ControllerService{apiClient: client}
			meta := &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"}
			var err error
			switch protocol {
			case ProtocolNFS:
				_, err = s.deleteNFSVolume(t.Context(), meta)
			case ProtocolISCSI:
				_, err = s.deleteISCSIVolume(t.Context(), meta)
			default:
				_, err = s.deleteNVMeOFVolume(t.Context(), meta)
			}
			if status.Code(err) != codes.Unavailable || calls != 1 {
				t.Fatalf("uncertain deletion calls=%d, err=%v", calls, err)
			}
		})
	}
}
