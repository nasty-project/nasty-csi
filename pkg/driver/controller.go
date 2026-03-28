// Package driver implements the CSI driver controller service.
package driver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Error message constants.
const (
	errMsgVolumeIDRequired   = "Volume ID is required"
	errMsgVolumeSizeTooSmall = "requested volume size %d bytes is below minimum %d bytes (1 GiB) enforced by NASty"
	msgVolumeIsHealthy       = "Volume is healthy"
)

// Default values.
const (
	defaultServerAddress = "defaultServerAddress"
	// MinVolumeSize is the minimum volume size enforced by NASty (1 GiB).
	// NASty API rejects quota/volsize values below this threshold.
	MinVolumeSize = 1 << 30 // 1 GiB in bytes (1073741824)
)

// VolumeContext key constants - these are used consistently across the driver.
const (
	VolumeContextKeyProtocol            = "protocol"
	VolumeContextKeyServer              = "server"
	VolumeContextKeyShare               = "share"
	VolumeContextKeyDatasetID           = "datasetID"
	VolumeContextKeyDatasetName         = "datasetName"
	VolumeContextKeyNFSShareUUID        = "nfsShareUUID"
	VolumeContextKeyNQN                 = "nqn"
	VolumeContextKeyNSID                = "nsid"
	VolumeContextKeyISCSIIQN            = "iscsiIQN"
	VolumeContextKeyISCSITargetUUID     = "iscsiTargetUUID"
	VolumeContextKeySMBShareUUID        = "smbShareUUID"
	VolumeContextKeyNVMeOFSubsystemUUID = "nvmeofSubsystemUUID"
	VolumeContextKeyExpectedCapacity    = "expectedCapacity"
	VolumeContextKeyClonedFromSnap      = "clonedFromSnapshot"
	VolumeContextValueTrue              = "true"
	VolumeContextValueFalse             = "false"
)

// Static errors for controller operations.
var (
	ErrVolumeNotFound  = errors.New("volume not found")
	ErrDatasetNotFound = errors.New("dataset not found for share")
	ErrInvalidVolumeID = errors.New("invalid subvolume ID")
)

// capacityErrorSubstrings are error message patterns that indicate insufficient filesystem capacity.
// NASty returns these when a filesystem or dataset doesn't have enough free space.

var capacityErrorSubstrings = []string{
	"insufficient space",
	"out of space",
	"not enough space",
	"no space left",
	"ENOSPC",
	"quota exceeded",
}

// isCapacityError checks if an error indicates a storage capacity issue.
// Returns codes.ResourceExhausted status if it is, nil otherwise.
func isCapacityError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	for _, substr := range capacityErrorSubstrings {
		if strings.Contains(errStr, substr) {
			return true
		}
	}
	return false
}

// createVolumeError returns an appropriate gRPC status error for volume creation failures.
// Maps capacity-related errors to ResourceExhausted per CSI spec.
func createVolumeError(msg string, err error) error {
	if isCapacityError(err) {
		return status.Errorf(codes.ResourceExhausted, "%s: %v", msg, err)
	}
	return status.Errorf(codes.Internal, "%s: %v", msg, err)
}

// VolumeMetadata contains information needed to manage a volume.
// This is used internally and for building VolumeContext.
// Note: Volume ID is now just the volume name (CSI spec compliant, max 128 bytes).
// All metadata is passed via VolumeContext.
type VolumeMetadata struct {
	Name                string
	Protocol            string
	DatasetID           string
	DatasetName         string
	Server              string // NASty server address
	NVMeOFNQN           string // NVMe-oF subsystem NQN
	NVMeOFSubsystemUUID string // NASty API UUID subsystem ID
	ISCSIIQN            string // iSCSI target IQN
	ISCSITargetUUID     string // NASty API UUID target ID
	NFSShareUUID        string // NASty API UUID share ID
	SMBShareUUID        string // NASty API UUID SMB share ID
}

// buildVolumeContext creates a VolumeContext map from VolumeMetadata.
// This is the standard way to pass volume metadata through CSI.
func buildVolumeContext(meta VolumeMetadata) map[string]string {
	ctx := map[string]string{
		VolumeContextKeyProtocol: meta.Protocol,
	}

	if meta.Server != "" {
		ctx[VolumeContextKeyServer] = meta.Server
	}
	if meta.DatasetID != "" {
		ctx[VolumeContextKeyDatasetID] = meta.DatasetID
	}
	if meta.DatasetName != "" {
		ctx[VolumeContextKeyDatasetName] = meta.DatasetName
	}

	// Protocol-specific fields
	switch meta.Protocol {
	case ProtocolNFS:
		if meta.NFSShareUUID != "" {
			ctx[VolumeContextKeyNFSShareUUID] = meta.NFSShareUUID
		}
	case ProtocolNVMeOF:
		if meta.NVMeOFNQN != "" {
			ctx[VolumeContextKeyNQN] = meta.NVMeOFNQN
		}
		if meta.NVMeOFSubsystemUUID != "" {
			ctx[VolumeContextKeyNVMeOFSubsystemUUID] = meta.NVMeOFSubsystemUUID
		}
	case ProtocolISCSI:
		if meta.ISCSIIQN != "" {
			ctx[VolumeContextKeyISCSIIQN] = meta.ISCSIIQN
		}
		if meta.ISCSITargetUUID != "" {
			ctx[VolumeContextKeyISCSITargetUUID] = meta.ISCSITargetUUID
		}
	case ProtocolSMB:
		if meta.SMBShareUUID != "" {
			ctx[VolumeContextKeySMBShareUUID] = meta.SMBShareUUID
		}
	}

	return ctx
}

// getProtocolFromVolumeContext determines the protocol from volume context.
// Falls back to NFS if protocol is not specified.
func getProtocolFromVolumeContext(ctx map[string]string) string {
	if protocol := ctx[VolumeContextKeyProtocol]; protocol != "" {
		return protocol
	}
	// Infer protocol from context keys
	if ctx[VolumeContextKeyNQN] != "" {
		return ProtocolNVMeOF
	}
	if ctx[VolumeContextKeyISCSIIQN] != "" {
		return ProtocolISCSI
	}
	if ctx[VolumeContextKeyShare] != "" {
		return ProtocolNFS
	}
	return ProtocolNFS
}

// ControllerService implements the CSI Controller service.
type ControllerService struct {
	csi.UnimplementedControllerServer
	apiClient    nastyapi.ClientInterface
	nodeRegistry *NodeRegistry
	// publishedVolumes tracks volumes published to nodes with their readonly state.
	// Key format: "volumeID:nodeID", value: readonly state.
	// Used to detect incompatible re-publish attempts per CSI spec.
	publishedVolumes   map[string]bool
	clusterID          string
	publishedVolumesMu sync.RWMutex
}

