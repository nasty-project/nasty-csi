// Package driver implements iSCSI-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	"github.com/nasty-project/nasty-csi/pkg/retry"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// iscsiVolumeParams holds validated parameters for iSCSI volume creation.
type iscsiVolumeParams struct {
	volumeName        string
	subvolumeName     string
	deleteStrategy    string
	storageClass      string
	targetIQN         string
	pvcNamespace      string
	pvcName           string
	comment           string
	compression       string
	server            string
	pool              string
	requestedCapacity int64
	markAdoptable     bool
}

// generateIQN creates a unique IQN for a volume's dedicated iSCSI target.
// Format: iqn.2024-01.io.nasty.csi:<volume-name>.
func generateIQN(volumeName string) string {
	return "iqn.2024-01.io.nasty.csi:" + volumeName
}

// validateISCSIParams validates and extracts iSCSI volume parameters from the request.
func validateISCSIParams(req *csi.CreateVolumeRequest) (*iscsiVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for iSCSI volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for iSCSI volumes")
	}

	// Resolve volume name using templating (if configured in StorageClass)
	volumeName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}

	// Resolve dataset comment from commentTemplate (if configured in StorageClass)
	comment, err := ResolveComment(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve comment template: %v", err)
	}

	// Get delete strategy (default: delete)
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = deleteStrategyDelete
	}

	// Check if volume should be marked as adoptable
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	// Get capacity
	capacityRange := req.GetCapacityRange()
	var requestedCapacity int64
	if capacityRange != nil {
		requestedCapacity = capacityRange.GetRequiredBytes()
	}
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	// Extract adoption metadata from CSI parameters
	pvcName := params["csi.storage.k8s.io/pvc/name"]
	pvcNamespace := params["csi.storage.k8s.io/pvc/namespace"]
	storageClass := params["csi.storage.k8s.io/sc/name"]

	// Optional compression setting
	compression := params["compression"]

	return &iscsiVolumeParams{
		requestedCapacity: requestedCapacity,
		pool:              pool,
		server:            server,
		volumeName:        volumeName,
		subvolumeName:     volumeName,
		targetIQN:         generateIQN(volumeName),
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		comment:           comment,
		compression:       compression,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
	}, nil
}

// buildISCSIVolumeResponse constructs a CSI CreateVolumeResponse for an iSCSI volume.
func buildISCSIVolumeResponse(volumeName, server string, subvol *nastyapi.Subvolume, target *nastyapi.ISCSITarget, capacity int64) *csi.CreateVolumeResponse {
	// Volume ID is pool/subvolumeName for O(1) lookups
	volumeID := subvol.Pool + "/" + subvol.Name

	meta := VolumeMetadata{
		Name:            volumeName,
		Protocol:        ProtocolISCSI,
		DatasetID:       volumeID,
		DatasetName:     subvol.Name,
		Server:          server,
		ISCSITargetUUID: target.ID,
		ISCSIIQN:        target.IQN,
	}

	// Build volume context with all necessary metadata
	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyExpectedCapacity] = strconv.FormatInt(capacity, 10)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolISCSI, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// createISCSIVolume creates an iSCSI volume (block subvolume + iSCSI target with LUN).
