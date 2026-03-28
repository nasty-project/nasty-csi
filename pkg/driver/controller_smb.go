// Package driver implements SMB-specific CSI controller operations.
package driver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// smbVolumeParams holds validated parameters for SMB volume creation.
type smbVolumeParams struct {
	filesystem              string
	volumeName        string
	subvolumeName     string
	deleteStrategy    string
	server            string
	comment           string
	compression       string
	smbUsername       string
	pvcName           string
	pvcNamespace      string
	storageClass      string
	requestedCapacity int64
	markAdoptable     bool
}

// validateSMBParams validates and extracts SMB volume parameters from the request.
func validateSMBParams(req *csi.CreateVolumeRequest) (*smbVolumeParams, error) {
	params := req.GetParameters()

	filesystem := params["filesystem"]
	if filesystem == "" {
		return nil, status.Error(codes.InvalidArgument, "filesystem parameter is required for SMB volumes")
	}

	server := params["server"]
	if server == "" {
		server = defaultServerAddress
		klog.V(4).Infof("No server parameter provided, using default: %s", defaultServerAddress)
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // Default 1GB
	}

	volumeName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}

	comment, err := ResolveComment(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve comment template: %v", err)
	}

	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}

	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue
	compression := params["compression"]

	return &smbVolumeParams{
		filesystem:              filesystem,
		server:            server,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		subvolumeName:     volumeName,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		comment:           comment,
		compression:       compression,
		smbUsername:       params["smbUsername"],
		pvcName:           params["csi.storage.k8s.io/pvc/name"],
		pvcNamespace:      params["csi.storage.k8s.io/pvc/namespace"],
		storageClass:      params["csi.storage.k8s.io/sc/name"],
	}, nil
}

// buildSMBVolumeResponse builds the CreateVolumeResponse for an SMB volume.
//
//nolint:dupl // Protocol-specific response builders intentionally follow the same pattern
func buildSMBVolumeResponse(volumeName, server string, subvol *nastyapi.Subvolume, smbShare *nastyapi.SMBShare, capacity int64) *csi.CreateVolumeResponse {
	volumeID := subvol.Filesystem + "/" + subvol.Name
	meta := VolumeMetadata{
		Name:         volumeName,
		Protocol:     ProtocolSMB,
		DatasetID:    volumeID,
		DatasetName:  subvol.Name,
		Server:       server,
		SMBShareUUID: smbShare.ID,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = smbShare.Name

	metrics.SetVolumeCapacity(volumeID, metrics.ProtocolSMB, capacity)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacity,
			VolumeContext: volumeContext,
		},
	}
}

// handleExistingSMBSubvolume handles the case when a subvolume already exists (idempotency).
func (s *ControllerService) handleExistingSMBSubvolume(ctx context.Context, params *smbVolumeParams, existingSubvol *nastyapi.Subvolume, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("Subvolume %s/%s already exists, checking idempotency for SMB", existingSubvol.Filesystem, existingSubvol.Name)

	shares, err := s.apiClient.ListSMBShares(ctx)
	if err != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to list SMB shares: %v", err)
	}

	var existingShare *nastyapi.SMBShare
	for i := range shares {
		if shares[i].Path == existingSubvol.Path {
			existingShare = &shares[i]
			break
		}
	}

	if existingShare == nil {
		return nil, false, nil
	}
	klog.V(4).Infof("SMB volume already exists (share ID: %s), returning existing volume", existingShare.ID)

	resp := buildSMBVolumeResponse(params.volumeName, params.server, existingSubvol, existingShare, params.requestedCapacity)
	timer.ObserveSuccess()
	return resp, true, nil
}

