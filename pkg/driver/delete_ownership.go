package driver

import (
	"context"
	"errors"

	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateDeletionOwnership(subvol *nastyapi.Subvolume, meta *VolumeMetadata, protocol string) error {
	if subvol == nil {
		return status.Error(codes.Unavailable, "Ownership query returned no subvolume; refusing deletion")
	}
	props := subvol.Properties
	if props[nastyapi.PropertyManagedBy] != nastyapi.ManagedByValue {
		return status.Errorf(codes.FailedPrecondition, "Subvolume %s is not proven to be managed by nasty-csi; verify ownership and recover metadata explicitly before retrying deletion", meta.DatasetID)
	}
	if name, present := props[nastyapi.PropertyCSIVolumeName]; present && name != meta.DatasetName {
		return status.Errorf(codes.FailedPrecondition, "Subvolume %s volume name mismatch; refusing deletion", meta.DatasetID)
	}
	if stored, present := props[nastyapi.PropertyProtocol]; present && stored != protocol {
		return status.Errorf(codes.FailedPrecondition, "Subvolume %s protocol mismatch; refusing deletion", meta.DatasetID)
	}
	if strategy := props[nastyapi.PropertyDeleteStrategy]; strategy != "" && strategy != nastyapi.DeleteStrategyDelete && strategy != nastyapi.DeleteStrategyRetain {
		return status.Errorf(codes.FailedPrecondition, "Subvolume %s has an unknown delete strategy; refusing deletion", meta.DatasetID)
	}
	return nil
}

// Unlike ordinary discovery, deletion must distinguish an existing unmanaged
// object from absence. Otherwise it silently reports success and leaks storage.
func (s *ControllerService) lookupDeletionVolume(ctx context.Context, volumeID string) (*VolumeMetadata, error) {
	var subvol *nastyapi.Subvolume
	var err error
	if isDatasetPathVolumeID(volumeID) {
		filesystem, name, splitErr := splitSubvolumeID(volumeID)
		if splitErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid volume ID: %v", splitErr)
		}
		subvol, err = s.apiClient.GetSubvolume(ctx, filesystem, name)
	} else {
		subvol, err = s.apiClient.FindSubvolumeByCSIVolumeName(ctx, "", volumeID)
	}
	if err != nil {
		if errors.Is(err, nastyapi.ErrDatasetNotFound) || isNotFoundError(err) {
			return nil, nil //nolint:nilnil // confirmed absence is idempotent
		}
		return nil, status.Errorf(codes.Unavailable, "Cannot verify deletion ownership: %v", err)
	}
	if subvol == nil {
		return nil, nil //nolint:nilnil // a successful lookup with no object is confirmed absence
	}
	meta := &VolumeMetadata{DatasetID: subvol.Filesystem + "/" + subvol.Name, DatasetName: subvol.Name}
	protocol := subvol.Properties[nastyapi.PropertyProtocol]
	if err := validateDeletionOwnership(subvol, meta, protocol); err != nil {
		return nil, err
	}
	switch protocol {
	case ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF:
	default:
		return nil, status.Error(codes.FailedPrecondition, "Missing or unknown volume protocol; recover metadata explicitly before deletion")
	}
	return extractVolumeMetadataFromSubvolume(volumeID, subvol)
}