func (s *ControllerService) createISCSIVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "create")
	klog.V(4).Info("Creating iSCSI volume")

	// Validate and extract parameters
	params, err := validateISCSIParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating iSCSI volume: %s with size: %d bytes", params.volumeName, params.requestedCapacity)

	// Check if subvolume already exists (idempotency)
	existingSubvol, err := s.apiClient.GetSubvolume(ctx, params.pool, params.subvolumeName)
	if err != nil && !isNotFoundError(err) {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to check for existing subvolume: %v", err)
	}

	// Handle existing subvolume (idempotency check)
	if existingSubvol != nil {
		resp, done, handleErr := s.handleExistingISCSISubvolume(ctx, params, existingSubvol, timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// Subvolume exists but no target — continue with target creation below
	}

	// Step 1: Create or reuse block subvolume
	subvol, subvolIsNew, err := s.getOrCreateSubvolume(ctx, params.pool, params.subvolumeName,
		"block", params.comment, params.compression, params.requestedCapacity, timer)
	if err != nil {
		return nil, err
	}

	// Determine block device path for the LUN
	if subvol.BlockDevice == nil || *subvol.BlockDevice == "" {
		if subvolIsNew {
			if delErr := s.apiClient.DeleteSubvolume(ctx, params.pool, params.subvolumeName); delErr != nil {
				klog.Errorf("Failed to cleanup newly-created block subvolume: %v", delErr)
			}
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Block subvolume %s has no block device path", params.subvolumeName)
	}
	blockDevice := *subvol.BlockDevice

	// Step 2: Create iSCSI target
	targetParams := nastyapi.ISCSITargetCreateParams{
		Name: params.volumeName,
	}
	target, err := s.apiClient.CreateISCSITarget(ctx, targetParams)
	if err != nil {
		// Cleanup: only delete subvolume if we just created it
		if subvolIsNew {
			klog.Errorf("Failed to create iSCSI target, cleaning up newly-created subvolume: %v", err)
			if delErr := s.apiClient.DeleteSubvolume(ctx, params.pool, params.subvolumeName); delErr != nil {
				klog.Errorf("Failed to cleanup subvolume: %v", delErr)
			}
		} else {
			klog.Warningf("Failed to create iSCSI target: %v (skipping subvolume cleanup — volume was pre-existing)", err)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create iSCSI target '%s': %v", params.volumeName, err)
	}

	// Step 3: Add LUN to target (points to block device)
	target, err = s.apiClient.AddISCSILun(ctx, target.ID, blockDevice)
	if err != nil {
		// Cleanup: delete target (always new), only delete subvolume if newly created
		klog.Errorf("Failed to add iSCSI LUN, cleaning up: %v", err)
		if delErr := s.apiClient.DeleteISCSITarget(ctx, target.ID); delErr != nil {
			klog.Errorf("Failed to cleanup iSCSI target: %v", delErr)
		}
		if subvolIsNew {
			if delErr := s.apiClient.DeleteSubvolume(ctx, params.pool, params.subvolumeName); delErr != nil {
				klog.Errorf("Failed to cleanup subvolume: %v", delErr)
			}
		} else {
			klog.Warningf("Skipping subvolume cleanup — volume was pre-existing")
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to add LUN to iSCSI target '%s': %v", target.ID, err)
	}

	// Step 4: Store xattr properties for metadata tracking
	props := nastyapi.ISCSIVolumePropertiesV1(nastyapi.ISCSIVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
		ClusterID:      s.clusterID,
	})

	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, params.pool, params.subvolumeName, props); propErr != nil {
		klog.Warningf("Failed to set xattr properties on %s: %v (volume created successfully)", params.subvolumeName, propErr)
	}

	klog.Infof("Created iSCSI volume: %s (subvolume: %s/%s, target: %s, IQN: %s)",
		params.volumeName, params.pool, params.subvolumeName, target.ID, target.IQN)

	timer.ObserveSuccess()
	return buildISCSIVolumeResponse(params.volumeName, params.server, subvol, target, params.requestedCapacity), nil
}

// handleExistingISCSISubvolume handles the case when a block subvolume already exists (idempotency).
func (s *ControllerService) handleExistingISCSISubvolume(ctx context.Context, params *iscsiVolumeParams, existingSubvol *nastyapi.Subvolume, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("Block subvolume %s already exists, checking idempotency", params.subvolumeName)

	// Check capacity from stored properties
	existingCapacity := params.requestedCapacity
	if existingSubvol.Properties != nil {
		if capStr, ok := existingSubvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
			if capBytes := nastyapi.StringToInt64(capStr); capBytes > 0 {
				existingCapacity = capBytes
			}
		}
	}

	if existingCapacity > 0 && existingCapacity != params.requestedCapacity {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.AlreadyExists,
			"Volume '%s' already exists with different capacity: existing=%d bytes, requested=%d bytes",
			params.volumeName, existingCapacity, params.requestedCapacity)
	}

	// Scan iSCSI targets by IQN pattern derived from volume name
	expectedIQN := generateIQN(params.volumeName)
	targets, listErr := s.apiClient.ListISCSITargets(ctx)
	if listErr == nil {
		for i := range targets {
			if targets[i].IQN == expectedIQN {
				klog.V(4).Infof("iSCSI volume already exists (target: %s, IQN: %s), returning existing volume",
					targets[i].ID, targets[i].IQN)
				resp := buildISCSIVolumeResponse(params.volumeName, params.server, existingSubvol, &targets[i], existingCapacity)
				timer.ObserveSuccess()
				return resp, true, nil
			}
		}
	}

	// Subvolume exists but no target — signal caller to proceed with target creation
	return nil, false, nil
}

