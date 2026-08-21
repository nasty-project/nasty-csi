// Package driver implements NVMe-oF-specific CSI controller operations.
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
	filesystem        string
	pvcName           string
	pvcNamespace      string
	compression       string
	foregroundTarget  string
	backgroundTarget  string
	promoteTarget     string
	metadataTarget    string
	blockFilesystem   string
	requestedCapacity int64
	dataReplicas      uint32
	markAdoptable     bool
	encrypted         bool
}

func nvmeofPropertiesV1(params *nvmeofVolumeParams, clusterID string) map[string]string {
	return nastyapi.VolumeProperties(nastyapi.VolumeParams{
		VolumeID:       params.volumeName,
		Protocol:       nastyapi.ProtocolNVMeOF,
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

// generateNQN creates a unique NQN for a volume's dedicated subsystem.
// Format: nqn.2026-02.io.nasty.csi:<volume-name>.
func generateNQN(nqnPrefix, volumeName string) string {
	return fmt.Sprintf("%s:%s", nqnPrefix, volumeName)
}

func nvmeSubsystemMatchesName(subsystem *nastyapi.NVMeOFSubsystem, volumeName string) bool {
	return subsystem != nil && strings.HasSuffix(subsystem.NQN, ":"+volumeName)
}

func nvmeSubsystemUsesDevice(subsystem *nastyapi.NVMeOFSubsystem, devicePath string) bool {
	if subsystem == nil || devicePath == "" {
		return false
	}
	for _, namespace := range subsystem.Namespaces {
		if namespace.DevicePath == devicePath {
			return true
		}
	}
	return false
}

func selectNVMeSubsystem(
	subsystems []nastyapi.NVMeOFSubsystem,
	expectedNQN, volumeName, devicePath string,
) (*nastyapi.NVMeOFSubsystem, error) {

	var exactCandidates []int
	var suffixCandidates []int
	for i := range subsystems {
		if subsystems[i].NQN == expectedNQN {
			exactCandidates = append(exactCandidates, i)
		}
		if nvmeSubsystemMatchesName(&subsystems[i], volumeName) {
			suffixCandidates = append(suffixCandidates, i)
		}
	}
	candidates := suffixCandidates
	if len(exactCandidates) > 0 {
		candidates = exactCandidates
	}
	if len(candidates) == 0 {
		return nil, nil //nolint:nilnil // no subsystem with this backend name
	}
	var matchingDevice []int
	for _, index := range candidates {
		if nvmeSubsystemUsesDevice(&subsystems[index], devicePath) {
			matchingDevice = append(matchingDevice, index)
		}
	}
	switch len(matchingDevice) {
	case 0:
		return nil, status.Errorf(codes.FailedPrecondition,
			"NVMe-oF subsystem candidates for %q are not linked to block device %s", volumeName, devicePath)
	case 1:
		return &subsystems[matchingDevice[0]], nil
	default:
		return nil, status.Errorf(codes.FailedPrecondition,
			"multiple NVMe-oF subsystems for %q are linked to block device %s", volumeName, devicePath)
	}
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

	filesystem := params[paramFilesystem]
	if filesystem == "" {
		return nil, status.Error(codes.InvalidArgument, "filesystem parameter is required for NVMe-oF volumes")
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
	encrypted := strings.EqualFold(params["encryption"], "true")

	// Optional compression and tiering settings
	compression := params["compression"]
	foregroundTarget := params["foregroundTarget"]
	backgroundTarget := params["backgroundTarget"]
	promoteTarget := params["promoteTarget"]
	metadataTarget := params["metadataTarget"]
	dataReplicas, err := parseDataReplicas(params["dataReplicas"])
	if err != nil {
		return nil, err
	}
	blockFilesystem, err := requestedBlockFilesystem(req.GetVolumeCapabilities())
	if err != nil {
		return nil, err
	}

	return &nvmeofVolumeParams{
		filesystem:        filesystem,
		server:            server,
		requestedCapacity: requestedCapacity,
		volumeName:        volumeName,
		subvolumeName:     volumeName,
		subsystemNQN:      subsystemNQN,
		deleteStrategy:    deleteStrategy,
		markAdoptable:     markAdoptable,
		comment:           comment,
		compression:       compression,
		foregroundTarget:  foregroundTarget,
		backgroundTarget:  backgroundTarget,
		promoteTarget:     promoteTarget,
		metadataTarget:    metadataTarget,
		blockFilesystem:   blockFilesystem,
		dataReplicas:      dataReplicas,
		pvcName:           pvcName,
		pvcNamespace:      pvcNamespace,
		storageClass:      storageClass,
		encrypted:         encrypted,
		nrIOQueues:        params["nvmeof.nr-io-queues"],
		queueSize:         params["nvmeof.queue-size"],
	}, nil
}

// buildNVMeOFVolumeResponse builds the CreateVolumeResponse for an NVMe-oF volume.
func buildNVMeOFVolumeResponse(volumeName, server string, subvol *nastyapi.Subvolume, subsystem *nastyapi.NVMeOFSubsystem, capacity int64) *csi.CreateVolumeResponse {
	// Volume ID is filesystem/subvolumeName for O(1) lookups
	volumeID := subvol.Filesystem + "/" + subvol.Name

	meta := VolumeMetadata{
		Name:        volumeName,
		Protocol:    ProtocolNVMeOF,
		DatasetID:   volumeID,
		DatasetName: subvol.Name,
		Server:      server,
		NVMeOFNQN:   subsystem.NQN,
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
func (s *ControllerService) createNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) { //nolint:gocyclo // creation keeps validation and rollback adjacent
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "create")
	klog.V(4).Info("Creating NVMe-oF volume")

	// Validate and extract parameters
	params, err := validateNVMeOFParams(req)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}

	// Per-PVC adoption annotation overrides StorageClass default
	if !params.markAdoptable && s.pvcHasAdoptableAnnotation(ctx, req.GetParameters()) {
		params.markAdoptable = true
	}

	klog.V(4).Infof("Creating NVMe-oF volume: %s with size: %d bytes, NQN: %s",
		params.volumeName, params.requestedCapacity, params.subsystemNQN)

	existingSubvol, selectedName, identity, err := s.selectExistingCreateSubvolume(
		ctx, req, params.filesystem, params.subvolumeName, ProtocolNVMeOF,
	)
	if err != nil {
		timer.ObserveError()
		return nil, err
	}
	params.volumeName = selectedName
	params.subvolumeName = selectedName
	nqnPrefix := req.GetParameters()["subsystemNQN"]
	if nqnPrefix == "" {
		nqnPrefix = defaultNQNPrefix
	}
	params.subsystemNQN = generateNQN(nqnPrefix, selectedName)
	createdByRequest := false

	// Handle existing subvolume (idempotency check)
	if existingSubvol != nil {
		resp, done, handleErr := s.handleExistingNVMeOFSubvolume(ctx, params, existingSubvol, identity, timer)
		if handleErr != nil {
			return nil, handleErr
		}
		if done {
			return resp, nil
		}
		// Subvolume exists but no subsystem — continue with subsystem creation
	}

	// Step 1: Create or reuse block subvolume
	requestedInitialization := params.blockFilesystem
	if req.GetVolumeContentSource() != nil {
		requestedInitialization = ""
	}
	if existingSubvol == nil {
		identity, err = newVolumeIdentityDecision(req, params.subvolumeName, ProtocolNVMeOF)
		if err != nil {
			timer.ObserveError()
			return nil, err
		}
	}
	subvol, created, err := s.getOrCreateSubvolume(ctx, params.filesystem, params.subvolumeName,
		"block", params.comment, params.compression, params.foregroundTarget, params.backgroundTarget, params.promoteTarget, params.metadataTarget, requestedInitialization, params.dataReplicas, params.requestedCapacity, timer)
	if err != nil {
		return nil, err
	}
	if created {
		createdByRequest = true
		if existingSubvol != nil {
			identity, err = newVolumeIdentityDecision(req, params.subvolumeName, ProtocolNVMeOF)
		}
	} else {
		identity, err = decideExistingVolumeIdentity(req, ProtocolNVMeOF, params.subvolumeName, subvol)
	}
	if err != nil {
		timer.ObserveError()
		if createdByRequest {
			return nil, s.rollbackCreatedSubvolume(ctx, subvol, err)
		}
		return nil, err
	}

	// Determine block device path
	if subvol.BlockDevice == nil || *subvol.BlockDevice == "" {
		timer.ObserveError()
		operationErr := status.Errorf(codes.Internal, "Block subvolume %s has no block device path", params.subvolumeName)
		if createdByRequest {
			return nil, s.rollbackCreatedSubvolume(ctx, subvol, operationErr)
		}
		return nil, operationErr
	}
	blockDevice := *subvol.BlockDevice
	if initErr := validateInitializedBlockFilesystem(subvol, requestedInitialization); initErr != nil {
		timer.ObserveError()
		if createdByRequest {
			return nil, s.rollbackCreatedSubvolume(ctx, subvol, initErr)
		}
		return nil, initErr
	}

	// Commit ownership before exposing the block device through NVMe-oF.
	if propertyErr := s.persistVolumeIdentity(ctx, subvol, nvmeofPropertiesV1(params, s.clusterID), identity); propertyErr != nil {
		timer.ObserveError()
		if createdByRequest {
			return nil, s.rollbackCreatedSubvolume(ctx, subvol, propertyErr)
		}
		return nil, propertyErr
	}

	// Step 2: Create NVMe-oF subsystem with namespace and port.
	subsystemParams := nastyapi.NVMeOFCreateParams{
		Name:       params.volumeName,
		DevicePath: blockDevice,
	}

	subsystem, err := s.apiClient.CreateNVMeOFSubsystem(ctx, subsystemParams)
	if err != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to create NVMe-oF subsystem '%s': %v", params.subsystemNQN, err)
	}
	if !nvmeSubsystemMatchesName(subsystem, params.volumeName) || !nvmeSubsystemUsesDevice(subsystem, blockDevice) {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal,
			"created NVMe-oF subsystem does not match backend name %q and block device %s", params.volumeName, blockDevice)
	}

	// Wait for NVMe-oF target to fully initialize the namespace
	const namespaceInitDelay = 3 * time.Second
	klog.V(4).Infof("Waiting %v for NVMe-oF namespace to be fully initialized", namespaceInitDelay)
	time.Sleep(namespaceInitDelay)

	klog.Infof("Created NVMe-oF volume: %s (subvolume: %s/%s, subsystem: %s, NQN: %s)",
		params.volumeName, params.filesystem, params.subvolumeName, subsystem.ID, subsystem.NQN)

	resp := buildNVMeOFVolumeResponse(params.volumeName, params.server, subvol, subsystem, params.requestedCapacity)
	injectQueueParams(resp.Volume.VolumeContext, params.nrIOQueues, params.queueSize)

	timer.ObserveSuccess()
	return resp, nil
}

// handleExistingNVMeOFSubvolume handles the case when a block subvolume already exists (idempotency).
func (s *ControllerService) handleExistingNVMeOFSubvolume(ctx context.Context, params *nvmeofVolumeParams, existingSubvol *nastyapi.Subvolume, identity volumeIdentityDecision, timer *metrics.OperationTimer) (*csi.CreateVolumeResponse, bool, error) {
	klog.V(4).Infof("Block subvolume %s already exists, checking idempotency", params.subvolumeName)
	if err := validateKnownBlockFilesystem(existingSubvol, params.blockFilesystem); err != nil {
		timer.ObserveError()
		return nil, false, err
	}

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

	if existingSubvol.BlockDevice == nil || *existingSubvol.BlockDevice == "" {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.FailedPrecondition,
			"block subvolume %s/%s has no block device", existingSubvol.Filesystem, existingSubvol.Name)
	}
	subsystems, listErr := s.apiClient.ListNVMeOFSubsystems(ctx)
	if listErr != nil {
		timer.ObserveError()
		return nil, false, status.Errorf(codes.Internal, "Failed to list NVMe-oF subsystems during idempotency check: %v", listErr)
	}
	subsystem, selectErr := selectNVMeSubsystem(subsystems, params.subsystemNQN, params.volumeName, *existingSubvol.BlockDevice)
	if selectErr != nil {
		timer.ObserveError()
		return nil, false, selectErr
	}
	if subsystem != nil {
		if err := s.persistVolumeIdentity(ctx, existingSubvol, nvmeofPropertiesV1(params, s.clusterID), identity); err != nil {
			timer.ObserveError()
			return nil, false, err
		}
		klog.V(4).Infof("NVMe-oF volume already exists (subsystem: %s, NQN: %s), returning existing volume",
			subsystem.ID, subsystem.NQN)
		resp := buildNVMeOFVolumeResponse(params.volumeName, params.server, existingSubvol, subsystem, existingCapacity)
		injectQueueParams(resp.Volume.VolumeContext, params.nrIOQueues, params.queueSize)
		timer.ObserveSuccess()
		return resp, true, nil
	}

	// Subvolume exists but no subsystem — signal caller to proceed with subsystem creation
	return nil, false, nil
}