// NewControllerService creates a new controller service.
func NewControllerService(apiClient nastyapi.ClientInterface, nodeRegistry *NodeRegistry, clusterID string) *ControllerService {
	return &ControllerService{
		apiClient:        apiClient,
		nodeRegistry:     nodeRegistry,
		clusterID:        clusterID,
		publishedVolumes: make(map[string]bool),
	}
}

// isDatasetPathVolumeID returns true if the volume ID is a full dataset path (new format).
// New-format IDs contain "/" (e.g., "filesystem/parent/pvc-xxx"), while legacy IDs are plain names ("pvc-xxx").
func isDatasetPathVolumeID(volumeID string) bool {
	return strings.Contains(volumeID, "/")
}

// lookupVolumeByCSIName finds a volume by its CSI volume name using xattr properties.
// This is the preferred method for volume discovery as it uses the source of truth (xattr properties).
// For new-format volume IDs (containing "/"), uses O(1) direct subvolume lookup (filesystem/name).
// For legacy volume IDs (plain names), falls back to O(n) property scan.
// Returns nil, nil if volume not found; returns error only on API failures.
func (s *ControllerService) lookupVolumeByCSIName(ctx context.Context, volumeName string) (*VolumeMetadata, error) {
	klog.V(4).Infof("Looking up volume by CSI name: %s", volumeName)

	// New-format volume IDs contain "/" (e.g., "filesystem/pvc-xxx") — use O(1) direct lookup
	if isDatasetPathVolumeID(volumeName) {
		return s.lookupVolumeBySubvolumePath(ctx, volumeName)
	}

	// Plain-name volume IDs — use O(n) property scan
	return s.lookupVolumeByPropertyScan(ctx, "", volumeName)
}

// lookupVolumeBySubvolumePath looks up a volume by its full subvolume path "filesystem/name" (O(1) lookup).
func (s *ControllerService) lookupVolumeBySubvolumePath(ctx context.Context, subvolumePath string) (*VolumeMetadata, error) {
	klog.V(4).Infof("Looking up volume by subvolume path (O(1)): %s", subvolumePath)

	filesystem, name, err := splitSubvolumeID(subvolumePath)
	if err != nil {
		return nil, fmt.Errorf("invalid subvolume path %s: %w", subvolumePath, err)
	}

	subvol, err := s.apiClient.GetSubvolume(ctx, filesystem, name)
	if err != nil {
		if isNotFoundError(err) {
			klog.V(4).Infof("Subvolume not found: %s", subvolumePath)
			return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil result
		}
		return nil, fmt.Errorf("failed to query subvolume %s: %w", subvolumePath, err)
	}
	if subvol == nil {
		klog.V(4).Infof("Subvolume not found: %s", subvolumePath)
		return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil result
	}

	return extractVolumeMetadataFromSubvolume(subvolumePath, subvol)
}

// lookupVolumeByPropertyScan finds a volume by scanning subvolumes for matching CSI volume name property (O(n)).
func (s *ControllerService) lookupVolumeByPropertyScan(ctx context.Context, filesystem, volumeName string) (*VolumeMetadata, error) {
	klog.V(4).Infof("Looking up volume by property scan (O(n)): %s (filesystem: %s)", volumeName, filesystem)

	subvol, err := s.apiClient.FindSubvolumeByCSIVolumeName(ctx, filesystem, volumeName)
	if err != nil {
		if errors.Is(err, nastyapi.ErrDatasetNotFound) || isNotFoundError(err) {
			klog.V(4).Infof("Volume not found by CSI name (property scan): %s", volumeName)
			return nil, nil //nolint:nilnil // not found
		}
		return nil, fmt.Errorf("failed to find subvolume by CSI volume name: %w", err)
	}
	if subvol == nil {
		klog.V(4).Infof("Volume not found by CSI name: %s", volumeName)
		return nil, nil //nolint:nilnil // nil, nil indicates "not found" - callers check for nil result
	}

	volumeID := subvol.Filesystem + "/" + subvol.Name
	return extractVolumeMetadataFromSubvolume(volumeID, subvol)
}

// extractVolumeMetadataFromSubvolume builds VolumeMetadata from a Subvolume.
// Verifies ownership and extracts all protocol-specific metadata from xattr properties.
// Returns nil, nil if the subvolume is not managed by nasty-csi.
func extractVolumeMetadataFromSubvolume(volumeID string, subvol *nastyapi.Subvolume) (*VolumeMetadata, error) {
	props := subvol.Properties
	if props == nil {
		klog.Warningf("Subvolume %s/%s has no properties, may not be managed by nasty-csi", subvol.Filesystem, subvol.Name)
		return nil, nil //nolint:nilnil // Subvolume exists but no properties - treat as not found
	}

	// Verify ownership
	if managedBy, ok := props[nastyapi.PropertyManagedBy]; !ok || managedBy != nastyapi.ManagedByValue {
		klog.Warningf("Subvolume %s/%s not managed by nasty-csi (managed_by=%s)", subvol.Filesystem, subvol.Name, managedBy)
		return nil, nil //nolint:nilnil // Not our volume - treat as not found
	}

	subvolumeID := subvol.Filesystem + "/" + subvol.Name

	// Build VolumeMetadata from properties
	meta := &VolumeMetadata{
		Name:        volumeID,
		DatasetID:   subvolumeID,
		DatasetName: subvol.Name,
	}

	// Extract protocol
	if protocol, ok := props[nastyapi.PropertyProtocol]; ok {
		meta.Protocol = protocol
	}

	klog.V(4).Infof("Found volume: %s (subvolume=%s, protocol=%s)", volumeID, subvolumeID, meta.Protocol)
	return meta, nil
}