// verifyISCSIOwnership verifies ownership of an iSCSI volume via xattr properties.
// Returns the deleteStrategy and a "not found" flag.
func (s *ControllerService) verifyISCSIOwnership(ctx context.Context, meta *VolumeMetadata) (deleteStrategy string, notFound bool, err error) {
	deleteStrategy = nastyapi.DeleteStrategyDelete

	pool, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		return deleteStrategy, false, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	subvol, err := s.apiClient.GetSubvolume(ctx, pool, name)
	if err != nil {
		if isNotFoundError(err) {
			return "", true, nil
		}
		klog.Warningf("Failed to verify subvolume ownership: %v (continuing with deletion)", err)
		return deleteStrategy, false, nil
	}

	props := subvol.Properties
	if props == nil {
		return deleteStrategy, false, nil
	}

	if managedBy, ok := props[nastyapi.PropertyManagedBy]; ok && managedBy != nastyapi.ManagedByValue {
		return "", false, status.Errorf(codes.FailedPrecondition,
			"Subvolume %s is not managed by nasty-csi (managed_by=%s)", meta.DatasetID, managedBy)
	}

	if volumeName, ok := props[nastyapi.PropertyCSIVolumeName]; ok {
		if volumeName != meta.DatasetName {
			return "", false, status.Errorf(codes.FailedPrecondition,
				"Subvolume %s volume name mismatch (stored=%s, requested=%s)", meta.DatasetID, volumeName, meta.DatasetName)
		}
	}

	if strategy, ok := props[nastyapi.PropertyDeleteStrategy]; ok && strategy != "" {
		deleteStrategy = strategy
	}

	klog.V(4).Infof("Ownership verified for subvolume %s (volume: %s)", meta.DatasetID, meta.Name)
	return deleteStrategy, false, nil
}

const deleteStrategyDelete = "delete"

