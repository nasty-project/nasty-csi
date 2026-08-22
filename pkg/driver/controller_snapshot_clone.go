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
	sourceProperties, sourceExists, err := s.snapshotSourceProperties(ctx, filesystem, parentSubvolume)
	if err != nil {
		return nil, err
	}
	newName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}
	destinationSelection, err := s.selectCloneDestination(ctx, req, filesystem, newName, protocol)
	if err != nil {
		return nil, err
	}
	selectedName := destinationSelection.name
	freshIdentity, err := newVolumeIdentityDecision(req, selectedName, protocol)
	if err != nil {
		return nil, err
	}

	// 3. Clone through the backend on every attempt. The backend validates
	// that an existing destination was created from this exact snapshot.
	klog.V(4).Infof("Cloning snapshot %s/%s@%s into new subvolume %s/%s",
		filesystem, parentSubvolume, meta.SnapshotName, filesystem, selectedName)
	clone, cloneErr := s.apiClient.CloneSnapshot(ctx, nastyapi.SnapshotCloneParams{
		Filesystem: filesystem,
		Subvolume:  parentSubvolume,
		Snapshot:   meta.SnapshotName,
		NewName:    selectedName,
	})
	if cloneErr != nil {
		klog.Errorf("Failed to clone snapshot %s/%s@%s: %v", filesystem, parentSubvolume, meta.SnapshotName, cloneErr)
		return nil, createVolumeError("failed to clone snapshot", cloneErr)
	}
	backendCreated := clone != nil && clone.Created
	createdClone := clone
	klog.Infof("Successfully cloned snapshot %s/%s@%s into subvolume %s/%s",
		filesystem, parentSubvolume, meta.SnapshotName, filesystem, selectedName)

	// 4. Ensure the clone satisfies the normalized CSI capacity range.
	requestedCapacity, capacityErr := s.ensureClonedVolumeCapacity(ctx, req, filesystem, selectedName)
	if capacityErr != nil {
		if backendCreated {
			return nil, s.rollbackCreatedSubvolume(ctx, createdClone, capacityErr)
		}
		return nil, capacityErr
	}

	clone, getErr := s.apiClient.GetSubvolume(ctx, filesystem, selectedName)
	if getErr != nil {
		operationErr := status.Errorf(codes.Internal, "failed to read cloned subvolume identity: %v", getErr)
		if backendCreated {
			return nil, s.rollbackCreatedSubvolume(ctx, createdClone, operationErr)
		}
		return nil, operationErr
	}
	identity, identityErr := classifyCloneIdentity(
		req, protocol, parentSubvolume, selectedName, clone, sourceProperties, sourceExists,
		backendCreated, destinationSelection.existed, destinationSelection.legacy, freshIdentity,
	)
	if identityErr != nil {
		if backendCreated {
			return nil, s.rollbackCreatedSubvolume(ctx, createdClone, identityErr)
		}
		return nil, identityErr
	}
	if backendCreated {
		clone.Properties = nil
	}
	csiProps := mergeProperties(map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requestedCapacity, 10),
	}, identity.properties)
	if propErr := s.persistVolumeIdentity(ctx, clone, csiProps, identity); propErr != nil {
		if backendCreated {
			return nil, s.rollbackCreatedSubvolume(ctx, clone, propErr)
		}
		return nil, propErr
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

func (s *ControllerService) snapshotSourceProperties(
	ctx context.Context, filesystem, subvolume string,
) (
	properties map[string]string,
	exists bool,
	err error,
) {

	source, err := s.apiClient.GetSubvolume(ctx, filesystem, subvolume)
	if isNotFoundError(err) {
		return map[string]string{}, false, nil
	}
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "failed to read snapshot source identity: %v", err)
	}
	if source == nil {
		return map[string]string{}, false, nil
	}
	return source.Properties, true, nil
}