// CreateVolume creates a new volume.
func (s *ControllerService) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// Log at Info level (not V(4)) so we can see when CreateVolume is called in CI
	klog.Infof("=== CreateVolume CALLED === Name: %s", req.GetName())
	if req.GetVolumeContentSource() != nil {
		if snap := req.GetVolumeContentSource().GetSnapshot(); snap != nil {
			klog.Infof("CreateVolume from Snapshot: SnapshotId=%s", snap.GetSnapshotId())
		}
		if vol := req.GetVolumeContentSource().GetVolume(); vol != nil {
			klog.Infof("CreateVolume from Volume: VolumeId=%s", vol.GetVolumeId())
		}
	}
	klog.V(4).Infof("CreateVolume called with request: %+v", req)

	// Log detailed debug info for troubleshooting
	s.logCreateVolumeDebugInfo(req)

	// Validate request
	if err := validateCreateVolumeRequest(req); err != nil {
		return nil, err
	}

	// Parse storage class parameters
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	// Determine protocol (default to NFS)
	protocol := params["protocol"]
	if protocol == "" {
		protocol = ProtocolNFS
	}

	// Validate access modes are safe for this protocol
	if err := validateAccessModeForProtocol(req.GetVolumeCapabilities(), protocol); err != nil {
		return nil, err
	}

	// Check for idempotency: if volume with same name already exists
	existingVolume, err := s.checkExistingVolume(ctx, req, params, protocol)
	if err != nil && !errors.Is(err, ErrVolumeNotFound) {
		return nil, err
	}
	if existingVolume != nil {
		klog.V(4).Infof("Returning existing volume for idempotency: %s", req.GetName())
		return existingVolume, nil
	}

	// Check for adoption: if volume exists elsewhere (different parentDataset) and can be adopted
	if resp, adopted, err := s.checkAndAdoptVolume(ctx, req, params, protocol); adopted {
		if err != nil {
			return nil, err
		}
		klog.Infof("Successfully adopted orphaned volume: %s", req.GetName())
		return resp, nil
	}

	// Check if creating from snapshot or volume clone
	if resp, handled, err := s.handleVolumeContentSource(ctx, req, protocol); handled {
		return resp, err
	}

	// Validate encryption requirement: if StorageClass requests encryption,
	// verify the target filesystem has bcachefs-level encryption enabled.
	if strings.EqualFold(params["encryption"], "true") {
		fsName := params["filesystem"]
		if fsName == "" {
			return nil, status.Error(codes.InvalidArgument, "encryption requires a filesystem parameter")
		}
		fs, err := s.apiClient.QueryFilesystem(ctx, fsName)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to query filesystem %q for encryption check: %v", fsName, err)
		}
		if fs.Options.Encrypted == nil || !*fs.Options.Encrypted {
			return nil, status.Errorf(codes.InvalidArgument,
				"StorageClass requires encryption but filesystem %q is not encrypted; "+
					"create an encrypted filesystem or remove the encryption parameter", fsName)
		}
		klog.V(4).Infof("Encryption check passed: filesystem %q is encrypted", fsName)
	}

	klog.V(4).Infof("Creating volume %s with protocol %s", req.GetName(), protocol)

	return s.createVolumeByProtocol(ctx, req, protocol)
}

// logCreateVolumeDebugInfo logs detailed debug information for CreateVolume troubleshooting.
func (s *ControllerService) logCreateVolumeDebugInfo(req *csi.CreateVolumeRequest) {
	klog.V(4).Infof("=== CreateVolume Debug Info ===")
	klog.V(4).Infof("Volume Name: %s", req.GetName())
	klog.V(4).Infof("VolumeContentSource: %+v", req.GetVolumeContentSource())
	if req.GetVolumeContentSource() != nil {
		klog.V(4).Infof("VolumeContentSource Type: %T", req.GetVolumeContentSource().GetType())
		klog.V(4).Infof("VolumeContentSource.Snapshot: %+v", req.GetVolumeContentSource().GetSnapshot())
		klog.V(4).Infof("VolumeContentSource.Volume: %+v", req.GetVolumeContentSource().GetVolume())
	}
	klog.V(4).Infof("Parameters: %+v", req.GetParameters())
	klog.V(4).Infof("CapacityRange: %+v", req.GetCapacityRange())
	klog.V(4).Infof("VolumeCapabilities: %+v", req.GetVolumeCapabilities())
	klog.V(4).Infof("AccessibilityRequirements: %+v", req.GetAccessibilityRequirements())
	klog.V(4).Infof("Secrets: [REDACTED - %d keys]", len(req.GetSecrets()))
	klog.V(4).Infof("===============================")
}

// validateCreateVolumeRequest validates the CreateVolume request parameters.
func validateCreateVolumeRequest(req *csi.CreateVolumeRequest) error {
	if req.GetName() == "" {
		return status.Error(codes.InvalidArgument, "Volume name is required")
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return status.Error(codes.InvalidArgument, "Volume capabilities are required")
	}

	// Validate minimum volume size (NASty enforces 1 GiB minimum for quota/volsize)
	if capacityRange := req.GetCapacityRange(); capacityRange != nil {
		requiredBytes := capacityRange.GetRequiredBytes()
		if requiredBytes > 0 && requiredBytes < MinVolumeSize {
			return status.Errorf(codes.InvalidArgument, errMsgVolumeSizeTooSmall, requiredBytes, MinVolumeSize)
		}
	}

	return nil
}

// isMultiNodeMode returns true if the access mode allows multiple nodes to access the volume.
func isMultiNodeMode(mode csi.VolumeCapability_AccessMode_Mode) bool {
	switch mode {
	case csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER:
		return true
	default:
		return false
	}
}

// validateAccessModeForProtocol checks that the requested volume capabilities are safe
// for the given protocol. Block protocols (NVMe-oF, iSCSI) support multi-node access
// only in raw block mode (e.g., KubeVirt live migration). Multi-node with a mounted
// filesystem on block protocols would corrupt ext4/xfs. File protocols (NFS, SMB)
// handle multi-node access natively.
func validateAccessModeForProtocol(caps []*csi.VolumeCapability, protocol string) error {
	for _, cap := range caps {
		if !isMultiNodeMode(cap.GetAccessMode().GetMode()) {
			continue
		}
		// Multi-node requested — block protocols only allow raw block mode
		if protocol == ProtocolNVMeOF || protocol == ProtocolISCSI {
			if cap.GetMount() != nil {
				return status.Errorf(codes.InvalidArgument,
					"multi-node access mode %s with mounted filesystem is not supported for %s — "+
						"use volumeMode: Block for multi-node block storage (e.g., KubeVirt live migration)",
					cap.GetAccessMode().GetMode(), protocol)
			}
		}
	}
	return nil
}

