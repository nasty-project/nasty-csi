// Package driver implements NVMe-oF-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	"github.com/nasty-project/nasty-csi/pkg/retry"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// NQN prefix for CSI-managed subsystems.
// Format: nqn.2026-02.io.nasty.csi:<volume-name>
// Each volume gets its own subsystem (independent subsystem architecture).
const defaultNQNPrefix = "nqn.2026-02.io.nasty.csi"

// nvmeofVolumeParams holds validated parameters for NVMe-oF volume creation.
type nvmeofVolumeParams struct {
	deleteStrategy    string
	comment           string
	volumeName        string
	subvolumeName     string
	subsystemNQN      string
	queueSize         string
	nrIOQueues        string
	storageClass      string
	server            string
	pool              string
	pvcName           string
	pvcNamespace      string
	compression       string
	requestedCapacity int64
	markAdoptable     bool
}

// generateNQN creates a unique NQN for a volume's dedicated subsystem.
// Format: nqn.2026-02.io.nasty.csi:<volume-name>.
func generateNQN(nqnPrefix, volumeName string) string {
	return fmt.Sprintf("%s:%s", nqnPrefix, volumeName)
}

// injectQueueParams adds optional NVMe-oF queue tuning parameters into the volume context.
// These are passed from StorageClass parameters to the node plugin via volumeContext so the
// node can apply --nr-io-queues and --queue-size when running nvme connect.
func injectQueueParams(volumeContext map[string]string, nrIOQueues, queueSize string) {
	if nrIOQueues != "" {
		volumeContext["nvmeof.nr-io-queues"] = nrIOQueues
	}
	if queueSize != "" {
		volumeContext["nvmeof.queue-size"] = queueSize
	}
}

// validateNVMeOFParams validates and extracts NVMe-oF volume parameters from the request.
func validateNVMeOFParams(req *csi.CreateVolumeRequest) (*nvmeofVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NVMe-oF volumes")
	}

	server := params["server"]
	if server == "" {
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
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

	// Generate unique NQN for this volume's dedicated subsystem
	nqnPrefix := params["subsystemNQN"]
	if nqnPrefix == "" {
		nqnPrefix = defaultNQNPrefix
	}
	subsystemNQN := generateNQN(nqnPrefix, volumeName)

	// Parse deleteStrategy from StorageClass parameters (default: "delete")
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}

	// Parse markAdoptable from StorageClass parameters (default: false)
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	// Extract adoption metadata from CSI parameters
	pvcName := params["csi.storage.k8s.io/pvc/name"]
	pvcNamespace := params["csi.storage.k8s.io/pvc/namespace"]
	storageClass := params["csi.storage.k8s.io/sc/name"]

	// Optional compression setting
	compression := params["compression"]

	return &nvmeofVolumeParams{
		pool:              pool,
		server:            server,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		subvolumeName:     volumeName,
		subsystemNQN:      subsystemNQN,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		comment:           comment,
		compression:       compression,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
		nrIOQueues:        params["nvmeof.nr-io-queues"],
		queueSize:         params["nvmeof.queue-size"],
	}, nil
}