// deleteNVMeOFVolume deletes an NVMe-oF volume and all associated resources.
// Subvolume is deleted first; if it fails, NVMe-oF subsystem is preserved to prevent orphaning.
func (s *ControllerService) deleteNVMeOFVolume(ctx context.Context, meta *VolumeMetadata) (*csi.DeleteVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "delete")
	klog.Infof("Deleting NVMe-oF volume: %s (subvolume: %s, NQN: %s)",
		meta.Name, meta.DatasetID, meta.NVMeOFNQN)

	filesystem, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	// Step 0: Verify ownership via xattr properties before deletion
	subvol, err := s.apiClient.GetSubvolume(ctx, filesystem, name)
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
	}

	// Step 1: Delete NVMe-oF subsystem first (must be removed before subvolume)
	// Always look up by NQN — UUIDs are not stored in xattrs (they don't survive clone/snapshot).
	nqn := meta.NVMeOFNQN
	if nqn == "" && name != "" {
		nqn = "nqn.2137-04.storage.nasty:" + name
	}
	subsystemID := ""
	if nqn != "" {
		klog.V(4).Infof("Looking up NVMe-oF subsystem by NQN: %s", nqn)
		subsystem, lookupErr := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, nqn)
		if lookupErr == nil && subsystem != nil {
			subsystemID = subsystem.ID
		}
	}

	if subsystemID != "" {
		if err := s.apiClient.DeleteNVMeOFSubsystem(ctx, subsystemID); err != nil {
			if !isNotFoundError(err) {
				klog.Errorf("Failed to delete NVMe-oF subsystem %s: %v", subsystemID, err)
				timer.ObserveError()
				return nil, status.Errorf(codes.Internal,
					"Failed to delete NVMe-oF subsystem %s: %v", subsystemID, err)
			}
		} else {
			klog.V(4).Infof("Deleted NVMe-oF subsystem: %s", subsystemID)
		}
	}

	// Step 2: Delete subvolume (subsystem is gone, no guard will block)
	deleteErr := s.apiClient.DeleteSubvolume(ctx, filesystem, name)
	if deleteErr != nil && !isNotFoundError(deleteErr) {
		retryConfig := retry.DeletionConfig("delete-nvmeof-subvol")
		err := retry.WithRetryNoResult(ctx, retryConfig, func() error {
			e := s.apiClient.DeleteSubvolume(ctx, filesystem, name)
			if e != nil && isNotFoundError(e) {
				return nil
			}
			return e
		})
		if err != nil {
			klog.Errorf("Subvolume %s deletion failed: %v", meta.DatasetID, err)
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal,
				"Failed to delete block subvolume %s: %v", meta.DatasetID, err)
		}
	}
	klog.V(4).Infof("Deleted block subvolume: %s", meta.DatasetID)

	// Clear volume capacity metric
	metrics.DeleteVolumeCapacity(meta.Name, metrics.ProtocolNVMeOF)

	klog.Infof("Deleted NVMe-oF volume: %s", meta.Name)
	timer.ObserveSuccess()
	return &csi.DeleteVolumeResponse{}, nil
}