// handleVolumeContentSource handles creating volumes from snapshots or clones.
// Returns (response, true, nil) if handled successfully, (nil, true, error) if handled with error,
// or (nil, false, nil) if not a content source request.
func (s *ControllerService) handleVolumeContentSource(ctx context.Context, req *csi.CreateVolumeRequest, protocol string) (*csi.CreateVolumeResponse, bool, error) {
	contentSource := req.GetVolumeContentSource()
	klog.V(4).Infof("Checking VolumeContentSource for volume %s: %+v", req.GetName(), contentSource)

	if contentSource == nil {
		klog.V(4).Infof("VolumeContentSource is nil for volume %s (normal volume creation)", req.GetName())
		return nil, false, nil
	}

	klog.V(4).Infof("VolumeContentSource is NOT nil for volume %s", req.GetName())

	// Check if creating from snapshot
	if snapshot := contentSource.GetSnapshot(); snapshot != nil {
		klog.V(4).Infof("=== SNAPSHOT RESTORE DETECTED === Creating volume %s from snapshot %s with protocol %s",
			req.GetName(), snapshot.GetSnapshotId(), protocol)
		resp, err := s.createVolumeFromSnapshot(ctx, req, snapshot.GetSnapshotId())
		if err != nil {
			klog.Errorf("Failed to create volume from snapshot: %v", err)
			return nil, true, err
		}
		return resp, true, nil
	}

	// Check if creating from volume (cloning)
	if volume := contentSource.GetVolume(); volume != nil {
		sourceVolumeID := volume.GetVolumeId()
		klog.V(4).Infof("=== VOLUME CLONE DETECTED === Creating volume %s from volume %s with protocol %s",
			req.GetName(), sourceVolumeID, protocol)
		resp, err := s.createVolumeFromVolume(ctx, req, sourceVolumeID)
		if err != nil {
			klog.Errorf("Failed to create volume from volume: %v", err)
			return nil, true, err
		}
		return resp, true, nil
	}

	klog.Warningf("VolumeContentSource exists but both snapshot and volume are nil for volume %s", req.GetName())
	return nil, false, nil
}

// createVolumeByProtocol creates a volume using the specified protocol.
func (s *ControllerService) createVolumeByProtocol(ctx context.Context, req *csi.CreateVolumeRequest, protocol string) (*csi.CreateVolumeResponse, error) {
	switch protocol {
	case ProtocolNFS:
		return s.createNFSVolume(ctx, req)
	case ProtocolNVMeOF:
		return s.createNVMeOFVolume(ctx, req)
	case ProtocolISCSI:
		return s.createISCSIVolume(ctx, req)
	case ProtocolSMB:
		return s.createSMBVolume(ctx, req)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported protocol: %s (supported: nfs, nvmeof, iscsi, smb)", protocol)
	}
}

// checkExistingVolume checks if a volume with the same name already exists and returns it for idempotency.
// For the NASty backend, each protocol handler manages its own idempotency checks.
// This function defers to protocol-specific handlers which use subvolume lookups.
// Returns ErrVolumeNotFound to signal that creation should proceed.
func (s *ControllerService) checkExistingVolume(_ context.Context, _ *csi.CreateVolumeRequest, _ map[string]string, _ string) (*csi.CreateVolumeResponse, error) {
	// Protocol-specific handlers (createNFSVolume, createISCSIVolume, etc.) perform
	// idempotency checks internally by querying the subvolume directly. This top-level
	// check is not needed for the NASty backend since subvolume IDs are deterministic.
	return nil, ErrVolumeNotFound
}

// createVolumeFromVolume creates a new volume by cloning an existing volume.
//
// Uses bcachefs's native O(1) writable snapshot — a single
// `bcachefs subvolume snapshot` (without -r) that creates a COW clone
// sharing data blocks with the source. No temporary snapshots needed.
func (s *ControllerService) createVolumeFromVolume(ctx context.Context, req *csi.CreateVolumeRequest, sourceVolumeID string) (*csi.CreateVolumeResponse, error) {
	klog.Infof("createVolumeFromVolume called for volume %s from source %s", req.GetName(), sourceVolumeID)

	// 1. Look up the source volume to get filesystem, name, and protocol
	sourceMeta, err := s.lookupVolumeByCSIName(ctx, sourceVolumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to lookup source volume %s: %v", sourceVolumeID, err)
	}
	if sourceMeta == nil {
		return nil, status.Errorf(codes.NotFound, "source volume %s not found", sourceVolumeID)
	}

	filesystem, sourceSubvolName, err := splitSubvolumeID(sourceMeta.DatasetID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid source volume dataset ID %q: %v", sourceMeta.DatasetID, err)
	}

	protocol := sourceMeta.Protocol
	klog.V(4).Infof("Volume clone: filesystem=%s, sourceSubvolume=%s, protocol=%s", filesystem, sourceSubvolName, protocol)

	// 2. Resolve the new subvolume name
	params := req.GetParameters()
	newName, err := ResolveVolumeName(params, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to resolve volume name: %v", err)
	}

	// 3. Check if the subvolume already exists (idempotency)
	existingSubvol, getErr := s.apiClient.GetSubvolume(ctx, filesystem, newName)
	if getErr != nil && !isNotFoundError(getErr) {
		return nil, status.Errorf(codes.Internal, "failed to check for existing subvolume %s/%s: %v", filesystem, newName, getErr)
	}

	if existingSubvol == nil {
		// Native COW clone — O(1), no temporary snapshot needed
		klog.V(4).Infof("Cloning subvolume %s/%s to %s/%s", filesystem, sourceSubvolName, filesystem, newName)

		_, cloneErr := s.apiClient.CloneSubvolume(ctx, filesystem, sourceSubvolName, newName)
		if cloneErr != nil {
			klog.Errorf("Failed to clone subvolume %s/%s: %v", filesystem, sourceSubvolName, cloneErr)
			return nil, status.Errorf(codes.Internal, "failed to clone volume: %v", cloneErr)
		}

		klog.Infof("Cloned subvolume %s/%s to %s/%s", filesystem, sourceSubvolName, filesystem, newName)
	} else {
		klog.V(4).Infof("Subvolume %s/%s already exists (idempotent clone), proceeding to share setup", filesystem, newName)
	}

	// 4. Set CSI metadata properties on the cloned subvolume
	requestedCapacity := req.GetCapacityRange().GetRequiredBytes()
	if requestedCapacity == 0 {
		requestedCapacity = 1 * 1024 * 1024 * 1024
	}

	csiProps := map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: req.GetName(),
		nastyapi.PropertyCapacityBytes: strconv.FormatInt(requestedCapacity, 10),
		nastyapi.PropertyProtocol:      protocol,
	}
	if _, propErr := s.apiClient.SetSubvolumeProperties(ctx, filesystem, newName, csiProps); propErr != nil {
		klog.Warningf("Failed to set CSI properties on cloned subvolume %s/%s: %v (volume will still work)", filesystem, newName, propErr)
	}

	// 5. Delegate to protocol-specific create to set up sharing
	klog.V(4).Infof("Delegating to createVolumeByProtocol for protocol %s", protocol)
	resp, err := s.createVolumeByProtocol(ctx, req, protocol)
	if err != nil {
		return nil, err
	}

	// Set the content source in the response so the CO knows this came from a volume
	if resp != nil && resp.Volume != nil {
		resp.Volume.ContentSource = &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Volume{
				Volume: &csi.VolumeContentSource_VolumeSource{
					VolumeId: sourceVolumeID,
				},
			},
		}
	}

	klog.Infof("Created volume %s from source volume %s (protocol: %s)", req.GetName(), sourceVolumeID, protocol)
	return resp, nil
}

