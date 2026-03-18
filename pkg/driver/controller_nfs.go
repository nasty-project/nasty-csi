// Package driver implements NFS-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
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

// nfsVolumeParams holds validated parameters for NFS volume creation.
type nfsVolumeParams struct {
	nfsClients        []nastyapi.NFSClient
	pool              string
	volumeName        string
	subvolumeName     string // short name within pool (e.g., "pvc-xxx")
	subvolumeID       string // full identifier: "pool/subvolumeName"
	deleteStrategy    string
	server            string
	comment           string
	compression       string
	pvcName           string
	pvcNamespace      string
	storageClass      string
	requestedCapacity int64
	markAdoptable     bool
}

// parseNFSClients parses the nfsClients StorageClass parameter into NFSClient slice.
// Format: "host1:options1,host2:options2" or "*:rw,no_root_squash" for wildcard.
// If empty, defaults to a wildcard client with rw,no_root_squash.
func parseNFSClients(clientsParam string) []nastyapi.NFSClient {
	if clientsParam == "" {
		return []nastyapi.NFSClient{
			{Host: "*", Options: "rw,no_root_squash"},
		}
	}

	var clients []nastyapi.NFSClient
	for _, entry := range strings.Split(clientsParam, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		client := nastyapi.NFSClient{Host: parts[0]}
		if len(parts) == 2 {
			client.Options = parts[1]
		} else {
			client.Options = "rw,no_root_squash"
		}
		clients = append(clients, client)
	}
	return clients
}

