package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

// Static errors for snapshot operations.
var (
	ErrProtocolRequired        = errors.New("protocol is required for snapshot ID encoding")
	ErrSourceVolumeRequired    = errors.New("source volume is required for snapshot ID encoding")
	ErrSnapshotNameRequired    = errors.New("snapshot name is required for snapshot ID encoding")
	ErrInvalidSnapshotIDFormat = errors.New("invalid compact snapshot ID format")
	ErrInvalidProtocol         = errors.New("invalid protocol in snapshot ID")
)

// SnapshotMetadata contains information needed to manage a snapshot.
type SnapshotMetadata struct {
	SnapshotName string `json:"snapshotName"` // Snapshot name (bare name, not full path)
	SourceVolume string `json:"sourceVolume"` // Source volume ID (filesystem/name)
	Protocol     string `json:"protocol"`     // Protocol (nfs, nvmeof, iscsi, smb)
	CreatedAt    int64  `json:"-"`            // Creation timestamp (Unix epoch)
}

// Compact snapshot ID format: {protocol}:{volume_id}@{snapshot_name}.
// Example: "nfs:tank/pvc-abc123@snap-xyz789"
// This format is CSI-compliant (under 128 bytes) and easy to parse.

// encodeSnapshotID encodes snapshot metadata into a compact snapshotID string.
// Format: {protocol}:{volume_id}@{snapshot_name}.
func encodeSnapshotID(meta SnapshotMetadata) (string, error) {
	if meta.Protocol == "" {
		return "", ErrProtocolRequired
	}
	if meta.SourceVolume == "" {
		return "", ErrSourceVolumeRequired
	}
	if meta.SnapshotName == "" {
		return "", ErrSnapshotNameRequired
	}
	return fmt.Sprintf("%s:%s@%s", meta.Protocol, meta.SourceVolume, meta.SnapshotName), nil
}

// decodeSnapshotID decodes a snapshotID string into snapshot metadata.
// Format: {protocol}:{volume_id}@{snapshot_name}.
func decodeSnapshotID(snapshotID string) (*SnapshotMetadata, error) {
	colonIdx := strings.Index(snapshotID, ":")
	if colonIdx == -1 {
		return nil, fmt.Errorf("%w: missing protocol separator", ErrInvalidSnapshotIDFormat)
	}

	protocol := snapshotID[:colonIdx]
	remainder := snapshotID[colonIdx+1:]

	if protocol != ProtocolNFS && protocol != ProtocolNVMeOF && protocol != ProtocolISCSI && protocol != ProtocolSMB {
		return nil, fmt.Errorf("%w: %s", ErrInvalidProtocol, protocol)
	}

	atIdx := strings.LastIndex(remainder, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("%w: missing snapshot separator", ErrInvalidSnapshotIDFormat)
	}

	volumeID := remainder[:atIdx]
	snapshotName := remainder[atIdx+1:]

	if volumeID == "" {
		return nil, fmt.Errorf("%w: empty volume ID", ErrInvalidSnapshotIDFormat)
	}
	if snapshotName == "" {
		return nil, fmt.Errorf("%w: empty snapshot name", ErrInvalidSnapshotIDFormat)
	}

	return &SnapshotMetadata{
		Protocol:     protocol,
		SourceVolume: volumeID,
		SnapshotName: snapshotName,
	}, nil
}