// DeleteVolume deletes a volume.
func (s *ControllerService) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	klog.V(4).Infof("DeleteVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	volumeID := req.GetVolumeId()
	klog.V(4).Infof("Deleting volume %s", volumeID)

	// Try property-based lookup first (preferred method - uses xattr properties as source of truth)
	// Pass empty prefix to search all datasets across all pools
	volumeMeta, err := s.lookupVolumeByCSIName(ctx, volumeID)
	if err != nil {
		klog.Errorf("Property-based lookup failed for volume %s: %v", volumeID, err)
		return nil, status.Errorf(codes.Internal, "Failed to lookup volume: %v", err)
	}

	if volumeMeta == nil {
		// Volume not found - return success per CSI spec (idempotent delete)
		klog.V(4).Infof("Volume %s not found, returning success (idempotent)", volumeID)
		return &csi.DeleteVolumeResponse{}, nil
	}

	klog.V(4).Infof("Found volume %s via property lookup: dataset=%s, protocol=%s", volumeID, volumeMeta.DatasetID, volumeMeta.Protocol)
	switch volumeMeta.Protocol {
	case ProtocolNFS:
		return s.deleteNFSVolume(ctx, volumeMeta)
	case ProtocolNVMeOF:
		return s.deleteNVMeOFVolume(ctx, volumeMeta)
	case ProtocolISCSI:
		return s.deleteISCSIVolume(ctx, volumeMeta)
	case ProtocolSMB:
		return s.deleteSMBVolume(ctx, volumeMeta)
	default:
		return nil, status.Errorf(codes.Internal, "Unknown protocol %s for volume %s", volumeMeta.Protocol, volumeID)
	}
}

// ControllerPublishVolume attaches a volume to a node.
func (s *ControllerService) ControllerPublishVolume(_ context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerPublishVolume called with request: %+v", req)

	// Validate required parameters per CSI spec
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Node ID is required")
	}

	volumeID := req.GetVolumeId()
	nodeID := req.GetNodeId()
	readonly := req.GetReadonly()

	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}

	// Validate node exists in registry
	// Per CSI spec: return NotFound if node doesn't exist
	if s.nodeRegistry != nil && !s.nodeRegistry.IsRegistered(nodeID) {
		return nil, status.Errorf(codes.NotFound, "node %s not found", nodeID)
	}

	// Check if volume is already published to this node with different readonly state
	// Per CSI spec: return AlreadyExists if re-published with incompatible capabilities
	publishKey := fmt.Sprintf("%s:%s", volumeID, nodeID)
	s.publishedVolumesMu.Lock()
	if existingReadonly, exists := s.publishedVolumes[publishKey]; exists {
		if existingReadonly != readonly {
			s.publishedVolumesMu.Unlock()
			klog.V(4).Infof("ControllerPublishVolume: volume %s already published to node %s with readonly=%v, rejecting request with readonly=%v",
				volumeID, nodeID, existingReadonly, readonly)
			return nil, status.Errorf(codes.AlreadyExists,
				"volume %s is already published to node %s with incompatible readonly mode", volumeID, nodeID)
		}
		// Already published with same readonly state - idempotent success
		s.publishedVolumesMu.Unlock()
		klog.V(4).Infof("ControllerPublishVolume: volume %s already published to node %s with same readonly=%v (idempotent)",
			volumeID, nodeID, readonly)
		return &csi.ControllerPublishVolumeResponse{}, nil
	}
	// Track this publish
	s.publishedVolumes[publishKey] = readonly
	s.publishedVolumesMu.Unlock()

	klog.V(4).Infof("ControllerPublishVolume: published volume %s to node %s (readonly=%v)", volumeID, nodeID, readonly)

	// For NFS and NVMe-oF, this is typically a no-op after validation
	return &csi.ControllerPublishVolumeResponse{}, nil
}

// ControllerUnpublishVolume detaches a volume from a node.
func (s *ControllerService) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(4).Infof("ControllerUnpublishVolume called with request: %+v", req)

	// Validate required parameters per CSI spec
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	volumeID := req.GetVolumeId()
	nodeID := req.GetNodeId()

	// Remove from published volumes tracking
	if nodeID != "" {
		publishKey := fmt.Sprintf("%s:%s", volumeID, nodeID)
		s.publishedVolumesMu.Lock()
		delete(s.publishedVolumes, publishKey)
		s.publishedVolumesMu.Unlock()
		klog.V(4).Infof("ControllerUnpublishVolume: unpublished volume %s from node %s", volumeID, nodeID)
	}

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities validates volume capabilities.
func (s *ControllerService) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	klog.V(4).Infof("ValidateVolumeCapabilities called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities are required")
	}

	volumeID := req.GetVolumeId()
	klog.V(4).Infof("ValidateVolumeCapabilities: validating volume %s", volumeID)

	// Look up the volume and determine its protocol
	meta, err := s.lookupVolumeByCSIName(ctx, volumeID)
	if err != nil || meta == nil {
		return nil, status.Errorf(codes.NotFound, "Volume %s not found", volumeID)
	}
	protocol := meta.Protocol

	// Validate capabilities against the volume's protocol
	if protocol != "" {
		if err := validateAccessModeForProtocol(req.GetVolumeCapabilities(), protocol); err != nil {
			// Per CSI spec: return Confirmed: nil with a message (not an error)
			return &csi.ValidateVolumeCapabilitiesResponse{
				Message: fmt.Sprintf("capabilities not confirmed: %v", err),
			}, nil
		}
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// ListVolumes lists all volumes.
func (s *ControllerService) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).Infof("ListVolumes called with request: %+v", req)

	// Single API call: get all CSI-managed datasets with their xattr properties
	entries, err := s.listManagedVolumes(ctx)
	if err != nil {
		klog.Errorf("Failed to list managed volumes: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list managed volumes: %v", err)
	}

	// Handle pagination
	maxEntries := int(req.GetMaxEntries())
	startingToken := req.GetStartingToken()

	// If starting token is provided, skip entries until we reach it
	startIdx := 0
	if startingToken != "" {
		found := false
		for i, entry := range entries {
			if entry.Volume.VolumeId == startingToken {
				startIdx = i + 1
				found = true
				break
			}
		}
		// CSI spec requires returning Aborted error for invalid starting token
		if !found {
			return nil, status.Errorf(codes.Aborted, "invalid starting_token: %s", startingToken)
		}
	}

	// Limit the number of entries if maxEntries is specified
	endIdx := len(entries)
	var nextToken string
	if maxEntries > 0 && startIdx+maxEntries < len(entries) {
		endIdx = startIdx + maxEntries
		// Set next token to the last entry's volume ID
		if endIdx < len(entries) {
			nextToken = entries[endIdx-1].Volume.VolumeId
		}
	}

	// Return the paginated entries
	paginatedEntries := entries[startIdx:endIdx]

	klog.V(4).Infof("Returning %d volumes (total: %d, start: %d, end: %d)",
		len(paginatedEntries), len(entries), startIdx, endIdx)

	return &csi.ListVolumesResponse{
		Entries:   paginatedEntries,
		NextToken: nextToken,
	}, nil
}