// validateNFSParams validates and extracts NFS volume parameters from the request.
func validateNFSParams(req *csi.CreateVolumeRequest) (*nfsVolumeParams, error) {
	params := req.GetParameters()

	pool := params["pool"]
	if pool == "" {
		return nil, status.Error(codes.InvalidArgument, "pool parameter is required for NFS volumes")
	}

	// Server parameter - optional for testing with default value
	server := params["server"]
	if server == "" {
		server = defaultServerAddress // Default for testing
		klog.V(4).Infof("No server parameter provided, using default: %s", defaultServerAddress)
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

	// Parse NFS clients from StorageClass parameters
	nfsClients := parseNFSClients(params["nfsClients"])

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

	// Compression can be set at top level
	compression := params["compression"]

	return &nfsVolumeParams{
		pool:              pool,
		server:            server,
		volumeName:        volumeName,
		subvolumeName:     volumeName,
		subvolumeID:       pool + "/" + volumeName,
		requestedCapacity: requestedCapacity,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		nfsClients:        nfsClients,
		comment:           comment,
		compression:       compression,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
	}, nil
}

// parseCapacityFromComment parses the capacity from an NFS share comment.
// Returns 0 if capacity cannot be parsed (backward compatibility).
func parseCapacityFromComment(comment string) int64 {
	if comment == "" {
		return 0
	}
	var parsedCapacity int64
	_, err := fmt.Sscanf(comment, "CSI Volume: %s | Capacity: %d", new(string), &parsedCapacity)
	if err != nil {
		return 0
	}
	return parsedCapacity
}

// buildNFSVolumeResponseFromSubvolume builds the CreateVolumeResponse for an NFS volume.
func buildNFSVolumeResponseFromSubvolume(volumeName, server string, subvol *nastyapi.Subvolume, nfsShare *nastyapi.NFSShare, capacity int64) *csi.CreateVolumeResponse {
	volumeID := subvol.Pool + "/" + subvol.Name

	meta := VolumeMetadata{
		Name:         volumeName,
		Protocol:     ProtocolNFS,
		DatasetID:    volumeID,
		DatasetName:  subvol.Name,
		Server:       server,
		NFSShareUUID: nfsShare.ID,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = subvol.Path

	// Record volume capacity metric
	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolNFS, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// nfsPropertiesV1 builds xattr property map for an NFS subvolume.
func nfsPropertiesV1(params *nfsVolumeParams, shareID, sharePath string, clusterID string) map[string]string {
	return nastyapi.NFSVolumePropertiesV1(nastyapi.NFSVolumeParams{
		VolumeID:       params.volumeName,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		ShareIDStr:     shareID,
		SharePath:      sharePath,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
		ClusterID:      clusterID,
	})
}

// handleExistingNFSSubvolume handles idempotency when a subvolume already exists.
func (s *ControllerService) handleExistingNFSSubvolume(ctx context.Context, params *nfsVolumeParams, existingSubvol *nastyapi.Subvolume, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("Subvolume %s already exists, checking idempotency", existingSubvol.Name)

	// Check existing NFS shares to find one for this subvolume path
	shares, err := s.apiClient.ListNFSShares(ctx)
	if err != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to list NFS shares: %v", err)
	}

	var existingShare *nastyapi.NFSShare
	for i := range shares {
		if shares[i].Path == existingSubvol.Path {
			existingShare = &shares[i]
			break
		}
	}

	if existingShare == nil {
		// Subvolume exists but no NFS share - continue with share creation
		return nil, false, nil
	}

	klog.V(4).Infof("NFS volume already exists (share ID: %s), checking capacity compatibility", existingShare.ID)

	var existingCapacity int64
	if existingShare.Comment != nil {
		existingCapacity = parseCapacityFromComment(*existingShare.Comment)
	}

	// CSI spec: return AlreadyExists if volume exists with incompatible capacity
	if existingCapacity > 0 && existingCapacity != params.requestedCapacity {
		klog.Warningf("Volume %s exists with different capacity (existing: %d, requested: %d)",
			params.volumeName, existingCapacity, params.requestedCapacity)
		timer.ObserveError()
		return nil, false, status.Errorf(codes.AlreadyExists,
			"Volume %s already exists with different capacity (existing: %d bytes, requested: %d bytes)",
			params.volumeName, existingCapacity, params.requestedCapacity)
	}

	klog.V(4).Infof("Capacity is compatible, returning existing volume")

	// Ensure properties are set (handles retry after context expired during property-setting)
	s.ensureNFSSubvolumeProperties(ctx, params, existingSubvol, existingShare)

	capacityToReturn := params.requestedCapacity
	if existingCapacity > 0 {
		capacityToReturn = existingCapacity
	}

	resp := buildNFSVolumeResponseFromSubvolume(params.volumeName, params.server, existingSubvol, existingShare, capacityToReturn)
	timer.ObserveSuccess()
	return resp, true, nil
}

// ensureNFSSubvolumeProperties checks if xattr properties are set and sets them if missing.
func (s *ControllerService) ensureNFSSubvolumeProperties(ctx context.Context, params *nfsVolumeParams, subvol *nastyapi.Subvolume, share *nastyapi.NFSShare) {
	// Read current properties from subvolume
	existing, err := s.apiClient.GetSubvolume(ctx, subvol.Pool, subvol.Name)
	if err != nil {
		klog.Warningf("Failed to check properties on subvolume %s/%s: %v (skipping property recovery)", subvol.Pool, subvol.Name, err)
		return
	}
	if existing.Properties != nil {
		if existing.Properties[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue {
			return // Properties already set
		}
	}

	klog.Infof("Recovering missing xattr properties on subvolume %s/%s (orphaned from interrupted creation)", subvol.Pool, subvol.Name)
	props := nfsPropertiesV1(params, share.ID, share.Path, s.clusterID)
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, subvol.Pool, subvol.Name, props); err != nil {
		klog.Warningf("Failed to recover xattr properties on subvolume %s/%s: %v (volume will still work)", subvol.Pool, subvol.Name, err)
	} else {
		klog.Infof("Successfully recovered xattr properties on subvolume %s/%s", subvol.Pool, subvol.Name)
	}
}

// createNFSShareForSubvolume creates an NFS share for a subvolume and stores xattr metadata for tracking.
// subvolumeIsNew indicates whether the subvolume was just created — if false, do NOT delete on failure.
func (s *ControllerService) createNFSShareForSubvolume(ctx context.Context, subvol *nastyapi.Subvolume, params *nfsVolumeParams, subvolumeIsNew bool, timer *metrics.OperationTimer) (*nastyapi.NFSShare, error) {
	comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", params.volumeName, params.requestedCapacity)
	enabled := true
	nfsShare, err := s.apiClient.CreateNFSShare(ctx, nastyapi.NFSShareCreateParams{
		Path:    subvol.Path,
		Comment: comment,
		Clients: params.nfsClients,
		Enabled: &enabled,
	})
	if err != nil {
		klog.Errorf("Failed to create NFS share for subvolume %s/%s (path: %s): %v", subvol.Pool, subvol.Name, subvol.Path, err)
		if subvolumeIsNew {
			if delErr := s.apiClient.DeleteSubvolume(ctx, subvol.Pool, subvol.Name); delErr != nil {
				klog.Errorf("Failed to cleanup subvolume after NFS share creation failure: %v", delErr)
			}
		} else {
			klog.Warningf("Skipping subvolume cleanup — subvolume was pre-existing")
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share for subvolume %s/%s: %v", subvol.Pool, subvol.Name, err)
	}

	klog.V(4).Infof("Created NFS share with ID: %s for path: %s", nfsShare.ID, nfsShare.Path)

	// Store xattr properties for CSI metadata tracking (Schema v1)
	props := nfsPropertiesV1(params, nfsShare.ID, nfsShare.Path, s.clusterID)
	klog.V(4).Infof("Storing xattr properties on subvolume %s/%s: deleteStrategy=%q", subvol.Pool, subvol.Name, params.deleteStrategy)
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, subvol.Pool, subvol.Name, props); err != nil {
		klog.Warningf("Failed to set xattr properties on subvolume %s/%s: %v (volume will still work)", subvol.Pool, subvol.Name, err)
	} else {
		klog.V(4).Infof("Successfully stored xattr properties on subvolume %s/%s", subvol.Pool, subvol.Name)
	}

	return nfsShare, nil
}

// createNFSVolume creates an NFS volume with a NASty subvolume and NFS share.
func (s *ControllerService) createNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "create")
	klog.V(4).Info("Creating NFS volume (NASty API)")

	// Validate and extract parameters
	params, err := validateNFSParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating subvolume: %s/%s with capacity: %d bytes", params.pool, params.subvolumeName, params.requestedCapacity)

	// Check if subvolume already exists (idempotency)
	existingSubvol, err := s.apiClient.GetSubvolume(ctx, params.pool, params.subvolumeName)
	if err != nil && !isNotFoundError(err) {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing subvolume: %v", err)
	}

	if existingSubvol != nil {
		resp, done, handleErr := s.handleExistingNFSSubvolume(ctx, params, existingSubvol, timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// Subvolume exists but no NFS share - continue with share creation below
	} else {
		// Create new subvolume
		createParams := nastyapi.SubvolumeCreateParams{
			Pool:          params.pool,
			Name:          params.subvolumeName,
			SubvolumeType: "filesystem",
			Comments:      params.comment,
		}
		if params.compression != "" {
			createParams.Compression = params.compression
		}
		if params.requestedCapacity > 0 {
			volsize := uint64(params.requestedCapacity)
			createParams.VolsizeBytes = &volsize
		}

		newSubvol, createErr := s.apiClient.CreateSubvolume(ctx, createParams)
		if createErr != nil {
			timer.ObserveError()
			return nil, createVolumeError(fmt.Sprintf("Failed to create subvolume %s/%s (%d bytes)", params.pool, params.subvolumeName, params.requestedCapacity), createErr)
		}
		existingSubvol = newSubvol
		klog.V(4).Infof("Created subvolume: %s/%s with path: %s", existingSubvol.Pool, existingSubvol.Name, existingSubvol.Path)
	}

	// Create NFS share for the subvolume
	isNew := existingSubvol != nil
	nfsShare, err := s.createNFSShareForSubvolume(ctx, existingSubvol, params, isNew, timer)
	if err != nil {
		return nil, err
	}

	// Build and return response
	resp := buildNFSVolumeResponseFromSubvolume(params.volumeName, params.server, existingSubvol, nfsShare, params.requestedCapacity)
	klog.Infof("Created NFS volume: %s (subvolume: %s/%s)", params.volumeName, params.pool, params.subvolumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// deleteNFSVolume deletes an NFS volume with ownership verification.
//
//nolint:gocyclo,gocognit // Complexity from ownership checks + CSI snapshot guard + idempotency
func (s *ControllerService) deleteNFSVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "delete")
	klog.V(4).Infof("Deleting NFS volume: %s (subvolumeID: %s, shareUUID: %s)", meta.Name, meta.DatasetID, meta.NFSShareUUID)

	// Parse pool and subvolume name from DatasetID ("pool/name")
	pool, subvolName, err := splitSubvolumeID(meta.DatasetID)
	if err != nil {
		// Fallback for legacy volumes: try by name
		pool = ""
		subvolName = meta.Name
		klog.V(4).Infof("Cannot parse subvolumeID %q, falling back to name %q", meta.DatasetID, meta.Name)
	}

	// Step 0: Verify ownership and read metadata from xattr properties
	deleteStrategy := nastyapi.DeleteStrategyDelete // Default
	shareUUID := meta.NFSShareUUID

	if pool != "" && subvolName != "" {
		subvol, getErr := s.apiClient.GetSubvolume(ctx, pool, subvolName)
		if getErr != nil {
			if isNotFoundError(getErr) {
				klog.V(4).Infof("Subvolume %s/%s not found, assuming already deleted (idempotency)", pool, subvolName)
				timer.ObserveSuccess()
				return &csi.DeleteVolumeResponse{}, nil
			}
			klog.Warningf("Failed to verify subvolume ownership via xattr properties: %v (continuing with deletion)", getErr)
		} else if subvol.Properties != nil {
			props := subvol.Properties

			// Verify ownership if properties exist
			if managedBy, ok := props[nastyapi.PropertyManagedBy]; ok && managedBy != nastyapi.ManagedByValue {
				klog.Errorf("Subvolume %s/%s is not managed by nasty-csi (managed_by=%s), refusing to delete", pool, subvolName, managedBy)
				timer.ObserveError()
				return nil, status.Errorf(codes.FailedPrecondition,
					"Subvolume %s/%s is not managed by nasty-csi (managed_by=%s)", pool, subvolName, managedBy)
			}

			// Verify volume name matches
			if volumeName, ok := props[nastyapi.PropertyCSIVolumeName]; ok {
				if volumeName != meta.Name {
					klog.Errorf("Subvolume %s/%s volume name mismatch: property=%s, requested=%s", pool, subvolName, volumeName, meta.Name)
					timer.ObserveError()
					return nil, status.Errorf(codes.FailedPrecondition,
						"Subvolume %s/%s volume name mismatch (stored=%s, requested=%s)", pool, subvolName, volumeName, meta.Name)
				}
			}

			// Read stored share UUID (may differ from metadata if share was re-created)
			if storedShareID, ok := props[nastyapi.PropertyNFSShareID]; ok && storedShareID != "" {
				if shareUUID == "" {
					shareUUID = storedShareID
				} else if storedShareID != shareUUID {
					klog.Warningf("NFS share UUID mismatch: stored=%s, metadata=%s (using stored)", storedShareID, shareUUID)
					shareUUID = storedShareID
				}
			}

			// Check deleteStrategy
			if strategy, ok := props[nastyapi.PropertyDeleteStrategy]; ok && strategy != "" {
				deleteStrategy = strategy
			}

			klog.V(4).Infof("Ownership verified for subvolume %s/%s via xattr properties", pool, subvolName)
		}
	}

	// Check if we should retain the volume instead of deleting
	if deleteStrategy == nastyapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has deleteStrategy=retain, skipping actual deletion (subvolume: %s, shareUUID: %s will be kept)",
			meta.Name, meta.DatasetID, shareUUID)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Step 1: Delete NFS share first
	if shareUUID != "" {
		klog.V(4).Infof("Deleting NFS share: UUID=%s", shareUUID)
		err := s.apiClient.DeleteNFSShare(ctx, shareUUID)
		switch {
		case err == nil:
			klog.V(4).Infof("Successfully deleted NFS share %s", shareUUID)
		case isNotFoundError(err):
			klog.V(4).Infof("NFS share %s not found, assuming already deleted (idempotency)", shareUUID)
		default:
			klog.Warningf("Failed to delete NFS share %s: %v (continuing with subvolume deletion)", shareUUID, err)
		}
	}

	// Step 2: Delete subvolume
	if pool == "" || subvolName == "" {
		klog.V(4).Infof("No subvolume pool/name available, skipping subvolume deletion")
	} else {
		klog.V(4).Infof("Deleting subvolume: %s/%s", pool, subvolName)

		firstErr := s.apiClient.DeleteSubvolume(ctx, pool, subvolName)
		if firstErr != nil && !isNotFoundError(firstErr) {
			retryConfig := retry.DeletionConfig("delete-nfs-subvolume")
			retryErr := retry.WithRetryNoResult(ctx, retryConfig, func() error {
				deleteErr := s.apiClient.DeleteSubvolume(ctx, pool, subvolName)
				if deleteErr != nil && isNotFoundError(deleteErr) {
					return nil
				}
				return deleteErr
			})

			if retryErr != nil {
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal, "Failed to delete subvolume %s/%s: %v", pool, subvolName, retryErr)
			}
		}
		klog.V(4).Infof("Successfully deleted subvolume %s/%s", pool, subvolName)
	}

	klog.Infof("Deleted NFS volume: %s", meta.Name)
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNFS)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// splitSubvolumeID splits "pool/name" into (pool, name).
func splitSubvolumeID(subvolumeID string) (string, string, error) {
	idx := strings.Index(subvolumeID, "/")
	if idx < 0 || idx == len(subvolumeID)-1 {
		return "", "", fmt.Errorf("invalid subvolume ID %q: expected pool/name format", subvolumeID)
	}
	return subvolumeID[:idx], subvolumeID[idx+1:], nil
}