// buildNVMeOFVolumeResponse builds the CreateVolumeResponse for an NVMe-oF volume.
func buildNVMeOFVolumeResponse(volumeName, server string, subvol *nastyapi.Subvolume, subsystem *nastyapi.NVMeOFSubsystem, capacity int64) *csi.CreateVolumeResponse {
	// Volume ID is pool/subvolumeName for O(1) lookups
	volumeID := subvol.Pool + "/" + subvol.Name

	meta := VolumeMetadata{
		Name:                volumeName,
		Protocol:            ProtocolNVMeOF,
		DatasetID:           volumeID,
		DatasetName:         subvol.Name,
		Server:              server,
		NVMeOFSubsystemUUID: subsystem.ID,
		NVMeOFNQN:           subsystem.NQN,
	}

	// Build volume context with all necessary metadata
	volumeContext := buildVolumeContext(meta)
	// NSID is always 1 with independent subsystem architecture (one subsystem per volume)
	volumeContext[VolumeContextKeyNSID] = "1"
	volumeContext[VolumeContextKeyExpectedCapacity] = strconv.FormatInt(capacity, 10)

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolNVMeOF, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// createNVMeOFVolume creates an NVMe-oF volume (block subvolume + NVMe-oF subsystem with namespace).
func (s *ControllerService) createNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "create")
	klog.V(4).Info("Creating NVMe-oF volume")

	// Validate and extract parameters
	params, err := validateNVMeOFParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating NVMe-oF volume: %s with size: %d bytes, NQN: %s",
		params.volumeName, params.requestedCapacity, params.subsystemNQN)

	// Check if subvolume already exists (idempotency)
	existingSubvol, err := s.apiClient.GetSubvolume(ctx, params.pool, params.subvolumeName)
	if err != nil && !isNotFoundError(err) {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to check for existing subvolume: %v", err)
	}

	// Handle existing subvolume (idempotency check)
	if existingSubvol != nil {
		resp, done, handleErr := s.handleExistingNVMeOFSubvolume(ctx, params, existingSubvol, timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// Subvolume exists but no subsystem — continue with subsystem creation
	}

	// Step 1: Create or reuse block subvolume
	subvol, subvolIsNew, err := s.getOrCreateSubvolume(ctx, params.pool, params.subvolumeName,
		"block", params.comment, params.compression, params.requestedCapacity, timer)
	if err != nil {
		return nil, err
	}

	// Determine block device path
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

	// Step 2: Create NVMe-oF subsystem with namespace (NASty quick-create API)
	subsystemParams := nastyapi.NVMeOFCreateParams{
		Name:       params.volumeName,
		DevicePath: blockDevice,
	}

	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, subsystemParams)
	if err != nil {
		// Cleanup: only delete subvolume if we just created it
		if subvolIsNew {
			klog.Errorf("Failed to create NVMe-oF subsystem, cleaning up newly-created subvolume: %v", err)
			if delErr := s.apiClient.DeleteSubvolume(ctx, params.pool, params.subvolumeName); delErr != nil {
				klog.Errorf("Failed to cleanup subvolume: %v", delErr)
			}
		} else {
			klog.Warningf("Failed to create NVMe-oF subsystem: %v (skipping subvolume cleanup — volume was pre-existing)", err)
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem '%s': %v", params.subsystemNQN, err)
	}

	// Wait for NVMe-oF target to fully initialize the namespace
	const namespaceInitDelay = 3 * time.Second
	klog.V(4).Infof("Waiting %v for NVMe-oF namespace to be fully initialized", namespaceInitDelay)
	time.Sleep(namespaceInitDelay)

	// Step 3: Store xattr properties for metadata tracking
	props := nastyapi.NVMeOFVolumePropertiesV1(nastyapi.NVMeOFVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		SubsystemIDStr: subsystem.ID,
		SubsystemNQN:   subsystem.NQN,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
		ClusterID:      s.clusterID,
	})
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, params.pool, params.subvolumeName, props); err != nil {
		klog.Warningf("Failed to set xattr properties on %s: %v (volume created successfully)", params.subvolumeName, err)
	}

	klog.Infof("Created NVMe-oF volume: %s (subvolume: %s/%s, subsystem: %s, NQN: %s)",
		params.volumeName, params.pool, params.subvolumeName, subsystem.ID, subsystem.NQN)

	resp := buildNVMeOFVolumeResponse(params.volumeName, params.server, subvol, subsystem, params.requestedCapacity)
	injectQueueParams(resp.Volume.VolumeContext, params.nrIOQueues, params.queueSize)

	timer.ObserveSuccess()
	return resp, nil
}

// handleExistingNVMeOFSubvolume handles the case when a block subvolume already exists (idempotency).
func (s *ControllerService) handleExistingNVMeOFSubvolume(ctx context.Context, params *nvmeofVolumeParams, existingSubvol *nastyapi.Subvolume, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
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

	// Check if subsystem exists by stored NQN
	var storedNQN string
	if existingSubvol.Properties != nil {
		storedNQN = existingSubvol.Properties[nastyapi.PropertyNVMeSubsystemNQN]
	}

	if storedNQN != "" {
		subsystem, err := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, storedNQN)
		if err == nil && subsystem != nil {
			klog.V(4).Infof("NVMe-oF volume already exists (subsystem: %s, NQN: %s), returning existing volume",
				subsystem.ID, subsystem.NQN)
			resp := buildNVMeOFVolumeResponse(params.volumeName, params.server, existingSubvol, subsystem, existingCapacity)
			injectQueueParams(resp.Volume.VolumeContext, params.nrIOQueues, params.queueSize)
			timer.ObserveSuccess()
			return resp, true, nil
		}
		klog.V(4).Infof("Stored subsystem NQN %s not found, will recreate: %v", storedNQN, err)
	}

	// Subvolume exists but no subsystem — signal caller to proceed with subsystem creation
	return nil, false, nil
}

