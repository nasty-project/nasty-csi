package driver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestVolumeLifecycleLocksSerializeAndCancel(t *testing.T) {
	var locks volumeLifecycleLocks
	release, err := locks.lock(context.Background(), "volume")
	if err != nil {
		t.Fatal(err)
	}
	other, err := locks.lock(context.Background(), "other-volume")
	if err != nil {
		t.Fatal(err)
	}
	other()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := locks.lock(ctx, "volume"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-volume lock = %v, want deadline exceeded", err)
	}
	release()
	if releaseAgain, err := locks.lock(context.Background(), "volume"); err != nil {
		t.Fatal(err)
	} else {
		releaseAgain()
	}
	if len(locks.entries) != 0 {
		t.Fatalf("released locks retained %d entries", len(locks.entries))
	}
}

func TestStageAndUnstageWaitForSameVolume(t *testing.T) {
	service := &NodeService{}
	release, err := service.lifecycleLocks.lock(context.Background(), "volume")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	//nolint:govet // Field alignment is not relevant for this small test table.
	for _, tt := range []struct {
		name string
		call func(context.Context) error
	}{
		{name: "stage", call: func(ctx context.Context) error {
			_, stageErr := service.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
				VolumeId: "volume", StagingTargetPath: "/staging",
				VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
			})
			return stageErr
		}},
		{name: "unstage", call: func(ctx context.Context) error {
			_, unstageErr := service.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: "volume", StagingTargetPath: "/staging"})
			return unstageErr
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			if got := status.Code(tt.call(ctx)); got != codes.DeadlineExceeded {
				t.Fatalf("operation code = %v, want DeadlineExceeded", got)
			}
		})
	}
}