// setupNFSVolumeFromClone sets up an NFS share for a cloned subvolume.
// TODO: Clone-from-snapshot operations are not yet supported by the NASty API.
func (s *ControllerService) setupNFSVolumeFromClone(_ context.Context, _ *csi.CreateVolumeRequest, _ *nastyapi.Subvolume, _ string, _ *cloneInfo) (*csi.CreateVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "TODO: clone-from-snapshot not yet supported by NASty API")
}

// adoptNFSVolume adopts an orphaned NFS volume by re-creating its NFS share.
func (s *ControllerService) adoptNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest, subvol *nastyapi.Subvolume, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting NFS volume: %s (subvolume=%s/%s)", volumeName, subvol.Pool, subvol.Name)

	// Get server parameter
	server := params["server"]
	if server == "" {
		server = defaultServerAddress
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}

	if subvol.Path == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Subvolume %s/%s has no path", subvol.Pool, subvol.Name)
	}

	// Check if an NFS share already exists for this path
	existingShares, err := s.apiClient.ListNFSShares(ctx)
	if err != nil {
		klog.Warningf("Failed to list NFS shares for %s: %v", subvol.Path, err)
	}

	var nfsShare *nastyapi.NFSShare
	for i := range existingShares {
		if existingShares[i].Path == subvol.Path {
			nfsShare = &existingShares[i]
			klog.Infof("Found existing NFS share for adopted volume: ID=%s, path=%s", nfsShare.ID, nfsShare.Path)
			break
		}
	}

	if nfsShare == nil {
		// Create new NFS share
		klog.Infof("Creating NFS share for adopted volume: %s", subvol.Path)
		comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", volumeName, requestedCapacity)
		enabled := true
		newShare, createErr := s.apiClient.CreateNFSShare(ctx, nastyapi.NFSShareCreateParams{
			Path:    subvol.Path,
			Comment: comment,
			Clients: []nastyapi.NFSClient{
				{Host: "*", Options: "rw,no_root_squash"},
			},
			Enabled: &enabled,
		})
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create NFS share for adopted volume: %v", createErr)
		}
		nfsShare = newShare
		klog.Infof("Created NFS share for adopted volume: ID=%s, path=%s", nfsShare.ID, nfsShare.Path)
	}

	// Update xattr properties with new share ID
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := nastyapi.NFSVolumePropertiesV1(nastyapi.NFSVolumeParams{
		VolumeID:       volumeName,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		ShareIDStr:     nfsShare.ID,
		SharePath:      nfsShare.Path,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
		ClusterID:      s.clusterID,
	})
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, subvol.Pool, subvol.Name, props); propErr != nil {
		klog.Warningf("Failed to update xattr properties on adopted volume %s/%s: %v", subvol.Pool, subvol.Name, propErr)
	}

	// Build response
	meta := VolumeMetadata{
		Name:         volumeName,
		Protocol:     ProtocolNFS,
		DatasetID:    subvol.Pool + "/" + subvol.Name,
		DatasetName:  subvol.Name,
		Server:       server,
		NFSShareUUID: nfsShare.ID,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = subvol.Path

	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolNFS, requestedCapacity)

	klog.Infof("Successfully adopted NFS volume: %s (shareID=%s)", volumeName, nfsShare.ID)
	timer.ObserveSuccess()

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      subvol.Pool + "/" + subvol.Name,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// expandNFSVolume expands an NFS volume by updating the subvolume capacity.
//
//nolint:dupl // Similar to expandNVMeOFVolume but with different protocol
func (s *ControllerService) expandNFSVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "expand")
	klog.V(4).Infof("Expanding NFS volume: %s (subvolumeID: %s) to %d bytes", meta.Name, meta.DatasetID, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "subvolume ID not found in volume metadata")
	}

	pool, subvolName, err := splitSubvolumeID(meta.DatasetID)
	if err != nil {
		klog.Warningf("Cannot parse subvolumeID %q for expansion: %v", meta.DatasetID, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "invalid subvolume ID %q: %v", meta.DatasetID, err)
	}

	klog.V(4).Infof("Expanding NFS subvolume %s/%s to %d bytes", pool, subvolName, requiredBytes)

	// Update capacity via xattr property (NASty handles quota enforcement via xattr)
	_, err = s.apiClient.SetSubvolumeProperties(ctx, pool, subvolName, map[string]string{
		nastyapi.PropertyCapacityBytes: fmt.Sprintf("%d", requiredBytes),
	})
	if err != nil {
		klog.Errorf("Failed to update capacity xattr for %s/%s: %v", pool, subvolName, err)
	}

	klog.Infof("Expanded NFS volume: %s to %d bytes", meta.Name, requiredBytes)
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolNFS, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: false, // NFS volumes don't require node-side expansion
	}, nil
}