// deleteISCSIVolume deletes an iSCSI volume and all associated resources.
// Subvolume is deleted first; if it fails, iSCSI target is preserved to prevent orphaning.
//
//nolint:dupl // Delete pattern shared with NVMe-oF
func (s *ControllerService) deleteISCSIVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, deleteStrategyDelete)
	klog.Infof("Deleting iSCSI volume: %s (subvolume: %s, target: %s)",
		meta.Name, meta.DatasetID, meta.ISCSITargetUUID)

	// Step 0: Verify ownership via xattr properties before deletion
	deleteStrategy, notFound, err := s.verifyISCSIOwnership(ctx, meta)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}
	if notFound {
		klog.V(4).Infof("Subvolume %s not found, assuming already deleted (idempotency)", meta.DatasetID)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	if deleteStrategy == nastyapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has delete strategy 'retain', skipping deletion", meta.Name)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	pool, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	// Step 1: Delete subvolume first (prevents orphaning iSCSI resources)
	// If the subvolume can't be deleted, bail immediately to prevent orphaning the iSCSI target.
	firstErr := s.apiClient.DeleteSubvolume(ctx, pool, name)
	if firstErr != nil && !isNotFoundError(firstErr) {
		// Retry with snapshot cleanup
		klog.Infof("Direct deletion failed for %s: %v — cleaning up snapshots before retry",
			meta.DatasetID, firstErr)

		retryConfig := retry.DeletionConfig("delete-iscsi-subvol")
		err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
			deleteErr := s.apiClient.DeleteSubvolume(ctx, pool, name)
			if deleteErr != nil && isNotFoundError(deleteErr) {
				return nil
			}
			return deleteErr
		})

		if err != nil {
			klog.Errorf("Subvolume %s deletion failed — skipping iSCSI target cleanup to avoid orphaning: %v", meta.DatasetID, err)
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal,
				"Failed to delete block subvolume %s: %v (iSCSI target preserved to prevent orphaning)", meta.DatasetID, err)
		}
	}
	klog.V(4).Infof("Deleted block subvolume: %s", meta.DatasetID)

	// Step 2: Subvolume is gone — clean up iSCSI target (best effort)
	if meta.ISCSITargetUUID != "" {
		if err := s.apiClient.DeleteISCSITarget(ctx, meta.ISCSITargetUUID); err != nil {
			if !isNotFoundError(err) {
				klog.Warningf("Failed to delete iSCSI target %s (subvolume already deleted, will retry): %v", meta.ISCSITargetUUID, err)
			}
		} else {
			klog.V(4).Infof("Deleted iSCSI target: %s", meta.ISCSITargetUUID)
		}
	}

	// Clear volume capacity metric
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolISCSI)

	klog.Infof("Deleted iSCSI volume: %s", meta.Name)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// expandISCSIVolume expands an iSCSI volume by updating the subvolume capacity property.
//
//nolint:dupl // Intentionally similar to NFS/NVMe-oF expansion logic
func (s *ControllerService) expandISCSIVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "expand")
	klog.V(4).Infof("Expanding iSCSI volume: %s (subvolume: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "subvolume ID not found in volume metadata")
	}

	pool, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	// Resize the underlying subvolume
	//nolint:gosec // G115: CSI capacity is always non-negative
	if _, err := s.apiClient.ResizeSubvolume(ctx, pool, name, uint64(requiredBytes)); err != nil {
		klog.Errorf("Failed to resize subvolume %s/%s: %v", pool, name, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to resize subvolume: %v", err)
	}

	// Update capacity via xattr property
	props := map[string]string{
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requiredBytes, 10),
	}
	_, err := s.apiClient.SetSubvolumeProperties(ctx, pool, name, props)
	if err != nil {
		klog.Errorf("Failed to expand iSCSI subvolume %s: %v", meta.DatasetID, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Failed to update capacity for iSCSI volume '%s'. Error: %v", meta.DatasetID, err)
	}

	klog.Infof("Expanded iSCSI volume: %s to %d bytes", meta.Name, requiredBytes)

	// Update volume capacity metric
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolISCSI, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: true, // iSCSI volumes require node-side filesystem expansion
	}, nil
}