// expandNVMeOFVolume expands an NVMe-oF volume by updating the capacity property.
//
//nolint:dupl // Intentionally similar to NFS/iSCSI expansion logic
func (s *ControllerService) expandNVMeOFVolume(ctx context.Context, meta *VolumeMetadata, requiredBytes int64, nodeExpansionRequired bool) (*csi.ControllerExpandVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "expand")
	klog.V(4).Infof("Expanding NVMe-oF volume: %s (subvolume: %s) to %d bytes", meta.Name, meta.DatasetName, requiredBytes)

	if meta.DatasetID == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "subvolume ID not found in volume metadata")
	}

	filesystem, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
	}

	// Resize the underlying subvolume
	//nolint:gosec // G115: CSI capacity is always non-negative
	if _, err := s.apiClient.ResizeSubvolume(ctx, filesystem, name, uint64(requiredBytes)); err != nil {
		klog.Errorf("Failed to resize subvolume %s/%s: %v", filesystem, name, err)
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to resize subvolume: %v", err)
	}

	// Update capacity via xattr property
	props := map[string]string{
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requiredBytes, 10),
	}
	_, err := s.apiClient.SetSubvolumeProperties(ctx, filesystem, name, props)
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
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

// adoptNVMeOFVolume adopts an orphaned NVMe-oF volume by recreating missing NASty resources.
// This enables GitOps workflows where clusters are recreated and need to adopt existing volumes.
func (s *ControllerService) adoptNVMeOFVolume(ctx context.Context, req *csi.CreateVolumeRequest, subvol *nastyapi.Subvolume, params map[string]string) (*csi.CreateVolumeResponse, error) {
	timer := metrics.NewVolumeOperationTimer(metrics.ProtocolNVMeOF, "adopt")
	volumeName := subvol.Name
	klog.Infof("Adopting NVMe-oF volume: %s (subvolume=%s/%s)", volumeName, subvol.Filesystem, subvol.Name)

	// Get server parameter
	server := params["server"]
	if server == "" {
		timer.ObserveError()
		return nil, status.Error(codes.InvalidArgument, "server parameter is required for NVMe-oF volumes")
	}
	requestedFilesystem, err := requestedBlockFilesystem(req.GetVolumeCapabilities())
	if err != nil {
		timer.ObserveError()
		return nil, err
	}
	if err := validateKnownBlockFilesystem(subvol, requestedFilesystem); err != nil {
		timer.ObserveError()
		return nil, err
	}

	// Get requested capacity
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024 // 1 GiB default
	}
	if subvol.BlockDevice == nil || *subvol.BlockDevice == "" {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Block subvolume %s/%s has no block device path", subvol.Filesystem, subvol.Name)
	}
	blockDevice := *subvol.BlockDevice
	deleteStrategy := params["deleteStrategy"]
	if deleteStrategy == "" {
		deleteStrategy = nastyapi.DeleteStrategyDelete
	}
	props := nastyapi.VolumeProperties(nastyapi.VolumeParams{
		VolumeID:       volumeName,
		Protocol:       nastyapi.ProtocolNVMeOF,
		CapacityBytes:  requestedCapacity,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		DeleteStrategy: deleteStrategy,
		PVCName:        params[CSIPVCName],
		PVCNamespace:   params[CSIPVCNamespace],
		StorageClass:   params["csi.storage.k8s.io/sc/name"],
		Adoptable:      params["markAdoptable"] == VolumeContextValueTrue,
		ClusterID:      s.clusterID,
	})
	identity, identityErr := identityProperties(subvol.Properties, req.GetName(), subvol.Name, ProtocolNVMeOF)
	if identityErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate adopted volume identity: %v", identityErr)
	}
	// Find an exact expected NQN first, then a unique compatible backend-generated NQN.
	subsystems, listErr := s.apiClient.ListNVMeOFSubsystems(ctx)
	if listErr != nil {
		timer.ObserveError()
		return nil, status.Errorf(codes.Internal, "Failed to list NVMe-oF subsystems while adopting volume: %v", listErr)
	}
	nqnPrefix := params["subsystemNQN"]
	if nqnPrefix == "" {
		nqnPrefix = defaultNQNPrefix
	}
	expectedNQN := generateNQN(nqnPrefix, volumeName)
	subsystem, selectErr := selectNVMeSubsystem(subsystems, expectedNQN, volumeName, blockDevice)
	if selectErr != nil {
		timer.ObserveError()
		return nil, selectErr
	}
	if subsystem != nil {
		klog.Infof("Found existing NVMe-oF subsystem for adopted volume: ID=%s, NQN=%s", subsystem.ID, subsystem.NQN)
	}
	if err := s.persistVolumeIdentity(ctx, subvol, props, volumeIdentityDecision{properties: identity, persist: true}); err != nil {
		timer.ObserveError()
		return nil, err
	}

	// If no subsystem found, create new one
	if subsystem == nil {
		klog.Infof("Creating new NVMe-oF subsystem for adopted volume: %s", volumeName)

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
		if !nvmeSubsystemMatchesName(subsystem, volumeName) || !nvmeSubsystemUsesDevice(subsystem, blockDevice) {
			timer.ObserveError()
			return nil, status.Errorf(codes.Internal,
				"created NVMe-oF subsystem does not match backend name %q and block device %s", volumeName, blockDevice)
		}
		klog.Infof("Created NVMe-oF subsystem for adopted volume: ID=%s, NQN=%s", subsystem.ID, subsystem.NQN)
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