// listManagedVolumes lists all CSI-managed volumes using FindManagedSubvolumes.
// Xattr properties store all metadata needed to build ListVolumes entries.
func (s *ControllerService) listManagedVolumes(ctx context.Context) ([]*csi.ListVolumesResponse_Entry, error) {
	klog.V(5).Info("Listing all managed volumes via FindManagedSubvolumes")

	// TODO: filesystem name should come from configuration — for now list from empty filesystem to trigger scan
	// When NASty API supports listing managed subvolumes across all pools, use that.
	// For now, return empty to avoid requiring filesystem configuration here.
	// This can be enhanced once filesystem configuration is available.
	subvols, err := s.apiClient.FindManagedSubvolumes(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to find managed subvolumes: %w", err)
	}

	var entries []*csi.ListVolumesResponse_Entry
	for i := range subvols {
		sv := &subvols[i]

		props := sv.Properties
		if props == nil {
			continue
		}

		volumeID := sv.Filesystem + "/" + sv.Name
		meta, err := extractVolumeMetadataFromSubvolume(volumeID, sv)
		if err != nil {
			klog.Warningf("Skipping subvolume %s/%s: failed to extract metadata: %v", sv.Filesystem, sv.Name, err)
			continue
		}
		if meta == nil {
			continue
		}

		// Get capacity from stored property
		var capacityBytes int64
		if capStr, ok := props[nastyapi.PropertyCapacityBytes]; ok {
			capacityBytes = nastyapi.StringToInt64(capStr)
		}

		entries = append(entries, &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      volumeID,
				CapacityBytes: capacityBytes,
				VolumeContext: buildVolumeContext(*meta),
			},
		})
	}

	klog.V(5).Infof("Found %d managed volumes", len(entries))
	return entries, nil
}

// GetCapacity returns the capacity of the storage filesystem.
func (s *ControllerService) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).Infof("GetCapacity called with request: %+v", req)

	// Extract filesystem name from StorageClass parameters
	params := req.GetParameters()
	if params == nil {
		klog.Warning("GetCapacity called without parameters, cannot determine filesystem")
		return &csi.GetCapacityResponse{}, nil
	}

	fsName := params["filesystem"]
	if fsName == "" {
		klog.Warning("GetCapacity called without filesystem parameter")
		return &csi.GetCapacityResponse{}, nil
	}

	// Query filesystem capacity from NASty
	fs, err := s.apiClient.QueryFilesystem(ctx, fsName)
	if err != nil {
		klog.Errorf("Failed to query filesystem %s: %v", fsName, err)
		return nil, status.Errorf(codes.Internal, "Failed to query filesystem capacity: %v", err)
	}

	// Return available capacity in bytes
	availableCapacity := int64(fs.AvailableBytes) //nolint:gosec // uint64 to int64 safe for realistic filesystem sizes
	klog.V(4).Infof("Pool %s capacity: total=%d bytes, available=%d bytes, used=%d bytes",
		fsName,
		fs.TotalBytes,
		availableCapacity,
		fs.UsedBytes)

	return &csi.GetCapacityResponse{
		AvailableCapacity: availableCapacity,
	}, nil
}

// ========================================
// Volume Adoption Foundation
// ========================================
// These functions provide the foundation for cross-cluster volume adoption.
// A volume is "adoptable" if it has nasty-csi metadata but its NASty resources
// (NFS share or NVMe-oF namespace) no longer exist.

// IsVolumeAdoptable checks if a volume can be adopted based on its xattr properties.
// A volume is adoptable if:
// 1. It has the managed_by property set to nasty-csi
// 2. It has a valid schema version
// 3. It has the required protocol-specific properties
// Returns false if the volume doesn't have proper nasty-csi metadata.
func IsVolumeAdoptable(props map[string]string) bool {
	// Check managed_by property
	managedBy, ok := props[nastyapi.PropertyManagedBy]
	if !ok || managedBy != nastyapi.ManagedByValue {
		return false
	}

	// Check protocol is set
	protocol, ok := props[nastyapi.PropertyProtocol]
	if !ok || protocol == "" {
		return false
	}

	// Verify protocol is a known value
	switch protocol {
	case nastyapi.ProtocolNFS, nastyapi.ProtocolNVMeOF, nastyapi.ProtocolISCSI, nastyapi.ProtocolSMB:
		// Known protocol
	default:
		return false
	}

	return true
}

// GetAdoptionInfo extracts adoption-relevant information from volume xattr properties.
// This is useful for building static PV manifests for adopted volumes.
func GetAdoptionInfo(props map[string]string) map[string]string {
	info := make(map[string]string)

	extract := func(key, infoKey string) {
		if v, ok := props[key]; ok && v != "" {
			info[infoKey] = v
		}
	}

	extract(nastyapi.PropertyCSIVolumeName, "volumeID")
	extract(nastyapi.PropertyProtocol, "protocol")
	extract(nastyapi.PropertyCapacityBytes, "capacityBytes")
	extract(nastyapi.PropertyDeleteStrategy, "deleteStrategy")
	extract(nastyapi.PropertyPVCName, "pvcName")
	extract(nastyapi.PropertyPVCNamespace, "pvcNamespace")
	extract(nastyapi.PropertyStorageClass, "storageClass")

	return info
}