// getISCSIVolumeInfo retrieves volume information and health status for an iSCSI volume.
func (s *ControllerService) getISCSIVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting iSCSI volume info: %s (subvolume: %s, target: %s)",
		meta.Name, meta.DatasetName, meta.ISCSITargetUUID)

	abnormal := false
	var messages []string

	pool, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	// Check 1: Verify block subvolume exists
	var subvol *nastyapi.Subvolume
	subvol, err := s.apiClient.GetSubvolume(ctx, pool, name)
	switch {
	case err != nil && isNotFoundError(err):
		abnormal = true
		messages = append(messages, fmt.Sprintf("Block subvolume %s/%s not found", pool, name))
	case err != nil:
		abnormal = true
		messages = append(messages, fmt.Sprintf("Block subvolume %s/%s query failed: %v", pool, name, err))
	default:
		klog.V(4).Infof("Block subvolume %s/%s exists", pool, name)
	}

	// Check 2: Verify iSCSI target exists
	var capacityBytes int64
	if meta.ISCSITargetUUID != "" {
		target, err := s.apiClient.GetISCSITargetByIQN(ctx, meta.ISCSIIQN)
		switch {
		case err != nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query iSCSI target by IQN %s: %v", meta.ISCSIIQN, err))
		case target == nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("iSCSI target %s not found", meta.ISCSITargetUUID))
		default:
			klog.V(4).Infof("iSCSI target %s is healthy (IQN: %s)", target.ID, target.IQN)
		}
	}

	// Get capacity from stored properties
	if subvol != nil && subvol.Properties != nil {
		if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
			capacityBytes = nastyapi.StringToInt64(capStr)
		}
	}

	// Build response message
	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	// Build volume context
	volumeContext := buildVolumeContext(*meta)

	klog.V(4).Infof("iSCSI volume %s status: abnormal=%t, message=%s", meta.Name, abnormal, message)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacityBytes,
			VolumeContext: volumeContext,
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}

// adoptISCSIVolume adopts an orphaned iSCSI volume by recreating missing NASty resources.
// This enables GitOps workflows where clusters are recreated and need to adopt existing volumes.
func (s *ControllerService) adoptISCSIVolume(ctx context.Context, req *csi.CreateVolumeRequest, subvol *nastyapi.Subvolume, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolISCSI, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting iSCSI volume: %s (subvolume=%s/%s)", volumeName, subvol.Pool, subvol.Name)

	// Get server parameter
	server := params["server"]
	if server == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for iSCSI volumes")
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Find existing target by scanning all targets for IQN matching the volume name
	var target *nastyapi.ISCSITarget
	targets, listErr := s.apiClient.ListISCSITargets(ctx)
	if listErr == nil {
		expectedIQN := generateIQN(volumeName)
		for i := range targets {
			if targets[i].IQN == expectedIQN {
				target = &targets[i]
				klog.Infof("Found existing target by IQN match: ID=%s, IQN=%s", target.ID, target.IQN)
				break
			}
		}
	}

	// Ensure block device path is available
	if subvol.BlockDevice == nil || *subvol.BlockDevice == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Block subvolume %s/%s has no block device path", subvol.Pool, subvol.Name)
	}
	blockDevice := *subvol.BlockDevice

	// If no target found, create new one
	if target == nil {
		klog.Infof("Creating new iSCSI target for adopted volume: %s", volumeName)

		newTarget, createErr := s.apiClient.CreateISCSITarget(ctx, nastyapi.ISCSITargetCreateParams{
			Name: volumeName,
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create iSCSI target for adopted volume: %v", createErr)
		}

		// Add LUN to target
		newTarget, createErr = s.apiClient.AddISCSILun(ctx, newTarget.ID, blockDevice)
		if createErr != nil {
			if delErr := s.apiClient.DeleteISCSITarget(ctx, newTarget.ID); delErr != nil {
				klog.Errorf("Failed to cleanup iSCSI target after LUN add failure: %v", delErr)
			}
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to add LUN to iSCSI target for adopted volume: %v", createErr)
		}
		target = newTarget
		klog.Infof("Created iSCSI target for adopted volume: ID=%s, IQN=%s", target.ID, target.IQN)
	}

	// Update xattr properties with new IDs
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := nastyapi.ISCSIVolumePropertiesV1(nastyapi.ISCSIVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
		ClusterID:      s.clusterID,
	})
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, subvol.Pool, subvol.Name, props); propErr != nil {
		klog.Warningf("Failed to update xattr properties on adopted volume %s/%s: %v", subvol.Pool, subvol.Name, propErr)
	}

	klog.Infof("Successfully adopted iSCSI volume: %s (target=%s, IQN=%s)", volumeName, target.ID, target.IQN)
	timer.ObserveSuccess()

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolISCSI, requestedCapacity)

	return buildISCSIVolumeResponse(volumeName, server, subvol, target, requestedCapacity), nil
}