// deleteNVMeOFVolume deletes an NVMe-oF volume and all associated resources.
// Subvolume is deleted first; if it fails, NVMe-oF subsystem is preserved to prevent orphaning.
//
//nolint:gocyclo,dupl // Complexity from ownership verification; delete pattern shared with iSCSI
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "delete")
	klog.Infof("Deleting NVMe-oF volume: %s (subvolume: %s, subsystem: %s)",
		meta.Name, meta.DatasetID, meta.NVMeOFSubsystemUUID)

	pool, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	// Step 0: Verify ownership via xattr properties before deletion
	subvol, err := s.apiClient.GetSubvolume(ctx, pool, name)
	if err != nil {
		if isNotFoundError(err) {
			klog.V(4).Infof("Subvolume %s not found, assuming already deleted (idempotency)", meta.DatasetID)
			timer.ObserveSuccess()
			return &csi.DeleteVolumeResponse{}, nil
		}
		klog.Warningf("Failed to verify subvolume ownership: %v (continuing with deletion)", err)
	}

	if subvol != nil && subvol.Properties != nil {
		props := subvol.Properties
		if managedBy, ok := props[nastyapi.PropertyManagedBy]; ok && managedBy != nastyapi.ManagedByValue {
			timer.ObserveError()
			return nil, status.Errorf(codes.FailedPrecondition,
				"Subvolume %s is not managed by nasty-csi (managed_by=%s)", meta.DatasetID, managedBy)
		}

		if deleteStrategy, ok := props[nastyapi.PropertyDeleteStrategy]; ok && deleteStrategy == nastyapi.DeleteStrategyRetain {
			klog.Infof("Volume %s has delete strategy 'retain', skipping deletion", meta.Name)
			timer.ObserveSuccess()
			return &csi.DeleteVolumeResponse{}, nil
		}

		// Update metadata with stored subsystem UUID
		if storedSubsystemID, ok := props[nastyapi.PropertyNVMeSubsystemID]; ok && storedSubsystemID != "" {
			if meta.NVMeOFSubsystemUUID == "" {
				meta.NVMeOFSubsystemUUID = storedSubsystemID
			}
		}
		if storedNQN, ok := props[nastyapi.PropertyNVMeSubsystemNQN]; ok && storedNQN != "" {
			if meta.NVMeOFNQN == "" {
				meta.NVMeOFNQN = storedNQN
			}
		}
	}

	// Step 1: Delete subvolume first (prevents orphaning NVMe-oF subsystem)
	firstErr := s.apiClient.DeleteSubvolume(ctx, pool, name)
	if firstErr != nil && !isNotFoundError(firstErr) {
		// Retry after snapshot cleanup
		klog.Infof("Direct deletion failed for %s: %v — cleaning up snapshots before retry",
			meta.DatasetID, firstErr)

		retryConfig := retry.DeletionConfig("delete-nvmeof-subvol")
		err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
			deleteErr := s.apiClient.DeleteSubvolume(ctx, pool, name)
			if deleteErr != nil && isNotFoundError(deleteErr) {
				return nil
			}
			return deleteErr
		})

		if err != nil {
			klog.Errorf("Subvolume %s deletion failed — skipping NVMe-oF subsystem cleanup to avoid orphaning: %v", meta.DatasetID, err)
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal,
				"Failed to delete block subvolume %s: %v (NVMe-oF subsystem preserved to prevent orphaning)", meta.DatasetID, err)
		}
	}
	klog.V(4).Infof("Deleted block subvolume: %s", meta.DatasetID)

	// Step 2: Subvolume is gone — clean up NVMe-oF subsystem (best effort)
	// Try by UUID first, then by NQN
	subsystemID := meta.NVMeOFSubsystemUUID
	if subsystemID == "" && meta.NVMeOFNQN != "" {
		// Look up subsystem by NQN
		subsystem, lookupErr := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, meta.NVMeOFNQN)
		if lookupErr == nil && subsystem != nil {
			subsystemID = subsystem.ID
		}
	}

	if subsystemID != "" {
		if err := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystemID); err != nil {
			if !isNotFoundError(err) {
				klog.Warningf("Failed to delete NVMe-oF subsystem %s (subvolume already deleted): %v", subsystemID, err)
			}
		} else {
			klog.V(4).Infof("Deleted NVMe-oF subsystem: %s", subsystemID)
		}
	}

	// Clear volume capacity metric
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNVMeOF)

	klog.Infof("Deleted NVMe-oF volume: %s", meta.Name)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// expandNVMeOFVolume expands an NVMe-oF volume by updating the capacity property.