// getOrCreateSubvolume gets an existing subvolume or creates a new one.
// Returns (subvolume, isNewlyCreated, error).
func (s *ControllerService) getOrCreateSubvolume(ctx context.Context, pool, name, subvolumeType, comment, compression string, requestedCapacity int64, timer *metrics.OperationTimer) (*nastyapi.Subvolume, bool, error) {
	// Try to get existing subvolume
	existing, err := s.apiClient.GetSubvolume(ctx, pool, name)
	if err == nil && existing != nil {
		klog.V(4).Infof("Using existing subvolume: %s/%s with path: %s", pool, name, existing.Path)
		return existing, false, nil
	}
	if err != nil && !isNotFoundError(err) {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to query subvolume %s/%s: %v", pool, name, err)
	}

	// Build creation parameters
	createParams := nastyapi.SubvolumeCreateParams{
		Pool:          pool,
		Name:          name,
		SubvolumeType: subvolumeType,
		Comments:      comment,
	}
	if compression != "" {
		createParams.Compression = compression
	}
	if requestedCapacity > 0 {
		volsize := uint64(requestedCapacity)
		createParams.VolsizeBytes = &volsize
	}

	subvol, err := s.apiClient.CreateSubvolume(ctx, createParams)
	if err != nil {
		timer.ObserveError()
		return nil, false, createVolumeError(fmt.Sprintf("Failed to create subvolume %s/%s (%d bytes)", pool, name, requestedCapacity), err)
	}

	klog.V(4).Infof("Created subvolume: %s/%s with path: %s", pool, name, subvol.Path)
	return subvol, true, nil
}