// createSMBShareForSubvolume creates an SMB share for a subvolume and stores xattr properties.
func (s *ControllerService) createSMBShareForSubvolume(ctx context.Context, subvol *nastyapi.Subvolume, params *smbVolumeParams, subvolumeIsNew bool, timer *metrics.OperationTimer) (*nastyapi.SMBShare, error) {
	comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", params.volumeName, params.requestedCapacity)
	createParams := nastyapi.SMBShareCreateParams{
		Name:    params.volumeName,
		Path:    subvol.Path,
		Comment: comment,
	}
	if params.smbUsername != "" {
		createParams.ValidUsers = []string{params.smbUsername}
	}
	smbShare, err := s.apiClient.CreateSMBShare(ctx, createParams)
	if err != nil {
		klog.Errorf("Failed to create SMB share '%s' for subvolume %s/%s (path: %s): %v", params.volumeName, subvol.Filesystem, subvol.Name, subvol.Path, err)
		if subvolumeIsNew {
			if delErr := s.apiClient.DeleteSubvolume(ctx, subvol.Filesystem, subvol.Name); delErr != nil {
				klog.Errorf("Failed to cleanup subvolume after SMB share creation failure: %v", delErr)
			}
		} else {
			klog.Warningf("Skipping subvolume cleanup — subvolume was pre-existing")
		}
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create SMB share '%s' for subvolume %s/%s: %v", params.volumeName, subvol.Filesystem, subvol.Name, err)
	}

	klog.V(4).Infof("Created SMB share %q with ID: %s for path: %s", smbShare.Name, smbShare.ID, smbShare.Path)

	props := nastyapi.VolumeProperties(nastyapi.VolumeParams{
		VolumeID:       params.volumeName,
		Protocol:       nastyapi.ProtocolSMB,
		CapacityBytes:  params.requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: params.deleteStrategy,
		PVCName:        params.pvcName,
		PVCNamespace:   params.pvcNamespace,
		StorageClass:   params.storageClass,
		Adoptable:      params.markAdoptable,
		ClusterID:      s.clusterID,
	})
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, subvol.Filesystem, subvol.Name, props); err != nil {
		klog.Warningf("Failed to set xattr properties on subvolume %s/%s: %v (volume will still work)", subvol.Filesystem, subvol.Name, err)
	}

	return smbShare, nil
}

// createSMBVolume creates an SMB volume with a NASty subvolume and SMB share.
func (s *ControllerService) createSMBVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "create")
	klog.V(4).Info("Creating SMB volume")

	params, err := validateSMBParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	klog.V(4).Infof("Creating subvolume: %s/%s with capacity: %d bytes", params.filesystem, params.subvolumeName, params.requestedCapacity)

	// Check if subvolume already exists (idempotency)
	existingSubvol, err := s.apiClient.GetSubvolume(ctx, params.filesystem, params.subvolumeName)
	if err != nil && !isNotFoundError(err) {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to query existing subvolume: %v", err)
	}

	if existingSubvol != nil {
		resp, done, handleErr := s.handleExistingSMBSubvolume(ctx, params, existingSubvol, timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// Subvolume exists but no SMB share - continue with share creation
	} else {
		// Create new subvolume
		newSubvol, _, createErr := s.getOrCreateSubvolume(ctx, params.filesystem, params.subvolumeName, "filesystem", params.comment, params.compression, params.requestedCapacity, timer)
		if createErr != nil {
			return nil, createErr
		}
		existingSubvol = newSubvol
	}

	isNew := existingSubvol != nil
	smbShare, err := s.createSMBShareForSubvolume(ctx, existingSubvol, params, isNew, timer)
	if err != nil {
		return nil, err
	}

	resp := buildSMBVolumeResponse(params.volumeName, params.server, existingSubvol, smbShare, params.requestedCapacity)
	klog.Infof("Created SMB volume: %s", params.volumeName)
	timer.ObserveSuccess()
	return resp, nil
}

