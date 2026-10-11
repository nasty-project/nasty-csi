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

type volumeDeleter func(context.Context, *VolumeMetadata) (*csi.DeleteVolumeResponse, error)

func safetyDeleter(s *ControllerService, protocol string) volumeDeleter {
	switch protocol {
	case ProtocolNFS:
		return s.deleteNFSVolume
	case ProtocolSMB:
		return s.deleteSMBVolume
	case ProtocolISCSI:
		return s.deleteISCSIVolume
	default:
		return s.deleteNVMeOFVolume
	}
}

func TestDeleteFailsClosedOnOwnershipReadFailure(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			client := &MockAPIClient{GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
				return nil, errors.New("ownership query timed out")
			}}
			// All other mock methods panic: no lookup or mutation is allowed.
			s := &ControllerService{apiClient: client}
			_, err := safetyDeleter(s, protocol)(context.Background(), &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"})
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("ownership query failure must be retryable: %v", err)
			}
		})
	}
}

func TestDeleteRetainedVolumeDoesNotTouchPresentation(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			client := &MockAPIClient{GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
				return &nastyapi.Subvolume{Properties: map[string]string{
					nastyapi.PropertyManagedBy:      nastyapi.ManagedByValue,
					nastyapi.PropertyCSIVolumeName:  "test",
					nastyapi.PropertyDeleteStrategy: nastyapi.DeleteStrategyRetain,
				}}, nil
			}}
			s := &ControllerService{apiClient: client}
			if _, err := safetyDeleter(s, protocol)(context.Background(), &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeletePreservesStorageOnPresentationQueryFailure(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			queryErr := errors.New("presentation query timed out")
			client := &MockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue}}, nil
				},
				ListNFSSharesFunc:           func(context.Context) ([]nastyapi.NFSShare, error) { return nil, queryErr },
				ListSMBSharesFunc:           func(context.Context) ([]nastyapi.SMBShare, error) { return nil, queryErr },
				GetISCSITargetByIQNFunc:     func(context.Context, string) (*nastyapi.ISCSITarget, error) { return nil, queryErr },
				GetNVMeOFSubsystemByNQNFunc: func(context.Context, string) (*nastyapi.NVMeOFSubsystem, error) { return nil, queryErr },
			}
			s := &ControllerService{apiClient: client}
			_, err := safetyDeleter(s, protocol)(context.Background(), &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"})
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("presentation query failure must preserve storage for retry: %v", err)
			}
		})
	}
}

func TestDeletePreservesStorageOnShareDeleteFailure(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB} {
		for _, byID := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/path", true: "/id"}[byID], func(t *testing.T) {
				cleanupErr := errors.New("share deletion timed out")
				client := &MockAPIClient{
					GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
						return &nastyapi.Subvolume{Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue}}, nil
					},
					ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
						return []nastyapi.NFSShare{{ID: "share", Path: "/fs/tank/test"}}, nil
					},
					ListSMBSharesFunc: func(context.Context) ([]nastyapi.SMBShare, error) {
						return []nastyapi.SMBShare{{ID: "share", Path: "/fs/tank/test"}}, nil
					},
					DeleteNFSShareFunc: func(context.Context, string) error { return cleanupErr },
					DeleteSMBShareFunc: func(context.Context, string) error { return cleanupErr },
				}
				meta := &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"}
				if byID {
					meta.NFSShareUUID, meta.SMBShareUUID = "share", "share"
				}
				s := &ControllerService{apiClient: client}
				_, err := safetyDeleter(s, protocol)(context.Background(), meta)
				if status.Code(err) != codes.Unavailable {
					t.Fatalf("share delete failure must preserve storage for retry: %v", err)
				}
			})
		}
	}
}

func TestDeleteRetriesAfterTargetQueryFailure(t *testing.T) {
	for _, protocol := range []string{ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			unavailable := true
			storageDeletes, targetDeletes := 0, 0
			client := &MockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Properties: map[string]string{nastyapi.PropertyManagedBy: nastyapi.ManagedByValue}}, nil
				},
				GetISCSITargetByIQNFunc: func(context.Context, string) (*nastyapi.ISCSITarget, error) {
					if unavailable {
						return nil, errors.New("query timed out")
					}
					return &nastyapi.ISCSITarget{ID: "target"}, nil
				},
				GetNVMeOFSubsystemByNQNFunc: func(context.Context, string) (*nastyapi.NVMeOFSubsystem, error) {
					if unavailable {
						return nil, errors.New("query timed out")
					}
					return &nastyapi.NVMeOFSubsystem{ID: "target"}, nil
				},
				DeleteISCSITargetFunc:     func(context.Context, string) error { targetDeletes++; return nil },
				DeleteNVMeOFSubsystemFunc: func(context.Context, string) error { targetDeletes++; return nil },
				DeleteSubvolumeFunc: func(context.Context, string, string) error {
					if targetDeletes != 1 {
						t.Fatal("storage deleted before presentation")
					}
					storageDeletes++
					return nil
				},
			}
			s := &ControllerService{apiClient: client}
			meta := &VolumeMetadata{DatasetID: "tank/test", DatasetName: "test"}
			if _, err := safetyDeleter(s, protocol)(context.Background(), meta); status.Code(err) != codes.Unavailable {
				t.Fatalf("first attempt: %v", err)
			}
			if storageDeletes != 0 || targetDeletes != 0 {
				t.Fatal("failed query performed deletion")
			}
			unavailable = false
			if _, err := safetyDeleter(s, protocol)(context.Background(), meta); err != nil {
				t.Fatal(err)
			}
			if storageDeletes != 1 || targetDeletes != 1 {
				t.Fatal("retry did not finish cleanup")
			}
		})
	}
}
