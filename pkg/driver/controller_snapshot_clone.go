package driver

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// cloneInfo holds information about a snapshot clone operation.
type cloneInfo struct {
	SnapshotID     string
	Mode           string
	OriginSnapshot string
}

// createVolumeFromSnapshot creates a new volume from a snapshot by cloning.
//
// The approach:
// 1. Decode the snapshot ID to get pool, parent subvolume, snapshot name, and protocol.
// 2. Resolve the new subvolume name from the CSI request (same naming as normal create).
// 3. Clone the snapshot into a new writable subvolume.
// 4. Set CSI metadata properties on the new subvolume.
// 5. Delegate to createVolumeByProtocol to set up protocol-specific sharing.
//    The protocol create function will find the existing subvolume (idempotency)
//    and create the share.
func (s *ControllerService) createVolumeFromSnapshot(ctx context.Context, req *csi.CreateVolumeRequest, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("createVolumeFromSnapshot called for volume %s from snapshot %s", req.GetName(), snapshotID)

	// 1. Decode snapshot ID to get source metadata
	meta, err := decodeSnapshotID(snapshotID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid snapshot ID %q: %v", snapshotID, err)
	}

	pool, parentSubvolume, err := splitSubvolumeID(meta.SourceVolume)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid source volume ID %q: %v", meta.SourceVolume, err)
	}

	protocol := meta.Protocol
	klog.V(4).Infof("Snapshot clone: pool=%s, parentSubvolume=%s, snapshot=%s, protocol=%s",
		pool, parentSubvolume, meta.SnapshotName, protocol)

	// 2. Resolve the new subvolume name using the same naming conventions as normal volume creation
	params := req.GetParameters()
	newName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}

	// 3. Check if the subvolume already exists (idempotency — clone may have already succeeded)
	existingSubvol, getErr := s.apiClient.GetSubvolume(ctx, pool, newName)
	if getErr != nil && !isNotFoundError(getErr) {
		return nil, status.Errorf(codes.Internal, "failed to check for existing subvolume %s/%s: %v", pool, newName, getErr)
	}

	if existingSubvol == nil {
		// Clone the snapshot to create the new subvolume
		klog.V(4).Infof("Cloning snapshot %s/%s@%s into new subvolume %s/%s",
			pool, parentSubvolume, meta.SnapshotName, pool, newName)

		_, cloneErr := s.apiClient.CloneSnapshot(ctx, nastyapi.SnapshotCloneParams{
			Pool:      pool,
			Subvolume: parentSubvolume,
			Snapshot:  meta.SnapshotName,
			NewName:   newName,
		})
		if cloneErr != nil {
			klog.Errorf("Failed to clone snapshot %s/%s@%s: %v", pool, parentSubvolume, meta.SnapshotName, cloneErr)
			return nil, status.Errorf(codes.Internal, "failed to clone snapshot: %v", cloneErr)
		}

		klog.Infof("Successfully cloned snapshot %s/%s@%s into subvolume %s/%s",
			pool, parentSubvolume, meta.SnapshotName, pool, newName)
	} else {
		klog.V(4).Infof("Subvolume %s/%s already exists (idempotent clone), proceeding to share setup", pool, newName)
	}

	// 4. Set CSI metadata properties on the cloned subvolume
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	csiProps := map[string]string{
		nastyapi.PropertyManagedBy:      nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName:  req.GetName(),
		nastyapi.PropertyCapacityBytes:  fmt.Sprintf("%d", requestedCapacity),
		nastyapi.PropertyProtocol:       protocol,
	}
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, pool, newName, csiProps); propErr != nil {
		klog.Warningf("Failed to set CSI properties on cloned subvolume %s/%s: %v (volume will still work)", pool, newName, propErr)
	}

	// 5. Delegate to protocol-specific create to set up sharing
	// The protocol create function will find the existing subvolume and create the share.
	klog.V(4).Infof("Delegating to createVolumeByProtocol for protocol %s", protocol)
	resp, err := s.createVolumeByProtocol(ctx, req, protocol)
	if err != nil {
		return nil, err
	}

	// Set the content source in the response so the CO knows this came from a snapshot
	if resp != nil && resp.Volume != nil {
		resp.Volume.ContentSource = &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: snapshotID,
				},
			},
		}
	}

	klog.Infof("Created volume %s from snapshot %s (protocol: %s)", req.GetName(), snapshotID, protocol)
	return resp, nil
}