// deleteSMBVolume deletes an SMB volume with ownership verification.
func (s *ControllerService) deleteSMBVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "delete")
	klog.V(4).Infof("Deleting SMB volume: %s (dataset: %s, share UUID: %s)", meta.Name, meta.DatasetName, meta.SMBShareUUID)

	deleteStrategy := nastyapi.DeleteStrategyDelete
	shareUUID := meta.SMBShareUUID

	filesystem, subvolName, parseErr := splitSubvolumeID(meta.DatasetID)
	if parseErr == nil && filesystem != "" && subvolName != "" {
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

			if managedBy, ok := props[nastyapi.PropertyManagedBy]; ok && managedBy != nastyapi.ManagedByValue {
				timer.ObserveError()
				return nil, status.Errorf(codes.FailedPrecondition,
					"Subvolume %s/%s is not managed by nasty-csi (managed_by=%s)", filesystem, subvolName, managedBy)
			}

			if volumeName, ok := props[nastyapi.PropertyCSIVolumeName]; ok {
				if volumeName != meta.DatasetName {
					timer.ObserveError()
					return nil, status.Errorf(codes.FailedPrecondition,
						"Subvolume %s/%s volume name mismatch (stored=%s, requested=%s)", filesystem, subvolName, volumeName, meta.DatasetName)
				}
			}

			if strategy, ok := props[nastyapi.PropertyDeleteStrategy]; ok && strategy != "" {
				deleteStrategy = strategy
			}
		}
	}

	if deleteStrategy == nastyapi.DeleteStrategyRetain {
		klog.Infof("Volume %s has deleteStrategy=retain, skipping actual deletion", meta.Name)
		timer.ObserveSuccess()
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Step 1: Delete SMB share
	if shareUUID != "" {
		klog.Infof("Deleting SMB share by UUID: %s", shareUUID)
		err := s.apiClient.DeleteSMBShare(ctx, shareUUID)
		switch {
		case err == nil:
			klog.Infof("Successfully deleted SMB share %s", shareUUID)
		case isNotFoundError(err):
			klog.Infof("SMB share %s not found, assuming already deleted", shareUUID)
		default:
			klog.Warningf("Failed to delete SMB share %s: %v", shareUUID, err)
		}
	}
	// Fallback: if share UUID was empty or deletion failed, try to find and delete by path
	if parseErr == nil && filesystem != "" && subvolName != "" {
		sharePath := "/fs/" + filesystem + "/" + subvolName
		shares, listErr := s.apiClient.ListSMBShares(ctx)
		if listErr == nil {
			for _, share := range shares {
				if share.Path == sharePath {
					klog.Infof("Found SMB share by path %s (id=%s), deleting", sharePath, share.ID)
					if err := s.apiClient.DeleteSMBShare(ctx, share.ID); err != nil && !isNotFoundError(err) {
						klog.Warningf("Failed to delete SMB share %s by path lookup: %v", share.ID, err)
					}
					break
				}
			}
		}
	}

	// Step 2: Delete subvolume
	if parseErr == nil && filesystem != "" && subvolName != "" {
		klog.V(4).Infof("Deleting subvolume: %s/%s", filesystem, subvolName)
		firstErr := s.apiClient.DeleteSubvolume(ctx, filesystem, subvolName)
		if firstErr != nil && !isNotFoundError(firstErr) {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to delete subvolume %s/%s: %v", filesystem, subvolName, firstErr)
		}
		klog.V(4).Infof("Successfully deleted subvolume %s/%s", filesystem, subvolName)
	}

	klog.Infof("Deleted SMB volume: %s", meta.Name)
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolSMB)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// adoptSMBVolume adopts an orphaned SMB volume by re-creating its SMB share.
func (s *ControllerService) adoptSMBVolume(ctx context.Context, req *csi.CreateVolumeRequest, subvol *nastyapi.Subvolume, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "adopt")
	volumeName := req.GetName()
	klog.Infof("Adopting SMB volume: %s (subvolume=%s/%s)", volumeName, subvol.Filesystem, subvol.Name)

	server := params["server"]
	if server == "" {
		server = defaultServerAddress
	}

	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024
	}

	if subvol.Path == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Subvolume %s/%s has no path", subvol.Filesystem, subvol.Name)
	}

	existingShares, err := s.apiClient.ListSMBShares(ctx)
	if err != nil {
		klog.Warningf("Failed to list SMB shares for %s/%s: %v", subvol.Filesystem, subvol.Name, err)
	}

	var smbShare *nastyapi.SMBShare
	for i := range existingShares {
		if existingShares[i].Path == subvol.Path {
			smbShare = &existingShares[i]
			klog.Infof("Found existing SMB share for adopted volume: ID=%s, name=%s", smbShare.ID, smbShare.Name)
			break
		}
	}

	if smbShare == nil {
		klog.Infof("Creating SMB share for adopted volume: %s", subvol.Path)
		comment := fmt.Sprintf("CSI Volume: %s | Capacity: %d", volumeName, requestedCapacity)
		adoptCreateParams := nastyapi.SMBShareCreateParams{
			Name:    volumeName,
			Path:    subvol.Path,
			Comment: comment,
		}
		if smbUsername := params["smbUsername"]; smbUsername != "" {
			adoptCreateParams.ValidUsers = []string{smbUsername}
		}
		newShare, createErr := s.apiClient.CreateSMBShare(ctx, adoptCreateParams)
		if createErr != nil {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal, "Failed to create SMB share for adopted volume: %v", createErr)
		}
		smbShare = newShare
	}

	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}
	markAdoptable := params["markAdoptable"] == VolumeContextValueTrue

	props := nastyapi.VolumeProperties(nastyapi.VolumeParams{
		VolumeID:       volumeName,
		Protocol:       nastyapi.ProtocolSMB,
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

	volumeID := subvol.Filesystem + "/" + subvol.Name
	meta := VolumeMetadata{
		Name:         volumeName,
		Protocol:     ProtocolSMB,
		DatasetID:    volumeID,
		DatasetName:  subvol.Name,
		Server:       server,
		SMBShareUUID: smbShare.ID,
	}

	volumeContext := buildVolumeContext(meta)
	volumeContext[VolumeContextKeyShare] = smbShare.Name

	metrics.SetVolumeCapacity(volumeName, metrics.ProtocolSMB, requestedCapacity)

	klog.Infof("Successfully adopted SMB volume: %s (shareID=%s)", volumeName, smbShare.ID)
	timer.ObserveSuccess()

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: requestedCapacity,
			VolumeContext: volumeContext,
		},
	}, nil
}

