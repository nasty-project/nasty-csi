package driver

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

// encodeSnapshotToken encodes an offset as a pagination token.
func encodeSnapshotToken(offset int) string {
	return strconv.Itoa(offset)
}

// parseSnapshotToken parses a pagination token to extract the offset.
func parseSnapshotToken(token string) (int, error) {
	var offset int
	_, err := fmt.Sscanf(token, "%d", &offset)
	if err != nil {
		return 0, fmt.Errorf("invalid token format: %w", err)
	}
	return offset, nil
}

// ListSnapshots lists snapshots.
func (s *ControllerService) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots called with request: %+v", req)

	// Special case: filter by snapshot ID
	if req.GetSnapshotId() != "" {
		return s.listSnapshotByID(ctx, req)
	}

	// Special case: filter by source volume ID
	if req.GetSourceVolumeId() != "" {
		return s.listSnapshotsBySourceVolume(ctx, req)
	}

	// General case: list all snapshots across all managed volumes
	return s.listAllSnapshots(ctx, req)
}

// ControllerGetSnapshot returns information about a specific snapshot.
func (s *ControllerService) ControllerGetSnapshot(ctx context.Context, req *csi.GetSnapshotRequest) (*csi.GetSnapshotResponse, error) {
	klog.V(4).Infof("ControllerGetSnapshot called with request: %+v", req)

	snapshotID := req.GetSnapshotId()
	if snapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID is required")
	}

	listResp, err := s.ListSnapshots(ctx, &csi.ListSnapshotsRequest{
		SnapshotId: snapshotID,
	})
	if err != nil {
		return nil, err
	}

	if len(listResp.Entries) == 0 {
		return nil, status.Errorf(codes.NotFound, "Snapshot %s not found", snapshotID)
	}

	return &csi.GetSnapshotResponse{
		Snapshot: listResp.Entries[0].Snapshot,
	}, nil
}

// listSnapshotByID handles listing a specific snapshot by ID.
func (s *ControllerService) listSnapshotByID(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	snapshotMeta, err := decodeSnapshotID(req.GetSnapshotId())
	if err != nil {
		klog.V(4).Infof("Invalid snapshot ID %q: %v - returning empty list", req.GetSnapshotId(), err)
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
	}

	pool, subvolumeName, err := splitSubvolumeID(snapshotMeta.SourceVolume)
	if err != nil {
		klog.V(4).Infof("Invalid source volume ID %q in snapshot %q: %v - returning empty list",
			snapshotMeta.SourceVolume, req.GetSnapshotId(), err)
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
	}

	// Look up the subvolume to verify snapshot existence and get capacity
	subvol, err := s.apiClient.GetSubvolume(ctx, pool, subvolumeName)
	if err != nil {
		klog.V(4).Infof("Source volume %s not found: %v - returning empty list", snapshotMeta.SourceVolume, err)
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
	}

	// Check if snapshot exists on this subvolume
	found := false
	for _, snapName := range subvol.Snapshots {
		if snapName == snapshotMeta.SnapshotName {
			found = true
			break
		}
	}
	if !found {
		klog.V(4).Infof("Snapshot %s not found on volume %s - returning empty list",
			snapshotMeta.SnapshotName, snapshotMeta.SourceVolume)
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
	}

	var sizeBytes int64
	if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
		sizeBytes = nastyapi.StringToInt64(capStr)
	}

	entry := &csi.ListSnapshotsResponse_Entry{
		Snapshot: &csi.Snapshot{
			SnapshotId:     req.GetSnapshotId(),
			SourceVolumeId: snapshotMeta.SourceVolume,
			CreationTime:   timestamppb.New(time.Now()),
			ReadyToUse:     true,
			SizeBytes:      sizeBytes,
		},
	}

	return &csi.ListSnapshotsResponse{
		Entries: []*csi.ListSnapshotsResponse_Entry{entry},
	}, nil
}

// listSnapshotsBySourceVolume handles listing snapshots for a specific source volume.
func (s *ControllerService) listSnapshotsBySourceVolume(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	sourceVolumeID := req.GetSourceVolumeId()

	pool, subvolumeName, err := splitSubvolumeID(sourceVolumeID)
	if err != nil {
		klog.V(4).Infof("Invalid source volume ID %q: %v - returning empty list", sourceVolumeID, err)
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
	}

	subvol, err := s.apiClient.GetSubvolume(ctx, pool, subvolumeName)
	if err != nil {
		klog.V(4).Infof("Source volume %s not found: %v - returning empty list", sourceVolumeID, err)
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
	}

	protocol := subvol.Properties[nastyapi.PropertyProtocol]
	if protocol == "" {
		protocol = ProtocolNFS
	}

	var sizeBytes int64
	if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
		sizeBytes = nastyapi.StringToInt64(capStr)
	}

	snapshots := subvol.Snapshots

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = len(snapshots)
	}

	startIndex := 0
	if req.GetStartingToken() != "" {
		startIndex, err = parseSnapshotToken(req.GetStartingToken())
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "Invalid starting token: %v", err)
		}
		if startIndex < 0 || startIndex >= len(snapshots) {
			return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
		}
	}

	endIndex := startIndex + maxEntries
	if endIndex > len(snapshots) {
		endIndex = len(snapshots)
	}

	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		snapName := snapshots[i]

		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapName,
			SourceVolume: sourceVolumeID,
			Protocol:     protocol,
			CreatedAt:    time.Now().Unix(),
		}

		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			klog.Warningf("Failed to encode snapshot ID for %s: %v - skipping", snapName, encodeErr)
			continue
		}

		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: sourceVolumeID,
				CreationTime:   timestamppb.New(time.Now()),
				ReadyToUse:     true,
				SizeBytes:      sizeBytes,
			},
		}
		entries = append(entries, entry)
	}

	var nextToken string
	if endIndex < len(snapshots) {
		nextToken = encodeSnapshotToken(endIndex)
	}

	return &csi.ListSnapshotsResponse{
		Entries:   entries,
		NextToken: nextToken,
	}, nil
}

// listAllSnapshots handles listing all snapshots across all managed volumes.
//
// NASty requires a pool name to query snapshots — there is no global snapshot index.
// Without a source volume or snapshot ID filter, we cannot determine the pool.
// This case returns an empty list per CSI spec (which allows it for optional LIST_SNAPSHOTS capability).
//
// Callers that need a full list should use ListSnapshots with SourceVolumeId filter.
func (s *ControllerService) listAllSnapshots(_ context.Context, _ *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	// TODO: If a driver-wide default pool is ever added to ControllerService, enumerate
	// all snapshots via FindManagedSubvolumes + ListSnapshots(ctx, pool).
	klog.V(4).Infof("ListSnapshots (all) called without filter — returning empty list (pool required by NASty API)")
	return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{}}, nil
}
