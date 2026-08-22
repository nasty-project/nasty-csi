package driver

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	propertyCSIRequestName  = "nasty-csi:csi_request_name"
	propertyVolumeUUID      = "nasty-csi:volume_uuid"
	propertyIdentityVersion = "nasty-csi:identity_version"
	identityVersion2        = "2"
)

type volumeIdentityDecision struct {
	properties map[string]string
	persist    bool
}

func newVolumeUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate volume UUID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func identityProperties(existing map[string]string, requestName, backendName, protocol string) (map[string]string, error) {
	uuid := existing[propertyVolumeUUID]
	if uuid == "" {
		var err error
		uuid, err = newVolumeUUID()
		if err != nil {
			return nil, err
		}
	}
	return map[string]string{
		nastyapi.PropertyCSIVolumeName: backendName,
		nastyapi.PropertyProtocol:      protocol,
		propertyCSIRequestName:         requestName,
		propertyVolumeUUID:             uuid,
		propertyIdentityVersion:        identityVersion2,
	}, nil
}

func mergeProperties(base, extra map[string]string) map[string]string {
	for key, value := range extra {
		base[key] = value
	}
	return base
}

func decideExistingVolumeIdentity(req *csi.CreateVolumeRequest, protocol, backendName string, subvol *nastyapi.Subvolume) (volumeIdentityDecision, error) {
	props := subvol.Properties
	managed := props[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue
	adoptExisting := req.GetParameters()["adoptExisting"] == VolumeContextValueTrue

	if managed && props[propertyIdentityVersion] == identityVersion2 {
		if props[propertyCSIRequestName] != req.GetName() || props[nastyapi.PropertyProtocol] != protocol {
			return volumeIdentityDecision{}, status.Errorf(codes.FailedPrecondition,
				"backend volume %s/%s belongs to CSI request %q using protocol %q",
				subvol.Filesystem, subvol.Name, props[propertyCSIRequestName], props[nastyapi.PropertyProtocol])
		}
		identity, err := identityProperties(props, req.GetName(), backendName, protocol)
		if err != nil {
			return volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to generate volume identity: %v", err)
		}
		return volumeIdentityDecision{
			properties: identity,
			persist:    props[propertyVolumeUUID] == "" || props[nastyapi.PropertyCSIVolumeName] != backendName,
		}, nil
	}

	if managed {
		legacyMatch := subvol.Name == backendName && props[nastyapi.PropertyProtocol] == protocol &&
			legacyPVCIdentityMatches(req.GetName(), req.GetParameters(), props)
		if !legacyMatch && !adoptExisting {
			return volumeIdentityDecision{}, status.Errorf(codes.AlreadyExists,
				"legacy backend volume %s/%s cannot be proven to belong to CSI request %q",
				subvol.Filesystem, subvol.Name, req.GetName())
		}
		identity, err := identityProperties(props, req.GetName(), backendName, protocol)
		if err != nil {
			return volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to generate volume identity: %v", err)
		}
		return volumeIdentityDecision{properties: identity, persist: true}, nil
	}

	if !adoptExisting {
		return volumeIdentityDecision{}, status.Errorf(codes.AlreadyExists,
			"backend volume %s/%s is not owned by nasty-csi; set adoptExisting=true to claim it",
			subvol.Filesystem, subvol.Name)
	}
	identity, err := identityProperties(props, req.GetName(), backendName, protocol)
	if err != nil {
		return volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to generate volume identity: %v", err)
	}
	return volumeIdentityDecision{properties: identity, persist: true}, nil
}

func legacyPVCIdentityMatches(requestName string, params, props map[string]string) bool {
	if props[nastyapi.PropertyCSIVolumeName] == requestName {
		return true
	}
	pvcName := params[CSIPVCName]
	pvcNamespace := params[CSIPVCNamespace]
	return pvcName != "" && pvcNamespace != "" &&
		props[nastyapi.PropertyPVCName] == pvcName && props[nastyapi.PropertyPVCNamespace] == pvcNamespace
}

func (s *ControllerService) selectExistingCreateSubvolume(
	ctx context.Context,
	req *csi.CreateVolumeRequest,
	filesystem, backendName, protocol string,
) (*nastyapi.Subvolume, string, volumeIdentityDecision, error) {

	legacyName, err := ResolveLegacyVolumeName(req.GetParameters(), req.GetName())
	if err != nil {
		return nil, "", volumeIdentityDecision{}, status.Errorf(codes.InvalidArgument, "failed to resolve legacy volume name: %v", err)
	}

	subvol, err := s.apiClient.GetSubvolume(ctx, filesystem, backendName)
	if err != nil && !isNotFoundError(err) {
		return nil, "", volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to query existing subvolume: %v", err)
	}
	selectedName := backendName
	if subvol == nil && legacyName != backendName {
		subvol, err = s.apiClient.GetSubvolume(ctx, filesystem, legacyName)
		if err != nil && !isNotFoundError(err) {
			return nil, "", volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to query legacy subvolume: %v", err)
		}
		if subvol != nil {
			selectedName = legacyName
		}
	}
	if subvol == nil {
		return nil, backendName, volumeIdentityDecision{}, nil
	}
	decision, err := decideExistingVolumeIdentity(req, protocol, selectedName, subvol)
	return subvol, selectedName, decision, err
}

func (s *ControllerService) persistVolumeIdentity(
	ctx context.Context,
	subvol *nastyapi.Subvolume,
	base map[string]string,
	decision volumeIdentityDecision,
) error {

	s.identityMu.Lock()
	defer s.identityMu.Unlock()

	if !decision.persist && base == nil {
		return nil
	}
	if decision.properties == nil {
		return status.Error(codes.Internal, "volume identity properties were not initialized")
	}
	props := mergeProperties(copyProperties(base), decision.properties)
	props[nastyapi.PropertyManagedBy] = nastyapi.ManagedByValue
	if createdAt := subvol.Properties[nastyapi.PropertyCreatedAt]; createdAt != "" {
		props[nastyapi.PropertyCreatedAt] = createdAt
	}
	if propertiesContain(subvol.Properties, props) {
		return nil
	}
	if _, err := s.apiClient.SetSubvolumeProperties(ctx, subvol.Filesystem, subvol.Name, props); err != nil {
		refreshed, readErr := s.apiClient.GetSubvolume(ctx, subvol.Filesystem, subvol.Name)
		if readErr != nil || refreshed == nil {
			return status.Errorf(codes.Aborted,
				"identity write state on %s/%s is unknown; retry without rollback: write error: %v, verification error: %v",
				subvol.Filesystem, subvol.Name, err, readErr)
		}
		subvol.Properties = refreshed.Properties
		if !propertiesContain(refreshed.Properties, props) {
			return status.Errorf(codes.Internal,
				"failed to persist identity properties on %s/%s; verification confirmed the intended state did not commit: %v",
				subvol.Filesystem, subvol.Name, err)
		}
		return status.Errorf(codes.Aborted,
			"identity properties on %s/%s may have committed despite an error; retry the identical request: %v",
			subvol.Filesystem, subvol.Name, err)
	}
	subvol.Properties = props
	return nil
}

func copyProperties(properties map[string]string) map[string]string {
	result := make(map[string]string, len(properties))
	for key, value := range properties {
		result[key] = value
	}
	return result
}

func propertiesContain(properties, expected map[string]string) bool {
	for key, value := range expected {
		if properties[key] != value {
			return false
		}
	}
	return true
}

func newVolumeIdentityDecision(req *csi.CreateVolumeRequest, backendName, protocol string) (volumeIdentityDecision, error) {
	properties, err := identityProperties(nil, req.GetName(), backendName, protocol)
	if err != nil {
		return volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to generate volume identity: %v", err)
	}
	return volumeIdentityDecision{properties: properties, persist: true}, nil
}

func (s *ControllerService) rollbackCreatedSubvolume(ctx context.Context, subvol *nastyapi.Subvolume, operationErr error) error {
	if status.Code(operationErr) == codes.Aborted {
		return operationErr
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	current, readErr := s.apiClient.GetSubvolume(rollbackCtx, subvol.Filesystem, subvol.Name)
	if readErr == nil && current != nil && hasCompleteV2Identity(current.Properties, subvol.Name) {
		return status.Errorf(codes.Aborted,
			"%v; rollback skipped because identity committed concurrently on %s/%s",
			operationErr, subvol.Filesystem, subvol.Name)
	}
	if readErr != nil && !isNotFoundError(readErr) {
		return status.Errorf(codes.Aborted,
			"%v; rollback state for %s/%s is unknown: %v",
			operationErr, subvol.Filesystem, subvol.Name, readErr)
	}
	if isNotFoundError(readErr) || current == nil {
		return operationErr
	}
	if rollbackErr := s.apiClient.DeleteSubvolume(rollbackCtx, subvol.Filesystem, subvol.Name); rollbackErr != nil {
		return status.Errorf(codes.Internal, "%v; additionally failed to roll back newly created subvolume %s/%s: %v",
			operationErr, subvol.Filesystem, subvol.Name, rollbackErr)
	}
	return operationErr
}

func hasCompleteV2Identity(properties map[string]string, backendName string) bool {
	return properties[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue &&
		properties[propertyIdentityVersion] == identityVersion2 &&
		properties[propertyCSIRequestName] != "" && properties[propertyVolumeUUID] != "" &&
		properties[nastyapi.PropertyProtocol] != "" &&
		properties[nastyapi.PropertyCSIVolumeName] == backendName
}

type cloneDestinationSelection struct {
	name    string
	existed bool
	legacy  bool
}

func (s *ControllerService) selectCloneDestination(
	ctx context.Context,
	req *csi.CreateVolumeRequest,
	filesystem, backendName, protocol string,
) (cloneDestinationSelection, error) {

	legacyName, err := ResolveLegacyVolumeName(req.GetParameters(), req.GetName())
	if err != nil {
		return cloneDestinationSelection{}, status.Errorf(codes.InvalidArgument, "failed to resolve legacy clone name: %v", err)
	}
	destination, err := s.apiClient.GetSubvolume(ctx, filesystem, backendName)
	if err != nil && !isNotFoundError(err) {
		return cloneDestinationSelection{}, status.Errorf(codes.Internal, "failed to query clone destination: %v", err)
	}
	if destination != nil {
		return cloneDestinationSelection{name: backendName, existed: true}, nil
	}
	if legacyName == backendName {
		return cloneDestinationSelection{name: backendName}, nil
	}
	destination, err = s.apiClient.GetSubvolume(ctx, filesystem, legacyName)
	if err != nil && !isNotFoundError(err) {
		return cloneDestinationSelection{}, status.Errorf(codes.Internal, "failed to query legacy clone destination: %v", err)
	}
	if destination == nil {
		return cloneDestinationSelection{name: backendName}, nil
	}
	props := destination.Properties
	legacyProof := props[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue &&
		destination.Name == legacyName && props[nastyapi.PropertyProtocol] == protocol &&
		legacyPVCIdentityMatches(req.GetName(), req.GetParameters(), props)
	if !legacyProof {
		return cloneDestinationSelection{}, status.Errorf(codes.AlreadyExists,
			"legacy clone destination %s/%s cannot be proven to belong to CSI request %q",
			destination.Filesystem, destination.Name, req.GetName())
	}
	return cloneDestinationSelection{name: legacyName, existed: true, legacy: true}, nil
}

func classifyCloneIdentity(
	req *csi.CreateVolumeRequest,
	protocol, sourceBackendName, backendName string,
	destination *nastyapi.Subvolume,
	sourceProperties map[string]string,
	sourceExists, backendCreated, destinationExisted, legacy bool,
	freshIdentity volumeIdentityDecision,
) (volumeIdentityDecision, error) {

	if backendCreated || !destinationExisted {
		return freshIdentity, nil
	}
	props := destination.Properties
	matchingDestination := props[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue &&
		props[propertyIdentityVersion] == identityVersion2 &&
		props[propertyCSIRequestName] == req.GetName() && props[nastyapi.PropertyProtocol] == protocol
	if matchingDestination {
		identity, err := identityProperties(props, req.GetName(), backendName, protocol)
		if err != nil {
			return volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to preserve clone identity: %v", err)
		}
		return volumeIdentityDecision{properties: identity, persist: true}, nil
	}

	sourceIdentityCopied := cloneCarriesSourceIdentity(props, sourceProperties)
	if !sourceExists {
		// Retained snapshots carry their deleted parent's identity. The backend
		// has already validated exact snapshot provenance at this point.
		sourceIdentityCopied = hasCompleteV2Identity(props, sourceBackendName) &&
			props[nastyapi.PropertyProtocol] == protocol
	}
	if sourceIdentityCopied && !legacy {
		return freshIdentity, nil
	}
	if legacy {
		legacyProof := props[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue &&
			destination.Name == backendName && props[nastyapi.PropertyProtocol] == protocol &&
			legacyPVCIdentityMatches(req.GetName(), req.GetParameters(), props)
		if !legacyProof {
			return volumeIdentityDecision{}, status.Errorf(codes.AlreadyExists,
				"legacy clone destination %s/%s lost ownership proof after source validation",
				destination.Filesystem, destination.Name)
		}
		existingProperties := props
		if sourceIdentityCopied {
			return freshIdentity, nil
		}
		identity, err := identityProperties(existingProperties, req.GetName(), backendName, protocol)
		if err != nil {
			return volumeIdentityDecision{}, status.Errorf(codes.Internal, "failed to initialize legacy clone identity: %v", err)
		}
		return volumeIdentityDecision{properties: identity, persist: true}, nil
	}

	return volumeIdentityDecision{}, status.Errorf(codes.AlreadyExists,
		"clone destination %s/%s has source provenance but incompatible CSI ownership", destination.Filesystem, destination.Name)
}

func cloneCarriesSourceIdentity(destination, source map[string]string) bool {
	if source[propertyVolumeUUID] != "" && destination[propertyVolumeUUID] == source[propertyVolumeUUID] {
		return true
	}
	if source[propertyCSIRequestName] != "" && destination[propertyCSIRequestName] == source[propertyCSIRequestName] &&
		source[nastyapi.PropertyCSIVolumeName] != "" &&
		destination[nastyapi.PropertyCSIVolumeName] == source[nastyapi.PropertyCSIVolumeName] {

		return true
	}
	if source[propertyIdentityVersion] == identityVersion2 {
		return false
	}
	if destination[propertyIdentityVersion] != "" || destination[propertyCSIRequestName] != "" || destination[propertyVolumeUUID] != "" {
		return false
	}
	for _, key := range []string{
		nastyapi.PropertyManagedBy,
		nastyapi.PropertyCSIVolumeName,
		nastyapi.PropertyProtocol,
		nastyapi.PropertyCreatedAt,
		nastyapi.PropertyCapacityBytes,
		nastyapi.PropertyPVCName,
		nastyapi.PropertyPVCNamespace,
		nastyapi.PropertyStorageClass,
	} {
		if value := source[key]; value != "" && destination[key] != value {
			return false
		}
	}
	return source[nastyapi.PropertyManagedBy] == nastyapi.ManagedByValue &&
		source[nastyapi.PropertyCSIVolumeName] != "" &&
		source[nastyapi.PropertyProtocol] != ""
}