// CreateSnapshot creates a volume snapshot.
//
// NASty supports bcachefs-native snapshots (read-only subvolume snapshots).
// Clone/restore from snapshot is not yet supported (returns Unimplemented).
func (s *ControllerService) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	timer := metrics.NewVolumeOperationTimer("snapshot", "create")
	klog.V(4).Infof("CreateSnapshot called with request: %+v", req)

	if req.GetName() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Snapshot name is required")
	}
	if req.GetSourceVolumeId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Source volume ID is required")
	}

	snapshotName := req.GetName()
	sourceVolumeID := req.GetSourceVolumeId()

	// source volume ID must be filesystem/name format
	filesystem, subvolumeName, err := splitSubvolumeID(sourceVolumeID)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Invalid source volume ID %q: %v", sourceVolumeID, err)
	}

	// Look up source subvolume to get protocol
	subvol, err := s.apiClient.GetSubvolume(ctx, filesystem, subvolumeName)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.NotFound, "Source volume %s not found: %v", sourceVolumeID, err)
	}

	protocol := subvol.Properties[nastyapi.PropertyProtocol]
	if protocol == "" {
		protocol = ProtocolNFS
	}

	// Check idempotency: if snapshot already exists on this subvolume, return it.
	// If it exists on a DIFFERENT volume, return ALREADY_EXISTS per CSI spec.
	managedSubvols, findErr := s.apiClient.FindManagedSubvolumes(ctx, "")
	if findErr == nil {
		for i := range managedSubvols {
			sv := &managedSubvols[i]
			for _, snap := range sv.Snapshots {
				if snap != snapshotName || (sv.Filesystem == filesystem && sv.Name == subvolumeName) {
					continue
				}
				timer.ObserveError()
				return nil, status.Errorf(codes.AlreadyExists,
					"snapshot %q already exists on different volume %s/%s", snapshotName, sv.Filesystem, sv.Name)
			}
		}
	}

	for _, existingSnap := range subvol.Snapshots {
		if existingSnap != snapshotName {
			continue
		}
		klog.Infof("Snapshot %s already exists on volume %s (idempotent)", snapshotName, sourceVolumeID)
		snapshotMeta := SnapshotMetadata{
			SnapshotName: snapshotName,
			SourceVolume: sourceVolumeID,
			Protocol:     protocol,
			CreatedAt:    time.Now().Unix(),
		}
		snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
		if encodeErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to encode snapshot ID: %v", encodeErr)
		}
		var sizeBytes int64
		if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
			sizeBytes = nastyapi.StringToInt64(capStr)
		}
		timer.ObserveSuccess()
		return &csi.CreateSnapshotResponse{
			Snapshot: &csi.Snapshot{
				SnapshotId:     snapshotID,
				SourceVolumeId: sourceVolumeID,
				CreationTime:   timestamppb.New(time.Now()),
				ReadyToUse:     true,
				SizeBytes:      sizeBytes,
			},
		}, nil
	}

	// Create snapshot
	snap, err := s.apiClient.CreateSnapshot(ctx, nastyapi.SnapshotCreateParams{
		Filesystem: filesystem,
		Subvolume:  subvolumeName,
		Name:       snapshotName,
		ReadOnly:   true,
	})
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create snapshot: %v", err)
	}

	klog.Infof("Successfully created snapshot %s on volume %s", snap.Name, sourceVolumeID)

	snapshotMeta := SnapshotMetadata{
		SnapshotName: snapshotName,
		SourceVolume: sourceVolumeID,
		Protocol:     protocol,
		CreatedAt:    time.Now().Unix(),
	}
	snapshotID, encodeErr := encodeSnapshotID(snapshotMeta)
	if encodeErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to encode snapshot ID: %v", encodeErr)
	}

	var sizeBytes int64
	if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
		sizeBytes = nastyapi.StringToInt64(capStr)
	}

	timer.ObserveSuccess()
	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     snapshotID,
			SourceVolumeId: sourceVolumeID,
			CreationTime:   timestamppb.New(time.Now()),
			ReadyToUse:     true,
			SizeBytes:      sizeBytes,
		},
	}, nil
}

// DeleteSnapshot deletes a snapshot.
func (s *ControllerService) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	timer := metrics.NewVolumeOperationTimer("snapshot", "delete")
	klog.V(4).Infof("DeleteSnapshot called with request: %+v", req)

	if req.GetSnapshotId() == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID is required")
	}

	snapshotID := req.GetSnapshotId()

	// Decode snapshot metadata
	snapshotMeta, err := decodeSnapshotID(snapshotID)
	if err != nil {
		// If we can't decode the snapshot ID, log a warning but return success
		// per CSI spec (DeleteSnapshot should be idempotent)
		klog.Warningf("Failed to decode snapshot ID %s: %v. Assuming snapshot doesn't exist.", snapshotID, err)
		timer.ObserveSuccess()
		return &csi.DeleteSnapshotResponse{}, nil
	}

	filesystem, subvolumeName, err := splitSubvolumeID(snapshotMeta.SourceVolume)
	if err != nil {
		klog.Warningf("Failed to parse source volume ID %s: %v. Assuming snapshot doesn't exist.", snapshotMeta.SourceVolume, err)
		timer.ObserveSuccess()
		return &csi.DeleteSnapshotResponse{}, nil
	}

	klog.Infof("Deleting snapshot %s on volume %s/%s", snapshotMeta.SnapshotName, filesystem, subvolumeName)

	if err := s.apiClient.DeleteSnapshot(ctx, filesystem, subvolumeName, snapshotMeta.SnapshotName); err != nil {
		if isNotFoundError(err) {
			klog.Infof("Snapshot %s not found on %s/%s, assuming already deleted", snapshotMeta.SnapshotName, filesystem, subvolumeName)
			timer.ObserveSuccess()
			return &csi.DeleteSnapshotResponse{}, nil
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to delete snapshot: %v", err)
	}

	klog.Infof("Successfully deleted snapshot %s on volume %s/%s", snapshotMeta.SnapshotName, filesystem, subvolumeName)
	timer.ObserveSuccess()
	return &csi.DeleteSnapshotResponse{}, nil
}

// isNotFoundError checks if an error indicates a resource was not found.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return containsAny(errStr, []string{"not found", "does not exist", "ENOENT", "404"})
}

// containsAny checks if a string contains any of the given substrings.
func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
