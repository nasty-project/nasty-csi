// Package driver implements NFS-specific CSI controller operations.
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

// nfsVolumeParams holds validated parameters for NFS volume creation.
type nfsVolumeParams struct {
	filesystem        string
	volumeName        string
	subvolumeName     string // short name within filesystem (e.g., "pvc-xxx")
	subvolumeID       string // full identifier: "filesystem/subvolumeName"
	deleteStrategy    string
	server            string
	comment           string
	compression       string
	foregroundTarget  string
	backgroundTarget  string
	promoteTarget     string
	metadataTarget    string
	pvcName           string
	pvcNamespace      string
	storageClass      string
	nfsClients        []nastyapi.NFSClient
	requestedCapacity int64
	dataReplicas      uint32
	markAdoptable     bool
	encrypted         bool
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

	filesystem := params["filesystem"]
	if filesystem == "" {
		return nil, status.Error(codes.InvalidArgument, "filesystem parameter is required for NFS volumes")
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

	// Compression and tiering can be set at top level
	compression := params["compression"]
	foregroundTarget := params["foregroundTarget"]
	backgroundTarget := params["backgroundTarget"]
	promoteTarget := params["promoteTarget"]
	metadataTarget := params["metadataTarget"]
	dataReplicas, err := parseDataReplicas(params["dataReplicas"])
	if err != nil {
		return nil, err
	}

	return &nfsVolumeParams{
		filesystem:        filesystem,
		server:            server,
		volumeName:        volumeName,
		subvolumeName:     volumeName,
		subvolumeID:       filesystem + "/" + volumeName,
		requestedCapacity: requestedCapacity,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		nfsClients:        nfsClients,
		comment:           comment,
		compression:       compression,
		foregroundTarget:  foregroundTarget,
		backgroundTarget:  backgroundTarget,
		promoteTarget:     promoteTarget,
		metadataTarget:    metadataTarget,
		dataReplicas:      dataReplicas,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
		encrypted:         strings.EqualFold(params["encryption"], "true"),
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
//
//nolint:dupl // Protocol-specific response builders intentionally follow the same pattern
func buildNFSVolumeResponseFromSubvolume(volumeName, server string, subvol *nastyapi.Subvolume, nfsShare *nastyapi.NFSShare, capacity int64) *csi.CreateVolumeResponse {
	volumeID := subvol.Filesystem + "/" + subvol.Name

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
func nfsPropertiesV1(params *nfsVolumeParams, clusterID string) map[string]string {
	return nastyapi.VolumeProperties(nastyapi.VolumeParams{
		VolumeID:       params.volumeName,
		Protocol:       nastyapi.ProtocolNFS,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
		ClusterID:      clusterID,
		Encrypted:      params.encrypted,
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
func (s *ControllerService) ensureNFSSubvolumeProperties(ctx context.Context, params *nfsVolumeParams, subvol *nastyapi.Subvolume, _ *nastyapi.NFSShare) {
	// Read current properties from subvolume
	existing, err := s.apiClient.GetSubvolume(ctx, subvol.Filesystem, subvol.Name)
	if err != nil {
		klog.Warningf("Failed to check properties on subvolume %s/%s: %v (skipping property recovery)", subvol.Filesystem, subvol.Name, err)
		return
	}
	if existing.Properties != nil {
		if existing.Properties[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue {
			return // Properties already set
		}
	}

	klog.Infof("Recovering missing xattr properties on subvolume %s/%s (orphaned from interrupted creation)", subvol.Filesystem, subvol.Name)
	props := nfsPropertiesV1(params, s.clusterID)
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, subvol.Filesystem, subvol.Name, props); err != nil {
		klog.Warningf("Failed to recover xattr properties on subvolume %s/%s: %v (volume will still work)", subvol.Filesystem, subvol.Name, err)
	} else {
		klog.Infof("Successfully recovered xattr properties on subvolume %s/%s", subvol.Filesystem, subvol.Name)
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
		klog.Errorf("Failed to create NFS share for subvolume %s/%s (path: %s): %v", subvol.Filesystem, subvol.Name, subvol.Path, err)
		if subvolumeIsNew {
			if delErr := s.apiClient.DeleteSubvolume(ctx, subvol.Filesystem, subvol.Name); delErr != nil {
				klog.Errorf("Failed to cleanup subvolume after NFS share creation failure: %v", delErr)
			}
		} else {
			klog.Warningf("Skipping subvolume cleanup — subvolume was pre-existing")
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NFS share for subvolume %s/%s: %v", subvol.Filesystem, subvol.Name, err)
	}

	klog.V(4).Infof("Created NFS share with ID: %s for path: %s", nfsShare.ID, nfsShare.Path)

	// Store xattr properties for CSI metadata tracking (Schema v1)
	props := nfsPropertiesV1(params, s.clusterID)
	klog.V(4).Infof("Storing xattr properties on subvolume %s/%s: deleteStrategy=%q", subvol.Filesystem, subvol.Name, params.deleteStrategy)
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, subvol.Filesystem, subvol.Name, props); err != nil {
		klog.Warningf("Failed to set xattr properties on subvolume %s/%s: %v (volume will still work)", subvol.Filesystem, subvol.Name, err)
	} else {
		klog.V(4).Infof("Successfully stored xattr properties on subvolume %s/%s", subvol.Filesystem, subvol.Name)
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

	// Per-PVC adoption annotation overrides StorageClass default
	if !params.markAdoptable && s.pvcHasAdoptableAnnotation(ctx, req.GetParameters()) {
		params.markAdoptable = true
	}

	klog.V(4).Infof("Creating subvolume: %s/%s with capacity: %d bytes", params.filesystem, params.subvolumeName, params.requestedCapacity)

	// Check if subvolume already exists (idempotency)
	existingSubvol, err := s.apiClient.GetSubvolume(ctx, params.filesystem, params.subvolumeName)
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
			Filesystem:    params.filesystem,
			Name:          params.subvolumeName,
			SubvolumeType: "filesystem",
			Comments:      params.comment,
		}
		if params.compression != "" {
			createParams.Compression = params.compression
		}
		if params.foregroundTarget != "" {
			createParams.ForegroundTarget = params.foregroundTarget
		}
		if params.backgroundTarget != "" {
			createParams.BackgroundTarget = params.backgroundTarget
		}
		if params.promoteTarget != "" {
			createParams.PromoteTarget = params.promoteTarget
		}
		if params.metadataTarget != "" {
			createParams.MetadataTarget = params.metadataTarget
		}
		if params.dataReplicas > 0 {
			createParams.DataReplicas = &params.dataReplicas
		}
		if params.requestedCapacity > 0 {
			volsize := uint64(params.requestedCapacity)
			createParams.VolsizeBytes = &volsize
		}

		newSubvol, createErr := s.apiClient.CreateSubvolume(ctx, createParams)
		if createErr != nil {
			timer.ObserveError()
			return nil, createVolumeError(fmt.Sprintf("Failed to create subvolume %s/%s (%d bytes)", params.filesystem, params.subvolumeName, params.requestedCapacity), createErr)
		}
		existingSubvol = newSubvol
		klog.V(4).Infof("Created subvolume: %s/%s with path: %s", existingSubvol.Filesystem, existingSubvol.Name, existingSubvol.Path)
	}

	// Create NFS share for the subvolume
	isNew := existingSubvol != nil
	nfsShare, err := s.createNFSShareForSubvolume(ctx, existingSubvol, params, isNew, timer)
	if err != nil {
		return nil, err
	}

	// Build and return response
	resp := buildNFSVolumeResponseFromSubvolume(params.volumeName, params.server, existingSubvol, nfsShare, params.requestedCapacity)
	klog.Infof("Created NFS volume: %s (subvolume: %s/%s)", params.volumeName, params.filesystem, params.subvolumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// deleteNFSVolume deletes an NFS volume with ownership verification.
func (s *ControllerService) deleteNFSVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) { //nolint:gocognit,gocyclo // deletion has many fallback paths by design
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "delete")
	klog.V(4).Infof("Deleting NFS volume: %s (subvolumeID: %s, shareUUID: %s)", meta.Name, meta.DatasetID, meta.NFSShareUUID)

	// Parse filesystem and subvolume name from DatasetID ("filesystem/name")
	filesystem, subvolName, err := splitSubvolumeID(meta.DatasetID)
	if err != nil {
		// Fallback for legacy volumes: try by name
		filesystem = ""
		subvolName = meta.Name
		klog.V(4).Infof("Cannot parse subvolumeID %q, falling back to name %q", meta.DatasetID, meta.Name)
	}

	// Step 0: Verify ownership and read metadata from xattr properties
	deleteStrategy := nastyapi.DeleteStrategyDelete // Default
	shareUUID := meta.NFSShareUUID

	if filesystem != "" && subvolName != "" {
		subvol, getErr := s.apiClient.GetSubvolume(ctx, filesystem, subvolName)
		if getErr != nil {
			if isNotFoundError(getErr) {
				klog.V(4).Infof("Subvolume %s/%s not found, assuming already deleted (idempotency)", filesystem, subvolName)
				timer.ObserveSuccess()
				return &csi.DeleteVolumeResponse{}, nil
			}
			klog.Warningf("Failed to verify subvolume ownership via xattr properties: %v (continuing with deletion)", getErr)
		} else if subvol.Properties != nil {
			props := subvol.Properties

			// Verify ownership if properties exist
			if managedBy, ok := props[nastyapi.PropertyManagedBy]; ok && managedBy != nastyapi.ManagedByValue {
				klog.Errorf("Subvolume %s/%s is not managed by nasty-csi (managed_by=%s), refusing to delete", filesystem, subvolName, managedBy)
				timer.ObserveError()
				return nil, status.Errorf(codes.FailedPrecondition,
					"Subvolume %s/%s is not managed by nasty-csi (managed_by=%s)", filesystem, subvolName, managedBy)
			}

			// Verify volume name matches — compare stored CSI name against
			// the subvolume name (DatasetName), not the full volume ID (Name)
			// which includes the filesystem prefix.
			if volumeName, ok := props[nastyapi.PropertyCSIVolumeName]; ok {
				if volumeName != meta.DatasetName {
					klog.Errorf("Subvolume %s/%s volume name mismatch: property=%s, requested=%s", filesystem, subvolName, volumeName, meta.DatasetName)
					timer.ObserveError()
					return nil, status.Errorf(codes.FailedPrecondition,
						"Subvolume %s/%s volume name mismatch (stored=%s, requested=%s)", filesystem, subvolName, volumeName, meta.DatasetName)
				}
			}

			// Check deleteStrategy
			if strategy, ok := props[nastyapi.PropertyDeleteStrategy]; ok && strategy != "" {
				deleteStrategy = strategy
			}

			klog.V(4).Infof("Ownership verified for subvolume %s/%s via xattr properties", filesystem, subvolName)
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
	// If share UUID is known, delete by UUID. Otherwise, look up by path.
	if shareUUID != "" {
		klog.Infof("Deleting NFS share by UUID: %s", shareUUID)
		err := s.apiClient.DeleteNFSShare(ctx, shareUUID)
		switch {
		case err == nil:
			klog.Infof("Successfully deleted NFS share %s", shareUUID)
		case isNotFoundError(err):
			klog.Infof("NFS share %s not found, assuming already deleted", shareUUID)
		default:
			klog.Warningf("Failed to delete NFS share %s: %v", shareUUID, err)
		}
	}
	// Fallback: if share UUID was empty or deletion failed, try to find and delete by path
	if filesystem != "" && subvolName != "" {
		sharePath := "/fs/" + filesystem + "/" + subvolName
		shares, listErr := s.apiClient.ListNFSShares(ctx)
		if listErr == nil {
			for _, share := range shares {
				if share.Path == sharePath {
					klog.Infof("Found NFS share by path %s (id=%s), deleting", sharePath, share.ID)
					if err := s.apiClient.DeleteNFSShare(ctx, share.ID); err != nil && !isNotFoundError(err) {
						klog.Warningf("Failed to delete NFS share %s by path lookup: %v", share.ID, err)
					}
					break
				}
			}
		}
	}

	// Step 2: Delete subvolume
	if filesystem == "" || subvolName == "" {
		klog.V(4).Infof("No subvolume filesystem/name available, skipping subvolume deletion")
	} else {
		klog.V(4).Infof("Deleting subvolume: %s/%s", filesystem, subvolName)

		firstErr := s.apiClient.DeleteSubvolume(ctx, filesystem, subvolName)
		if firstErr != nil && !isNotFoundError(firstErr) {
			retryConfig := retry.DeletionConfig("delete-nfs-subvolume")
			retryErr := retry.WithRetryNoResult(ctx, retryConfig, func() error {
				deleteErr := s.apiClient.DeleteSubvolume(ctx, filesystem, subvolName)
				if deleteErr != nil && isNotFoundError(deleteErr) {
					return nil
				}
				return deleteErr
			})

			if retryErr != nil {
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal, "Failed to delete subvolume %s/%s: %v", filesystem, subvolName, retryErr)
			}
		}
		klog.V(4).Infof("Successfully deleted subvolume %s/%s", filesystem, subvolName)
	}

	klog.Infof("Deleted NFS volume: %s", meta.Name)
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNFS)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// splitSubvolumeID splits "filesystem/name" into (filesystem, name).
func splitSubvolumeID(subvolumeID string) (filesystem, name string, err error) {
	idx := strings.Index(subvolumeID, "/")
	if idx < 0 || idx == len(subvolumeID)-1 {
		return "", "", fmt.Errorf("%w: %q expected filesystem/name format", ErrInvalidVolumeID, subvolumeID)
	}
	return subvolumeID[:idx], subvolumeID[idx+1:], nil
}

// adoptNFSVolume adopts an orphaned NFS volume by re-creating its NFS share.
func (s *ControllerService) adoptNFSVolume(ctx context.Context, req *csi.CreateVolumeRequest, subvol *nastyapi.Subvolume, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting NFS volume: %s (subvolume=%s/%s)", volumeName, subvol.Filesystem, subvol.Name)

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
		return nil, status.Errorf(codes.Internal, "Subvolume %s/%s has no path", subvol.Filesystem, subvol.Name)
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

	props := nastyapi.VolumeProperties(nastyapi.VolumeParams{
		VolumeID:       volumeName,
		Protocol:       nastyapi.ProtocolNFS,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		PVCName:        params["csi.storage.k8s.io/pvc/name"],
		PVCNamespace:   params["csi.storage.k8s.io/pvc/namespace"],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      markAdoptable,
		ClusterID:      s.clusterID,
	})
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, subvol.Filesystem, subvol.Name, props); propErr != nil {
		klog.Warningf("Failed to update xattr properties on adopted volume %s/%s: %v", subvol.Filesystem, subvol.Name, propErr)
	}

	// Build response
	meta := VolumeMetadata{
		Name:         volumeName,
		Protocol:     ProtocolNFS,
		DatasetID:    subvol.Filesystem + "/" + subvol.Name,
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
			VolumeId:      subvol.Filesystem + "/" + subvol.Name,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// expandNFSVolume expands an NFS volume by updating the subvolume capacity.
func (s *ControllerService) expandNFSVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNFS, "expand")
	klog.V(4).Infof("Expanding NFS volume: %s (subvolumeID: %s) to %d bytes", meta.Name, meta.DatasetID, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "subvolume ID not found in volume metadata")
	}

	filesystem, subvolName, err := splitSubvolumeID(meta.DatasetID)
	if err != nil {
		klog.Warningf("Cannot parse subvolumeID %q for expansion: %v", meta.DatasetID, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "invalid subvolume ID %q: %v", meta.DatasetID, err)
	}

	klog.V(4).Infof("Expanding NFS subvolume %s/%s to %d bytes", filesystem, subvolName, requiredBytes)

	// Resize the underlying subvolume
	//nolint:gosec // G115: CSI capacity is always non-negative
	if _, resizeErr := s.apiClient.ResizeSubvolume(ctx, filesystem, subvolName, uint64(requiredBytes)); resizeErr != nil {
		klog.Errorf("Failed to resize subvolume %s/%s: %v", filesystem, subvolName, resizeErr)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to resize subvolume: %v", resizeErr)
	}

	// Update capacity via xattr property (NASty handles quota enforcement via xattr)
	_, err = s.apiClient.SetSubvolumeProperties(ctx, filesystem, subvolName, map[string]string{
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requiredBytes, 10),
	})
	if err != nil {
		klog.Errorf("Failed to update capacity xattr for %s/%s: %v", filesystem, subvolName, err)
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
func (s *ControllerService) getOrCreateSubvolume(ctx context.Context, filesystem, name, subvolumeType, comment, compression, foregroundTarget, backgroundTarget, promoteTarget, metadataTarget string, dataReplicas uint32, requestedCapacity int64, timer *metrics.OperationTimer) (*nastyapi.Subvolume, bool, error) {
	// Try to get existing subvolume
	existing, err := s.apiClient.GetSubvolume(ctx, filesystem, name)
	if err == nil && existing != nil {
		klog.V(4).Infof("Using existing subvolume: %s/%s with path: %s", filesystem, name, existing.Path)
		return existing, false, nil
	}
	if err != nil && !isNotFoundError(err) {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to query subvolume %s/%s: %v", filesystem, name, err)
	}

	// Build creation parameters
	createParams := nastyapi.SubvolumeCreateParams{
		Filesystem:    filesystem,
		Name:          name,
		SubvolumeType: subvolumeType,
		Comments:      comment,
	}
	if compression != "" {
		createParams.Compression = compression
	}
	if foregroundTarget != "" {
		createParams.ForegroundTarget = foregroundTarget
	}
	if backgroundTarget != "" {
		createParams.BackgroundTarget = backgroundTarget
	}
	if promoteTarget != "" {
		createParams.PromoteTarget = promoteTarget
	}
	if metadataTarget != "" {
		createParams.MetadataTarget = metadataTarget
	}
	if dataReplicas > 0 {
		createParams.DataReplicas = &dataReplicas
	}
	if requestedCapacity > 0 {
		volsize := uint64(requestedCapacity)
		createParams.VolsizeBytes = &volsize
	}

	subvol, err := s.apiClient.CreateSubvolume(ctx, createParams)
	if err != nil {
		timer.ObserveError()
		return nil, false, createVolumeError(fmt.Sprintf("Failed to create subvolume %s/%s (%d bytes)", filesystem, name, requestedCapacity), err)
	}

	klog.V(4).Infof("Created subvolume: %s/%s with path: %s", filesystem, name, subvol.Path)
	return subvol, true, nil
}
