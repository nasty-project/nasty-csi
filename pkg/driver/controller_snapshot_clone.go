package driver

import (
	"context"
	"strconv"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// cloneInfo holds information about a snapshot clone operation.
// createVolumeFromSnapshot creates a new volume from a snapshot by cloning.
//
// The approach:
//  1. Decode the snapshot ID to get filesystem, parent subvolume, snapshot name, and protocol.
//  2. Resolve the new subvolume name from the CSI request (same naming as normal create).
//  3. Clone the snapshot into a new writable subvolume.
//  4. Set CSI metadata properties on the new subvolume.
//  5. Delegate to createVolumeByProtocol to set up protocol-specific sharing.
//     The protocol create function will find the existing subvolume (idempotency)
//     and create the share.
func (s *ControllerService) createVolumeFromSnapshot(ctx context.Context, req *csi.CreateVolumeRequest, snapshotID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("createVolumeFromSnapshot called for volume %s from snapshot %s", req.GetName(), snapshotID)

	// 1. Decode snapshot ID to get source metadata
	meta, err := decodeSnapshotID(snapshotID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "snapshot %q not found: %v", snapshotID, err)
	}

	filesystem, parentSubvolume, err := splitSubvolumeID(meta.SourceVolume)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid source volume ID %q: %v", meta.SourceVolume, err)
	}

	protocol := meta.Protocol
	if normalizeErr := normalizeCreateCapacity(req, protocol); normalizeErr != nil {
		return nil, normalizeErr
	}
	klog.V(4).Infof("Snapshot clone: filesystem=%s, parentSubvolume=%s, snapshot=%s, protocol=%s",
		filesystem, parentSubvolume, meta.SnapshotName, protocol)

	// 2. Resolve the new subvolume name using the same naming conventions as normal volume creation
	params := req.GetParameters()
	if requestedFilesystem := params[paramFilesystem]; requestedFilesystem != "" && requestedFilesystem != filesystem {
		return nil, status.Errorf(codes.InvalidArgument,
			"snapshot restore must use source filesystem %q, requested %q", filesystem, requestedFilesystem)
	}
	if requestedProtocol := params["protocol"]; requestedProtocol != "" && requestedProtocol != protocol {
		return nil, status.Errorf(codes.InvalidArgument,
			"snapshot restore must use source protocol %q, requested %q", protocol, requestedProtocol)
	}
	newName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}

	// 3. Clone through the backend on every attempt. The backend validates
	// that an existing destination was created from this exact snapshot.
	klog.V(4).Infof("Cloning snapshot %s/%s@%s into new subvolume %s/%s",
		filesystem, parentSubvolume, meta.SnapshotName, filesystem, newName)
	if _, cloneErr := s.apiClient.CloneSnapshot(ctx, nastyapi.SnapshotCloneParams{
		Filesystem: filesystem,
		Subvolume:  parentSubvolume,
		Snapshot:   meta.SnapshotName,
		NewName:    newName,
	}); cloneErr != nil {
		klog.Errorf("Failed to clone snapshot %s/%s@%s: %v", filesystem, parentSubvolume, meta.SnapshotName, cloneErr)
		return nil, createVolumeError("failed to clone snapshot", cloneErr)
	}
	klog.Infof("Successfully cloned snapshot %s/%s@%s into subvolume %s/%s",
		filesystem, parentSubvolume, meta.SnapshotName, filesystem, newName)

	// 4. Ensure the clone satisfies the normalized CSI capacity range.
	requestedCapacity, capacityErr := s.ensureClonedVolumeCapacity(ctx, req, filesystem, newName)
	if capacityErr != nil {
		return nil, capacityErr
	}

	csiProps := map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: req.GetName(),
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requestedCapacity, 10),
		nastyapi.PropertyProtocol:      protocol,
	}
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, filesystem, newName, csiProps); propErr != nil {
		return nil, status.Errorf(codes.Internal,
			"failed to set CSI properties on cloned subvolume %s/%s: %v", filesystem, newName, propErr)
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