// checkAndAdoptVolume searches for an orphaned volume by CSI name and adopts it if eligible.
// This enables GitOps workflows where clusters are recreated and need to adopt existing volumes.
// Returns (response, true, nil) if adopted successfully, (nil, true, error) if adoption failed,
// or (nil, false, nil) if no adoptable volume found.
func (s *ControllerService) checkAndAdoptVolume(ctx context.Context, req *csi.CreateVolumeRequest, params map[string]string, protocol string) (*csi.CreateVolumeResponse, bool, error) {
	volumeName := req.GetName()
	adoptExisting := params["adoptExisting"] == VolumeContextValueTrue
	filesystem := params["filesystem"]

	klog.V(4).Infof("Checking for adoptable volume: %s (adoptExisting=%v)", volumeName, adoptExisting)

	// Search for subvolume by CSI volume name
	subvol, err := s.apiClient.FindSubvolumeByCSIVolumeName(ctx, filesystem, volumeName)
	if err != nil {
		klog.V(4).Infof("Error searching for orphaned volume %s: %v", volumeName, err)
		return nil, false, nil // Not found or error - continue with normal creation
	}
	if subvol == nil {
		klog.V(4).Infof("No orphaned volume found for %s", volumeName)
		return nil, false, nil // Not found - continue with normal creation
	}

	// Found a subvolume with matching CSI volume name - check if adoption is allowed
	props := subvol.Properties
	if props == nil {
		klog.V(4).Infof("Subvolume %s/%s has no properties, cannot adopt", subvol.Filesystem, subvol.Name)
		return nil, false, nil
	}

	// Verify it's managed by nasty-csi
	if !IsVolumeAdoptable(props) {
		klog.V(4).Infof("Subvolume %s/%s is not adoptable (missing required properties)", subvol.Filesystem, subvol.Name)
		return nil, false, nil
	}

	// Check if adoption is allowed: either volume has adoptable=true OR StorageClass has adoptExisting=true
	volumeAdoptable := props[nastyapi.PropertyAdoptable] == VolumeContextValueTrue
	if !volumeAdoptable && !adoptExisting {
		klog.V(4).Infof("Volume %s found but adoption not allowed (adoptable=%v, adoptExisting=%v)",
			volumeName, volumeAdoptable, adoptExisting)
		return nil, false, nil
	}

	// Verify protocol matches
	volumeProtocol := props[nastyapi.PropertyProtocol]
	if volumeProtocol != protocol {
		klog.Warningf("Cannot adopt volume %s: protocol mismatch (volume=%s, requested=%s)",
			volumeName, volumeProtocol, protocol)
		return nil, true, status.Errorf(codes.FailedPrecondition,
			"Cannot adopt volume %s: protocol mismatch (volume has %s, requested %s)",
			volumeName, volumeProtocol, protocol)
	}

	klog.Infof("Found adoptable volume %s (subvolume=%s/%s, protocol=%s, adoptable=%v, adoptExisting=%v)",
		volumeName, subvol.Filesystem, subvol.Name, volumeProtocol, volumeAdoptable, adoptExisting)

	// Adopt the volume: re-create missing NASty resources based on protocol
	switch protocol {
	case ProtocolNFS:
		resp, err := s.adoptNFSVolume(ctx, req, subvol, params)
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil

	case ProtocolNVMeOF:
		resp, err := s.adoptNVMeOFVolume(ctx, req, subvol, params)
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil

	case ProtocolISCSI:
		resp, err := s.adoptISCSIVolume(ctx, req, subvol, params)
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil

	case ProtocolSMB:
		resp, err := s.adoptSMBVolume(ctx, req, subvol, params)
		if err != nil {
			return nil, true, err
		}
		return resp, true, nil

	default:
		return nil, true, status.Errorf(codes.InvalidArgument,
			"Unsupported protocol for adoption: %s", protocol)
	}
}

// ControllerGetCapabilities returns controller capabilities.
func (s *ControllerService) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	klog.V(4).Info("ControllerGetCapabilities called")

	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_PUBLISH_READONLY,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
					},
				},
			},
			// Note: GET_SNAPSHOT capability is supported but not advertised because
			// csi-test v5.4.0 doesn't recognize it yet. Re-enable when csi-test is updated.
			// {
			// 	Type: &csi.ControllerServiceCapability_Rpc{
			// 		Rpc: &csi.ControllerServiceCapability_RPC{
			// 			Type: csi.ControllerServiceCapability_RPC_GET_SNAPSHOT,
			// 		},
			// 	},
			// },
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_GET_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_VOLUME_CONDITION,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
					},
				},
			},
		},
	}, nil
}

// Snapshot operations are implemented in controller_snapshot.go

// ControllerExpandVolume expands a volume.
func (s *ControllerService) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	klog.V(4).Infof("ControllerExpandVolume called with request: %+v", req)

	// Validate request
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	if req.GetCapacityRange() == nil {
		return nil, status.Error(codes.InvalidArgument, "Capacity range is required")
	}

	volumeID := req.GetVolumeId()
	requiredBytes := req.GetCapacityRange().GetRequiredBytes()

	// Validate minimum volume size (NASty enforces 1 GiB minimum for quota/volsize)
	if requiredBytes > 0 && requiredBytes < MinVolumeSize {
		return nil, status.Errorf(codes.InvalidArgument, errMsgVolumeSizeTooSmall, requiredBytes, MinVolumeSize)
	}

	klog.Infof("ControllerExpandVolume: Expanding volume %s to %d bytes", volumeID, requiredBytes)

	// Look up volume using xattr properties as source of truth
	volumeMeta, err := s.lookupVolumeByCSIName(ctx, volumeID)
	if err != nil {
		klog.Errorf("ControllerExpandVolume: Property-based lookup failed for volume %s: %v", volumeID, err)
		return nil, status.Errorf(codes.Internal, "Failed to lookup volume: %v", err)
	}

	if volumeMeta == nil {
		klog.Errorf("ControllerExpandVolume: Volume %s not found", volumeID)
		return nil, status.Errorf(codes.NotFound, "Volume %s not found for expansion", volumeID)
	}

	klog.V(4).Infof("ControllerExpandVolume: Found volume %s via property lookup: dataset=%s, protocol=%s", volumeID, volumeMeta.DatasetID, volumeMeta.Protocol)
	switch volumeMeta.Protocol {
	case ProtocolNFS:
		klog.Infof("Expanding NFS volume %s with dataset %s to %d bytes", volumeID, volumeMeta.DatasetName, requiredBytes)
		return s.expandNFSVolume(ctx, volumeMeta, requiredBytes)
	case ProtocolNVMeOF:
		klog.Infof("Expanding NVMe-oF volume %s with dataset %s to %d bytes", volumeID, volumeMeta.DatasetName, requiredBytes)
		return s.expandNVMeOFVolume(ctx, volumeMeta, requiredBytes)
	case ProtocolISCSI:
		klog.Infof("Expanding iSCSI volume %s with dataset %s to %d bytes", volumeID, volumeMeta.DatasetName, requiredBytes)
		return s.expandISCSIVolume(ctx, volumeMeta, requiredBytes)
	case ProtocolSMB:
		klog.Infof("Expanding SMB volume %s with dataset %s to %d bytes", volumeID, volumeMeta.DatasetName, requiredBytes)
		return s.expandSMBVolume(ctx, volumeMeta, requiredBytes)
	default:
		return nil, status.Errorf(codes.Internal, "Unknown protocol %s for volume %s", volumeMeta.Protocol, volumeID)
	}
}