// expandSMBVolume expands an SMB volume by updating the subvolume capacity.
func (s *ControllerService) expandSMBVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolSMB, "expand")
	klog.V(4).Infof("Expanding SMB volume: %s (dataset: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "subvolume ID not found in volume metadata")
	}

	filesystem, subvolName, err := splitSubvolumeID(meta.DatasetID)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "invalid subvolume ID %q: %v", meta.DatasetID, err)
	}

	// Resize the underlying subvolume
	//nolint:gosec // G115: CSI capacity is always non-negative
	if _, resizeErr := s.apiClient.ResizeSubvolume(ctx, filesystem, subvolName, uint64(requiredBytes)); resizeErr != nil {
		klog.Errorf("Failed to resize subvolume %s/%s: %v", filesystem, subvolName, resizeErr)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to resize subvolume: %v", resizeErr)
	}

	_, err = s.apiClient.SetSubvolumeProperties(ctx, filesystem, subvolName, map[string]string{
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requiredBytes, 10),
	})
	if err != nil {
		klog.Errorf("Failed to update capacity xattr for %s/%s: %v", filesystem, subvolName, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to update capacity for '%s/%s': %v", filesystem, subvolName, err)
	}

	klog.Infof("Expanded SMB volume: %s to %d bytes", meta.Name, requiredBytes)
	metrics.SetVolumeCapacity(meta.Name, metrics.ProtocolSMB, requiredBytes)

	timer.ObserveSuccess()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: false,
	}, nil
}

// getSMBVolumeInfo retrieves volume information and health status for an SMB volume.
func (s *ControllerService) getSMBVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting SMB volume info: %s (dataset: %s, shareUUID: %s)", meta.Name, meta.DatasetName, meta.SMBShareUUID)

	abnormal := false
	var messages []string

	filesystem, subvolName, err := splitSubvolumeID(meta.DatasetID)
	if err == nil {
		subvol, getErr := s.apiClient.GetSubvolume(ctx, filesystem, subvolName)
		if getErr != nil || subvol == nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Subvolume %s/%s not accessible: %v", filesystem, subvolName, getErr))
		}
	}

	if meta.SMBShareUUID != "" {
		foundShare, shareErr := s.apiClient.GetSMBShare(ctx, meta.SMBShareUUID)
		if shareErr != nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query SMB share %s: %v", meta.SMBShareUUID, shareErr))
		} else {
			switch {
			case foundShare == nil:
				abnormal = true
				messages = append(messages, fmt.Sprintf("SMB share %s not found", meta.SMBShareUUID))
			case !foundShare.Enabled:
				abnormal = true
				messages = append(messages, fmt.Sprintf("SMB share %s is disabled", meta.SMBShareUUID))
			}
		}
	}

	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	volumeContext := buildVolumeContext(*meta)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: 0,
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