//
//nolint:dupl // Intentionally similar to NFS/iSCSI expansion logic
func (s *ControllerService) expandNVMeOFVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "expand")
	klog.V(4).Infof("Expanding NVMe-oF volume: %s (subvolume: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

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
		klog.Errorf("Failed to expand NVMe-oF subvolume %s: %v", meta.DatasetID, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"Failed to update capacity for NVMe-oF volume '%s'. Error: %v", meta.DatasetID, err)
	}

	klog.Infof("Expanded NVMe-oF volume: %s to %d bytes", meta.Name, requiredBytes)

	// Update volume capacity metric
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolNVMeOF, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: false, // NVMe-oF block volumes don't need node-side expansion
	}, nil
}

// setupNVMeOFVolumeFromClone sets up NVMe-oF infrastructure for a cloned volume.
// TODO: Implement when NASty supports subvolume cloning.
func (s *ControllerService) setupNVMeOFVolumeFromClone(_ context.Context, _ *csi.CreateVolumeRequest, _ *nastyapi.Subvolume, _ string, _ *cloneInfo) (*csi.CreateVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NVMe-oF volume cloning is not yet supported by the NASty backend")
}

// adoptNVMeOFVolume adopts an orphaned NVMe-oF volume by recreating missing NASty resources.
// This enables GitOps workflows where clusters are recreated and need to adopt existing volumes.
func (s *ControllerService) adoptNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest, subvol *nastyapi.Subvolume, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting NVMe-oF volume: %s (subvolume=%s/%s)", volumeName, subvol.Pool, subvol.Name)

	// Get server parameter
	server := params["server"]
	if server == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	// Try to find existing subsystem by stored NQN in subvolume properties
	var subsystem *nastyapi.NVMeOFSubsystem
	if subvol.Properties != nil {
		if storedNQN := subvol.Properties[nastyapi.PropertyNVMeSubsystemNQN]; storedNQN != "" {
			existingSubsystem, lookupErr := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, storedNQN)
			if lookupErr == nil && existingSubsystem != nil {
				subsystem = existingSubsystem
				klog.Infof("Found existing NVMe-oF subsystem for adopted volume: ID=%s, NQN=%s", subsystem.ID, subsystem.NQN)
			}
		}
	}

	// Ensure block device path is available
	if subvol.BlockDevice == nil || *subvol.BlockDevice == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Block subvolume %s/%s has no block device path", subvol.Pool, subvol.Name)
	}
	blockDevice := *subvol.BlockDevice

	// If no subsystem found, create new one
	if subsystem == nil {
		klog.Infof("Creating new NVMe-oF subsystem for adopted volume: %s", volumeName)

		nqnPrefix := params["subsystemNQN"]
		if nqnPrefix == "" {
			nqnPrefix = defaultNQNPrefix
		}
		subsystemNQN := generateNQN(nqnPrefix, volumeName)

		newSubsystem, createErr := s.apiClient.CreateNVMeOFSubsystem(ctx, nastyapi.NVMeOFCreateParams{
			Name:       volumeName,
			DevicePath: blockDevice,
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem for adopted volume (NQN: %s): %v", subsystemNQN, createErr)
		}
		subsystem = newSubsystem
		klog.Infof("Created NVMe-oF subsystem for adopted volume: ID=%s, NQN=%s", subsystem.ID, subsystem.NQN)
	}

	// Update xattr properties with new IDs
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := nastyapi.NVMeOFVolumePropertiesV1(nastyapi.NVMeOFVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		SubsystemIDStr: subsystem.ID,
		SubsystemNQN:   subsystem.NQN,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
		ClusterID:      s.clusterID,
	})
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, subvol.Pool, subvol.Name, props); propErr != nil {
		klog.Warningf("Failed to update xattr properties on adopted volume %s/%s: %v", subvol.Pool, subvol.Name, propErr)
	}

	klog.Infof("Successfully adopted NVMe-oF volume: %s (subsystem=%s, NQN=%s)", volumeName, subsystem.ID, subsystem.NQN)
	timer.ObserveSuccess()

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolNVMeOF, requestedCapacity)

	nrIOQueues := params["nvmeof.nr-io-queues"]
	queueSize := params["nvmeof.queue-size"]
	resp := buildNVMeOFVolumeResponse(volumeName, server, subvol, subsystem, requestedCapacity)
	injectQueueParams(resp.Volume.VolumeContext, nrIOQueues, queueSize)

	return resp, nil
}