// ControllerGetVolume returns volume information including health status.
// This is used by Kubernetes to monitor volume health and report conditions.
// Per CSI spec, this returns VolumeCondition with Abnormal flag and Message.
func (s *ControllerService) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("ControllerGetVolume called with request: %+v", req)

	// Validate request
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	volumeID := req.GetVolumeId()
	klog.V(4).Infof("Getting volume info for: %s", volumeID)

	// Look up volume using xattr properties as source of truth
	volumeMeta, err := s.lookupVolumeByCSIName(ctx, volumeID)
	if err != nil {
		klog.Errorf("ControllerGetVolume: Property-based lookup failed for volume %s: %v", volumeID, err)
		return nil, status.Errorf(codes.Internal, "Failed to lookup volume: %v", err)
	}

	if volumeMeta == nil {
		klog.V(4).Infof("Volume %s not found", volumeID)
		return nil, status.Errorf(codes.NotFound, "Volume %s not found", volumeID)
	}

	switch volumeMeta.Protocol {
	case ProtocolNFS:
		return s.getNFSVolumeInfo(ctx, volumeMeta)
	case ProtocolNVMeOF:
		return s.getNVMeOFVolumeInfo(ctx, volumeMeta)
	case ProtocolISCSI:
		return s.getISCSIVolumeInfo(ctx, volumeMeta)
	case ProtocolSMB:
		return s.getSMBVolumeInfo(ctx, volumeMeta)
	default:
		return nil, status.Errorf(codes.Internal, "Unknown protocol %s for volume %s", volumeMeta.Protocol, volumeID)
	}
}

// getNFSVolumeInfo retrieves volume information and health status for an NFS volume.
//
//nolint:dupl // Each protocol's health check has unique verification logic despite similar structure
func (s *ControllerService) getNFSVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting NFS volume info: %s (subvolume: %s, shareUUID: %s)", meta.Name, meta.DatasetID, meta.NFSShareUUID)

	abnormal := false
	var messages []string
	var capacityBytes int64

	// Check 1: Verify subvolume exists
	filesystem, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr == nil {
		subvol, err := s.apiClient.GetSubvolume(ctx, filesystem, name)
		switch {
		case err != nil && isNotFoundError(err):
			abnormal = true
			messages = append(messages, fmt.Sprintf("Subvolume %s not found", meta.DatasetID))
		case err != nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("Subvolume %s query failed: %v", meta.DatasetID, err))
		default:
			klog.V(4).Infof("Subvolume %s/%s exists", filesystem, name)
			if subvol.Properties != nil {
				if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
					capacityBytes = nastyapi.StringToInt64(capStr)
				}
			}
		}
	}

	// Check 2: Verify NFS share exists and is enabled
	if meta.NFSShareUUID != "" {
		foundShare, err := s.apiClient.GetNFSShare(ctx, meta.NFSShareUUID)
		if err != nil {
			abnormal = true
			messages = append(messages, fmt.Sprintf("Failed to query NFS share %s: %v", meta.NFSShareUUID, err))
		} else {
			switch {
			case foundShare == nil:
				abnormal = true
				messages = append(messages, fmt.Sprintf("NFS share %s not found", meta.NFSShareUUID))
			case !foundShare.Enabled:
				abnormal = true
				messages = append(messages, fmt.Sprintf("NFS share %s is disabled", meta.NFSShareUUID))
			default:
				klog.V(4).Infof("NFS share %s is healthy (enabled: %t, path: %s)", foundShare.ID, foundShare.Enabled, foundShare.Path)
			}
		}
	}

	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	klog.V(4).Infof("NFS volume %s status: abnormal=%t, message=%s", meta.Name, abnormal, message)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacityBytes,
			VolumeContext: buildVolumeContext(*meta),
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}

// getNVMeOFVolumeInfo retrieves volume information and health status for an NVMe-oF volume.
//
//nolint:dupl // Each protocol's health check has unique verification logic despite similar structure
func (s *ControllerService) getNVMeOFVolumeInfo(ctx context.Context, meta *VolumeMetadata) (*csi.ControllerGetVolumeResponse, error) {
	klog.V(4).Infof("Getting NVMe-oF volume info: %s (subvolume: %s, NQN: %s)",
		meta.Name, meta.DatasetID, meta.NVMeOFNQN)

	abnormal := false
	var messages []string
	var capacityBytes int64

	// Check 1: Verify block subvolume exists
	filesystem, name, splitErr := splitSubvolumeID(meta.DatasetID)
	if splitErr == nil {
		subvol, err := s.apiClient.GetSubvolume(ctx, filesystem, name)
		switch {
		case err != nil && isNotFoundError(err):
			abnormal = true
			messages = append(messages, fmt.Sprintf("Block subvolume %s not found", meta.DatasetID))
		case err != nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("Block subvolume %s query failed: %v", meta.DatasetID, err))
		default:
			klog.V(4).Infof("Block subvolume %s/%s exists", filesystem, name)
			if subvol.Properties != nil {
				if capStr, ok := subvol.Properties[nastyapi.PropertyCapacityBytes]; ok {
					capacityBytes = nastyapi.StringToInt64(capStr)
				}
			}
		}
	}

	// Check 2: Verify NVMe-oF subsystem exists by NQN
	if meta.NVMeOFNQN != "" {
		foundSubsystem, err := s.apiClient.GetNVMeOFSubsystemByNQN(ctx, meta.NVMeOFNQN)
		switch {
		case err != nil:
			abnormal = true
			messages = append(messages, fmt.Sprintf("NVMe-oF subsystem not found for NQN %s: %v", meta.NVMeOFNQN, err))
		case foundSubsystem == nil:
			abnormal = true
			messages = append(messages, "NVMe-oF subsystem not found for NQN "+meta.NVMeOFNQN)
		default:
			klog.V(4).Infof("NVMe-oF subsystem %s is healthy (NQN: %s)", foundSubsystem.ID, foundSubsystem.NQN)
		}
	}

	message := msgVolumeIsHealthy
	if abnormal {
		message = strings.Join(messages, "; ")
	}

	klog.V(4).Infof("NVMe-oF volume %s status: abnormal=%t, message=%s", meta.Name, abnormal, message)

	return &csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      meta.Name,
			CapacityBytes: capacityBytes,
			VolumeContext: buildVolumeContext(*meta),
		},
		Status: &csi.ControllerGetVolumeResponse_VolumeStatus{
			VolumeCondition: &csi.VolumeCondition{
				Abnormal: abnormal,
				Message:  message,
			},
		},
	}, nil
}

// ControllerModifyVolume modifies a volume.
func (s *ControllerService) ControllerModifyVolume(_ context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	klog.V(4).Infof("ControllerModifyVolume called with request: %+v", req)

	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, errMsgVolumeIDRequired)
	}

	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume not implemented")
}
