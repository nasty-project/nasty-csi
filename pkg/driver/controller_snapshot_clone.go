package driver

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// cloneInfo holds information about a snapshot clone operation.
// Reserved for future use when NASty supports snapshot restore.
type cloneInfo struct {
	SnapshotID     string
	Mode           string
	OriginSnapshot string
}

// createVolumeFromSnapshot creates a new volume from a snapshot by cloning.
//
// TODO: Implement bcachefs snapshot restore once NASty API supports it.
// For now, restoring from a snapshot is not supported.
func (s *ControllerService) createVolumeFromSnapshot(_ context.Context, req *csi.CreateVolumeRequest, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("createVolumeFromSnapshot called for volume %s, snapshotID %s — not yet implemented", req.GetName(), snapshotID)
	return nil, status.Errorf(codes.Unimplemented,
		"restoring volumes from snapshots is not yet supported by the NASty CSI driver; "+
			"snapshot ID: %s", snapshotID)
}
